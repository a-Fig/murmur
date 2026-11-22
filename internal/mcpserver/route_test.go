package mcpserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/title"
)

// Tests of how the server finds out how to reach its client, for the daemon to hand messages over as
// turns (#57): the environment, and inside T3 the thread, found in T3's database.

// The hello carries what the environment says about reaching the client, and nothing where it
// says nothing.
func TestHelloCarriesTheRouteFromTheEnvironment(t *testing.T) {
	for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		t.Setenv(k, "")
	}
	if r := helloFor("claude-code", "/work").Route; r != (proto.Route{}) {
		t.Errorf("a bare environment gave %+v", r)
	}
	t.Setenv("T3CODE_CLI_PATH", "/home/u/.t3/bin/t3")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok-1")
	want := proto.Route{T3CLI: "/home/u/.t3/bin/t3", T3Home: "/home/u/.t3", MsgSocket: "/tmp/claude.sock", MsgToken: "tok-1"}
	if r := helloFor("claude-code", "/work").Route; r != want {
		t.Errorf("route %+v, want %+v", r, want)
	}
}

// T3's headless server (`t3 serve`) sets no T3CODE_CLI_PATH, only the thread's ID and T3CODE_HOME
// (#780). Its Claude Code session is still T3's: the route has the `t3` that serves, which the
// daemon uses ahead of the messaging socket, as it does in T3's app. With no `t3` to be found it
// gets no socket, which starts no turn there, so its messages wait as notices rather than being
// taken as read.
func TestHeadlessT3IsFound(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok-1")
	t.Cleanup(func() { servingT3 = func() (string, bool) { return "", false } })
	servingT3 = func() (string, bool) { return "/home/u/.local/bin/t3", true }
	h := helloFor("claude-code", "/work")
	if want := (proto.Route{T3CLI: "/home/u/.local/bin/t3", T3Home: "/home/u/.t3", T3Thread: "thread-1",
		MsgSocket: "/tmp/claude.sock", MsgToken: "tok-1"}); h.Route != want {
		t.Errorf("route %+v, want %+v", h.Route, want)
	}
	if !h.ByThread || h.Agent != "claude-code in T3" {
		t.Errorf("hello %+v: want it registered by its thread, as in T3", h)
	}

	servingT3 = func() (string, bool) { return "", true }
	h = helloFor("claude-code", "/work")
	if h.Route.MsgSocket != "" || h.Route.T3CLI != "" || h.ByThread || h.Agent != "claude-code" {
		t.Errorf("with no t3 to be found: %+v, want no socket, no T3 command line, and not by its thread", h)
	}

	// Only Claude Code's thread is looked for: another agent with the variable keeps its own route.
	servingT3 = func() (string, bool) { return "/home/u/.local/bin/t3", true }
	t.Setenv("MURMUR_PI_MESSAGING_SOCKET", "/tmp/pi.sock")
	t.Setenv("MURMUR_PI_MESSAGING_TOKEN", "tok-2")
	if h := helloFor("pi", "/work"); h.Route.T3CLI != "" || h.Route.MsgSocket != "/tmp/pi.sock" || h.Agent != "pi" {
		t.Errorf("pi with the thread's variable: %+v", h)
	}

	// A claude run from the thread's shell isn't the thread: it keeps its own socket, and isn't T3's.
	agentAbove = func() bool { return true }
	t.Cleanup(func() { agentAbove = func() bool { return false } })
	if h := helloFor("claude-code", "/work"); h.Route.MsgSocket != "/tmp/claude.sock" || h.Route.T3CLI != "" || h.ByThread {
		t.Errorf("under the thread's agent: %+v", h)
	}
}

