package mcpserver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of one address per T3 thread (#642): inside T3 the server takes its session's ID from the
// thread T3 names in its environment, not from the client's session under it, which changes when
// T3 moves the thread to another provider instance.

// Two servers of one thread, under the Claude sessions of two provider instances, register as one
// session, with the thread, and say they did.
func TestTheThreadGivesTheSessionItsID(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "865a81aa-aaaa-bbbb-cccc-ddddeeeeffff")
	p := helloFor("claude-code", "/work")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff")
	w := helloFor("claude-code", "/work")
	if p.ID != w.ID || p.ID != threadID("thread-1") || len(p.ID) != 6 {
		t.Errorf("the thread's servers registered as %s and %s", p.ID, w.ID)
	}
	if w.T3Thread != "thread-1" || !w.ByThread || w.ClaudeSession != "a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff" {
		t.Errorf("hello: %+v", w)
	}
	if threadID("thread-2") == p.ID {
		t.Error("two threads share an ID")
	}

	// Only Claude Code's servers take it: an OpenCode server serves many sessions, say, and can't
	// tell which thread is whose.
	if h := helloFor("opencode", "/work"); h.ByThread || h.T3Thread != "" {
		t.Errorf("opencode: %+v", h)
	}
	// A claude run from a thread's shell has the thread's variable and an agent above it: it isn't
	// the thread.
	agentAbove = func() bool { return true }
	t.Cleanup(func() { agentAbove = func() bool { return false } })
	if h := helloFor("claude-code", "/work"); h.ByThread || h.T3Thread != "" || h.ID != "a4ca6b" {
		t.Errorf("under another agent: %+v", h)
	}
	agentAbove = func() bool { return false }
	// Outside T3 (no T3 command line) the variable is someone else's, and the session is as before.
	t.Setenv("T3CODE_CLI_PATH", "")
	if h := helloFor("claude-code", "/work"); h.ByThread || h.T3Thread != "" || h.ID != "a4ca6b" {
		t.Errorf("outside T3: %+v", h)
	}
}

// startedLink runs a link for hello against f, as the server does inside T3, and waits for it to
// register.
func startedLink(t *testing.T, f *fakeDaemon, fd *finder, h proto.Hello) *link {
	t.Helper()
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l := &link{c: &client.Client{GitDir: gitDir}, hello: h, changed: make(chan struct{}), seek: currentSeek()}
	if fd != nil {
		l.find = fd.find
	}
	go l.run(ctx)
	if _, err := l.wait(ctx, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	return l
}

// A thread named before #642 has its name at an older address, which the daemon gives the server,
// and the server goes by it from then on, reconnections included.
func TestAServerTakesTheThreadsAddressFromTheDaemon(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff")
	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.ByThread {
			return "", "865a81"
		}
		return "", ""
	}}
	l := startedLink(t, f, nil, helloFor("claude-code", "/work"))
	if id, _ := l.wait(context.Background(), time.Second); id != "865a81" {
		t.Fatalf("registered as %s, not the address the daemon gave", id)
	}
	f.dropConnections()
	waitFor(t, "a reconnection", func() bool { return f.helloCount() == 2 })
	if h := f.lastHello(); h.ID != "865a81" || !h.Reconnect {
		t.Errorf("the reconnection: %+v", h)
	}

	// A server that didn't make its ID from the thread keeps its own, whatever the reply says.
	t.Setenv("T3CODE_CLI_PATH", "")
	g := &fakeDaemon{helloReply: func(proto.Hello) (string, string) { return "", "865a81" }}
	l = startedLink(t, g, nil, helloFor("claude-code", "/work"))
	if id, _ := l.wait(context.Background(), time.Second); id != "a4ca6b" {
		t.Errorf("a server outside T3 took the ID %s", id)
	}
}

// A server whose thread's address stays in use (a claude run from the thread's shell, which
// inherits the thread's variable) gives up the thread's address after a few tries and is a session
// of its own, which looks for its thread in T3's database as before.
func TestAServerWhoseThreadIsLiveIsASessionOfItsOwn(t *testing.T) {
	fastSeek(t)
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0d0d0d0d-aaaa-bbbb-cccc-ddddeeeeffff")
	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.ByThread {
			return "session ID " + h.ID + " is in use", ""
		}
		return "", ""
	}}
	fd := &finder{}
	startedLink(t, f, fd, helloFor("claude-code", "/work"))
	if h := f.lastHello(); h.ID != "0d0d0d" || h.ByThread || h.T3Thread != "" {
		t.Fatalf("registered with %+v", h)
	}
	waitFor(t, "a look in T3's database", func() bool {
		asked := fd.askedAbout()
		return len(asked) > 0 && asked[0] == "0d0d0d0d-aaaa-bbbb-cccc-ddddeeeeffff"
	})
}

// Antigravity's programs, as T3 installs them, are agents to the walk above a server: a claude run
// from an Antigravity thread's shell isn't the T3 thread whose variable it inherited (#709).
func TestAntigravityIsAnAgentAbove(t *testing.T) {
	dir := filepath.Join("home", ".t3", "tools", "antigravity-acp", "versions", "d24f4c73")
	for _, name := range []string{"agy_acp_server.exe", "localharness_external.exe", "agy_acp_server.par", "localharness_external"} {
		if exe := filepath.Join(dir, name); !agentPrograms[programName(exe)] {
			t.Errorf("%s isn't taken for an agent", exe)
		}
	}
}
