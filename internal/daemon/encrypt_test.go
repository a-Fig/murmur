package daemon_test

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// newOwnedDevice is newDevice with git's user.name set in the clone, which presence carries.
func newOwnedDevice(t *testing.T, remote, owner string) *device {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	git(t, dir, "config", "user.name", owner)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	d.start()
	return d
}

// remoteObjects is every object in the remote, as one string: what anyone who can read the repo
// can read.
func remoteObjects(t *testing.T, remote string) string {
	t.Helper()
	return git(t, remote, "cat-file", "--batch-all-objects", "--batch")
}

// doing is the status a device's who shows for addr, and whether it lists addr.
func (d *device) doing(addr string) (string, bool) {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == addr {
				return s.Doing, true
			}
		}
	}
	return "", false
}

// Three devices, one joining after the others are talking: each reads the others' presence and
// messages, and the remote holds none of what they said or are doing.
func TestBusIsEncrypted(t *testing.T) {
	remote := newRemote(t)
	a := newOwnedDevice(t, remote, "Alice Hiddenowner7")
	b := newOwnedDevice(t, remote, "Bob Hiddenowner7")
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "status-alpha-hidden7"})
	b.do(proto.Request{Op: proto.OpStatus, Session: "bbbbbb", Doing: "status-bravo-hidden7"})
	eventually(t, "each sees the other's status", func() bool {
		da, _ := b.doing(alice)
		db, _ := a.doing(bob)
		return da == "status-alpha-hidden7" && db == "status-bravo-hidden7"
	})

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "subject-direct-hidden7", Body: "body-direct-hidden7"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "bob gets it", func() bool {
		in := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
		return len(in) == 1 && in[0].Body == "body-direct-hidden7" && in[0].FromOwner == "Alice Hiddenowner7"
	})

	// A device that joins later reads the others' presence once they see it and push theirs again,
	// and their messages from then on.
	c := newOwnedDevice(t, remote, "Carol Hiddenowner7")
	carol, _ := c.session("cccccc", "")
	eventually(t, "c reads a's and b's presence", func() bool {
		da, _ := c.doing(alice)
		db, _ := c.doing(bob)
		return da == "status-alpha-hidden7" && db == "status-bravo-hidden7"
	})
	eventually(t, "a reads c's presence", func() bool { return a.seesSession(carol) })
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "bbbbbb", To: "*", Subject: "subject-broadcast-hidden7", Body: "body-broadcast-hidden7"}); r.Error != "" {
		t.Fatalf("broadcast: %+v", r)
	}
	for _, d := range []*device{a, c} {
		eventually(t, "the broadcast", func() bool {
			for _, m := range d.do(proto.Request{Op: proto.OpInbox}).Messages {
				if m.Body == "body-broadcast-hidden7" {
					return true
				}
			}
			return false
		})
	}

	all := remoteObjects(t, remote)
	// The name registry, which stays in the clear, holds session IDs and names; nothing else does.
	// The markers end in a digit so no session's name can hold one: "secret" is one of the
	// adjectives names are made from, and a session named secret-otter failed this test.
	for _, s := range []string{"hidden7", "Hiddenowner7", `"host"`, `"to"`, `"subject"`, `"doing"`, `"agent"`,
		"started (", " sessions\n"} {
		if strings.Contains(all, s) {
			t.Errorf("the remote holds %q", s)
		}
	}
	// What is readable there: device IDs, box keys, seals.
	if !strings.Contains(all, `"boxKey"`) || !strings.Contains(all, `"seal"`) || !strings.Contains(all, a.self()) {
		t.Errorf("the remote holds no box keys or seals:\n%.2000s", all)
	}
}

