package daemon

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// Codex's and Grok's hooks (#225): finding the session a hook speaks for, and telling when a
// session's hook isn't running.

func agentHookDaemon(t *testing.T) *Daemon {
	return &Daemon{dir: t.TempDir(), log: log.New(io.Discard, "", 0), device: "here-0001", started: time.Now(),
		sessions: map[string]*session{}, threadRead: map[threadKey]time.Time{}, peers: map[string]*peer{},
		st: state{Inbox: map[string][]*inMsg{}}, pushKick: make(chan struct{}, 1)}
}

// A Codex session's MCP server learns its thread's ID only from a call to one of Murmur's tools.
// Before that, the hook finds the session by its process (an ancestor of the hook's), and the
// session takes the hook's ID, by which later hooks find it wherever they run from.
func TestACodexHookFindsItsSessionByProcessThenByID(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	codex := d.newSessionLocked(&proto.Hello{ID: "cccccc", ClientPID: 700}, "codex in T3", "", "cccccc")
	codex.clientPID = 700
	claude := d.newSessionLocked(&proto.Hello{ID: "aaaaaa", ClientPID: 700}, "claude-code", "", "aaaaaa")
	claude.clientPID = 700

	got := d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-1", Ancestors: []int{900, 800, 700}})
	if len(got) != 1 || got[0] != codex || codex.hookNative != "thread-1" {
		t.Fatalf("by process: %v, hookNative %q", ids(got), codex.hookNative)
	}
	got = d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-1", Ancestors: []int{901}})
	if len(got) != 1 || got[0] != codex {
		t.Fatalf("by the ID it took: %v", ids(got))
	}
	// Another thread of the same process isn't this session's: it has an ID, and it isn't that one.
	if got := d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-2", Ancestors: []int{700}}); len(got) != 0 {
		t.Fatalf("another thread: %v", ids(got))
	}
	// Nor is a Claude Code hook a Codex one's, or the other way round.
	if got := d.hookSessionsLocked(proto.Request{Agent: "grok", ClaudeSession: "g", Ancestors: []int{700}}); len(got) != 0 {
		t.Fatalf("a grok hook: %v", ids(got))
	}
	if claude.hookNative != "" || claude.hookSeen {
		t.Fatal("a Codex hook touched the Claude Code session")
	}
}

// The ID the MCP server learned (the route's Native) finds the session without its process.
func TestAnAgentHookFindsItsSessionByTheRoutesID(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.newSessionLocked(&proto.Hello{ID: "gggggg"}, "grok", "", "gggggg")
	s.route.Native = "grok-session"
	got := d.touchHookLocked(proto.Request{Op: proto.OpNotices, Agent: "grok", ClaudeSession: "grok-session"})
	if len(got) != 1 || got[0] != s || !s.hookSeen || !s.hooked {
		t.Fatalf("by Native: %v, hookSeen %v, hooked %v", ids(got), s.hookSeen, s.hooked)
	}
}

// Muse's hook (#461) gives the session's MUSE_SESSION_ID, by which its MCP server registered, and
// finds the session by it. A side session Muse runs in the turn, whose hook comes from the same
// process with an ID of its own, is not the session's, nor does the session take its ID.
func TestAMuseHookFindsItsSessionByItsID(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.newSessionLocked(&proto.Hello{ID: "mmmmmm", ClientPID: 700}, "muse in T3", "", "mmmmmm")
	s.clientPID, s.route.Native = 700, "01a1e597-b944"
	if got := d.touchHookLocked(proto.Request{Op: proto.OpNotices, Agent: "muse", ClaudeSession: "01a1e597-c358", Ancestors: []int{800, 700}}); len(got) != 0 || s.hookNative != "" || s.hooked {
		t.Fatalf("a side session's hook took %v, hookNative %q, hooked %v", ids(got), s.hookNative, s.hooked)
	}
	got := d.touchHookLocked(proto.Request{Op: proto.OpNotices, Agent: "muse", ClaudeSession: "01a1e597-b944", Ancestors: []int{800, 700}})
	if len(got) != 1 || got[0] != s || !s.hooked {
		t.Fatalf("by its ID: %v, hooked %v", ids(got), s.hooked)
	}
}

// Two sessions of one process with no ID yet (T3 runs every Codex thread in one app server):
// nothing tells them apart, so the hook speaks for neither, but both have a hook that runs.
func TestAnAgentHookOfSeveralUnknownSessionsTakesNone(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.newSessionLocked(&proto.Hello{ID: "aaaaaa"}, "codex in T3", "", "aaaaaa")
	b := d.newSessionLocked(&proto.Hello{ID: "bbbbbb"}, "codex in T3", "", "bbbbbb")
	a.clientPID, b.clientPID = 700, 700
	got := d.touchHookLocked(proto.Request{Op: proto.OpNotices, Agent: "codex", ClaudeSession: "thread-1", Ancestors: []int{800, 700}})
	if len(got) != 0 || a.hookNative != "" || b.hookNative != "" {
		t.Fatalf("took %v", ids(got))
	}
	if !a.hookSeen || !b.hookSeen || a.hooked || b.hooked {
		t.Fatalf("hookSeen %v %v, hooked %v %v", a.hookSeen, b.hookSeen, a.hooked, b.hooked)
	}
}

