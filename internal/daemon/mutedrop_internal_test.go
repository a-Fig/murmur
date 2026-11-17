package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// A message from another device that a session here drops, having muted its sender, is listed in
// that session's presence, so the sender's daemon can tell it (#466).
func TestAMessageDroppedForAMuteIsListed(t *testing.T) {
	t.Parallel()
	d, peer, _ := rateRig(t, Options{Poll: 50 * time.Millisecond, MuteGrace: -1}, true)
	ctx := context.Background()
	pres := bus.Presence{V: bus.Version, Device: "peer-0001", Host: "peer", At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "test", Started: time.Now().UTC()}}}
	msg, err := bus.Encode(pres, "peer-0001: 1 sessions")
	if err != nil {
		t.Fatal(err)
	}
	sha, err := peer.NewCommit(ctx, "", msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Push(ctx, sha, brand.PresencePrefix+"peer-0001", bus.Force, ""); err != nil {
		t.Fatal(err)
	}
	within(t, "the peer's session to be seen", time.Minute, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		name, _, _ := d.sessionInfoLocked("peer-0001/aaaaaa")
		return name == "quiet-otter"
	})
	d.mu.Lock()
	d.st.Mutes = map[string]map[string]time.Time{d.tagKeyLocked("bbbbbb"): {"quiet-otter": time.Now()}}
	d.mu.Unlock()
	sent := map[string]bool{}
	tip := ""
	// To everyone and to the device: the peer writes in the clear, which can't be sent to a session.
	for _, to := range []string{"*", d.device} {
		e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", At: time.Now().UTC(),
			To: to, Subject: "hello"}
		msg, err := bus.Encode(e, e.Line())
		if err != nil {
			t.Fatal(err)
		}
		if tip, err = peer.NewCommit(ctx, tip, msg); err != nil {
			t.Fatal(err)
		}
		sent[e.ID] = true
	}
	if err := peer.Push(ctx, tip, brand.MeshPrefix+"peer-0001", bus.FastForward, ""); err != nil {
		t.Fatal(err)
	}
	within(t, "both messages to be listed as dropped", time.Minute, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		got := d.mutedropsLocked("bbbbbb")
		return len(got) == 2 && sent[got[0]] && sent[got[1]]
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := len(d.st.Inbox["bbbbbb"]); n != 0 {
		t.Errorf("the session that muted the sender got %d messages", n)
	}
}

// A sender here is told, once, in its inbox and from Murmur, of a message another device lists as
// dropped for a mute; not of one whose muter it was named when it sent.
func TestASenderIsToldItsMessageWasDroppedForAMute(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, Options{}, false)
	from := bus.Addr(d.device, "bbbbbb")
	quiet := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: from, To: "*", Subject: "the failing page"}
	named := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: from, To: "*", Subject: "the log"}
	elsewhere := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: from, To: "other-0002/cccccc", Subject: "not theirs"}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.outgoingLocked(from, "*", quiet, nil)
	d.outgoingLocked(from, "*", named, []string{"quiet-otter"})
	d.outgoingLocked(from, "brisk-finch", elsewhere, nil)
	// A device lists what it dropped: one it never got, sent to another device's session, isn't believed.
	there := []bus.SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Dropped: []string{quiet.ID, named.ID, elsewhere.ID, bus.NewID()}}}
	d.droppedThereLocked("peer-0001", there)
	d.droppedThereLocked("peer-0001", there) // a presence read again
	inbox := d.st.Inbox["bbbbbb"]
	if len(inbox) != 1 {
		t.Fatalf("the sender's inbox: %+v", inbox)
	}
	e := inbox[0].Event
	if !d.isTellNotice(e) || !strings.Contains(e.Subject, `quiet-otter didn't get "the failing page"`) ||
		!strings.Contains(e.Body, "until they unmute you") {
		t.Errorf("the notice: %+v", e)
	}
	if o := d.st.Outgoing[quiet.ID]; !slices.Contains(o.Told, "quiet-otter") {
		t.Errorf("the message's record after the notice: %+v", o)
	}
}
