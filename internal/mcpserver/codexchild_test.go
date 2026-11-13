package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
)

// A subagent Codex spawned says so in its tool calls, and its server registers it as a child
// (#487). The _meta here is the shape Codex 0.164 and 0.167 send (2026-11-12).

// subagentCall is a tool call of thread, which Codex spawned from parent.
func subagentCall(thread, parent string) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "send"}}
	req.Params.SetMeta(map[string]any{
		"threadId":  thread,
		"sessionId": parent,
		"x-codex-turn-metadata": map[string]any{
			"thread_id": thread, "parent_thread_id": parent, "thread_source": "subagent",
			"subagent_kind": "thread_spawn",
		},
	})
	return req
}

// codexLink starts a link for a Codex session outside T3 against f, the way the server does.
func codexLink(t *testing.T, f *fakeDaemon) *server {
	t.Helper()
	for _, k := range []string{"T3CODE_CLI_PATH", "T3CODE_HOME", "CODEX_HOME"} {
		t.Setenv(k, "")
	}
	gitDir := f.serve(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c := &client.Client{GitDir: gitDir}
	l := &link{c: c, hello: helloFor("codex", "/work"), changed: make(chan struct{}), seek: currentSeek()}
	go l.run(ctx)
	return &server{c: c, link: l}
}

func TestCodexCallOf(t *testing.T) {
	if c := codexCallOf(subagentCall("th-kid", "th-main").Params.GetMeta()); c != (codexCall{thread: "th-kid", parent: "th-main", child: true}) {
		t.Errorf("a subagent's call: %+v", c)
	}
	person := map[string]any{"threadId": "th-1", "x-codex-turn-metadata": map[string]any{"thread_source": "user", "parent_thread_id": "th-0"}}
	if c := codexCallOf(person); c != (codexCall{thread: "th-1"}) {
		t.Errorf("a person's call: %+v", c)
	}
	if c := codexCallOf(map[string]any{"threadId": "th-2"}); c != (codexCall{thread: "th-2"}) {
		t.Errorf("a call with no turn metadata: %+v", c)
	}
	if c := codexCallOf(nil); c != (codexCall{}) {
		t.Errorf("no _meta: %+v", c)
	}
}

// Codex starts a subagent's server for its first call: the server waits for that call before it
// registers, and registers as a child, with its parent.
func TestACodexSubagentRegistersAsAChild(t *testing.T) {
	f := &fakeDaemon{}
	s := codexLink(t, f)
	time.Sleep(150 * time.Millisecond)
	if n := f.helloCount(); n != 0 {
		t.Fatalf("registered %d times before the first call", n)
	}
	s.noteMeta(context.Background(), subagentCall("th-kid", "th-main"))
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
	if h := f.lastHello(); h.Native != "th-kid" || !h.Child || h.ParentNative != "th-main" {
		t.Errorf("hello: %+v", h.Route)
	}
}

// A person's thread gets its server with no call: it registers once the wait is up, as no child.
// A subagent's call that comes after that tells the daemon before noteMeta returns, so the call
// that brought it is a child's.
func TestACodexSessionWithNoCallRegistersAfterTheWait(t *testing.T) {
	wait := codexFirstCallWait
	codexFirstCallWait = 50 * time.Millisecond
	t.Cleanup(func() { codexFirstCallWait = wait })
	f := &fakeDaemon{}
	s := codexLink(t, f)
	waitFor(t, "the server to register", func() bool { return f.helloCount() > 0 })
	if h := f.lastHello(); h.Native != "" || h.Child {
		t.Errorf("hello: %+v", h.Route)
	}
	if _, err := s.link.wait(context.Background(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.noteMeta(context.Background(), subagentCall("th-late", "th-main"))
	rs := f.routesTold()
	if len(rs) == 0 {
		t.Fatal("noteMeta returned before the daemon was told the session is a child")
	}
	if r := rs[len(rs)-1]; r.Native != "th-late" || !r.Child || r.ParentNative != "th-main" {
		t.Errorf("route told: %+v", r)
	}
}
