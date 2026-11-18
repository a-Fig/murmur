package daemon

import (
	"context"
	"crypto/ed25519"
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

// presenceCommit makes the commit of p, sealed with k if k isn't nil, without putting it on the
// remote, so the same presence can be put back later.
func (r *giveUpRig) presenceCommit(p bus.Presence, k ed25519.PrivateKey) string {
	r.t.Helper()
	msg, err := bus.Encode(p, "presence")
	if k != nil {
		msg, err = bus.EncodeSealed(p, "presence", k, giveUpPeer, brand.PresencePrefix+giveUpPeer)
	}
	if err != nil {
		r.t.Fatal(err)
	}
	tree := r.git(r.author, "hash-object", "-t", "tree", "-w", "--stdin")
	return r.git(r.author, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", tree, "-m", msg)
}

// pushPresenceSHA puts the presence commit sha on the peer's presence ref.
func (r *giveUpRig) pushPresenceSHA(sha string) {
	r.t.Helper()
	r.git(r.author, "push", "-q", "--force", r.remote, sha+":"+brand.PresencePrefix+giveUpPeer)
}

// farPresence is the peer's presence with one session, quiet-otter, and its agents' Receive files
// off or on: a current daemon's.
func farPresence(receiveOff bool) bus.Presence {
	return bus.Presence{V: bus.Version, Device: giveUpPeer, Host: "far", At: time.Now().UTC(), Files: true, FilesOff: receiveOff,
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "claude", Started: time.Now().UTC()}}}
}

// fromBeforeFiles is the peer's presence as a daemon from before files writes it.
func fromBeforeFiles() bus.Presence {
	p := farPresence(false)
	p.Files = false
	return p
}

const (
	farTakesNone = "  files: takes none (Receive files is off for agents on its device)"
	farUnknown   = "  files: takes: unknown"
)

// wantFiles fails the test unless lookup's files line for quiet-otter on the peer is want.
func (r *giveUpRig) wantFiles(when, want string) {
	r.t.Helper()
	if _, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa")); line != want {
		r.t.Errorf("%s: lookup says %q, want %q", when, line, want)
	}
}

// A sync whose read of an upgraded device's presence runs out of time, after the fetch brought it,
// leaves its files unknown, not "older than files", until a sync reads it (#859, Sol's review).
func TestAPresenceFetchedButNotReadInTimeLeavesFilesUnknown(t *testing.T) {
	r := newGiveUpRig(t)
	addr := bus.Addr(giveUpPeer, "aaaaaa")
	r.pushPresenceOf(fromBeforeFiles())
	r.sync()
	r.wantFiles("before the upgrade", "  files: takes none (its "+brand.Product+" is older than files)")

	r.pushPresenceOf(farPresence(true))
	limit := r.d.opt.ReadFirstLimit
	r.d.opt.ReadFirstLimit = time.Nanosecond // the presence is fetched, and its read cut short
	r.d.pollOnce(r.ctx)                      // its error is the cut, which is what this is about
	r.d.opt.ReadFirstLimit = limit
	r.d.mu.Lock()
	fetched := r.d.known[brand.PresencePrefix+giveUpPeer] == r.d.presenceTip[giveUpPeer]
	read := r.d.peers[giveUpPeer].readSHA == r.d.presenceTip[giveUpPeer]
	r.d.mu.Unlock()
	if !fetched || read {
		t.Fatalf("fetched %v and read %v; want it fetched and not read, or this tests nothing", fetched, read)
	}
	r.wantFiles("fetched, not read", farUnknown)
	if note := r.noFilesNote(addr); note != "" {
		t.Errorf("send's note, fetched and not read: %q, want none", note)
	}

	r.sync()
	r.wantFiles("read", farTakesNone)
}

