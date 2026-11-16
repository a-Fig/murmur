package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// The two races that sank the first fix for #40 (Codex's review, 2026-10-29), as tests: a presence
// newer than the registry it was read with must not have a healthy registry rewritten, and a
// repair found while a push is out must not be cleared when that push lands without the name.

// seePresence has d read a presence of dev's, from a daemon before encryption, that shows one
// session, id, called name. Caller holds advMu, as a sync does.
func seePresence(t *testing.T, d *Daemon, dev, id, name string) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().UTC()
	p := bus.Presence{V: bus.Version, Device: dev, Host: "far", At: at,
		Sessions: []bus.SessionInfo{{ID: id, Name: name, Agent: "claude", Started: at}}}
	msg, err := bus.Encode(p, "presence")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := d.repo.NewCommit(ctx, "", msg)
	if err != nil {
		t.Fatal(err)
	}
	d.updatePeer(ctx, brand.PresencePrefix+dev, sha)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.st.Used[name]; !ok {
		t.Fatalf("%s, in %s's presence, wasn't taken for used", name, dev)
	}
}

// nameCommit makes a registry record on parent in d's clone: name, given to device/session.
func nameCommit(t *testing.T, d *Daemon, parent, name, device, session string) string {
	t.Helper()
	rec := bus.NameRecord{V: bus.Version, Name: name, Device: device, Session: session, At: time.Now().UTC()}
	msg, err := bus.Encode(rec, rec.Subject())
	if err != nil {
		t.Fatal(err)
	}
	sha, err := d.repo.NewCommit(context.Background(), parent, msg)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// Another device takes a name, and its presence shows it, after this daemon last read the
// registry: the presence is newer than what was read, and the name isn't lost. The next sync
// lists the registry with that name in it, and leaves it as it is.
func TestPresenceNewerThanTheRegistryReadIsNoLoss(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	ctx := context.Background()
	theirs := freshNames(t, 81, 1)[0]
	tip := nameCommit(t, d, d.st.NamesTip, theirs, "far-0001", "ffffff")
	runGit(t, filepath.Dir(d.opt.GitDir), "push", "-q", "origin", tip+":"+brand.NamesRef)

	d.advMu.Lock()
	seePresence(t, d, "far-0001", "ffffff", theirs)
	d.processNames(ctx, tip) // the next sync, which lists the registry with the name
	d.advMu.Unlock()

	if d.st.NamesRepair {
		t.Error("a registry that holds every name was set to be repaired")
	}
	if d.st.NamesTip != tip || d.st.NamesGood != tip {
		t.Errorf("the registry's tip became %.7s, last read in full %.7s; want both %.7s", d.st.NamesTip, d.st.NamesGood, tip)
	}
	if w := d.nextNames(); !w.empty() {
		t.Errorf("the assigner has work to do: %+v", w)
	}
	if got, err := d.repo.RemoteRef(ctx, brand.NamesRef); err != nil || got != tip {
		t.Errorf("the registry on the remote is %.7s (%v); it should still be %.7s", got, err, tip)
	}
}

// A sync lists the registry, and before it reads it this daemon's own push lands on the tip
// listed, with what it set aside. Meanwhile a presence showed a name the registry doesn't hold, and
// the sync, reading the tip it listed, finds that name lost. The push that lands must not clear the
// repair: it was built before the name was known, and nothing newer was read to show that it is
// still missing. Otherwise no one writes the name back.
func TestRepairFoundWhileAPushIsOutIsStillMade(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	ctx := context.Background()
	listed := d.st.NamesTip
	lost := freshNames(t, 82, 1)[0]

	d.advMu.Lock() // the sync, which has listed the registry at listed
	pushed := make(chan error, 1)
	go func() { pushed <- d.pushNames(ctx, nameWork{reserve: 1}) }()
	deadline := time.Now().Add(time.Minute)
	for {
		// Landed on the remote: the push is waiting for advMu to record it.
		if got, err := d.repo.RemoteRef(ctx, brand.NamesRef); err != nil {
			t.Fatal(err)
		} else if got != listed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the push didn't land")
		}
		time.Sleep(20 * time.Millisecond)
	}
	seePresence(t, d, "far-0001", "ffffff", lost)
	d.processNames(ctx, listed)
	d.advMu.Unlock()
	if err := <-pushed; err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	repair := d.st.NamesRepair
	d.mu.Unlock()
	if !repair {
		t.Fatalf("after the push landed there is no repair to make, though the registry lacks %s", lost)
	}
	w := d.nextNames()
	if !w.repair {
		t.Fatalf("the assigner has %+v; want a repair", w)
	}
	if err := d.pushNames(ctx, w); err != nil {
		t.Fatal(err)
	}
	tip, err := d.repo.RemoteRef(ctx, brand.NamesRef)
	if err != nil {
		t.Fatal(err)
	}
	if claims, _ := claimsAt(t, d, tip); claims[lost] == "" {
		t.Fatalf("the registry lost %s: %v", lost, claims)
	}
}

// A sync reads the registry before any presence, so a name a presence shows for the first time is
// checked by the next sync, against a listing made after the presence was read. Read the other way,
// the same sync would check it against the registry it listed before fetching the presence, which
// can be older than the name (TestPresenceNewerThanTheRegistryReadIsNoLoss).
func TestASyncReadsTheRegistryBeforeAnyPresence(t *testing.T) {
	t.Parallel()
	remote := map[string]string{
		brand.MeshPrefix + "aaaa-0001":     "1",
		brand.PresencePrefix + "aaaa-0001": "2",
		brand.PresencePrefix + "0000-0001": "3",
		brand.TeamRef:                      "4",
		brand.NamesRef:                     "5",
	}
	order := readOrder(remote)
	if len(order) == 0 || order[0] != brand.NamesRef {
		t.Fatalf("a sync reads %v; the registry should come first", order)
	}
}
