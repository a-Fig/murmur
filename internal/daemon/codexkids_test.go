package daemon_test

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/title"
)

// Sessions another session started are off the mesh outside T3's own delegates too (#452): the
// subagents Codex spawns, by Codex's state (#454), and sessions a hook names. End to end, with a
// stand-in for Codex's state.

// fakeCodex is the stand-in for Codex's state: who started each thread, as the test sets it. A
// thread it doesn't have is one Codex hasn't written yet, or doesn't keep.
type fakeCodex struct {
	mu  sync.Mutex
	lin map[string]title.Lineage
}

func (f *fakeCodex) set(thread string, l title.Lineage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lin[thread] = l
}

func (f *fakeCodex) read(_ context.Context, _, thread string) (title.Lineage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.lin[thread]
	if !ok {
		return title.Lineage{}, title.ErrNoThread
	}
	return l, nil
}

// newCodexDevice is newDevice whose Codex state is codex.
func newCodexDevice(t *testing.T, remote string, codex *fakeCodex) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: 3 * time.Second, poll: 100 * time.Millisecond,
		codexLineage: codex.read}
	d.start()
	return d
}

// agentSession registers a session of agent whose MCP server's parent process is pid, with the
// route r.
func (d *device) agentSession(id, agent string, pid int, r proto.Route) (string, net.Conn) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: agent, ClientPID: pid, Route: r})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn
}

// codexIn is the route of a Codex session outside T3 whose MCP server knows its thread.
func codexIn(thread string) proto.Route { return proto.Route{Native: thread, CodexCLI: "codex"} }

