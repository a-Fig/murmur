package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

// logBook keeps a daemon's whole log, for a test that waits on what it says.
type logBook struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBook) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBook) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), s)
}

// An automatic check follows what the owner does while it runs (Sol's review of #1289). A lookup
// held open while the owner turns the setting off, switches out of Team mode, or hands the mesh to
// another device writes nothing when it returns, even with someone gone from it. One held open while
// the owner makes a key by hand is taken again for the new key. After a switch out
// of Team mode and back, the new key gets a fresh lookup of its own, not the sample taken for the
// old one. And a cache saved by a daemon that didn't know the setting doesn't turn it off.
func TestAnAutomaticCheckFollowsTheOwnersChanges(t *testing.T) {
	remote := newRemote(t)
	dir := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	if _, err := setup.RefsOnly(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	// Only a clone of a GitHub repo checks collaborators.
	busAddr := "https://github.com/example/owner-changes.git"
	git(t, dir, "config", "url."+filepath.ToSlash(remote)+".insteadOf", busAddr)
	git(t, dir, "remote", "set-url", "origin", busAddr)
	var mu sync.Mutex
	collab := []string{"alice", "bob"}
	var hold chan []string // the next lookup waits for what is sent on it
	var aListed, bListed atomic.Int32
	held, stopped := make(chan struct{}, 1), make(chan bool, 1)
	la, lb := &logBook{}, &logBook{}
	a := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond, logTo: la,
		listCollaborators: func(ctx context.Context, _ string) ([]string, error) {
			aListed.Add(1)
			mu.Lock()
			h, got := hold, collab
			hold = nil
			mu.Unlock()
			if h == nil {
				return got, nil
			}
			held <- struct{}{}
			select {
			case got = <-h: // returned even though the lookup was stopped
			case <-time.After(time.Minute):
				return nil, errors.New("fixture lookup never released")
			}
			stopped <- ctx.Err() != nil
			return got, nil
		}}
	holdNext := func() chan []string {
		h := make(chan []string)
		mu.Lock()
		hold = h
		mu.Unlock()
		return h
	}
	waitHeld := func() {
		t.Helper()
		select {
		case <-held:
		case <-time.After(30 * time.Second):
			t.Fatal("the owner didn't start a lookup")
		}
	}
	release := func(h chan []string, logins []string) {
		t.Helper()
		h <- logins
		if !<-stopped {
			t.Fatal("the lookup in flight wasn't stopped by the change")
		}
	}
	patch := func(p *page, body string) {
		t.Helper()
		if code, resp := p.call("PATCH", "/api/repo-policy", p.token, body); code != http.StatusNoContent {
			t.Fatalf("PATCH %s: %d %s", body, code, resp)
		}
	}
	rosters := func() int {
		return strings.Count(git(t, remote, "log", "--format=%s", brand.TeamRef), "roster for ")
	}
	settled := func(what string, listed int32, epochs, rostered int) {
		t.Helper()
		time.Sleep(time.Second) // a lookup or a write the change failed to stop would come in this time
		if n := aListed.Load(); n != listed {
			t.Fatalf("%s: %d lookups, want %d", what, n, listed)
		}
		if n := len(teamIDs(t, remote)); n != epochs {
			t.Fatalf("%s: %d team keys, want %d", what, n, epochs)
		}
		if n := rosters(); n != rostered {
			t.Fatalf("%s: %d rosters, want %d", what, n, rostered)
		}
	}
	const stale, first, same = "so it starts again", "had no roster yet", "Nobody has left"

	a.start()
	teammate := filepath.Join(t.TempDir(), "teammate")
	git(t, filepath.Dir(teammate), "clone", "-q", "--no-local", remote, teammate)
	git(t, teammate, "config", "url."+filepath.ToSlash(remote)+".insteadOf", busAddr)
	git(t, teammate, "remote", "set-url", "origin", busAddr)
	b := &device{t: t, gitDir: filepath.Join(teammate, ".git"), stale: testStale, poll: 100 * time.Millisecond, logTo: lb,
		listCollaborators: func(context.Context, string) ([]string, error) {
			bListed.Add(1)
			return []string{"alice"}, nil
		}}
	b.start()
	pa, pb := a.page(), b.page()
	eventually(t, "teammate verifies the owner", func() bool { return pb.state().MeshOwner != "" })
	patch(pa, `{"autoRotate":true}`)
	eventually(t, "the first check records the roster", func() bool { return la.count(first) == 1 })
	settled("turned on", 1, 1, 1)

	// Off while a lookup is out: bob is gone from what it returns, and nothing rotates.
	h := holdNext()
	a.stop()
	a.start()
	waitHeld()
	pa = a.page()
	patch(pa, `{"autoRotate":false}`)
	release(h, []string{"alice"})
	eventually(t, "the check starts again", func() bool { return la.count(stale) == 1 })
	settled("turned off during a lookup", 2, 1, 1)

	// Out of Team mode while a lookup is out: no rotation in Private mode.
	h = holdNext()
	patch(pa, `{"autoRotate":true}`)
	waitHeld()
	patch(pa, `{"encryptionMode":"private"}`)
	release(h, []string{"alice"})
	eventually(t, "the check starts again", func() bool { return la.count(stale) == 2 })
	settled("switched out of Team mode during a lookup", 3, 1, 1)

	// Back to Team mode: the switch makes a new key, and the check takes a fresh lookup for it and
	// only records its roster. Bob's leaving was seen by no roster this key had.
	mu.Lock()
	collab = []string{"alice"}
	mu.Unlock()
	patch(pa, `{"encryptionMode":"team"}`)
	eventually(t, "the new key's first check records its roster", func() bool { return la.count(first) == 2 })
	settled("back in Team mode", 4, 2, 2)

	// A daemon that didn't know the setting saves the cache without it, and without the version
	// that says it is there. The next one reads the setting back from the signed history.
	a.stop()
	statePath := filepath.Join(a.gitDir, brand.StateDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	owner, _ := st["teamOwner"].(map[string]any)
	policy, _ := owner["policy"].(map[string]any)
	if policy["autoRotate"] != true || st["teamPolicyV"] == nil {
		t.Fatalf("the cache didn't hold the setting and its version: %s", data)
	}
	delete(policy, "autoRotate")
	delete(st, "teamPolicyV")
	if data, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a.start()
	pa = a.page()
	eventually(t, "the setting is read back from the history", func() bool { return pa.state().AutoRotate })
	eventually(t, "a check once the history is verified", func() bool { return la.count(same) == 1 })
	settled("after an older daemon's cache", 5, 2, 2)

	// A key made by hand while a lookup is out: the lookup was a sample for the key before it, so the
	// check starts again with a fresh one, and doesn't hold the old sample against the new roster.
	h = holdNext()
	a.stop()
	a.start()
	waitHeld()
	if r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice"}, Force: true}); r.Error != "" {
		t.Fatalf("rotate: %+v", r)
	}
	h <- []string{} // alice gone from the old sample
	if <-stopped {
		t.Fatal("a rotation by hand stopped the lookup; it should be compared, then taken again")
	}
	eventually(t, "the check starts again", func() bool { return la.count(stale) == 3 })
	eventually(t, "the fresh lookup finds nobody gone", func() bool { return la.count(same) == 2 })
	settled("rotated by hand during a lookup", 7, 3, 3) // the key made by hand has its roster

	// Handed to b while a lookup is out: a writes nothing, though nobody is left in what it gets.
	h = holdNext()
	a.stop()
	a.start()
	waitHeld()
	if n := bListed.Load(); n != 0 {
		t.Fatalf("the teammate looked up collaborators %d times before it owned the mesh", n)
	}
	if r := a.do(proto.Request{Op: proto.OpTransferOwner, To: b.self()}); r.Error != "" {
		t.Fatalf("transfer: %+v", r)
	}
	// The new owner may be handed the mesh before it is granted the key made by hand. Its check then
	// can't read that key's roster, and can't rotate without the key, so it waits for the grant and
	// checks again when it comes, not at the hour's retry (test-linux on 8034d2c9).
	eventually(t, "the new owner checks", func() bool { return lb.count(same) == 1 })
	release(h, []string{})
	eventually(t, "the old owner's check starts again", func() bool { return la.count(stale) == 4 })
	settled("transferred during a lookup", 8, 3, 3)
	if n, want := bListed.Load(), 1+int32(lb.count("hasn't been granted every team key")); n != want {
		t.Fatalf("the new owner looked up collaborators %d times, want %d", n, want)
	}
}

