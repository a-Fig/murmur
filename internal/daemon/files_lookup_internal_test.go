package daemon

import (
	"context"
	"crypto/ecdh"
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
	farTakesThem = "  files: takes them"
	farUnknown   = "  files: takes: unknown"
)

// wantFiles fails the test unless lookup's files line for quiet-otter on the peer is want.
func (r *giveUpRig) wantFiles(when, want string) {
	r.t.Helper()
	if _, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa")); line != want {
		r.t.Errorf("%s: lookup says %q, want %q", when, line, want)
	}
}

// wantNote fails the test unless send's note on a message with files to quiet-otter says its
// agents' Receive files is off, if off, and says nothing otherwise: an unknown recipient is
// promised nothing either way.
func (r *giveUpRig) wantNote(when string, off bool) {
	r.t.Helper()
	note := r.noFilesNote(bus.Addr(giveUpPeer, "aaaaaa"))
	if off && !strings.Contains(note, "Receive files is off for the agents on its device") || !off && note != "" {
		r.t.Errorf("%s: send's note is %q", when, note)
	}
}

// remoteSHA is the presence commit the peer's ref points to on the remote now.
func (r *giveUpRig) remoteSHA() string {
	r.t.Helper()
	return r.git(r.remote, "rev-parse", brand.PresencePrefix+giveUpPeer)
}

// before is what the peer's presence said before a newer one came, in Sol's scenarios (#865): a
// daemon from before files, or a current one with its agents' Receive files off. While the newer
// one isn't read, a device that has files keeps its setting. One without is unknown throughout,
// never taken for a daemon from before files: it may have just upgraded. Once read, the newer one says its own.
var before = []struct {
	name    string
	pres    func() bus.Presence
	newer   bus.Presence
	shown   string // what lookup says before the newer presence comes
	off     bool   // whether send's note says Receive files is off, then and while the newer one waits
	read    string // once the newer one is read
	readOff bool
}{
	{"from before files", fromBeforeFiles, farPresence(true), farUnknown, false, farTakesNone, true},
	{"with files", func() bus.Presence { return farPresence(true) }, farPresence(false), farTakesNone, true, farTakesThem, false},
}

// A sync whose read of the newer presence runs out of time, after the fetch brought it, leaves the
// device's files where its last read put them, or unknown, until a sync reads it (#859, Sol's
// review).
func TestAPresenceFetchedButNotReadInTime(t *testing.T) {
	for _, b := range before {
		t.Run(b.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			r.pushPresenceOf(b.pres())
			r.sync()
			r.wantFiles("before", b.shown)

			r.pushPresenceOf(b.newer)
			newer := r.remoteSHA()
			limit := r.d.opt.ReadFirstLimit
			r.d.opt.ReadFirstLimit = time.Nanosecond // the presence is fetched, and its read cut short
			r.d.pollOnce(r.ctx)                      // its error is the cut, which is what this is about
			r.d.opt.ReadFirstLimit = limit
			r.d.mu.Lock()
			fetched := r.d.known[brand.PresencePrefix+giveUpPeer] == newer
			read := r.d.peers[giveUpPeer].sha == newer
			r.d.mu.Unlock()
			if !fetched || read {
				t.Fatalf("fetched %v and read %v; want it fetched and not read, or this tests nothing", fetched, read)
			}
			r.wantFiles("fetched, not read", b.shown)
			r.wantNote("fetched, not read", b.off)

			r.sync()
			r.wantFiles("read", b.read)
			r.wantNote("read", b.readOff)
		})
	}
}

