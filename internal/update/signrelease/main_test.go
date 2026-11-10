package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/update"
)

// A key made with -generate signs a release whose release.json and signature the updater accepts.
func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "key")
	var out bytes.Buffer
	if err := run([]string{"-generate", secretPath}, "", nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("the secret is readable by others: %v", st.Mode())
	}
	pub, err := update.ParseKey(out.String())
	if err != nil {
		t.Fatalf("printed %q: %v", &out, err)
	}
	if err := run([]string{"-generate", secretPath}, "", nil, &out, &out); err == nil {
		t.Fatal("overwrote a key")
	}
	secret, _ := os.ReadFile(secretPath)
	keys := []ed25519.PublicKey{pub}

	dist := t.TempDir()
	files := map[string]string{"mur-linux-amd64": "linux", "mur-windows-arm64.exe": "windows", "SHA256SUMS": "sums"}
	for name, body := range files {
		os.WriteFile(filepath.Join(dist, name), []byte(body), 0o644)
	}
	os.Mkdir(filepath.Join(dist, "sub"), 0o755)
	for _, tag := range []string{"v1.2.4-nightly.20261109", "v1.2.4"} { // a second run replaces the first
		if err := run([]string{"-tag", tag, dist}, string(secret), keys, &out, &out); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _ := os.ReadFile(filepath.Join(dist, update.ManifestName))
	sig, _ := os.ReadFile(filepath.Join(dist, update.SignatureName))
	if err := update.Verify(keys, manifest, sig); err != nil {
		t.Fatal(err)
	}
	var m update.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	if m.Tag != "v1.2.4" || m.Channel != "stable" || len(m.Assets) != len(files) {
		t.Fatalf("manifest %s", manifest)
	}
	for name, body := range files {
		sum := sha256.Sum256([]byte(body))
		if m.Assets[name].SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: %q", name, m.Assets[name].SHA256)
		}
	}
}

func TestRefusals(t *testing.T) {
	key := func() (ed25519.PublicKey, string) {
		dir := t.TempDir()
		var out bytes.Buffer
		if err := run([]string{"-generate", filepath.Join(dir, "key")}, "", nil, &out, &out); err != nil {
			t.Fatal(err)
		}
		pub, _ := update.ParseKey(out.String())
		secret, _ := os.ReadFile(filepath.Join(dir, "key"))
		return pub, string(secret)
	}
	pub, secret := key()
	otherPub, _ := key()
	for _, c := range []struct {
		name   string
		args   []string
		secret string
		keys   []ed25519.PublicKey
		why    string // in the error; "" for no error and nothing signed
	}{
		{"no secret, no key in mur", []string{"-tag", "v1.2.4"}, "", nil, ""},
		{"no secret, a key in mur", []string{"-tag", "v1.2.4"}, "", []ed25519.PublicKey{pub}, "not set"},
		{"a key mur doesn't list", []string{"-tag", "v1.2.4"}, secret, []ed25519.PublicKey{otherPub}, "is not in internal/update/keys.go"},
		{"a malformed secret", []string{"-tag", "v1.2.4"}, "c2hvcnQ=", nil, "32-byte"},
		{"a tag that isn't a version", []string{"-tag", "latest"}, secret, nil, "not a release tag"},
		{"no directory", []string{"-tag", "v1.2.4"}, secret, nil, "give -generate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dist := t.TempDir()
			os.WriteFile(filepath.Join(dist, "mur-linux-amd64"), []byte("linux"), 0o644)
			args := c.args
			if c.name != "no directory" {
				args = append(args, dist)
			}
			var out bytes.Buffer
			err := run(args, c.secret, c.keys, &out, &out)
			if c.why == "" && err != nil || c.why != "" && (err == nil || !strings.Contains(err.Error(), c.why)) {
				t.Fatalf("err %v, want %q", err, c.why)
			}
			if _, err := os.Stat(filepath.Join(dist, update.SignatureName)); err == nil {
				t.Fatal("signed")
			}
		})
	}
}
