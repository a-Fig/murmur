package daemon_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
	"github.com/a-Fig/murmur/internal/web"
)

// utf16File is text as Windows PowerShell writes a redirected file: UTF-16, little end first, after
// its mark.
func utf16File(text string) []byte {
	b := []byte{0xff, 0xfe}
	for _, u := range utf16.Encode([]rune(text)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// utf16BE is text as UTF-16, big end first, with no mark.
func utf16BE(text string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(text)) {
		b = append(b, byte(u>>8), byte(u))
	}
	return b
}

// writeFile writes a file a test sends, in a directory of its own, and returns its full path.
func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// inboxMessage waits for the one message in a session's inbox and returns it.
func inboxMessage(t *testing.T, d *device, sess string) proto.MessageView {
	t.Helper()
	var got []proto.MessageView
	eventually(t, "a message for "+sess, func() bool {
		got = append(got, d.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages...)
		return len(got) > 0
	})
	if len(got) != 1 {
		t.Fatalf("%d messages for %s, want one: %+v", len(got), sess, got)
	}
	return got[0]
}

// A message's files reach a session on another device as files on its own machine, byte for byte,
// saved inside the git directory, and the turn it reads gives their paths (#570). On the remote
// they sit encrypted in one blob on a ref of the message's own, which the receiver keeps no copy of.
func TestFilesTravelWithAMessage(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	var work, before string
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) {
		work = filepath.Dir(d.gitDir)
		before = git(t, work, "status", "--porcelain", "--ignored")
	})
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	eventually(t, "b to see alice", func() bool { return b.seesSession(alice) })

	image := append([]byte("\x89PNG\r\n\x1a\n\x00"), bytes.Repeat([]byte{0xfe, 0x00, 0x0d, 0x0a}, 5000)...)
	notes := []byte("first line\r\nsecond line\n")
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "the failing page",
		Files: []string{writeFile(t, "shot one.png", image), writeFile(t, "notes.txt", notes)}})
	if r.Error != "" || r.Note != "" || len(r.Messages) != 1 {
		t.Fatalf("send: %+v", r)
	}
	id := r.Messages[0].ID

	// The hook's notice says there are files, and isn't taken for the whole message.
	var notices []proto.MessageView
	eventually(t, "a notice", func() bool {
		notices = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"}).Messages
		return len(notices) > 0
	})
	if text := view.Notices(notices, 1); !strings.Contains(text, `"the failing page", with 2 files`) {
		t.Errorf("notice: %q", text)
	}
	m := inboxMessage(t, b, "bbbbbb")
	if len(m.Files) != 2 || m.Files[0].Name != "shot_one.png" || m.Files[1].Name != "notes.txt" {
		t.Fatalf("files: %+v", m.Files)
	}
	for i, want := range [][]byte{image, notes} {
		f := m.Files[i]
		got, err := os.ReadFile(f.Path)
		if err != nil || !bytes.Equal(got, want) || f.Size != int64(len(want)) {
			t.Errorf("%s: %d bytes at %q, %v; want %d", f.Name, len(got), f.Path, err, len(want))
		}
		if !strings.HasPrefix(f.Path, b.gitDir+string(filepath.Separator)) {
			t.Errorf("%s is saved outside the git directory, at %s", f.Name, f.Path)
		}
	}
	turn := view.Turn(m)
	if !strings.Contains(turn, "Files, saved on this machine:\n- "+m.Files[0].Path+" (20 KB)\n- "+m.Files[1].Path+" (24 bytes)\n") {
		t.Errorf("turn:\n%s", turn)
	}
	if inbox := view.Inbox(proto.Response{Messages: []proto.MessageView{m}}); !strings.Contains(inbox, "- "+m.Files[1].Path+" (24 bytes)\n") {
		t.Errorf("inbox:\n%s", inbox)
	}

	// On the remote: one ref, whose name says neither the message nor the device, holding one
	// blob longer than the files, with nothing in the clear. The names are inside the encrypted
	// event.
	refs := strings.Fields(git(t, remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files"))
	if len(refs) != 1 || strings.Contains(refs[0], a.self()) || strings.Contains(refs[0], id) {
		t.Fatalf("files refs on the remote: %v", refs)
	}
	if tree := git(t, remote, "ls-tree", "--name-only", refs[0]); tree != "0" {
		t.Errorf("the files commit's tree: %q", tree)
	}
	if c := git(t, remote, "cat-file", "commit", refs[0]); strings.Contains(c, a.self()) || !strings.Contains(c, " 946684800 +0000") {
		t.Errorf("the files commit says who or when:\n%s", c)
	}
	if size := git(t, remote, "cat-file", "-s", refs[0]+":0"); size != "20480" { // 20009+24 and 28 each is 20089
		t.Errorf("the pack is %s bytes", size)
	}
	if blob := git(t, remote, "cat-file", "blob", refs[0]+":0"); strings.Contains(blob, "first line") {
		t.Error("the remote holds a file in the clear")
	}
	if log := git(t, remote, "log", "--format=%B", "refs/murmur/mesh/"+a.self()); strings.Contains(log, "notes.txt") {
		t.Error("the sender's log names a file in the clear")
	}
	if !strings.Contains(busText(t, remote, a), `"name":"notes.txt"`) {
		t.Error("the event doesn't list the file")
	}
	// The receiver's checkout and refs are as they were, and so is the sender's copy for its page.
	if after := git(t, work, "status", "--porcelain", "--ignored"); after != before {
		t.Errorf("the receiver's working tree changed:\n%s", after)
	}
	if refs := git(t, work, "for-each-ref", "refs/murmur/files"); refs != "" {
		t.Errorf("the receiver kept a files ref: %s", refs)
	}
	at := strings.TrimPrefix(refs[0], "refs/murmur/files/")
	copies, _ := filepath.Glob(filepath.Join(a.gitDir, "murmur", "files", at+"-*", "notes.txt"))
	if len(copies) != 1 {
		t.Fatalf("the sender's copies: %v", copies)
	}
	if got, err := os.ReadFile(copies[0]); err != nil || !bytes.Equal(got, notes) {
		t.Errorf("the sender's copy: %q, %v", got, err)
	}
	// Neither clone keeps the files commit: it was made, and fetched, in a directory since deleted.
	commit := git(t, remote, "rev-parse", refs[0])
	for who, d := range map[string]*device{"sender": a, "receiver": b} {
		if gitOK(filepath.Dir(d.gitDir), "cat-file", "-t", commit) != "" {
			t.Errorf("the %s's clone holds the files commit", who)
		}
		if left, _ := filepath.Glob(filepath.Join(d.gitDir, "murmur", "files-scratch-*")); len(left) > 0 {
			t.Errorf("the %s left %v", who, left)
		}
	}
}

// A session on the sender's own device has the files at once, with nothing fetched.
func TestFilesForASessionOnTheSameDevice(t *testing.T) {
	t.Parallel()
	a := newDevice(t, newRemote(t))
	a.session("aaaaaa", "")
	bob, _ := a.session("bbbbbb", "")
	log := []byte("panic: runtime error\n")
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "the log", Files: []string{writeFile(t, "run.log", log)}}); r.Error != "" {
		t.Fatalf("send: %s", r.Error)
	}
	m := inboxMessage(t, a, "bbbbbb")
	if len(m.Files) != 1 {
		t.Fatalf("files: %+v", m.Files)
	}
	if got, err := os.ReadFile(m.Files[0].Path); err != nil || !bytes.Equal(got, log) {
		t.Errorf("the file: %q, %v", got, err)
	}
}

