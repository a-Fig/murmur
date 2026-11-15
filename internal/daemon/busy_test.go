package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// A session Murmur's hook runs in is told of a message by the hook while it is at work, not given
// it as a turn T3 would queue; what it hasn't read when its turn ends goes in as a turn then (#202),
// where the turn's end is a hook that can't keep the turn going, as these tests' turnEnd is. Claude
// Code's Stop hook can, and keeps it going with them instead (busy_continue_test.go).

// hookNotices is what the hook asks for after a prompt or a tool call of Claude session claude.
func (d *device) hookNotices(claude string) proto.Response {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpNotices, ClaudeSession: claude, Whole: true})
	if r.Error != "" {
		d.t.Fatalf("notices: %s", r.Error)
	}
	return r
}

// finishedATurn is the hook through one turn of Claude session claude, a prompt and its end, so
// the daemon knows the session's Stop hook runs.
func (d *device) finishedATurn(claude string) {
	d.t.Helper()
	d.hookNotices(claude)
	d.turnEnd(claude)
}

// turnEnd is the hook at the end of a turn of Claude session claude.
func (d *device) turnEnd(claude string) {
	d.t.Helper()
	if r := d.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: claude}); r.Error != "" {
		d.t.Fatalf("turnend: %s", r.Error)
	}
}

func viewSubjects(ms []proto.MessageView) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Subject)
	}
	return out
}

// While the hook has the session at work, a message is a notice with the count, not a T3 turn. The
// count isn't repeated after every tool call, only when it changes. When the turn ends, both
// messages, still unread, go in as turns, in order.
func TestABusySessionIsToldAndGetsTheTurnWhenIdle(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob") // a prompt: the session is at work
	a.send("aaaaaa", bob, "first", "its body")
	var r proto.Response
	eventually(t, "the notice", func() bool {
		r = b.hookNotices("claude-bob")
		return len(r.Messages) > 0
	})
	if got := viewSubjects(r.Messages); len(got) != 1 || got[0] != "first" || r.Unread != 1 {
		t.Fatalf("notice: %v, %d unread; want [first], 1", got, r.Unread)
	}
	if r := b.hookNotices("claude-bob"); len(r.Messages) != 0 || r.Unread != 0 {
		t.Errorf("the next tool call was told %v, %d unread; want nothing, the count unchanged", viewSubjects(r.Messages), r.Unread)
	}
	a.send("aaaaaa", bob, "second", "its body")
	eventually(t, "the second notice", func() bool {
		r = b.hookNotices("claude-bob")
		return len(r.Messages) > 0
	})
	if got := viewSubjects(r.Messages); len(got) != 1 || got[0] != "second" || r.Unread != 2 {
		t.Errorf("notice: %v, %d unread; want [second], 2", got, r.Unread)
	}
	holds(t, "nothing handed to T3 while at work", time.Second, func() bool { return len(calls()) == 0 })

	b.turnEnd("claude-bob")
	eventually(t, "both as turns", func() bool { return len(calls()) == 2 })
	if !strings.Contains(calls()[0].Stdin, "first") || !strings.Contains(calls()[1].Stdin, "second") {
		t.Errorf("the turns: %q, %q", calls()[0].Stdin, calls()[1].Stdin)
	}
	eventually(t, "nothing unread", func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0
	})
	holds(t, "no more turns", time.Second, func() bool { return len(calls()) == 2 })
}

// A message the agent read with inbox during its turn is not given to it again when the turn ends.
func TestAMessageReadDuringTheTurnIsNotATurnAfter(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "read me now", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	if got := viewSubjects(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages); len(got) != 1 {
		t.Fatalf("inbox: %v", got)
	}
	b.turnEnd("claude-bob")
	holds(t, "no turn for a message already read", 1500*time.Millisecond, func() bool { return len(calls()) == 0 })
}

