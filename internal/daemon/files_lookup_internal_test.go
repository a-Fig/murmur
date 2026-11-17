package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// lookupFilesOf is the files line lookup gives for the session at addr, and what it was made from.
func (r *giveUpRig) lookupFilesOf(addr string) (string, string) {
	r.t.Helper()
	r.d.mu.Lock()
	det := r.d.detailLocked(addr, proto.CLISession, 0)
	r.d.mu.Unlock()
	for _, line := range strings.Split(view.Lookup(proto.Response{Lookup: det}), "\n") {
		if strings.HasPrefix(line, "  files: ") {
			return det.TakesFiles, line
		}
	}
	r.t.Fatalf("lookup of %s has no files line", addr)
	return "", ""
}

// pushPresenceOf puts p, unsealed, on the peer's presence ref.
func (r *giveUpRig) pushPresenceOf(p bus.Presence) {
	r.t.Helper()
	msg, err := bus.Encode(p, "presence")
	if err != nil {
		r.t.Fatal(err)
	}
	r.pushPresence(msg)
}

// failPresenceFetches holds a lock on the local copy of the peer's presence ref, as another git
// would, so its fetch fails. The returned func lets the next sync fetch it again.
func (r *giveUpRig) failPresenceFetches() (recovered func()) {
	r.t.Helper()
	ref := brand.PresencePrefix + giveUpPeer
	lock := filepath.Join(r.gitDir, filepath.FromSlash(ref)+".lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
	return func() {
		r.t.Helper()
		if err := os.Remove(lock); err != nil {
			r.t.Fatal(err)
		}
		r.d.mu.Lock()
		delete(r.d.unfetched, ref) // no minute's wait before the next try
		r.d.mu.Unlock()
	}
}

// noFilesNote is what send's result would say of a message with files to the session at addr.
func (r *giveUpRig) noFilesNote(addr string) string {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	return r.d.noFilesNoteLocked(bus.Event{Type: bus.Message, From: r.d.device + "/aaaaaa", To: addr}, "quiet-otter")
}

// A device that ran a daemon from before files and has since upgraded isn't taken for one from
// before files while its new presence fails to fetch: lookup says its files are unknown and send
// promises nothing, until the new presence is read (#859).
func TestAnUpgradedDeviceWhosePresenceFailsToFetchIsUnknownUntilRead(t *testing.T) {
	r := newGiveUpRig(t)
	addr := bus.Addr(giveUpPeer, "aaaaaa")
	sessions := []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "claude", Started: time.Now().UTC()}}
	r.pushPresenceOf(bus.Presence{V: bus.Version, Device: giveUpPeer, Host: "far", At: time.Now().UTC(), Sessions: sessions})
	r.sync()
	if _, line := r.lookupFilesOf(addr); line != "  files: takes none (its "+brand.Product+" is older than files)" {
		t.Fatalf("before the upgrade: %q", line)
	}
	if note := r.noFilesNote(addr); !strings.Contains(note, "from before files") {
		t.Fatalf("send's note before the upgrade: %q", note)
	}

	recovered := r.failPresenceFetches()
	r.pushPresenceOf(bus.Presence{V: bus.Version, Device: giveUpPeer, Host: "far", At: time.Now().UTC(), Sessions: sessions,
		Files: true, FilesOff: true})
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != "" || line != "  files: takes: unknown" {
		t.Errorf("its new presence unfetched: %q (%q), want takes: unknown", line, got)
	}
	if note := r.noFilesNote(addr); note != "" {
		t.Errorf("send's note, its new presence unfetched: %q, want none", note)
	}

	recovered()
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != proto.FilesOff || line != "  files: takes none (Receive files is off for agents on its device)" {
		t.Errorf("its new presence read: %q (%q)", line, got)
	}
	if note := r.noFilesNote(addr); !strings.Contains(note, "Receive files is off for the agents on its device") {
		t.Errorf("send's note, its new presence read: %q", note)
	}
}

// A presence sealed for a team key this device gets later is unknown until then, and read once it
// has the key (#859).
func TestLookupOfAPresenceForALateKeyIsKnownOnceTheKeyComes(t *testing.T) {
	r := newGiveUpRig(t)
	addr := bus.Addr(giveUpPeer, "aaaaaa")
	team := bus.NewBoxKey()
	id := bus.TeamID(bus.BoxPublic(team))
	enc, err := bus.EncryptPresence(bus.Presence{V: bus.Version, Device: giveUpPeer, Host: "far", At: time.Now().UTC(),
		BoxKey: bus.BoxPublic(bus.NewBoxKey()), Files: true,
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "claude", Started: time.Now().UTC()}}},
		map[string]string{id: bus.BoxPublic(team)})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := bus.EncodeSealed(enc, "presence", bus.NewSignKey(), giveUpPeer, brand.PresencePrefix+giveUpPeer)
	if err != nil {
		t.Fatal(err)
	}
	r.pushPresence(msg)
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != "" || line != "  files: takes: unknown" {
		t.Errorf("before the key: %q (%q), want takes: unknown", line, got)
	}
	r.d.mu.Lock()
	r.d.addTeamKeysLocked(bus.Keyring{id: team})
	r.d.mu.Unlock()
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != proto.FilesOn || line != "  files: takes them" {
		t.Errorf("with the key: %q (%q), want takes them", line, got)
	}
}

// A device whose presence was read and has no Files runs a daemon from before files, and takes none
// (#859).
func TestLookupSaysADeviceFromBeforeFilesTakesNone(t *testing.T) {
	r := newGiveUpRig(t)
	p := bus.Presence{V: bus.Version, Device: giveUpPeer, Host: "far", At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "claude", Started: time.Now().UTC()}}}
	msg, err := bus.Encode(p, "presence")
	if err != nil {
		t.Fatal(err)
	}
	r.pushPresence(msg)
	r.sync()
	got, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa"))
	if want := "  files: takes none (its " + brand.Product + " is older than files)"; got != proto.FilesOld || line != want {
		t.Errorf("lookup: %q (%q), want %q", line, got, want)
	}
}

// A current daemon's presence whose box doesn't open here says nothing of files, not even that it
// predates them: its Files is unread, not false (#859). Its names are kept, as for one sealed for a
// key this device lacks.
func TestLookupOfADeviceWhosePresenceDoesntOpenSaysUnknown(t *testing.T) {
	r := newGiveUpRig(t)
	r.d.mu.Lock()
	to := map[string]string{r.d.device: bus.BoxPublic(r.d.boxKey)}
	r.d.mu.Unlock()
	box, err := bus.SealBox([]byte("not a presence"), []byte("presence\x00"+giveUpPeer), to)
	if err != nil {
		t.Fatal(err)
	}
	p := bus.Presence{V: bus.Version, Device: giveUpPeer, At: time.Now().UTC(), BoxKey: bus.BoxPublic(bus.NewBoxKey()),
		Box: box, Names: map[string]string{"quiet-otter": "aaaaaa"}}
	msg, err := bus.EncodeSealed(p, "presence", bus.NewSignKey(), giveUpPeer, brand.PresencePrefix+giveUpPeer)
	if err != nil {
		t.Fatal(err)
	}
	r.pushPresence(msg)
	r.sync()
	r.d.mu.Lock()
	peer := r.d.peers[giveUpPeer]
	r.d.mu.Unlock()
	if peer == nil || peer.locked || !peer.unread {
		t.Fatalf("the peer: %+v, want it known, unread and not locked", peer)
	}
	got, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa"))
	if want := "  files: takes: unknown"; got != "" || line != want {
		t.Errorf("lookup: %q (%q), want %q", line, got, want)
	}
}