// presenceOn reads the presence commit of dev on the remote.
func presenceOn(t *testing.T, remote, dev string) bus.Presence {
	t.Helper()
	var p bus.Presence
	if err := bus.Decode(git(t, remote, "log", "-1", "--format=%B", "refs/murmur/presence/"+dev), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// lastEventOn reads the last event of dev's log on the remote, as written.
func lastEventOn(t *testing.T, remote, dev string) bus.Event {
	t.Helper()
	var e bus.Event
	if err := bus.Decode(git(t, remote, "log", "-1", "--format=%B", "refs/murmur/mesh/"+dev), &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// A device from before encryption: it writes in the clear, which an updated device reads, and it
// can't read what an updated device writes, so a message to it is refused rather than sent where
// it can't be read, or sent in the clear.
func TestOlderDevice(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	const old = "oldhost-0001"
	pushOld := func() {
		pres := bus.Presence{V: bus.Version, Device: old, Host: "oldhost", At: time.Now().UTC(),
			Sessions: []bus.SessionInfo{{ID: "oooooo", Agent: "claude", Started: time.Now().UTC()}}}
		msg, err := bus.Encode(pres, old+": 1 sessions")
		if err != nil {
			t.Fatal(err)
		}
		pushMessage(t, remote, "refs/murmur/presence/"+old, msg)
	}
	// Pushed until a sees it: one presence is fresh for three seconds here, and a slow machine can
	// take longer than that to read it (Windows CI on 43ee91c3).
	eventually(t, "a sees the older device's session", func() bool {
		pushOld()
		return a.seesSession(old + "/oooooo")
	})

	pushEvent(t, remote, "refs/murmur/mesh/"+old, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: old + "/oooooo", To: alice, Subject: "from the older device", At: time.Now().UTC()})
	eventually(t, "a reads its plaintext", func() bool {
		in := a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages
		return len(in) == 1 && in[0].Subject == "from the older device"
	})

	pushOld() // fresh again, whatever the stale limit did meanwhile
	eventually(t, "a's session start on its log", func() bool {
		return gitOK(remote, "rev-parse", "--verify", "-q", "refs/murmur/mesh/"+a.self()) != ""
	})
	before := git(t, remote, "rev-parse", "refs/murmur/mesh/"+a.self())
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: old + "/oooooo", Subject: "can't be read there"})
	if !strings.Contains(r.Error, "not sent") || !strings.Contains(r.Error, "before the bus was encrypted") {
		t.Fatalf("send to an older device: %+v", r)
	}
	if after := git(t, remote, "rev-parse", "refs/murmur/mesh/"+a.self()); after != before {
		t.Fatal("something went out")
	}
	// A broadcast goes out, for the devices that can read it, and not in the clear.
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: "*", Subject: "for everyone"}); r.Error != "" {
		t.Fatalf("broadcast: %+v", r)
	}
	e := lastEventOn(t, remote, a.self())
	if e.Type != bus.Boxed || e.Box == nil || len(e.Box.Keys) != 2 || e.Box.Keys[a.self()] == "" || !slices.ContainsFunc(e.Box.Recipients(), bus.IsTeamID) {
		t.Fatalf("broadcast as written, for the team key and its writer alone: %+v", e)
	}
	// What the older device makes of it: an event of a type it doesn't know, passed over; and a's
	// presence, a device with no sessions.
	if e.Clean(); e.Valid() {
		t.Fatal("an older reader would take the box for an event")
	}
	if p := presenceOn(t, remote, a.self()); p.Device != a.self() || len(p.Sessions) != 0 || p.BoxKey == "" || p.Box == nil {
		t.Fatalf("a's presence as an older reader sees it: %+v", p)
	}
}

