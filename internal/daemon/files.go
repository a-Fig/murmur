package daemon

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/scrub"
	"github.com/a-Fig/murmur/internal/web"
)

// Files a message carries (#570, docs/design/files.md). The sender's daemon encrypts each and
// pushes them as one blob to a ref of the message's own (bus.FilesRef) before it writes the event. A
// receiving daemon fetches that ref only for a message that is for one of its sessions, saves the
// files under its state directory, and queues the message once they are there, so the paths the
// agent is given work by the time it reads them.

const (
	// fileFetchLimit bounds one try at fetching a message's files; fileTries is how many tries a
	// message gets before it is queued without them.
	fileFetchLimit = time.Minute
	fileTries      = 3
	// scratchPrefix begins the name of a directory git writes a files commit's objects to, or a
	// fetch's, inside the daemon's own (bus.Repo.Scratch). Each is deleted when its work is done.
	scratchPrefix = "files-scratch-"
	// scratchKeep is how old a scratch directory is before a prune takes it for left behind: no
	// send or fetch lasts that long.
	scratchKeep = 10 * time.Minute
)

// fileWait is a message held back from the inboxes of sessions here until its files are fetched.
type fileWait struct {
	Event bus.Event `json:"event"`
	For   []string  `json:"for"` // the sessions it is for
	// Since is when it began to wait: messages are fetched for in the order they came.
	Since time.Time `json:"since"`
}

// filesDir is where the files of message e are saved: inside the git directory, never the working
// tree. It is named by the files ref and by what the event says the files are, their names and
// sums. Not by the message's ID, which is short and anyone's to repeat, and not by the ref alone:
// a teammate could push their own blob over a ref and send a message naming it, and a device that
// read theirs first would show their file under the real message. With the sums in the name, a
// directory holds only files that are what its message says. "" if e's files can't be had.
func (d *Daemon) filesDir(e bus.Event) string {
	if e.FilesAt == "" || len(e.Files) == 0 {
		return ""
	}
	h := sha256.New()
	for _, f := range e.Files {
		fmt.Fprintf(h, "%s\x00%s\x00", f.Name, f.Sum)
	}
	return filepath.Join(d.dir, "files", e.FilesAt+"-"+hex.EncodeToString(h.Sum(nil))[:12])
}

// scratch makes a directory for git to write objects to in place of the clone's, and returns the
// repo that writes there and what deletes the directory.
func (d *Daemon) scratch() (*bus.Repo, func(), error) {
	dir, err := os.MkdirTemp(d.dir, scratchPrefix)
	if err != nil {
		return nil, nil, err
	}
	return d.repo.Scratch(dir), func() {
		if err := os.RemoveAll(dir); err != nil {
			d.log.Printf("a files scratch directory is left, for pruning to remove: %v", err)
		}
	}, nil
}

// textsOf is what the secret check reads of a file's bytes. A file with no zero byte is read as
// text, with what isn't UTF-8 passed over, so one stray byte doesn't hide the rest. UTF-16 with its
// mark, as Windows PowerShell writes a redirected file, is decoded, and read as well as the bytes
// themselves: a mark mustn't hide a key in the clear after it.
//
// A file with a zero byte was once taken for binary and not checked, but keys sit in the clear
// between zero bytes in a .tar, a SQLite file or Linux's /proc/self/environ (#737). Its regions of
// text are read, as `strings` reads a binary: as UTF-8, and as UTF-16 with no mark, either end
// first, from either byte. A region is every character but a zero up to what can't be one, so a
// name stays with its value on the next line or past a control character, and only a whole region
// shorter than minRun is passed over. Reading only the regions keeps a binary quick: random bytes
// break into short ones. Unread: what is compressed, as an image or a zip, which isn't unpacked,
// and UTF-16 with no mark past U+2000 (CJK, say), where every pair of ASCII bytes would also read
// as text.
func textsOf(b []byte) []string {
	var texts []string
	if len(b) >= 2 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		var order binary.ByteOrder = binary.LittleEndian
		if b[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			units = append(units, order.Uint16(b[i:]))
		}
		if last, ok := cutUnit(b[2:], order); ok {
			units = append(units, uint16(last))
		}
		texts = append(texts, string(utf16.Decode(units)))
	}
	if !slices.Contains(b, 0) {
		return append(texts, strings.ToValidUTF8(string(b), " "))
	}
	texts = append(texts, utf8Regions(b))
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for off := range 2 {
			texts = append(texts, utf16Regions(b[off:], order))
		}
	}
	return texts
}

// minRun is the shortest region of text utf8Regions and utf16Regions keep: shorter than any secret
// the check knows, with what names it.
const minRun = 8

// regions gathers regions of text, keeping those of minRun characters or more, a line each.
type regions struct {
	out strings.Builder
	cur []rune
}

func (g *regions) add(r rune) { g.cur = append(g.cur, r) }

