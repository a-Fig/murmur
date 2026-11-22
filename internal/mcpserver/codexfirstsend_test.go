package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
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
	f := &fakeDaemon{}
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
	f := &fakeDaemon{}
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
	f := &fakeDaemon{helloGate: make(chan struct{})}
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
