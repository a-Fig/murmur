package bus

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBoxOpensForItsRecipientsOnly(t *testing.T) {
	a, b, c := NewBoxKey(), NewBoxKey(), NewBoxKey()
	box, err := SealBox([]byte("hello"), []byte("aad"), map[string]string{"a": BoxPublic(a), "b": BoxPublic(b)})
	if err != nil {
		t.Fatal(err)
	}
	for dev, key := range map[string]*ecdh.PrivateKey{"a": a, "b": b} {
		if got, err := box.Open(dev, key, []byte("aad")); err != nil || string(got) != "hello" {
			t.Fatalf("%s: %q %v", dev, got, err)
		}
	}
	if _, err := box.Open("c", c, []byte("aad")); !errors.Is(err, ErrNotForUs) {
		t.Fatalf("c opened it: %v", err)
	}
	// c taking a's entry gets nothing: the wrapped key is a's alone.
	if _, err := box.Open("a", c, []byte("aad")); err == nil {
		t.Fatal("c opened a's entry")
	}
	if _, err := box.Open("a", a, []byte("other")); err == nil {
		t.Fatal("opened with other aad")
	}
	// Any change to the ciphertext breaks it.
	tampered := *box
	data := []byte(tampered.Data)
	data[3] ^= 1
	tampered.Data = string(data)
	if _, err := tampered.Open("a", a, []byte("aad")); err == nil {
		t.Fatal("tampered box opened")
	}
}

func TestBoxRefusesAKeyThatAgreesNoSecret(t *testing.T) {
	zero := b64.EncodeToString(make([]byte, 32))
	if ValidBoxKey(zero) || ValidBoxKey("not base64!") || !ValidBoxKey(BoxPublic(NewBoxKey())) {
		t.Fatal("ValidBoxKey")
	}
	if _, err := SealBox([]byte("x"), nil, map[string]string{"a": zero}); err == nil {
		t.Fatal("sealed for a low-order key")
	}
}

func TestEncryptedEventHidesEverything(t *testing.T) {
	k := NewBoxKey()
	e := Event{V: Version, ID: NewID(), Type: Message, From: "dev-0001/aaaaaa", At: time.Now().UTC(),
		To: "dev-0002/bbbbbb", Subject: "the secret subject", Body: "the secret body"}
	boxed, err := EncryptEvent(e, "dev-0001", map[string]string{"dev-0002": BoxPublic(k)})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := Encode(boxed, EventSubject)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"secret", "aaaaaa", "bbbbbb", e.ID, "message"} {
		if strings.Contains(msg, s) {
			t.Fatalf("%q is readable in %s", s, msg)
		}
	}
	// What a daemon from before encryption does with it: decode it, and pass over it as invalid.
	var old Event
	if err := Decode(msg, &old); err != nil {
		t.Fatal(err)
	}
	if old.Clean(); old.Valid() {
		t.Fatalf("an older reader takes it for an event: %+v", old)
	}
	in, err := DecryptEvent(old, "dev-0001", Keyring{"dev-0002": k})
	if err != nil || in.Subject != e.Subject || in.Body != e.Body || in.From != e.From || in.ID != e.ID {
		t.Fatalf("decrypted %+v, %v", in, err)
	}
	// Moved to another device's log, it doesn't open.
	if _, err := DecryptEvent(old, "dev-0003", Keyring{"dev-0002": k}); err == nil {
		t.Fatal("opened on another device's log")
	}
	// A box inside a box is refused.
	inner, _ := EncryptEvent(e, "dev-0001", map[string]string{"dev-0002": BoxPublic(k)})
	outer, _ := EncryptEvent(inner, "dev-0001", map[string]string{"dev-0002": BoxPublic(k)})
	if _, err := DecryptEvent(outer, "dev-0001", Keyring{"dev-0002": k}); err == nil {
		t.Fatal("opened a box in a box")
	}
}

