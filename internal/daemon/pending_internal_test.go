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

// A pending send that an earlier push landed is settled as landed when a later push runs out of
// time and the batch is given up: the remote is asked in time of its own, not in what the push
// left, which is none (#951). Its sender, told it wasn't sent, would send it twice.
func TestAGiveUpAfterAPushTimedOutStillAsksTheRemote(t *testing.T) {
	t.Parallel()
	const pushTime = 300 * time.Millisecond
	d, _, _ := rateRig(t, Options{PendingRetry: 20 * time.Millisecond, PendingPush: pushTime, PendingGiveUp: 500 * time.Millisecond}, false)
	ctx := context.Background()
	before := d.sendFirst(t) // a tip to build on
	type settled struct {
		landed, absent bool
	}
	cut := errors.New("the connection dropped")
	var n atomic.Int32
	d.writeMu.Lock()
	d.testPush = func(real func() error) error {
		switch n.Add(1) {
		case 1: // the first send's push lands, and says it didn't
			if err := real(); err != nil {
				return err
			}
			return cut
		case 2: // the second send's push is lost
			return cut
		}
		// Each push again hangs past its time, as git does until it is stopped.
		time.Sleep(pushTime + 100*time.Millisecond)
		return fmt.Errorf("%w: signal: killed", context.DeadlineExceeded)
	}
	d.writeMu.Unlock()
	first, second := make(chan settled, 1), make(chan settled, 1)
	for _, c := range []struct {
		subject string
		got     chan settled
	}{{"landed", first}, {"lost", second}} {
		err := d.appendEventIf(ctx, message(d, c.subject), nil, func(landed, absent bool, _ error) {
			c.got <- settled{landed, absent}
		})
		if !errors.As(err, new(pendingError)) {
			t.Fatalf("the send of %q ended with %v, want it pending", c.subject, err)
		}
	}
	for _, c := range []struct {
		name string
		got  chan settled
		want settled
	}{{"the send the remote has", first, settled{landed: true}}, {"the send it doesn't have", second, settled{absent: true}}} {
		select {
		case s := <-c.got:
			if s != c.want {
				t.Errorf("%s settled %+v, want %+v", c.name, s, c.want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s was never settled", c.name)
		}
	}
	// The next write builds on the send that landed, and lands.
	d.writeMu.Lock()
	d.testPush = nil
	d.writeMu.Unlock()
	if err := d.appendEvent(ctx, message(d, "the next")); err != nil {
		t.Fatalf("the next send: %v", err)
	}
	tip, err := d.repo.RemoteRef(ctx, d.meshRef())
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-list", "--count", before+".."+tip)); count != "2" {
		t.Errorf("the remote's log has %s commits past the first send, want 2", count)
	}
}

// A daemon that stops asks the remote before it gives its pending sends up: one that an earlier
// push landed is settled as landed, and one the remote doesn't have as absent, where both were
// called not sent, unasked (#857).
func TestAStoppingDaemonAsksTheRemoteBeforeItGivesUp(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
	ctx := context.Background()
	before := d.sendFirst(t) // a tip to build on
	running, stop := context.WithCancel(ctx)
	defer stop()
	type settled struct {
		landed, absent bool
	}
	cut := errors.New("the connection dropped")
	var n atomic.Int32
	d.writeMu.Lock()
	d.bgCtx = running
	d.testPush = func(real func() error) error {
		if n.Add(1) == 1 { // the first send's push lands, and says it didn't
			if err := real(); err != nil {
				return err
			}
		}
		return cut // the second send's is lost
	}
	d.writeMu.Unlock()
	first, second := make(chan settled, 1), make(chan settled, 1)
	for _, c := range []struct {
		subject string
		got     chan settled
	}{{"landed", first}, {"lost", second}} {
		err := d.appendEventIf(ctx, message(d, c.subject), nil, func(landed, absent bool, _ error) {
			c.got <- settled{landed, absent}
		})
		if !errors.As(err, new(pendingError)) {
			t.Fatalf("the send of %q ended with %v, want it pending", c.subject, err)
		}
	}
	stop()
	for _, c := range []struct {
		name string
		got  chan settled
		want settled
	}{{"the send the remote has", first, settled{landed: true}}, {"the send it doesn't have", second, settled{absent: true}}} {
		select {
		case s := <-c.got:
			if s != c.want {
				t.Errorf("%s settled %+v, want %+v", c.name, s, c.want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s was never settled", c.name)
		}
	}
	d.bg.Wait()
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if tip, err := d.repo.RemoteRef(ctx, d.meshRef()); err != nil || d.ownTip != tip || tip == before {
		t.Errorf("the next write would build on %s; the remote's tip is %s (%v), and was %s before the sends", d.ownTip, tip, err, before)
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

// A pending send given up because git couldn't log in to the remote or access the repository tells
// the sender so, and asks the user to check their login and access rather than retrying (#858).
// Auth phrases that appear only in git's command arguments with empty stderr don't trigger it.
func TestAGivenUpSendSaysWhenTheRemoteRefusedCredentials(t *testing.T) {
	t.Parallel()
	gitErr := func(stderr string) error {
		return &gitx.Error{
			Args:   []string{"push", "origin", "sha:refs/murmur/mesh/dev"},
			Stderr: stderr,
			Err:    errors.New("exit status 128"),
		}
	}
	wantAuth := "Your message to quiet-otter couldn't be pushed, and was given up: git couldn't log in to the remote or access the repository. Ask your user to check their git login and access before sending again."
	wantPlain := "Your message to quiet-otter couldn't be pushed, and was given up: the push kept failing. Send it again if it still matters."
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"https auth failed", gitErr("remote: Invalid username or token.\nfatal: Authentication failed for 'https://github.com/a/b.git/'\n"), wantAuth},
		{"ssh permission denied", gitErr("git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.\n"), wantAuth},
		{"terminal prompts disabled", gitErr("fatal: could not read Username for 'https://github.com': terminal prompts disabled\n"), wantAuth},
		{"wrapped git error", fmt.Errorf("push failed: %w", gitErr("remote: Permission to a/b.git denied to alice.\nfatal: unable to access 'https://github.com/a/b.git/': The requested URL returned error: 403\n")), wantAuth},
		{"repository not found", gitErr("remote: Repository not found.\nfatal: repository 'https://github.com/a/b.git/' not found\n"), wantAuth},
		{"host key verification failed", gitErr("Host key verification failed.\nfatal: Could not read from remote repository.\n"), wantAuth},
		{"auth words only in args with empty stderr", &gitx.Error{
			Args:   []string{"push", "origin", "sha:refs/murmur/Authentication failed"},
			Stderr: "  \n",
			Err:    errors.New("exit status 128"),
		}, wantPlain},
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
			if e := inbox[0].Event; e.Subject != `Not sent: "auth check"` || !d.isTellNotice(e) || e.Body != c.want {
				t.Errorf("the give-up notice: %+v, want body %q", e, c.want)
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
		wantAll := "Your message to * couldn't be pushed, and was given up: git couldn't log in to the remote or access the repository. Ask your user to check their git login and access before sending again."
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

// A send calling dropSwitchedOff stops waiting when its own context ends, rather than holding
// writeMu for pendingLook, and leaves unchecked pending sends for retryPending (#1073).
func TestDropSwitchedOffRespectsCallersContext(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: time.Hour, PendingGiveUp: time.Hour}, false)
	before := d.sendFirst(t)
	d.writeMu.Lock()
	d.pendBase = before
	settled := false
	d.pendSends = []pendingSend{{
		sha:    before,
		still:  func() bool { return false },
		settle: func(bool, bool, error) { settled = true },
	}}
	d.writeMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := d.appendEventIf(ctx, message(d, "after cancel"), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("appendEventIf with canceled ctx: %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("appendEventIf waited %v past its canceled ctx", time.Since(start))
	}
	d.writeMu.Lock()
	left := len(d.pendSends)
	d.writeMu.Unlock()
	if settled || left != 1 {
		t.Errorf("settled=%v left=%d, want unchecked pending send left for retryPending", settled, left)
	}
}

// When retryPending's push is rejected just as its push timeout expires, settling pending sends
// against the remote tip still uses its own pendingLook timeout so landed sends aren't falsely
// reported as refused (#1073). PendingGiveUp is an hour so settlement can only happen on the
// rejected retry itself, not through giveUpPending.
func TestRejectedRetrySettlesLandedSendWithOwnTimeout(t *testing.T) {
	t.Parallel()
	const pushTime = 200 * time.Millisecond
	d, _, _ := rateRig(t, Options{PendingRetry: 20 * time.Millisecond, PendingPush: pushTime, PendingGiveUp: time.Hour}, false)
	ctx := context.Background()
	d.sendFirst(t)
	type settled struct {
		landed, absent bool
	}
	cut := errors.New("the connection dropped")
	ref := d.meshRef()
	var n atomic.Int32
	var advanceErr atomic.Value
	d.writeMu.Lock()
	d.testPush = func(real func() error) error {
		switch n.Add(1) {
		case 1:
			// First send lands on remote, then reports unsure.
			if err := real(); err != nil {
				return err
			}
			return cut
		case 2:
			// Second send is lost locally.
			return cut
		}
		// Advance remote tip on top of the landed first send so the retry is rejected,
		// and sleep past pushTime so retryPending's push ctx is already expired on ErrRejected.
		tip, rerr := d.repo.RemoteRef(ctx, ref)
		if rerr != nil || tip == "" {
			advanceErr.Store(fmt.Errorf("RemoteRef: %q %w", tip, rerr))
			return bus.ErrRejected
		}
		c, cerr := d.repo.NewCommit(ctx, tip, "remote advance\n")
		if cerr != nil {
			advanceErr.Store(fmt.Errorf("NewCommit: %w", cerr))
			return bus.ErrRejected
		}
		if perr := d.repo.Push(ctx, c, ref, bus.FastForward, ""); perr != nil {
			advanceErr.Store(fmt.Errorf("Push: %w", perr))
			return bus.ErrRejected
		}
		time.Sleep(pushTime + 80*time.Millisecond)
		return bus.ErrRejected
	}
	d.writeMu.Unlock()

	first, second := make(chan settled, 1), make(chan settled, 1)
	for _, c := range []struct {
		subject string
		got     chan settled
	}{{"landed", first}, {"lost", second}} {
		err := d.appendEventIf(ctx, message(d, c.subject), nil, func(landed, absent bool, _ error) {
			c.got <- settled{landed, absent}
		})
		if !errors.As(err, new(pendingError)) {
			t.Fatalf("send %q: %v, want pending", c.subject, err)
		}
	}
	for _, c := range []struct {
		name string
		got  chan settled
		want settled
	}{
		{"landed send", first, settled{landed: true}},
		{"lost send", second, settled{absent: true}},
	} {
		select {
		case s := <-c.got:
			if s != c.want {
				t.Errorf("%s: settled %+v, want %+v", c.name, s, c.want)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s was never settled", c.name)
		}
	}
	if v := advanceErr.Load(); v != nil {
		t.Fatalf("advancing remote tip failed: %v", v)
	}
	if calls := n.Load(); calls != 3 {
		t.Errorf("testPush called %d times, want 3 (settled on first rejected retry)", calls)
	}
}

// When reconciling a rejected push is canceled during IsAncestor, settlePendingOn leaves unchecked
// sends pending rather than reporting them refused (#1073).
func TestSettlePendingOnKeepsUncheckedOnCanceledContext(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{PendingRetry: time.Hour, PendingGiveUp: time.Hour}, false)
	tip := d.sendFirst(t)
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	d.pendBase = tip
	settled := false
	d.pendSends = []pendingSend{{
		sha:    tip,
		settle: func(bool, bool, error) { settled = true },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if d.settlePendingOn(ctx, tip, bus.ErrRejected, false) {
		t.Fatal("settlePendingOn returned true on a canceled context")
	}
	if settled || len(d.pendSends) != 1 {
		t.Errorf("settled=%v len=%d, want unchecked send preserved", settled, len(d.pendSends))
	}
}
