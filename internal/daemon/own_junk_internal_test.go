package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
)

// Something pushed over this device's log that isn't here and can't be fetched in time, as a large
// object would be, is replaced by the next write, which lands; the sends don't each fail on it
// (#222).
func TestAnUnfetchableTipOverTheOwnLogIsReplaced(t *testing.T) {
	t.Parallel()
	d, peer, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
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
	d.testFetch = func(context.Context) error { fetched++; return context.DeadlineExceeded }
	d.writeMu.Unlock()
	if err := d.appendEvent(ctx, message(d, "after the junk")); err != nil {
		t.Fatalf("the send over an unfetchable tip: %v", err)
	}
	tip, err := d.repo.RemoteRef(ctx, d.meshRef())
	if err != nil {
		t.Fatal(err)
	}
	if parent := strings.TrimSpace(runGit(t, d.opt.GitDir, "rev-parse", tip+"^")); tip == junk || parent != before {
		t.Errorf("the remote's tip %s, on %s; want the send, on %s", tip, parent, before)
	}
	if fetched != 1 {
		t.Errorf("the tip was fetched %d times, want once", fetched)
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
