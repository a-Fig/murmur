package daemon

import (
	"errors"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// keyRig is a daemon with only what the team key's rules need: one peer, pinned, with a sealed
// presence.
func keyRig(t *testing.T) (*Daemon, *peer) {
	t.Helper()
	d := &Daemon{opt: Options{StaleAfter: time.Minute, StoppedInbox: 24 * time.Hour}, device: "me-0001",
		dir: t.TempDir(), log: log.New(io.Discard, "", 0), boxKey: bus.NewBoxKey(), peers: map[string]*peer{},
		teamKeys: bus.Keyring{}, teamChanged: make(chan struct{}),
		unknownAt: map[string]unknownKey{},
		pollWake:  make(chan struct{}, 1), kick: make(chan struct{}, 1),
		st: state{Pins: map[string]*keyPin{}, TeamGranted: map[string][]string{}, GrantMark: map[string]time.Time{}, GrantReadAt: map[string]time.Time{}}}
	at := time.Now().UTC()
	d.st.Pins["peer-0001"] = &keyPin{Sign: "s", Box: bus.BoxPublic(bus.NewBoxKey()), At: at}
	p := &peer{pres: bus.Presence{V: bus.Version, Device: "peer-0001", At: at}, sealed: true}
	d.peers["peer-0001"] = p
	return d, p
}

// withEpoch gives the rig an epoch, and its key if held.
func withEpoch(d *Daemon, held bool) teamEpoch {
	k := bus.NewBoxKey()
	ep := teamEpoch{ID: bus.TeamID(bus.BoxPublic(k)), Pub: bus.BoxPublic(k), By: d.device, At: time.Now().UTC()}
	d.st.Epochs = append(d.st.Epochs, ep)
	if held {
		d.teamKeys[ep.ID] = k
	}
	return ep
}

// A device is granted the team keys when its sealed presence names the current epoch: it was written
// and pushed after the epoch was made, by a device that could push then. A presence that names an
// older epoch, or none, is no such proof, which is what keeps a removed collaborator from a new
// epoch, replaying an old presence included. How old the presence is doesn't matter, nor whether
// this daemon saw it arrive: a device that joined while nobody was around is granted by the first key
// holder to sync.
func TestGrantNeedsAPresenceNamingTheCurrentEpoch(t *testing.T) {
	d, p := keyRig(t)
	old := withEpoch(d, true)
	ep := withEpoch(d, true)
	for _, named := range []string{"", old.ID} {
		p.pres.Epoch = named
		if due := d.grantsDueLocked(); len(due) != 0 {
			t.Fatalf("granted on a presence naming %q: %v", named, due)
		}
	}
	p.pres.Epoch = ep.ID
	p.pres.At = time.Now().Add(-30 * 24 * time.Hour) // pushed a month ago, and away since
	if due := d.grantsDueLocked(); len(due) != 1 || due[0] != "peer-0001" {
		t.Fatalf("not granted on an old presence naming the current epoch: %v", due)
	}
	p.sealed = false
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted on a presence that isn't sealed: %v", due)
	}
}

// A grant goes by the keys a device says it holds, not by what the team ref shows it was granted: a
// grant that didn't open, or carried a key under another's ID, leaves the device without, and it is
// granted again. A device that holds every key this one could give it is left alone.
func TestGrantGoesByTheKeysADeviceHolds(t *testing.T) {
	d, p := keyRig(t)
	old := withEpoch(d, true)
	ep := withEpoch(d, true)
	p.pres.Epoch = ep.ID
	d.st.TeamGranted["peer-0001"] = []string{old.ID, ep.ID} // the ref says so
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("not granted to a device the ref shows as granted, and that holds nothing: %v", due)
	}
	p.pres.Holds = []string{ep.ID}
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("not granted to a device missing an old key: %v", due)
	}
	p.pres.Holds = []string{old.ID, ep.ID}
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted to a device that holds every key: %v", due)
	}
	// A key this device lacks itself is not one it can give.
	delete(d.teamKeys, old.ID)
	p.pres.Holds = []string{ep.ID}
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted with nothing to give that the device lacks: %v", due)
	}
	// A key for an epoch a rewritten team ref no longer shows is still given.
	k := bus.NewBoxKey()
	d.teamKeys[bus.TeamID(bus.BoxPublic(k))] = k
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("not granted a held key the team ref no longer shows: %v", due)
	}
}