// Antigravity under T3's headless server (`t3 serve`) has T3CODE_THREAD_ID and T3CODE_HOME without
// T3CODE_CLI_PATH (#886): it finds the serving `t3` like Claude Code does, without taking Claude
// Code's messaging socket.
func TestHeadlessT3AntigravityIsFound(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	t.Setenv(threadEnv, "thread-agy-1")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok-1")
	t.Cleanup(func() { servingT3 = func() (string, bool) { return "", false } })
	servingT3 = func() (string, bool) { return "/home/u/.local/bin/t3", true }
	h := helloFor("antigravity", "/work")
	if want := (proto.Route{T3CLI: "/home/u/.local/bin/t3", T3Home: "/home/u/.t3", T3Thread: "thread-agy-1"}); h.Route != want {
		t.Errorf("route %+v, want %+v", h.Route, want)
	}
	if !h.ByThread || h.ID != threadID("thread-agy-1") || h.Agent != "antigravity in T3" {
		t.Errorf("hello %+v: want registered by thread as antigravity in T3", h)
	}

	servingT3 = func() (string, bool) { return "", true }
	h = helloFor("antigravity", "/work")
	if h.Route.T3CLI != "" || h.Route.T3Thread != "" || h.Route.MsgSocket != "" || h.ByThread || h.Agent != "antigravity" {
		t.Errorf("with no t3 found: %+v", h)
	}

	servingT3 = func() (string, bool) { return "", false }
	h = helloFor("antigravity", "/work")
	if h.Route.T3CLI != "" || h.Route.T3Thread != "" || h.ByThread || h.Agent != "antigravity" {
		t.Errorf("under another parent (tmux/editor): %+v", h)
	}

	servingT3 = func() (string, bool) { return "/home/u/.local/bin/t3", true }
	agentAbove = func() bool { return true }
	t.Cleanup(func() { agentAbove = func() bool { return false } })
	if h := helloFor("antigravity", "/work"); h.Route.T3CLI != "" || h.Route.T3Thread != "" || h.ByThread || h.Agent != "antigravity" {
		t.Errorf("under another agent: %+v", h)
	}
}

// finder stands in for T3's database: it has the threads in threads, but answers the first
// misses calls, whoever is asked about, as a database without the row yet would. It records what
// it was asked.
type finder struct {
	mu      sync.Mutex
	threads map[string]string
	misses  int
	asked   []string
	// missing, if set, is the answer for a session with no thread, in place of a failure.
	missing error
}

func (f *finder) find(_ context.Context, native string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, native)
	if len(f.asked) <= f.misses {
		return "", errors.New("T3's database has no thread for this session")
	}
	if th, ok := f.threads[native]; ok {
		return th, nil
	}
	if f.missing != nil {
		return "", f.missing
	}
	return "", errors.New("T3's database has no thread for this session")
}

func (f *finder) askedAbout() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// fastSeek makes the server's retries quick for the length of the test.
func fastSeek(t *testing.T) {
	t.Helper()
	first, longest, total := seekFirst, seekMax, seekFor
	seekFirst, seekMax, seekFor = 20*time.Millisecond, 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() { seekFirst, seekMax, seekFor = first, longest, total })
}

// fastSeek puts the timings back when its test ends, and a link's lookup may still be running
// then: the servers a test opens end with the test, but their goroutines are not waited for. A
// lookup that read the variables would race with that, and the race detector fails the run (CI did,
// on TestEveryToolTakesTheThreadFromMeta). The link takes its timings when it is made, so changing
// the variables while it looks is harmless. This test can fail only under -race.
func TestALookupInProgressLeavesTheTimingVariablesAlone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &link{c: &client.Client{}, hello: helloFor("claude-code", "/work"), changed: make(chan struct{}),
		find: (&finder{}).find, seek: seekTimes{total: time.Minute, first: time.Hour, max: time.Hour}}
	l.startSeek(ctx)
	first, longest, total := seekFirst, seekMax, seekFor
	seekFirst, seekMax, seekFor = time.Hour, time.Hour, time.Hour // what a test's cleanup does, as the lookup runs
	seekFirst, seekMax, seekFor = first, longest, total
	cancel()
	waitFor(t, "the lookup to end", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return !l.seeking
	})
}

// started runs a link against f the way the server does: inside T3 if fd is given.
func started(t *testing.T, f *fakeDaemon, fd *finder, env map[string]string) *server {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client.Client{GitDir: gitDir}
	l := &link{c: c, hello: helloFor("claude-code", "/work"), changed: make(chan struct{}), seek: currentSeek()}
	if fd != nil {
		l.find = fd.find
	}
	go l.run(ctx)
	if _, err := l.wait(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return &server{c: c, link: l}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// lastRoute waits for the daemon to be told a route, and returns the latest.
func lastRoute(t *testing.T, f *fakeDaemon) proto.Route {
	t.Helper()
	var got proto.Route
	eventually(t, "the daemon to be told the route", func() bool {
		rs := f.routesTold()
		if len(rs) == 0 {
			return false
		}
		got = rs[len(rs)-1]
		return true
	})
	return got
}

func metaCall(threadID string) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "who"}}
	req.Params.SetMeta(map[string]any{"threadId": threadID})
	return req
}