func (g *regions) end() {
	if len(g.cur) >= minRun {
		g.out.WriteString(string(g.cur))
		g.out.WriteByte('\n')
	}
	g.cur = g.cur[:0]
}

func (g *regions) String() string {
	g.end()
	return g.out.String()
}

// utf8Regions is b's regions of UTF-8: every character but a zero, up to a byte that isn't UTF-8.
// U+FFFD written out in full is a character like any other.
func utf8Regions(b []byte) string {
	var g regions
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		b = b[n:]
		if r != 0 && (r != utf8.RuneError || n > 1) {
			g.add(r)
		} else {
			g.end()
		}
	}
	return g.String()
}

// utf16Regions is b's regions of UTF-16 in byte order order: every character but a zero below
// U+2000, Latin, Greek and Cyrillic letters among them, and no pair of ASCII bytes, the lowest of
// which, two spaces, is U+2020.
func utf16Regions(b []byte, order binary.ByteOrder) string {
	var g regions
	for i := 0; i+1 < len(b); i += 2 {
		if u := order.Uint16(b[i:]); u != 0 && u < 0x2000 {
			g.add(rune(u))
		} else {
			g.end()
		}
	}
	if last, ok := cutUnit(b, order); ok {
		g.add(last)
	}
	return g.String()
}

// cutUnit is the character an odd byte at the end of UTF-16 b would be, if its other byte, cut
// off, was a zero: an ASCII character's, little end first. A file cut short mustn't lose a key's
// last letter.
func cutUnit(b []byte, order binary.ByteOrder) (rune, bool) {
	if len(b)%2 == 0 || order != binary.LittleEndian {
		return 0, false
	}
	c := b[len(b)-1]
	return rune(c), c != 0 && c < 0x80
}

// sendFile is a file to send with a message: what the sender called it, a path from an agent or a
// name from the page, and what is in it.
type sendFile struct {
	name string
	data []byte
}

// unixPath says whether path is a Unix shell's, as Git Bash's /tmp is, on Windows: it starts at a
// root with a forward slash and names no drive. Two slashes are a share's, and full already.
func unixPath(path string) bool {
	return runtime.GOOS == "windows" && strings.HasPrefix(path, "/")
}

// sendRoots is the folders a send may read files from (#690): the clone's worktrees, the main one
// among them, and the temp folder, each with its links resolved, and the clone's git directory,
// which none of them may read inside. The daemon reads a file as the user, outside any sandbox the
// agent runs in, so an agent that was tricked, or a sandboxed one, could otherwise send anything
// the user can read, ~/.ssh and cloud credentials among it, to the whole team. What an agent makes
// as it works, screenshots, logs and diffs, is in the clone or in temp, and anything else it can
// copy there first. The git directory is out: its config can hold a remote's credentials. So is
// ~/.claude, where a screenshot pasted into Claude Code may be, as it also holds Claude Code's
// credentials and every conversation.
//
// Each folder is there as it is spelled and with its links resolved, as a path may come either way
// (/tmp and /private/tmp on macOS). Git's list of worktrees is in the git directory, which an agent
// can write, so a worktree counts only if its .git leads back to this clone: a line naming ~ there
// doesn't make ~ one (Sol's review of #768).
func (d *Daemon) sendRoots(ctx context.Context) (roots, gitDirs []string) {
	both := func(list *[]string, dir string) {
		*list = append(*list, filepath.Clean(dir))
		if real, err := filepath.EvalSymlinks(dir); err == nil && real != filepath.Clean(dir) {
			*list = append(*list, real)
		}
	}
	both(&gitDirs, d.opt.GitDir)
	if out, err := (gitx.Git{Dir: d.opt.GitDir}).Run(ctx, "", "worktree", "list", "--porcelain", "-z"); err == nil {
		// -z ends each line with a NUL, as a path can hold a newline, and a record with an empty one.
		var dir string
		var stale bool
		for _, line := range append(strings.Split(out, "\x00"), "") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				dir, stale = filepath.FromSlash(strings.TrimPrefix(line, "worktree ")), false
			case line == "bare" || line == "prunable" || strings.HasPrefix(line, "prunable "):
				stale = true
			case line == "":
				if dir != "" && !stale && d.ownWorktree(dir) {
					both(&roots, dir)
				}
				dir = ""
			}
		}
	} else {
		d.log.Printf("listing the clone's worktrees, for where a send may read files: %v", err)
	}
	if d.opt.TempDir != "" {
		both(&roots, d.opt.TempDir)
	} else {
		both(&roots, os.TempDir())
		if runtime.GOOS != "windows" {
			both(&roots, "/tmp")
		}
	}
	return roots, gitDirs
}