// A message that comes while the session is idle wakes it as before, and one that comes during a
// turn with no tool call after it, so nothing was told, goes in when the turn ends.
func TestAnIdleSessionIsWokenAndAnUntoldMessageWaitsForTheTurnEnd(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	b.turnEnd("claude-bob")
	a.send("aaaaaa", bob, "while idle", "its body")
	eventually(t, "the idle session woken", func() bool { return len(calls()) == 1 })

	b.hookNotices("claude-bob") // the turn it was woken for
	a.send("aaaaaa", bob, "late in the turn", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "late in the turn") })
	holds(t, "no turn while at work", time.Second, func() bool { return len(calls()) == 1 })
	b.turnEnd("claude-bob")
	eventually(t, "the turn after", func() bool { return len(calls()) == 2 })
	if !strings.Contains(calls()[1].Stdin, "late in the turn") {
		t.Errorf("the turn: %q", calls()[1].Stdin)
	}
}

// A subagent's tool call during the session's turn tells it nothing: the notice would go to the
// subagent (#188).
func TestASubagentsCallTellsNothing(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob") // the turn the subagent works in
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: true})
	a.send("aaaaaa", bob, "for the parent", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "for the parent") })
	holds(t, "nothing told to the subagent, no turn", time.Second, func() bool {
		r := b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: true})
		return len(r.Messages) == 0 && r.Unread == 0 && len(calls()) == 0
	})
	if got := viewSubjects(b.hookNotices("claude-bob").Messages); len(got) != 1 {
		t.Errorf("the session's own next tool call was told %v", got)
	}
}

// In T3, a turn that ends without the Stop hook, as one the user interrupts does, ends when a
// read of T3's database that began after the hook last ran finds no run: the message isn't held
// for good. That holds when a late hook came after the run's recorded end, too.
func TestAnInterruptedTurnInT3EndsByT3sDatabase(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {Busy: true, LastRun: time.Now()}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "after the stop", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	holds(t, "no turn while the run goes on", time.Second, func() bool { return len(calls()) == 0 })
	// The run ended a minute ago by T3's clock, before the hook's last call: a late hook.
	threads.set("thread-bob", t3db.State{LastRun: time.Now().Add(-time.Minute)})
	eventually(t, "the turn once a read finds no run", func() bool { return len(calls()) == 1 })
}

// A subagent working in the background after the session's turn ended doesn't hold the session's
// messages: its tool calls don't mark the session at work.
func TestABackgroundSubagentDoesntHoldTheSession(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: true})
	a.send("aaaaaa", bob, "while the subagent works", "its body")
	eventually(t, "the turn", func() bool { return len(calls()) == 1 })
}

// A clone joined before #202 has no Stop hook until join runs again. Outside T3, until the daemon
// has seen a turn end, a busy session gets its message as a turn, as before: held, it would never
// be seen to finish its turn.
func TestBeforeTheFirstStopABusySessionGetsTheTurn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "no stop hook", "its body")
	eventually(t, "the turn", func() bool { return len(calls()) == 1 })
}

// reconnected registers a session that was registered with a daemon that went away, as a session's
// MCP server does after the daemon restarts.
func (d *device) reconnected(id, claude string, r proto.Route) string {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "test", ClaudeSession: claude, Route: r, Reconnect: true})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self
}

// A session that reconnects after a daemon restart, before its hook has run again, is at work while
// T3 shows its run going, and for its first minute until its thread has been read at all: what was
// held for the end of its turn mustn't go to T3's queue mid-turn. A subagent's hook doesn't end
// that; the session's own does.
func TestAReconnectedSessionIsHeldWhileItsRunGoes(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{}} // no thread read yet
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob := b.reconnected("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "after the restart", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "after the restart") })
	holds(t, "no turn before the thread is read", time.Second, func() bool { return len(calls()) == 0 })
	// A subagent's command, bracketed like any (#21), is a subagent's hook too.
	b.do(proto.Request{Op: proto.OpToolStart, ClaudeSession: "claude-bob", Subagent: true, ToolUseID: "toolu_sub"})
	b.do(proto.Request{Op: proto.OpToolEnd, ClaudeSession: "claude-bob", Subagent: true, ToolUseID: "toolu_sub"})
	holds(t, "no turn after a subagent's command before the thread is read", time.Second, func() bool { return len(calls()) == 0 })

	threads.set("thread-bob", t3db.State{Busy: true, LastRun: time.Now()})
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: true})
	holds(t, "no turn while the run goes, a subagent's hook or not", 1500*time.Millisecond, func() bool { return len(calls()) == 0 })

	threads.set("thread-bob", t3db.State{LastRun: time.Now()})
	eventually(t, "the turn once the run is over", func() bool { return len(calls()) == 1 })
}
