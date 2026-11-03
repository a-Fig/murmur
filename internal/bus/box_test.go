package bus_test

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// An encrypted event opens for each of its recipients, as the event it was, and for nobody else;
// what it says isn't in what goes on the bus (#156).
func TestEncryptedEventOpensForItsRecipientsOnly(t *testing.T) {
	a, b, c := bus.NewBoxKey(), bus.NewBoxKey(), bus.NewBoxKey()
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "dev-a/s1", At: time.Now().UTC(),
		To: "dev-b/s2", Subject: "the secret subject", Body: "the secret body"}
	out, err := bus.EncryptEvent(e, map[string]string{"dev-a": bus.BoxPublic(a), "dev-b": bus.BoxPublic(b)})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := bus.Encode(out, out.Line())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "secret") || strings.Contains(msg, "dev-b/s2") {
		t.Fatalf("the bus carries what is encrypted:\n%s", msg)
	}
	var read bus.Event
	if err := bus.Decode(msg, &read); err != nil || !read.Valid() {
		t.Fatalf("decoded %+v, %v", read, err)
	}
	for dev, k := range map[string]*ecdh.PrivateKey{"dev-a": a, "dev-b": b} {
		got, err := bus.DecryptEvent(read, dev, k)
		if err != nil || got.Subject != e.Subject || got.Body != e.Body || got.To != e.To {
			t.Errorf("%s opened %+v, %v", dev, got, err)
		}
	}
	if _, err := bus.DecryptEvent(read, "dev-c", c); !errors.Is(err, bus.ErrNotForUs) {
		t.Errorf("a device it isn't for: %v, want ErrNotForUs", err)
	}
	// A recipient's key used under another device's name opens nothing.
	if _, err := bus.DecryptEvent(read, "dev-a", c); err == nil {
		t.Error("opened with the wrong key")
	}
}

// A box can't be moved to another event: it is bound to the event's ID and sender.
func TestEncryptedEventIsBoundToItsEvent(t *testing.T) {
	k := bus.NewBoxKey()
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "dev-a/s1", At: time.Now().UTC(),
		To: "*", Subject: "s"}
	out, err := bus.EncryptEvent(e, map[string]string{"dev-a": bus.BoxPublic(k)})
	if err != nil {
		t.Fatal(err)
	}
	moved := out
	moved.ID = bus.NewID()
	if _, err := bus.DecryptEvent(moved, "dev-a", k); err == nil {
		t.Error("a box moved to another event opened")
	}
	moved = out
	moved.From = "dev-x/s1"
	if _, err := bus.DecryptEvent(moved, "dev-a", k); err == nil {
		t.Error("a box moved to another sender opened")
	}
}

// An encrypted presence keeps its device and box key readable, and hides its host, owner and
// sessions.
func TestEncryptedPresence(t *testing.T) {
	k := bus.NewBoxKey()
	p := bus.Presence{V: bus.Version, Device: "dev-a", Host: "secret-host", Owner: "Secret Owner", At: time.Now().UTC(),
		BoxKey: bus.BoxPublic(k), Sessions: []bus.SessionInfo{{ID: "s1", Doing: "secret work"}}}
	out, err := bus.EncryptPresence(p, map[string]string{"dev-a": bus.BoxPublic(k)})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := bus.Encode(out, "x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, "ecret") || !strings.Contains(msg, p.BoxKey) {
		t.Fatalf("presence on the bus:\n%s", msg)
	}
	var read bus.Presence
	if err := bus.Decode(msg, &read); err != nil {
		t.Fatal(err)
	}
	got, err := bus.DecryptPresence(read, "dev-a", k)
	if err != nil || got.Host != p.Host || got.Owner != p.Owner || len(got.Sessions) != 1 || got.Sessions[0].Doing != "secret work" {
		t.Fatalf("opened %+v, %v", got, err)
	}
}

// A box key is checked before it is used: one that isn't base64, isn't 32 bytes, or is a point that
// agrees no secret (all zeros) would fail every box made for it (#160 review).
func TestValidBoxKey(t *testing.T) {
	if !bus.ValidBoxKey(bus.BoxPublic(bus.NewBoxKey())) {
		t.Error("a real key refused")
	}
	for _, k := range []string{"", "not base64!", "AAAA", base64.StdEncoding.EncodeToString(make([]byte, 32))} {
		if bus.ValidBoxKey(k) {
			t.Errorf("%q taken as a key", k)
		}
	}
}
