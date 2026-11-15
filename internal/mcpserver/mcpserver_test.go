package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

// A server that starts a daemon runs this binary as `mur daemon --git-dir DIR`, which under test is
// the test binary. Here it only notes in DIR that it was asked to, with its PID, and exits: a test
// of a server that must not start one looks for that note, and each test's cleanup waits for the
// process to be gone (see waitSpawned).
func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == "daemon" && os.Args[2] == "--git-dir" {
		f, err := os.OpenFile(filepath.Join(os.Args[3], spawnNote), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "spawned %d\n", os.Getpid())
			f.Close()
		}
		return
	}
	// The tests run in whatever session started them, which may be one in T3 with a messaging socket:
	// a server that took those from its environment would look its thread up in the real T3 database.
	for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN"} {
		os.Unsetenv(k)
	}
	// Servers publish their records here, not in the real user's config directory.
	dir, err := os.MkdirTemp("", "murmur-sessions-")
	if err != nil {
		panic(err)
	}
	os.Setenv(brand.SessionsDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const spawnNote = "daemon-was-spawned"

// fakeDaemon answers hellos and requests the way the daemon does, as far as the server's side of
// following Claude Code's session ID goes: every reply to a request carries the Claude Code
// session ID the daemon holds, and a hello's reply carries the one it was given.
type fakeDaemon struct {
	live string // what the daemon holds, as a hook has set it
	// helloGate, if set, holds every hello's reply until it is closed.
	helloGate chan struct{}
	// routeError, if set, is the daemon's answer to a route: one that has no such request.
	routeError string
	// sendReply, if set, is the daemon's answer to a send.
	sendReply func(proto.Request) proto.Response

	mu       sync.Mutex
	hellos   []proto.Hello
	requests []proto.Request // everything but hellos and pings
	routes   []proto.Route   // what OpRoute requests carried
	conns    []net.Conn      // the hello connections, to drop
}

func (f *fakeDaemon) serve(t *testing.T) (gitDir string) {
	t.Helper()
	return f.serveAt(t, t.TempDir())
}

// serveAt is serve with the git dir already chosen, for a test that has a real repository.
func (f *fakeDaemon) serveAt(t *testing.T, gitDir string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	dir := client.StateDir(gitDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// It holds the clone's lock, as a daemon does: a client takes a daemon.json beside a free lock
	// for a dead daemon's.
	lock, err := proc.Lock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	info, _ := json.Marshal(proto.Info{Addr: ln.Addr().String(), Token: "tok"})
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), info, 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	return gitDir
}

func (f *fakeDaemon) handle(conn net.Conn) {
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return
	}
	var req proto.Request
	if json.Unmarshal(line, &req) != nil {
		conn.Close()
		return
	}
	reply := func(resp proto.Response) {
		b, _ := json.Marshal(resp)
		conn.Write(append(b, '\n'))
	}
	if req.Op == proto.OpPing && req.Nonce != "" { // the daemon's proof (#371)
		reply(proto.Response{OK: true, Protocol: proto.Protocol, Proof: proto.Proof("tok", conn.LocalAddr().String(), req.Nonce)})
		if line, err = r.ReadBytes('\n'); err != nil {
			conn.Close()
			return
		}
		req = proto.Request{}
		if json.Unmarshal(line, &req) != nil {
			conn.Close()
			return
		}
	}
	if req.Op == proto.OpHello {
		f.mu.Lock()
		f.hellos = append(f.hellos, *req.Hello)
		f.conns = append(f.conns, conn)
		live := f.live
		if live == "" {
			live = req.Hello.ClaudeSession
		}
		f.mu.Unlock()
		if f.helloGate != nil {
			<-f.helloGate
		}
		reply(proto.Response{OK: true, Self: "dev/" + req.Hello.ID, ClaudeSession: live, Protocol: proto.Protocol})
		return // the connection stays open until the test drops it
	}
	defer conn.Close()
	f.mu.Lock()
	if req.Op != proto.OpPing { // finding the daemon is the client's, and not a request of the session's
		f.requests = append(f.requests, req)
	}
	live := f.live
	refuse := req.Op == proto.OpRoute && f.routeError != ""
	if req.Op == proto.OpRoute && req.Route != nil {
		f.routes = append(f.routes, *req.Route)
	}
	f.mu.Unlock()
	if refuse {
		reply(proto.Response{Error: f.routeError})
		return
	}
	if req.Op == proto.OpSend && f.sendReply != nil {
		reply(f.sendReply(req))
		return
	}
	reply(proto.Response{OK: true, Self: "dev/" + req.Session, ClaudeSession: live, Protocol: proto.Protocol})
}

