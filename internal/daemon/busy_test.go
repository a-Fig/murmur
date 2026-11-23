package daemon_test

import (
	"context"
	"os"
	"path/filepath"
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

// handedTurn is the hook through the turn a message handed to T3 started, call: its prompt, as
// Claude Code's UserPromptSubmit sends it, is what T3 was given, then its end.
func (d *device) handedTurn(claude string, call t3Call) {
	d.t.Helper()
	if r := d.do(proto.Request{Op: proto.OpNotices, ClaudeSession: claude, Whole: true, Prompt: call.Stdin}); r.Error != "" {
		d.t.Fatalf("notices: %s", r.Error)
	}
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
// messages, still unread, go in as turns, in order: the second once the turn the first started has
// ended, not queued behind it in T3 (#607).
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
	eventually(t, "the first as a turn", func() bool { return len(calls()) == 1 })
	holds(t, "the second not queued behind it", time.Second, func() bool { return len(calls()) == 1 })
	b.handedTurn("claude-bob", calls()[0]) // the turn the first started
	eventually(t, "the second as a turn", func() bool { return len(calls()) == 2 })
	if !strings.Contains(calls()[0].Stdin, "first") || !strings.Contains(calls()[1].Stdin, "second") {
		t.Errorf("the turns: %q, %q", calls()[0].Stdin, calls()[1].Stdin)
	}
	eventually(t, "nothing unread", func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0
	})
	holds(t, "no more turns", time.Second, func() bool { return len(calls()) == 2 })
}

// A turn the hook sees start and end while `t3 thread send` is still handing it over is the one the
// send started: the next message goes when it ends, not after the minute a handoff holds the
// session for a turn the hook hasn't seen (#677). A T3 slow to answer, as on a loaded machine, left
// the send returning after the turn's end, and the hold then covered a turn already over.
//
// One device (#1010): the sender is a session beside bob, as nothing here turns on where a
// message came from. deliver_repeat_test.go says why that is enough.
func TestATurnSeenBeforeItsHandoffReturnsHoldsNothing(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), fakeT3Sleep), []byte("3s"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})

	b.finishedATurn("claude-bob")
	b.send("aaaaaa", bob, "first", "its body")
	eventually(t, "the first handed to T3", func() bool { return len(calls()) == 1 })
	b.handedTurn("claude-bob", calls()[0]) // the turn the first started, over before the send returns
	b.send("aaaaaa", bob, "second", "its body")
	// Well within the minute of the hold, which the second waited out before.
	for end := time.Now().Add(30 * time.Second); len(calls()) < 2; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the second wasn't handed over within 30 s of the turn's end")
		}
	}
	if !strings.Contains(calls()[1].Stdin, "second") {
		t.Errorf("the second turn: %q", calls()[1].Stdin)
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

// T3 has a run going from when it takes a prompt, and Claude Code fires UserPromptSubmit only when
// it starts the turn, at times minutes later (#607). A message that comes in between waits for the
// hook, as in any turn, not in T3's queue as a turn of its own.
func TestARunTheHookHasntSeenStartHoldsTheMessage(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-bob": {LastRun: time.Now()}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	// The user types in the thread: T3 has the run going, and Claude Code hasn't started the turn.
	// Not at once: a read within a second of Stop may find the run Stop ended still open.
	time.Sleep(1200 * time.Millisecond)
	threads.set("thread-bob", t3db.State{Busy: true, LastRun: time.Now()})
	n := threads.reads()
	eventually(t, "a read that began after the run started", func() bool { return threads.reads() >= n+2 })
	a.send("aaaaaa", bob, "before the prompt's hook", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "before the prompt's hook") })
	holds(t, "no turn while the run goes", time.Second, func() bool { return len(calls()) == 0 })

	// The turn starts, and the hook tells of it.
	if got := viewSubjects(b.hookNotices("claude-bob").Messages); len(got) != 1 {
		t.Fatalf("the prompt's hook told of %q, want the message", got)
	}
	threads.set("thread-bob", t3db.State{LastRun: time.Now()})
	b.turnEnd("claude-bob")
	eventually(t, "the turn after the run", func() bool { return len(calls()) == 1 })
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
