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
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/scrub"
	"github.com/a-Fig/murmur/internal/t3"
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
// among them, and the temp folder; and the clone's git directory, which none of them may read
// inside. The daemon reads a file as the user, outside any sandbox the agent runs in, so an agent
// that was tricked, or a sandboxed one, could otherwise send anything the user can read, ~/.ssh
// and cloud credentials among it, to the whole team. What an agent makes as it works, screenshots,
// logs and diffs, is in the clone or in temp, and anything else it can copy there first. The git
// directory is out: its config can hold a remote's credentials. So is ~/.claude, where a screenshot
// pasted into Claude Code may be, as it also holds Claude Code's credentials and every
// conversation.
//
// Two rounds of Sol's review of #768 shaped how. Each folder is opened as an os.Root, and every
// later look and read goes through that handle, so the folder checked is the one read from, though
// its path be swapped for a link after. A worktree is found above the file sent, as the nearest
// folder with a .git that leads back to this clone, looked at through its handle (#772), and that
// its registration or a session vouches for (ownWorktree). `git worktree list` isn't run: it looks
// at each one, and would sign in to a share one names. The git directory is known by what it is,
// not how it is spelled, so neither a case nor an alias gets around it.
type sendRoots struct {
	roots []sendRoot
	git   os.FileInfo // the clone's git directory; nil if it couldn't be looked at
	// worktrees is the git directory's worktrees folder, which a linked worktree's .git points
	// into; nil if there is none.
	worktrees os.FileInfo
	// notOurs is the folders with a .git that were looked at and aren't this clone's worktrees, so
	// a send's second file doesn't look again.
	notOurs []os.FileInfo
	// gitRoot is the git directory's handle, which a linked worktree's registration is read
	// through; nil if it couldn't be opened.
	gitRoot *os.Root
	// sessions is where this device's sessions are: a main worktree kept apart from its git
	// directory counts only if one is in it.
	sessions []string
	vols     map[string]*os.Root
}

type sendRoot struct {
	path string // with its links resolved
	r    *os.Root
	id   os.FileInfo // what it is, looked at through r
}

// fileSender is the session's identity and route when the daemon accepted its send request.
type fileSender struct {
	agent string
	route proto.Route
}

func (d *Daemon) openSendRoots(sender fileSender) *sendRoots {
	s := &sendRoots{vols: map[string]*os.Root{}}
	gitDir, err := s.local(d.opt.GitDir)
	var git sendRoot
	ok := false
	if err == nil {
		git, ok = s.open(gitDir)
	}
	if ok {
		s.gitRoot = git.r
		s.git = git.id
		if info, err := git.r.Lstat("worktrees"); err == nil && info.IsDir() {
			s.worktrees = info
		}
	} else {
		d.log.Printf("looking at the git directory %s, for where a send may read files: it can't be opened", d.opt.GitDir)
	}
	d.mu.Lock()
	for _, ss := range d.sessions {
		if filepath.IsAbs(ss.cwd) && !netPath(ss.cwd) {
			s.sessions = append(s.sessions, filepath.Clean(ss.cwd))
		}
	}
	d.mu.Unlock()
	temps := []string{d.opt.TempDir}
	if d.opt.TempDir == "" {
		temps = []string{os.TempDir()}
		if runtime.GOOS != "windows" {
			temps = append(temps, "/tmp")
		}
	}
	for _, dir := range temps {
		if root, ok := s.open(dir); ok && !s.add(root) {
			root.r.Close()
		}
	}
	if route := sender.route; sender.agent == "antigravity in T3" && (route.T3CLI != "" || route.T3Thread != "") {
		for _, home := range t3.Homes(route.T3CLI, route.T3Home) {
			if route.T3Home != "" && home != filepath.Clean(route.T3Home) {
				break // an explicit T3 home belongs to this session
			}
			root, matched, ok := s.openAntigravityTemp(home)
			if ok && !s.add(root) {
				root.r.Close()
			}
			if matched {
				break
			}
		}
	}
	return s
}

// openAntigravityTemp opens only T3's temp subtree. The home in the route may be an alias,
// so pin it first, then open each child as the directory looked at, with no link in its place.
// matched reports whether home holds a T3 userdata database, so the caller fails closed on that
// home instead of falling through to another T3 installation if antigravity-tmp is absent or linked.
func (s *sendRoots) openAntigravityTemp(home string) (sendRoot, bool, bool) {
	root, ok := s.open(home)
	if !ok {
		return sendRoot{}, false, false
	}
	matched := false
	for _, part := range []string{"userdata", "antigravity-tmp"} {
		if part == "antigravity-tmp" {
			// A CLI on PATH may live outside T3's home. Check for its database in userdata.
			if info, err := root.r.Lstat(filepath.Base(t3.StateDB(home))); err != nil || !info.Mode().IsRegular() {
				root.r.Close()
				return sendRoot{}, false, false
			}
			matched = true
		}
		info, err := root.r.Lstat(part)
		if err != nil || !info.IsDir() || linkIn(info, func() error { _, err := root.r.Readlink(part); return err }) {
			root.r.Close()
			return sendRoot{}, matched, false
		}
		next, err := root.r.OpenRoot(part)
		root.r.Close()
		if err != nil {
			return sendRoot{}, matched, false
		}
		id, err := next.Stat(".")
		if err != nil || !os.SameFile(id, info) {
			next.Close()
			return sendRoot{}, matched, false
		}
		root = sendRoot{filepath.Join(root.path, part), next, id}
	}
	return root, true, true
}

