package daemon_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
)

// Handoff is the one thing the daemon does to the user's other programs, so it stops with the
// user's consent (#55): a clone that hasn't joined, or has left, runs no `t3` and writes to no
// socket. The daemon exits within ConsentPoll of the consent going; these tests make that poll
// long, to show that handoff doesn't rely on it.

// A message that arrives by sync once the user has left waits in the inbox. It isn't handed over, and
// isn't counted as a failure either: when the user joins again, it goes in front of what came after.
func TestNoTurnIsHandedOverForAUserWhoHasLeft(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var joined atomic.Bool
	joined.Store(true)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.consent, d.consentPoll, d.logTo = joined.Load, time.Hour, &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	joined.Store(false)
	a.send("aaaaaa", bob, "while gone", "")
	holds(t, "no call of t3 for a user who has left", 2*time.Second, func() bool { return len(calls()) == 0 })
	if strings.Contains(out.String(), "as a turn through T3 failed") {
		t.Errorf("a message held back for want of consent was logged as a failure:\n%s", out.String())
	}

	joined.Store(true)
	a.send("aaaaaa", bob, "after joining again", "")
	eventually(t, "both messages", func() bool { return len(calls()) >= 2 })
	for i, want := range []string{"while gone", "after joining again"} {
		if got := calls()[i].Stdin; !strings.Contains(got, "via "+brand.Product+":\n\n"+want+"\n") {
			t.Errorf("handoff %d was %q, want %q", i, got, want)
		}
	}
}

// A handoff that failed before anything started, and waits to try again, doesn't try again for a
// user who left in between. Without the guard the second attempt would fail to start too, and be
// logged as a failure; with it, the attempt isn't made.
func TestRetryOfAHandoffStopsWhenTheUserLeaves(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _, calls := lateT3(t) // never installed: every attempt fails to start
	var joined atomic.Bool
	joined.Store(true)
	var out syncBuf
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) {
		d.consent, d.consentPoll, d.logTo = joined.Load, time.Hour, &out
		d.pushRetry = 1500 * time.Millisecond
	})
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "")
	eventually(t, "the first attempt to fail", func() bool { return strings.Contains(out.String(), "nothing was sent") })
	joined.Store(false)
	holds(t, "no second attempt", 3*time.Second, func() bool { return !strings.Contains(out.String(), "as a turn through T3 failed") })
	if n := len(calls()); n != 0 {
		t.Errorf("the stand-in ran %d times", n)
	}
}
