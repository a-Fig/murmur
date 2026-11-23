package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/proto"
)

// Muse, Pi and Grok give the server its session's own ID in the environment, and the hello carries
// it, with an ID made from it; what Claude Code put in an environment they passed on isn't theirs.
func TestHelloTakesTheSessionFromTheAgentsVariable(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff") // a Claude Code session's, inherited
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok")
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	for agent, v := range nativeEnv {
		t.Run(agent, func(t *testing.T) {
			t.Setenv(v, "01a190f5-a509-7000-bd17-2120db057488")
			h := helloFor(agent, "/work")
			if h.Native != "01a190f5-a509-7000-bd17-2120db057488" || h.ID != nativeID(h.Native) || len(h.ID) != 6 {
				t.Errorf("hello: %+v", h)
			}
			if h.ClaudeSession != "" || h.MsgSocket != "" || h.MsgToken != "" {
				t.Errorf("took Claude Code's variables: %+v", h)
			}
			if h.Agent != agent+" in T3" || h.T3CLI != "/t3/bin/t3" {
				t.Errorf("agent %q, route %+v", h.Agent, h.Route)
			}
		})
	}
	// Two IDs made in the same second, as Muse's and OpenCode's begin with the time, get different
	// Murmur IDs.
	if nativeID("01a190f5-a509-7000-bd17-2120db057488") == nativeID("01a190f5-a50a-7000-8000-000000000000") {
		t.Error("two sessions share an ID")
	}
}

// Pi's hello carries the socket Murmur's extension opened in the session, the way Claude Code's
// carries its messaging socket, and no other agent takes those variables.
func TestPiHelloCarriesTheExtensionsSocket(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MESSAGING_SOCKET", "/tmp/claude.sock")
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", "tok")
	t.Setenv("MURMUR_PI_MESSAGING_SOCKET", "/tmp/murmur-pi-1.sock")
	t.Setenv("MURMUR_PI_MESSAGING_TOKEN", "pi-tok")
	t.Setenv("MURMUR_PI_SESSION_FILE", "/home/u/.pi/agent/sessions/a.jsonl")
	if h := helloFor("pi", "/work"); h.MsgSocket != "/tmp/murmur-pi-1.sock" || h.MsgToken != "pi-tok" || h.Native == "" {
		t.Errorf("pi: %+v", h)
	}
	for _, agent := range []string{"muse", "grok", "opencode", "codex"} {
		if h := helloFor(agent, "/work"); h.MsgSocket != "" || h.MsgToken != "" {
			t.Errorf("%s took a socket: %+v", agent, h.Route)
		}
	}
}

// After Pi moves its session to another file (/new, /resume, /fork), the server says the file the
// session left, and goes by the address the daemon gives it, that of the session it left, from
// then on (#1145). While that address stays in use, it gives the file up after a few tries and is
// a session of its own, at the ID made from its file, as before.
func TestAMovedPiServerTakesTheAddressOfTheSessionItLeft(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	t.Setenv("MURMUR_PI_SESSION_FILE", "/s/b.jsonl")
	t.Setenv("MURMUR_PI_PREVIOUS_SESSION_FILE", "/s/a.jsonl")
	if h := helloFor("pi", "/work"); h.From != "/s/a.jsonl" || !h.TakesSelf || h.Native != "/s/b.jsonl" || h.ID != nativeID("/s/b.jsonl") {
		t.Fatalf("pi: %+v", h)
	}
	if h := helloFor("muse", "/work"); h.From != "" {
		t.Errorf("muse took Pi's file: %+v", h)
	}

	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.From != "" {
			return "", "0a0a0a"
		}
		return "", ""
	}}
	l := startedLink(t, f, nil, helloFor("pi", "/work"))
	if id, _ := l.wait(context.Background(), time.Second); id != "0a0a0a" {
		t.Fatalf("registered as %s, not the address the daemon gave", id)
	}
	f.dropConnections()
	waitFor(t, "a reconnection", func() bool { return f.helloCount() == 2 })
	if h := f.lastHello(); h.ID != "0a0a0a" || !h.Reconnect {
		t.Errorf("the reconnection: %+v", h)
	}

	g := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.From != "" {
			return "session ID 0a0a0a is in use", ""
		}
		return "", ""
	}}
	startedLink(t, g, nil, helloFor("pi", "/work"))
	if h := g.lastHello(); h.ID != nativeID("/s/b.jsonl") || h.From != "" {
		t.Errorf("past the address in use, registered with %+v", h)
	}
}

// A server that both made its ID from a T3 thread and says the conversation it left gives both up
// together once its address stays in use: one round of retries, about 7 s, not two.
func TestAServerGivesUpTheThreadAndTheFileLeftTogether(t *testing.T) {
	fastSeek(t)
	f := &fakeDaemon{helloReply: func(h proto.Hello) (string, string) {
		if h.ByThread || h.From != "" {
			return "session ID " + h.ID + " is in use", ""
		}
		return "", ""
	}}
	h := proto.Hello{ID: threadID("thread-1"), Agent: "pi in T3", ByThread: true, From: "/s/a.jsonl",
		Route: proto.Route{T3CLI: "/t3/bin/t3", T3Thread: "thread-1", Native: "/s/b.jsonl"}}
	startedLink(t, f, &finder{}, h)
	if n := f.helloCount(); n != 4 {
		t.Errorf("registered after %d hellos, not 3 refused and 1 taken", n)
	}
	if h := f.lastHello(); h.ByThread || h.From != "" || h.ID != nativeID("/s/b.jsonl") {
		t.Errorf("registered with %+v", h)
	}
}

