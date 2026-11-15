package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// A T3 thread idle for half an hour loses its MCP server, and with it its session; its route is
// kept so that a message can still wake it. That is of no use if the daemon that would hand over the
// message has gone, which it did ten minutes after the last session (#87, seen live on 2026-10-30).

// A daemon whose only session was a T3 thread that stopped stays up past the idle time, and
// hands over a message that comes then, through `t3`.
func TestDaemonWithAThreadToWakeStaysUpAndHandsOver(t *testing.T) {
	// Not parallel: it times the daemon's own exit against its idle and keep windows, which other
	// tests' load can push past (seen once in a full -race run, 2026-11-12).
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.idle, d.grace, d.keep = time.Second, 300*time.Millisecond, time.Hour })
	alice, _ := a.session("aaaaaa", "")
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	conn.Close()
	eventually(t, "bob to stop", func() bool { return busLines(b, bob, "stopped") == 1 && !a.listsSession(bob) })

	holds(t, "the daemon up long after the idle time", 4*time.Second, func() bool { return !b.exited() })
	a.send("aaaaaa", bob, "wake up", "someone wrote")
	eventually(t, "the handoff to the sleeping thread", func() bool { return len(calls()) == 1 })
	if got := strings.Join(calls()[0].Args, " "); got != "thread send thread-bob -" {
		t.Errorf("t3 was run as %q, want thread send thread-bob -", got)
	}
	holds(t, "the daemon still up", time.Second, func() bool { return !b.exited() })
}

// With no thread to wake, a daemon with no sessions exits after the idle time as before: the
// session had no T3 route, which is the case for Claude Code outside T3, whose messaging socket
// dies with the session.
func TestDaemonWithNothingToWakeStillExits(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDeviceWith(t, remote, func(d *device) { d.idle, d.grace, d.keep = time.Second, 300*time.Millisecond, time.Hour })
	_, conn := b.session("bbbbbb", "")
	holds(t, "the daemon up with a session", 2*time.Second, func() bool { return !b.exited() })
	conn.Close()
	eventually(t, "the daemon to exit once the session was gone and the idle time passed", b.exited)
}

// A thread's route is kept for as long as its inbox is (StoppedInbox); when that is over, so is the
// daemon's reason to stay.
func TestDaemonLeavesOnceTheThreadIsNoLongerKept(t *testing.T) {
	// Not parallel: it times the daemon's own exit against its idle and keep windows, which other
	// tests' load can push past (seen once in a full -race run, 2026-11-12).
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	b := newDeviceWith(t, remote, func(d *device) { d.idle, d.grace, d.keep = time.Second, 300*time.Millisecond, 8*time.Second })
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	conn.Close()
	eventually(t, "bob to stop", func() bool { return busLines(b, bob, "stopped") == 1 })

	// Past the idle time, the route still kept. (The stop is timed when the daemon sees it, and
	// reaches the bus later; the room left must outlast a slow publication.)
	holds(t, "the daemon up while the thread is kept", 2*time.Second, func() bool { return !b.exited() })
	eventually(t, "the daemon to exit once the thread was not kept", b.exited)
}

// A daemon with no session looks at the remote less often, and a session that comes makes it look
// at once and at the usual rate. QuietPoll here is an hour, so a poll that happens is the session's.
func TestPollSlowsWithoutASessionAndQuickensWhenOneComes(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	var out syncBuf
	b := newDeviceWith(t, remote, func(d *device) { d.quietPoll, d.grace, d.logTo = time.Hour, 300*time.Millisecond, &out })
	eventually(t, "b to slow its poll", func() bool { return strings.Count(out.String(), "no session is live") == 1 })

	carol, _ := a.session("cccccc", "") // appears after b's last look
	holds(t, "b not looking at the remote", 2500*time.Millisecond, func() bool { return !b.listsSession(carol) })

	_, conn := b.session("bbbbbb", "")
	eventually(t, "b to see carol once it has a session", func() bool { return b.seesSession(carol) })
	// Not only that look: the ones after it, at the usual rate (the session's name has its own look).
	dave, _ := a.session("dddddd", "")
	eventually(t, "b to go on looking, and see dave", func() bool { return b.seesSession(dave) })
	if !strings.Contains(out.String(), "a session is live") {
		t.Errorf("the faster rate was not logged:\n%s", out.String())
	}

	conn.Close()
	eventually(t, "b to slow its poll again", func() bool { return strings.Count(out.String(), "no session is live") == 2 })
}
