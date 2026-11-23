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

	// Only Claude Code's and Antigravity's servers take it: an OpenCode server serves many
	// sessions, say, and can't tell which thread is whose.
	if h := helloFor("opencode", "/work"); h.ByThread || h.T3Thread != "" {
		t.Errorf("opencode: %+v", h)
	}
	// A claude run from a thread's shell has the thread's variable and an agent above it: it isn't
	// the thread.
	agentAbove = func(string) bool { return true }
	t.Cleanup(func() { agentAbove = func(string) bool { return false } })
	if h := helloFor("claude-code", "/work"); h.ByThread || h.T3Thread != "" || h.ID != sessionID("a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff") {
		t.Errorf("under another agent: %+v", h)
	}
	agentAbove = func(string) bool { return false }
	// With no T3 command line and not started by a t3 (servingT3, as TestMain sets it), the variable
	// is only inherited, and the session is as before.
	t.Setenv("T3CODE_CLI_PATH", "")
	if h := helloFor("claude-code", "/work"); h.ByThread || h.T3Thread != "" || h.ID != sessionID("a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff") {
		t.Errorf("outside T3: %+v", h)
	}
}

// Antigravity threads in T3 also receive T3CODE_THREAD_ID and register by the thread when no other
// agent runs above them, without taking Claude Code's session or messaging socket variables (#886).
func TestAntigravityTakesTheThreadFromTheEnvironment(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	t.Setenv(threadEnv, "thread-agy-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "865a81aa-aaaa-bbbb-cccc-ddddeeeeffff")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok-1")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")

	first := helloFor("antigravity", "/work")
	second := helloFor("antigravity", "/work")
	if first.ID != second.ID || first.ID != threadID("thread-agy-1") || len(first.ID) != 6 {
		t.Errorf("restarts registered as %s and %s, want %s", first.ID, second.ID, threadID("thread-agy-1"))
	}
	if first.T3Thread != "thread-agy-1" || !first.ByThread || first.T3CLI != "/t3/bin/t3" || first.T3Home != "/home/u/.t3" {
		t.Errorf("route: %+v, byThread=%v", first.Route, first.ByThread)
	}
	if first.Agent != "antigravity in T3" {
		t.Errorf("agent = %q, want antigravity in T3", first.Agent)
	}
	if first.ClaudeSession != "" || first.MsgSocket != "" || first.MsgToken != "" {
		t.Errorf("took Claude Code variables: %+v", first)
	}

	// If ANTIGRAVITY_CONVERSATION_ID is also present, Native is kept while the thread gives the ID.
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "conv-agy-1")
	withConv := helloFor("antigravity", "/work")
	if withConv.ID != threadID("thread-agy-1") || withConv.T3Thread != "thread-agy-1" || !withConv.ByThread || withConv.Native != "conv-agy-1" {
		t.Errorf("with conversation ID: %+v", withConv)
	}

	// An agy run from another agent's shell (claude or agy) has an agent above it: it must not
	// take the enclosing T3 thread's identity.
	agentAbove = func(string) bool { return true }
	t.Cleanup(func() { agentAbove = func(string) bool { return false } })
	if nested := helloFor("antigravity", "/work"); nested.ByThread || nested.T3Thread != "" || nested.ID != nativeID("conv-agy-1") {
		t.Errorf("nested with conversation ID: %+v", nested)
	}
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
	if nested := helloFor("antigravity", "/work"); nested.ByThread || nested.T3Thread != "" || nested.ID == threadID("thread-agy-1") {
		t.Errorf("nested without conversation ID: %+v", nested)
	}
	agentAbove = func(string) bool { return false }

	// Outside T3 (no T3CODE_CLI_PATH and not started by servingT3), T3CODE_THREAD_ID is ignored.
	t.Setenv("T3CODE_CLI_PATH", "")
	if outside := helloFor("antigravity", "/work"); outside.ByThread || outside.T3Thread != "" || outside.T3CLI != "" || outside.Agent != "antigravity" {
		t.Errorf("outside T3: %+v", outside)
	}
}