// ownWorktree says dir's .git is this clone's git directory, or a file that points into its
// worktrees folder: what git makes for the main worktree and for a linked one.
func (d *Daemon) ownWorktree(dir string) bool {
	if netPath(dir) {
		return false
	}
	git := filepath.Join(dir, ".git")
	info, err := os.Lstat(git)
	if err != nil {
		return false
	}
	target := git
	if info.Mode().IsRegular() {
		b, err := os.ReadFile(git)
		ptr, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if err != nil || !ok || netPath(ptr) {
			return false
		}
		if !filepath.IsAbs(ptr) {
			ptr = filepath.Join(dir, ptr)
		}
		target = ptr
	} else if !info.IsDir() {
		return false
	}
	same := func(a, b string) bool {
		ia, err1 := os.Stat(a)
		ib, err2 := os.Stat(b)
		return err1 == nil && err2 == nil && os.SameFile(ia, ib)
	}
	return same(target, d.opt.GitDir) || same(filepath.Dir(target), filepath.Join(d.opt.GitDir, "worktrees"))
}

// netPath says path, on Windows, names a share or a device rather than a drive: \\host\share,
// \/host/share, \\?\ and \\.\ among them. Looking at a share signs in to its host as the user.
func netPath(path string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	vol := filepath.VolumeName(path)
	return vol != "" && !(len(vol) == 2 && vol[1] == ':')
}

var errOutside = errors.New("outside where send reads")

// resolveSend follows path's links one part at a time, each looked at through an os.Root on the
// root it is in, and says which root the file it leads to is in and where. A part outside every
// root is never looked at, nor is a link to a share, so neither a link nor a race reaches a network
// host or a file elsewhere.
func resolveSend(path string, roots []string, open func(string) (*os.Root, error)) (root, rel string, err error) {
	cur := filepath.Clean(path)
	for hops := 0; hops < 40; hops++ {
		i := slices.IndexFunc(roots, func(r string) bool { return inDir(cur, r) })
		if i < 0 {
			return "", "", errOutside
		}
		root = roots[i]
		rel = relIn(root, cur)
		r, err := open(root)
		if err != nil {
			return "", "", err
		}
		parts := strings.Split(rel, string(filepath.Separator))
		done, linked := ".", false
		for j, part := range parts {
			if part == "." {
				continue
			}
			next := filepath.Join(done, part)
			info, err := r.Lstat(next)
			if err != nil {
				return "", "", err
			}
			if info.Mode()&os.ModeSymlink == 0 && !isReparseLink(info) {
				done = next
				continue
			}
			to, err := r.Readlink(next)
			if err != nil {
				return "", "", err
			}
			if netPath(to) {
				return "", "", errOutside
			}
			if !filepath.IsAbs(to) {
				to = filepath.Join(root, done, to)
			}
			cur, linked = filepath.Join(append([]string{to}, parts[j+1:]...)...), true
			break
		}
		if !linked {
			return root, done, nil
		}
	}
	return "", "", errors.New("too many links")
}

// isReparseLink says info is a Windows junction, which os.Root follows as it does a link, and which
// Go reports as irregular rather than as a link.
func isReparseLink(info os.FileInfo) bool {
	return runtime.GOOS == "windows" && info.Mode()&os.ModeIrregular != 0
}

// relIn is path from root, which inDir says it is in.
func relIn(root, path string) string {
	if runtime.GOOS == "windows" {
		root, path = strings.ToLower(root), strings.ToLower(path)
	}
	rel, _ := filepath.Rel(root, path)
	return rel
}