func TestEncryptedPresence(t *testing.T) {
	k, sk := NewBoxKey(), NewSignKey()
	p := Presence{V: Version, Device: "dev-0001", Host: "secret-host", Owner: "Secret Owner", Nick: "secretnick", At: time.Now().UTC(),
		Color: 4, ColorAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), BoxKey: BoxPublic(k), Offline: []string{"cccccc"},
		Sessions: []SessionInfo{{ID: "aaaaaa", Name: "quiet-otter", Agent: "claude", Branch: "secret-branch", Doing: "secret status"},
			{ID: PersonSession, Name: "secretperson", Agent: AgentPerson}}}
	enc, err := EncryptPresence(p, map[string]string{"dev-0001": BoxPublic(k)})
	if err != nil {
		t.Fatal(err)
	}
	ref := "refs/murmur/presence/dev-0001"
	msg, err := EncodeSealed(enc, PresenceSubject, sk, "dev-0001", ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"secret", "Secret", "cccccc", "claude", "color", "2026-11-14"} {
		if strings.Contains(msg, s) {
			t.Fatalf("%q is readable in %s", s, msg)
		}
	}
	// Session names stay readable, with their sessions' IDs, as the name registry has them.
	if enc.Names["quiet-otter"] != "aaaaaa" || len(enc.Names) != 1 {
		t.Fatalf("names beside the box: %v", enc.Names)
	}
	if st, seal := OpenSeal(msg, ref); st != Sealed || seal.By != "dev-0001" || seal.Key != SignPublic(sk) {
		t.Fatalf("seal: %v %+v", st, seal)
	}
	var read Presence
	if err := Decode(msg, &read); err != nil {
		t.Fatal(err)
	}
	// An older reader sees the device, and no sessions.
	if read.Device != "dev-0001" || len(read.Sessions) != 0 || read.Host != "" {
		t.Fatalf("older reader sees %+v", read)
	}
	open, err := DecryptPresence(read, Keyring{"dev-0001": k})
	if err != nil || open.Host != p.Host || open.Owner != p.Owner || open.Nick != p.Nick || len(open.Sessions) != 2 ||
		open.Color != 4 || !open.ColorAt.Equal(p.ColorAt) ||
		open.Sessions[0].Doing != "secret status" || len(open.Offline) != 1 || open.Box != nil {
		t.Fatalf("opened %+v, %v", open, err)
	}
}

func TestSealBindsRefAndBytes(t *testing.T) {
	sk := NewSignKey()
	ref := "refs/murmur/presence/dev-0001"
	msg, err := EncodeSealed(Presence{V: Version, Device: "dev-0001", BoxKey: "x"}, PresenceSubject, sk, "dev-0001", ref)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := OpenSeal(msg, ref); st != Sealed {
		t.Fatalf("own seal: %v", st)
	}
	if st, _ := OpenSeal(msg, "refs/murmur/presence/dev-0002"); st != BadSeal {
		t.Fatalf("on another ref: %v", st)
	}
	for i := strings.Index(msg, "\n\n") + 2; i < len(msg)-1; i++ {
		b := []byte(msg)
		b[i] ^= 1
		if st, _ := OpenSeal(string(b), ref); st == Sealed {
			t.Fatalf("byte %d changed and the seal still holds: %s", i, b)
		}
	}
	plain, _ := Encode(Presence{V: Version, Device: "dev-0001"}, "x")
	if st, _ := OpenSeal(plain, ref); st != Unsealed {
		t.Fatalf("plain record: %v", st)
	}
	// A seal moved onto other content.
	var top map[string]json.RawMessage
	_ = json.Unmarshal([]byte(msg[strings.Index(msg, "\n\n")+2:]), &top)
	forged := "x\n\n{\"v\":1,\"device\":\"dev-0001\",\"boxKey\":\"y\",\"seal\":" + string(top["seal"]) + "}\n"
	if st, _ := OpenSeal(forged, ref); st != BadSeal {
		t.Fatalf("moved seal: %v", st)
	}
}

// A nickname stands in for a device's ID in an address (#190), so one read from the bus is held
// to what Murmur writes: the form of a person's name, and not the device's own ID.
func TestPresenceCleanDropsABadNick(t *testing.T) {
	for nick, want := range map[string]string{"tyler": "tyler", "Tyler": "", "a/b": "", "dev-0001": "", "everyone": ""} {
		p := Presence{Device: "dev-0001", Nick: nick}
		if p.Clean(); p.Nick != want {
			t.Errorf("the nickname %q is read as %q, want %q", nick, p.Nick, want)
		}
	}
}