// A thread T3's database has at once is in the first hello, found by the Claude Code session ID.
func TestThreadFoundAtOnceIsInTheFirstHello(t *testing.T) {
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"claude-session-1": "thread-1"}}
	started(t, f, fd, map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session-1", "T3CODE_CLI_PATH": "/t3/bin/t3"})
	if h := f.lastHello(); h.T3Thread != "thread-1" || h.T3CLI != "/t3/bin/t3" {
		t.Errorf("hello: %+v", h.Route)
	}
	if got := fd.askedAbout(); len(got) != 1 || got[0] != "claude-session-1" {
		t.Errorf("T3's database was asked about %q", got)
	}
}

// A thread T3 hasn't written down yet is looked for again, and told to the daemon when found, and
// a hello after that has it.
func TestThreadFoundLaterIsToldToTheDaemon(t *testing.T) {
	fastSeek(t)
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"claude-session-1": "thread-1"}, misses: 3}
	started(t, f, fd, map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session-1", "T3CODE_CLI_PATH": "/t3/bin/t3",
		"CLAUDE_CODE_MESSAGING_TOKEN": "tok-1"})
	if h := f.lastHello(); h.T3Thread != "" {
		t.Errorf("the thread was in the hello, though T3 had none: %+v", h.Route)
	}
	got := lastRoute(t, f)
	if got.T3Thread != "thread-1" || got.T3CLI != "/t3/bin/t3" || got.MsgToken != "tok-1" {
		t.Errorf("route told: %+v", got)
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(fd.askedAbout()); n != 4 {
		t.Errorf("T3's database was asked %d times, want 4 and then no more", n)
	}
	before := f.helloCount()
	f.dropConnections()
	eventually(t, "the server to register again", func() bool { return f.helloCount() > before })
	if h := f.lastHello(); h.T3Thread != "thread-1" {
		t.Errorf("the second hello: %+v", h.Route)
	}
}

// Codex's tool calls carry its thread ID in _meta: the first call finds the thread, and the
// daemon is told, and the same ID again is no news.
func TestCodexThreadIDInAToolCallFindsTheThread(t *testing.T) {
	fastSeek(t)
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"codex-thread-9": "thread-9"}}
	s := started(t, f, fd, map[string]string{"CLAUDE_CODE_SESSION_ID": "", "T3CODE_CLI_PATH": "/t3/bin/t3"})
	s.noteMeta(context.Background(), metaCall("codex-thread-9"))
	if got := lastRoute(t, f); got.T3Thread != "thread-9" {
		t.Errorf("route told: %+v", got)
	}
	n := len(fd.askedAbout())
	s.noteMeta(context.Background(), metaCall("codex-thread-9"))
	time.Sleep(200 * time.Millisecond)
	if len(fd.askedAbout()) != n {
		t.Errorf("T3's database was asked again about a thread already found")
	}
}

