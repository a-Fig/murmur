package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// When the daemon stops, pending sends are settled against the remote first (#857): a send whose
// push actually landed isn't reported to its sender as not sent, while one that didn't is given up.
func TestAStoppingDaemonSettlesPendingSendsAgainstTheRemote(t *testing.T) {
	t.Parallel()
	cut := errors.New("the connection dropped")

	t.Run("its push had landed", func(t *testing.T) {
		t.Parallel()
		d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(real func() error) error {
			if err := real(); err != nil {
				return err
			}
			return cut
		}
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "already landed"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		tip, err := d.repo.RemoteRef(ctx, d.meshRef())
		if err != nil {
			t.Fatal(err)
		}
		stop()
		d.bg.Wait()
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 || ownTip != tip || len(inbox) != 0 {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s), inbox=%+v", pending, ownTip, tip, inbox)
		}
	})

	t.Run("its push hadn't landed", func(t *testing.T) {
		t.Parallel()
		d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		before := d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(func() error) error { return cut }
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "never landed"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		stop()
		d.bg.Wait()
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 || ownTip != before {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s)", pending, ownTip, before)
		}
		if len(inbox) != 1 || inbox[0].Event.Subject != `Not sent: "never landed"` || !strings.Contains(inbox[0].Event.Body, errDaemonStopped.Error()) {
			t.Errorf("sender's inbox: %+v, want one Not sent notice for daemon stop", inbox)
		}
	})

	t.Run("one landed and a second didn't", func(t *testing.T) {
		t.Parallel()
		d, _, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		d.sendFirst(t)
		var n atomic.Int32
		d.writeMu.Lock()
		d.testPush = func(real func() error) error {
			if n.Add(1) == 1 {
				if err := real(); err != nil {
					return err
				}
			}
			return cut
		}
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "first"}, nil); r.Error != "" {
			t.Fatalf("first send: %s", r.Error)
		}
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "second"}, nil); r.Error != "" {
			t.Fatalf("second send: %s", r.Error)
		}
		stop()
		d.bg.Wait()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if len(inbox) != 1 || inbox[0].Event.Subject != `Not sent: "second"` {
			t.Errorf("sender's inbox: %+v, want Not sent only for second", inbox)
		}
	})

	t.Run("a later commit on the remote carries it", func(t *testing.T) {
		t.Parallel()
		d, peer, _ := rateRig(t, Options{PendingRetry: time.Hour}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(real func() error) error {
			if err := real(); err != nil {
				return err
			}
			return cut
		}
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "carried"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		// Another clone advances this device's log on top of the landed commit, so the remote's tip
		// isn't in this clone's object store yet and must be fetched to settle.
		ref := d.meshRef()
		if err := peer.Fetch(ctx, []string{ref}); err != nil {
			t.Fatal(err)
		}
		landedTip, err := peer.RemoteRef(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := bus.Encode(message(d, "from another clone"), bus.EventSubject)
		if err != nil {
			t.Fatal(err)
		}
		peerTip, err := peer.NewCommit(ctx, landedTip, msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(ctx, peerTip, ref, bus.FastForward, ""); err != nil {
			t.Fatal(err)
		}
		stop()
		d.bg.Wait()
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 || ownTip != peerTip || len(inbox) != 0 {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s), inbox=%+v", pending, ownTip, peerTip, inbox)
		}
	})

	t.Run("stop lands during give-up's fetch", func(t *testing.T) {
		t.Parallel()
		d, peer, _ := rateRig(t, Options{PendingRetry: 20 * time.Millisecond, PendingGiveUp: time.Millisecond, PendingStopLimit: 5 * time.Second}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		d.sendFirst(t)
		ref := d.meshRef()
		goRetry := make(chan struct{})
		var pushCalls, fetchCalls atomic.Int32
		d.writeMu.Lock()
		d.testPush = func(real func() error) error {
			if pushCalls.Add(1) == 1 {
				if err := real(); err != nil {
					return err
				}
				return cut
			}
			<-goRetry
			return cut
		}
		d.testFetch = func(fctx context.Context) error {
			if fetchCalls.Add(1) == 1 {
				stop()
				<-fctx.Done()
				return fctx.Err()
			}
			return d.repo.Fetch(fctx, []string{ref})
		}
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "stop mid-fetch"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		// Another clone advances the remote on top of the landed commit, forcing giveUpPending
		// into fetchOwn where the stop cancels the first fetch.
		if err := peer.Fetch(ctx, []string{ref}); err != nil {
			t.Fatal(err)
		}
		landedTip, err := peer.RemoteRef(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := bus.Encode(message(d, "from another clone"), bus.EventSubject)
		if err != nil {
			t.Fatal(err)
		}
		peerTip, err := peer.NewCommit(ctx, landedTip, msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(ctx, peerTip, ref, bus.FastForward, ""); err != nil {
			t.Fatal(err)
		}
		close(goRetry)
		d.bg.Wait()
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if got := fetchCalls.Load(); got != 2 {
			t.Errorf("fetchCalls = %d, want 2 (interrupted non-stop lookup, then stop-time lookup)", got)
		}
		if pending != 0 || ownTip != peerTip || len(inbox) != 0 {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s), inbox=%+v", pending, ownTip, peerTip, inbox)
		}
	})
}

// A stopping daemon whose remote is unreachable or stalls still finishes promptly (bounded by
// PendingStopLimit) and gives its pending sends up (#857).
func TestAStoppingDaemonWithAnUnreachableRemoteGivesUpPromptly(t *testing.T) {
	t.Parallel()
	cut := errors.New("the connection dropped")

	t.Run("remote URL is unreachable", func(t *testing.T) {
		t.Parallel()
		d, _, clone := rateRig(t, Options{PendingRetry: time.Hour, PendingStopLimit: 2 * time.Second}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		before := d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(func() error) error { return cut }
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "offline stop"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}
		runGit(t, clone, "config", "remote.origin.url", filepath.Join(clone, "no-such-remote.git"))

		start := time.Now()
		stop()
		d.bg.Wait()
		took := time.Since(start)
		if took > 10*time.Second {
			t.Errorf("stop took %s with an unreachable remote, want it prompt", took)
		}
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 || ownTip != before {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s)", pending, ownTip, before)
		}
		if len(inbox) != 1 || inbox[0].Event.Subject != `Not sent: "offline stop"` || !strings.Contains(inbox[0].Event.Body, errDaemonStopped.Error()) {
			t.Errorf("sender's inbox: %+v, want one Not sent notice for daemon stop", inbox)
		}
	})

	t.Run("remote stalls past PendingStopLimit", func(t *testing.T) {
		t.Parallel()
		d, _, clone := rateRig(t, Options{PendingRetry: time.Hour, PendingStopLimit: 500 * time.Millisecond}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		before := d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(func() error) error { return cut }
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "stalled stop"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}

		// Stall the remote check until released; cleanup releases it so no shell lingers on Windows.
		dir := filepath.ToSlash(t.TempDir())
		script := dir + "/stall.sh"
		body := "#!/bin/sh\n" +
			"touch '" + dir + "/stalled'\n" +
			"while [ ! -f '" + dir + "/release' ]; do sleep 0.2; done\n" +
			"touch '" + dir + "/done'\n" +
			"exit 1\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, clone, "config", "remote.origin.uploadpack", "sh '"+script+"'")
		t.Cleanup(func() {
			if _, err := os.Stat(dir + "/stalled"); err != nil {
				return
			}
			if err := os.WriteFile(dir+"/release", nil, 0o644); err != nil {
				t.Error(err)
				return
			}
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				if _, err := os.Stat(dir + "/done"); err == nil {
					return
				}
			}
			t.Error("the stalled request never ended")
		})

		start := time.Now()
		stop()
		d.bg.Wait()
		took := time.Since(start)
		if _, err := os.Stat(dir + "/stalled"); err != nil {
			t.Fatalf("the remote wasn't asked on stop, so this tested nothing")
		}
		if took > 10*time.Second {
			t.Errorf("stop took %s with a stalled remote, want it bounded by PendingStopLimit", took)
		}
		d.writeMu.Lock()
		pending, ownTip := len(d.pendSends), d.ownTip
		d.writeMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 || ownTip != before {
			t.Errorf("after stop: %d pending, ownTip=%s (want %s)", pending, ownTip, before)
		}
		if len(inbox) != 1 || inbox[0].Event.Subject != `Not sent: "stalled stop"` || !strings.Contains(inbox[0].Event.Body, errDaemonStopped.Error()) {
			t.Errorf("sender's inbox: %+v, want one Not sent notice for daemon stop", inbox)
		}
	})

	t.Run("writeMu held past PendingStopLimit", func(t *testing.T) {
		t.Parallel()
		d, _, clone := rateRig(t, Options{PendingRetry: time.Hour, PendingStopLimit: 300 * time.Millisecond}, false)
		stopCtx, stop := context.WithCancel(context.Background())
		defer stop()
		d.writeMu.Lock()
		d.bgCtx = stopCtx
		d.writeMu.Unlock()
		ctx := context.Background()
		d.sendFirst(t)
		d.writeMu.Lock()
		d.testPush = func(func() error) error { return cut }
		d.writeMu.Unlock()
		from := bus.Addr(d.device, "bbbbbb")
		if r := d.sendWith(ctx, from, proto.Request{To: "*", Subject: "lock held"}, nil); r.Error != "" {
			t.Fatalf("the send: %s", r.Error)
		}

		// Record any attempt to contact the remote during stop; none should happen once the lock
		// wait exhausts the remote portion of the budget.
		dir := filepath.ToSlash(t.TempDir())
		script := dir + "/contact.sh"
		body := "#!/bin/sh\n" +
			"touch '" + dir + "/contacted'\n" +
			"exec git upload-pack \"$1\"\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, clone, "config", "remote.origin.uploadpack", "sh '"+script+"'")

		d.writeMu.Lock()
		start := time.Now()
		stop()
		d.bg.Wait()
		took := time.Since(start)
		d.writeMu.Unlock()

		if took > 5*time.Second {
			t.Errorf("stop took %s with writeMu held, want it bounded by PendingStopLimit", took)
		}
		if _, err := os.Stat(dir + "/contacted"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the remote was contacted after writeMu was held past PendingStopLimit (stat err: %v)", err)
		}
		d.pendMu.Lock()
		pending := len(d.pendSends)
		d.pendMu.Unlock()
		d.mu.Lock()
		inbox := d.st.Inbox["bbbbbb"]
		d.mu.Unlock()
		if pending != 0 {
			t.Errorf("%d sends left pending, want none", pending)
		}
		if len(inbox) != 1 || inbox[0].Event.Subject != `Not sent: "lock held"` || !strings.Contains(inbox[0].Event.Body, errDaemonStopped.Error()) {
			t.Errorf("sender's inbox: %+v, want one Not sent notice for daemon stop", inbox)
		}
	})
}
