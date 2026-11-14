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

	"github.com/a-Fig/murmur/internal/names"
)

// Everything a device writes that carries content is encrypted (#156): on a public repo anyone can
// read the bus, though only collaborators can write it. Events and presence are encrypted for the
// team key (team.go); the team key's private half is itself carried in boxes, one recipient per
// device, each device's X25519 box key published in its sealed presence (see Seal).
//
// A record is encrypted once, with a fresh content key, and the content key is wrapped for each
// recipient: an ephemeral X25519 key agreed with the recipient's public key, through HKDF-SHA256, as
// age does it. AES-256-GCM does both. Which keys can read a record is visible, by ID; what it says
// is not. All of it is Go's standard library.

// Box is an encrypted record: the content key wrapped for each recipient, and the content.
type Box struct {
	Eph   string            `json:"eph"`   // the ephemeral public key, base64
	Keys  map[string]string `json:"keys"`  // recipient (a TeamID, or a device) → its wrapped content key, base64
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

// ErrNotForUs means a box names none of the keys its reader holds: a team key this device hasn't
// been granted yet, or a box for other devices.
var ErrNotForUs = errors.New("not encrypted for this device")

// errBadBox means a box that names this device doesn't open. Nothing in it is trusted.
var errBadBox = errors.New("an encrypted record that doesn't open")

// boxContext names what HKDF derives, so that a key agreed for the bus means nothing anywhere
// else. Like sigContext it names no product.
const boxContext = "agent-bus box v1"

func wrapKey(shared, eph, pub []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, shared, append(slices.Clip(eph), pub...), boxContext, 32)
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
	if _, err := rand.Read(cek); err != nil {
		return nil, err
	}
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
		shared, err := eph.ECDH(pub) // fails for a low-order point, which agrees no secret
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
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
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
	ephRaw, err := b64.DecodeString(b.Eph)
	if err != nil {
		return nil, errBadBox
	}
	eph, err := ecdh.X25519().NewPublicKey(ephRaw)
	if err != nil {
		return nil, errBadBox
	}
	shared, err := k.ECDH(eph)
	if err != nil {
		return nil, errBadBox
	}
	kek, err := wrapKey(shared, ephRaw, k.PublicKey().Bytes())
	if err != nil {
		return nil, errBadBox
	}
	aead, err := gcm(kek)
	if err != nil {
		return nil, errBadBox
	}
	w, err := b64.DecodeString(wrapped)
	if err != nil {
		return nil, errBadBox
	}
	cek, err := aead.Open(nil, make([]byte, aead.NonceSize()), w, nil)
	if err != nil || len(cek) != 32 {
		return nil, errBadBox
	}
	if aead, err = gcm(cek); err != nil {
		return nil, errBadBox
	}
	nonce, err := b64.DecodeString(b.Nonce)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, errBadBox
	}
	data, err := b64.DecodeString(b.Data)
	if err != nil {
		return nil, errBadBox
	}
	plain, err := aead.Open(nil, nonce, data, aad)
	if err != nil {
		return nil, errBadBox
	}
	return plain, nil
}

// Keyring is the private keys a reader holds, by the recipient ID a box names them by: a team key's
// TeamID, or a device ID for a box key.
type Keyring map[string]*ecdh.PrivateKey

// OpenAny opens a box with whichever key of keys it was encrypted for. ErrNotForUs says it names
// none of them; Recipients says which it does name.
func (b *Box) OpenAny(keys Keyring, aad []byte) ([]byte, error) {
	for _, id := range b.Recipients() {
		if k, ok := keys[id]; ok {
			return b.Open(id, k, aad)
		}
	}
	return nil, ErrNotForUs
}