// While the newer presence is being fetched, the device's files are where its last read put them,
// or unknown (#859, Sol's review). The remote's upload-pack holds every request after the sync's
// listing, a fetch's retry too, until the test lets them go.
func TestAPresenceBeingFetched(t *testing.T) {
	for _, b := range before {
		t.Run(b.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			r.pushPresenceOf(b.pres())
			r.sync()
			r.wantFiles("before", b.shown)
			r.pushPresenceOf(b.newer)
			newer := r.remoteSHA()

			dir := filepath.ToSlash(t.TempDir())
			script := dir + "/hold.sh"
			body := "#!/bin/sh\n" +
				"echo x >> '" + dir + "/calls'\n" +
				"if [ \"$(wc -l < '" + dir + "/calls')\" -ge 2 ]; then\n" +
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
			r.d.mu.Lock()
			read := r.d.peers[giveUpPeer].sha == newer
			r.d.mu.Unlock()
			if read {
				t.Fatal("the newer presence was read while every fetch was held")
			}
			r.wantFiles("being fetched", b.shown)
			r.wantNote("being fetched", b.off)
			release()
			if err := <-done; err != nil {
				t.Fatalf("the sync: %v", err)
			}
			r.wantFiles("fetched and read", b.read)
		})
	}
}

// A presence read, then a newer one that fails to fetch, then the ref back on the one read: what it
// said of files shows throughout (#859, Sol's review).
func TestFilesShowAgainWhenThePresenceGoesBackToTheOneRead(t *testing.T) {
	for _, b := range before {
		t.Run(b.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			a := r.presenceCommit(b.pres(), nil)
			r.pushPresenceSHA(a)
			r.sync()
			r.wantFiles("A read", b.shown)

			recovered := r.failPresenceFetches()
			defer recovered()
			r.pushPresenceOf(b.newer)
			r.sync()
			r.wantFiles("B unfetched", b.shown)

			r.pushPresenceSHA(a)
			r.sync()
			r.wantFiles("back on A", b.shown)
		})
	}
}

// A presence refused as not the device's own, sealed with a key other than the one pinned for it,
// leaves the last good one standing, files and all (#859, Sol's review).
func TestARefusedPresenceLeavesWhatTheLastGoodOneSaidOfFiles(t *testing.T) {
	for _, b := range before {
		t.Run(b.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			sealed := func(p bus.Presence) bus.Presence {
				p.BoxKey = bus.BoxPublic(bus.NewBoxKey())
				return p
			}
			r.pushPresenceSHA(r.presenceCommit(sealed(b.pres()), bus.NewSignKey()))
			r.sync()
			r.wantFiles("A read", b.shown)

			bad := r.presenceCommit(sealed(b.newer), bus.NewSignKey()) // not the key pinned for the device
			r.pushPresenceSHA(bad)
			r.sync()
			r.d.mu.Lock()
			refused := r.d.refusedSHA[giveUpPeer] == bad
			r.d.mu.Unlock()
			if !refused {
				t.Fatal("B wasn't refused, so this tested nothing")
			}
			r.wantFiles("B refused", b.shown)
			r.wantNote("B refused", b.off)
		})
	}
}

// Sol's fourth review of #865: A, then B, a presence sealed for a team key this device doesn't hold
// yet, then C, refused for a signing key other than B's. Nothing that opened says more than A did,
// so lookup shows A's setting, or unknown, and never takes the device for one from before files.
func TestALockedThenARefusedPresenceLeaveTheLastOneRead(t *testing.T) {
	for _, b := range before {
		t.Run(b.name, func(t *testing.T) {
			r := newGiveUpRig(t)
			r.pushPresenceOf(b.pres())
			r.sync()
			r.wantFiles("A read", b.shown)

			team := bus.NewBoxKey()
			k := bus.NewSignKey()
			locked := func(p bus.Presence, k ed25519.PrivateKey) string {
				p.BoxKey = bus.BoxPublic(bus.NewBoxKey())
				enc, err := bus.EncryptPresence(p, map[string]string{bus.TeamID(bus.BoxPublic(team)): bus.BoxPublic(team)})
				if err != nil {
					t.Fatal(err)
				}
				return r.presenceCommit(enc, k)
			}
			r.pushPresenceSHA(locked(b.newer, k))
			r.sync()
			r.d.mu.Lock()
			isLocked := r.d.peers[giveUpPeer].locked
			r.d.mu.Unlock()
			if !isLocked {
				t.Fatal("B wasn't taken for one sealed for a key this device lacks, so this tested nothing")
			}
			r.wantFiles("B locked", b.shown)

			c := locked(b.newer, bus.NewSignKey()) // not B's signing key, which is now pinned
			r.pushPresenceSHA(c)
			r.sync()
			r.d.mu.Lock()
			refused := r.d.refusedSHA[giveUpPeer] == c
			r.d.mu.Unlock()
			if !refused {
				t.Fatal("C wasn't refused, so this tested nothing")
			}
			r.wantFiles("C refused", b.shown)
			r.wantNote("C refused", b.off)
		})
	}
}

