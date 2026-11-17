package daemon

import (
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
