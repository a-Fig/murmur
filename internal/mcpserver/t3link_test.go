package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
)

// More tests of how the server finds its T3 thread (#57), across the server's turning on and off.

// useFinder makes the server's lookups in T3's database ask fd.
func useFinder(t *testing.T, fd *finder) {
	t.Helper()
	old := threadOf
	threadOf = func(ctx context.Context, _, _, native string) (string, error) { return fd.find(ctx, native) }
	t.Cleanup(func() { threadOf = old })
}

// Codex puts its thread ID in the _meta of every tool call, and every tool takes it: a session
// whose first call is lookup, say, is found as well as one whose first call is who.
func TestEveryToolTakesTheThreadFromMeta(t *testing.T) {
	fastSeek(t)
	for tool, args := range map[string]map[string]any{
		"who":    {},
		"lookup": {"name": "quiet-otter"},
		"status": {"doing": "testing"},
		"send":   {"to": "quiet-otter", "subject": "hello"},
		"inbox":  {},
	} {
		t.Run(tool, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_SESSION_ID", "")
			t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
			useFinder(t, &finder{threads: map[string]string{"codex-thread-9": "thread-9"}})
			f := &fakeDaemon{}
			p := repo(t, true, f)
			if _, err := p.RecordConsent(); err != nil {
				t.Fatal(err)
			}
			cs, _ := open(t, p.Top, 25*time.Millisecond)
			waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args,
				Meta: mcp.Meta{"threadId": "codex-thread-9"}})
			if err != nil || res.IsError {
				t.Fatalf("%s: %v %+v", tool, err, res)
			}
			if got := lastRoute(t, f); got.T3Thread != "thread-9" {
				t.Errorf("route told after %s: %+v", tool, got)
			}
		})
	}
}

// A link made when the server turns on again, after a leave and a join, finds its thread as the
// first did: the finder is the link's, set up where the link is made.
func TestLinkMadeOnReactivationStillFindsItsThread(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-session-1")
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	fd := &finder{threads: map[string]string{"claude-session-1": "thread-1"}}
	useFinder(t, fd)
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, 25*time.Millisecond)
	waitFor(t, "the first registration", func() bool { return f.helloCount() == 1 })
	if h := f.lastHello(); h.T3Thread != "thread-1" {
		t.Fatalf("the first hello: %+v", h.Route)
	}

	if _, err := p.ForgetConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tools to go", func() bool { return len(toolNamesOf(t, cs)) == 0 })
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second registration", func() bool { return f.helloCount() == 2 })
	if h := f.lastHello(); h.T3Thread != "thread-1" {
		t.Errorf("the hello after joining again: %+v", h.Route)
	}
	if n := len(fd.askedAbout()); n != 2 {
		t.Errorf("T3's database was asked %d times, want once for each link", n)
	}
}

// A daemon that refuses the route is one that has no such request, left running by an older mur.
// The session works, and messages for it come as notices: the server says so, once, and not at
// every attempt.
func TestARefusedRouteIsLoggedOnce(t *testing.T) {
	fastSeek(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "claude-session-1")
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	f := &fakeDaemon{routeError: "unknown op route"}
	fd := &finder{threads: map[string]string{"claude-session-1": "thread-1"}, misses: 1}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var mu sync.Mutex
	var lines []string
	l := &link{c: &client.Client{GitDir: gitDir}, hello: helloFor("claude-code", "/work"), changed: make(chan struct{}), find: fd.find, seek: currentSeek(),
		logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		}}
	go l.run(ctx)
	if _, err := l.wait(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the route to be refused", func() bool { return len(f.routesTold()) > 0 })
	l.tellRoute(ctx)
	l.tellRoute(ctx)
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.Contains(lines[0], "route") {
		t.Errorf("logged %q, want one line about the route", lines)
	}
}

// A Pi server started after a move, on a daemon too old to follow it, registers as a new session
// with the link up (#1214). It logs that once, and a reconnect doesn't log it again. On a daemon
// that follows moves it logs nothing, and nor does a /resume of the file already open, which Pi
// also reports as a previous file.
func TestAPiMoveAnOlderDaemonCantFollowIsLoggedOnce(t *testing.T) {
	for _, c := range []struct {
		protocol int
		prev     string
		lines    int
	}{{proto.FromProtocol - 1, "/s/a.jsonl", 1}, {proto.FromProtocol, "/s/a.jsonl", 0}, {proto.FromProtocol - 1, "/s/b.jsonl", 0}} {
		t.Run(fmt.Sprint("protocol ", c.protocol, " from ", c.prev), func(t *testing.T) {
			t.Setenv("MURMUR_PI_SESSION_FILE", "/s/b.jsonl")
			t.Setenv("MURMUR_PI_PREVIOUS_SESSION_FILE", c.prev)
			f := &fakeDaemon{protocol: c.protocol}
			gitDir := f.serve(t)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)

			var mu sync.Mutex
			var lines []string
			l := &link{c: &client.Client{GitDir: gitDir}, hello: helloFor("pi", "/work"), changed: make(chan struct{}),
				logf: func(format string, args ...any) {
					mu.Lock()
					defer mu.Unlock()
					lines = append(lines, fmt.Sprintf(format, args...))
				}}
			go l.run(ctx)
			if _, err := l.wait(ctx, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			if h := f.lastHello(); h.From != c.prev || h.Reconnect {
				t.Fatalf("first hello: %+v", h)
			}
			f.dropConnections()
			waitFor(t, "the reconnect", func() bool { return f.helloCount() >= 2 })
			if _, err := l.wait(ctx, 10*time.Second); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(lines) != c.lines || c.lines == 1 && !strings.Contains(lines[0], "Pi's move from /s/a.jsonl") {
				t.Errorf("logged %q, want %d line(s) about the move", lines, c.lines)
			}
		})
	}
}
