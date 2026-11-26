package bus

import (
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// The team key (#156, Tyler's decision of 2026-11-10). Every event and presence is encrypted for one
// X25519 key the whole team shares, so every device that can push, and has published its box key,
// reads everything ever sent: a device that was away reads what it missed, and one that joins later
// reads the history. The key is versioned. An epoch record on the team ref publishes a team public
// key and wraps its private half for devices; a grant record wraps every team private key there has
// been for one device. A new epoch (a rotation) is how a collaborator who has lost push access stops
// reading what is new: the epoch is granted only to devices that publish a presence after it, which
// takes push access. docs/design/encryption.md has the reasons and the costs.

// Team record types.
const (
	TeamEpoch  = "epoch"
	TeamGrant  = "grant"
	TeamRoster = "roster"
	TeamPolicy = "policy"
)

// TeamRecord is one record on the team ref, sealed by the device that wrote it.
type TeamRecord struct {
	V    int    `json:"v"`
	Type string `json:"type"`
	// ID is an epoch's team key, as TeamID names it, or the one a roster is for; Pub is an epoch's
	// public half, base64.
	ID  string `json:"id,omitempty"`
	Pub string `json:"pub,omitempty"`
	// To is the device a grant is for, and IDs the team keys it carries, readable so that every
	// device knows what has been granted without opening it.
	To  string   `json:"for,omitempty"`
	IDs []string `json:"ids,omitempty"`
	// Box holds the private halves (teamSecret), wrapped for each device it is for: an epoch's for
	// its writer, a grant's for To. A roster's holds the collaborators, encrypted for team key ID.
	Box *Box      `json:"box,omitempty"`
	At  time.Time `json:"at"`
	// AdminSeq orders the owner's epochs, rosters and policy updates together. The initial
	// epoch carries the initial policy and starts at 1. Grants carry no admin sequence.
	AdminSeq uint64      `json:"adminSeq,omitempty"`
	Policy   *RepoPolicy `json:"policy,omitempty"`
}

// RepoPolicy is the owner's repo-wide settings. Team encryption and file transfers are the
// original defaults. Readers support additional values as they implement those behaviors.
type RepoPolicy struct {
	EncryptionMode string `json:"encryptionMode"`
	AllowFiles     bool   `json:"allowFiles"`
}

func (p RepoPolicy) supported() bool { return p.EncryptionMode == "team" && p.AllowFiles }

// teamSecret is what a team record's box holds.
type teamSecret struct {
	Keys map[string]string `json:"keys,omitempty"` // TeamID → private key, base64
	// Collaborators is, in a roster, the people who could push to the repo when it was written:
	// what `mur rotate` compares against.
	Collaborators []string `json:"collaborators,omitempty"`
}

// teamPrefix opens every TeamID. No device ID can hold a colon, so a box's recipients never mix one
// up with a device.
const teamPrefix = "team:"

// TeamID names a team key by its public half: a short hash, which is all a box needs to say which
// key it was encrypted for.
func TeamID(pub string) string {
	raw, err := b64.DecodeString(pub)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return teamPrefix + hex.EncodeToString(sum[:8])
}

// IsTeamID says whether id names a team key rather than a device.
func IsTeamID(id string) bool {
	return strings.HasPrefix(id, teamPrefix) && len(id) == len(teamPrefix)+16
}

func teamAAD(r TeamRecord) []byte {
	return []byte("team\x00" + r.Type + "\x00" + r.ID + "\x00" + r.To + "\x00" + strings.Join(r.IDs, ","))
}

// NewEpoch makes an epoch record for a new team key k, its private half wrapped for recipients
// (device → box key).
func NewEpoch(k *ecdh.PrivateKey, recipients map[string]string, at time.Time) (TeamRecord, error) {
	pub := BoxPublic(k)
	id := TeamID(pub)
	r := TeamRecord{V: Version, Type: TeamEpoch, ID: id, Pub: pub, At: at.UTC()}
	plain, err := json.Marshal(teamSecret{Keys: map[string]string{id: b64.EncodeToString(k.Bytes())}})
	if err != nil {
		return TeamRecord{}, err
	}
	if r.Box, err = SealBox(plain, teamAAD(r), recipients); err != nil {
		return TeamRecord{}, err
	}
	return r, nil
}

// NewGrant makes a grant of every key in keys for device to, whose box key is boxKey.
func NewGrant(to, boxKey string, keys Keyring, at time.Time) (TeamRecord, error) {
	r := TeamRecord{V: Version, Type: TeamGrant, To: to, At: at.UTC()}
	secret := teamSecret{Keys: map[string]string{}}
	for id, k := range keys {
		if !IsTeamID(id) {
			continue
		}
		secret.Keys[id] = b64.EncodeToString(k.Bytes())
		r.IDs = append(r.IDs, id)
	}
	if len(r.IDs) == 0 {
		return TeamRecord{}, errors.New("no team keys to grant")
	}
	slices.Sort(r.IDs)
	plain, err := json.Marshal(secret)
	if err != nil {
		return TeamRecord{}, err
	}
	if r.Box, err = SealBox(plain, teamAAD(r), map[string]string{to: boxKey}); err != nil {
		return TeamRecord{}, err
	}
	return r, nil
}

// NewRoster makes a roster record: the repo's collaborators, encrypted for the team key id, whose
// public half is pub, so that every device granted that key reads it, whichever device wrote it. An
// epoch can't carry the list itself, as its box is wrapped for its writer's device alone.
func NewRoster(id, pub string, collaborators []string, at time.Time) (TeamRecord, error) {
	r := TeamRecord{V: Version, Type: TeamRoster, ID: id, At: at.UTC()}
	plain, err := json.Marshal(teamSecret{Collaborators: collaborators})
	if err != nil {
		return TeamRecord{}, err
	}
	if r.Box, err = SealBox(plain, teamAAD(r), map[string]string{id: pub}); err != nil {
		return TeamRecord{}, err
	}
	return r, nil
}

// OpenRoster opens a roster with the team keys in keys, and returns the collaborators it lists.
func (r TeamRecord) OpenRoster(keys Keyring) ([]string, error) {
	k := keys[r.ID]
	if r.Type != TeamRoster || r.Box == nil || k == nil {
		return nil, ErrNotForUs
	}
	plain, err := r.Box.Open(r.ID, k, teamAAD(r))
	if err != nil {
		return nil, err
	}
	var s teamSecret
	if json.Unmarshal(plain, &s) != nil {
		return nil, errBadBox
	}
	return s.Collaborators, nil
}

// Valid says whether a team record has the form its type needs. A record that doesn't is junk, and
// is passed over.
func (r TeamRecord) Valid() bool {
	if r.V != Version || r.Policy != nil && !r.Policy.supported() {
		return false
	}
	if r.Type == TeamPolicy {
		return r.AdminSeq > 1 && r.Policy != nil && r.Box == nil && r.ID == "" && r.Pub == "" && r.To == "" && len(r.IDs) == 0
	}
	if r.Box == nil || len(r.Box.Keys) == 0 || r.Policy != nil && (r.Type != TeamEpoch || r.AdminSeq != 1) {
		return false
	}
	switch r.Type {
	case TeamEpoch:
		return r.ID != "" && r.ID == TeamID(r.Pub) && ValidBoxKey(r.Pub) && r.To == "" && len(r.IDs) == 0
	case TeamGrant:
		if r.AdminSeq != 0 || !ValidDevice(r.To) || len(r.IDs) == 0 || len(r.Box.Keys) != 1 || r.Box.Keys[r.To] == "" {
			return false
		}
		for _, id := range r.IDs {
			if !IsTeamID(id) {
				return false
			}
		}
		return r.ID == "" && r.Pub == ""
	case TeamRoster:
		return IsTeamID(r.ID) && r.Pub == "" && r.To == "" && len(r.IDs) == 0 && len(r.Box.Keys) == 1 && r.Box.Keys[r.ID] != ""
	}
	return false
}

// Subject is the commit subject a team record is written with. Only what the record shows anyway.
func (r TeamRecord) Subject() string {
	switch r.Type {
	case TeamPolicy:
		return "repo policy"
	case TeamGrant:
		return "grant to " + r.To
	case TeamRoster:
		return "roster for " + r.ID
	}
	return "epoch " + r.ID
}

// Open opens a team record as device dev, with its box key k, and returns the team keys it carries,
// each checked against the ID it is given under: a key that doesn't match its ID is dropped, so a
// grant can't hand a device a key under another key's name. With an epoch, the epoch's own key must
// be among them. ErrNotForUs says the record isn't for dev.
func (r TeamRecord) Open(dev string, k *ecdh.PrivateKey) (Keyring, error) {
	plain, err := r.Box.Open(dev, k, teamAAD(r))
	if err != nil {
		return nil, err
	}
	var s teamSecret
	if json.Unmarshal(plain, &s) != nil {
		return nil, errBadBox
	}
	keys := Keyring{}
	for id, enc := range s.Keys {
		raw, err := b64.DecodeString(enc)
		if err != nil {
			continue
		}
		priv, err := ecdh.X25519().NewPrivateKey(raw)
		if err != nil || TeamID(BoxPublic(priv)) != id {
			continue
		}
		keys[id] = priv
	}
	if r.Type == TeamEpoch && keys[r.ID] == nil {
		return nil, errBadBox
	}
	return keys, nil
}
