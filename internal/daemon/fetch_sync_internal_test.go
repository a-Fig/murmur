package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// A sync that runs out of time fetches nothing more (#978). Its batch fails, here on a ref the
// remote hasn't, and the sync's time runs out as it does: the refs it would fetch one by one wait
// for the next sync, and no git runs for them, nor is any set aside as failing.
func TestASyncOutOfTimeStartsNoMoreFetches(t *testing.T) {
	t.Parallel()
	d, peer, _ := rateRig(t, Options{}, false)
	bg := context.Background()
	refs := []string{brand.PresencePrefix + "gone-0001"}
	listed := map[string]string{refs[0]: strings.Repeat("1", 40)}
	for _, dev := range []string{"peer-0001", "peer-0002", "peer-0003"} {
		ref := brand.PresencePrefix + dev
		sha, err := peer.NewCommit(bg, "", "presence of "+dev)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(bg, sha, ref, bus.Force, ""); err != nil {
			t.Fatal(err)
		}
		refs, listed[ref] = append(refs, ref), sha
	}
	trace := filepath.Join(t.TempDir(), "trace")
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	calls := 0
	d.testFetched = func([]string) {
		calls++
		if calls == 1 {
			cancel() // the sync's time runs out during its batch
			d.repo.Git.Env = append(d.repo.Git.Env, "GIT_TRACE="+trace)
		}
	}
	d.advMu.Lock()
	failed, tips := d.fetch(ctx, refs, listed)
	d.advMu.Unlock()
	if calls != 1 {
		t.Errorf("the sync fetched %d times; want its batch alone, once its time was out", calls)
	}
	if b, err := os.ReadFile(trace); err == nil && len(b) > 0 {
		t.Errorf("git ran after the sync's time was out:\n%s", b)
	}
	if len(failed) != len(refs) || len(tips) != 0 {
		t.Errorf("failed %v, brought %v; want every ref failed and nothing brought", failed, tips)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.unfetched) != 0 {
		t.Errorf("refs a sync out of time didn't fetch were set aside: %v", d.unfetched)
	}
}

// A sync goes by what its own fetch brought, not by the local ref, which another write may have set
// since (#979): here the ref is set back to an older commit just after the fetch, as a slow fetch
// begun earlier, landing then, once did.
func TestASyncReadsWhatItsFetchBrought(t *testing.T) {
	t.Parallel()
	d, peer, clone := rateRig(t, Options{}, false)
	ctx := context.Background()
	ref := brand.PresencePrefix + "peer-0001"
	push := func(msg string) string {
		t.Helper()
		sha, err := peer.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		if err := peer.Push(ctx, sha, ref, bus.Force, ""); err != nil {
			t.Fatal(err)
		}
		return sha
	}
	old := push("old")
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cur := push("new")
	d.testFetched = func(refs []string) {
		runGit(t, clone, "update-ref", ref, old)
	}
	if err := d.pollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	got := d.known[ref]
	d.mu.Unlock()
	if got != cur {
		t.Errorf("the sync took %s for %s, not %s, which its fetch brought", got, ref, cur)
	}
}

// The refs fetches leave are removed on a timer, a bus with nothing to fetch too (#982): here, refs a
// daemon stopped mid-fetch left, which no fetch since has come to remove. A try that fails, while
// another git holds packed-refs, is made again.
func TestTheRefsFetchesLeaveAreRemovedOnAQuietBus(t *testing.T) {
	t.Parallel()
	d, _, clone := rateRig(t, Options{TidyEvery: 20 * time.Millisecond}, false)
	sha, err := d.repo.NewCommit(context.Background(), "", "x")
	if err != nil {
		t.Fatal(err)
	}
	stray := brand.RefRoot + "fetch-00000000deadbeef/presence/peer-0001"
	runGit(t, clone, "update-ref", stray, sha)
	lock := filepath.Join(d.opt.GitDir, "packed-refs.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.tidyLoop(ctx)
	}()
	defer func() { cancel(); <-done }()
	left := func() bool {
		return strings.Contains(runGit(t, clone, "for-each-ref", "--format=%(refname)", brand.RefRoot), brand.RefRoot+"fetch-")
	}
	time.Sleep(300 * time.Millisecond) // several tries, each failing on the lock
	if !left() {
		t.Log("the refs went while packed-refs was locked; this git doesn't lock it to delete a loose ref")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); left(); {
		if time.Now().After(deadline) {
			t.Fatalf("%s is still there", stray)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