// Kiro threads in T3 inherit T3CODE_THREAD_ID, T3CODE_CLI_PATH and T3CODE_HOME in their stdio
// MCP servers and register by thread as "kiro in T3" (#908, #909).
func TestKiroTakesTheThreadFromTheEnvironment(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	t.Setenv(threadEnv, "thread-kiro-1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "865a81aa-aaaa-bbbb-cccc-ddddeeeeffff")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok-1")

	first := helloFor("kiro", "/work")
	second := helloFor("kiro", "/work")
	if first.ID != second.ID || first.ID != threadID("thread-kiro-1") || len(first.ID) != 6 {
		t.Errorf("restarts registered as %s and %s, want %s", first.ID, second.ID, threadID("thread-kiro-1"))
	}
	if first.T3Thread != "thread-kiro-1" || !first.ByThread || first.T3CLI != "/t3/bin/t3" || first.T3Home != "/home/u/.t3" {
		t.Errorf("route: %+v, byThread=%v", first.Route, first.ByThread)
	}
	if first.Agent != "kiro in T3" {
		t.Errorf("agent = %q, want kiro in T3", first.Agent)
	}
	if first.ClaudeSession != "" || first.MsgSocket != "" || first.MsgToken != "" {
		t.Errorf("took Claude Code variables: %+v", first)
	}

	// A kiro-cli started from another agent's shell must not take the enclosing T3 thread.
	agentAbove = func(string) bool { return true }
	t.Cleanup(func() { agentAbove = func(string) bool { return false } })
	if nested := helloFor("kiro", "/work"); nested.ByThread || nested.T3Thread != "" || nested.ID == threadID("thread-kiro-1") {
		t.Errorf("nested kiro: %+v", nested)
	}
	agentAbove = func(string) bool { return false }

	// Outside T3 (no T3CODE_CLI_PATH and not started by servingT3), T3CODE_THREAD_ID is ignored.
	t.Setenv("T3CODE_CLI_PATH", "")
	if outside := helloFor("kiro", "/work"); outside.ByThread || outside.T3Thread != "" || outside.T3CLI != "" || outside.Agent != "kiro" {
		t.Errorf("outside T3: %+v", outside)
	}
}

