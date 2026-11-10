package update

// Signed releases (#193). release.yml writes release.json, which names the release's tag, its
// channel and every published file's SHA-256, and signs it with an ed25519 key whose public half is
// in keys.go. A binary with a key there installs only a release whose release.json.sig verifies
// against one of them, whose release.json names the tag GitHub served it under, whose channel this
// daemon takes, and whose version is newer than the running one. So an old signed binary can't come
// back under a new tag, a nightly can't pass for a stable release by having GitHub's prerelease
// flag cleared, and a release uploaded by someone without the key installs nowhere.
//
// The signature is an SSH signature (OpenSSH's PROTOCOL.sshsig, hash sha512), so anyone can check
// one with `ssh-keygen -Y verify` from OpenSSH 8.1 or later (docs/releasing.md).

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

const (
	// ManifestName and SignatureName are the release assets that carry the signature.
	ManifestName  = "release.json"
	SignatureName = ManifestName + ".sig"
	// Namespace is the SSH signature namespace (`ssh-keygen -n`). A signature made for anything
	// else, even with the same key, doesn't verify as a release. Changing it would make every
	// binary with a key refuse the releases after the change.
	Namespace = brand.Name + "-release"

	sigMagic             = "SSHSIG"
	sigHash              = "sha512"
	keyType              = "ssh-ed25519"
	armorBegin, armorEnd = "-----BEGIN SSH SIGNATURE-----", "-----END SSH SIGNATURE-----"
)

// Manifest is release.json.
type Manifest struct {
	Tag     string           `json:"tag"`     // the release's tag, v included
	Channel string           `json:"channel"` // Channel(Tag)
	Assets  map[string]Asset `json:"assets"`  // by file name
}

type Asset struct {
	SHA256 string `json:"sha256"` // lowercase hex
}

// Channel is the channel a release tag belongs to: "stable" for vX.Y.Z, "prerelease" for one with a
// suffix (an rc or a nightly), and "" for a tag that isn't a release version.
func Channel(tag string) string {
	_, pre, ok := version(tag)
	switch {
	case !ok:
		return ""
	case pre == "":
		return "stable"
	}
	return "prerelease"
}