// Recipients is the IDs a box is encrypted for, sorted.
func (b *Box) Recipients() []string {
	ids := make([]string, 0, len(b.Keys))
	for id := range b.Keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// eventAAD binds an encrypted event to the log of the device that wrote it.
func eventAAD(device string) []byte { return []byte("event\x00" + device) }

// EncryptEvent returns e as device writes it on its log, encrypted for recipients (device → box
// key): an event of type Boxed that holds e whole, sender, recipient and type included, so that
// only its writer's log and the commit's time say anything about it.
func EncryptEvent(e Event, device string, recipients map[string]string) (Event, error) {
	plain, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	box, err := SealBox(plain, eventAAD(device), recipients)
	if err != nil {
		return Event{}, err
	}
	return Event{V: e.V, Type: Boxed, Box: box}, nil
}

// DecryptEvent opens a Boxed event read from device's log with whichever of keys it was encrypted
// for. The event inside must not be boxed again; whether it is valid, and its sender device's, is
// the caller's to check, as for any event.
func DecryptEvent(e Event, device string, keys Keyring) (Event, error) {
	if e.Box == nil {
		return Event{}, errBadBox
	}
	plain, err := e.Box.OpenAny(keys, eventAAD(device))
	if err != nil {
		return Event{}, err
	}
	var in Event
	if err := json.Unmarshal(plain, &in); err != nil || in.Type == Boxed || in.Box != nil {
		return Event{}, errBadBox
	}
	return in, nil
}

// presenceSecret is what an encrypted presence hides: all of it but its device, time and box key.
type presenceSecret struct {
	Host     string        `json:"host"`
	Owner    string        `json:"owner,omitempty"`
	Nick     string        `json:"nick,omitempty"`
	Sessions []SessionInfo `json:"sessions"`
	Offline  []string      `json:"offline,omitempty"`
	Groups   bool          `json:"groups,omitempty"`
	Receipts bool          `json:"receipts,omitempty"`
	Epoch    string        `json:"epoch,omitempty"`
	Holds    []string      `json:"holds,omitempty"`
}

func presenceAAD(device string) []byte { return []byte("presence\x00" + device) }

// EncryptPresence returns p with its host, owner, nickname, sessions, offline sessions, epoch and held team
// keys encrypted for recipients. A daemon from before encryption reads it as a device with no sessions.
func EncryptPresence(p Presence, recipients map[string]string) (Presence, error) {
	plain, err := json.Marshal(presenceSecret{Host: p.Host, Owner: p.Owner, Nick: p.Nick, Sessions: p.Sessions, Offline: p.Offline,
		Groups: p.Groups, Receipts: p.Receipts, Epoch: p.Epoch, Holds: p.Holds})
	if err != nil {
		return Presence{}, err
	}
	out := Presence{V: p.V, Device: p.Device, At: p.At, Sessions: []SessionInfo{}, BoxKey: p.BoxKey}
	for _, s := range p.Sessions {
		// Only names the registry holds in the clear anyway: a person's (#242) isn't one.
		if s.ID != PersonSession && names.Valid(s.Name) {
			if out.Names == nil {
				out.Names = map[string]string{}
			}
			out.Names[s.Name] = s.ID
		}
	}
	if out.Box, err = SealBox(plain, presenceAAD(p.Device), recipients); err != nil {
		return Presence{}, err
	}
	return out, nil
}

// DecryptPresence opens an encrypted presence with whichever of keys it was encrypted for.
func DecryptPresence(p Presence, keys Keyring) (Presence, error) {
	plain, err := p.Box.OpenAny(keys, presenceAAD(p.Device))
	if err != nil {
		return Presence{}, err
	}
	var s presenceSecret
	if err := json.Unmarshal(plain, &s); err != nil {
		return Presence{}, errBadBox
	}
	p.Host, p.Owner, p.Nick, p.Sessions, p.Offline, p.Box = s.Host, s.Owner, s.Nick, s.Sessions, s.Offline, nil
	p.Groups, p.Receipts, p.Epoch, p.Holds = s.Groups, s.Receipts, s.Epoch, s.Holds
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