// sendFiles sends as the page's person, with files, as a browser does: a form with a part for
// each. It returns how the daemon answered.
func (p *page) sendFiles(to, text string, files ...web.Upload) (int, string) {
	p.t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	w.WriteField("to", to)
	w.WriteField("text", text)
	for _, u := range files {
		part, err := w.CreateFormFile("file", u.Name)
		if err != nil {
			p.t.Fatal(err)
		}
		part.Write(u.Data)
	}
	w.Close()
	req, err := http.NewRequest("POST", p.base+"/api/send", &b)
	if err != nil {
		p.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatalf("sending files to %s: %v", to, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// A person on the page gets an agent's files and sends their own (#570). The page lists what a
// message carries and gives each file when it is asked for, which is when the daemon fetches it:
// nothing waits on a fetch for a person. A file the person uploads reaches an agent as an agent's
// does, and is checked as one is.
func TestFilesToAndFromThePage(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada-lovelace")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	// From an agent to the person.
	chart := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
	var r proto.Response
	eventually(t, "bob's device to know ada", func() bool {
		r = b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "ada-lovelace", Subject: "the chart", Files: []string{writeFile(t, "chart.png", chart)}})
		return r.Error == ""
	})
	if strings.Contains(r.Note, "text alone") {
		t.Errorf("the sender was told ada gets no files: %q", r.Note)
	}
	var m web.Message
	eventually(t, "the page to list the message", func() (ok bool) {
		m, ok = pa.messageWith("the chart")
		return ok
	})
	if len(m.Files) != 1 || m.Files[0].Name != "chart.png" || m.Files[0].Size != int64(len(chart)) {
		t.Fatalf("the files the page lists: %+v", m.Files)
	}
	if code, body := pa.call("GET", "/api/file?id="+m.ID+"&i=0", pa.token, ""); code != http.StatusOK || body != string(chart) {
		t.Errorf("the file, asked for: %d %q", code, body)
	}
	if code, _ := pa.call("GET", "/api/file?id="+m.ID+"&i=1", pa.token, ""); code != http.StatusNotFound {
		t.Errorf("a file the message hasn't got: %d, want 404", code)
	}

	// From the person to an agent, with nothing written: two files of one name, as two pasted
	// screenshots are. The subject is the names they were sent under.
	notes, more := []byte("what I see on my screen\n"), []byte("and after a reload\n")
	code, body := pa.sendFiles(bobName, "", web.Upload{Name: "my notes.txt", Data: notes}, web.Upload{Name: "my notes.txt", Data: more})
	if code != http.StatusOK {
		t.Fatalf("the person's send: %d %s", code, body)
	}
	const names = "my_notes.txt, 2-my_notes.txt"
	got := inboxMessage(t, b, "bbbbbb")
	if got.Subject != names || len(got.Files) != 2 || got.Files[0].Name+", "+got.Files[1].Name != names {
		t.Fatalf("what bob got: %+v", got)
	}
	for i, want := range [][]byte{notes, more} {
		if saved, err := os.ReadFile(got.Files[i].Path); err != nil || !bytes.Equal(saved, want) {
			t.Errorf("bob's copy of %s: %q, %v", got.Files[i].Name, saved, err)
		}
	}
	// The person's own page shows them too, from the copies the send kept.
	mine, ok := pa.messageWith(names)
	if !ok || len(mine.Files) != 2 {
		t.Fatalf("the person's own message on the page: %+v, %v", mine, ok)
	}
	if code, body := pa.call("GET", "/api/file?id="+mine.ID+"&i=1", pa.token, ""); code != http.StatusOK || body != string(more) {
		t.Errorf("the person's own file, asked for: %d %q", code, body)
	}

	// A key in a file the person attached stops the send, as one in an agent's does.
	code, body = pa.sendFiles(bobName, "my settings", web.Upload{Name: "env.txt", Data: []byte("AWS=AKIAIOSFODNN7EXAMPLE\n")})
	if code != http.StatusBadRequest || !strings.Contains(body, "env.txt") {
		t.Errorf("a key in an attached file: %d %s", code, body)
	}
}

// What a send's files may be: full paths of files, within the limits, with no secret in a text
// one. A refused send writes nothing to the remote.
func TestFilesASendRefuses(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	a.session("aaaaaa", "")
	small := writeFile(t, "small.txt", []byte("fine"))
	var nine []string
	for range bus.MaxFiles + 1 {
		nine = append(nine, small)
	}
	big := writeFile(t, "big.bin", make([]byte, bus.MaxFile+1))
	half := writeFile(t, "half.bin", make([]byte, bus.MaxFile-1))
	for what, c := range map[string]struct {
		files []string
		why   string
	}{
		"a path that isn't full":    {[]string{"small.txt"}, "isn't a full path"},
		"a Git Bash path":           {[]string{"/tmp/shot.png"}, "pass the Windows path, as `cygpath -w '/tmp/shot.png'` prints it"},
		"a file that isn't there":   {[]string{filepath.Join(t.TempDir(), "nothing.txt")}, "can't read"},
		"a directory":               {[]string{t.TempDir()}, "isn't a file"},
		"too many files":            {nine, "at most 8 files"},
		"a file over the limit":     {[]string{big}, "at most 10 MB"},
		"files over the limit":      {[]string{half, half, half}, "at most 20 MB together"},
		"a key in a text file":      {[]string{small, writeFile(t, "env.txt", []byte("AWS=AKIAIOSFODNN7EXAMPLE\n"))}, "env.txt"},
		"a key in a file named png": {[]string{writeFile(t, "shot.png", []byte("AKIAIOSFODNN7EXAMPLE"))}, "shot.png"},
		"a key in UTF-16":           {[]string{writeFile(t, "ps.txt", utf16File("key AKIAIOSFODNN7EXAMPLE\r\n"))}, "ps.txt"},
		"a key after a stray byte":  {[]string{writeFile(t, "latin.txt", []byte("caf\xe9 AKIAIOSFODNN7EXAMPLE\n"))}, "latin.txt"},
		// A zero byte once made a file binary, and unchecked (#737).
		"a key in an environ":                  {[]string{writeFile(t, "environ", []byte("HOME=/root\x00GH=ghp_0123456789abcdefghijABCDEFGHIJ0123456789\x00"))}, "environ"},
		"a key in a tar":                       {[]string{writeFile(t, "conf.tar", append(append(make([]byte, 512), "AWS=AKIAIOSFODNN7EXAMPLE\n"...), make([]byte, 512)...))}, "conf.tar"},
		"a key in UTF-16 with no mark":         {[]string{writeFile(t, "le.txt", utf16File("key AKIAIOSFODNN7EXAMPLE\r\n")[2:])}, "le.txt"},
		"a key in big-end UTF-16 with no mark": {[]string{writeFile(t, "be.txt", append([]byte{0}, utf16File("key AKIAIOSFODNN7EXAMPLE")[2:]...))}, "be.txt"},
		// What Sol's review of #753 found getting through the first fix.
		"a name on the line above its value": {[]string{writeFile(t, "split.bin", []byte("TOKEN=\nabc123abc123\x00"))}, "split.bin"},
		"a bearer token on the next line":    {[]string{writeFile(t, "bearer.bin", []byte("Bearer\nabc123abc123abc123abc123\x00"))}, "bearer.bin"},
		"a password with an accent":          {[]string{writeFile(t, "accent.bin", []byte("PASSWORD=abc123édef456\x00"))}, "accent.bin"},
		"a big-end key before a CJK letter":  {[]string{writeFile(t, "cjk.bin", utf16BE("AKIAIOSFODNN7EXAMPLE中"))}, "cjk.bin"},
		"a key in the clear after a mark":    {[]string{writeFile(t, "mark.txt", []byte("\xff\xfeAKIAIOSFODNN7EXAMPLE"))}, "mark.txt"},
		// And what its second round found.
		"a name a form feed above its value":  {[]string{writeFile(t, "ff.bin", []byte("TOKEN=\fabc123abc123\x00"))}, "ff.bin"},
		"a URL password around a U+FFFD":      {[]string{writeFile(t, "fffd.bin", []byte("https://user:abc�def@host\x00"))}, "fffd.bin"},
		"a URL password around a control":     {[]string{writeFile(t, "ctl.bin", []byte("https://user:abc\x01def@host\x00"))}, "ctl.bin"},
		"a URL password in UTF-16, a control": {[]string{writeFile(t, "ctl16.bin", utf16File("https://user:abc\x01def@host")[2:])}, "ctl16.bin"},
		"UTF-16 cut short":                    {[]string{writeFile(t, "cut.bin", utf16File("AKIAIOSFODNN7EXAMPLE")[2:41])}, "cut.bin"},
		"UTF-16 with its mark, cut short":     {[]string{writeFile(t, "cutmark.txt", utf16File("AKIAIOSFODNN7EXAMPLE")[:41])}, "cutmark.txt"},
	} {
		if what == "a Git Bash path" && runtime.GOOS != "windows" {
			continue // a full path everywhere else
		}
		r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "files", Files: c.files})
		if !strings.Contains(r.Error, "not sent") || !strings.Contains(r.Error, c.why) {
			t.Errorf("%s: %q, want it not sent, saying %q", what, r.Error, c.why)
		}
	}
	if refs := git(t, remote, "for-each-ref", "refs/murmur/files"); refs != "" {
		t.Errorf("a refused send left files on the remote: %s", refs)
	}
}