// startedLink runs a link for hello against f, as the server does inside T3, and waits for it to
// register.
func startedLink(t *testing.T, f *fakeDaemon, fd *finder, h proto.Hello) *link {
	t.Helper()
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l := &link{c: &client.Client{GitDir: gitDir}, hello: h, native: h.Native, changed: make(chan struct{}), seek: currentSeek()}
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
	if id, _ := l.wait(context.Background(), time.Second); id != sessionID("a4ca6bbb-aaaa-bbbb-cccc-ddddeeeeffff") {
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
	if h := f.lastHello(); h.ID != sessionID("0d0d0d0d-aaaa-bbbb-cccc-ddddeeeeffff") || h.ByThread || h.T3Thread != "" {
		t.Fatalf("registered with %+v", h)
	}
	waitFor(t, "a look in T3's database", func() bool {
		asked := fd.askedAbout()
		return len(asked) > 0 && asked[0] == "0d0d0d0d-aaaa-bbbb-cccc-ddddeeeeffff"
	})
}

// The walk above a server takes a claude or agy run from an Antigravity thread's shell for one
// with an agent above it (#709 for Antigravity 1.4.5's agy_acp_server/localharness_external, #889
// for 1.5.13's agy). A T3 thread's own claude or agy has none.
func TestAgentInChain(t *testing.T) {
	t3 := filepath.Join("home", ".t3", "tools", "antigravity-acp", "win32-x64", "versions", "d24f4c73")
	for what, c := range map[string]struct {
		chain []string // the client first, then its parents
		want  bool
	}{
		"claude from Antigravity 1.4.5's shell, Windows": {[]string{"claude.exe", "powershell.exe",
			filepath.Join(t3, "localharness_external.exe"), filepath.Join(t3, "agy_acp_server.exe"),
			filepath.Join(t3, "agy_acp_server.exe"), "T3 Code (Beta).exe"}, true},
		"claude from Antigravity 1.4.5's shell, Linux": {[]string{"claude", "bash",
			filepath.Join(t3, "localharness_external"), filepath.Join(t3, "agy_acp_server.par")}, true},
		"claude from Antigravity 1.5.13's shell, Windows": {[]string{"claude.exe", "powershell.exe",
			filepath.Join(t3, "agy.exe"), "T3 Code (Beta).exe"}, true},
		"claude from Antigravity 1.5.13's shell, Linux": {[]string{"claude", "bash",
			filepath.Join(t3, "agy"), "t3"}, true},
		"agy from Antigravity 1.5.13's shell, Windows": {[]string{"agy.exe", "powershell.exe",
			filepath.Join(t3, "agy.exe"), "T3 Code (Beta).exe"}, true},
		"agy from a Claude thread's shell":           {[]string{"agy", "bash", "claude", "node"}, true},
		"a T3 thread's own agy, Windows":             {[]string{filepath.Join(t3, "agy.exe"), "T3 Code (Beta).exe"}, false},
		"a headless T3 thread's own agy":             {[]string{"agy", "t3"}, false},
		"a T3 thread's own claude":                   {[]string{"claude", "node", "T3 Code (Beta).exe"}, false},
		"claude from a thread's shell":               {[]string{"claude", "bash", "claude", "node"}, true},
		"an unreadable parent":                       {[]string{"claude", ""}, false},
		"the client alone":                           {[]string{"claude"}, false},
		"claude from a Kiro thread's shell, Windows": {[]string{"claude.exe", "powershell.exe", "node.exe", "kiro-cli.exe", "T3 Code (Beta).exe"}, true},
		"claude from a Kiro thread's shell, Linux":   {[]string{"claude", "bash", "node", "kiro-cli", "t3"}, true},
	} {
		got := agentInChain(len(c.chain), func(i int) (string, string, bool) {
			return c.chain[i], "", c.chain[i] != ""
		})
		if got != c.want {
			t.Errorf("%s: %v, want %v", what, got, c.want)
		}
	}
}

// Kiro CLI 3.4.0 spawns node (acp-server.js), which spawns mur mcp --agent kiro. Kiro's own
// kiro-cli launcher above node must not look like an enclosing agent, while an agent above
// kiro-cli still does, and servingT3Parent looks past kiro-cli to find headless t3.
func TestKiroProcessChain(t *testing.T) {
	for what, c := range map[string]struct {
		chain []string
		want  bool
	}{
		"a T3 thread's own kiro, Windows":        {[]string{"node.exe", "kiro-cli.exe", "T3 Code (Beta).exe"}, false},
		"a headless T3 thread's own kiro, Linux": {[]string{"node", "kiro-cli", "t3"}, false},
		"kiro from a Claude thread's shell":      {[]string{"node.exe", "kiro-cli.exe", "powershell.exe", "claude.exe", "T3 Code (Beta).exe"}, true},
		"kiro from a Kiro thread's shell":        {[]string{"node.exe", "kiro-cli.exe", "powershell.exe", "node.exe", "kiro-cli.exe", "T3 Code (Beta).exe"}, true},
	} {
		got := agentInChainFor("kiro", len(c.chain), func(i int) (string, string, bool) {
			return c.chain[i], "", c.chain[i] != ""
		})
		if got != c.want {
			t.Errorf("%s: agentInChainFor = %v, want %v", what, got, c.want)
		}
	}

	parent, ok := servingT3Parent("kiro", 3, func(i int) (string, string, bool) {
		chain := []string{"node", "kiro-cli", "/usr/local/bin/t3"}
		return chain[i], "", true
	})
	if !ok || parent != "/usr/local/bin/t3" {
		t.Errorf("servingT3Parent(kiro) = (%q, %v), want (/usr/local/bin/t3, true)", parent, ok)
	}
}

// An Antigravity server whose thread's address stays in use drops ByThread and falls back to its
// native ID (when known), looking up its own thread in T3's database.
func TestAnAntigravityServerWhoseThreadIsLiveFallsBackToNative(t *testing.T) {
	fastSeek(t)
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "conv-nested-agy")
	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.ByThread {
			return "session ID " + h.ID + " is in use", ""
		}
		return "", ""
	}}
	fd := &finder{}
	startedLink(t, f, fd, helloFor("antigravity", "/work"))
	if h := f.lastHello(); h.ID != nativeID("conv-nested-agy") || h.ByThread || h.T3Thread != "" {
		t.Fatalf("registered with %+v", h)
	}
	waitFor(t, "a look in T3's database", func() bool {
		asked := fd.askedAbout()
		return len(asked) > 0 && asked[0] == "conv-nested-agy"
	})
}