// syncWriter is a log a test can read while the daemon writes it.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A teammate with push access swaps a device's keys: a presence sealed with another key, or not
// sealed at all, is refused once the device's own key is pinned, and messages go on being encrypted
// to the device's own key.
func TestKeySwapIsRefused(t *testing.T) {
	remote := newRemote(t)
	logs := &syncWriter{}
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	a := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond, logTo: logs}
	a.start()
	b := newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	bdev := b.self()
	eventually(t, "a sees bob", func() bool { return a.seesSession(bob) })
	realKey := presenceOn(t, remote, bdev).BoxKey

	// b goes away, saying goodbye, and its presence is gone; a forger puts its own in its place.
	b.stop()
	eventually(t, "b gone on a", func() bool { return !a.seesSession(bob) })
	evilBox, evilSign := bus.NewBoxKey(), bus.NewSignKey()
	forged := bus.Presence{V: bus.Version, Device: bdev, Host: "forged", At: time.Now().UTC(), BoxKey: bus.BoxPublic(evilBox),
		Sessions: []bus.SessionInfo{{ID: "e0e0e0", Agent: "claude", Started: time.Now().UTC()}}}
	enc, err := bus.EncryptPresence(forged, map[string]string{a.self(): presenceOn(t, remote, a.self()).BoxKey})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := bus.EncodeSealed(enc, bus.PresenceSubject, evilSign, bdev, "refs/murmur/presence/"+bdev)
	if err != nil {
		t.Fatal(err)
	}
	pushMessage(t, remote, "refs/murmur/presence/"+bdev, msg)
	eventually(t, "the refusal", func() bool { return strings.Contains(logs.String(), "isn't the one pinned for "+bdev) })
	if a.seesSession(bdev + "/e0e0e0") {
		t.Fatal("a took the forged presence")
	}
	// Unsealed, the same.
	plain, _ := bus.Encode(bus.Presence{V: bus.Version, Device: bdev, Host: "forged", At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{{ID: "e1e1e1", Agent: "claude", Started: time.Now().UTC()}}}, "x")
	pushMessage(t, remote, "refs/murmur/presence/"+bdev, plain)
	// Refusals are logged once a minute for each device, so this one is known by a having fetched it.
	want := git(t, remote, "rev-parse", "refs/murmur/presence/"+bdev)
	eventually(t, "a fetches the second forgery", func() bool {
		return gitOK(filepath.Dir(a.gitDir), "rev-parse", "refs/murmur/presence/"+bdev) == want
	})
	holds(t, "no forged session on a", 2*time.Second, func() bool {
		return !a.seesSession(bdev+"/e1e1e1") && !a.seesSession(bdev+"/e0e0e0")
	})

	// b comes back with its own keys, and a reads it again; a message to it is encrypted to b's
	// own key, never the forger's.
	b.start()
	b.session("bbbbbb", "")
	eventually(t, "a sees bob again", func() bool { return a.seesSession(bob) })
	if r := a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "still yours"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	if got := presenceOn(t, remote, bdev).BoxKey; got != realKey {
		t.Fatalf("b's key changed across a restart: %s, was %s", got, realKey)
	}
	eventually(t, "bob reads it", func() bool {
		in := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
		return len(in) == 1 && in[0].Subject == "still yours"
	})
}

// cantPush makes d's pushes fail while its fetches still work: a collaborator removed from a public
// repo, who can still read it.
func (d *device) cantPush(remote string, on bool) {
	url := remote
	if on {
		url = filepath.Join(filepath.Dir(remote), "nowhere.git")
	}
	git(d.t, filepath.Dir(d.gitDir), "config", "remote.origin.pushurl", url)
}

// opensLog says whether ring opens every encrypted event on dev's log on the remote.
func opensLog(t *testing.T, remote, dev string, ring bus.Keyring) bool {
	t.Helper()
	work := t.TempDir()
	git(t, work, "init", "-q")
	git(t, work, "fetch", "-q", remote, "refs/murmur/mesh/"+dev)
	for _, msg := range strings.Split(git(t, work, "log", "--format=%B%x1e", "FETCH_HEAD"), "\x1e") {
		var e bus.Event
		if msg = strings.TrimSpace(msg); msg == "" || bus.Decode(msg, &e) != nil || e.Type != bus.Boxed {
			continue
		}
		if _, err := bus.DecryptEvent(e, dev, ring); err != nil {
			return false
		}
	}
	return true
}