// A roster written without a rotation is written only over the roster it was decided against. If a
// teammate's device has recorded someone meanwhile, writing over it would take them off with no new
// key, and their leaving would never be seen.
func TestRosterIsNotWrittenOverOneItWasNotComparedWith(t *testing.T) {
	d, _ := keyRig(t)
	ep := withEpoch(d, true)
	r, err := bus.NewRoster(ep.ID, ep.Pub, []string{"alice", "bob"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d.st.Epochs[0].Roster = &r
	_, pushed, err := d.teamWork(t.Context(), &teamAsk{collaborators: []string{"alice", "carol"}, basis: []string{"alice"}})
	if !errors.Is(err, errRosterMoved) || pushed {
		t.Fatalf("pushed %v, err %v: want errRosterMoved", pushed, err)
	}
}

// A device that is away keeps the presence it left, which lists the keys it held then. Once a grant
// for it is read, it is granted again only on a presence signed later: it has run since, and still
// lacks a key.
func TestGrantIsNotRepeatedForADeviceThatIsAway(t *testing.T) {
	d, p := keyRig(t)
	ep := withEpoch(d, true)
	p.pres.Epoch = ep.ID
	grant, err := bus.NewGrant("peer-0001", d.st.Pins["peer-0001"].Box, d.teamKeys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sign := bus.NewSignKey()
	msg, err := bus.EncodeSealed(grant, grant.Subject(), sign, d.device, brand.TeamRef)
	if err != nil {
		t.Fatal(err)
	}
	d.applyTeamLocked(bus.Commit{SHA: "abc", Message: msg})
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted again on the presence the grant answered: %v", due)
	}
	// It comes back, and its next presence still lists no key: the grant was junk to it.
	// A presence pushed while it still polls for the grant is no sign it couldn't open it.
	p.pres.At = p.pres.At.Add(time.Second)
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted again before the device had time to read the grant: %v", due)
	}
	d.st.GrantReadAt["peer-0001"] = time.Now().Add(-grantReadWindow - time.Second)
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("not granted again on a later presence that still lacks the key: %v", due)
	}
	p.pres.Holds = []string{ep.ID}
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted to a device that now holds the key: %v", due)
	}
}

// Only a device holding the current key grants, and only to a device with a pinned box key: one on a
// Murmur from before encryption has none.
func TestGrantNeedsTheKeyAndAPin(t *testing.T) {
	d, p := keyRig(t)
	p.pres.Epoch = withEpoch(d, false).ID
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted a key this device doesn't hold: %v", due)
	}
	d2, p2 := keyRig(t)
	p2.pres.Epoch = withEpoch(d2, true).ID
	if due := d2.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("the rig doesn't grant before the pin's box key is taken away: %v", due)
	}
	d2.st.Pins["peer-0001"].Box = ""
	if due := d2.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted a device with no box key: %v", due)
	}
}

// A record is encrypted for the current team key, and for its writer, which reads its own log; before
// there is a team key, nothing goes out. A message to a device on a Murmur from before encryption is
// refused.
func TestEncryptForTheTeamKey(t *testing.T) {
	d, _ := keyRig(t)
	msg := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "me-0001/aaaaaa", To: "*", Subject: "x"}
	if _, err := d.encryptEventLocked(msg); !errors.Is(err, errNoTeamKey) {
		t.Fatalf("encrypted with no team key: %v", err)
	}
	withEpoch(d, false)
	ep := withEpoch(d, false) // a rotation: the newest is current
	boxed, err := d.encryptEventLocked(msg)
	if err != nil {
		t.Fatal(err)
	}
	if got := boxed.Box.Recipients(); len(got) != 2 || got[0] != d.device || got[1] != ep.ID {
		t.Fatalf("encrypted for %v, want %s and %s", got, d.device, ep.ID)
	}
	if in, ok, _ := d.openEventLocked(boxed, d.device, "ref", "sha"); !ok || in.Subject != "x" {
		t.Fatalf("a device without the team key can't read its own record: %+v", in)
	}
	d.peers["old-0001"] = &peer{pres: bus.Presence{V: bus.Version, Device: "old-0001"}}
	msg.To = "old-0001/bbbbbb"
	if _, err := d.encryptEventLocked(msg); err == nil || !strings.Contains(err.Error(), "from before the bus was encrypted") {
		t.Fatalf("a message to an older device: %v", err)
	}
}