// Sol's fourth review of #865: a fetch outside the syncs brings a daemon's presence from before
// files, and lands after the remote has moved on to its upgrade. The sync reads what landed, the
// old one, while the listing shows the new: lookup says unknown, not that it predates files, and the
// next sync reads the upgrade.
func TestAnOldPresenceThatLandsBehindTheUpgradeIsUnknown(t *testing.T) {
	r := newGiveUpRig(t)
	ref := brand.PresencePrefix + giveUpPeer
	r.pushPresenceOf(fromBeforeFiles())
	r.git(r.work, "fetch", "-q", r.remote, "+"+ref+":"+ref) // as the fetch outside the syncs left it
	r.pushPresenceOf(farPresence(true))
	newer := r.remoteSHA()
	r.d.mu.Lock()
	r.d.unfetched[ref] = fetchFail{sha: newer, slow: true, landed: true}
	r.d.mu.Unlock()
	r.sync()
	r.d.mu.Lock()
	p := r.d.peers[giveUpPeer]
	readOld := p != nil && p.sha != "" && p.sha != newer
	r.d.mu.Unlock()
	if !readOld {
		t.Fatalf("the sync didn't read the presence that landed (peer %+v), so this tested nothing", p)
	}
	r.wantFiles("the old one read, the upgrade listed", farUnknown)
	r.wantNote("the old one read, the upgrade listed", false)

	r.sync()
	r.wantFiles("the upgrade read", farTakesNone)
}

// A device that has files, republishing as a heartbeat does, is never unknown in lookup: the last
// presence read stands while each newer one is fetched and read (#859).
func TestADeviceWithFilesIsNeverUnknownAsItRepublishes(t *testing.T) {
	r := newGiveUpRig(t)
	r.pushPresenceOf(farPresence(true))
	r.sync()
	r.wantFiles("first read", farTakesNone)
	limit := r.d.opt.ReadFirstLimit
	for i := range 6 {
		r.pushPresenceOf(farPresence(true)) // a new At, so a new presence
		if i%2 == 0 {
			r.d.opt.ReadFirstLimit = time.Nanosecond // fetched, not read
		}
		r.d.pollOnce(r.ctx)
		r.d.opt.ReadFirstLimit = limit
		r.wantFiles("republished", farTakesNone)
	}
}

// noFilesNote is what send's result would say of a message with files to the session at addr.
func (r *giveUpRig) noFilesNote(addr string) string {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	return r.d.noFilesNoteLocked(bus.Event{Type: bus.Message, From: r.d.device + "/aaaaaa", To: addr}, "quiet-otter")
}

// A device that ran a daemon from before files and has since upgraded, its new presence failing to
// fetch: lookup says its files are unknown and send promises nothing, until the new presence is
// read (#859).
func TestAnUpgradedDeviceWhosePresenceFailsToFetchIsUnknownUntilRead(t *testing.T) {
	r := newGiveUpRig(t)
	r.pushPresenceOf(fromBeforeFiles())
	r.sync()
	r.wantFiles("before the upgrade", farUnknown)
	r.wantNote("before the upgrade", false)

	recovered := r.failPresenceFetches()
	r.pushPresenceOf(farPresence(true))
	r.sync()
	r.wantFiles("its new presence unfetched", farUnknown)
	r.wantNote("its new presence unfetched", false)

	recovered()
	r.sync()
	r.wantFiles("its new presence read", farTakesNone)
	r.wantNote("its new presence read", true)
}

