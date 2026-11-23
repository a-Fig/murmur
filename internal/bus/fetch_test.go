package bus_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
)

// fetchRig is a clone, local, with a remote it fetches bus refs from; push puts a new commit on one
// of them.
type fetchRig struct {
	t      *testing.T
	ctx    context.Context
	local  string
	r      *bus.Repo
	pusher *bus.Repo
}

func newFetchRig(t *testing.T, env ...string) *fetchRig {
	t.Helper()
	root := t.TempDir()
	remote, local, other := filepath.Join(root, "remote.git"), filepath.Join(root, "local.git"), filepath.Join(root, "other.git")
	for _, d := range []string{remote, local, other} {
		if out, err := exec.Command("git", "init", "-q", "--bare", d).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
	}
	return &fetchRig{t: t, ctx: context.Background(), local: local,
		r:      &bus.Repo{Git: gitx.Git{Dir: local, Env: env}, Remote: remote, Name: "test", Email: "test@example.invalid"},
		pusher: &bus.Repo{Git: gitx.Git{Dir: other}, Remote: remote, Name: "peer", Email: "peer@example.invalid"}}
}

func (f *fetchRig) push(ref, msg string) string {
	f.t.Helper()
	sha, err := f.pusher.NewCommit(f.ctx, "", msg)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.pusher.Push(f.ctx, sha, ref, bus.Force, ""); err != nil {
		f.t.Fatal(err)
	}
	return sha
}

// at is the local ref's commit, or "".
func (f *fetchRig) at(ref string) string {
	out, _ := exec.Command("git", "--git-dir="+f.local, "rev-parse", "-q", "--verify", ref).Output()
	return strings.TrimSpace(string(out))
}

// left lists the fetches' own refs.
func (f *fetchRig) left() string {
	f.t.Helper()
	out, err := exec.Command("git", "--git-dir="+f.local, "for-each-ref", "--format=%(refname)", brand.RefRoot).Output()
	if err != nil {
		f.t.Fatal(err)
	}
	var own []string
	for _, name := range strings.Fields(string(out)) {
		if strings.HasPrefix(name, brand.RefRoot+"fetch-") {
			own = append(own, name)
		}
	}
	return strings.Join(own, " ")
}

// lock makes the lock another git would hold on name, a ref or packed-refs, and returns its undoing.
func (f *fetchRig) lock(name string) func() {
	f.t.Helper()
	p := filepath.Join(f.local, filepath.FromSlash(name)+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return func() {
		if err := os.Remove(p); err != nil {
			f.t.Fatal(err)
		}
	}
}

// The refs a fetch writes before it sets the bus refs (#965) go with a Tidy, not with the fetch,
// whose sync would otherwise wait on packed-refs, which a delete locks (#978): when setting the bus
// refs fails, and while another git holds packed-refs, when the bus refs are set all the same and
// the Tidy fails, leaving them to the next. Tidy with all removes those another process left too.
func TestFetchLeavesItsRefsToTidy(t *testing.T) {
	f := newFetchRig(t)
	ctx, ref := f.ctx, brand.RefRoot+"mesh/dev"

	// The fetch lands, and setting the bus ref fails: another git holds its lock.
	first := f.push(ref, "first")
	unlock := f.lock(ref)
	if _, err := f.r.Fetch(ctx, []string{ref}); err == nil {
		t.Error("a fetch whose ref was locked worked")
	}
	unlock()
	if got := f.at(ref); got != "" {
		t.Errorf("a fetch that failed set %s to %s", ref, got)
	}
	if err := f.r.Tidy(ctx, false); err != nil {
		t.Fatal(err)
	}
	if own := f.left(); own != "" {
		t.Errorf("a fetch that failed left %s, tidied", own)
	}

	// A fetch that works leaves its own ref to Tidy too: its caller doesn't wait for a delete.
	if _, err := f.r.Fetch(ctx, []string{ref}); err != nil {
		t.Fatal(err)
	}
	if f.left() == "" {
		t.Error("a fetch removed its own ref itself")
	}
	if err := f.r.Tidy(ctx, false); err != nil {
		t.Fatal(err)
	}

	// Another git holds packed-refs: the bus ref is set, and the fetch's own ref waits for a Tidy
	// that can delete it.
	unlock = f.lock("packed-refs")
	tips, err := f.r.Fetch(ctx, []string{ref})
	if err != nil {
		t.Fatalf("a fetch while packed-refs was locked: %v", err)
	}
	if tips[ref] != first || f.at(ref) != first {
		t.Errorf("the fetch brought %v and set %s to %q, not %s", tips, ref, f.at(ref), first)
	}
	if f.left() == "" {
		t.Error("the fetch removed its own ref itself")
	}
	// However long the clone's config has git wait for the lock (-1: for ever), a Tidy waits a
	// second or so, and the daemon's stop isn't held up.
	if out, err := exec.Command("git", "--git-dir="+f.local, "config", "core.packedRefsTimeout", "-1").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, out)
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	began := time.Now()
	if err := f.r.Tidy(tctx, false); err == nil {
		t.Log("Tidy deleted while packed-refs was locked; this git doesn't lock it to delete a loose ref")
	}
	cancel()
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("a Tidy while packed-refs was locked took %s", took)
	}
	unlock()
	if err := f.r.Tidy(ctx, false); err != nil {
		t.Fatal(err)
	}
	if own := f.left(); own != "" {
		t.Errorf("the Tidy after left %s", own)
	}

	// Refs another process left are Tidy's with all only.
	stray := brand.RefRoot + "fetch-00000000deadbeef/mesh/dev"
	if out, err := exec.Command("git", "--git-dir="+f.local, "update-ref", stray, first).CombinedOutput(); err != nil {
		t.Fatalf("git update-ref: %v: %s", err, out)
	}
	if err := f.r.Tidy(ctx, false); err != nil {
		t.Fatal(err)
	}
	if f.left() != stray {
		t.Errorf("a Tidy without all took %s, which no fetch here wrote", stray)
	}
	if err := f.r.Tidy(ctx, true); err != nil {
		t.Fatal(err)
	}
	if own := f.left(); own != "" {
		t.Errorf("a Tidy with all left %s", own)
	}
}

