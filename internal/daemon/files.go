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

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/scrub"
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

// textOf is a file's bytes as text, if it is text the secret check can read: UTF-16 with its mark,
// as Windows PowerShell writes a redirected file, or anything with no zero byte in it, with
// whatever isn't UTF-8 passed over, so one stray byte doesn't hide the rest.
func textOf(b []byte) (string, bool) {
	if len(b) >= 2 && len(b)%2 == 0 && (b[0] == 0xff && b[1] == 0xfe || b[0] == 0xfe && b[1] == 0xff) {
		var order binary.ByteOrder = binary.LittleEndian
		if b[0] == 0xfe {
			order = binary.BigEndian
		}
		units := make([]uint16, 0, len(b)/2-1)
		for i := 2; i < len(b); i += 2 {
			units = append(units, order.Uint16(b[i:]))
		}
		return string(utf16.Decode(units)), true
	}
	if slices.Contains(b, 0) {
		return "", false
	}
	return strings.ToValidUTF8(string(b), " "), true
}

// unixPath says whether path is a Unix shell's, as Git Bash's /tmp is, on Windows: it starts at a
// root with a forward slash and names no drive. Two slashes are a share's, and full already.
func unixPath(path string) bool {
	return runtime.GOOS == "windows" && strings.HasPrefix(path, "/")
}

// readSendFiles reads the files a send names and holds them to the limits. A text file is checked
// for secrets as a body is; anything else can't be.
func readSendFiles(paths []string) ([][]byte, []bus.File, error) {
	if len(paths) > bus.MaxFiles {
		return nil, nil, fmt.Errorf("a message carries at most %d files, and this has %d", bus.MaxFiles, len(paths))
	}
	var total int64
	data := make([][]byte, 0, len(paths))
	files := make([]bus.File, 0, len(paths))
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			if unixPath(path) {
				// Git Bash's /tmp, say: Windows can't open it, and would read it on the current
				// drive, where another file of that name may be (#706).
				return nil, nil, fmt.Errorf("%s isn't a full path on Windows: pass the Windows path, as `cygpath -w '%s'` prints it", path, path)
			}
			return nil, nil, fmt.Errorf("%s isn't a full path", path)
		}
		info, err := os.Stat(path)
		switch {
		case err != nil:
			return nil, nil, fmt.Errorf("can't read %s: %v", path, errors.Unwrap(err))
		case !info.Mode().IsRegular():
			return nil, nil, fmt.Errorf("%s isn't a file", path)
		case info.Size() > bus.MaxFile:
			return nil, nil, fmt.Errorf("%s is %s, and a file can be at most %d MB", path, proto.Size(info.Size()), bus.MaxFile>>20)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, fmt.Errorf("can't read %s: %v", path, errors.Unwrap(err))
		}
		b, err := io.ReadAll(io.LimitReader(f, bus.MaxFile+1)) // it may have grown since the look
		f.Close()
		switch {
		case err != nil:
			return nil, nil, fmt.Errorf("can't read %s: %v", path, err)
		case len(b) > bus.MaxFile:
			return nil, nil, fmt.Errorf("%s is over %d MB, the most a file can be", path, bus.MaxFile>>20)
		}
		if total += int64(len(b)); total > bus.MaxFilesTotal {
			return nil, nil, fmt.Errorf("a message's files can come to at most %d MB together", bus.MaxFilesTotal>>20)
		}
		if text, ok := textOf(b); ok {
			if err := scrub.Check(text); err != nil {
				return nil, nil, fmt.Errorf("%s: %v", path, err)
			}
		}
		data = append(data, b)
		files = append(files, bus.File{Name: filepath.Base(path)})
	}
	return data, files, nil
}

// putFiles encrypts the files a send names and pushes them to a new files ref, and returns what
// the event says of them and the ref's name. It keeps a copy of each where a receiver's would be,
// so a session on this device, and the page, have them without a fetch.
func (d *Daemon) putFiles(ctx context.Context, paths []string) (files []bus.File, at string, err error) {
	data, files, err := readSendFiles(paths)
	if err != nil {
		return nil, "", err
	}
	at = bus.NewFilesAt()
	sealed := make([][]byte, len(data))
	for i, b := range data {
		blob, f, err := bus.SealFile(b, at, i)
		if err != nil {
			return nil, "", err
		}
		f.Name = files[i].Name
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
		if err := saveFile(dir, f.Name, data[i]); err != nil {
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
// held for a person: the page doesn't show files yet, and will ask for one when it shows it.
// Caller holds mu.
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
			err := d.fetchFiles(ctx, e)
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
// is tried again, as the network may be why; a ref that isn't there, or holds something else, is
// not. What the fetch brings goes to a scratch directory, not the clone's objects.
func (d *Daemon) fetchFiles(ctx context.Context, e bus.Event) error {
	if d.filesHere(e) {
		return nil
	}
	if e.FilesAt == "" {
		return bus.ErrNoFile
	}
	repo, done, err := d.scratch()
	if err != nil {
		return err
	}
	defer done()
	ref := bus.FilesRef(e.FilesAt)
	var sha string
	for try := 1; ; try++ {
		fctx, cancel := context.WithTimeout(ctx, fileFetchLimit)
		sha, err = repo.FetchFiles(fctx, ref, bus.MaxPack)
		cancel()
		if err == nil || errors.Is(err, bus.ErrNoFile) || errors.Is(err, bus.ErrTooLarge) || try == fileTries || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(try) * 5 * time.Second):
		}
	}
	if err != nil {
		return err
	}
	pack, err := repo.ReadPack(ctx, sha, bus.MaxPack)
	if err != nil {
		return err
	}
	var failed []string
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
			err = saveFile(d.filesDir(e), f.Name, plain)
		}
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", f.Name, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}

// pruneFiles deletes the copies of files kept longer than FileKeep, and from the remote the files
// refs this device pushed that long ago.
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
}

// noFilesNoteLocked tells the sender of message e, which carries files, which of its recipients
// get the text alone, with no sign that there was more: those on a device whose daemon is from
// before files, and a person, whose page doesn't show files yet. who is what the sender called a
// person it sent to. Caller holds mu.
func (d *Daemon) noFilesNoteLocked(e bus.Event, who string) string {
	if bus.IsPerson(e.To) {
		// A person gets only what is sent to them, never a broadcast, so they are the one recipient.
		return who + " reads this on " + brand.Product + "'s page, which doesn't show files yet: the text alone gets there."
	}
	to, _ := d.recipientsOfLocked(e)
	var old []string
	for _, addr := range to {
		dev, _ := bus.SplitAddr(addr)
		if p := d.peers[dev]; dev != d.device && p != nil && !p.locked && !p.pres.Files {
			name, _, _ := d.sessionInfoLocked(addr)
			old = append(old, nameOr(name, addr))
		}
	}
	switch len(old) {
	case 0:
		return ""
	case 1:
		return old[0] + " is on a device that runs a " + brand.Product + " from before files, so it gets the text alone."
	}
	return strings.Join(old[:len(old)-1], ", ") + " and " + old[len(old)-1] + " are on devices that run a " +
		brand.Product + " from before files, so they get the text alone."
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
