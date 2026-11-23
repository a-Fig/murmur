package daemon

import (
	"context"
	"testing"
)

// A presence push is recorded as the device's other pushes are, so the next poll finds nothing
// of its own to fetch: a fetch there would bring back only what updatePeer skips, at a round trip
// to the remote for every heartbeat. The record is the daemon's own; the clone's copy of the ref is
// written with the first presence of a run and left at it (#1100).
func TestOwnPresenceIsNotFetchedBack(t *testing.T) {
	d, _, _ := rateRig(t, Options{}, false)
	ctx := context.Background()
	ref := d.presenceRef()
	first := ""
	for i := 0; i < 2; i++ { // the first push, then a heartbeat over it
		if err := d.publishPresence(ctx); err != nil {
			t.Fatal(err)
		}
		listed, err := d.repo.ListRemote(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if listed[ref] == "" {
			t.Fatalf("push %d: %s isn't on the remote", i+1, ref)
		}
		d.mu.Lock()
		for r, sha := range readable(listed) {
			if d.known[r] != sha {
				t.Errorf("push %d: the next poll would fetch %s: listed at %.7s, known at %.7s", i+1, r, sha, d.known[r])
			}
		}
		d.mu.Unlock()
		local, err := d.repo.LocalRefs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if first == "" {
			first = listed[ref]
		}
		if local[ref] != first {
			t.Errorf("push %d: local %s is %.7s, want what the first push made, %.7s", i+1, ref, local[ref], first)
		}
	}
}