// inDir says path is root or inside it. Windows' paths don't tell case apart.
func inDir(path, root string) bool {
	if runtime.GOOS == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// sendTemp is the folder a refusal tells an agent to copy a file into.
func (d *Daemon) sendTemp() string {
	return cmp.Or(d.opt.TempDir, os.TempDir())
}

// readSendFiles reads the files an agent's send names, each no larger than a file may be, and only
// from where sendRoots allows. A path is followed through its links first, and the file read is
// the one it leads to, so a link in temp to ~/.ssh is refused.
func (d *Daemon) readSendFiles(ctx context.Context, paths []string) ([]sendFile, error) {
	if len(paths) > bus.MaxFiles {
		return nil, fmt.Errorf("a message carries at most %d files, and this has %d", bus.MaxFiles, len(paths))
	}
	files := make([]sendFile, 0, len(paths))
	for _, path := range paths {
		if netPath(path) {
			// A share, or a device path: opening it, or looking at it, would sign in to whatever
			// host it names, as the user. Two slashes are a share's on Windows, never Git Bash's.
			return nil, fmt.Errorf("%s is a network path, which send doesn't read: copy the file into %s and send it from there", path, d.sendTemp())
		}
		if !filepath.IsAbs(path) {
			if unixPath(path) {
				// Git Bash's /tmp, say: Windows can't open it, and would read it on the current
				// drive, where another file of that name may be (#706).
				return nil, fmt.Errorf("%s isn't a full path on Windows: pass the Windows path, as `cygpath -w '%s'` prints it", path, path)
			}
			return nil, fmt.Errorf("%s isn't a full path", path)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	roots, gitDirs := d.sendRoots(ctx)
	opened := map[string]*os.Root{}
	defer func() {
		for _, r := range opened {
			r.Close()
		}
	}()
	open := func(dir string) (*os.Root, error) {
		if r := opened[dir]; r != nil {
			return r, nil
		}
		r, err := os.OpenRoot(dir)
		if err == nil {
			opened[dir] = r
		}
		return r, err
	}
	outside := func(path string) error {
		return fmt.Errorf("%s isn't in this repo's clone or the temp folder, the only places send reads files from: copy it into %s and send it from there", path, d.sendTemp())
	}
	for _, path := range paths {
		root, rel, err := resolveSend(path, roots, open)
		switch {
		case errors.Is(err, errOutside):
			return nil, outside(path)
		case err != nil:
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		case slices.ContainsFunc(gitDirs, func(g string) bool { return inDir(filepath.Join(root, rel), g) }):
			return nil, outside(path)
		}
		// Opened through the root, the file can't be one outside it even if a part of its path
		// became a link since the look: os.Root refuses a link that leads out. A link swapped in
		// to the git directory, inside the main worktree, is left; an agent that can make one there
		// can read that directory itself.
		r, _ := open(root)
		if info, err := r.Lstat(rel); err != nil {
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		} else if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s isn't a file", path) // and opening a pipe would wait for a writer
		}
		f, err := r.Open(rel)
		if err != nil {
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		}
		info, err := f.Stat()
		switch {
		case err != nil:
			f.Close()
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		case !info.Mode().IsRegular():
			f.Close()
			return nil, fmt.Errorf("%s isn't a file", path)
		case info.Size() > bus.MaxFile:
			f.Close()
			return nil, fmt.Errorf("%s is %s, and a file can be at most %d MB", path, proto.Size(info.Size()), bus.MaxFile>>20)
		}
		b, err := io.ReadAll(io.LimitReader(f, bus.MaxFile+1)) // it may have grown since the look
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		}
		files = append(files, sendFile{path, b})
	}
	return files, nil
}

// checkSendFiles holds the files of a send to the limits, and checks each for secrets as a body is
// (textsOf).
func checkSendFiles(files []sendFile) error {
	if len(files) > bus.MaxFiles {
		return fmt.Errorf("a message carries at most %d files, and this has %d", bus.MaxFiles, len(files))
	}
	var total int64
	for _, f := range files {
		if len(f.data) > bus.MaxFile {
			return fmt.Errorf("%s is %s, and a file can be at most %d MB", f.name, proto.Size(int64(len(f.data))), bus.MaxFile>>20)
		}
		if total += int64(len(f.data)); total > bus.MaxFilesTotal {
			return fmt.Errorf("a message's files can come to at most %d MB together", bus.MaxFilesTotal>>20)
		}
		if err := scrub.Check(textsOf(f.data)...); err != nil {
			return fmt.Errorf("%s: %v", f.name, err)
		}
	}
	return nil
}

// putFiles encrypts the files of a send and pushes them to a new files ref, and returns what the
// event says of them and the ref's name. It keeps a copy of each where a receiver's would be, so a
// session on this device, and the page, have them without a fetch.
func (d *Daemon) putFiles(ctx context.Context, in []sendFile) (files []bus.File, at string, err error) {
	if err := checkSendFiles(in); err != nil {
		return nil, "", err
	}
	at = bus.NewFilesAt()
	sealed := make([][]byte, len(in))
	files = make([]bus.File, len(in))
	for i, sf := range in {
		blob, f, err := bus.SealFile(sf.data, at, i)
		if err != nil {
			return nil, "", err
		}
		f.Name = sf.name
		sealed[i], files[i] = blob, f
	}
	files = bus.CleanFiles(files) // the names a receiver will save them under
	pack, err := bus.PackFiles(sealed)
	if err != nil {
		return nil, "", err
	}
	repo, done, err := d.scratch()
	if err != nil {
		return nil, "", err
	}
	defer done()
	sha, err := repo.NewFilesCommit(ctx, pack)
	if err != nil {
		return nil, "", err
	}
	d.mu.Lock()
	d.st.FileRefs[at] = time.Now().Round(0)
	d.saveLocked()
	d.mu.Unlock()
	if err := d.pushFrom(ctx, repo, sha, bus.FilesRef(at), bus.FastForward, ""); err != nil {
		// Left to the next prune to delete: a push can land and still be told it failed.
		d.dropFileRef(at, true)
		return nil, "", fmt.Errorf("its files didn't go out: %w", err)
	}
	dir := d.filesDir(bus.Event{FilesAt: at, Files: files})
	for i, f := range files {
		if err := saveFile(dir, f.Name, in[i].data); err != nil {
			d.log.Printf("keeping a copy of %s, sent on files ref %s: %v", f.Name, at, err)
		}
	}
	return files, at, nil
}

// dropFileRef forgets a files ref this device pushed, or with due set leaves it to the next prune
// to delete from the remote: its message never went out.
func (d *Daemon) dropFileRef(at string, due bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if due {
		d.st.FileRefs[at] = time.Now().Add(-d.opt.FileKeep).Round(0)
	} else {
		delete(d.st.FileRefs, at)
	}
	d.saveLocked()
}

// saveFile writes one file of a message in dir, its message's directory, where sessions are told it
// is. It is written beside its place and renamed, so a path that exists holds the whole file.
func saveFile(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".part-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// filesHere says every file of e that can be read is saved here.
func (d *Daemon) filesHere(e bus.Event) bool {
	for _, f := range e.Files {
		if f.Readable() && (e.FilesAt == "" || !fileExists(filepath.Join(d.filesDir(e), f.Name))) {
			return false
		}
	}
	return true
}

// holdForFilesLocked says message e, for session id, waits for its files: they aren't here yet, so
// the fetch is started and the message is queued when it ends, with or without them. Nothing is
// held for a person: the page asks for a file when it shows it (fileOf). Caller holds mu.
func (d *Daemon) holdForFilesLocked(id string, e bus.Event) bool {
	if len(e.Files) == 0 || id == bus.PersonSession || d.filesHere(e) {
		return false
	}
	w := d.st.FilesWait[e.ID]
	if w == nil {
		w = &fileWait{Event: e, Since: time.Now().Round(0)}
		d.st.FilesWait[e.ID] = w
	}
	if !slices.Contains(w.For, id) {
		w.For = append(w.For, id)
	}
	d.kickFiles()
	return true
}

func (d *Daemon) kickFiles() {
	select {
	case d.fileKick <- struct{}{}:
	default:
	}
}

// nextFileWaitLocked is the message that has waited longest for its files. Caller holds mu.
func (d *Daemon) nextFileWaitLocked() (bus.Event, bool) {
	ids := sortedKeys(d.st.FilesWait)
	if len(ids) == 0 {
		return bus.Event{}, false
	}
	first := slices.MinFunc(ids, func(a, b string) int {
		return cmp.Or(d.st.FilesWait[a].Since.Compare(d.st.FilesWait[b].Since), cmp.Compare(a, b))
	})
	return d.st.FilesWait[first].Event, true
}

// fileLoop fetches the files of the messages that wait for them, one message at a time in the
// order they came, and deletes what is past FileKeep. Messages go first: a prune is a push for each
// ref past its time, and a session shouldn't wait for those.
func (d *Daemon) fileLoop(ctx context.Context) {
	prune := time.NewTicker(d.opt.FilePrune)
	defer prune.Stop()
	pruned := false
	for {
		for ctx.Err() == nil {
			d.mu.Lock()
			e, ok := d.nextFileWaitLocked()
			d.mu.Unlock()
			if !ok {
				break
			}
			err := d.fetchFiles(ctx, e, fileTries)
			if ctx.Err() != nil {
				return // still waiting when the daemon next starts
			}
			if err != nil {
				d.log.Printf("message %s is queued without its files: %v", e.ID, err)
			}
			d.mu.Lock()
			d.filesFetchedLocked(e.ID)
			d.mu.Unlock()
		}
		if !pruned {
			pruned = true
			d.pruneFiles(ctx)
			continue // a message may have come meanwhile
		}
		select {
		case <-ctx.Done():
			return
		case <-d.fileKick:
		case <-prune.C:
			d.pruneFiles(ctx)
		}
	}
}

// errFetchLater is a fetch of files that failed in a way another try might not: the remote
// couldn't be reached, or the time ran out.
var errFetchLater = errors.New("the files couldn't be fetched just now")

// filesFetchedLocked queues the message with event ID id, whose files' fetch has ended, for the
// sessions it waited for. Caller holds mu.
func (d *Daemon) filesFetchedLocked(id string) {
	w := d.st.FilesWait[id]
	if w == nil {
		return
	}
	delete(d.st.FilesWait, id)
	for _, sess := range w.For {
		if to := d.filesRecipientLocked(sess, w.Event); to != "" {
			d.queueInboxLocked(to, w.Event)
		}
	}
	d.saveLocked()
}

// filesRecipientLocked is where message e, which waited for its files for session id, goes now:
// id, or the session that has taken id's name since (forwardLocked). A fetch can take minutes, and
// a session that took the name meanwhile, as one that resumed the conversation in another process,
// took what id had in its inbox then (adoptLocked), not what waited here: the message would land in
// an inbox nobody reads (#703). A session that took id's T3 thread rather than its name is a
// recipient already (takeFilesWaitLocked).
//
// "" if that session sent e, or has gone off the mesh meanwhile (a T3 thread settled, or found to be
// a delegate), as a message that came for it then would be refused (addressedLocked, queueLocked).
// "" too if a session the message was forwarded to has muted its sender. id's own mute was asked
// when the message came, and stands as it was then: a message let through in a mute's grace (#466)
// isn't dropped because the fetch outlasted the grace (Sol's review). Caller holds mu.
func (d *Daemon) filesRecipientLocked(id string, e bus.Event) string {
	to := id
	if f := d.forwardLocked(bus.Addr(d.device, id)); f != "" {
		_, to = bus.SplitAddr(f)
	}
	if e.From == bus.Addr(d.device, to) || d.offMeshLocked(to) || to != id && d.mutedHereLocked(to, e) {
		return ""
	}
	return to
}

// takeFilesWaitLocked makes session to a recipient, in from's place, of each message that waits
// for its files for from: to has taken from's T3 thread (takeInboxLocked), and with it what from
// had unread, which a message still waiting for its files is too. A route taken over this way
// carries no name from one to the other, so forwardLocked wouldn't find to when the fetch ends
// (#703). The caller saves the state. Caller holds mu.
func (d *Daemon) takeFilesWaitLocked(to, from string) {
	for _, w := range d.st.FilesWait {
		i := slices.Index(w.For, from)
		if i < 0 {
			continue
		}
		w.For = slices.Delete(w.For, i, i+1)
		if !slices.Contains(w.For, to) {
			w.For = append(w.For, to)
		}
	}
}

// fetchFiles brings the files of message e from its files ref and saves them. A fetch that fails
// is tried again, up to tries times, as the network may be why; a ref that isn't there, or holds
// something else, is not. What the fetch brings goes to a scratch directory, not the clone's
// objects.
func (d *Daemon) fetchFiles(ctx context.Context, e bus.Event, tries int) error {
	release, err := d.holdFiles(ctx)
	if err != nil {
		return err
	}
	defer release()
	return d.fetchFilesHeld(ctx, e, tries)
}

// holdFiles waits for the one turn there is to fetch files, and returns what gives it back. One
// at a time: the page asks for each file of a message as it shows it, and the loop may be fetching
// for the same message. Whoever comes second finds the files here. A caller whose time runs out
// while it waits gives up.
func (d *Daemon) holdFiles(ctx context.Context) (release func(), err error) {
	select {
	case d.fileOne <- struct{}{}:
		return func() { <-d.fileOne }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", errFetchLater, ctx.Err())
	}
}

// fetchFilesHeld is fetchFiles for a caller that holds the turn (holdFiles). What stops it is
// errFetchLater unless it would stop every try: the ref isn't there, or what it holds isn't the
// message's files.
func (d *Daemon) fetchFilesHeld(ctx context.Context, e bus.Event, tries int) error {
	if d.filesHere(e) {
		return nil
	}
	if e.FilesAt == "" {
		return bus.ErrNoFile
	}
	repo, done, err := d.scratch()
	if err != nil {
		return fmt.Errorf("%w: %w", errFetchLater, err)
	}
	defer done()
	ref := bus.FilesRef(e.FilesAt)
	var sha string
	for try := 1; ; try++ {
		fctx, cancel := context.WithTimeout(ctx, fileFetchLimit)
		sha, err = repo.FetchFiles(fctx, ref, bus.MaxPack)
		cancel()
		if err == nil || errors.Is(err, bus.ErrNoFile) || errors.Is(err, bus.ErrTooLarge) || try >= tries || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", errFetchLater, ctx.Err())
		case <-time.After(time.Duration(try) * 5 * time.Second):
		}
	}
	if errors.Is(err, bus.ErrNoFile) || errors.Is(err, bus.ErrTooLarge) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errFetchLater, err)
	}
	pack, err := repo.ReadPack(ctx, sha, bus.MaxPack)
	if errors.Is(err, bus.ErrNoFile) || errors.Is(err, bus.ErrTooLarge) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: %w", errFetchLater, err)
	}
	var failed []string
	later := false // a file couldn't be saved, which a full disk does: the remote still has it
	for i, f := range e.Files {
		if !f.Readable() {
			continue
		}
		blob, err := bus.PackedFile(pack, e.Files, i)
		var plain []byte
		if err == nil {
			plain, err = bus.OpenFile(blob, f, e.FilesAt, i)
		}
		if err == nil {
			if err = saveFile(d.filesDir(e), f.Name, plain); err != nil {
				later = true
			}
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", f.Name, err))
		}
	}
	switch {
	case len(failed) == 0:
		return nil
	case later:
		return fmt.Errorf("%w: %s", errFetchLater, strings.Join(failed, "; "))
	}
	return errors.New(strings.Join(failed, "; "))
}

