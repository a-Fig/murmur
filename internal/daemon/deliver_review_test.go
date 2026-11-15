package daemon_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// Tests from the review of handing messages over as turns (#57): what a restart, a hook, a thread
// that came back as a new session, and a client session's ID in T3 did to a handoff.

// A message that waited in an inbox over a restart is handed over when the daemon is back, though
// nothing new arrives to start a handoff: both tries failed because there was no `t3` to run. (A
// handoff that got as far as running `t3` is not repeated after a restart: deliver_repeat_test.go.)
func TestMessagesKeptOverARestartAreHandedOverWhenTheDaemonIsBack(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, install, calls := lateT3(t)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.logTo = 300*time.Millisecond, time.Hour, &out })
	alice, _ := a.session("aaaaaa", "")
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 && !a.listsSession(bob) })

	a.send("aaaaaa", bob, "while T3 was away", "wake up")
	eventually(t, "both tries to fail", func() bool { return strings.Contains(out.String(), "as a turn through T3 failed") })

	install() // T3 is back, and so is the daemon
	b.restart()
	eventually(t, "the handoff after the restart", func() bool { return len(calls()) == 1 })
}

// The hook doesn't tell a session of the messages queued behind the one being handed over: they are
// turns too, and told by a notice they would never be handed over.
func TestHookLeavesMessagesWaitingForAHandoffAlone(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Sleep, "1500ms")
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "one")
	a.send("aaaaaa", bob, "second", "two")
	eventually(t, "the first handoff to start", func() bool { return len(calls()) == 1 })
	eventually(t, "the second message to reach b", func() bool { return logHas(b, "second") })
	if got := b.hook("claude-bob", 0); len(got) != 0 {
		t.Errorf("the hook told of %q, which are on their way as turns", got)
	}
	eventually(t, "both messages to be handed over", func() bool { return len(calls()) == 2 })
	holds(t, "nothing unread", time.Second, func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0 && len(calls()) == 2
	})
}

// A thread whose MCP server was replaced by a new session is one recipient of a message for
// everyone, not two: the session it replaced, in its grace or stopped, still has the thread.
func TestMessageForEveryoneReachesAThreadOnce(t *testing.T) {
	t.Parallel()
	route := func(exe string) proto.Route { return proto.Route{T3CLI: exe, T3Thread: "thread-bob"} }
	t.Run("the old session stopped", func(t *testing.T) {
		remote := newRemote(t)
		exe, calls := useFakeT3(t)
		a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
		alice, _ := a.session("aaaaaa", "")
		old, conn := b.sessionRoute("bbbbbb", "claude-1", route(exe))
		eventually(t, "presence", func() bool { return a.seesSession(old) && b.seesSession(alice) })
		conn.Close()
		eventually(t, "the old session to stop", func() bool { return busLines(b, old, "stopped") == 1 && !a.listsSession(old) })
		bob, _ := b.sessionRoute("cccccc", "claude-2", route(exe))
		eventually(t, "presence", func() bool { return a.seesSession(bob) })

		a.send("aaaaaa", "*", "to everyone", "hello")
		eventually(t, "the handoff", func() bool { return len(calls()) >= 1 })
		holds(t, "one handoff", 1500*time.Millisecond, func() bool { return len(calls()) == 1 })
	})
	t.Run("the old session in its grace", func(t *testing.T) {
		remote := newRemote(t)
		exe, calls := useFakeT3(t)
		a, b := newDevice(t, remote), newTimedDevice(t, remote, 20*time.Second, time.Hour)
		alice, _ := a.session("aaaaaa", "")
		old, conn := b.sessionRoute("bbbbbb", "claude-1", route(exe))
		eventually(t, "presence", func() bool { return a.seesSession(old) && b.seesSession(alice) })
		conn.Close()                       // dropped; its grace runs
		time.Sleep(500 * time.Millisecond) // the daemon has seen it go, before the new one comes (#99)
		bob, _ := b.sessionRoute("cccccc", "claude-2", route(exe))
		eventually(t, "presence", func() bool { return a.seesSession(bob) })

		a.send("aaaaaa", "*", "to everyone", "hello")
		eventually(t, "the handoff", func() bool { return len(calls()) >= 1 })
		holds(t, "one handoff", 1500*time.Millisecond, func() bool { return len(calls()) == 1 })
	})
}