// A message whose files can't be had still arrives, saying which file isn't there: a files ref
// that was never pushed, and one that holds something else. Anyone with push access can write
// either.
func TestMessageArrivesWithoutFilesThatCannotBeHad(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	_, f, err := bus.SealFile([]byte("what the event describes"), bus.NewFilesAt(), 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "gone.txt"
	forger := "forger-0001"
	pushEvent(t, remote, "refs/murmur/mesh/"+forger, bus.Event{V: bus.Version, ID: "00000000000000aa", Type: bus.Message,
		From: forger + "/cccccc", At: time.Now().UTC(), To: bob, Subject: "no ref", Files: []bus.File{f}, FilesAt: bus.NewFilesAt()})
	m := inboxMessage(t, b, "bbbbbb")
	if m.Subject != "no ref" || len(m.Files) != 1 || m.Files[0].Path != "" {
		t.Fatalf("message: %+v", m)
	}
	if turn := view.Turn(m); !strings.Contains(turn, "- gone.txt (24 bytes): not here.") {
		t.Errorf("turn:\n%s", turn)
	}

	// A ref that holds something else where the file should be.
	at := bus.NewFilesAt()
	work := t.TempDir()
	git(t, work, "init", "-q")
	blob := gitIn(t, work, strings.Repeat("not the file ", 400), "hash-object", "-w", "--stdin")
	tree := gitIn(t, work, "100644 blob "+blob+"\t0\n", "mktree")
	commit := gitIn(t, work, "files", "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", tree)
	git(t, work, "push", "-q", remote, commit+":refs/murmur/files/"+at)
	f.Name = "swapped.txt"
	pushEvent(t, remote, "refs/murmur/mesh/"+forger, bus.Event{V: bus.Version, ID: "00000000000000bb", Type: bus.Message,
		From: forger + "/cccccc", At: time.Now().UTC(), To: bob, Subject: "wrong blob", Files: []bus.File{f}, FilesAt: at})
	m = inboxMessage(t, b, "bbbbbb")
	if m.Subject != "wrong blob" || len(m.Files) != 1 || m.Files[0].Path != "" {
		t.Fatalf("message: %+v", m)
	}
	if saved, _ := filepath.Glob(filepath.Join(b.gitDir, "murmur", "files", "*", "swapped.txt")); len(saved) > 0 {
		t.Errorf("a blob that isn't the file was saved as it: %v", saved)
	}
}

func fileThere(path string) bool {
	_, err := os.Stat(path)
	return path != "" && err == nil
}

// A sender takes a message's files off the remote once they are past their time, and its own
// copies with them. A receiver deletes its copies too.
func TestFilesLeaveTheRemote(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	b.session("bbbbbb", "")
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "for a while", Files: []string{writeFile(t, "a.txt", []byte("a"))}})
	if r.Error != "" {
		t.Fatalf("send: %s", r.Error)
	}
	got := inboxMessage(t, b, "bbbbbb")
	if len(got.Files) != 1 || !fileThere(got.Files[0].Path) {
		t.Fatalf("what the receiver got: %+v", got.Files)
	}
	b.fileKeep, b.filePrune = time.Millisecond, 300*time.Millisecond
	b.restart()
	eventually(t, "the receiver's copy to go", func() bool { return !fileThere(got.Files[0].Path) })
	listed := func() string { return gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files") }
	saved := func() []string {
		got, _ := filepath.Glob(filepath.Join(a.gitDir, "murmur", "files", "*", "a.txt"))
		return got
	}
	if len(saved()) != 1 || listed() == "" {
		t.Fatalf("after the send: the copies %v, the files refs %q", saved(), listed())
	}
	// The same device, with a time to keep files that has already passed.
	a.fileKeep, a.filePrune = time.Millisecond, 300*time.Millisecond
	a.restart()
	eventually(t, "the files ref to leave the remote", func() bool { return listed() == "" })
	eventually(t, "the sender's copy to go", func() bool { return len(saved()) == 0 })
}

