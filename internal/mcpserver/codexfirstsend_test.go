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
	"github.com/a-Fig/murmur/internal/t3db"
)

// A Codex session in T3 learns its thread from its first tool call's _meta (#966). The daemon has
// to hear that thread, and read whether it is a delegate's, before a call that changes something
// goes out; a call that only reads doesn't wait for it.

// codexInT3 starts a link for a Codex session inside T3 against f, whose database finds each thread
// after a pause of slow, as T3's may.
func codexInT3(t *testing.T, f *fakeDaemon, fd *finder, slow time.Duration) *server {
	t.Helper()
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("T3CODE_HOME", "")
	t.Setenv(threadEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client.Client{GitDir: gitDir}
	l := &link{c: c, hello: helloFor("codex", "/work"), changed: make(chan struct{}), seek: currentSeek(),
		find: func(ctx context.Context, native string) (string, error) {
			select {
			case <-time.After(slow):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return fd.find(ctx, native)
		}}
	go l.run(ctx)
	if _, err := l.wait(ctx, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return &server{c: c, link: l}
}

func codexCallIn(tool, thread string) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool}}
	req.Params.SetMeta(map[string]any{"threadId": thread})
	return req
}

// sentAfterRoute says whether the daemon was told the route naming thread before the first send.
func sentAfterRoute(f *fakeDaemon, thread string) (routed, sent bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		switch {
		case r.Op == proto.OpRoute && r.Route != nil && r.Route.T3Thread == thread:
			routed = true
		case r.Op == proto.OpSend:
			return routed, true
		}
	}
	return routed, false
}

// The first call is a send: the daemon hears the thread before the send reaches it.
func TestACodexFirstSendWaitsForItsThread(t *testing.T) {
	fastSeek(t)
	f := &fakeDaemon{routeDelay: 100 * time.Millisecond}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	s := codexInT3(t, f, fd, 300*time.Millisecond)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
}

// A call that only reads goes on at once, and a send right after it waits for the look the read
// started.
func TestACodexReadDoesntWaitButTheSendAfterItDoes(t *testing.T) {
	fastSeek(t)
	f := &fakeDaemon{routeDelay: 100 * time.Millisecond}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	slow := 500 * time.Millisecond
	s := codexInT3(t, f, fd, slow)
	start := time.Now()
	if _, err := s.callFrom(context.Background(), codexCallIn("who", "codex-kid"), proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took >= slow {
		t.Errorf("who waited %v for the thread", took)
	}
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
	// Once the thread is known, a send waits for nothing.
	start = time.Now()
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "again"}); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took >= slow {
		t.Errorf("a later send waited %v", took)
	}
}

// A thread found while the hello is still on its way: the daemon hears it before a send that
// waited for the registration goes on.
func TestACodexThreadFoundDuringTheHelloIsToldBeforeTheSend(t *testing.T) {
	fastSeek(t)
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("T3CODE_HOME", "")
	t.Setenv(threadEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	f := &fakeDaemon{helloGate: make(chan struct{}), routeDelay: 100 * time.Millisecond}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client.Client{GitDir: gitDir}
	l := &link{c: c, hello: helloFor("codex", "/work"), changed: make(chan struct{}), seek: currentSeek(), find: fd.find}
	go l.run(ctx)
	s := &server{c: c, link: l}
	waitFor(t, "the hello to be sent", func() bool { return f.helloCount() > 0 })
	done := make(chan error, 1)
	go func() {
		_, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"})
		done <- err
	}()
	waitFor(t, "the thread to be found", l.threadKnown)
	time.Sleep(50 * time.Millisecond)
	close(f.helloGate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
}

// sendsReached counts the sends that reached the daemon.
func sendsReached(f *fakeDaemon) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.Op == proto.OpSend {
			n++
		}
	}
	return n
}