func (f *fakeDaemon) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeDaemon) routesTold() []proto.Route {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Route(nil), f.routes...)
}

func (f *fakeDaemon) helloCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.hellos)
}

func (f *fakeDaemon) lastHello() proto.Hello {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hellos[len(f.hellos)-1]
}

func (f *fakeDaemon) hook(claude string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = claude
}

func (f *fakeDaemon) dropConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
}

// The hello carries the server's parent process, which is the Claude Code process that started it,
// and the Claude Code session ID in its environment.
func TestHelloNamesTheParentProcess(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff")
	h := helloFor("claude-code", "/work")
	if h.ClientPID != os.Getppid() || h.ClientPID == 0 {
		t.Fatalf("the hello says the parent is %d, want %d", h.ClientPID, os.Getppid())
	}
	if h.ClaudeSession != "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff" || h.ID != "0a1b2c" || h.Cwd != "/work" {
		t.Fatalf("hello: %+v", h)
	}
}

// A reconnect (the daemon restarted, say) must not bring back the Claude Code session ID the
// server's environment has, which a /clear made stale: after any call, the server registers with
// the one the daemon reported.
func TestReconnectBringsTheLiveClaudeID(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff")
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client.Client{GitDir: p.GitDir}
	s := &server{c: c, place: p, link: &link{c: c, hello: helloFor("claude-code", "/work"), changed: make(chan struct{})}}
	go s.link.run(ctx)

	if _, err := s.call(ctx, proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if h := f.lastHello(); h.ClaudeSession != "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff" {
		t.Fatalf("first hello: %+v", h)
	}

	// /clear: a hook told the daemon. The server learns it with its next call.
	f.hook("fffffff0-live")
	if _, err := s.call(ctx, proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatalf("call after the switch: %v", err)
	}

	// The daemon's connection drops and the server registers again.
	before := f.helloCount()
	f.dropConnections()
	deadline := time.Now().Add(10 * time.Second)
	for f.helloCount() == before {
		if time.Now().After(deadline) {
			t.Fatal("the server never registered again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	h := f.lastHello()
	if h.ClaudeSession != "fffffff0-live" {
		t.Fatalf("the second hello brought back %q, want the live ID", h.ClaudeSession)
	}
	if h.ID != "0a1b2c" || !h.Reconnect {
		t.Fatalf("the session's own ID must not change: %+v", h)
	}
}

// repo makes a git repository whose files say it uses Murmur (if marked), with a daemon that
// answers for it, and returns the repository's place.
func repo(t *testing.T, marked bool, f *fakeDaemon) join.Place {
	t.Helper()
	dir := t.TempDir()
	// Registered after the temporary directory and before the server (open), so it runs after the
	// server has stopped and before the directory is removed.
	t.Cleanup(func() { waitSpawned(t, filepath.Join(dir, ".git")) })
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if marked {
		if _, err := setup.Init(context.Background(), dir); err != nil {
			t.Fatal(err)
		}
	}
	p, err := join.Locate(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		f.serveAt(t, p.GitDir)
	}
	return p
}

// open serves the repo's MCP server over an in-memory connection and returns a client of it, and
// the number of times the client was told the tool list changed.
func open(t *testing.T, dir string, poll time.Duration) (*mcp.ClientSession, *atomic.Int32) {
	t.Helper()
	return openWith(t, Options{Agent: "claude-code", Version: "test", Dir: dir, Poll: poll})
}

// openWith is open with the options given.
func openWith(t *testing.T, opt Options) (*mcp.ClientSession, *atomic.Int32) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ct, st := mcp.NewInMemoryTransports()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Serve(ctx, opt, st)
	}()
	var changed atomic.Int32
	c := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed.Add(1) },
	})
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); cancel(); <-done })
	return cs, &changed
}