// An invisible device writes nothing to the bus, a delete of its files refs included (#789): one
// past its time stays while the device is invisible, and goes when it is back online, without
// waiting for the next prune.
func TestAnInvisibleDeviceDeletesItsFilesRefsOnceBack(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	b.session("bbbbbb", "")
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "for a while", Files: []string{writeFile(t, "a.txt", []byte("a"))}})
	if r.Error != "" {
		t.Fatalf("send: %s", r.Error)
	}
	inboxMessage(t, b, "bbbbbb")
	listed := func() string { return gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files") }
	a.page().mode("invisible")
	eventually(t, "b to drop a", func() bool { return !b.lists(a.self()) })
	// The same device, still invisible, with a time to keep files that has already passed, and a
	// prune it would wait an hour for after its first.
	a.fileKeep, a.filePrune = time.Millisecond, time.Hour
	a.restart()
	holds(t, "the files ref, while its device is invisible", 2*time.Second, func() bool { return listed() != "" })
	a.page().mode("online")
	eventually(t, "the files ref to leave the remote", func() bool { return listed() == "" })
}

// A files ref whose device has gone, as a deleted clone's has, is deleted from the remote by a
// device that read of it (#708), and only once that device is gone: a live one deletes its own.
func TestFilesOfAGoneDeviceLeaveTheRemote(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "for a while", Files: []string{writeFile(t, "a.txt", []byte("a"))}})
	if r.Error != "" {
		t.Fatalf("send: %s", r.Error)
	}
	inboxMessage(t, b, "bbbbbb")
	listed := func() string { return gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files") }

	// b, with a time to keep files that has long passed, leaves the ref of a, which is live.
	b.fileKeep, b.filePrune = time.Millisecond, 200*time.Millisecond
	b.restart()
	eventually(t, "b to see alice", func() bool { return b.seesSession(alice) })
	time.Sleep(time.Second)
	if listed() == "" {
		t.Fatal("b deleted the files ref of a live device")
	}
	a.stop()
	eventually(t, "the files ref of a, gone, to leave the remote", func() bool { return listed() == "" })
}

