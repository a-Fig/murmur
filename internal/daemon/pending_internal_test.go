package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// A send whose push ended unsure is pushed again until it lands, never on the log twice, and its
// sender told only if it is given up (#781, Tyler, 2026-11-16).
func TestAnUnsureSendIsPushedAgainUntilItLands(t *testing.T) {
	t.Parallel()
	type settled struct {
		landed, absent bool
	}
	cut := errors.New("the connection dropped")
	for _, c := range []struct {
		name string
		// push stands in for the push of the log, the nth (from 0): it may call the real one.
		push func(n int, real func() error) error
		want settled
	}{
		{"its push landed and said it didn't", func(n int, real func() error) error {
			if err := real(); n > 0 || err != nil {
				return err
			}
			return cut
		}, settled{landed: true}},
		{"its push failed twice, then landed", func(n int, real func() error) error {
			if n < 3 {
				return cut
			}
			return real()
		}, settled{landed: true}},
		{"no push ever lands", func(int, func() error) error { return cut }, settled{absent: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, _, _ := rateRig(t, Options{PendingRetry: 20 * time.Millisecond, PendingGiveUp: 2 * time.Second}, false)
			ctx := context.Background()
			ref := d.meshRef()
			before := d.sendFirst(t) // a tip to build on
			var n atomic.Int32
			d.writeMu.Lock()
			d.testPush = func(real func() error) error { return c.push(int(n.Add(1)-1), real) }
			d.writeMu.Unlock()
			got := make(chan settled, 1)
			err := d.appendEventIf(ctx, message(d, "the failing page"), nil, func(landed, absent bool, _ error) {
				got <- settled{landed, absent}
			})
			if !errors.As(err, new(pendingError)) {
				t.Fatalf("the send ended with %v, want it pending", err)
			}
			var s settled
			select {
			case s = <-got:
			case <-time.After(30 * time.Second):
				t.Fatal("the pending send was never settled")
			}
			if s != c.want {
				t.Errorf("settled %+v, want %+v", s, c.want)
			}
			tip, err := d.repo.RemoteRef(ctx, ref)
			if err != nil {
				t.Fatal(err)
			}
			count := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-list", "--count", before+".."+tip))
			if want := map[bool]string{true: "1", false: "0"}[c.want.landed]; count != want {
				t.Errorf("the remote's log has %s commits past the first send, want %s", count, want)
			}
			// The next write builds on what the remote has, and lands.
			d.writeMu.Lock()
			d.testPush = nil
			d.writeMu.Unlock()
			if err := d.appendEvent(ctx, message(d, "the next")); err != nil {
				t.Errorf("the next send: %v", err)
			}
		})
	}
}

// A later write that lands carries a send pending under it along.
func TestALaterSendCarriesAPendingOne(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
	ctx := context.Background()
	before := d.sendFirst(t)
	d.writeMu.Lock()
	d.testPush = func(func() error) error { return errors.New("the connection dropped") }
	d.writeMu.Unlock()
	var landed atomic.Bool
	if err := d.appendEventIf(ctx, message(d, "first"), nil, func(l, _ bool, _ error) { landed.Store(l) }); !errors.As(err, new(pendingError)) {
		t.Fatalf("the send ended with %v, want it pending", err)
	}
	d.writeMu.Lock()
	d.testPush = nil
	d.writeMu.Unlock()
	if err := d.appendEvent(ctx, message(d, "second")); err != nil {
		t.Fatal(err)
	}
	if !landed.Load() {
		t.Error("the pending send wasn't settled as landed")
	}
	tip, _ := d.repo.RemoteRef(ctx, d.meshRef())
	if count := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-list", "--count", before+".."+tip)); count != "2" {
		t.Errorf("the remote's log has %s commits past the first send, want 2", count)
	}
}

// A sender whose message is given up is told in its inbox, from Murmur, whether it may land yet.
func TestASenderIsToldOfAGivenUpSend(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	from := bus.Addr(d.device, "bbbbbb")
	d.mu.Lock()
	d.givenUpLocked(from, "the failing page", "quiet-otter", true, errors.New("the connection dropped"))
	d.givenUpLocked(from, "the log", "quiet-otter", false, errors.New("the connection dropped"))
	inbox := d.st.Inbox["bbbbbb"]
	d.mu.Unlock()
	if len(inbox) != 2 {
		t.Fatalf("the sender's inbox: %+v", inbox)
	}
	if e := inbox[0].Event; e.Subject != `Not sent: "the failing page"` || !strings.Contains(e.Body, "Send it again") || !d.isTellNotice(e) {
		t.Errorf("the notice of a send the remote doesn't have: %+v", e)
	}
	if e := inbox[1].Event; !strings.Contains(e.Body, "may have landed") || !strings.Contains(e.Body, "lookup quiet-otter") {
		t.Errorf("the notice of a send that may land yet: %+v", e)
	}
}

// sendFirst writes a message to d's log and returns the tip of the remote's after it.
func (d *Daemon) sendFirst(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	if err := d.appendEvent(ctx, message(d, "first")); err != nil {
		t.Fatal(err)
	}
	tip, err := d.repo.RemoteRef(ctx, d.meshRef())
	if err != nil || tip == "" {
		t.Fatalf("the remote's tip after the first send: %q, %v", tip, err)
	}
	return tip
}

func message(d *Daemon, subject string) bus.Event {
	return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: d.device + "/bbbbbb",
		At: time.Now().UTC(), To: "*", Subject: subject}
}

// A send whose push ends unsure as the daemon stops, with no loop left to push it again or give it
// up, fails unsure, as before #781, rather than being left pending for nobody (Sol's review of #818).
func TestAnUnsureSendAsTheDaemonStopsIsntLeftPending(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	before := d.sendFirst(t)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	d.writeMu.Lock()
	d.bgCtx = stopped
	d.testPush = func(func() error) error { return errors.New("the connection dropped") }
	d.writeMu.Unlock()
	err := d.appendEventIf(context.Background(), message(d, "late"), nil, func(bool, bool, error) {
		t.Error("a send never left pending was settled")
	})
	if errors.As(err, new(pendingError)) || !errors.As(err, new(unsureError)) {
		t.Errorf("the send ended with %v, want it unsure and not pending", err)
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if d.ownTip != before || len(d.pendSends) != 0 {
		t.Errorf("the next write builds on %s with %d pending, want %s with none", d.ownTip, len(d.pendSends), before)
	}
}