// A fetch whose time has run out before it begins runs no git at all (#978): not the fetch, nor any
// removing of what it would have written, which on Git for Windows can wait for packed-refs. A sync
// that runs out of time calls Fetch for each ref it has left.
func TestAFetchOutOfTimeRunsNoGit(t *testing.T) {
	trace := filepath.Join(t.TempDir(), "trace")
	f := newFetchRig(t, "GIT_TRACE="+trace)
	ref := brand.RefRoot + "mesh/dev"
	f.push(ref, "first")
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	for range 3 {
		if _, err := f.r.Fetch(ctx, []string{ref}); err == nil {
			t.Fatal("a fetch out of time worked")
		}
	}
	if b, err := os.ReadFile(trace); err == nil && len(b) > 0 {
		t.Errorf("fetches out of time ran git:\n%s", b)
	}
}

// Of two writes to a bus ref, the one begun later stands, whichever ends first (#979): a fetch begun
// before a newer fetch, or before the daemon's own update, can't rewind the ref when it lands. Each
// fetch says what it brought, which is what its caller reads.
func TestAnOlderFetchDoesntRewindTheRef(t *testing.T) {
	f := newFetchRig(t)
	ctx, ref := f.ctx, brand.RefRoot+"presence/dev"
	for _, newer := range []string{"fetch", "update"} {
		t.Run(newer, func(t *testing.T) {
			old := f.push(ref, "old "+newer)
			// The older fetch has its commit, and waits to set the ref until the newer write is done.
			held, release := make(chan struct{}), make(chan struct{})
			first := true
			bus.SetTestBeforeSet(func([]string) {
				if first {
					first = false
					close(held)
					<-release
				}
			})
			defer bus.SetTestBeforeSet(nil)
			type result struct {
				tips map[string]string
				err  error
			}
			older := make(chan result)
			go func() {
				tips, err := f.r.Fetch(ctx, []string{ref})
				older <- result{tips, err}
			}()
			<-held
			var cur string
			switch newer {
			case "fetch":
				cur = f.push(ref, "new "+newer)
				tips, err := f.r.Fetch(ctx, []string{ref})
				if err != nil || tips[ref] != cur {
					t.Fatalf("the newer fetch: %v, brought %v, not %s", err, tips, cur)
				}
			case "update":
				// A commit written here, as the daemon's own are once pushed.
				var err error
				if cur, err = f.r.NewCommit(ctx, "", "new "+newer); err != nil {
					t.Fatal(err)
				}
				if err := f.r.UpdateLocal(ctx, ref, cur); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			res := <-older
			if res.err != nil || res.tips[ref] != old {
				t.Fatalf("the older fetch: %v, brought %v, not %s", res.err, res.tips, old)
			}
			if got := f.at(ref); got != cur {
				t.Errorf("the older fetch, landing last, set %s to %s; the newer %s had set %s", ref, got, newer, cur)
			}
		})
	}
}

// A bus ref another git holds locked fails its write soon, even in a clone whose
// core.filesRefLockTimeout says to wait forever: every Fetch and every send's UpdateLocal sets its
// refs one at a time, and a send whose push had worked waited behind it, for another ref, until
// the Fetch's deadline (#998).
func TestALockedRefFailsItsWriteSoonAndHoldsUpNoOther(t *testing.T) {
	f := newFetchRig(t) // not parallel: other tests set the package's testBeforeSet
	peer, mine := brand.RefRoot+"mesh/peer", brand.RefRoot+"mesh/mine"
	if out, err := exec.Command("git", "--git-dir="+f.local, "config", "core.filesRefLockTimeout", "-1").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, out)
	}
	sha := f.push(peer, "a peer's commit")
	if _, err := f.r.Fetch(f.ctx, []string{peer}); err != nil {
		t.Fatal(err)
	}
	defer f.lock(peer)()

	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	locked := make(chan error, 1)
	go func() { locked <- f.r.UpdateLocal(ctx, peer, sha) }()
	if err := f.r.UpdateLocal(ctx, mine, sha); err != nil {
		t.Fatalf("the write of an unlocked ref: %v", err)
	}
	if err := <-locked; err == nil {
		t.Error("the write of the locked ref worked")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the writes took %s", took)
	}
	if f.at(mine) != sha {
		t.Error("the unlocked ref wasn't set")
	}
}

