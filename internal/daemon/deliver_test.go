package daemon_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/testgit"
)

// Tests of handing a message over to a session as a turn (#57). `t3` is a stand-in: the test
// binary itself, put in a test's own directory as fakeT3Name and run from there as
// `t3 thread send <thread> -`, which records what it was asked.

// fakeT3Name is the stand-in's file name, less the extension Windows needs: TestMain runs the
// stand-in instead of the tests when the binary was started under this name.
const fakeT3Name = "fake-t3"

// The stand-in is told what to do by files beside it, not by the environment, so that each test
// has its own and can call t.Parallel (#282): an environment variable can't be set in a parallel
// test, and one test's would reach the stand-in another test started. fakeT3Calls is the file it
// appends its calls to; fakeT3Fail, if there, makes it fail; fakeT3Sleep holds a duration it takes,
// as a `t3` that is busy.
const (
	fakeT3Calls = "calls.jsonl"
	fakeT3Fail  = "fail"
	fakeT3Sleep = "sleep"
)

func TestMain(m *testing.M) {
	// By the path it was started at when the executable's can't be read (Linux without /proc), so a
	// stand-in never runs the whole suite instead: the daemon starts it by that path (the route's T3CLI).
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	if strings.TrimSuffix(filepath.Base(exe), filepath.Ext(exe)) == fakeT3Name {
		os.Exit(fakeT3(filepath.Dir(exe)))
	}
	// The daemon reads conversation titles from the clients' state under the user's home (#66): in
	// tests that is an empty directory, so nothing here reads the real ~/.t3, ~/.codex or ~/.claude.
	home, err := os.MkdirTemp("", "daemon-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	os.Setenv("HOME", home)
	os.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	// The hook looks for its session's MCP server among the records of this user's (#422): none
	// here, so it finds its clone from its working directory, and never a real session's.
	os.Setenv(brand.SessionsDirEnv, filepath.Join(home, "sessions"))
	testgit.Isolate() // HOME keeps out ~/.gitconfig, but not the system's, or a GIT_CONFIG_GLOBAL set already
	started := time.Now()
	code := m.Run()
	os.RemoveAll(home)
	if path := os.Getenv("MURMUR_GIT_STATS"); path != "" && gitStats.ran() { // not for `go test -list`
		// How git did over the whole run, failing or not, added to the file it names (#513). go test
		// shows a passing package's output only with -v, so CI prints the file in a step of its own.
		if f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "git in a test run of %s: %s\n", time.Since(started).Round(time.Second), gitStats.summary(24*time.Hour))
			f.Close()
		}
	}
	os.Exit(code)
}

// t3Call is one run of the stand-in `t3`, which also stands in for `codex` and `opencode` (#135).
type t3Call struct {
	Args      []string `json:"args"`
	Stdin     string   `json:"stdin"`
	Home      string   `json:"home"`
	CodexHome string   `json:"codexHome"`
}

// fakeT3 is the stand-in `t3`, run from dir: it records its arguments, standard input, T3CODE_HOME
// and CODEX_HOME, and takes its time or fails if told to. It reads what it is told before it records
// the call, as a process reads its environment when it starts, so a test that changes the settings
// once it has seen the call changes only the calls after it.
func fakeT3(dir string) int {
	var sleep time.Duration
	if s, err := os.ReadFile(filepath.Join(dir, fakeT3Sleep)); err == nil {
		sleep, _ = time.ParseDuration(strings.TrimSpace(string(s)))
	}
	_, err := os.Stat(filepath.Join(dir, fakeT3Fail))
	fail := err == nil
	in, _ := io.ReadAll(os.Stdin)
	line, _ := json.Marshal(t3Call{Args: os.Args[1:], Stdin: string(in), Home: os.Getenv("T3CODE_HOME"), CodexHome: os.Getenv("CODEX_HOME")})
	f, err := os.OpenFile(filepath.Join(dir, fakeT3Calls), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 3
	}
	f.Write(append(line, '\n'))
	f.Close()
	time.Sleep(sleep)
	if fail {
		os.Stdout.WriteString("no thread with that id\nsecond line\n")
		return 1
	}
	return 0
}