// A message for everyone that comes after a thread's new session registered, and before it found its
// thread, is handed to the thread once (#70): the old session, in its grace, still has the thread
// and hands its copy over at once, and the new one's copy is not handed over again when the route
// moves to it. A message for the new session alone is another event, and still goes in.
func TestMessageForEveryoneBeforeTheThreadIsFoundReachesItOnce(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	b := newTimedDevice(t, remote, 20*time.Second, time.Hour)
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	_, conn := b.sessionRoute("bbbbbb", "claude-1", route)
	conn.Close()
	time.Sleep(500 * time.Millisecond) // the daemon has seen it go; its grace is long
	// In T3, its thread not found yet.
	bob, _ := b.sessionRoute("cccccc", "claude-2", proto.Route{T3CLI: exe})
	b.session("dddddd", "")

	b.send("dddddd", "*", "to everyone", "hello")
	// Handed over, not only on its way: one still on its way when the route moves is taken over
	// as it is (see takeInboxLocked), which is another case.
	eventually(t, "the old session's copy to be handed over", func() bool { return logHas(b, `handed "to everyone"`) })
	b.send("dddddd", bob, "to the new one", "hi")
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "cccccc", Route: &route}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}

	eventually(t, "the new session's own message to be handed over", func() bool { return len(calls()) >= 2 })
	holds(t, "two handoffs", 1500*time.Millisecond, func() bool { return len(calls()) == 2 })
	for i, want := range []string{"to everyone", "to the new one"} {
		if got := calls()[i].Stdin; !strings.Contains(got, want) {
			t.Errorf("call %d carried %q, want the message %q", i, got, want)
		}
	}
	if inbox, notices := b.unread("cccccc", "claude-2"); len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("left unread: inbox %q, notices %q", inbox, notices)
	}
}

// A session that found its T3 thread keeps its name under the thread, and the Claude Code session
// ID it had before no longer brings that name to a second process of the same client session: a
// name is an address, and two live sessions can't share one.
func TestClientSessionIDDoesNotBringBackTheNameOfASessionInT3(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	b := newDevice(t, remote)
	first, _ := b.sessionRoute("bbbbbb", "claude-1", proto.Route{T3CLI: exe})
	name := b.named(first)
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "bbbbbb", Route: &proto.Route{T3CLI: exe, T3Thread: "thread-bob"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	b.nameIs(first, name)

	second, _ := b.sessionRoute("cccccc", "claude-1", proto.Route{})
	if other := b.named(second); other == name {
		t.Errorf("two live sessions are both named %s", name)
	}
	b.nameIs(first, name)
}

// What the session that a thread's new session replaces still had unread goes to the new one, and
// is handed over: with the old route retired, nothing else would. One that is on its way in as the
// replacement registers is handed over once.
func TestReplacementOfAThreadTakesWhatTheOldSessionHadUnread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Sleep, "4s")
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 20*time.Second, time.Hour)
	alice, _ := a.session("aaaaaa", "")
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	old, conn := b.sessionRoute("bbbbbb", "claude-1", route)
	eventually(t, "presence", func() bool { return a.seesSession(old) && b.seesSession(alice) })

	a.send("aaaaaa", old, "first", "one")
	eventually(t, "the first handoff to start", func() bool { return len(calls()) == 1 })
	a.send("aaaaaa", old, "second", "two")
	eventually(t, "the second message to reach b", func() bool { return logHas(b, "second") })
	conn.Close()                       // dropped, in its grace, with the second unread and the first on its way
	time.Sleep(500 * time.Millisecond) // the daemon has seen it go, and the grace is long
	b.sessionRoute("cccccc", "claude-2", route)

	eventually(t, "the second message to be handed over", func() bool { return len(calls()) >= 2 })
	holds(t, "each handed over once", 2500*time.Millisecond, func() bool { return len(calls()) == 2 })
	if inbox, _ := b.unread("cccccc", "claude-2"); len(inbox) != 0 {
		t.Errorf("inbox: %q", inbox)
	}
}

