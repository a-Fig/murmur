package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
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

// A sender whose message is given up is told in its inbox, from Murmur, that it wasn't sent, and
// why in plain words, not git's (Tyler, 2026-11-16).
func TestASenderIsToldOfAGivenUpSend(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	from := bus.Addr(d.device, "bbbbbb")
	d.mu.Lock()
	d.givenUpLocked(from, "the failing page", "quiet-otter", fmt.Errorf("%w: signal: killed", context.DeadlineExceeded))
	d.givenUpLocked(from, "the log", "quiet-otter", errors.New("the connection dropped"))
	inbox := d.st.Inbox["bbbbbb"]
	d.mu.Unlock()
	if len(inbox) != 2 {
		t.Fatalf("the sender's inbox: %+v", inbox)
	}
	if e := inbox[0].Event; e.Subject != `Not sent: "the failing page"` || !d.isTellNotice(e) ||
		e.Body != "Your message to quiet-otter couldn't be pushed, and was given up: the push kept timing out. Send it again if it still matters." {
		t.Errorf("the notice of a send whose pushes timed out: %+v", e)
	}
	if e := inbox[1].Event; !strings.Contains(e.Body, "given up: the push kept failing.") {
		t.Errorf("the notice of a send whose pushes failed: %+v", e)
	}
}

// A pending send given up because the remote refused the git credentials tells the sender so, and
// that the user has to fix their git login rather than telling it to try again (#858).
func TestAGivenUpSendSaysWhenTheRemoteRefusedCredentials(t *testing.T) {
	t.Parallel()
	gitErr := func(stderr string) error {
		return &gitx.Error{
			Args:   []string{"push", "origin", "sha:refs/murmur/mesh/dev"},
			Stderr: stderr,
			Err:    errors.New("exit status 128"),
		}
	}
	want := "Your message to quiet-otter couldn't be pushed, and was given up: the remote refused git credentials. Ask your user to fix their git login before sending again."
	for _, c := range []struct {
		name string
		err  error
	}{
		{"https auth failed", gitErr("remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/a/b.git/'\n")},
		{"ssh permission denied", gitErr("git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n")},
		{"terminal prompts disabled", gitErr("fatal: could not read Username for 'https://github.com': terminal prompts disabled\n")},
		{"wrapped git error", fmt.Errorf("push failed: %w", gitErr("remote: Permission to a/b.git denied to alice.\nfatal: unable to access 'https://github.com/a/b.git/': The requested URL returned error: 403\n"))},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, _, _ := rateRig(t, Options{}, false)
			from := bus.Addr(d.device, "bbbbbb")
			d.mu.Lock()
			d.givenUpLocked(from, "auth check", "quiet-otter", c.err)
			inbox := d.st.Inbox["bbbbbb"]
			d.mu.Unlock()
			if len(inbox) != 1 {
				t.Fatalf("the sender's inbox: %+v", inbox)
			}
			if e := inbox[0].Event; e.Subject != `Not sent: "auth check"` || !d.isTellNotice(e) || e.Body != want {
				t.Errorf("the give-up notice: %+v, want body %q", e, want)
			}
		})
	}

	t.Run("pending send given up after auth failure", func(t *testing.T) {
		t.Parallel()
		d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
		ctx := context.Background()
		d.sendFirst(t)
		authErr := gitErr("remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/a/b.git/'\n")
		d.writeMu.Lock()
		d.testPush = func(func() error) error { return authErr }
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "after expiry"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		d.writeMu.Lock()
		d.pendFrom = time.Now().Add(-time.Hour)
		d.retryPending()
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		wantAll := "Your message to * couldn't be pushed, and was given up: the remote refused git credentials. Ask your user to fix their git login before sending again."
		if len(inbox) != 1 {
			t.Fatalf("the sender's inbox has %d messages, want 1", len(inbox))
		}
		if got := inbox[0].Event.Body; got != wantAll {
			t.Errorf("the give-up notice body:\n got: %q\nwant: %q", got, wantAll)
		}
	})
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

// The person's send from the page isn't left pending: the page would show it sent, and has nowhere
// to show its give-up, so a push that ends unsure is refused at once, as before #781 (Fable's
// review of rc.2).
func TestThePersonsUnsureSendIsRefusedAtOnce(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: 20 * time.Millisecond, PendingGiveUp: 2 * time.Second}, false)
	d.sendFirst(t) // a tip to build on
	d.writeMu.Lock()
	d.testPush = func(func() error) error { return errors.New("the connection dropped") }
	d.writeMu.Unlock()
	r := d.sendWith(context.Background(), d.personAddr(), proto.Request{Op: proto.OpSend, To: "*", Subject: "from the page"}, nil)
	if !strings.Contains(r.Error, "not sent") {
		t.Fatalf("the person's send whose push failed answered %+v, want it refused as not sent", r)
	}
	d.writeMu.Lock()
	pending := len(d.pendSends)
	d.writeMu.Unlock()
	if pending != 0 {
		t.Errorf("%d sends left pending, want none", pending)
	}
}