// A write that waits for another to finish setting refs gives up when its context ends (#998).
func TestAWriteWaitingToSetRefsEndsWithItsContext(t *testing.T) {
	f := newFetchRig(t) // not parallel: other tests set the package's testBeforeSet
	peer, mine := brand.RefRoot+"mesh/peer", brand.RefRoot+"mesh/mine"
	sha := f.push(peer, "a peer's commit")
	if _, err := f.r.Fetch(f.ctx, []string{peer}); err != nil {
		t.Fatal(err)
	}
	release := f.r.HoldSet()
	defer release()
	ctx, cancel := context.WithTimeout(f.ctx, 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.r.UpdateLocal(ctx, mine, sha) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the write worked while another held the lock")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the write was still waiting 10 s after its context ended")
	}
}

// With a git kept between reads, a Fetch asks it what the fetch wrote and starts no for-each-ref,
// which was a process for every fetch (#1008). What it returns, the bus refs it sets, the refs of
// its own it leaves to Tidy, and what a fetch that failed leaves are as they are without one.
func TestFetchAsksTheKeptGitWhatItWrote(t *testing.T) {
	for _, mode := range []string{"a git for each read", "a git kept between reads", "a reader that was closed"} {
		kept := mode == "a git kept between reads"
		t.Run(mode, func(t *testing.T) {
			f := newFetchRig(t)
			if mode != "a git for each read" {
				f.r.KeepReader()
				t.Cleanup(f.r.Close)
			}
			if mode == "a reader that was closed" {
				f.r.Close() // it answers no more, as one that fails, and the refs are listed as without one
			}
			listed := 0
			was := gitx.Observe
			gitx.Observe = func(sub string, _ time.Time, _ time.Duration) {
				if sub == "for-each-ref" {
					listed++
				}
			}
			t.Cleanup(func() { gitx.Observe = was })
			ctx := f.ctx
			mesh, names := brand.RefRoot+"mesh/device-1a2b3c4d", brand.NamesRef

			first, second := f.push(mesh, "first"), f.push(names, "names")
			tips, err := f.r.Fetch(ctx, []string{mesh, names})
			if err != nil || tips[mesh] != first || tips[names] != second || len(tips) != 2 {
				t.Fatalf("the fetch brought %v, %v; want %s and %s", tips, err, first, second)
			}
			if f.at(mesh) != first || f.at(names) != second {
				t.Errorf("the bus refs are at %s and %s, want %s and %s", f.at(mesh), f.at(names), first, second)
			}
			// A ref that moves is read at what the fetch brought, a name no earlier fetch used.
			third := f.push(mesh, "third")
			if tips, err := f.r.Fetch(ctx, []string{mesh}); err != nil || tips[mesh] != third || f.at(mesh) != third {
				t.Errorf("after the ref moved the fetch brought %v, %v, and the ref is at %s; want %s", tips, err, f.at(mesh), third)
			}
			if want := map[bool]int{false: 2, true: 0}[kept]; listed != want {
				t.Errorf("%d for-each-ref started by two fetches, want %d", listed, want)
			}
			if f.left() == "" {
				t.Error("the fetches left none of their own refs for Tidy")
			}
			if err := f.r.Tidy(ctx, false); err != nil {
				t.Fatal(err)
			}
			if own := f.left(); own != "" {
				t.Errorf("Tidy left %s: the fetches hadn't noted what they wrote", own)
			}

			// A fetch that fails, one of its refs not being on the remote, brings nothing, sets
			// nothing, and leaves nothing Tidy can't find.
			fourth := f.push(mesh, "fourth")
			gone := brand.RefRoot + "mesh/device-00000000"
			if tips, err := f.r.Fetch(ctx, []string{mesh, gone}); err == nil {
				t.Errorf("a fetch of a ref that isn't there brought %v", tips)
			}
			if f.at(mesh) != third {
				t.Errorf("a fetch that failed moved the bus ref to %s (the remote's is %s), want it left at %s", f.at(mesh), fourth, third)
			}
			if err := f.r.Tidy(ctx, false); err != nil {
				t.Fatal(err)
			}
			if own := f.left(); own != "" {
				t.Errorf("after a fetch that failed, Tidy left %s", own)
			}
		})
	}
}