// A send reads files only from the clone's worktrees and the temp folder, never its git directory,
// and follows a link to where it leads before it looks (#690). A refusal says where to copy the file.
func TestSendReadsFilesOnlyFromTheCloneAndTemp(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	base := t.TempDir()
	temp, outside := filepath.Join(base, "temp"), filepath.Join(base, "outside")
	for _, dir := range []string{temp, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = temp })
	a.session("aaaaaa", "")
	put := func(dir, name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("contents of "+name), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	send := func(path string) string {
		return a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "a file", Files: []string{path}}).Error
	}
	for what, path := range map[string]string{
		"a file in temp":           put(temp, "shot.png"),
		"a file in the clone":      put(filepath.Dir(a.gitDir), "notes.txt"),
		"a file in temp, by a dot": filepath.Join(temp, "..", "temp", "shot.png"),
	} {
		if err := send(path); err != "" {
			t.Errorf("%s: %s", what, err)
		}
	}
	secret := put(outside, "id_ed25519")
	refused := map[string]string{
		"a file elsewhere":                secret,
		"a file in the git directory":     put(a.gitDir, "config-copy"),
		"a way out of temp by its parent": filepath.Join(temp, "..", "outside", "id_ed25519"),
	}
	if link := filepath.Join(temp, "key"); os.Symlink(secret, link) == nil {
		refused["a link in temp to a file elsewhere"] = link
		if os.Symlink(filepath.Join("..", "outside"), filepath.Join(temp, "out")) == nil {
			refused["a relative link in temp to a folder elsewhere"] = filepath.Join(temp, "out", "id_ed25519")
		}
		if os.Symlink(filepath.Join(temp, "shot.png"), filepath.Join(temp, "shot-link.png")) == nil {
			if err := send(filepath.Join(temp, "shot-link.png")); err != "" {
				t.Errorf("a link in temp to a file in temp: %s", err)
			}
		}
	}
	// A linked worktree's files are sent. A worktree's registration is in the git directory, which
	// an agent can write; pointing it at a folder elsewhere doesn't make that folder one.
	clone := filepath.Dir(a.gitDir)
	wt := filepath.Join(base, "wt")
	gitOK(clone, "worktree", "add", "--detach", wt)
	if err := send(put(wt, "diff.txt")); err != "" {
		t.Errorf("a file in a linked worktree: %s", err)
	}
	// A repo of its own inside the clone, as a submodule or a vendored one is, is passed over for
	// the worktree around it (#772).
	nested := filepath.Join(clone, "vendored")
	gitOK(clone, "init", "-q", nested)
	if err := send(put(nested, "lib.txt")); err != "" {
		t.Errorf("a file in a repo inside the clone: %s", err)
	}
	// A repo of its own outside the clone and temp isn't one of its worktrees.
	elsewhere := filepath.Join(base, "elsewhere")
	gitOK(base, "init", "-q", elsewhere)
	refused["a file in another repo"] = put(elsewhere, "secret.txt")
	// One registered by relative paths, as git worktree add --relative-paths does.
	wt2 := filepath.Join(base, "wt2")
	gitOK(clone, "worktree", "add", "--detach", wt2)
	reg := filepath.Join(a.gitDir, "worktrees", "wt2")
	relTo := func(from, to string) []byte {
		rel, err := filepath.Rel(from, to)
		if err != nil {
			t.Fatal(err)
		}
		return []byte(filepath.ToSlash(rel) + "\n")
	}
	if err := os.WriteFile(filepath.Join(reg, "gitdir"), relTo(reg, filepath.Join(wt2, ".git")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt2, ".git"), append([]byte("gitdir: "), relTo(wt2, reg)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := send(put(wt2, "rel.txt")); err != "" {
		t.Errorf("a file in a worktree registered by relative paths: %s", err)
	}
	// A registration naming the git directory itself as a worktree, with a .git there to match.
	if err := os.MkdirAll(filepath.Join(a.gitDir, "worktrees", "evil"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.gitDir, "worktrees", "evil", "gitdir"), []byte(filepath.ToSlash(filepath.Join(a.gitDir, ".git"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.gitDir, ".git"), []byte("gitdir: "+filepath.ToSlash(filepath.Join(a.gitDir, "worktrees", "evil"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused["the git directory's config, by a registration naming it as a worktree"] = filepath.Join(a.gitDir, "config")
	if err := os.MkdirAll(filepath.Join(outside, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.gitDir, "worktrees", "wt", "gitdir"), []byte(filepath.ToSlash(filepath.Join(outside, ".git"))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused["a file in a folder a worktree's registration was pointed at"] = secret
	// A .git file an agent wrote, in a folder elsewhere, pointing at the git directory or at a
	// worktree's registration, doesn't make that folder a worktree (Sol's review of #805).
	for name, ptr := range map[string]string{"planted": a.gitDir, "planted2": reg} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.ToSlash(ptr)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		refused["a file in a folder with a .git planted to point at "+name] = put(dir, "secret.txt")
	}
	// Nor does a hard link to a real worktree's .git: the same file, in another folder.
	wt3, linked := filepath.Join(base, "wt3"), filepath.Join(base, "linked")
	gitOK(clone, "worktree", "add", "--detach", wt3)
	if err := os.MkdirAll(linked, 0o700); err != nil {
		t.Fatal(err)
	}
	if os.Link(filepath.Join(wt3, ".git"), filepath.Join(linked, ".git")) == nil {
		refused["a file in a folder with a hard link to a worktree's .git"] = put(linked, "secret.txt")
	}
	if upper := filepath.Join(clone, ".GIT", "config"); func() bool { _, err := os.Stat(upper); return err == nil }() {
		refused["a file in the git directory, spelled in other case"] = upper // Windows', macOS's
	}
	if alias := filepath.Join(temp, "alias"); os.Symlink(clone, alias) == nil {
		refused["a file in the git directory, by a link to the clone"] = filepath.Join(alias, ".git", "config")
	}
	for what, path := range refused {
		err := send(path)
		if !strings.Contains(err, "isn't in this repo's clone or the temp folder") || !strings.Contains(err, "copy it into "+temp) {
			t.Errorf("%s: %q, want it refused, saying to copy it into %s", what, err, temp)
		}
	}
	if runtime.GOOS == "windows" {
		for _, path := range []string{`\\localhost\c$\Windows\win.ini`, `\/localhost/c$/Windows/win.ini`, `\\?\C:\Windows\win.ini`} {
			if err := send(path); !strings.Contains(err, "is a network path") {
				t.Errorf("%s: %q, want it refused as a network path", path, err)
			}
		}
	}
}

// The same files sent to several names, a message for each, go up once: every message names the
// one files ref, and each recipient gets them (#739).
func TestASendToSeveralUploadsTheFilesOnce(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	a.session("dddddd", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	eventually(t, "a to see bob and carol", func() bool { return a.seesSession(bob) && a.seesSession(carol) })
	log := writeFile(t, "build.log", bytes.Repeat([]byte("line\n"), 400))
	send := func(from, to, path string) {
		t.Helper()
		if r := a.do(proto.Request{Op: proto.OpSend, Session: from, To: to, Subject: "files", Files: []string{path}}); r.Error != "" {
			t.Fatalf("send to %s: %s", to, r.Error)
		}
	}
	send("aaaaaa", bob, log)
	// Another session's upload between the two doesn't make the second upload the log again.
	send("dddddd", alice, writeFile(t, "other.txt", []byte("something else")))
	send("aaaaaa", carol, log)
	if refs := strings.Fields(gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files")); len(refs) != 2 {
		t.Errorf("files refs on the remote: %v, want two: the log's and the other's", refs)
	}
	for _, sess := range []string{"bbbbbb", "cccccc"} {
		if m := inboxMessage(t, b, sess); len(m.Files) != 1 || !fileThere(m.Files[0].Path) {
			t.Errorf("%s got %+v", sess, m.Files)
		}
	}
}

// A device uploads at most FilesPerDay of files in a day. Past it a send with new files is refused,
// saying when it can go; the same files sent again take the ref already up, and cost nothing (#739).
func TestFilesPastADaysUploadsAreRefused(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.filesPerDay = 6000 }) // a pack is 4 KB at least
	a.session("aaaaaa", "")
	first := writeFile(t, "one.bin", bytes.Repeat([]byte{1}, 2000))
	send := func(path string) proto.Response {
		return a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "files", Files: []string{path}})
	}
	if r := send(first); r.Error != "" {
		t.Fatalf("the first: %s", r.Error)
	}
	if r := send(first); r.Error != "" {
		t.Errorf("the same files again: %s", r.Error)
	}
	r := send(writeFile(t, "two.bin", bytes.Repeat([]byte{2}, 2000)))
	if !strings.Contains(r.Error, "not sent: this device has sent") || !strings.Contains(r.Error, "in the last day") || !strings.Contains(r.Error, "it can send these after") {
		t.Errorf("past the day's uploads: %q", r.Error)
	}
	if refs := strings.Fields(gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files")); len(refs) != 1 {
		t.Errorf("files refs on the remote: %v, want one", refs)
	}
}

// An upload the remote refused isn't counted in the day's uploads (#788): every try says the
// files didn't go out, none that the device has sent its day's worth, and the files go once the
// remote takes them.
func TestARefusedUploadIsNotCounted(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.filesPerDay = 6000 }) // a pack is 4 KB at least
	a.session("aaaaaa", "")
	hook := filepath.Join(remote, "hooks", "pre-receive")
	refuse := "#!/bin/sh\nwhile read old new ref; do case \"$ref\" in refs/murmur/files/*) echo no files today >&2; exit 1;; esac; done\n"
	if err := os.WriteFile(hook, []byte(refuse), 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, "one.bin", bytes.Repeat([]byte{1}, 2000))
	send := func() proto.Response {
		return a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "files", Files: []string{path}})
	}
	for try := 1; try <= 3; try++ {
		if r := send(); !strings.Contains(r.Error, "its files didn't go out") || strings.Contains(r.Error, "in the last day") {
			t.Fatalf("try %d, refused by the remote: %q", try, r.Error)
		}
	}
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if r := send(); r.Error != "" {
		t.Errorf("once the remote takes files: %s", r.Error)
	}
}

// A device keeps at most FilesHeld of files, the oldest message's going first (#739).
func TestFilesPastWhatADeviceHoldsGoOldestFirst(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.filesHeld = 3000 })
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	var paths []string
	for i, name := range []string{"old.bin", "new.bin"} {
		f := writeFile(t, name, bytes.Repeat([]byte{byte(i)}, 2000))
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: name, Files: []string{f}}); r.Error != "" {
			t.Fatalf("send %s: %s", name, r.Error)
		}
		m := inboxMessage(t, b, "bbbbbb")
		if len(m.Files) != 1 {
			t.Fatalf("%s: %+v", name, m.Files)
		}
		paths = append(paths, m.Files[0].Path)
		time.Sleep(50 * time.Millisecond) // the two copies apart in time, on a coarse clock too
	}
	eventually(t, "the old copy to go", func() bool { return !fileThere(paths[0]) })
	if !fileThere(paths[1]) {
		t.Errorf("the new copy went too")
	}
}