// teamIDs is the team keys on the remote's team ref, oldest first.
func teamIDs(t *testing.T, remote string) []string {
	t.Helper()
	if gitOK(remote, "rev-parse", "--verify", "-q", "refs/murmur/team") == "" {
		return nil
	}
	var ids []string
	for _, msg := range strings.Split(git(t, remote, "log", "--reverse", "--format=%B%x1e", "refs/murmur/team"), "\x1e") {
		var r bus.TeamRecord
		if msg = strings.TrimSpace(msg); msg != "" && bus.Decode(msg, &r) == nil && r.Type == bus.TeamEpoch {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// A device that joins later is granted every team key, and reads everything written before it
// existed: the whole history, not only what came after.
func TestLaterDeviceReadsHistory(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	a.session("aaaaaa", "")
	if r := a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "before c existed"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	c := newDevice(t, remote)
	eventually(t, "c is granted the team key", func() bool {
		ids := teamIDs(t, remote)
		return len(ids) == 1 && ringOf(t, c)[ids[0]] != nil
	})
	if !opensLog(t, remote, a.self(), ringOf(t, c)) {
		t.Fatal("c can't read what a wrote before c existed")
	}
}

// A rotation makes a new team key, which a device that can't push never gets, so it reads nothing
// written after; a device that can push gets it, and the old keys with it. Once the device can push
// again, it is granted the new key too.
func TestRotationLeavesOutADeviceThatCantPush(t *testing.T) {
	remote := newRemote(t)
	a, b, c := newDevice(t, remote), newDevice(t, remote), newDevice(t, remote)
	eventually(t, "b and c hold the team key", func() bool {
		ids := teamIDs(t, remote)
		return len(ids) == 1 && ringOf(t, b)[ids[0]] != nil && ringOf(t, c)[ids[0]] != nil
	})
	first := teamIDs(t, remote)[0]
	c.cantPush(remote, true)
	r := a.do(proto.Request{Op: proto.OpRotate, Force: true})
	if r.Error != "" {
		t.Fatalf("rotate: %+v", r)
	}
	eventually(t, "the new epoch", func() bool { return len(teamIDs(t, remote)) == 2 })
	second := teamIDs(t, remote)[1]
	if second == first {
		t.Fatal("the rotation kept the key")
	}
	eventually(t, "b is granted the new key", func() bool { return ringOf(t, b)[second] != nil })
	if r := a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "after the rotation"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	if e := lastEventOn(t, remote, a.self()); e.Box == nil || e.Box.Keys[second] == "" || e.Box.Keys[a.self()] == "" || len(e.Box.Keys) != 2 {
		t.Fatalf("written for %v, want %s and its writer alone", e.Box, second)
	}
	if !opensLog(t, remote, a.self(), ringOf(t, b)) {
		t.Fatal("b can't read a's log after the rotation")
	}
	holds(t, "c never gets the new key while it can't push", 2*time.Second, func() bool {
		return ringOf(t, c)[second] == nil
	})
	if opensLog(t, remote, a.self(), ringOf(t, c)) {
		t.Fatal("c read what was written after the rotation")
	}

	// Pushing again, c is granted the new key, and reads everything.
	c.cantPush(remote, false)
	eventually(t, "c is granted the new key", func() bool { return ringOf(t, c)[second] != nil })
	if !opensLog(t, remote, a.self(), ringOf(t, c)) {
		t.Fatal("c, back, can't read a's log")
	}
}

// Without a collaborator list to compare, a rotation needs --force; with one, it happens only when
// someone on the current epoch's roster has left. Someone who joined since is put on the roster, so
// that their leaving is seen too.
func TestRotateComparesCollaborators(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	eventually(t, "the first epoch", func() bool { return len(teamIDs(t, remote)) == 1 })
	if r := a.do(proto.Request{Op: proto.OpRotate}); r.Error == "" {
		t.Fatalf("rotated with no list: %+v", r)
	}
	// The first epoch has no roster, so the first rotation with a list makes a key that has one.
	if r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice", "bob"}}); r.Error != "" {
		t.Fatalf("rotate: %+v", r)
	}
	eventually(t, "the second epoch", func() bool { return len(teamIDs(t, remote)) == 2 })
	if r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"bob", "alice"}}); r.Error != "" ||
		!strings.Contains(r.Note, "Nobody has left") || strings.Contains(r.Note, "joined") {
		t.Fatalf("the same people, listed in another order: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice", "bob", "carol"}}); r.Error != "" ||
		!strings.Contains(r.Note, "Nobody has left") || !strings.Contains(r.Note, "joined") {
		t.Fatalf("rotated when nobody left: %+v", r)
	}
	if n := len(teamIDs(t, remote)); n != 2 {
		t.Fatalf("%d epochs after carol joined, want 2", n)
	}
	// Carol leaves again: she is on the roster, so that is seen.
	r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice", "bob"}})
	if r.Error != "" || !strings.Contains(r.Note, "carol") {
		t.Fatalf("rotate after carol left: %+v", r)
	}
	eventually(t, "the third epoch", func() bool { return len(teamIDs(t, remote)) == 3 })
}

