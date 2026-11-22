package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of a Codex session's name outside T3 (#36). Codex's MCP server learns its thread ID at its
// first tool call and tells the daemon in a route (Native); the name follows that thread.

// native tells the daemon a session's Codex thread, as its MCP server does at its first tool call.
func (d *device) native(id, thread string) {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpRoute, Session: id, Route: &proto.Route{Native: thread}})
	if r.Error != "" {
		d.t.Fatalf("route: %s", r.Error)
	}
}

// A Codex CLI session whose MCP server restarts (a new Murmur session ID, the same Codex thread)
// has its old name back once the thread ID arrives; a session that has already shown the name it
// was given under its Murmur ID keeps that name when it learns its thread.
func TestCodexNameFollowsItsThreadAcrossRestarts(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.session("bbbbbb", "")
	name := b.named(first)
	b.native("bbbbbb", "thread-1")
	b.nameIs(first, name) // the name it had under its own ID stays with the thread
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.session("cccccc", "")
	if other := b.named(second); other == name {
		t.Fatalf("a new session took %s before it knew its thread", name)
	}
	b.native("cccccc", "thread-1")
	b.nameIs(second, name)
}

// Another Codex thread is another name, and each thread keeps its own when they come back in turn.
func TestCodexThreadsHaveTheirOwnNames(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.session("bbbbbb", "")
	b.native("bbbbbb", "thread-1")
	name := b.named(first)
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, conn := b.session("cccccc", "")
	b.native("cccccc", "thread-2")
	other := b.named(second)
	if other == name {
		t.Fatalf("two Codex threads share the name %s", name)
	}
	conn.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })

	third, _ := b.session("dddddd", "")
	b.native("dddddd", "thread-1")
	b.nameIs(third, name)
}

// A Codex thread that another program goes on with, as the app does one begun in the command
// line, keeps its name there though the first program is still open (#568): the session whose
// tool call showed the thread last has the name and what was sent to it, and the first has a new
// name, which a daemon restart doesn't undo. A second server of the same process is, as before,
// a session of its own.
func TestACodexThreadKeepsItsNameInAnotherProgram(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	a.session("aaaaaa", "")
	cli, _ := b.agentSession("bbbbbb", "codex", 700, proto.Route{})
	b.native("bbbbbb", "thread-1")
	name := b.named(cli)
	eventually(t, "a to see the thread's name", func() bool { return a.nameOf(cli) == name })
	a.send("aaaaaa", name, "before the move", "")
	eventually(t, "the message to reach the command line's session", func() bool { return b.queuedFor(cli, "before the move") })

	app, _ := b.agentSession("cccccc", "codex", 800, proto.Route{})
	if first := b.named(app); first == name {
		t.Fatalf("the app's session took %s before it knew its thread", name)
	}
	b.native("cccccc", "thread-1")
	b.nameIs(app, name)
	var other string
	eventually(t, "the command line's session to have a name of its own", func() bool {
		other = b.nameOf(cli)
		return other != "" && other != name
	})
	if m := inboxMessage(t, b, "cccccc"); m.Subject != "before the move" {
		t.Errorf("the app's session got %q", m.Subject)
	}
	if r := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}); len(r.Messages) != 0 {
		t.Errorf("the command line's session kept %+v", r.Messages)
	}
	// Once a has seen the move: a message carries an address, and one sent in between goes to the
	// session the sender still took for the name's (session-names.md, "A name held by another
	// session").
	eventually(t, "a to see the name move", func() bool { return a.nameOf(app) == name && a.nameOf(cli) == other })
	a.send("aaaaaa", name, "after the move", "")
	if m := inboxMessage(t, b, "cccccc"); m.Subject != "after the move" {
		t.Errorf("sent to the name after the move, the app's session got %q", m.Subject)
	}

	// Another server of the app's process on the same thread is a session of its own.
	twin, _ := b.agentSession("dddddd", "codex", 800, codexIn("thread-1"))
	if got := b.named(twin); got == name {
		t.Errorf("a second server of one process took %s from the first", name)
	}
	b.nameIs(app, name)

	// Nor does another agent's session whose own ID happens to be the thread's take it.
	pi, _ := b.agentSession("ffffff", "pi", 950, proto.Route{Native: "thread-1"})
	if got := b.named(pi); got == name {
		t.Errorf("a Pi session took %s from a Codex thread", name)
	}
	b.nameIs(app, name)

	// After a restart of the daemon the first program's server, which still knows the thread,
	// registers first, and doesn't take the name back.
	b.restart()
	cli, cliConn := b.agentSession("bbbbbb", "codex", 700, codexIn("thread-1"))
	if got := b.named(cli); got != other {
		t.Errorf("after a restart the command line's session is %s, not %s", got, other)
	}
	app, appConn := b.agentSession("cccccc", "codex", 800, codexIn("thread-1"))
	b.nameIs(app, name)

	// With both stopped, a message for the address the registry has the name at, the first
	// session's, is the thread's: the next program to go on with the thread gets it. That one
	// knows its thread as it registers, and takes the name from no one.
	cliConn.Close()
	appConn.Close()
	eventually(t, "both to stop", func() bool { return busLines(b, cli, "stopped") == 1 && busLines(b, app, "stopped") == 1 })
	a.send("aaaaaa", cli, "while nobody had the thread", "")
	eventually(t, "the message to be kept", func() bool { return b.queuedFor(cli, "while nobody had the thread") })
	next, _ := b.agentSession("eeeeee", "codex", 900, codexIn("thread-1"))
	b.nameIs(next, name)
	if m := inboxMessage(t, b, "eeeeee"); m.Subject != "while nobody had the thread" {
		t.Errorf("the thread's next session got %q", m.Subject)
	}
}