// useFakeT3 puts a stand-in `t3` in a directory of the test's own. It returns the path to put in a
// route as T3CLI, and what the stand-in has been asked so far.
func useFakeT3(t *testing.T) (exe string, calls func() []t3Call) {
	t.Helper()
	exe, calls = fakeT3Path(t)
	installFakeT3(t, exe)
	return exe, calls
}

// fakeT3Path is where a test's stand-in goes, in a new directory, and what the stand-in there has
// been asked so far. Nothing is there yet: installFakeT3 puts it there.
func fakeT3Path(t *testing.T) (exe string, calls func() []t3Call) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, fakeT3Name)
	if runtime.GOOS == "windows" {
		exe += ".exe" // the only kind of file Windows runs as a program
	}
	path := filepath.Join(dir, fakeT3Calls)
	return exe, func() []t3Call {
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		var out []t3Call
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			var c t3Call
			if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
				t.Errorf("a call of the stand-in t3: %v", err)
				continue
			}
			out = append(out, c)
		}
		return out
	}
}

// installFakeT3 puts the test binary at exe, whole or not at all: it is made beside exe and renamed
// into place, so a daemon that runs exe meanwhile never finds half a command. A hard link costs
// nothing where it can be made; elsewhere, as across volumes, the binary is copied. On Windows it
// is always copied: a hard link would be another name of the running test binary, and Windows
// won't remove a program's file while it runs, by any of its names, so the test's directory
// couldn't be cleaned up.
func installFakeT3(t *testing.T, exe string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tmp := exe + ".tmp"
	if runtime.GOOS == "windows" || os.Link(self, tmp) != nil {
		if err := copyFile(self, tmp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		t.Fatal(err)
	}
}

// copyFile copies src to a new file dst that can be run.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// setFakeT3 changes one of the settings (fakeT3Fail, fakeT3Sleep) of the stand-in at exe, for the
// calls that start after it; an empty value takes it away. The file is written whole, by renaming,
// so a stand-in that starts meanwhile reads the old value or the new one.
func setFakeT3(t *testing.T, exe, setting, value string) {
	t.Helper()
	path := filepath.Join(filepath.Dir(exe), setting)
	if value == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(path+".tmp", []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

// sessionRoute registers a session whose MCP server found out how to reach its client.
func (d *device) sessionRoute(id, claude string, r proto.Route) (string, net.Conn) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "test", ClaudeSession: claude, Route: r})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn
}

// send sends a message from session id, and says what the daemon answered.
func (d *device) send(id, to, subject, body string) proto.Response {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpSend, Session: id, To: to, Subject: subject, Body: body})
	if r.Error != "" {
		d.t.Fatalf("send to %s: %s", to, r.Error)
	}
	return r
}

// unread is what a session still has unread: its inbox and what the hook would say, as subjects.
func (d *device) unread(id, claude string) (inbox, notices []string) {
	for _, m := range d.do(proto.Request{Op: proto.OpNotices, ClaudeSession: claude}).Messages {
		notices = append(notices, m.Subject)
	}
	for _, m := range d.do(proto.Request{Op: proto.OpInbox, Session: id}).Messages {
		inbox = append(inbox, m.Subject)
	}
	return inbox, notices
}