// A cache from a daemon that didn't know every policy field is read again from the team history's
// root. With the remote's team ref gone, that read goes to the cached tip, from the objects the
// clone still has, and writes resume: presence included (Sol's re-review of #1289).
func TestAnOlderCacheIsReadAgainWithTheTeamRefGone(t *testing.T) {
	remote := newRemote(t)
	dir := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	if _, err := setup.RefsOnly(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	a := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	a.start()
	eventually(t, "the owner verifies its mesh", func() bool { return a.page().state().MeshOwner != "" })
	presence := brand.PresencePrefix + a.self()
	a.stop()
	if teamIDs(t, remote) == nil {
		t.Fatal("no team history on the remote to remove")
	}
	git(t, remote, "update-ref", "-d", brand.TeamRef)
	statePath := filepath.Join(a.gitDir, brand.StateDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	if st["teamPolicyV"] == nil || st["teamTip"] == nil {
		t.Fatalf("the cache didn't hold the team tip and the policy version: %s", data)
	}
	delete(st, "teamPolicyV")
	if data, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitOK(remote, "rev-parse", "--verify", "-q", presence)
	a.start()
	eventually(t, "presence is pushed again", func() bool {
		after := gitOK(remote, "rev-parse", "--verify", "-q", presence)
		return after != "" && after != before
	})
}