// A process with a known session and one without an ID may run a third conversation the daemon
// hasn't heard of yet, so a hook it doesn't know speaks for neither. And once the MCP server learns
// its session's ID, a different one taken from a hook no longer finds the session.
func TestAnAgentHookNeedsMoreThanAProcessOfSeveral(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.newSessionLocked(&proto.Hello{ID: "aaaaaa"}, "codex in T3", "", "aaaaaa")
	b := d.newSessionLocked(&proto.Hello{ID: "bbbbbb"}, "codex in T3", "", "bbbbbb")
	a.clientPID, b.clientPID = 700, 700
	a.route.Native = "thread-a"
	if got := d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-c", Ancestors: []int{700}}); len(got) != 0 || b.hookNative != "" {
		t.Fatalf("took %v, hookNative %q", ids(got), b.hookNative)
	}

	b.hookNative, b.route.Native = "thread-c", "thread-b"
	if got := d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-c", Ancestors: []int{900}}); len(got) != 0 || b.hookNative != "" {
		t.Fatalf("the stale ID found %v, hookNative %q", ids(got), b.hookNative)
	}
	if got := d.hookSessionsLocked(proto.Request{Agent: "codex", ClaudeSession: "thread-b", Ancestors: []int{900}}); len(got) != 1 || got[0] != b {
		t.Fatalf("by its own ID: %v", ids(got))
	}
}

// A session counts as having no hook once it called one of Murmur's tools a while ago and no hook
// of it has reported: only for agents join installs the hook in, and never once one has.
func TestHookOff(t *testing.T) {
	d := agentHookDaemon(t)
	d.mu.Lock()
	defer d.mu.Unlock()
	long := time.Now().Add(-2 * hookGrace)
	for _, c := range []struct {
		agent      string
		toolCalled time.Time
		seen       bool
		want       bool
	}{
		{"codex", long, false, true},
		{"codex in T3", long, false, true},
		{"claude-code", long, false, true},
		{"grok", long, false, true},
		{"pi", long, false, true},
		{"pi in T3", long, false, true},
		{"muse", long, false, true},
		{"muse in T3", long, false, true},
		{"antigravity", long, false, true},
		{"antigravity in T3", long, false, true},
		{"codex", long, true, false},
		{"codex", time.Now(), false, false},  // in its grace
		{"codex", time.Time{}, false, false}, // no tool call: maybe nothing to tell
		{"opencode", long, false, false},     // no hook to run
	} {
		s := &session{agent: c.agent, toolCalled: c.toolCalled, hookSeen: c.seen}
		if got := d.hookOffLocked(s); got != c.want {
			t.Errorf("%s, called %v, seen %v: %v, want %v", c.agent, c.toolCalled, c.seen, got, c.want)
		}
	}

	s := d.newSessionLocked(&proto.Hello{ID: "cccccc"}, "codex", "", "cccccc")
	s.toolCalled = long
	d.peers["peer-0002"] = &peer{pres: bus.Presence{Sessions: []bus.SessionInfo{{ID: "dddddd", HookOff: true}, {ID: "eeeeee"}}}}
	for _, c := range []struct {
		dev, sess string
		want      bool
	}{{"here-0001", "cccccc", true}, {"here-0001", "zzzzzz", false}, {"peer-0002", "dddddd", true}, {"peer-0002", "eeeeee", false}, {"gone-0003", "dddddd", false}} {
		if got, _ := d.peerHookOffLocked(c.dev, c.sess); got != c.want {
			t.Errorf("%s/%s: %v, want %v", c.dev, c.sess, got, c.want)
		}
	}
}

func ids(ss []*session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.id)
	}
	return out
}

