package bus

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// A repo can require that what its devices write to the bus be readable by its devices only (#156):
// on a public repo anyone can read the bus, though only collaborators can write it. Each device then
// encrypts its records to the devices they are for, with the X25519 key each device publishes in
// its presence (a box key). A presence is sealed (see Seal), so a box key is as much the device's as
// its signing key is.
//
// A record is encrypted once, with a fresh content key, and the content key is wrapped for each
// recipient: an ephemeral X25519 key agreed with the recipient's box key, through HKDF-SHA256, as
// age does it. AES-256-GCM does both. Which devices can read a record is visible, by ID; what it
// says is not.

// Box is an encrypted record: the content key wrapped for each recipient device, and the content.
type Box struct {
	Eph   string            `json:"eph"`   // the ephemeral public key, base64
	Keys  map[string]string `json:"keys"`  // recipient device → its wrapped content key, base64
	Nonce string            `json:"nonce"` // base64
	Data  string            `json:"data"`  // base64
}

var b64 = base64.StdEncoding

// NewBoxKey makes a device's box key.
func NewBoxKey() *ecdh.PrivateKey {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}

// BoxPublic is a box key's public half as presence carries it.
func BoxPublic(k *ecdh.PrivateKey) string { return b64.EncodeToString(k.PublicKey().Bytes()) }

// ErrNotForUs means a box has no key for the device opening it.
var ErrNotForUs = errors.New("not encrypted for this device")