// pruneFiles deletes the copies of files kept longer than FileKeep, and from the remote the files
// refs this device pushed that long ago, and those of devices that are gone (pruneOthersFiles).
func (d *Daemon) pruneFiles(ctx context.Context) {
	// Scratch directories nothing deleted: a daemon stopped mid-work left them, or Windows wouldn't
	// let one go while a scanner still held a file in it. Only old ones: a send can be using one
	// made a moment ago, before this loop started.
	left, _ := filepath.Glob(filepath.Join(d.dir, scratchPrefix+"*"))
	for _, dir := range left {
		if info, err := os.Stat(dir); err == nil && time.Since(info.ModTime()) > scratchKeep {
			os.RemoveAll(dir)
		}
	}
	root := filepath.Join(d.dir, "files")
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > d.opt.FileKeep {
			if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
				d.log.Printf("deleting the files saved from files ref %s: %v", entry.Name(), err)
			}
		}
	}
	d.mu.Lock()
	var due []string
	for at, sent := range d.st.FileRefs {
		if time.Since(sent) > d.opt.FileKeep {
			due = append(due, at)
		}
	}
	d.mu.Unlock()
	slices.Sort(due)
	for _, at := range due {
		// Deleting a ref that is already gone succeeds: the look after a refused push finds it at
		// "", which is what was asked for.
		if err := d.push(ctx, "", bus.FilesRef(at), bus.Force, ""); err != nil {
			d.log.Printf("deleting files ref %s from the remote: %v", at, err)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		d.dropFileRef(at, false)
	}
	d.pruneOthersFiles(ctx)
}