// The roster is encrypted for the team key, not for the device that wrote it, so a teammate's device
// compares against it too: `mur rotate` there doesn't make a new key when nobody has left.
func TestRosterIsReadByEveryDevice(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	eventually(t, "the first epoch", func() bool { return len(teamIDs(t, remote)) == 1 })
	if r := a.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice", "bob"}}); r.Error != "" {
		t.Fatalf("rotate: %+v", r)
	}
	second := teamIDs(t, remote)[1]
	eventually(t, "b is granted the new key", func() bool { return ringOf(t, b)[second] != nil })
	if r := b.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice", "bob"}}); r.Error != "" ||
		!strings.Contains(r.Note, "Nobody has left") {
		t.Fatalf("b, with nobody gone: %+v", r)
	}
	if n := len(teamIDs(t, remote)); n != 2 {
		t.Fatalf("%d epochs, want 2", n)
	}
	if r := b.do(proto.Request{Op: proto.OpRotate, Collaborators: []string{"alice"}}); r.Error != "" || !strings.Contains(r.Note, "bob") {
		t.Fatalf("b, after bob left: %+v", r)
	}
}

// A clone whose keys are lost while its device ID stays takes a new ID: its teammates would refuse
// a new key under the old one.
func TestLostKeysTakeANewDevice(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	first := a.self()
	a.stop()
	stateDir := filepath.Join(a.gitDir, "murmur")
	if _, err := os.Stat(filepath.Join(stateDir, "keys")); err != nil {
		t.Fatalf("no keys file: %v", err)
	}
	// Restarted with its keys, it is the same device.
	a.start()
	if a.self() != first {
		t.Fatal("a new ID with the same keys")
	}
	a.stop()
	if err := os.Remove(filepath.Join(stateDir, "keys")); err != nil {
		t.Fatal(err)
	}
	a.start()
	second := a.self()
	if second == first {
		t.Fatalf("still %s with new keys", first)
	}
	// With state.json gone too, the clone's copy of its own sealed presence tells the same.
	eventually(t, "a's copy of its own presence", func() bool {
		_, sealed := sealedCopy(filepath.Dir(a.gitDir), second)
		return sealed
	})
	a.stop()
	for _, f := range []string{"keys", "state.json"} {
		if err := os.Remove(filepath.Join(stateDir, f)); err != nil {
			t.Fatal(err)
		}
	}
	a.start()
	if third := a.self(); third == second || third == first {
		t.Fatalf("kept an old ID with new keys: %s", third)
	}
	var st struct {
		Pins map[string]json.RawMessage `json:"pins"`
	}
	b, err := os.ReadFile(filepath.Join(stateDir, "state.json"))
	if err != nil || json.Unmarshal(b, &st) != nil || st.Pins[a.self()] == nil {
		t.Fatalf("no pin for the new device: %s %v", b, err)
	}
}