// offersTools reports whether the names include the tools every agent gets. Tools added since
// (lookup, #21) are not a reason for the tests of when the server is on to change.
func offersTools(names []string) bool {
	for _, want := range []string{"who", "status", "send", "inbox"} {
		if !slices.Contains(names, want) {
			return false
		}
	}
	return true
}

func toolNamesOf(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// settle gives a server that might wrongly act time to do it.
func settle() { time.Sleep(400 * time.Millisecond) }

// waitSpawned waits for every daemon a server started for the clone to have run and exited. A
// daemon runs in the clone's top directory, and on Windows a directory can't be removed while a
// process works in it (#116). The log file is created just before a start, so where it exists a
// daemon was started, and its note, with its PID, comes once it has run. The server has stopped by
// now, and Serve waits for a start in progress, so no other start can follow.
func waitSpawned(t *testing.T, gitDir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(gitDir, "murmur", "daemon.log")); err != nil {
		return
	}
	deadline := time.Now().Add(15 * time.Second)
	var pids []int
	for len(pids) == 0 {
		if time.Now().After(deadline) {
			t.Errorf("a daemon was started for %s and never ran", gitDir)
			return
		}
		time.Sleep(20 * time.Millisecond)
		b, _ := os.ReadFile(filepath.Join(gitDir, spawnNote))
		for _, line := range strings.Split(string(b), "\n") {
			if pid, err := strconv.Atoi(strings.TrimPrefix(line, "spawned ")); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	for _, pid := range pids {
		p, err := os.FindProcess(pid)
		if err != nil {
			continue // on Windows, the process has exited already
		}
		exited := make(chan struct{})
		go func() { p.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(time.Until(deadline)):
			t.Errorf("the daemon started for %s (pid %d) is still running", gitDir, pid)
		}
	}
}

// spawned reports whether anything tried to start a daemon for the clone: the note the test binary
// leaves when run as one, or the log file the client creates just before it does.
func spawned(p join.Place) bool {
	for _, name := range []string{spawnNote, filepath.Join("murmur", "daemon.log")} {
		if _, err := os.Stat(filepath.Join(p.GitDir, name)); err == nil {
			return true
		}
	}
	return false
}

// A user-scope server starts in every repo the user opens. In one that hasn't invited Murmur, or
// that this user hasn't joined, it lists no tools and no instructions, and never reaches a daemon:
// not to start one, and not to register with one that is already running.
func TestPassiveWhereItIsNotInvited(t *testing.T) {
	for name, tc := range map[string]struct{ marked, joined bool }{
		"no marker, not joined": {false, false},
		"marker, not joined":    {true, false},
		"joined, no marker":     {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeDaemon{}
			p := repo(t, tc.marked, f)
			if tc.joined {
				if _, err := p.RecordConsent(); err != nil {
					t.Fatal(err)
				}
			}
			cs, changed := open(t, p.Top, 25*time.Millisecond)
			settle()
			if names := toolNamesOf(t, cs); len(names) != 0 {
				t.Errorf("tools listed where Murmur is off: %q", names)
			}
			if got := cs.InitializeResult().Instructions; got != "" {
				t.Errorf("instructions given where Murmur is off: %q", got)
			}
			if f.helloCount() != 0 || spawned(p) || changed.Load() != 0 {
				t.Errorf("hellos=%d spawned=%v list changes=%d: it acted where Murmur is off", f.helloCount(), spawned(p), changed.Load())
			}
		})
	}
	t.Run("outside any repository", func(t *testing.T) {
		cs, _ := open(t, t.TempDir(), 25*time.Millisecond)
		settle()
		if names := toolNamesOf(t, cs); len(names) != 0 {
			t.Errorf("tools listed outside a repo: %q", names)
		}
	})
}

// A repo with no daemon running, where Murmur is off: the server doesn't start one.
func TestPassiveStartsNoDaemon(t *testing.T) {
	p := repo(t, true, nil)
	open(t, p.Top, 25*time.Millisecond)
	settle()
	if spawned(p) {
		t.Error("started a daemon in a repo the user hasn't joined")
	}
	// The same repo once joined does want one, which shows the check above can see it.
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a daemon to be started", func() bool { return spawned(p) })
}

// Joining while the session is open turns the server on: the tools appear through a list change the
// client is told about, and the session registers. Leaving turns it off again, and when the
// daemon goes away with it the server does not start another.
func TestJoinThenLeaveWhileRunning(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f)
	cs, changed := open(t, p.Top, 25*time.Millisecond)
	settle()
	if len(toolNamesOf(t, cs)) != 0 || f.helloCount() != 0 {
		t.Fatalf("on before joining: tools %q, hellos %d", toolNamesOf(t, cs), f.helloCount())
	}

	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tools to appear", func() bool { return offersTools(toolNamesOf(t, cs)) })
	// Leaving takes away the tools named in toolNames, so a tool added without being named there
	// would stay after a leave.
	if got, want := slices.Sorted(slices.Values(toolNamesOf(t, cs))), slices.Sorted(slices.Values(toolNames)); !slices.Equal(got, want) {
		t.Errorf("the server offers %q but takes away %q on a leave", got, want)
	}
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	waitFor(t, "the client to be told the tool list changed", func() bool { return changed.Load() > 0 })
	if spawned(p) {
		t.Error("started a daemon though one answers")
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil || res.IsError {
		t.Fatalf("who: %v %+v", err, res)
	}

	// Leave: the consent goes, then the daemon stops (as `mur join --leave` does it, in that
	// order).
	if _, err := p.ForgetConsent(); err != nil {
		t.Fatal(err)
	}
	f.dropConnections()
	os.Remove(filepath.Join(client.StateDir(p.GitDir), "daemon.json"))
	waitFor(t, "the tools to go", func() bool { return len(toolNamesOf(t, cs)) == 0 })
	time.Sleep(1500 * time.Millisecond) // longer than the wait before a registration is retried
	if spawned(p) {
		t.Error("started a daemon after the user left")
	}
	if f.helloCount() != 1 {
		t.Errorf("registered again after the user left: %d hellos", f.helloCount())
	}
}

// The window between a leave and the next look at the files is closed by asking before every
// registration: the daemon stops with the consent already gone, and the server, which has not yet
// noticed, must neither register again nor start another daemon.
func TestLeaveBeforeTheServerNoticesStartsNoDaemon(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, time.Hour) // never looks again
	if cs.InitializeResult().Instructions == "" {
		t.Error("a joined repo's server gave no instructions")
	}
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })

	p.ForgetConsent()
	f.dropConnections()
	os.Remove(filepath.Join(client.StateDir(p.GitDir), "daemon.json"))
	time.Sleep(2500 * time.Millisecond) // the link would retry after one second, then two
	if spawned(p) || f.helloCount() != 1 {
		t.Errorf("spawned=%v hellos=%d: it acted after the consent was gone", spawned(p), f.helloCount())
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "isn't") {
		t.Errorf("a call after leaving: %+v", res)
	}
}