// othersFile is a files ref another device pushed, as read of in its log: whose, and how long this
// daemon has run since it first read of it (ageOthersFilesLocked).
type othersFile struct {
	Device string        `json:"device"`
	Age    time.Duration `json:"age"`
}

// noteOthersFilesLocked notes the files ref message e names, read in dev's log, if dev is another
// device. It is saved with the log's cursor. Caller holds mu.
func (d *Daemon) noteOthersFilesLocked(e bus.Event, dev string) {
	if e.Type != bus.Message || e.FilesAt == "" || dev == d.device {
		return
	}
	if _, ok := d.st.OthersFiles[e.FilesAt]; !ok {
		d.st.OthersFiles[e.FilesAt] = othersFile{Device: dev}
	}
}

// ageOthersFilesLocked adds to each note's age the time since the last call, as now, a reading of
// the monotonic clock, says, and at most twice FilePrune. No wall clock goes into an age: one set
// forward by weeks, as a clock is when it is put right, would make every note old at once. The
// cap holds a machine asleep, which Windows' monotonic clock counts, and the first call adds nothing,
// so the time a daemon was down isn't counted. That only makes a ref wait longer. Caller holds mu.
func (d *Daemon) ageOthersFilesLocked(now time.Time) {
	if !d.othersAged.IsZero() {
		step := min(max(now.Sub(d.othersAged), 0), 2*d.opt.FilePrune)
		for at, f := range d.st.OthersFiles {
			f.Age += step
			d.st.OthersFiles[at] = f
		}
	}
	d.othersAged = now
}