// A subagent Codex spawned leaves who, takes no messages and can't send, on both devices; the
// refusals name the session in the thread that spawned it. Only the thread its own MCP server
// names marks a session: a thread ID that came from a hook's pairing doesn't, since a subagent's
// hook can be paired with the session that spawned it. Inside T3 it is T3's to say.
func TestCodexSubagentIsReachedThroughTheThreadThatSpawnedIt(t *testing.T) {
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	codex := &fakeCodex{lin: map[string]title.Lineage{"th-main": {}, "th-spawned": {Subagent: true, Parent: "th-x"},
		"th-hooked": {Subagent: true, Parent: "th-y"}}}
	a, b := newDevice(t, remote), newCodexDevice(t, remote, codex)
	alice, _ := a.session("aaaaaa", "")
	main, _ := b.agentSession("bbbbbb", "codex", 300, codexIn("th-main"))
	sub, _ := b.agentSession("cccccc", "codex", 300, codexIn("th-kid"))
	lone, _ := b.agentSession("dddddd", "codex", 600, proto.Route{CodexCLI: "codex"})
	inT3, _ := b.agentSession("eeeeee", "codex in T3", 300, proto.Route{T3CLI: exe, Native: "th-spawned"})
	mainName, subName := b.named(main), b.named(sub)
	a.nameIs(main, mainName)
	a.nameIs(sub, subName)
	eventually(t, "presence", func() bool {
		return b.seesSession(alice) && a.seesSession(sub) && b.listsSession(lone) && b.listsSession(inT3)
	})

	// The lone session of its process is paired with the hook of a subagent it spawned.
	b.do(proto.Request{Op: proto.OpNotices, Agent: "codex", ClaudeSession: "th-hooked", Ancestors: []int{921, 920, 600, 1}})
	codex.set("th-kid", title.Lineage{Subagent: true, Parent: "th-main"})
	eventually(t, "the subagent to leave both devices' who", func() bool {
		return a.seesSession(main) && !a.listsSession(sub) && !b.listsSession(sub)
	})
	if !b.listsSession(main) {
		t.Error("the thread that spawned the subagent left who")
	}
	if !b.listsSession(lone) {
		t.Error("a session left who on the word of a hook's pairing")
	}
	if !b.listsSession(inT3) {
		t.Error("a spawned thread in T3 left who on Codex's word, which is T3's to give")
	}
	if resp := a.trySend("aaaaaa", subName, "for the subagent"); resp.OK || !strings.Contains(resp.Error, "delegate") {
		t.Errorf("a send to the subagent from another device: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("bbbbbb", subName, "to my subagent"); resp.OK || !strings.Contains(resp.Error, "Message "+mainName+" instead") {
		t.Errorf("a send to the subagent on its device: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("cccccc", alice, "from the subagent"); resp.OK || !strings.Contains(resp.Error, mainName) {
		t.Errorf("the subagent's own send: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("bbbbbb", alice, "from the thread that spawned it"); !resp.OK {
		t.Errorf("the parent's send: %q", resp.Error)
	}
}

// A hook that says its session is another's child (#455) takes it off the mesh, with the parent
// it names, whether the session's MCP server registered before the hook spoke or after.
func TestAHookNamesItsSessionAChild(t *testing.T) {
	remote := newRemote(t)
	b := newCodexDevice(t, remote, &fakeCodex{lin: map[string]title.Lineage{}})
	main, _ := b.agentSession("bbbbbb", "opencode", 700, proto.Route{Native: "ses-main"})
	early, _ := b.agentSession("cccccc", "opencode", 700, proto.Route{Native: "ses-early"})
	mainName, earlyName := b.named(main), b.named(early)
	eventually(t, "the sessions to be listed", func() bool { return b.listsSession(main) && b.listsSession(early) })

	b.do(proto.Request{Op: proto.OpNotices, Native: "ses-early", Child: true, ParentNative: "ses-main"})
	b.do(proto.Request{Op: proto.OpNotices, Native: "ses-late", Child: true, ParentNative: "ses-main"})
	eventually(t, "the child to leave who", func() bool { return !b.listsSession(early) })
	if resp := b.trySend("bbbbbb", earlyName, "to my child"); resp.OK || !strings.Contains(resp.Error, "Message "+mainName+" instead") {
		t.Errorf("a send to the child: ok %v, error %q", resp.OK, resp.Error)
	}
	if resp := b.trySend("cccccc", mainName, "from the child"); resp.OK || !strings.Contains(resp.Error, mainName) {
		t.Errorf("the child's own send: ok %v, error %q", resp.OK, resp.Error)
	}
	// A request that doesn't say so changes nothing.
	b.do(proto.Request{Op: proto.OpNotices, Native: "ses-early"})

	// One named before it registered is a child as it registers, with no refresh between (#487).
	late, _ := b.agentSession("dddddd", "opencode", 700, proto.Route{Native: "ses-late"})
	if resp := b.trySend("dddddd", mainName, "from the late child"); resp.OK || !strings.Contains(resp.Error, mainName) {
		t.Errorf("the first send of a child named before it registered: ok %v, error %q", resp.OK, resp.Error)
	}
	if b.listsSession(late) || b.listsSession(early) || !b.listsSession(main) {
		t.Errorf("who lists late %v, early %v, main %v; want only main", b.listsSession(late), b.listsSession(early), b.listsSession(main))
	}
}

// A Codex tool call that says its thread is a subagent's makes the session a child as the daemon
// hears it (#487): in the hello, so it is never listed and its start is never logged, or in a
// route told later, before the call that brought it. Codex's state isn't needed. Inside T3 it is
// T3's to say.
func TestACodexCallThatSaysSubagentMakesAChildAtOnce(t *testing.T) {
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	a := newDevice(t, remote)
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	b := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: 3 * time.Second, poll: 100 * time.Millisecond,
		grace: 300 * time.Millisecond, codexLineage: (&fakeCodex{lin: map[string]title.Lineage{}}).read}
	b.start()
	alice, _ := a.session("aaaaaa", "")
	main, _ := b.agentSession("bbbbbb", "codex", 300, codexIn("th-main"))
	kid := codexIn("th-kid")
	kid.Child, kid.ParentNative = true, "th-main"
	sub, subConn := b.agentSession("cccccc", "codex", 300, kid)
	mainName := b.named(main)
	if resp := b.trySend("cccccc", alice, "from the subagent"); resp.OK || !strings.Contains(resp.Error, mainName) {
		t.Errorf("the first send of a subagent that registered as one: ok %v, error %q", resp.OK, resp.Error)
	}
	late, _ := b.agentSession("dddddd", "codex", 300, proto.Route{CodexCLI: "codex"})
	inT3 := proto.Route{T3CLI: exe, Native: "th-t3kid", Child: true, ParentNative: "th-main"}
	t3kid, _ := b.agentSession("eeeeee", "codex in T3", 300, inT3)
	eventually(t, "presence", func() bool {
		return a.seesSession(main) && a.listsSession(late) && b.listsSession(t3kid)
	})
	if a.listsSession(sub) || b.listsSession(sub) {
		t.Error("a subagent that registered as one was listed")
	}
	for _, ev := range a.do(proto.Request{Op: proto.OpLog, Limit: 100}).Events {
		if strings.Contains(ev.Line, "cccccc") {
			t.Errorf("the other device's log has the subagent: %q", ev.Line)
		}
	}

	late2 := codexIn("th-late")
	late2.Child, late2.ParentNative = true, "th-main"
	if resp := b.do(proto.Request{Op: proto.OpRoute, Session: "dddddd", Route: &late2}); !resp.OK {
		t.Fatalf("route: %q", resp.Error)
	}
	if resp := b.trySend("dddddd", alice, "from the late subagent"); resp.OK || !strings.Contains(resp.Error, mainName) {
		t.Errorf("a send right after the route said subagent: ok %v, error %q", resp.OK, resp.Error)
	}
	eventually(t, "the late subagent to leave who", func() bool { return !a.listsSession(late) && !b.listsSession(late) })
	if !b.listsSession(t3kid) {
		t.Error("a thread in T3 left who on Codex's word, which is T3's to give")
	}
	if !b.listsSession(main) {
		t.Error("the thread that spawned the subagents left who")
	}

	// Its stop is nobody's news either. A session that stops after it is logged, so by then its
	// own stop would have been too.
	_, otherConn := b.agentSession("ffffff", "codex", 900, proto.Route{CodexCLI: "codex"})
	subConn.Close()
	time.Sleep(time.Second) // past its grace
	otherConn.Close()
	eventually(t, "the other session's stop to reach the other device's log", func() bool {
		for _, ev := range a.do(proto.Request{Op: proto.OpLog, Limit: 100}).Events {
			if strings.Contains(ev.Line, "ffffff") && strings.Contains(ev.Line, "stop") {
				return true
			}
		}
		return false
	})
	for _, ev := range a.do(proto.Request{Op: proto.OpLog, Limit: 100}).Events {
		if strings.Contains(ev.Line, "cccccc") {
			t.Errorf("the other device's log has the subagent: %q", ev.Line)
		}
	}
}