// A note of another device's files ref goes as soon as the ref is off the remote (#747): it used
// to wait out twice FileKeep of this daemon's running, long after its ref had gone.
func TestANoteOfAFilesRefGoesWithTheRef(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.filePrune = 300 * time.Millisecond })
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a to see bob", func() bool { return a.seesSession(bob) })
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "noted", Files: []string{writeFile(t, "a.txt", []byte("a"))}})
	if r.Error != "" {
		t.Fatalf("send: %s", r.Error)
	}
	inboxMessage(t, b, "bbbbbb")
	// ok is false while the file is being replaced, which says nothing either way.
	noted := func() (is, ok bool) {
		st, err := os.ReadFile(filepath.Join(b.gitDir, brand.StateDir, "state.json"))
		var js map[string]json.RawMessage
		if err != nil || json.Unmarshal(st, &js) != nil {
			return false, false
		}
		_, is = js["othersFiles"]
		return is, true
	}
	eventually(t, "b to note the files ref of a", func() bool { is, ok := noted(); return ok && is })
	holds(t, "the note, while the ref is on the remote", time.Second, func() bool { is, ok := noted(); return !ok || is })

	ref := strings.TrimSpace(gitOK(remote, "for-each-ref", "--format=%(refname)", "refs/murmur/files"))
	git(t, remote, "update-ref", "-d", ref) // as its own daemon does when its time is up
	eventually(t, "the note to go with the ref", func() bool { is, ok := noted(); return ok && !is })
}

// A folder above the clone or temp that can be passed through but not listed, as a shared host's
// /home of mode 711 is, doesn't stop a send (Fable's review of #768).
func TestSendReadsFilesBelowAFolderThatCantBeListed(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a folder's search-only mode is Unix's, and root ignores it")
	}
	t.Parallel()
	remote := newRemote(t)
	locked := filepath.Join(t.TempDir(), "locked")
	temp := filepath.Join(locked, "temp")
	if err := os.MkdirAll(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(temp, "shot.png")
	if err := os.WriteFile(shot, []byte("a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	a := newDeviceWith(t, remote, func(d *device) { d.tempDir = temp })
	a.session("aaaaaa", "")
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "a file", Files: []string{shot}}); r.Error != "" {
		t.Errorf("a file in temp, below a folder that can't be listed: %s", r.Error)
	}
}
