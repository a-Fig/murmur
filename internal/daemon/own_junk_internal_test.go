package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// A tip over this device's log that isn't here and can't be fetched is never replaced on a guess:
// a fetch that runs out of OwnFetchLimit, fails otherwise, or is cut short by the send's own time
// fails the send quickly and leaves the tip, as a slow link times out as a large object does, and
// the tip may be this device's own history (#222, Sol's review of #845). Once the tip is here, as
// the sync's background fetch brings it, sends work again.
func TestAnUnfetchableTipOverTheOwnLogFailsSendsAndIsLeft(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		fetch func(ctx context.Context, stop func()) error
	}{
		{"its fetch runs out of time", func(ctx context.Context, _ func()) error { <-ctx.Done(); return ctx.Err() }},
		{"its fetch fails otherwise", func(context.Context, func()) error { return errors.New("cannot lock ref") }},
		{"the send's own time runs out", func(ctx context.Context, stop func()) error { stop(); <-ctx.Done(); return ctx.Err() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, peer, _ := rateRig(t, Options{OwnFetchLimit: 200 * time.Millisecond}, false)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			d.sendFirst(t)
			msg, err := bus.Encode(bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: "*", Subject: "junk"}, "junk")
			if err != nil {
				t.Fatal(err)
			}
			junk, err := peer.NewCommit(ctx, "", msg)
			if err != nil {
				t.Fatal(err)
			}
			if err := peer.Push(ctx, junk, d.meshRef(), bus.Force, ""); err != nil {
				t.Fatal(err)
			}
			d.writeMu.Lock()
			d.testFetch = func(fctx context.Context) error { return c.fetch(fctx, stop) }
			d.writeMu.Unlock()
			err = d.appendEvent(ctx, message(d, "after the junk"))
			bg := context.Background()
			tip, rerr := d.repo.RemoteRef(bg, d.meshRef())
			if rerr != nil {
				t.Fatal(rerr)
			}
			if err == nil || tip != junk {
				t.Fatalf("the send: %v, the remote's tip %s; want it failed and the tip left", err, tip)
			}
			// The tip arrives, as the sync's background fetch brings it: sends work again.
			if _, err := d.repo.Fetch(bg, []string{d.meshRef()}); err != nil {
				t.Fatal(err)
			}
			d.writeMu.Lock()
			d.testFetch = func(context.Context) error { t.Error("fetched a tip that is here"); return nil }
			d.writeMu.Unlock()
			if err := d.appendEvent(bg, message(d, "once it is here")); err != nil {
				t.Errorf("a send once the tip is here: %v", err)
			}
		})
	}
}

// A tip of this device's log that this clone wrote, ahead of what the daemon knew, as after a
// restart before the first sync, is built on without being fetched.
func TestAnOwnTipAheadIsBuiltOnWithoutAFetch(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
	first := d.sendFirst(t)
	ahead := d.sendFirst(t)
	d.writeMu.Lock()
	d.ownTip = first // as a daemon that hadn't read its own log yet
	d.testFetch = func(context.Context) error { t.Error("fetched a tip this clone wrote"); return nil }
	d.writeMu.Unlock()
	if err := d.appendEvent(ctx, message(d, "next")); err != nil {
		t.Fatal(err)
	}
	tip, err := d.repo.RemoteRef(ctx, d.meshRef())
	if err != nil {
		t.Fatal(err)
	}
	if parent := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-parse", tip+"^")); parent != ahead {
		t.Errorf("the send was built on %s, want the remote's tip %s", parent, ahead)
	}
}