// pruneOthersFiles deletes from the remote the files refs of devices that are gone, which only their
// own daemon would otherwise delete: a deleted clone's would stay for good (#708). A ref goes once
// this daemon has run for twice FileKeep since it first read of it. Its event was written after it
// was pushed, so its own daemon, by then, would have deleted it a FileKeep before, and no receiver
// can still need it. That counts no wall clock, this one's or the sender's, so it holds when either
// is wrong, and for an event a teammate wrote to name another's ref. The device being gone, its
// presence stale or off the remote, only spares a live one's own prune the race. Every daemon here
// may delete the same ref: deleting one already gone succeeds.
func (d *Daemon) pruneOthersFiles(ctx context.Context) {
	d.mu.Lock()
	d.ageOthersFilesLocked(time.Now())
	var due []string
	for at, f := range d.st.OthersFiles {
		if f.Age > 2*d.opt.FileKeep {
			due = append(due, at)
		}
	}
	if len(d.st.OthersFiles) > 0 {
		d.saveLocked()
	}
	d.mu.Unlock()
	if len(due) == 0 {
		return
	}
	listed, err := d.repo.ListRemote(ctx)
	if err != nil {
		d.log.Printf("listing the remote's files refs: %v", err)
		return
	}
	forget := func(at string) {
		d.mu.Lock()
		delete(d.st.OthersFiles, at)
		d.saveLocked()
		d.mu.Unlock()
	}
	slices.Sort(due)
	for _, at := range due {
		if listed[bus.FilesRef(at)] == "" {
			forget(at) // its own daemon, or another here, deleted it
			continue
		}
		d.mu.Lock()
		p := d.peers[d.st.OthersFiles[at].Device]
		live := d.lastPollOK.IsZero() || p != nil && !d.staleLocked(p)
		d.mu.Unlock()
		if live {
			continue
		}
		if err := d.push(ctx, "", bus.FilesRef(at), bus.Force, ""); err != nil {
			d.log.Printf("deleting files ref %s, of a device that is gone, from the remote: %v", at, err)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		forget(at)
	}
}

// noFilesNoteLocked tells the sender of message e, which carries files, which of its recipients
// are on a device whose daemon is from before files: they get the text alone, with no sign that
// there was more. who is what the sender called the one it sent to, if it sent to one. Caller
// holds mu.
func (d *Daemon) noFilesNoteLocked(e bus.Event, who string) string {
	to, direct := d.recipientsOfLocked(e)
	var old []string
	for _, addr := range to {
		dev, _ := bus.SplitAddr(addr)
		if p := d.peers[dev]; dev != d.device && p != nil && !p.locked && !p.pres.Files {
			name, _, _ := d.sessionInfoLocked(addr)
			if direct {
				name = who
			}
			old = append(old, nameOr(name, addr))
		}
	}
	switch len(old) {
	case 0:
		return ""
	case 1:
		return old[0] + " is on a device that runs a " + brand.Product + " from before files, so the text alone gets there."
	}
	return strings.Join(old[:len(old)-1], ", ") + " and " + old[len(old)-1] + " are on devices that run a " +
		brand.Product + " from before files, so the text alone gets there."
}

// fileMiss is a fetch for the page that failed: when, and how.
type fileMiss struct {
	at  time.Time
	err error
}

// pageFetchAgain is how long the page's asks for a message's files go unanswered by a new fetch
// after one failed. A message's pictures are asked for together, and each would otherwise wait out
// a fetch that the one before it just saw fail.
const pageFetchAgain = 20 * time.Second

// pageFetch fetches the files of message e for the page. Once: the page asks again by itself, and
// a person is looking at it meanwhile. Not at all if a fetch for the message failed a moment ago,
// which it says again.
func (d *Daemon) pageFetch(ctx context.Context, e bus.Event) error {
	release, err := d.holdFiles(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Looked at with the turn held: the fetch that was waited for may be the one that failed.
	d.mu.Lock()
	miss, missed := d.fileMiss[e.FilesAt]
	d.mu.Unlock()
	if missed && time.Since(miss.at) < pageFetchAgain {
		return miss.err
	}
	err = d.fetchFilesHeld(ctx, e, 1)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case err == nil:
		delete(d.fileMiss, e.FilesAt)
	case ctx.Err() != nil:
		// The page went, or its time ran out: that says nothing of the remote.
	default:
		d.log.Printf("fetching the files of message %s for the page: %v", e.ID, err)
		d.fileMiss[e.FilesAt] = fileMiss{time.Now(), err}
	}
	for at, m := range d.fileMiss { // few, and small, but not kept for good
		if time.Since(m.at) > pageFetchAgain {
			delete(d.fileMiss, at)
		}
	}
	return err
}

// noFile is why the page can't have a file at all, in words to show (web.ErrNoFile).
type noFile string

func (e noFile) Error() string        { return string(e) }
func (e noFile) Is(target error) bool { return target == web.ErrNoFile }

// fileOf returns the index'th file of the message with that ID, for the page: its name and what is
// in it, fetched first if it isn't here.
func (d *Daemon) fileOf(ctx context.Context, id string, index int) (string, []byte, error) {
	d.mu.Lock()
	i := slices.IndexFunc(d.msgs, func(m pageMsg) bool { return m.e.ID == id })
	var e bus.Event
	if i >= 0 {
		e = d.msgs[i].e
	}
	d.mu.Unlock()
	if i < 0 || index >= len(e.Files) {
		return "", nil, noFile("no such file")
	}
	f := e.Files[index]
	gone := noFile(f.Name + " isn't here: it couldn't be fetched, or was sent more than a week ago")
	if !f.Readable() || e.FilesAt == "" {
		return "", nil, gone
	}
	path := filepath.Join(d.filesDir(e), f.Name)
	if !fileExists(path) {
		if err := d.pageFetch(ctx, e); errors.Is(err, errFetchLater) && !fileExists(path) {
			return "", nil, errors.New(f.Name + " couldn't be fetched just now")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, gone
	}
	return f.Name, data, nil
}

// fileViewsLocked is the files of message e as a session here is told of them: each with the path
// it is saved at, or none if it isn't here (it couldn't be fetched, or is past FileKeep). Caller
// holds mu.
func (d *Daemon) fileViewsLocked(e bus.Event) []proto.FileView {
	if len(e.Files) == 0 {
		return nil
	}
	views := make([]proto.FileView, 0, len(e.Files))
	for _, f := range e.Files {
		v := proto.FileView{Name: f.Name, Size: f.Size}
		if f.Readable() && e.FilesAt != "" {
			if path := filepath.Join(d.filesDir(e), f.Name); fileExists(path) {
				v.Path = path
			}
		}
		views = append(views, v)
	}
	return views
}