// A message for a session in T3 runs `t3 thread send <thread> -` with the text on standard input,
// once, and is no longer unread: not in the inbox, not in the hook's notices.
func TestMessageForAT3SessionIsHandedOverAsATurn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Home: "/t3/home", T3Thread: "thread-bob"})
	aliceName := a.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	body := "Are you changing it?\nI say \"yes\" — and 100% über-sure.\n\n  indented"
	a.send("aaaaaa", bob, "auth middleware?", body)
	eventually(t, "the handoff", func() bool { return len(calls()) > 0 })

	c := calls()[0]
	if strings.Join(c.Args, " ") != "thread send thread-bob -" {
		t.Errorf("t3 was run as %q, want thread send thread-bob -", c.Args)
	}
	if c.Home != "/t3/home" {
		t.Errorf("T3CODE_HOME was %q", c.Home)
	}
	for _, want := range []string{
		"Message from " + aliceName + " (test",
		"via " + brand.Product + ":\n\nauth middleware?\n\n" + body + "\n",
		"Reply with " + brand.Product + "'s `send` to " + aliceName + " if a reply is needed.",
	} {
		if !strings.Contains(c.Stdin, want) {
			t.Errorf("the turn lacks %q:\n%s", want, c.Stdin)
		}
	}

	eventually(t, "the message to be marked read", func() bool { return strings.Contains(out.String(), "as a turn") })
	holds(t, "one handoff, and nothing unread", 1500*time.Millisecond, func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0 && len(calls()) == 1
	})
	if !logHas(b, "handed \"auth middleware?\"") {
		t.Errorf("the log doesn't say the message was handed over: %+v", b.do(proto.Request{Op: proto.OpLog, Limit: 50}).Events)
	}
}

// Messages for one session go in one at a time, in the order they came, and each once.
func TestMessagesForOneSessionAreHandedOverInOrder(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	want := []string{"one", "two", "three"}
	for _, s := range want {
		a.send("aaaaaa", bob, s, "")
	}
	eventually(t, "three handoffs", func() bool { return len(calls()) >= 3 })
	holds(t, "three handoffs, no more", time.Second, func() bool { return len(calls()) == 3 })
	for i, c := range calls() {
		if !strings.Contains(c.Stdin, "via "+brand.Product+":\n\n"+want[i]+"\n") {
			t.Errorf("handoff %d was %q, want %q", i, c.Stdin, want[i])
		}
	}
}

// Only a session's first turn from a sender says what agent the sender is and whose machine it is
// on; later ones name it alone (#216). Another sender is introduced in its own first turn.
func TestASenderIsDescribedOnlyInItsFirstTurn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	carol, _ := a.session("cccccc", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	aliceName, carolName := a.named(alice), a.named(carol)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) && b.seesSession(carol) })

	a.send("aaaaaa", bob, "one", "")
	eventually(t, "the first handoff", func() bool { return len(calls()) >= 1 })
	a.send("aaaaaa", bob, "two", "")
	eventually(t, "the second handoff", func() bool { return len(calls()) >= 2 })
	a.send("cccccc", bob, "three", "")
	eventually(t, "the third handoff", func() bool { return len(calls()) >= 3 })

	for i, want := range []string{
		"Message from " + aliceName + " (test",
		"Message from " + aliceName + " via " + brand.Product + ":\n\ntwo\n",
		"Message from " + carolName + " (test",
	} {
		if c := calls()[i]; !strings.HasPrefix(c.Stdin, want) {
			t.Errorf("handoff %d was %q, want it to start %q", i, c.Stdin, want)
		}
	}
}

// A session that isn't in T3 and has no messaging socket can't be put a turn into, so its
// messages are the hook's notices, as they were, and the daemon says so once.
func TestWithNoWayInMessagesStayNotices(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	_, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "body one")
	a.send("aaaaaa", bob, "second", "body two")
	var notices []string
	eventually(t, "both notices", func() bool {
		notices = append(notices, b.hook("claude-bob", 0)...)
		return len(notices) >= 2
	})
	holds(t, "no handoff", 500*time.Millisecond, func() bool { return len(calls()) == 0 })
	if got := inboxOf(b, "bbbbbb"); len(got) != 2 {
		t.Errorf("the inbox keeps what the hook announced: %q", got)
	}
	if n := strings.Count(out.String(), "has no way to take a message as a turn"); n != 1 {
		t.Errorf("the log says it %d times, want once:\n%s", n, out.String())
	}
}

