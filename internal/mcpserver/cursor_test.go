package mcpserver

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/here"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cursorLink starts a Cursor session's link against f, with named as what its hook has said.
func cursorLink(t *testing.T, f *fakeDaemon, named func() (string, bool)) *link {
	t.Helper()
	for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "T3CODE_THREAD_ID"} {
		t.Setenv(k, "")
	}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	l := &link{
		c: &client.Client{GitDir: gitDir}, hello: helloFor("cursor", "/work"),
		changed: make(chan struct{}), seek: currentSeek(), named: named,
		goBG: func(fn func()) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn()
			}()
		},
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return l
}

// shortNameWait makes a Cursor server wait for its hook only briefly.
func shortNameWait(t *testing.T, wait time.Duration) {
	w, p, l := cursorNameWait, cursorNamePoll, cursorLatePoll
	cursorNameWait, cursorNamePoll, cursorLatePoll = wait, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { cursorNameWait, cursorNamePoll, cursorLatePoll = w, p, l })
}

// A Cursor session's server registers once its hook has named the chat, by an ID made from it
// (#921): two chats at once are two sessions, and the same chat resumed in another process is the
// same one, at the same address.
func TestACursorSessionRegistersByItsChat(t *testing.T) {
	shortNameWait(t, 10*time.Second)
	var named atomicString
	f := &fakeDaemon{}
	cursorLink(t, f, named.named)
	time.Sleep(100 * time.Millisecond)
	if n := f.helloCount(); n != 0 {
		t.Fatalf("registered %d times before the hook named the chat", n)
	}
	named.set("chat-a")
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
	first := f.lastHello()
	if first.ID != nativeID("chat-a") || first.Native != "chat-a" {
		t.Errorf("hello: id %q native %q", first.ID, first.Native)
	}

	g := &fakeDaemon{}
	cursorLink(t, g, func() (string, bool) { return "chat-b", false })
	waitFor(t, "the second chat to register", func() bool { return g.helloCount() > 0 })
	if h := g.lastHello(); h.ID == first.ID || h.ID != nativeID("chat-b") {
		t.Errorf("two chats: %q and %q", first.ID, h.ID)
	}

	resumed := &fakeDaemon{}
	cursorLink(t, resumed, func() (string, bool) { return "chat-a", false })
	waitFor(t, "the resumed chat to register", func() bool { return resumed.helloCount() > 0 })
	if h := resumed.lastHello(); h.ID != first.ID {
		t.Errorf("resumed as %q, was %q", h.ID, first.ID)
	}

	// A reconnect, after the daemon dropped the connection, registers with the same ID.
	f.dropConnections()
	waitFor(t, "the reconnect", func() bool { return f.helloCount() > 1 })
	if h := f.lastHello(); h.ID != first.ID || h.Native != "chat-a" {
		t.Errorf("reconnected as %q (%q)", h.ID, h.Native)
	}
}

// A chat its hook hasn't named when the wait is up registers under an ID of its own, and a name
// that comes after keeps that ID: the server tells the daemon the chat as soon as it is named, and
// says it again in each hello.
func TestACursorSessionTheHookNeverNamed(t *testing.T) {
	shortNameWait(t, 50*time.Millisecond)
	var named atomicString
	f := &fakeDaemon{}
	cursorLink(t, f, named.named)
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
	first := f.lastHello()
	if first.Native != "" || first.ID == "" {
		t.Errorf("hello: %+v", first)
	}
	named.set("chat-late")
	waitFor(t, "the route with the chat", func() bool {
		r := f.routesTold()
		return len(r) > 0 && r[len(r)-1].Native == "chat-late"
	})
	f.dropConnections()
	waitFor(t, "the reconnect", func() bool { return f.helloCount() > 1 })
	if h := f.lastHello(); h.ID != first.ID || h.Native != "chat-late" {
		t.Errorf("reconnected as %q (%q), was %q", h.ID, h.Native, first.ID)
	}
}