func wrapKey(shared, eph, pub []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, append(slices.Clip(eph), pub...), "agent-bus box v1", 32)
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealBox encrypts plain for each recipient, device → its box key (base64), binding it to aad: a box
// opened with other aad fails, so it can't be moved to another record.
func SealBox(plain, aad []byte, recipients map[string]string) (*Box, error) {
	if len(recipients) == 0 {
		return nil, errors.New("no recipients")
	}
	eph := NewBoxKey()
	cek := make([]byte, 32)
	rand.Read(cek)
	box := &Box{Eph: BoxPublic(eph), Keys: map[string]string{}}
	for dev, pubB64 := range recipients {
		raw, err := b64.DecodeString(pubB64)
		if err != nil {
			return nil, err
		}
		pub, err := ecdh.X25519().NewPublicKey(raw)
		if err != nil {
			return nil, err
		}
		shared, err := eph.ECDH(pub)
		if err != nil {
			return nil, err
		}
		kek, err := wrapKey(shared, eph.PublicKey().Bytes(), raw)
		if err != nil {
			return nil, err
		}
		aead, err := gcm(kek)
		if err != nil {
			return nil, err
		}
		// Each wrapping key is used once, for one content key, so a zero nonce is safe.
		box.Keys[dev] = b64.EncodeToString(aead.Seal(nil, make([]byte, aead.NonceSize()), cek, nil))
	}
	aead, err := gcm(cek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	rand.Read(nonce)
	box.Nonce = b64.EncodeToString(nonce)
	box.Data = b64.EncodeToString(aead.Seal(nil, nonce, plain, aad))
	return box, nil
}

// Open decrypts a box as device dev, with its box key.
func (b *Box) Open(dev string, k *ecdh.PrivateKey, aad []byte) ([]byte, error) {
	wrapped, ok := b.Keys[dev]
	if !ok {
		return nil, ErrNotForUs
	}
	bad := errors.New("box doesn't open")
	ephRaw, err := b64.DecodeString(b.Eph)
	if err != nil {
		return nil, bad
	}
	eph, err := ecdh.X25519().NewPublicKey(ephRaw)
	if err != nil {
		return nil, bad
	}
	shared, err := k.ECDH(eph)
	if err != nil {
		return nil, bad
	}
	kek, err := wrapKey(shared, ephRaw, k.PublicKey().Bytes())
	if err != nil {
		return nil, bad
	}
	aead, err := gcm(kek)
	if err != nil {
		return nil, bad
	}
	w, err := b64.DecodeString(wrapped)
	if err != nil {
		return nil, bad
	}
	cek, err := aead.Open(nil, make([]byte, aead.NonceSize()), w, nil)
	if err != nil {
		return nil, bad
	}
	if aead, err = gcm(cek); err != nil {
		return nil, bad
	}
	nonce, err := b64.DecodeString(b.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, bad
	}
	data, err := b64.DecodeString(b.Data)
	if err != nil {
		return nil, bad
	}
	plain, err := aead.Open(nil, nonce, data, aad)
	if err != nil {
		return nil, bad
	}
	return plain, nil
}

// Config is the record on ConfigRef: the repo's settings for the bus.
type Config struct {
	V         int       `json:"v"`
	Encrypted bool      `json:"encrypted"`
	At        time.Time `json:"at"`
}

func eventAAD(e Event) []byte { return []byte("event\x00" + e.ID + "\x00" + e.From) }

// EncryptEvent returns e encrypted for recipients (device → box key): an Encrypted event with
// e's ID, sender and time, which holds e whole.
func EncryptEvent(e Event, recipients map[string]string) (Event, error) {
	plain, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	out := Event{V: e.V, ID: e.ID, Type: Encrypted, From: e.From, At: e.At}
	if out.Box, err = SealBox(plain, eventAAD(out), recipients); err != nil {
		return Event{}, err
	}
	return out, nil
}

// DecryptEvent opens an Encrypted event as device dev. The event inside must be the outer one's:
// the same ID and sender, and not itself encrypted.
func DecryptEvent(e Event, dev string, k *ecdh.PrivateKey) (Event, error) {
	plain, err := e.Box.Open(dev, k, eventAAD(e))
	if err != nil {
		return Event{}, err
	}
	var in Event
	if err := json.Unmarshal(plain, &in); err != nil {
		return Event{}, err
	}
	if in.ID != e.ID || in.From != e.From || in.Type == Encrypted || in.Box != nil {
		return Event{}, errors.New("an encrypted event that isn't its own")
	}
	return in, nil
}

// presenceSecret is what an encrypted presence hides.
type presenceSecret struct {
	Host     string        `json:"host"`
	Owner    string        `json:"owner,omitempty"`
	Sessions []SessionInfo `json:"sessions"`
}

func presenceAAD(device string) []byte { return []byte("presence\x00" + device) }

// EncryptPresence returns p with its host, owner and sessions encrypted for recipients.
func EncryptPresence(p Presence, recipients map[string]string) (Presence, error) {
	plain, err := json.Marshal(presenceSecret{Host: p.Host, Owner: p.Owner, Sessions: p.Sessions})
	if err != nil {
		return Presence{}, err
	}
	out := Presence{V: p.V, Device: p.Device, At: p.At, Sessions: []SessionInfo{}, BoxKey: p.BoxKey}
	if out.Enc, err = SealBox(plain, presenceAAD(p.Device), recipients); err != nil {
		return Presence{}, err
	}
	return out, nil
}

// DecryptPresence opens an encrypted presence as device dev.
func DecryptPresence(p Presence, dev string, k *ecdh.PrivateKey) (Presence, error) {
	plain, err := p.Enc.Open(dev, k, presenceAAD(p.Device))
	if err != nil {
		return Presence{}, err
	}
	var s presenceSecret
	if err := json.Unmarshal(plain, &s); err != nil {
		return Presence{}, err
	}
	p.Host, p.Owner, p.Sessions, p.Enc = s.Host, s.Owner, s.Sessions, nil
	return p, nil
}

// ValidBoxKey says whether s is a box key a box can be made for: an X25519 public key, in base64,
// that agrees a secret with another key. A low-order point, such as all zeros, agrees none.
func ValidBoxKey(s string) bool {
	raw, err := b64.DecodeString(s)
	if err != nil {
		return false
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return false
	}
	_, err = NewBoxKey().ECDH(pub)
	return err == nil
}