// A box for an epoch this device hasn't been granted makes the log wait, and one for a key it holds
// opens. One for a team key the team ref doesn't show waits as well, since a rotation's records can
// be fetched before its epoch is read, until a sync that began after has read the team ref whole;
// then it is junk, passed over.
func TestOpenWaitsForAGrant(t *testing.T) {
	d, _ := keyRig(t)
	ep := withEpoch(d, false)
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: "*", Subject: "x", At: time.Now().UTC()}
	boxed, err := bus.EncryptEvent(e, "peer-0001", map[string]string{ep.ID: ep.Pub})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, wait := d.openEventLocked(boxed, "peer-0001", "ref", "sha"); ok || !wait {
		t.Fatalf("ok %v, wait %v: want a wait", ok, wait)
	}
	stray := bus.NewBoxKey()
	junk, _ := bus.EncryptEvent(e, "peer-0001", map[string]string{bus.TeamID(bus.BoxPublic(stray)): bus.BoxPublic(stray)})
	d.syncSeq, d.teamReadSeq = 7, 7 // met in a sync that had read the team ref before fetching the log
	if _, ok, wait := d.openEventLocked(junk, "peer-0001", "ref", "sha"); ok || !wait {
		t.Fatalf("ok %v, wait %v: want a wait, its epoch may not be read yet", ok, wait)
	}
	d.syncSeq = 8 // the next sync fails to read the team ref
	if _, ok, wait := d.openEventLocked(junk, "peer-0001", "ref", "sha"); ok || !wait {
		t.Fatalf("ok %v, wait %v: want a wait while the team ref is unread", ok, wait)
	}
	d.syncSeq, d.teamReadSeq = 9, 9 // and the one after reads it, with no such key
	if _, ok, wait := d.openEventLocked(junk, "peer-0001", "ref", "sha"); ok || wait {
		t.Fatalf("ok %v, wait %v: want junk passed over", ok, wait)
	}
	// Had that sync shown the key, the record would wait for a grant, not be passed over.
	d.syncSeq, d.teamReadSeq = 10, 10
	if _, ok, wait := d.openEventLocked(junk, "peer-0001", "ref", "sha2"); ok || !wait {
		t.Fatalf("ok %v, wait %v: want a wait for another record", ok, wait)
	}
	d.st.Epochs = append(d.st.Epochs, teamEpoch{ID: bus.TeamID(bus.BoxPublic(stray)), Pub: bus.BoxPublic(stray)})
	d.syncSeq, d.teamReadSeq = 11, 11
	if _, ok, wait := d.openEventLocked(junk, "peer-0001", "ref", "sha2"); ok || !wait {
		t.Fatalf("ok %v, wait %v: want a wait for the grant", ok, wait)
	}
	d2, _ := keyRig(t)
	ep2 := withEpoch(d2, true)
	boxed2, _ := bus.EncryptEvent(e, "peer-0001", map[string]string{ep2.ID: ep2.Pub})
	if in, ok, _ := d2.openEventLocked(boxed2, "peer-0001", "ref", "sha"); !ok || in.Subject != "x" {
		t.Fatalf("didn't open with the key held: %+v", in)
	}
}

// A device that lost its team keys file while its state stayed reads the team ref again: its grants
// are still there, and no teammate would grant again a device the ref shows as granted.
func TestLostTeamKeysAreReadAgain(t *testing.T) {
	d, _ := keyRig(t)
	d.st.TeamOwnerChecked = true
	ep := withEpoch(d, true)
	d.st.TeamTip, d.st.TeamGranted[d.device] = "abc", []string{ep.ID}
	if d.rereadTeamIfKeysLostLocked(); d.st.TeamTip != "abc" {
		t.Fatal("read again with every granted key held")
	}
	delete(d.teamKeys, ep.ID)
	if d.rereadTeamIfKeysLostLocked(); d.st.TeamTip != "" {
		t.Fatal("not read again with a granted key missing")
	}
	// A device still waiting for its first grant has lost nothing.
	d2, _ := keyRig(t)
	withEpoch(d2, false)
	d2.st.TeamTip = "abc"
	if d2.rereadTeamIfKeysLostLocked(); d2.st.TeamTip != "abc" {
		t.Fatal("read again by a device that was never granted the key")
	}
}