// A failed handoff is left to the hook's notice, which still shows the message, and the log says so
// once per session however many messages fail. It is not tried again: the failure may have come
// after T3 took the message (see deliver_repeat_test.go).
func TestFailedHandoffFallsBackToTheNotice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Fail, "1")
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "body one")
	eventually(t, "the attempt", func() bool { return len(calls()) >= 1 })
	var notices []string
	eventually(t, "the notice", func() bool {
		notices = append(notices, b.hook("claude-bob", 0)...)
		return len(notices) > 0
	})
	if notices[0] != "first" {
		t.Fatalf("notices: %q", notices)
	}
	a.send("aaaaaa", bob, "second", "body two")
	eventually(t, "an attempt more", func() bool { return len(calls()) >= 2 })
	eventually(t, "the second notice", func() bool {
		n := b.hook("claude-bob", 0)
		return len(n) > 0 && n[0] == "second"
	})
	holds(t, "one attempt for each message", 800*time.Millisecond, func() bool { return len(calls()) == 2 })

	if n := strings.Count(out.String(), "as a turn through T3 failed"); n != 1 {
		t.Errorf("the log says it %d times, want once:\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "no thread with that id") || strings.Contains(out.String(), "second line") {
		t.Errorf("the log should carry t3's first line of output, and only that:\n%s", out.String())
	}
	if strings.Contains(out.String(), "body one") {
		t.Errorf("the log has a message's text:\n%s", out.String())
	}
}

// The page's wake setting "never" keeps messages as notices, for the device or for one session; a
// message that waited is handed over when the setting is lifted.
func TestWakeNeverKeepsTheNotice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	pg := b.page()
	put := func(settings string) {
		t.Helper()
		if code, body := pg.call("PUT", "/api/settings", pg.token, settings); code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", settings, code, body)
		}
	}
	put(`{"wake":{"when":"never"}}`)
	a.send("aaaaaa", bob, "held", "")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "held") })
	holds(t, "no handoff under never", time.Second, func() bool { return len(calls()) == 0 })

	// One session allowed while the device says never.
	put(`{"wake":{"when":"never"},"sessionWake":{"` + bobName + `":{"when":"always"}}}`)
	eventually(t, "handoff for the session that may be woken", func() bool { return len(calls()) == 1 })
	if !strings.Contains(calls()[0].Stdin, "held") {
		t.Errorf("the turn handed over: %q", calls()[0].Stdin)
	}

	// The device allowed while this session says never.
	put(`{"wake":{"when":"always"},"sessionWake":{"` + bobName + `":{"when":"never"}}}`)
	a.send("aaaaaa", bob, "held again", "")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "held again") })
	holds(t, "no handoff to a session that says never", time.Second, func() bool { return len(calls()) == 1 })
	if n := b.hook("claude-bob", 0); len(n) != 1 || n[0] != "held again" {
		t.Errorf("notices: %q", n)
	}
}

// A session of T3 whose MCP server has stopped (T3 stops an idle thread's after 30 minutes) is
// still reached, through its thread, which the daemon remembers; a restart of the daemon doesn't
// forget it.
func TestMessageForAStoppedT3SessionStillReachesItsThread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	alice, _ := a.session("aaaaaa", "")
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 && !a.listsSession(bob) })
	a.send("aaaaaa", bob, "while you were away", "wake up")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-bob -" || !strings.Contains(c.Stdin, "wake up") {
		t.Errorf("t3 was run as %q with %q", c.Args, c.Stdin)
	}
	// The stand-in notes its call when it starts; the message leaves the inbox when it has ended. A
	// restart before that would find the message marked Attempted and leave it to the notice, not
	// hand it over again; this test waits for the handoff to end so as not to test that case.
	eventually(t, "the first handoff to finish", func() bool { return logHas(b, `handed "while you were away"`) })

	b.restart()
	a.send("aaaaaa", bob, "and again", "still there?")
	eventually(t, "the handoff after a restart", func() bool { return len(calls()) == 2 })
	if c := calls()[1]; strings.Join(c.Args, " ") != "thread send thread-bob -" || !strings.Contains(c.Stdin, "still there?") {
		t.Errorf("t3 was run as %q with %q", c.Args, c.Stdin)
	}

	// Back, it finds nothing unread: both messages went in as turns.
	if _, err := b.restartedMCP("bbbbbb", ""); err != nil {
		t.Fatal(err)
	}
	if inbox, _ := b.unread("bbbbbb", ""); len(inbox) != 0 {
		t.Errorf("inbox: %q", inbox)
	}
}