// A Codex server that knows its thread as it registers takes the name from another program's
// live session too (#568), and one that comes back from its grace doesn't take it back.
func TestACodexThreadMovesAtRegistrationAndStaysMoved(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 5*time.Second, time.Hour)
	cli, _ := b.agentSession("bbbbbb", "codex", 700, codexIn("thread-1"))
	name := b.named(cli)
	app, appConn := b.agentSession("cccccc", "codex", 800, codexIn("thread-1"))
	b.nameIs(app, name)

	// The app's server drops, and in its grace a third program goes on with the thread.
	appConn.Close()
	third, _ := b.agentSession("dddddd", "codex", 900, codexIn("thread-1"))
	b.nameIs(third, name)
	back, _ := b.agentSession("cccccc", "codex", 800, codexIn("thread-1"))
	if got := b.named(back); got == name {
		t.Errorf("a session back from its grace took %s back", name)
	}
	b.nameIs(third, name)
}

// Inside T3 the T3 thread wins: two sessions of one thread share a name even when Codex gave them
// different thread IDs of its own, and a session's name does not follow a Codex thread ID that
// another T3 thread's session also has.
func TestT3ThreadWinsOverTheCodexThread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", Native: "codex-1"})
	name := b.named(first)
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.sessionRoute("cccccc", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", Native: "codex-2"})
	b.nameIs(second, name)

	third, _ := b.sessionRoute("dddddd", "", proto.Route{T3CLI: exe, T3Thread: "thread-carol", Native: "codex-1"})
	if other := b.named(third); other == name {
		t.Errorf("a Codex thread ID joined two T3 threads under the name %s", name)
	}
}

// A Codex session that learns its thread while its first name is being pushed keeps that name for
// the thread rather than taking a second one (#874).
func TestACodexThreadFoundWhileItsNameIsPushedGetsOneName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newPushGate(false)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	first, conn := b.agentSession("bbbbbb", "codex", 700, proto.Route{})
	select {
	case <-g.held:
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
	b.native("bbbbbb", "thread-push")
	g.set(true)
	name := b.named(first)

	// Only one name assignment should have been pushed for the session.
	assigned := 0
	for _, ev := range b.do(proto.Request{Op: proto.OpLog, Limit: 500}).Events {
		if strings.Contains(ev.Line, " -> "+first) {
			assigned++
		}
	}
	if assigned != 1 {
		t.Errorf("session %s got %d name assignments, want 1", first, assigned)
	}
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, _ := b.agentSession("cccccc", "codex", 800, proto.Route{})
	b.native("cccccc", "thread-push")
	b.nameIs(second, name)
}