// gitOK runs git and returns its output, or "" if it fails.
func gitOK(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sealedCopy reads the clone's local copy of dev's presence, and says whether it is sealed by dev.
func sealedCopy(work, dev string) (string, bool) {
	ref := "refs/murmur/presence/" + dev
	msg := gitOK(work, "log", "-1", "--format=%B", ref, "--")
	st, seal := bus.OpenSeal(msg, ref)
	return msg, st == bus.Sealed && seal.By == dev
}

// boxKeyOf reads a test device's private box key from its keys file.
func boxKeyOf(t *testing.T, d *device) *ecdh.PrivateKey {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.gitDir, "murmur", "keys"))
	if err != nil {
		t.Fatal(err)
	}
	var kf struct {
		Box string `json:"box"`
	}
	if err := json.Unmarshal(b, &kf); err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(kf.Box)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// ringOf is every key d opens boxes with: its box key, and the team keys it has been granted.
func ringOf(t *testing.T, d *device) bus.Keyring {
	t.Helper()
	ring := bus.Keyring{d.self(): boxKeyOf(t, d)}
	b, err := os.ReadFile(filepath.Join(d.gitDir, "murmur", "team-keys"))
	if os.IsNotExist(err) {
		return ring
	}
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for id, h := range raw {
		kb, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		k, err := ecdh.X25519().NewPrivateKey(kb)
		if err != nil {
			t.Fatal(err)
		}
		ring[id] = k
	}
	return ring
}

// openedLog is d's log on the remote as d reads it, newest first: each commit's subject and JSON,
// with every encrypted event opened, as the log read before encryption.
func openedLog(t *testing.T, remote string, d *device) string {
	t.Helper()
	dev, ring := d.self(), ringOf(t, d)
	work := t.TempDir()
	git(t, work, "init", "-q")
	git(t, work, "fetch", "-q", remote, "refs/murmur/mesh/"+dev)
	var out []string
	for _, msg := range strings.Split(git(t, work, "log", "--format=%B%x1e", "FETCH_HEAD"), "\x1e") {
		if msg = strings.TrimSpace(msg); msg == "" {
			continue
		}
		var e bus.Event
		if bus.Decode(msg, &e) == nil && e.Type == bus.Boxed {
			in, err := bus.DecryptEvent(e, dev, ring)
			if err != nil {
				t.Fatalf("a box on %s's log doesn't open for it: %v", dev, err)
			}
			if msg, err = bus.Encode(in, in.Line()); err != nil {
				t.Fatal(err)
			}
		}
		out = append(out, strings.TrimSpace(msg))
	}
	return strings.Join(out, "\n")
}

// openedPresence is d's presence on the remote as d reads it: its JSON, opened.
func openedPresence(t *testing.T, remote string, d *device) string {
	t.Helper()
	dev := d.self()
	if gitOK(remote, "rev-parse", "--verify", "-q", "refs/murmur/presence/"+dev) == "" {
		return ""
	}
	p := presenceOn(t, remote, dev)
	if p.Box != nil {
		open, err := bus.DecryptPresence(p, ringOf(t, d))
		if err != nil {
			t.Fatalf("%s's presence doesn't open for it: %v", dev, err)
		}
		p = open
	}
	b, _ := json.Marshal(p)
	return string(b)
}

// lists says whether d's who shows device dev at all, stale or not.
func (d *device) lists(dev string) bool {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		if dv.ID == dev {
			return true
		}
	}
	return false
}