// Keys parses the keys built into this binary (keys.go). A line that doesn't parse is an error, not
// a key skipped: dropping it could leave the list empty, and an empty list checks no signature.
func Keys() ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, line := range releaseKeys {
		k, err := ParseKey(line)
		if err != nil {
			return nil, fmt.Errorf("release key %q: %w", line, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ParseKey reads an `ssh-ed25519 AAAA... [comment]` line.
func ParseKey(line string) (ed25519.PublicKey, error) {
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != keyType {
		return nil, errors.New("not an ssh-ed25519 key")
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil {
		return nil, err
	}
	return parseKeyBlob(blob)
}

// FormatKey writes key as ParseKey reads it, and as ssh-keygen and allowed_signers files do.
func FormatKey(key ed25519.PublicKey) string {
	return keyType + " " + base64.StdEncoding.EncodeToString(keyBlob(key))
}

// Sign signs message as `ssh-keygen -Y sign -n murmur-release` would, and returns the armored
// signature.
func Sign(key ed25519.PrivateKey, message []byte) []byte { return sign(key, Namespace, message) }

func sign(key ed25519.PrivateKey, namespace string, message []byte) []byte {
	pub := key.Public().(ed25519.PublicKey)
	sig := ed25519.Sign(key, signedData(namespace, "", message))
	blob := binary.BigEndian.AppendUint32([]byte(sigMagic), 1)
	blob = putString(blob, keyBlob(pub))
	blob = putString(blob, []byte(namespace))
	blob = putString(blob, nil)
	blob = putString(blob, []byte(sigHash))
	blob = putString(blob, putString(putString(nil, []byte(keyType)), sig))
	enc := base64.StdEncoding.EncodeToString(blob)
	var out strings.Builder
	out.WriteString(armorBegin + "\n")
	for len(enc) > 70 {
		out.WriteString(enc[:70] + "\n")
		enc = enc[70:]
	}
	out.WriteString(enc + "\n" + armorEnd + "\n")
	return []byte(out.String())
}

// Verify says whether armored is a release signature of message by one of keys.
func Verify(keys []ed25519.PublicKey, message, armored []byte) error {
	text := strings.TrimSpace(string(armored))
	// The length check comes first: the two lines share their dashes, so text can start with one
	// and end with the other while being shorter than both.
	if len(text) < len(armorBegin)+len(armorEnd) || !strings.HasPrefix(text, armorBegin) || !strings.HasSuffix(text, armorEnd) {
		return errors.New("the signature is not an SSH signature")
	}
	blob, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text[len(armorBegin):len(text)-len(armorEnd)]), ""))
	if err != nil {
		return fmt.Errorf("the signature is not an SSH signature: %w", err)
	}
	head := binary.BigEndian.AppendUint32([]byte(sigMagic), 1)
	if !bytes.HasPrefix(blob, head) {
		return errors.New("the signature is not a version 1 SSH signature")
	}
	var fields [5][]byte // public key, namespace, reserved, hash, signature
	rest, ok := blob[len(head):], true
	for i := range fields {
		if fields[i], rest, ok = readString(rest); !ok {
			return errors.New("the signature is malformed")
		}
	}
	if len(rest) != 0 {
		return errors.New("the signature is malformed")
	}
	if string(fields[1]) != Namespace {
		return fmt.Errorf("the signature is for %q, not %q", fields[1], Namespace)
	}
	if string(fields[3]) != sigHash {
		return fmt.Errorf("the signature hashes with %q, not %s", fields[3], sigHash)
	}
	pub, err := parseKeyBlob(fields[0])
	if err != nil {
		return err
	}
	trusted := false
	for _, k := range keys {
		trusted = trusted || k.Equal(pub)
	}
	if !trusted {
		return fmt.Errorf("the signing key %s is not one this mur trusts", FormatKey(pub))
	}
	typ, rest, ok1 := readString(fields[4])
	sig, rest, ok2 := readString(rest)
	if !ok1 || !ok2 || len(rest) != 0 || string(typ) != keyType || len(sig) != ed25519.SignatureSize {
		return errors.New("the signature is malformed")
	}
	if !ed25519.Verify(pub, signedData(string(fields[1]), string(fields[2]), message), sig) {
		return errors.New("the signature does not verify")
	}
	return nil
}

// checkRelease checks a release's release.json and its signature as the release tag, served to a
// daemon running version running on the given channel, and returns the SHA-256 it lists for name.
func checkRelease(keys []ed25519.PublicKey, manifest, sig []byte, tag, running, name string, prerelease bool) (string, error) {
	if err := Verify(keys, manifest, sig); err != nil {
		return "", err
	}
	var m Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		return "", fmt.Errorf("%s: %w", ManifestName, err)
	}
	if m.Tag != tag {
		return "", fmt.Errorf("%s is signed for %q, not this release's tag", ManifestName, m.Tag)
	}
	if m.Channel == "" || m.Channel != Channel(m.Tag) {
		return "", fmt.Errorf("%s's channel %q does not match its tag", ManifestName, m.Channel)
	}
	if m.Channel != "stable" && !prerelease {
		return "", errors.New("it is signed as a prerelease, and this daemon takes stable releases")
	}
	if !newer(m.Tag, running) {
		return "", fmt.Errorf("it is signed as %s, which is not newer than %s", m.Tag, running)
	}
	a := m.Assets[name]
	if len(a.SHA256) != 64 {
		return "", fmt.Errorf("%s lists no %s", ManifestName, name)
	}
	return strings.ToLower(a.SHA256), nil
}

// signedData is what an SSH signature signs: the namespace and a hash of the message, never the
// message itself.
func signedData(namespace, reserved string, message []byte) []byte {
	h := sha512.Sum512(message)
	b := putString([]byte(sigMagic), []byte(namespace))
	b = putString(b, []byte(reserved))
	b = putString(b, []byte(sigHash))
	return putString(b, h[:])
}

func keyBlob(key ed25519.PublicKey) []byte {
	return putString(putString(nil, []byte(keyType)), key)
}

func parseKeyBlob(blob []byte) (ed25519.PublicKey, error) {
	typ, rest, ok1 := readString(blob)
	key, rest, ok2 := readString(rest)
	if !ok1 || !ok2 || len(rest) != 0 || string(typ) != keyType || len(key) != ed25519.PublicKeySize {
		return nil, errors.New("not an ssh-ed25519 key")
	}
	return ed25519.PublicKey(key), nil
}

// putString and readString are SSH's string: a 32-bit big-endian length, then the bytes.
func putString(b, s []byte) []byte {
	return append(binary.BigEndian.AppendUint32(b, uint32(len(s))), s...)
}

func readString(b []byte) (s, rest []byte, ok bool) {
	if len(b) < 4 {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(b)
	if uint64(len(b)-4) < uint64(n) {
		return nil, nil, false
	}
	return b[4 : 4+n], b[4+n:], true
}