// A message that was on its way in when the new session of a thread took over keeps its place:
// what came after it waits until it has been handed over, tried again or not, so the turns arrive in
// the order the messages did. The first message waits for its retry because the first attempt could
// not start `t3`, which is not there yet.
func TestReplacementHandsOverInOrderWhatWasOnItsWay(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, install, calls := lateT3(t)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) {
		d.grace, d.keep, d.pushRetry, d.logTo = 20*time.Second, time.Hour, 3*time.Second, &out
	})
	alice, _ := a.session("aaaaaa", "")
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	old, conn := b.sessionRoute("bbbbbb", "claude-1", route)
	eventually(t, "presence", func() bool { return a.seesSession(old) && b.seesSession(alice) })

	a.send("aaaaaa", old, "first", "one")
	eventually(t, "the first handoff to fail before it started", func() bool {
		return strings.Contains(out.String(), "nothing was sent")
	})
	install() // the retry, 3 s on, finds it
	a.send("aaaaaa", old, "second", "two")
	eventually(t, "the second message to reach b", func() bool { return logHas(b, "second") })
	conn.Close()                       // dropped, in its grace, with the first on its way, waiting for its retry
	time.Sleep(500 * time.Millisecond) // the daemon has seen it go
	b.sessionRoute("cccccc", "claude-2", route)

	eventually(t, "both messages to have been handed over", func() bool { return len(calls()) >= 2 })
	holds(t, "each handed over once", 2*time.Second, func() bool { return len(calls()) == 2 })
	for i, want := range []string{"first", "second"} {
		if got := calls()[i].Stdin; !strings.Contains(got, want) {
			t.Errorf("call %d carried %q, want the message %q", i, got, want)
		}
	}
}

// quietReplacement is a device on which a T3 thread's first session has dropped, in its grace, and
// a second session of the thread has registered without knowing the thread yet, with a third session
// to send from, and the page saying never to wake a session. found tells the daemon the second
// session's thread.
func quietReplacement(t *testing.T) (b *device, calls func() []t3Call, found func()) {
	t.Helper()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	b = newDeviceWith(t, remote, func(d *device) { d.grace, d.keep = 20*time.Second, time.Hour })
	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"never"}}`); code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", code, body)
	}
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	_, conn := b.sessionRoute("bbbbbb", "claude-1", route)
	conn.Close()
	time.Sleep(500 * time.Millisecond) // the daemon has seen it go
	b.sessionRoute("cccccc", "claude-2", proto.Route{T3CLI: exe})
	b.session("dddddd", "")
	return b, calls, func() {
		t.Helper()
		if r := b.do(proto.Request{Op: proto.OpRoute, Session: "cccccc", Route: &route}); r.Error != "" {
			t.Fatalf("route: %s", r.Error)
		}
	}
}

// A message the new session of a thread sent for everyone, before it found its thread, is in the
// inbox of the session it replaces, which isn't a sender: it must not come back to the new session.
func TestReplacementIsNotHandedItsOwnBroadcast(t *testing.T) {
	t.Parallel()
	b, calls, found := quietReplacement(t)
	b.send("cccccc", "*", "from the new one", "hello")
	found()

	inbox, notices := b.unread("cccccc", "claude-2")
	if len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("the new session got its own message back: inbox %q, notices %q", inbox, notices)
	}
	if n := len(calls()); n != 0 {
		t.Errorf("%d turns handed over while the page says never", n)
	}
}

// A message for everyone that the new session of a thread has read, before it found its thread, is
// not told again from the inbox of the session it replaces.
func TestReplacementIsNotHandedWhatItHasRead(t *testing.T) {
	t.Parallel()
	b, _, found := quietReplacement(t)
	b.send("dddddd", "*", "to everyone", "hi")
	if r := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}); len(r.Messages) != 1 {
		t.Fatalf("the new session read %d messages, want 1", len(r.Messages))
	}
	found()

	inbox, notices := b.unread("cccccc", "claude-2")
	if len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("a message it had read came back: inbox %q, notices %q", inbox, notices)
	}
}