// add adds root, unless it is one already, or is the git directory or inside it: a registration an
// agent wrote can name the git directory as a worktree.
func (s *sendRoots) add(root sendRoot) bool {
	if slices.ContainsFunc(s.roots, func(r sendRoot) bool { return os.SameFile(r.id, root.id) }) {
		return false
	}
	if s.git != nil && os.SameFile(root.id, s.git) {
		return false // its handle, though its path was swapped since it was looked at
	}
	for dir := root.path; ; dir = filepath.Dir(dir) {
		if info, err := s.stat(dir); err == nil && os.SameFile(info, s.git) {
			return false
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	s.roots = append(s.roots, root)
	return true
}

func (s *sendRoots) Close() {
	for _, r := range s.roots {
		r.r.Close()
	}
	for _, r := range s.vols {
		r.Close()
	}
	if s.gitRoot != nil {
		s.gitRoot.Close()
	}
}

// open opens dir as a root, through its volume's handle, so a part of it swapped for a link to a
// share is refused rather than followed.
func (s *sendRoots) open(dir string) (sendRoot, bool) {
	path, err := s.local(dir)
	if err != nil {
		return sendRoot{}, false
	}
	var r *os.Root
	if runtime.GOOS == "windows" {
		vol, rel, err := s.volume(path)
		if err != nil {
			return sendRoot{}, false
		}
		r, err = vol.OpenRoot(rel)
	} else {
		// No share to sign in to off Windows, and os.Root, from /, would need every folder above
		// readable, where a lookup needs only search: a /home of mode 711 would refuse every send.
		r, err = os.OpenRoot(path)
	}
	if err != nil {
		return sendRoot{}, false
	}
	id, err := r.Stat(".")
	if err != nil {
		r.Close()
		return sendRoot{}, false
	}
	return sendRoot{path, r, id}, true
}

// worktreeAt is dir, whose folder is info, as a root, if it has a .git and is one of this clone's
// worktrees (ownWorktree); it is added to s's roots then.
func (s *sendRoots) worktreeAt(dir string, info os.FileInfo) (sendRoot, bool) {
	if s.git == nil || slices.ContainsFunc(s.notOurs, func(n os.FileInfo) bool { return os.SameFile(n, info) }) {
		return sendRoot{}, false
	}
	if _, err := s.stat(filepath.Join(dir, ".git")); err != nil {
		return sendRoot{}, false
	}
	root, ok := s.open(dir)
	switch {
	case !ok:
	case s.ownWorktree(root, s.worktrees) && s.add(root):
		return root, true
	default:
		root.r.Close()
	}
	s.notOurs = append(s.notOurs, info)
	return sendRoot{}, false
}

// ownWorktree says root's .git, looked at through its handle, is this clone's git directory, or a
// file that points into its worktrees folder: what git makes for the main worktree and a linked one.
// A .git file is anyone's to write, so it alone doesn't make a folder a worktree (Sol's review of
// #805): a linked one's registration in the git directory must name it back, and a main worktree
// kept apart from its git directory must hold one of this device's sessions. Before #772 the same
// two were how worktrees were found.
func (s *sendRoots) ownWorktree(root sendRoot, worktrees os.FileInfo) bool {
	info, err := root.r.Lstat(".git")
	switch {
	case err != nil || s.git == nil:
		return false
	case info.IsDir():
		return os.SameFile(info, s.git)
	case !info.Mode().IsRegular():
		return false
	}
	b, err := root.r.ReadFile(".git")
	ptr, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if err != nil || !ok || netPath(ptr) {
		return false
	}
	if !filepath.IsAbs(ptr) {
		ptr = filepath.Join(root.path, ptr)
	}
	ptr, err = s.local(ptr)
	if err != nil {
		return false
	}
	if target, err := s.stat(ptr); err == nil && os.SameFile(target, s.git) {
		// A main worktree whose git directory is elsewhere.
		return slices.ContainsFunc(s.sessions, func(cwd string) bool { return s.holds(root, cwd) })
	}
	parent, err := s.stat(filepath.Dir(ptr))
	if err != nil || worktrees == nil || !os.SameFile(parent, worktrees) || s.gitRoot == nil {
		return false
	}
	// Its registration, read through the git directory's handle, names this .git back. Its path
	// can be relative to it (git worktree add --relative-paths).
	reg := filepath.Join("worktrees", filepath.Base(ptr))
	b, err = s.gitRoot.ReadFile(filepath.Join(reg, "gitdir"))
	back := strings.TrimSpace(string(b))
	if err != nil || back == "" || netPath(back) {
		return false
	}
	if !filepath.IsAbs(back) {
		back = filepath.Join(ptr, back)
	}
	back, err = s.local(back)
	if err != nil {
		return false
	}
	// Both the file and its folder: a hard link to a worktree's .git, elsewhere, is the same file
	// (Sol's second review of #805).
	named, err := s.stat(back)
	if err != nil || !os.SameFile(named, info) {
		return false
	}
	dir, err := s.stat(filepath.Dir(back))
	return err == nil && os.SameFile(dir, root.id)
}

// holds says the folder cwd is root or inside it, by what each folder above it is, so a short
// name (C:\MYREPO~1) or a link doesn't miss.
func (s *sendRoots) holds(root sendRoot, cwd string) bool {
	dir, err := s.local(cwd)
	if err != nil {
		return false
	}
	for ; ; dir = filepath.Dir(dir) {
		if info, err := s.stat(dir); err == nil && os.SameFile(info, root.id) {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

// volume is the root of path's volume, opened once, and path from there.
func (s *sendRoots) volume(path string) (*os.Root, string, error) {
	vol := filepath.VolumeName(path) + string(filepath.Separator)
	r := s.vols[vol]
	if r == nil {
		var err error
		if r, err = os.OpenRoot(vol); err != nil {
			return nil, "", err
		}
		s.vols[vol] = r
	}
	rel := strings.TrimPrefix(path[len(vol)-1:], string(filepath.Separator))
	return r, cmp.Or(rel, "."), nil
}

// stat looks at path, which has no links, through its volume's handle.
func (s *sendRoots) stat(path string) (os.FileInfo, error) {
	if runtime.GOOS != "windows" {
		return os.Lstat(path) // see open
	}
	vol, rel, err := s.volume(path)
	if err != nil {
		return nil, err
	}
	return vol.Lstat(rel)
}

// readlink is where the link at path leads, read as stat looks.
func (s *sendRoots) readlink(path string) (string, error) {
	if runtime.GOOS != "windows" {
		return os.Readlink(path)
	}
	vol, rel, err := s.volume(path)
	if err != nil {
		return "", err
	}
	return vol.Readlink(rel)
}

// local is path with its links resolved, followed one part at a time through its volume's handle:
// a link to a share, or one swapped in partway, is refused (errOutside) rather than followed, so
// looking never signs in to a network host.
func (s *sendRoots) local(path string) (string, error) {
	path = filepath.Clean(path)
	if netPath(path) || !filepath.IsAbs(path) {
		return "", errOutside
	}
	vol := filepath.VolumeName(path)
	done := vol + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path[len(vol):], string(filepath.Separator)), string(filepath.Separator))
	for hops := 0; len(parts) > 0; {
		part := parts[0]
		parts = parts[1:]
		if part == "" || part == "." {
			continue
		}
		next := filepath.Join(done, part)
		if part == ".." {
			done = filepath.Dir(done)
			continue
		}
		info, err := s.stat(next)
		if err != nil {
			return "", err
		}
		if !linkIn(info, func() error { _, err := s.readlink(next); return err }) {
			done = next
			continue
		}
		if hops++; hops > 40 {
			return "", errors.New("too many links")
		}
		to, err := s.readlink(next)
		if err != nil {
			return "", err
		}
		if netPath(to) {
			return "", errOutside
		}
		if !filepath.IsAbs(to) {
			to = filepath.Join(done, to) // done has no links, so .. in to means its parent
		}
		vol = filepath.VolumeName(to)
		done = vol + string(filepath.Separator)
		parts = append(strings.Split(strings.TrimPrefix(to[len(vol):], string(filepath.Separator)), string(filepath.Separator)), parts...)
	}
	return done, nil
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

// openSend opens the file path leads to, if it is in a root and not in the git directory. Every
// look is through the root's handle, and a link met there, which can only be one swapped in since
// local looked, is refused.
func (s *sendRoots) openSend(path string) (*os.File, error) {
	real, err := s.local(path)
	if err != nil {
		return nil, err
	}
	// The root it is in is found by what each folder above it is, not by how its path is spelled,
	// which a short Windows name (RUNNER~1) or a case can change. A folder above it with a .git is
	// looked at as it is met, the nearest first, and is a root if it is one of this clone's
	// worktrees (#772): only the worktree a file is in is opened, not every one the clone has, which
	// took 1 to 4 s a send on a clone with 220 (Fable's review of #768). One that isn't, as a
	// nested repo, is passed over, so a file in it is still in the worktree around it.
	var root sendRoot
	var rest []string
	dir := real
	for ; ; dir = filepath.Dir(dir) {
		if info, err := s.stat(dir); err == nil {
			if i := slices.IndexFunc(s.roots, func(r sendRoot) bool { return os.SameFile(info, r.id) }); i >= 0 {
				root = s.roots[i]
				break
			}
			if r, ok := s.worktreeAt(dir, info); ok {
				root = r
				break
			}
		}
		if filepath.Dir(dir) == dir {
			return nil, errOutside
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
	}
	// Nor is a root inside the git directory, however it got there since it was added (#773).
	for up := dir; filepath.Dir(up) != up; {
		up = filepath.Dir(up)
		if info, err := s.stat(up); err == nil && s.git != nil && os.SameFile(info, s.git) {
			return nil, errOutside
		}
	}
	if len(rest) == 0 {
		return nil, errNotFile
	}
	// Down from the root a part at a time, each folder opened as a handle of its own before the next
	// (#773): a look from the root at a longer path would go through the folders above again, and
	// os.Root follows a link in them that stays inside the root, as one to the main worktree's .git
	// does, so a folder swapped for one between two looks would lead there unchecked. How each part
	// is opened is the system's (descend).
	return s.descend(root, rest)
}

// sendRace, if set, runs in descend just before a part is opened: tests swap the part there, as a
// racing agent would.
var sendRace func(part string)

// linkIn says info, an entry of a folder, is a link local follows, readlink reading where it
// leads: a symbolic link, or on Windows a junction. Go reports any other reparse point as irregular
// too, a OneDrive placeholder, a ProjFS file or an app's execution alias among them, but those have
// nowhere to lead, so readlink fails on them, and they are the file or folder they stand for (#773).
func linkIn(info os.FileInfo, readlink func() error) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	return runtime.GOOS == "windows" && info.Mode()&os.ModeIrregular != 0 && readlink() == nil
}

// plainFile says info is a file to read: a regular one, or on Windows one held by a reparse point
// that leads nowhere (linkIn), as a OneDrive file not yet downloaded is.
func plainFile(info os.FileInfo) bool {
	m := info.Mode()
	return m.IsRegular() || runtime.GOOS == "windows" && m&^os.ModeIrregular&os.ModeType == 0 && !info.IsDir()
}

var errNotFile = errors.New("not a file")

// sendTemp is the folder a refusal tells an agent to copy a file into.
func (d *Daemon) sendTemp() string {
	return cmp.Or(d.opt.TempDir, os.TempDir())
}

// readSendFiles reads the files an agent's send names, each no larger than a file may be, and only
// from where sendRoots allows. A path is followed through its links first, and the file read is
// the one it leads to, so a link in temp to ~/.ssh is refused (openSend).
func (d *Daemon) readSendFiles(sender fileSender, paths []string) ([]sendFile, error) {
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
	s := d.openSendRoots(sender)
	defer s.Close()
	var total int64
	for _, path := range paths {
		f, err := s.openSend(path)
		switch {
		case errors.Is(err, errOutside):
			return nil, fmt.Errorf("%s isn't in this repo's clone or the temp folders send reads from: copy it into %s and send it from there", path, d.sendTemp())
		case errors.Is(err, errNotFile):
			return nil, fmt.Errorf("%s isn't a file", path)
		case err != nil:
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		}
		info, err := f.Stat()
		switch {
		case err != nil:
			f.Close()
			return nil, fmt.Errorf("can't read %s: %v", path, err)
		case info.Size() > bus.MaxFile:
			f.Close()
			return nil, fmt.Errorf("%s is %s, and a file can be at most %d MB", path, proto.Size(info.Size()), bus.MaxFile>>20)
		}
		// Before the file is read: files that come to too much together are refused for that, and
		// the rest of them needn't be read to say so.
		if total += info.Size(); total > bus.MaxFilesTotal {
			f.Close()
			return nil, errFilesTotal()
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

// errFilesTotal is the refusal of files that come to more together than a message carries.
func errFilesTotal() error {
	return fmt.Errorf("a message's files can come to at most %d MB together", bus.MaxFilesTotal>>20)
}

// checkSendFiles holds the files of a send to the limits, and checks each for secrets as a body is
// (textsOf).
//
// The limits come first, for every file, since they cost nothing, and the check for secrets reads
// every byte: files 30 MB together were checked 20 MB deep before they were refused for their size,
// which took a busy machine past a send's 30 s in the tests (#1075).
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
			return errFilesTotal()
		}
	}
	for _, f := range files {
		if err := scrub.Check(textsOf(f.data)...); err != nil {
			return fmt.Errorf("%s: %v", f.name, err)
		}
	}
	return nil
}

// fileUpload is a push of files this device made: when, how many bytes went (#739), and to which
// files ref, by which a push that left nothing on the remote is taken off the count (#788).
type fileUpload struct {
	At    time.Time `json:"at"`
	Bytes int64     `json:"bytes"`
	Ref   string    `json:"ref,omitempty"`
}

// recentUpload is files this device pushed: the ref, what the event said of them, when, and
// whether a message naming the ref has gone out.
type recentUpload struct {
	at    string
	files []bus.File
	when  time.Time
	sent  bool
}

// uploadReuse is how long after a push a send of the same files takes the same ref.
const uploadReuse = 10 * time.Minute

// filesSum is a sum over the names and contents of the files of a send, in order.
func filesSum(in []sendFile) [sha256.Size]byte {
	h := sha256.New()
	for _, f := range in {
		fmt.Fprintf(h, "%d:%s:%d:", len(f.name), f.name, len(f.data))
		h.Write(f.data)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return sum
}

// reuseFilesLocked is the ref this device pushed the same files to within uploadReuse, and what
// its event said of them. A send to several names is a message for each, and once uploaded the
// files once for each (#739). A file is sealed to its ref and place, not its message, so every
// message can name the one ref. Only a ref a message has gone out with is taken, and only while
// the prune hasn't it due: one whose message failed is left for the prune to delete. Caller holds
// mu.
func (d *Daemon) reuseFilesLocked(sum [sha256.Size]byte) ([]bus.File, string, bool) {
	for k, u := range d.uploads {
		if time.Since(u.when) > uploadReuse {
			delete(d.uploads, k)
		}
	}
	u := d.uploads[sum]
	if u == nil || !u.sent {
		return nil, "", false
	}
	if pushed, ok := d.st.FileRefs[u.at]; !ok || time.Since(pushed) > d.opt.FileKeep-uploadReuse {
		return nil, "", false
	}
	return slices.Clone(u.files), u.at, true
}

// filesSentLocked notes that a message naming files ref at went out, so a send of the same files
// may take it. Caller holds mu.
func (d *Daemon) filesSentLocked(at string) {
	for _, u := range d.uploads {
		if u.at == at {
			u.sent = true
		}
	}
}

// filesUnsent lets go of files ref at, pushed for a message that then failed: nothing names it, so
// it is off the remote at the next prune, and no other send takes it.
func (d *Daemon) filesUnsent(at string) {
	d.mu.Lock()
	for k, u := range d.uploads {
		if u.at == at {
			delete(d.uploads, k)
		}
	}
	d.mu.Unlock()
	d.dropFileRef(at, true)
}

// uploadRoomLocked says whether this device may push size more bytes of files, by FilesPerDay over
// the last 24 hours, and if it may, counts them as pushed; if not, it says why, with when it may.
// The look and the count are one step, so two sends at once can't both take the last of the room.
// ref is the files ref they go to. Caller holds mu.
func (d *Daemon) uploadRoomLocked(size int64, ref string) error {
	now := time.Now()
	d.st.Uploads = slices.DeleteFunc(d.st.Uploads, func(u fileUpload) bool { return now.Sub(u.At) >= 24*time.Hour })
	var used int64
	for _, u := range d.st.Uploads {
		used += u.Bytes
	}
	over := used + size - d.opt.FilesPerDay
	if over <= 0 {
		// Counted before the push, which may land though it is said to fail. One known not to
		// have is taken off again (refundUploadLocked).
		d.st.Uploads = append(d.st.Uploads, fileUpload{At: now.Round(0), Bytes: size, Ref: ref})
		return nil
	}
	when := now
	for _, u := range d.st.Uploads { // oldest first: they are kept in the order made
		if over -= u.Bytes; over <= 0 {
			when = u.At.Add(24 * time.Hour)
			break
		}
	}
	// By the calendar, not 24 hours: across a spring clock change, 24 hours from 23:30 is 00:30 two
	// days on.
	at := when.Local().Format("15:04")
	sameDay := func(a, b time.Time) bool { return a.Format(time.DateOnly) == b.Format(time.DateOnly) }
	switch {
	case sameDay(when.Local(), now.Local()):
	case sameDay(when.Local(), now.Local().AddDate(0, 0, 1)):
		at += " tomorrow"
	default:
		at += " on " + when.Local().Format("Mon Jan 2")
	}
	return fmt.Errorf("this device has sent %s of files in the last day, and sends at most %s a day; it can send these after %s",
		proto.Size(used), proto.Size(d.opt.FilesPerDay), at)
}

// refundUploadLocked takes what was pushed to files ref at off the day's count, as the remote is
// known not to have it, and says whether it was counted. Without it an agent that tried a 15 MB
// recording a dozen times over a bad connection had uploaded nothing and could send no files for
// a day (#788). Caller holds mu.
func (d *Daemon) refundUploadLocked(at string) bool {
	n := len(d.st.Uploads)
	d.st.Uploads = slices.DeleteFunc(d.st.Uploads, func(u fileUpload) bool { return u.Ref == at })
	return len(d.st.Uploads) < n
}

// uploadLost says whether the push of files ref at, which ended with err, is known to have left
// nothing on the remote. One never sent did: for want of consent, by an invisible device, or
// because its time ran out before git was started for it (notPushedError). One
// cut off at its time, or by the daemon stopping, may land yet, as what its git started can outlive
// it: the prune looks, once that has had its time (pruneFiles). For any other the remote is asked,
// one it refused included: "remote failed to report status" reads as a refusal, and is what a
// push that landed says when the connection broke after.
func (d *Daemon) uploadLost(ctx context.Context, at string, err error) bool {
	switch {
	case errors.Is(err, ErrNoConsent), errors.Is(err, ErrInvisible), errors.As(err, new(notPushedError)):
		return true
	case ctx.Err() != nil, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return false
	}
	return d.filesRefGone(ctx, at)
}

// uploadSettle is how long after an upload began the prune leaves its ref be, if the push failed:
// the push's own time, and five minutes for what it started to end.
const uploadSettle = pushTimeout + 5*time.Minute

// filesRefGone says whether the remote, asked now, has no files ref at. A remote that can't be
// asked says nothing.
func (d *Daemon) filesRefGone(ctx context.Context, at string) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tip, err := d.repo.RemoteRef(ctx, bus.FilesRef(at))
	return err == nil && tip == ""
}

// sameFilesBusy is what a send ends with when its time ran out waiting for another send of the
// same files (putFiles, #763). It reads as what an agent can act on, and unwraps to the context's
// error for whoever asks why.
type sameFilesBusy struct{ err error }

func (e sameFilesBusy) Error() string {
	return "another send of the same files was still under way; send it again"
}

func (e sameFilesBusy) Unwrap() error { return e.err }

// putFiles encrypts the files of a send and pushes them to a new files ref, and returns what the
// event says of them and the ref's name. The same files sent again within uploadReuse, as a send
// to several names does, take the ref already pushed, and reused says so. It keeps a copy of each
// where a receiver's would be, so a session on this device, and the page, have them without a
// fetch. The caller tells filesSentLocked once the message is out, and filesUnsent if it fails
// with a ref not reused, unless its push may land yet (unsureError).
//
// Only one upload of the same files is under way at a time (#763). A ref is taken by another send
// once its message is out, so two sessions here that sent the same files at the same moment both
// found none to take: each uploaded, each was counted in the day's uploads, and the later's
// note of its ref replaced the earlier's. A second send now waits for the first's message to go
// out or fail, and then takes its ref or uploads itself. end ends this send's turn: the caller
// calls it once the message is out or has failed, after filesSentLocked, so that whoever waited
// finds the ref, or after filesUnsent. It is called here if putFiles doesn't return a ref, a
// panic included (the page's sends run where one is recovered), and does nothing for a ref reused.
//
// still is asked once the push's turn is had: Send files may have gone off while the send waited.
func (d *Daemon) putFiles(ctx context.Context, in []sendFile, still func() bool) (files []bus.File, at string, reused bool, end func(), err error) {
	end = func() {}
	if err := checkSendFiles(in); err != nil {
		return nil, "", false, end, err
	}
	sum := filesSum(in)
	turn := make(chan struct{})
	for {
		d.mu.Lock()
		files, at, ok := d.reuseFilesLocked(sum)
		if ok {
			d.mu.Unlock()
			if still != nil && !still() {
				return nil, "", false, end, errSendOff
			}
			return files, at, true, end, nil
		}
		ahead := d.sending[sum]
		if ahead == nil {
			if d.sending == nil {
				d.sending = map[[sha256.Size]byte]chan struct{}{}
			}
			d.sending[sum] = turn
			d.mu.Unlock()
			break
		}
		d.mu.Unlock()
		select {
		case <-ahead:
		case <-ctx.Done():
			return nil, "", false, end, sameFilesBusy{ctx.Err()}
		}
	}
	end = sync.OnceFunc(func() {
		d.mu.Lock()
		delete(d.sending, sum)
		d.mu.Unlock()
		close(turn)
	})
	pushed := false
	defer func() {
		if !pushed {
			end()
		}
	}()
	at = bus.NewFilesAt()
	sealed := make([][]byte, len(in))
	files = make([]bus.File, len(in))
	for i, sf := range in {
		blob, f, err := bus.SealFile(sf.data, at, i)
		if err != nil {
			return nil, "", false, end, err
		}
		f.Name = sf.name
		sealed[i], files[i] = blob, f
	}
	files = bus.CleanFiles(files) // the names a receiver will save them under
	pack, err := bus.PackFiles(sealed)
	if err != nil {
		return nil, "", false, end, err
	}
	repo, done, err := d.scratch()
	if err != nil {
		return nil, "", false, end, err
	}
	defer done()
	sha, err := repo.NewFilesCommit(ctx, pack)
	if err != nil {
		return nil, "", false, end, err
	}
	d.mu.Lock()
	if err := d.uploadRoomLocked(int64(len(pack)), at); err != nil {
		d.mu.Unlock()
		return nil, "", false, end, err
	}
	d.st.FileRefs[at] = time.Now().Round(0)
	d.saveLocked()
	d.mu.Unlock()
	if err := d.pushFrom(ctx, repo, sha, bus.FilesRef(at), bus.FastForward, "", still); err != nil {
		// Left to the next prune to delete: a push can land and still be told it failed. It
		// stays counted too, unless it is known not to have landed.
		if d.uploadLost(ctx, at, err) {
			d.mu.Lock()
			d.refundUploadLocked(at)
			d.mu.Unlock()
		}
		d.dropFileRef(at, true) // which saves
		return nil, "", false, end, fmt.Errorf("its files didn't go out: %w", err)
	}
	// It went up, and stays counted whatever becomes of the ref: a prune that found it gone, its
	// message having failed, would take for never landed what an earlier delete took away.
	d.mu.Lock()
	for i := range d.st.Uploads {
		if d.st.Uploads[i].Ref == at {
			d.st.Uploads[i].Ref = ""
		}
	}
	d.mu.Unlock()
	dir := d.filesDir(bus.Event{FilesAt: at, Files: files})
	unpin := d.pinFilesDir(dir)
	defer unpin()
	for i, f := range files {
		if err := saveFile(dir, f.Name, in[i].data); err != nil {
			d.log.Printf("keeping a copy of %s, sent on files ref %s: %v", f.Name, at, err)
		}
	}
	d.mu.Lock()
	if d.uploads == nil {
		d.uploads = map[[sha256.Size]byte]*recentUpload{}
	}
	d.uploads[sum] = &recentUpload{at: at, files: slices.Clone(files), when: time.Now()}
	d.mu.Unlock()
	d.holdFilesHeld() // its own copies, pinned, stay
	pushed = true
	return files, at, false, end, nil
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

// refuses says sw, a Receive files switch, refuses message e's files: it is from another device,
// and sw is off (#765, Tyler, 2026-11-16). Its text arrives, and its files are neither fetched nor
// shown: to this device's sessions if sw is agentRecv, on the page if pageRecv. What a session of
// this device sends is here already, and no download.
func (d *Daemon) refuses(sw *fileSwitch, e bus.Event) bool {
	dev, _ := bus.SplitAddr(e.From)
	return len(e.Files) > 0 && dev != d.device && !sw.on.Load()
}

// errReceiveOff is why a message waiting for its files went without them: Receive files was turned
// off meanwhile.
var errReceiveOff = errors.New("Receive files is off")

// errSendOff is why a send's files didn't go out: Send files was turned off while it waited.
var errSendOff = errors.New("sending files is off in this repo")

// fileSwitch is a Receive files switch, whose going off ends the fetches made for it.
type fileSwitch struct {
	on atomic.Bool
	// off is closed when the switch goes off, and made anew when it goes on. mu guards it, and
	// orders the changes to on.
	mu  sync.Mutex
	off chan struct{}
}

// receiving is ctx, ended as soon as sw goes off, so a fetch under way, or waiting to retry, stops
// then rather than minutes later (Sol's review of #769).
func (sw *fileSwitch) receiving(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	sw.mu.Lock()
	off, on := sw.off, sw.on.Load()
	sw.mu.Unlock()
	if !on {
		cancel()
		return ctx, cancel
	}
	go func() {
		select {
		case <-off:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// set turns sw on or off, and says whether it changed. Off ends every fetch that receiving started.
func (sw *fileSwitch) set(on bool) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.on.Swap(on) == on {
		return false
	}
	if on {
		sw.off = make(chan struct{})
	} else {
		close(sw.off)
	}
	return true
}

// holdForFilesLocked says message e, for session id, waits for its files: they aren't here yet, so
// the fetch is started and the message is queued when it ends, with or without them. Nothing is
// held for a person: the page asks for a file when it shows it (fileOf). Caller holds mu.
func (d *Daemon) holdForFilesLocked(id string, e bus.Event) bool {
	if len(e.Files) == 0 || id == bus.PersonSession || d.refuses(&d.agentRecv, e) || d.filesHere(e) {
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
			err := errReceiveOff
			if !d.refuses(&d.agentRecv, e) {
				err = d.fetchFiles(ctx, e, fileTries, &d.agentRecv)
			}
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
		// The first time round, and when the device is out of invisible with a prune owed.
		if !pruned || d.pruneOwed.Load() && !d.hidden.Load() {
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
	if f := d.forwardNamedLocked(bus.Addr(d.device, id), e.ToName); f != "" {
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
func (d *Daemon) fetchFiles(ctx context.Context, e bus.Event, tries int, sw *fileSwitch) error {
	release, err := d.holdFiles(ctx)
	if err != nil {
		return err
	}
	defer release()
	return d.fetchFilesHeld(ctx, e, tries, sw)
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
// message's files. sw is the Receive files switch it fetches for: the agents' or the page's.
func (d *Daemon) fetchFilesHeld(ctx context.Context, e bus.Event, tries int, sw *fileSwitch) error {
	if d.filesHere(e) {
		return nil
	}
	// Checked again here, after the wait for the turn, and the fetch ends if it goes off.
	if d.refuses(sw, e) {
		return errReceiveOff
	}
	if dev, _ := bus.SplitAddr(e.From); dev != d.device {
		var stop context.CancelFunc
		ctx, stop = sw.receiving(ctx)
		defer stop()
	}
	if e.FilesAt == "" {
		return bus.ErrNoFile
	}
	// Whatever it saves, all or some, counts toward FilesHeld, whoever asked: the loop or the page.
	// Its own directory is pinned while it writes, and through that look (defers run last first).
	defer d.pinFilesDir(d.filesDir(e))()
	defer d.holdFilesHeld()
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
			if d.refuses(sw, e) {
				return errReceiveOff
			}
			return fmt.Errorf("%w: %w", errFetchLater, ctx.Err())
		case <-time.After(time.Duration(try) * 5 * time.Second):
		}
	}
	if d.refuses(sw, e) {
		return errReceiveOff
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
		if d.refuses(sw, e) {
			return errReceiveOff
		}
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
	d.holdFilesHeld()
	// An invisible device writes nothing to the bus (mode.go), and deleting a ref is a write: one
	// that shows, by when it lands, that the device is running. The deletes were refused one by
	// one, a line in the log for each ref at every prune, and the refs stayed (#789). They are
	// left for the prune fileLoop runs once the device is out of invisible, which setMode wakes
	// it for. The copies kept here, above, go on time either way, and the refs of other devices
	// age on: the daemon runs, which is what their age counts.
	hidden := d.hidden.Load()
	d.pruneOwed.Store(hidden)
	if hidden {
		d.mu.Lock()
		d.ageOthersFilesLocked(time.Now())
		if len(d.st.OthersFiles) > 0 {
			d.saveLocked()
		}
		d.mu.Unlock()
		return
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
		// A push that was cut off, and so stayed counted in the day's uploads, is taken off the
		// count if it never landed (#788). Looked at before the delete, which leaves no ref
		// either way, and not until uploadSettle after the push began: what a killed git started
		// may be sending still, and a ref that landed after the look would be on the remote
		// uncounted, and after the delete, for good.
		d.mu.Lock()
		i := slices.IndexFunc(d.st.Uploads, func(u fileUpload) bool { return u.Ref == at })
		counted := i >= 0
		early := counted && time.Since(d.st.Uploads[i].At) < uploadSettle
		d.mu.Unlock()
		if early {
			continue // at the next prune
		}
		if counted && d.filesRefGone(ctx, at) {
			d.mu.Lock()
			if d.refundUploadLocked(at) {
				d.saveLocked()
			}
			d.mu.Unlock()
		}
		// Deleting a ref that is already gone succeeds: the look after a refused push finds it at
		// "", which is what was asked for.
		if err := d.push(ctx, "", bus.FilesRef(at), bus.Force, ""); err != nil {
			if errors.Is(err, ErrInvisible) {
				d.pruneOwed.Store(true) // the device went invisible meanwhile
				return
			}
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

// holdFilesHeld deletes the files this device keeps, those it sent and those it fetched, a message's
// at a time and the oldest first, until they come to FilesHeld (#739). FileKeep alone left no
// bound: a loop sending a 20 MB log to everyone each minute would fill every teammate's disk by a
// gigabyte an hour. A message whose files went is told of as one whose files can't be had. A
// directory a send or a fetch is writing stays, though it counts (pinFilesDir): the caller's own,
// as the page reads a file it fetched once the fetch is over, and another's. A file the page asks
// for again is fetched again, and held to FilesHeld again.
func (d *Daemon) holdFilesHeld() {
	root := filepath.Join(d.dir, "files")
	entries, _ := os.ReadDir(root)
	type held struct {
		name string
		at   time.Time
		size int64
	}
	var all []held
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		h := held{name: entry.Name(), at: info.ModTime()}
		filepath.WalkDir(filepath.Join(root, entry.Name()), func(_ string, f fs.DirEntry, err error) error {
			if err == nil && !f.IsDir() {
				if fi, err := f.Info(); err == nil {
					h.size += fi.Size()
				}
			}
			return nil
		})
		all = append(all, h)
		total += h.size
	}
	if total <= d.opt.FilesHeld {
		return
	}
	slices.SortFunc(all, func(a, b held) int { return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.name, b.name)) })
	for _, h := range all {
		if total <= d.opt.FilesHeld {
			break
		}
		// Claimed under mu, so no writer pins it until the delete is over (pinFilesDir waits), and
		// deleted without it: every delivery takes mu, and deleting 20 MB, Defender scanning, takes a
		// while.
		path := filepath.Join(root, h.name)
		d.mu.Lock()
		if d.filesBusy[path] > 0 || d.filesEvicting[path] != nil {
			d.mu.Unlock()
			continue
		}
		if d.filesEvicting == nil {
			d.filesEvicting = map[string]chan struct{}{}
		}
		done := make(chan struct{})
		d.filesEvicting[path] = done
		d.mu.Unlock()
		err := os.RemoveAll(path)
		d.mu.Lock()
		delete(d.filesEvicting, path)
		close(done)
		d.mu.Unlock()
		if err != nil {
			d.log.Printf("deleting the files saved from files ref %s, past the %s kept: %v", h.name, proto.Size(d.opt.FilesHeld), err)
			continue
		}
		total -= h.size
	}
}

// pinFilesDir keeps holdFilesHeld off directory dir, which a send or a fetch is writing files to,
// until the unpin it returns is called. A fetch that lost the first of its files to another's
// eviction while it saved the rest would say it had them all (Sol's review of #760). A directory
// being deleted is waited for: the writer then makes it afresh.
func (d *Daemon) pinFilesDir(dir string) (unpin func()) {
	if dir == "" {
		return func() {}
	}
	d.mu.Lock()
	for d.filesEvicting[dir] != nil {
		done := d.filesEvicting[dir]
		d.mu.Unlock()
		<-done
		d.mu.Lock()
	}
	defer d.mu.Unlock()
	if d.filesBusy == nil {
		d.filesBusy = map[string]int{}
	}
	d.filesBusy[dir]++
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.filesBusy[dir]--; d.filesBusy[dir] <= 0 {
			delete(d.filesBusy, dir)
		}
	}
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
// own daemon would otherwise delete: a deleted clone's would stay for good (#708). A note whose ref
// is off the remote already goes at once, whatever its age: a ref is pushed before the event that
// names it, so one not listed has been deleted, and the note had nothing left to do but wait out
// twice FileKeep of this daemon's running: most of a year for one that runs an hour a day (#747). A ref goes once
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
	// The notes there are before the listing: one a sync makes while the listing runs can name
	// a ref pushed after it, which the listing lacks though the ref is there.
	notes := slices.Collect(maps.Keys(d.st.OthersFiles))
	if len(notes) > 0 {
		d.saveLocked()
	}
	d.mu.Unlock()
	if len(notes) == 0 {
		return
	}
	// Within a time of its own, as a sync's listing is: this one runs at every prune now, and one
	// left waiting on a dead connection would hold up every message that carries files.
	lctx, cancel := context.WithTimeout(ctx, time.Minute)
	listed, err := d.repo.ListRemote(lctx)
	cancel()
	if err != nil {
		d.log.Printf("listing the remote's files refs: %v", err)
		return
	}
	d.mu.Lock()
	gone := false
	for _, at := range notes {
		if _, ok := d.st.OthersFiles[at]; ok && listed[bus.FilesRef(at)] == "" {
			delete(d.st.OthersFiles, at) // its own daemon, or another here, deleted it
			gone = true
		}
	}
	if gone {
		d.saveLocked()
	}
	d.mu.Unlock()
	forget := func(at string) {
		d.mu.Lock()
		delete(d.st.OthersFiles, at)
		d.saveLocked()
		d.mu.Unlock()
	}
	slices.Sort(due)
	for _, at := range due {
		if listed[bus.FilesRef(at)] == "" {
			continue // its note went above
		}
		d.mu.Lock()
		p := d.peers[d.st.OthersFiles[at].Device]
		live := d.lastPollOK.IsZero() || p != nil && !d.staleLocked(p)
		d.mu.Unlock()
		if live {
			continue
		}
		if err := d.push(ctx, "", bus.FilesRef(at), bus.Force, ""); err != nil {
			if errors.Is(err, ErrInvisible) {
				d.pruneOwed.Store(true) // the device went invisible meanwhile
				return
			}
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
// are known to take none, as Receive files is off for them: they get the text alone. One whose
// device doesn't say is left out (peerFilesLocked): nothing is promised either way. who is what
// the sender called the one it sent to, if it sent to one. Caller holds mu.
func (d *Daemon) noFilesNoteLocked(e bus.Event, who string) string {
	to, direct := d.recipientsOfLocked(e)
	var off, people []string
	for _, addr := range to {
		dev, _ := bus.SplitAddr(addr)
		p := d.peers[dev]
		if dev == d.device || p == nil {
			continue
		}
		name, _, _ := d.sessionInfoLocked(addr)
		if direct {
			name = who
		}
		if d.peerFilesLocked(p, addr) != proto.FilesOff {
			continue
		}
		if bus.IsPerson(addr) {
			people = append(people, nameOr(name, addr))
		} else {
			off = append(off, nameOr(name, addr)) // its person turned their agents' Receive files off (#765)
		}
	}
	var notes []string
	switch len(off) {
	case 0:
	case 1:
		notes = append(notes, off[0]+" takes no files: Receive files is off for the agents on its device, so the text alone gets there.")
	default:
		notes = append(notes, andList(off)+" take no files: Receive files is off for the agents on their devices, so the text alone gets there.")
	}
	switch len(people) {
	case 0:
	case 1:
		notes = append(notes, people[0]+" takes no files: Receive files is off, so the text alone gets there.")
	default:
		notes = append(notes, andList(people)+" take no files: Receive files is off, so the text alone gets there.")
	}
	return strings.Join(notes, " ")
}

// andList is names joined as a sentence lists them: "a, b and c".
func andList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
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
	err = d.fetchFilesHeld(ctx, e, 1, &d.pageRecv)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case err == nil:
		delete(d.fileMiss, e.FilesAt)
	case ctx.Err() != nil, errors.Is(err, errReceiveOff):
		// The page went, or its time ran out, or Receive files went off: that says nothing of the
		// remote.
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
	i := slices.IndexFunc(d.msgs, func(m pageMsg) bool { return m.id == id })
	var e bus.Event
	if i >= 0 {
		e = d.msgs[i].e
	}
	d.mu.Unlock()
	if i < 0 || index >= len(e.Files) || d.refuses(&d.pageRecv, e) {
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
	if d.refuses(&d.pageRecv, e) {
		return "", nil, noFile("no such file") // went off while it was fetched
	}
	return f.Name, data, nil
}

// fileViewsLocked is the files of message e as a session here is told of them: each with the path
// it is saved at, or none if it isn't here (it couldn't be fetched, or is past FileKeep or FilesHeld). Caller
// holds mu.
func (d *Daemon) fileViewsLocked(e bus.Event) []proto.FileView {
	if len(e.Files) == 0 || d.refuses(&d.agentRecv, e) {
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