// A send to a session whose hook isn't running says "it gets this as its next turn" only when the
// daemon has a turn-delivery route for it (#888): Antigravity in T3 (whose MCP server has no
// T3Thread), a T3 session without a thread, or a Claude Code session without a messaging socket
// gets "it sees this when it next reads its inbox." instead.
func TestPeerCanTurnAndHookOffSendNote(t *testing.T) {
	d, _, _ := rateRig(t, rateOptions(), false)
	long := time.Now().Add(-2 * hookGrace)
	addLocal := func(id, agent string, route proto.Route, dropped bool) string {
		d.mu.Lock()
		defer d.mu.Unlock()
		s := d.newSessionLocked(&proto.Hello{ID: id, Route: route}, agent, "", id)
		s.route = route
		s.toolCalled = long
		if dropped {
			s.dropped = time.Now()
		}
		return bus.Addr(d.device, id)
	}

	agyT3 := addLocal("agyt30", "antigravity in T3", proto.Route{T3CLI: "/t3/bin/t3"}, false)
	codexNoThread := addLocal("cdx000", "codex in T3", proto.Route{T3CLI: "/t3/bin/t3"}, false)
	codexT3 := addLocal("cdx111", "codex in T3", proto.Route{T3CLI: "/t3/bin/t3", T3Thread: "th-codex"}, false)
	claudeNoSock := addLocal("cld000", "claude-code", proto.Route{}, false)
	claudeSock := addLocal("cld111", "claude-code", proto.Route{MsgSocket: "/tmp/s", MsgToken: "tok"}, false)
	claudeSockDropped := addLocal("cld222", "claude-code", proto.Route{MsgSocket: "/tmp/s", MsgToken: "tok"}, true)
	claudeT3Dropped := addLocal("cld333", "claude-code in T3", proto.Route{T3CLI: "/t3/bin/t3", T3Thread: "th-c"}, true)
	codexCLI := addLocal("cdx222", "codex", proto.Route{CodexCLI: "/bin/codex", Native: "th-1"}, false)
	agyLocal := addLocal("agy000", "antigravity", proto.Route{}, false)

	d.mu.Lock()
	if d.st.Pins == nil {
		d.st.Pins = map[string]*keyPin{}
	}
	d.st.Pins["peer-0002"] = &keyPin{Box: bus.BoxPublic(bus.NewBoxKey())}
	d.peers["peer-0002"] = &peer{
		lastChange: time.Now(),
		pres: bus.Presence{
			Device: "peer-0002",
			At:     time.Now().UTC(),
			Sessions: []bus.SessionInfo{
				{ID: "p11111", Agent: "antigravity in T3", HookOff: true},
				{ID: "p22222", Agent: "claude-code in T3", HookOff: true},
				{ID: "p33333", Agent: "claude-code", HookOff: true},
				{ID: "p44444", Agent: "codex", HookOff: true},
			},
		},
	}
	for _, tc := range []struct {
		addr, agent string
		want        bool
	}{
		{agyT3, "antigravity in T3", false},
		{codexNoThread, "codex in T3", false},
		{codexT3, "codex in T3", true},
		{claudeNoSock, "claude-code", false},
		{claudeSock, "claude-code", true},
		{claudeSockDropped, "claude-code", false},
		{claudeT3Dropped, "claude-code in T3", true},
		{codexCLI, "codex", true},
		{agyLocal, "antigravity", false},
		{"peer-0002/p11111", "antigravity in T3", false},
		{"peer-0002/p22222", "claude-code in T3", true},
		{"peer-0002/p33333", "claude-code", true},
		{"peer-0002/p44444", "codex", false},
	} {
		dev, sess := bus.SplitAddr(tc.addr)
		if got := d.peerCanTurnLocked(dev, sess, tc.agent); got != tc.want {
			t.Errorf("peerCanTurnLocked(%s, %q) = %v, want %v", tc.addr, tc.agent, got, tc.want)
		}
	}
	d.mu.Unlock()

	ctx := context.Background()
	for _, tc := range []struct {
		what, to, wantSubstr, forbidSubstr string
	}{
		{"local antigravity in T3 without route", agyT3, "when it next reads its inbox.", "as its next turn"},
		{"local codex in T3 without T3Thread", codexNoThread, "when it next reads its inbox.", "as its next turn"},
		{"local codex in T3 with T3Thread", codexT3, "as its next turn.", "next reads its inbox"},
		{"local claude-code without socket", claudeNoSock, "when it next reads its inbox.", "as its next turn"},
		{"local claude-code with socket", claudeSock, "as its next turn.", "next reads its inbox"},
		{"local codex with CodexCLI and Native", codexCLI, "as its next turn.", "next reads its inbox"},
		{"remote antigravity in T3", "peer-0002/p11111", "when it next reads its inbox.", "as its next turn"},
		{"remote claude-code in T3", "peer-0002/p22222", "as its next turn.", "next reads its inbox"},
	} {
		resp := d.handle(ctx, proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: tc.to, Subject: "ping"})
		if !resp.OK {
			t.Fatalf("%s: send failed: %s", tc.what, resp.Error)
		}
		if !strings.Contains(resp.Note, tc.wantSubstr) || strings.Contains(resp.Note, tc.forbidSubstr) {
			t.Errorf("%s: note = %q, want %q and not %q", tc.what, resp.Note, tc.wantSubstr, tc.forbidSubstr)
		}
	}
}