// The server looks at the files every few seconds, and until it does, its registered session is
// still up. A tool call in that window must not reach a daemon that is still running, or it would
// publish for a user who has left, or whose repo no longer says it uses Murmur (Codex review, 2026-10-29).
func TestCallAfterLeavingNeverReachesAStillRunningDaemon(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, time.Hour) // never looks again
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	ctx := context.Background()
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"doing": "x"}}); err != nil || res.IsError {
		t.Fatalf("status while joined: %v %+v", err, res)
	}
	before := f.requestCount()
	if before != 1 {
		t.Fatalf("the daemon saw %d requests for one call", before)
	}

	if _, err := p.ForgetConsent(); err != nil {
		t.Fatal(err)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "status", Arguments: map[string]any{"doing": "after leaving"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "isn't on") {
		t.Errorf("a call after leaving: %+v", res)
	}
	if f.requestCount() != before {
		t.Errorf("the daemon was sent %d requests after the user left", f.requestCount()-before)
	}

	// The same when the repo stops saying it uses Murmur, say on a checkout of an older branch.
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		os.Remove(filepath.Join(p.Top, name))
	}
	if res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "who"}); err != nil || !res.IsError || f.requestCount() != before {
		t.Errorf("a call with the marker gone: %v %+v, %d requests", err, res, f.requestCount()-before)
	}
}
