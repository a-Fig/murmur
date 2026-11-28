package bus

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// An epoch opens for the devices it was wrapped for and gives back its own key, and a box encrypted
// for its team key then opens with that key.
func TestEpochOpens(t *testing.T) {
	team, dev, other := NewBoxKey(), NewBoxKey(), NewBoxKey()
	r, err := NewEpoch(team, map[string]string{"dev-0001": BoxPublic(dev)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Valid() || r.ID != TeamID(BoxPublic(team)) || !IsTeamID(r.ID) {
		t.Fatalf("epoch %+v", r)
	}
	keys, err := r.Open("dev-0001", dev)
	if err != nil || keys[r.ID] == nil {
		t.Fatalf("opened %v %v", keys, err)
	}
	if _, err := r.Open("dev-0002", other); !errors.Is(err, ErrNotForUs) {
		t.Fatalf("opened for a device it wasn't for: %v", err)
	}
	e := Event{V: Version, ID: NewID(), Type: Message, From: "dev-0003/aaaaaa", To: "*", Subject: "hi", At: time.Now().UTC()}
	boxed, err := EncryptEvent(e, "dev-0003", map[string]string{r.ID: r.Pub})
	if err != nil {
		t.Fatal(err)
	}
	if in, err := DecryptEvent(boxed, "dev-0003", keys); err != nil || in.Subject != "hi" {
		t.Fatalf("decrypted %+v, %v", in, err)
	}
	if _, err := DecryptEvent(boxed, "dev-0003", Keyring{"dev-0001": dev}); !errors.Is(err, ErrNotForUs) {
		t.Fatalf("opened without the team key: %v", err)
	}
}

// A grant carries every team key for one device, readable as IDs and nothing more, and a key under
// an ID that isn't its own is dropped when opened.
func TestGrantCarriesEveryKey(t *testing.T) {
	k1, k2, dev := NewBoxKey(), NewBoxKey(), NewBoxKey()
	id1, id2 := TeamID(BoxPublic(k1)), TeamID(BoxPublic(k2))
	g, err := NewGrant("dev-0002", BoxPublic(dev), Keyring{id1: k1, id2: k2, "dev-0001": NewBoxKey()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !g.Valid() || len(g.IDs) != 2 {
		t.Fatalf("grant %+v", g)
	}
	keys, err := g.Open("dev-0002", dev)
	if err != nil || keys[id1] == nil || keys[id2] == nil || len(keys) != 2 {
		t.Fatalf("opened %v, %v", keys, err)
	}
	// A key given under another key's ID is dropped.
	forged, _ := NewGrant("dev-0002", BoxPublic(dev), Keyring{id1: k2}, time.Now())
	if keys, err := forged.Open("dev-0002", dev); err != nil || len(keys) != 0 {
		t.Fatalf("a key under the wrong ID was taken: %v, %v", keys, err)
	}
	// A record whose readable IDs were changed doesn't open: they are bound into the box.
	g.IDs = []string{id1}
	if _, err := g.Open("dev-0002", dev); err == nil {
		t.Fatal("opened with its IDs changed")
	}
}

// A roster opens with the team key it was written for, on any device that holds it, and with no
// other key.
func TestRosterOpensWithTheTeamKey(t *testing.T) {
	team, other := NewBoxKey(), NewBoxKey()
	id := TeamID(BoxPublic(team))
	r, err := NewRoster(id, BoxPublic(team), []string{"alice", "bob"}, time.Now())
	if err != nil || !r.Valid() {
		t.Fatalf("roster %+v, %v", r, err)
	}
	if got, err := r.OpenRoster(Keyring{id: team}); err != nil || len(got) != 2 || got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("opened %v, %v", got, err)
	}
	if _, err := r.OpenRoster(Keyring{TeamID(BoxPublic(other)): other}); !errors.Is(err, ErrNotForUs) {
		t.Fatalf("opened without its team key: %v", err)
	}
	if _, err := r.OpenRoster(Keyring{id: other}); err == nil {
		t.Fatal("opened with another key under its ID")
	}
}

// A record without the form its type needs is junk.
func TestTeamRecordValid(t *testing.T) {
	team, dev := NewBoxKey(), NewBoxKey()
	r, _ := NewEpoch(team, map[string]string{"dev-0001": BoxPublic(dev)}, time.Now())
	bad := r
	bad.ID = TeamID(BoxPublic(NewBoxKey()))
	if bad.Valid() {
		t.Fatal("an epoch whose ID isn't its key's is valid")
	}
	g, _ := NewGrant("dev-0002", BoxPublic(dev), Keyring{r.ID: team}, time.Now())
	g.To = "dev-0003"
	if g.Valid() {
		t.Fatal("a grant wrapped for a device other than its To is valid")
	}
}

// A policy written before automatic rotation was a setting has no autoRotate field, and reads as
// off: an existing mesh doesn't start rotating when its owner updates (#1285). Off is left out of
// what is written, so an older reader sees the policy it knew.
func TestAPolicyWithoutAutoRotateReadsOff(t *testing.T) {
	var p RepoPolicy
	if err := json.Unmarshal([]byte(`{"encryptionMode":"team","allowFiles":true}`), &p); err != nil {
		t.Fatal(err)
	}
	if p != (RepoPolicy{EncryptionMode: EncryptionTeam, AllowFiles: true}) {
		t.Fatalf("read %+v", p)
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"encryptionMode":"team","allowFiles":true}` {
		t.Fatalf("wrote %s", b)
	}
}