// T3's database fails as the thread is looked up (#980): a send changes nothing and says to try
// again, rather than going out before the daemon knows whether the session is a delegate. Once the
// database answers, the next send looks the thread up, tells the daemon, and goes out after it.
func TestACodexSendWaitsForAFailedLookup(t *testing.T) {
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}, misses: 1 << 30}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("who", "codex-kid"), proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatalf("who, with the thread not found: %v", err)
	}
	_, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"})
	if !errors.Is(err, errUnclassified) {
		t.Errorf("the send while T3's database fails: %v, want errUnclassified", err)
	}
	r, _, _ := result(nil, proto.Response{}, err)
	t.Logf("what the agent reads: %s", r.Content[0].(*mcp.TextContent).Text)
	if n := sendsReached(f); n != 0 {
		t.Fatalf("%d sends reached the daemon before it was told the thread", n)
	}
	fd.mu.Lock()
	fd.misses = 0
	fd.mu.Unlock()
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send once the database answers: %v", err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
}

// The daemon takes the route but can't read the thread's lineage (#980): the send changes nothing
// and says to try again. Once the daemon can read it, the next send tells it the route again, and
// goes out after a reply that says the thread was read.
func TestACodexSendWaitsForTheDaemonToReadTheThread(t *testing.T) {
	var mu sync.Mutex
	unread := true
	f := &fakeDaemon{threadUnread: func(proto.Route) bool { mu.Lock(); defer mu.Unlock(); return unread }}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	s := codexInT3(t, f, fd, 0)
	_, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"})
	if !errors.Is(err, errUnclassified) {
		t.Errorf("the send while the daemon can't read the thread: %v, want errUnclassified", err)
	}
	if n := sendsReached(f); n != 0 {
		t.Fatalf("%d sends reached the daemon while it couldn't read the thread", n)
	}
	if _, err := s.callFrom(context.Background(), codexCallIn("lookup", "codex-kid"), proto.Request{Op: proto.OpLookup, Name: "boss"}); err != nil {
		t.Errorf("lookup, with the thread unread: %v", err)
	}
	mu.Lock()
	unread = false
	mu.Unlock()
	told := len(f.routesTold())
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send once the daemon reads the thread: %v", err)
	}
	if n := len(f.routesTold()); n <= told {
		t.Error("the route wasn't told again before the send")
	}
	if n := sendsReached(f); n != 1 {
		t.Errorf("%d sends reached the daemon, want 1", n)
	}
}

// The daemon's reply to the hello itself says it couldn't read the thread, as a hello found the
// thread in flight, or a reconnecting one, may (#980): the send that waited for that registration
// changes nothing, and nor does inbox's wait hold it.
func TestACodexSendAfterAnUnreadHelloChangesNothing(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("T3CODE_HOME", "")
	t.Setenv(threadEnv, "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	f := &fakeDaemon{helloGate: make(chan struct{}), threadUnread: func(proto.Route) bool { return true }}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client.Client{GitDir: gitDir}
	l := &link{c: c, hello: helloFor("codex", "/work"), changed: make(chan struct{}), seek: currentSeek(), find: fd.find}
	go l.run(ctx)
	s := &server{c: c, link: l}
	waitFor(t, "the hello to be sent", func() bool { return f.helloCount() > 0 })
	done := make(chan error, 1)
	go func() {
		_, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"})
		done <- err
	}()
	waitFor(t, "the thread to be found", l.threadKnown)
	time.Sleep(50 * time.Millisecond)
	close(f.helloGate)
	if err := <-done; !errors.Is(err, errUnclassified) {
		t.Errorf("the send: %v, want errUnclassified", err)
	}
	if n := sendsReached(f); n != 0 {
		t.Errorf("%d sends reached the daemon, which couldn't read the thread", n)
	}
	if _, err := s.callFrom(context.Background(), codexCallIn("inbox", "codex-kid"), proto.Request{Op: proto.OpInbox}); err != nil {
		t.Errorf("inbox, with the thread unread: %v", err)
	}
}

// A codex run from a T3 thread's shell inherits T3's variables, but T3's database has no thread for
// it: it isn't T3's, and its sends go on as a session's outside T3 do, rather than being told to
// try again for ever.
func TestACodexSessionT3HasNoThreadForGoesOn(t *testing.T) {
	f := &fakeDaemon{}
	fd := &finder{missing: t3db.ErrNotFound}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-shell"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send: %v", err)
	}
	if n := sendsReached(f); n != 1 {
		t.Errorf("%d sends reached the daemon, want 1", n)
	}
}