// A presence sealed for a team key this device gets later is unknown until then, and read once it
// has the key (#859).
func TestLookupOfAPresenceForALateKeyIsKnownOnceTheKeyComes(t *testing.T) {
	r := newGiveUpRig(t)
	addr := bus.Addr(giveUpPeer, "aaaaaa")
	team := bus.NewBoxKey()
	id := bus.TeamID(bus.BoxPublic(team))
	r.pushPresence(lockedPresence(t, team))
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != "" || line != farUnknown {
		t.Errorf("before the key: %q (%q), want takes: unknown", line, got)
	}
	r.d.mu.Lock()
	r.d.addTeamKeysLocked(bus.Keyring{id: team})
	r.d.mu.Unlock()
	r.sync()
	if got, line := r.lookupFilesOf(addr); got != proto.FilesOn || line != farTakesThem {
		t.Errorf("with the key: %q (%q), want takes them", line, got)
	}
}

// A presence read again because a key came, its SHA the same, is no sign of life: a device that had
// gone stale stays stale, its lastChange where its last heartbeat put it (#871).
func TestALateKeyDoesntMakeAStaleDeviceLookReachable(t *testing.T) {
	r := newGiveUpRig(t)
	team := bus.NewBoxKey()
	id := bus.TeamID(bus.BoxPublic(team))
	r.pushPresence(lockedPresence(t, team))
	r.sync()
	r.d.mu.Lock()
	p := r.d.peers[giveUpPeer]
	if p == nil || !p.locked {
		r.d.mu.Unlock()
		t.Fatalf("before the key: %+v, want it read and locked", p)
	}
	// No heartbeat since, for longer than StaleAfter.
	quiet := p.lastChange.Add(-2 * r.d.opt.StaleAfter)
	p.lastChange = quiet
	sha := p.sha
	r.d.addTeamKeysLocked(bus.Keyring{id: team})
	r.d.mu.Unlock()
	r.sync()
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	p = r.d.peers[giveUpPeer]
	if p.locked || p.sha != sha {
		t.Fatalf("with the key: locked %v at %s, want %s read again and opened", p.locked, short(p.sha), short(sha))
	}
	if !p.lastChange.Equal(quiet) || !r.d.staleLocked(p) {
		t.Errorf("with the key: lastChange %v, stale %v; want %v, stale", p.lastChange, r.d.staleLocked(p), quiet)
	}
}

// lockedPresence is a sealed presence of giveUpPeer, with one session that takes files, encrypted
// for the team key team.
func lockedPresence(t *testing.T, team *ecdh.PrivateKey) string {
	t.Helper()
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
	return msg
}

// A device whose presence was read and has no Files runs a daemon from before files, or is one
// whose newer presence isn't read yet: lookup can't tell them apart, so says unknown, and send
// promises nothing (#859).
func TestLookupOfADeviceFromBeforeFilesSaysUnknown(t *testing.T) {
	r := newGiveUpRig(t)
	r.pushPresenceOf(fromBeforeFiles())
	r.sync()
	if got, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa")); got != "" || line != farUnknown {
		t.Errorf("lookup: %q (%q), want %q", line, got, farUnknown)
	}
	r.wantNote("from before files", false)
}

// A current daemon's presence whose box doesn't open here says nothing of files: its Files wasn't
// read, so it isn't false (#859). Its names are kept, as for one sealed for a
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
	if peer == nil || peer.locked || peer.read != nil {
		t.Fatalf("the peer: %+v, want it known, with nothing read and not locked", peer)
	}
	got, line := r.lookupFilesOf(bus.Addr(giveUpPeer, "aaaaaa"))
	if want := "  files: takes: unknown"; got != "" || line != want {
		t.Errorf("lookup: %q (%q), want %q", line, got, want)
	}
}