// Only a presence signed later than every other taken from the device is new: one put back on its
// ref, or the same one wrapped in a new commit, renews nothing, after a restart included (Sol's
// second review of #156).
func TestOnlyALaterPresenceIsNew(t *testing.T) {
	d, _ := keyRig(t)
	d.st.Pins["peer-0001"].At = time.Time{} // no presence taken yet
	t0 := time.Now().Add(-time.Minute).UTC()
	if !d.newPresenceLocked("peer-0001", t0) {
		t.Fatal("the first presence isn't new")
	}
	if d.newPresenceLocked("peer-0001", t0) || d.newPresenceLocked("peer-0001", t0.Add(-time.Hour)) {
		t.Fatal("the same presence, or an older one, is new")
	}
	if !d.newPresenceLocked("peer-0001", t0.Add(time.Second)) {
		t.Fatal("a later presence isn't new")
	}
}

// A device's own presence times never go back, so its teammates take each as new.
func TestOwnPresenceTimesNeverGoBack(t *testing.T) {
	d, _ := keyRig(t)
	d.st.PresenceAt = time.Now().Add(time.Hour).UTC() // the clock was set back an hour since
	first := d.nextPresenceAtLocked()
	if !first.After(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("went back to %v", first)
	}
	if next := d.nextPresenceAtLocked(); !next.After(first) {
		t.Fatalf("%v isn't after %v", next, first)
	}
}

// A rotation lands right after a grant was read: the device holds the old key and names the new
// epoch, so it lacks the new key, which no grant has carried. It is granted at once.
func TestGrantForANewEpochIsNotHeldBackByTheReadWindow(t *testing.T) {
	d, p := keyRig(t)
	old := withEpoch(d, true)
	d.st.TeamGranted["peer-0001"] = []string{old.ID}
	d.st.GrantMark["peer-0001"] = d.st.Pins["peer-0001"].At
	d.st.GrantReadAt["peer-0001"] = time.Now()
	ep := withEpoch(d, true)
	p.pres.Epoch = ep.ID
	p.pres.Holds = []string{old.ID}
	p.pres.At = p.pres.At.Add(5 * time.Second)
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("new epoch's key withheld from a device granted moments earlier: %v", due)
	}
}

// The window is timed on this daemon's clock from when it read the grant, so a device whose clock
// ran an hour ahead and then went back is granted again once the window has passed.
func TestGrantWindowIgnoresARecipientClockThatWentBack(t *testing.T) {
	d, p := keyRig(t)
	ep := withEpoch(d, true)
	p.pres.Epoch = ep.ID
	mark := time.Now().UTC().Add(time.Hour)
	d.st.TeamGranted["peer-0001"] = []string{ep.ID}
	d.st.GrantMark["peer-0001"] = mark
	d.st.GrantReadAt["peer-0001"] = time.Now().Add(-grantReadWindow - time.Second)
	p.pres.At = mark.Add(time.Millisecond)
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("a device whose clock went back is not granted again: %v", due)
	}
	p.pres.At = mark
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("granted again on a presence no newer than the mark: %v", due)
	}
}

// A GrantReadAt saved while this daemon's clock was ahead, and read back after it was put right,
// holds no window; nor does a GrantMark with no GrantReadAt, as an upgraded state has (#1302 review).
func TestGrantWindowNeedsAReadTimeInThePast(t *testing.T) {
	d, p := keyRig(t)
	ep := withEpoch(d, true)
	p.pres.Epoch = ep.ID
	mark := time.Now().UTC().Add(-time.Minute)
	d.st.TeamGranted["peer-0001"] = []string{ep.ID}
	d.st.GrantMark["peer-0001"] = mark
	p.pres.At = mark.Add(time.Second)
	d.st.GrantReadAt["peer-0001"] = time.Now().Add(time.Hour)
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("a read time in the future held the grant back: %v", due)
	}
	delete(d.st.GrantReadAt, "peer-0001")
	if due := d.grantsDueLocked(); len(due) != 1 {
		t.Fatalf("a mark with no read time held the grant back: %v", due)
	}
	d.st.GrantReadAt["peer-0001"] = time.Now()
	if due := d.grantsDueLocked(); len(due) != 0 {
		t.Fatalf("a grant read just now didn't hold a repeat back: %v", due)
	}
}
