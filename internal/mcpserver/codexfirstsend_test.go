package mcpserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
)

// A Codex session in T3 learns its thread from its first tool call's _meta (#966). A call that
// changes something waits while the server looks the thread up and tells the daemon, which reads
// whether it is a delegate's, and once more if that failed; then it goes on, read or not (#993). A
// call that only reads doesn't wait.

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

// slowSeek keeps the background seek from looking again during the test, so what a call looks up
// is the call's own.
func slowSeek(t *testing.T) {
	t.Helper()
	first := seekFirst
	seekFirst = time.Hour
	t.Cleanup(func() { seekFirst = first })
}

// T3's database fails as the thread is looked up (#980): the first send looks once more, and then
// goes on, unclassified, as Tyler chose (#993). A later send doesn't look again.
func TestACodexSendGoesOnWhenTheLookupFails(t *testing.T) {
	slowSeek(t)
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}, misses: 1 << 30}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send while T3's database fails: %v", err)
	}
	if n := len(fd.askedAbout()); n != 2 {
		t.Errorf("T3's database was asked %d times before the first send went on, want 2", n)
	}
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "again"}); err != nil {
		t.Fatalf("the second send: %v", err)
	}
	if n := len(fd.askedAbout()); n != 2 {
		t.Errorf("T3's database was asked again for the second send (%d asks)", n)
	}
	if n := sendsReached(f); n != 2 {
		t.Errorf("%d sends reached the daemon, want 2", n)
	}
}

// The daemon takes the route but can't read the thread's lineage (#980): the first send tells it the
// route once more, and then goes on (#993). A later send doesn't tell it again.
func TestACodexSendGoesOnWhenTheDaemonCantReadTheThread(t *testing.T) {
	slowSeek(t)
	f := &fakeDaemon{threadUnread: func(proto.Route) bool { return true }}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send while the daemon can't read the thread: %v", err)
	}
	if n := len(f.routesTold()); n != 2 {
		t.Errorf("the daemon was told the route %d times before the send, want 2", n)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "again"}); err != nil {
		t.Fatalf("the second send: %v", err)
	}
	if n := len(f.routesTold()); n != 2 {
		t.Errorf("the route was told again for the second send (%d routes)", n)
	}
}

// The daemon's reply to the hello itself says it couldn't read the thread, as one for a hello that
// found the thread in flight, or a reconnecting one, may (#980): the send that waited for that
// registration tells the daemon the route once more, and goes on (#993).
func TestACodexSendAfterAnUnreadHelloGoesOn(t *testing.T) {
	slowSeek(t)
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
	if err := <-done; err != nil {
		t.Fatalf("the send: %v", err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
	// One route from the hello's loop, which found the thread in flight, and one from the retry.
	if n := len(f.routesTold()); n != 2 {
		t.Errorf("the daemon was told the route %d times, want 2", n)
	}
}

// A codex run from a T3 thread's shell inherits T3's variables, but T3's database has no thread for
// it: its send goes on at once, after the one more look.
func TestACodexSessionT3HasNoThreadForGoesOn(t *testing.T) {
	slowSeek(t)
	f := &fakeDaemon{}
	fd := &finder{}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-shell"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send: %v", err)
	}
	if n := sendsReached(f); n != 1 {
		t.Errorf("%d sends reached the daemon, want 1", n)
	}
}

// T3 hasn't written a delegate's row yet when its first call looks (#980): the send looks once more,
// finds it, and goes out after the daemon has been told the thread, so a delegate's send is still
// refused.
func TestACodexSendWaitsForARowT3WroteLate(t *testing.T) {
	slowSeek(t)
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}, misses: 1}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the send: %v", err)
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("the send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
}

// Two changing calls at once, as Codex may make, and a row T3 wrote late (#993): the second waits
// for the first's retry rather than going on before it, so neither send goes out unclassified.
func TestParallelCodexSendsWaitForTheOneRetry(t *testing.T) {
	slowSeek(t)
	f := &fakeDaemon{}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}, misses: 1}
	s := codexInT3(t, f, fd, 200*time.Millisecond)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"})
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if routed, sent := sentAfterRoute(f, "thread-kid"); !sent || !routed {
		t.Errorf("a send reached the daemon (%v) before it was told the thread (%v)", sent, routed)
	}
	if n := len(fd.askedAbout()); n != 2 {
		t.Errorf("T3's database was asked %d times, want 2: the first look and one retry", n)
	}
}

// The one retry is used up, and then the daemon restarts, and its reply to the reconnect's hello
// says it couldn't read the thread, as a brief failure of T3's database makes it (#1000): the first
// send after the reconnect tells the new daemon the route again, which reads it now, rather than
// going out unclassified.
func TestACodexSendAfterAReconnectGetsItsOwnRetry(t *testing.T) {
	slowSeek(t)
	var unread atomic.Bool
	unread.Store(true)
	f := &fakeDaemon{threadUnread: func(proto.Route) bool { return unread.Load() }}
	fd := &finder{threads: map[string]string{"codex-kid": "thread-kid"}}
	s := codexInT3(t, f, fd, 0)
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "hi"}); err != nil {
		t.Fatalf("the first send: %v", err)
	}
	if n := len(f.routesTold()); n != 2 {
		t.Fatalf("the daemon was told the route %d times before the first send, want 2", n)
	}

	f.dropConnections()
	waitFor(t, "the reconnect's hello", func() bool { return f.helloCount() == 2 })
	if _, err := s.link.wait(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	unread.Store(false) // T3's database is back
	if _, err := s.callFrom(context.Background(), codexCallIn("send", "codex-kid"), proto.Request{Op: proto.OpSend, To: "boss", Body: "again"}); err != nil {
		t.Fatalf("the send after the reconnect: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	routes, sends := 0, 0
	for _, r := range f.requests {
		switch {
		case r.Op == proto.OpRoute && r.Route != nil && r.Route.T3Thread == "thread-kid":
			routes++
		case r.Op == proto.OpSend:
			sends++
		}
		if sends == 2 {
			break
		}
	}
	if routes != 3 {
		t.Errorf("the daemon was told the route %d times before the send after the reconnect, want 3", routes)
	}
}
