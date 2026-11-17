package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// Something pushed over this device's log that isn't here and can't be fetched in time, as a large
// object would be, is replaced by the next write, which lands; the sends don't each fail on it
// (#222). A fetch that fails otherwise, or that the send's own time cut short, says nothing of the
// tip: the send fails, and the tip stays (Opus's review of #845).
func TestAnUnfetchableTipOverTheOwnLogIsReplaced(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name     string
		fetch    func(ctx context.Context, stop func()) error
		replaced bool
	}{
		{"its fetch runs out of time", func(ctx context.Context, _ func()) error { <-ctx.Done(); return ctx.Err() }, true},
		{"its fetch fails otherwise", func(context.Context, func()) error { return errors.New("cannot lock ref") }, false},
		{"the send's own time runs out", func(ctx context.Context, stop func()) error { stop(); <-ctx.Done(); return ctx.Err() }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, peer, _ := rateRig(t, Options{OwnFetchLimit: 200 * time.Millisecond}, false)
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			before := d.sendFirst(t)
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
			fetched := 0
			d.writeMu.Lock()
			d.testFetch = func(fctx context.Context) error { fetched++; return c.fetch(fctx, stop) }
			d.writeMu.Unlock()
			err = d.appendEvent(ctx, message(d, "after the junk"))
			tip, rerr := d.repo.RemoteRef(context.Background(), d.meshRef())
			if rerr != nil {
				t.Fatal(rerr)
			}
			if !c.replaced {
				if err == nil || tip != junk {
					t.Errorf("the send: %v, the remote's tip %s; want it failed and the tip left", err, tip)
				}
				return
			}
			if err != nil {
				t.Fatalf("the send over an unfetchable tip: %v", err)
			}
			if parent := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-parse", tip+"^")); tip == junk || parent != before {
				t.Errorf("the remote's tip %s, on %s; want the send, on %s", tip, parent, before)
			}
			if fetched != 1 {
				t.Errorf("the tip was fetched %d times, want once", fetched)
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