// In T3 Code one process runs every Cursor thread, and a name left for it may be any of theirs: a
// server takes it only while it is its process's one server of Cursor's.
func TestNamedOnlyForTheProcesssOneServer(t *testing.T) {
	dir := t.TempDir()
	self := here.Session{ServerPID: os.Getpid(), ClientPID: os.Getppid(), ClientStarted: "t0", Agent: "cursor"}
	here.Publish(dir, self)
	if got, shared := namedOnly(dir, self); got != "" || shared {
		t.Errorf("before the hook: %q, shared %v", got, shared)
	}
	here.Name(dir, self, "chat-1", time.Now())
	if got, shared := namedOnly(dir, self); got != "chat-1" || shared {
		t.Errorf("one server: %q, shared %v", got, shared)
	}
	other := self
	other.ServerPID = os.Getppid()
	here.Publish(dir, other)
	if got, shared := namedOnly(dir, self); got != "" || !shared {
		t.Errorf("two servers: %q, shared %v", got, shared)
	}
}

// A tool call ends the wait: the call waits for the session to register, and in a headless run the
// hook that names the chat may come only after the call.
func TestAToolCallEndsACursorSessionsWait(t *testing.T) {
	shortNameWait(t, time.Hour)
	old := cursorCLI
	cursorCLI = func(int) bool { return true }
	t.Cleanup(func() { cursorCLI = old })
	for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "T3CODE_THREAD_ID"} {
		t.Setenv(k, "")
	}
	f := &fakeDaemon{}
	top, _ := joinedRepo(t, f)
	cs, _ := openWith(t, Options{Agent: "cursor", Dir: top, Poll: 25 * time.Millisecond, HereDir: t.TempDir()})
	time.Sleep(100 * time.Millisecond)
	if n := f.helloCount(); n != 0 {
		t.Fatalf("registered %d times before a call", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "who"}); err != nil {
		t.Fatalf("CallTool(who): %v", err)
	}
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
	if h := f.lastHello(); h.Native != "" {
		t.Errorf("hello: %+v", h)
	}
}

// Only a server that Cursor's command line started waits for its hook: under T3 Code or the editor,
// which run many chats in one process, it registers at once. One that waits registers by the chat
// its hook names in the per-user records.
func TestOnlyCursorsCommandLineWaitsForItsHook(t *testing.T) {
	for _, cli := range []bool{false, true} {
		t.Run(map[bool]string{false: "elsewhere", true: "cli"}[cli], func(t *testing.T) {
			shortNameWait(t, time.Hour)
			old := cursorCLI
			cursorCLI = func(int) bool { return cli }
			t.Cleanup(func() { cursorCLI = old })
			for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "T3CODE_THREAD_ID"} {
				t.Setenv(k, "")
			}
			f := &fakeDaemon{}
			top, _ := joinedRepo(t, f)
			hdir := t.TempDir()
			openWith(t, Options{Agent: "cursor", Dir: top, Poll: 25 * time.Millisecond, HereDir: hdir})
			if !cli {
				waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
				return
			}
			var rec here.Session
			waitFor(t, "the server's record", func() (ok bool) { rec, ok = mine(t, hdir); return })
			time.Sleep(100 * time.Millisecond)
			if n := f.helloCount(); n != 0 {
				t.Fatalf("registered %d times before the hook named the chat", n)
			}
			if err := here.Name(hdir, rec, "chat-cli", time.Now()); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
			if h := f.lastHello(); h.ID != nativeID("chat-cli") || h.Native != "chat-cli" {
				t.Errorf("hello: id %q native %q", h.ID, h.Native)
			}
		})
	}
}

// One of several Cursor servers in its process registers at once: no hook can name its chat.
func TestACursorSessionOfASharedProcessDoesntWait(t *testing.T) {
	shortNameWait(t, time.Hour)
	f := &fakeDaemon{}
	cursorLink(t, f, func() (string, bool) { return "", true })
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
}

// atomicString is a string a test sets while a link reads it.
type atomicString struct {
	mu sync.Mutex
	s  string
}

func (a *atomicString) get() string  { a.mu.Lock(); defer a.mu.Unlock(); return a.s }
func (a *atomicString) set(s string) { a.mu.Lock(); a.s = s; a.mu.Unlock() }

func (a *atomicString) named() (string, bool) { return a.get(), false }