// A session's name follows its T3 thread: a new MCP server in the same thread, with another
// Claude session ID (/clear, a restart, another account), has the name the thread had.
func TestNameFollowsTheT3Thread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	first, conn := b.sessionRoute("bbbbbb", "claude-1", route)
	name := b.named(first)
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.sessionRoute("cccccc", "claude-2", route)
	b.nameIs(second, name)

	// Another thread is another name.
	third, _ := b.sessionRoute("dddddd", "claude-3", proto.Route{T3CLI: exe, T3Thread: "thread-carol"})
	if other := b.named(third); other == name {
		t.Errorf("two threads share the name %s", name)
	}
}

// A session that finds its thread after it registered (the MCP server reads T3's database in the
// background) keeps its name, and from then on takes messages as turns, including one that came
// while the thread was unknown.
func TestSessionThatFindsItsThreadLaterIsHandedItsMessages(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe})
	name := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "early", "")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "early") })
	holds(t, "no handoff without a thread", 500*time.Millisecond, func() bool { return len(calls()) == 0 })

	r := b.do(proto.Request{Op: proto.OpRoute, Session: "bbbbbb", Route: &proto.Route{T3CLI: exe, T3Thread: "thread-bob"}})
	if r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	eventually(t, "the early message to be handed over", func() bool { return len(calls()) == 1 })
	b.nameIs(bob, name)
	a.send("aaaaaa", bob, "late", "")
	eventually(t, "the late message to be handed over", func() bool { return len(calls()) == 2 })
}

// Where a session can be reached is local: the thread, the T3 path and above all the messaging
// token are in state.json at most (the thread, so that a message can still find it), and never in
// the log, on the page or on the bus.
func TestRouteStaysOnTheDevice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	const token, thread = "TOKEN-do-not-leak-0123", "thread-do-not-leak-4567"
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{
		T3CLI: exe, T3Thread: thread, MsgSocket: "/run/leak-socket", MsgToken: token})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	a.send("aaaaaa", bob, "hello", "there")
	b.send("bbbbbb", alice, "reply", "right back")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })
	eventually(t, "the reply", func() bool { return logHas(a, "reply") })
	time.Sleep(time.Second) // presence and logs go out on the next syncs

	secrets := []string{token, thread, "leak-socket"}
	pg := b.page()
	_, state := pg.call("GET", "/api/state", pg.token, "")
	_, msgs := pg.call("GET", "/api/messages", pg.token, "")
	bus := git(t, remote, "log", "--all", "--format=%B")
	for what, text := range map[string]string{"the log": out.String(), "the page's state": state, "the page's messages": msgs,
		"the bus": bus, "who": mustJSON(t, b.do(proto.Request{Op: proto.OpWho})), "the daemon's events": mustJSON(t, b.do(proto.Request{Op: proto.OpLog, Limit: 100}))} {
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s has %q", what, s)
			}
		}
	}
	if len(bus) == 0 {
		t.Error("the bus is empty, so the check above proved nothing")
	}
	st, err := os.ReadFile(filepath.Join(b.gitDir, brand.StateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st), token) {
		t.Errorf("state.json has the messaging token")
	}
	if !strings.Contains(string(st), thread) {
		t.Errorf("state.json doesn't keep the thread, so a stopped session couldn't be reached")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