// Where T3's database has nothing, and outside T3, the server tells the daemon nothing about a T3
// thread. Outside T3 it does tell the daemon the client's own ID for the conversation (Codex's
// thread ID), which is how the daemon finds the conversation's name (#66).
func TestNoThreadMeansNoRoute(t *testing.T) {
	fastSeek(t)
	t.Run("outside T3", func(t *testing.T) {
		f := &fakeDaemon{}
		s := started(t, f, nil, map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session-1", "T3CODE_CLI_PATH": "", "CODEX_HOME": ""})
		s.noteMeta(context.Background(), metaCall("x"))
		eventually(t, "the daemon to be told the thread ID", func() bool { return len(f.routesTold()) > 0 })
		// With no CODEX_HOME the server names Codex's default home, so the daemon doesn't take its
		// own, which is the home of whichever session started it (#88, Sol's review of #301).
		want := proto.Route{Native: "x", CodexHome: title.DefaultCodexDir()}
		if rs := f.routesTold(); len(rs) != 1 || rs[0] != want || f.lastHello().T3Thread != "" {
			t.Errorf("told %+v, hello %+v", rs, f.lastHello().Route)
		}
	})
	t.Run("outside T3, with a Codex home named", func(t *testing.T) {
		home := t.TempDir()
		f := &fakeDaemon{}
		s := started(t, f, nil, map[string]string{"CLAUDE_CODE_SESSION_ID": "", "T3CODE_CLI_PATH": "", "CODEX_HOME": home})
		s.noteMeta(context.Background(), metaCall("x"))
		eventually(t, "the daemon to be told the thread ID", func() bool { return len(f.routesTold()) > 0 })
		if rs := f.routesTold(); len(rs) != 1 || rs[0] != (proto.Route{Native: "x", CodexHome: home}) {
			t.Errorf("told %+v", rs)
		}
	})
	t.Run("a database with no such session", func(t *testing.T) {
		f := &fakeDaemon{}
		fd := &finder{}
		started(t, f, fd, map[string]string{"CLAUDE_CODE_SESSION_ID": "claude-session-1", "T3CODE_CLI_PATH": "/t3/bin/t3"})
		eventually(t, "a few more tries", func() bool { return len(fd.askedAbout()) >= 4 })
		if rs := f.routesTold(); len(rs) != 0 || f.lastHello().T3Thread != "" {
			t.Errorf("told %+v, hello %+v", rs, f.lastHello().Route)
		}
	})
}

// A thread found while the server's hello is still on its way is not lost: the daemon has no
// session yet to be told it, and the hello doesn't have it, so the server tells it once registered.
func TestThreadFoundWhileRegisteringIsToldAfterwards(t *testing.T) {
	fastSeek(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-session-1")
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	gate := make(chan struct{})
	f := &fakeDaemon{helloGate: gate}
	fd := &finder{threads: map[string]string{"claude-session-1": "thread-1"}, misses: 1}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	l := &link{c: &client.Client{GitDir: gitDir}, hello: helloFor("claude-code", "/work"), changed: make(chan struct{}), find: fd.find, seek: currentSeek()}
	go l.run(ctx)
	eventually(t, "the thread to be found", l.threadKnown)
	// The hello was made before the thread was found, but finding the daemon takes a moment, so it
	// may reach the daemon after.
	eventually(t, "the hello to arrive", func() bool { return f.helloCount() > 0 })
	if h := f.lastHello(); h.T3Thread != "" {
		t.Fatalf("the hello had the thread already: %+v", h.Route)
	}
	close(gate)
	if _, err := l.wait(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := lastRoute(t, f); got.T3Thread != "thread-1" {
		t.Errorf("route told: %+v", got)
	}
}

// A Codex thread of T3's headless server has none of T3's variables but the T3CODE_HOME join
// forwards, and the t3 that serves runs Codex. Its route is T3's, and its thread comes from the
// tool calls' _meta, as in T3's app. A codex t3 doesn't run keeps Codex's own route, as does one
// with an agent above it (a thread's shell's) or with no T3 home, for which no t3 is looked for
// (#802).
func TestHeadlessT3CodexIsFound(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	t.Setenv(threadEnv, "")
	t.Setenv("T3CODE_HOME", "/home/u/.t3")
	asked := false
	found := func() string { asked = true; return "/home/u/.local/bin/t3" }
	t.Cleanup(func() { codexT3 = func() string { return "" } })
	codexT3 = found
	h := helloFor("codex", "/work")
	if r := h.Route; r.T3CLI != "/home/u/.local/bin/t3" || r.T3Home != "/home/u/.t3" || r.CodexCLI != "" {
		t.Errorf("route %+v, want T3's", h.Route)
	}
	if h.ByThread || h.Agent != "codex in T3" {
		t.Errorf("hello %+v: want T3's label, and the thread from _meta, not the variable", h)
	}

	codexT3 = func() string { return "" }
	if h := helloFor("codex", "/work"); h.Route.T3CLI != "" || h.Agent != "codex" {
		t.Errorf("a codex t3 doesn't run: %+v", h)
	}

	agentAbove = func() bool { return true }
	t.Cleanup(func() { agentAbove = func() bool { return false } })
	asked, codexT3 = false, found
	if h := helloFor("codex", "/work"); h.Route.T3CLI != "" || h.Agent != "codex" || asked {
		t.Errorf("a codex under the thread's codex: %+v, looked for t3: %v", h, asked)
	}
	agentAbove = func() bool { return false }

	t.Setenv("T3CODE_HOME", "")
	asked = false
	if h := helloFor("codex", "/work"); h.Route.T3CLI != "" || asked {
		t.Errorf("with no T3 home: %+v, looked for t3: %v", h, asked)
	}
}
