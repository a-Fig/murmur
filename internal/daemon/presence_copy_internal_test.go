package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// The clone's own copy of this device's presence ref is written with the first presence of a run
// and left there by the later ones: loadIdentity reads it only for its seal, and a git for every
// heartbeat was a third of what a heartbeat costs (#1002). A fetch that sets the copy to what the
// remote had, such as junk pushed over the presence, has the next presence write it again.
func TestTheClonesCopyOfItsPresenceIsWrittenOnceARun(t *testing.T) {
	t.Parallel()
	d, peer, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
	ref := d.presenceRef()
	local := func() string {
		t.Helper()
		refs, err := d.repo.LocalRefs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return refs[ref]
	}
	sealed := func(sha string) bool {
		t.Helper()
		c, err := d.repo.ReadCommit(ctx, sha, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		st, seal := bus.OpenSeal(c.Message, ref)
		return st == bus.Sealed && seal.By == d.device
	}
	publish := func() string {
		t.Helper()
		if err := d.publishPresence(ctx); err != nil {
			t.Fatal(err)
		}
		tip, err := d.repo.RemoteRef(ctx, ref)
		if err != nil || tip == "" {
			t.Fatalf("the remote's presence after a publish: %q, %v", tip, err)
		}
		return tip
	}

	first := publish()
	if got := local(); got != first || !sealed(got) {
		t.Fatalf("after the first presence the clone's copy is %q, want %s, sealed", got, first)
	}
	second := publish()
	if second == first {
		t.Fatal("the second presence is the first again, so the test says nothing")
	}
	if got := local(); got != first {
		t.Errorf("after the second presence the clone's copy is %s, want the first's, %s, left alone", got, first)
	}
	d.mu.Lock()
	known := d.known[ref]
	d.mu.Unlock()
	if known != second {
		t.Errorf("the daemon knows its presence as %s, want the one it pushed last, %s", known, second)
	}

	// Junk over the presence, from another clone: the next sync fetches it into the copy.
	junk, err := peer.NewCommit(ctx, "", "not a presence\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Push(ctx, junk, ref, bus.Force, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := local(); got != junk {
		t.Fatalf("after a sync with junk on the remote the clone's copy is %s, want the junk, %s", got, junk)
	}
	third := publish()
	if got := local(); got != third || !sealed(got) {
		t.Errorf("after the presence that follows the clone's copy is %q, want %s, sealed again", got, third)
	}

	// A fetch apart from the syncs, of a ref too slow for one, may have set the copy too.
	d.endSlowFetch(ref, third, third, nil)
	fourth := publish()
	if got := local(); got != fourth {
		t.Errorf("after a slow fetch of the ref and the next presence the clone's copy is %s, want %s", got, fourth)
	}
	// A fetch of the ref that fails is noted too: it may have written the copy before it failed
	// (#1130). Here the sync doesn't even try, the ref having failed a moment ago.
	junk2, err := peer.NewCommit(ctx, "", "not a presence either\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Push(ctx, junk2, ref, bus.Force, ""); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.unfetched = map[string]fetchFail{ref: {sha: junk2, at: time.Now()}}
	d.mu.Unlock()
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := local(); got != fourth {
		t.Fatalf("after a sync that didn't fetch the ref the clone's copy is %s, want it still %s", got, fourth)
	}
	fourth = publish()
	if got := local(); got != fourth {
		t.Errorf("after a failed fetch of the ref and the next presence the clone's copy is %s, want %s", got, fourth)
	}
	// And a fetch of another ref leaves it alone.
	d.endSlowFetch(d.meshRef(), "", "", nil)
	if fifth := publish(); local() != fourth || fifth == fourth {
		t.Errorf("after a slow fetch of another ref the clone's copy is %s, want it left at %s", local(), fourth)
	}
}

// A copy that repairRefs finds unreadable and removes is written again by the next presence. The
// daemon knows that presence already, so no sync fetches the ref back, and without this the copy
// stayed missing for the rest of the run: a clone that then lost its keys and state.json kept its
// ID with new keys, which its teammates refuse (loadIdentity).
func TestARepairedCopyOfThePresenceIsWrittenAgain(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
	ref := d.presenceRef()
	if err := d.publishPresence(ctx); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(d.opt.GitDir, filepath.FromSlash(ref))
	if err := os.WriteFile(file, make([]byte, 41), 0o644); err != nil {
		t.Fatal(err)
	}
	if !d.repairRefs() {
		t.Fatalf("repairRefs left %s, full of null bytes", file)
	}
	if err := d.publishPresence(ctx); err != nil {
		t.Fatal(err)
	}
	tip, err := d.repo.RemoteRef(ctx, ref)
	if err != nil || tip == "" {
		t.Fatalf("the remote's presence after a publish: %q, %v", tip, err)
	}
	refs, err := d.repo.LocalRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := refs[ref]; got != tip {
		t.Errorf("after a repair and the next presence the clone's copy is %q, want %s", got, tip)
	}
}