// While an upgraded device's new presence is being fetched, what its old one said of files isn't
// given as true (#859, Sol's review). The fetch is held by the remote's upload-pack, the second
// request of the sync after its listing, until the test lets it go.
func TestAPresenceBeingFetchedLeavesFilesUnknown(t *testing.T) {
	r := newGiveUpRig(t)
	r.pushPresenceOf(fromBeforeFiles())
	r.sync()
	r.wantFiles("before the upgrade", "  files: takes none (its "+brand.Product+" is older than files)")
	r.pushPresenceOf(farPresence(true))

	dir := filepath.ToSlash(t.TempDir())
	script := dir + "/hold.sh"
	body := "#!/bin/sh\n" +
		"echo x >> '" + dir + "/calls'\n" +
		"if [ \"$(wc -l < '" + dir + "/calls')\" -eq 2 ]; then\n" +
		"\ttouch '" + dir + "/held'\n" +
		"\twhile [ ! -f '" + dir + "/release' ]; do sleep 0.1; done\n" +
		"fi\n" +
		"exec git upload-pack \"$1\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(r.work, "config", "remote.origin.uploadpack", "sh '"+script+"'")
	release := func() {
		if _, err := os.Stat(dir + "/release"); err != nil {
			os.WriteFile(dir+"/release", nil, 0o644)
		}
	}
	t.Cleanup(release)

	done := make(chan error, 1)
	go func() { done <- r.d.pollOnce(context.Background()) }()
	held := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(dir + "/held"); err == nil {
			held = true
			break
		}
	}
	if !held {
		release()
		t.Fatalf("the fetch was never held, so this tested nothing (the sync returned %v)", <-done)
	}
	r.wantFiles("being fetched", farUnknown)
	release()
	if err := <-done; err != nil {
		t.Fatalf("the sync: %v", err)
	}
	r.wantFiles("fetched and read", farTakesNone)
}

// A presence read, then a newer one that fails to fetch, then the ref back on the one read: what it
// said of files is known again, though no read clears anything (#859, Sol's review).
func TestFilesAreKnownAgainWhenThePresenceGoesBackToTheOneRead(t *testing.T) {
	r := newGiveUpRig(t)
	a := r.presenceCommit(farPresence(true), nil)
	r.pushPresenceSHA(a)
	r.sync()
	r.wantFiles("A read", farTakesNone)

	recovered := r.failPresenceFetches()
	defer recovered()
	r.pushPresenceOf(farPresence(false))
	r.sync()
	r.wantFiles("B unfetched", farUnknown)

	r.pushPresenceSHA(a)
	r.sync()
	r.wantFiles("back on A", farTakesNone)
}

// A presence refused as not the device's own, sealed with a key other than the one pinned for it,
// leaves the last good one standing, files and all (#859, Sol's review).
func TestARefusedPresenceLeavesWhatTheLastGoodOneSaidOfFiles(t *testing.T) {
	r := newGiveUpRig(t)
	addr := bus.Addr(giveUpPeer, "aaaaaa")
	sealed := func(receiveOff bool) bus.Presence {
		p := farPresence(receiveOff)
		p.BoxKey = bus.BoxPublic(bus.NewBoxKey())
		return p
	}
	r.pushPresenceSHA(r.presenceCommit(sealed(true), bus.NewSignKey()))
	r.sync()
	r.wantFiles("A read", farTakesNone)

	b := r.presenceCommit(sealed(false), bus.NewSignKey()) // not the key pinned for the device
	r.pushPresenceSHA(b)
	r.sync()
	r.d.mu.Lock()
	refused := r.d.refusedSHA[giveUpPeer] == b
	r.d.mu.Unlock()
	if !refused {
		t.Fatal("B wasn't refused, so this tested nothing")
	}
	r.wantFiles("B refused", farTakesNone)
	if note := r.noFilesNote(addr); !strings.Contains(note, "Receive files is off for the agents on its device") {
		t.Errorf("send's note, B refused: %q", note)
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
// predates them: its Files wasn't read, so it isn't false (#859). Its names are kept, as for one sealed for a
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
	if peer == nil || peer.locked || peer.readSHA != "" {
		t.Fatalf("the peer: %+v, want it known, with nothing read and not locked", peer)
	}
	got, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa"))
	if want := "  files: takes: unknown"; got != "" || line != want {
		t.Errorf("lookup: %q (%q), want %q", line, got, want)
	}
}