func openCodeCall(session string) *mcp.CallToolRequest {
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "who"}}
	if session != "" {
		req.Params.SetMeta(map[string]any{openCodeSessionMeta: session})
	}
	return req
}

// One OpenCode server serves every session in its directory: each session that calls is
// registered on its own, by the ID the call names, and found in T3 by it.
func TestASharedServerRegistersEachSessionThatCalls(t *testing.T) {
	fastSeek(t)
	t.Setenv("T3CODE_CLI_PATH", "/t3/bin/t3")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff")
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	finds := &finder{threads: map[string]string{"ses_aaa": "thread-a", "ses_bbb": "thread-b"}}
	old := threadOf
	threadOf = func(ctx context.Context, _, _, native string) (string, error) { return finds.find(ctx, native) }
	t.Cleanup(func() { threadOf = old })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &server{opt: Options{Agent: "opencode"}, cwd: "/work", place: p, c: &client.Client{GitDir: p.GitDir},
		links: map[string]*sessionLink{}, lctx: ctx}
	if f.helloCount() != 0 {
		t.Fatal("registered before any call")
	}
	for _, ses := range []string{"ses_aaa", "ses_bbb", "ses_aaa"} {
		if _, err := s.callFrom(ctx, openCodeCall(ses), proto.Request{Op: proto.OpWho}); err != nil {
			t.Fatalf("%s: %v", ses, err)
		}
	}
	if n := len(s.links); n != 2 {
		t.Errorf("%d sessions, want 2", n)
	}
	threads := map[string]string{}
	f.mu.Lock()
	hellos := append([]proto.Hello(nil), f.hellos...)
	f.mu.Unlock()
	for _, h := range hellos {
		threads[h.Native] = h.T3Thread
		if h.ClaudeSession != "" || h.ID != nativeID(h.Native) || h.Agent != "opencode in T3" {
			t.Errorf("hello: %+v", h)
		}
	}
	if threads["ses_aaa"] != "thread-a" || threads["ses_bbb"] != "thread-b" {
		t.Errorf("threads: %v", threads)
	}

	// A session quiet for longer than sharedIdle is dropped at the next call of another.
	old2 := sharedIdle
	sharedIdle = 0
	t.Cleanup(func() { sharedIdle = old2 })
	time.Sleep(10 * time.Millisecond)
	if _, err := s.callFrom(ctx, openCodeCall("ses_bbb"), proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.links["ses_aaa"]; ok || len(s.links) != 1 {
		t.Errorf("sessions left: %v", s.links)
	}

	// A call that names no session (an older OpenCode) is one session for the whole process.
	if _, err := s.callFrom(ctx, openCodeCall(""), proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.links[""]; !ok {
		t.Error("no session for the calls that name none")
	}
}

// OpenCode 1 names no session in a call, so its server's sessions are one, by the ID Murmur's
// plugin put in the server's environment, with the plugin's socket as its way in (#446). A call
// that names its session, as OpenCode 2's do, is that session's, and the socket isn't its.
func TestOpenCode1IsTheSessionThePluginNamed(t *testing.T) {
	fastSeek(t)
	t.Setenv("MURMUR_OPENCODE_SESSION", "9f3c0a7d21b4e865")
	t.Setenv("MURMUR_OPENCODE_MESSAGING_SOCKET", "/tmp/murmur-opencode-9f3c0a7d21b4e865.sock")
	t.Setenv("MURMUR_OPENCODE_MESSAGING_TOKEN", "oc-tok")
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &server{opt: Options{Agent: "opencode"}, cwd: "/work", place: p, c: &client.Client{GitDir: p.GitDir},
		links: map[string]*sessionLink{}, lctx: ctx}
	for range 2 {
		if _, err := s.callFrom(ctx, openCodeCall(""), proto.Request{Op: proto.OpWho}); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	hellos := append([]proto.Hello(nil), f.hellos...)
	f.mu.Unlock()
	if len(hellos) != 1 {
		t.Fatalf("%d hellos, want 1: %+v", len(hellos), hellos)
	}
	h := hellos[0]
	if h.Native != "9f3c0a7d21b4e865" || h.ID != nativeID(h.Native) || h.Agent != "opencode" ||
		h.MsgSocket != "/tmp/murmur-opencode-9f3c0a7d21b4e865.sock" || h.MsgToken != "oc-tok" {
		t.Errorf("hello: %+v", h)
	}

	if h := helloFor("opencode", "/work"); h.MsgSocket == "" || h.Native != "9f3c0a7d21b4e865" {
		t.Errorf("OpenCode 1's hello: %+v", h)
	}
	for _, v := range []string{"MURMUR_OPENCODE_SESSION", "MURMUR_OPENCODE_MESSAGING_SOCKET", "MURMUR_OPENCODE_MESSAGING_TOKEN"} {
		t.Setenv(v, "")
	}
	if _, err := s.callFrom(ctx, openCodeCall("ses_aaa"), proto.Request{Op: proto.OpWho}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	last := f.hellos[len(f.hellos)-1]
	f.mu.Unlock()
	if last.Native != "ses_aaa" || last.MsgSocket != "" || last.MsgToken != "" {
		t.Errorf("OpenCode 2's hello: %+v", last)
	}
}

// Off, a shared server registers nobody.
func TestASharedServerOffRegistersNobody(t *testing.T) {
	s := &server{opt: Options{Agent: "opencode"}}
	if _, err := s.callFrom(context.Background(), openCodeCall("ses_aaa"), proto.Request{Op: proto.OpWho}); err != errPassive {
		t.Errorf("err %v", err)
	}
}
