// Command signrelease signs a release for release.yml, and makes the keys it signs with (#193). It is
// not part of mur; docs/releasing.md says how it is used.
//
//	go run ./internal/update/signrelease -generate FILE  # a new key: its secret to FILE, its public line to stdout
//	go run ./internal/update/signrelease -tag TAG DIR    # sign DIR's files as the release TAG
//
// Signing writes DIR/release.json, the tag, its channel and the SHA-256 of every other file in DIR,
// and DIR/release.json.sig, with the key in RELEASE_SIGNING_KEY (base64 of an ed25519 seed). Without
// that key it signs nothing, which is an error once mur has a release key built in (keys.go): such a
// release would reach no daemon that has one.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/a-Fig/murmur/internal/update"
)

func main() {
	keys, err := update.Keys()
	if err == nil {
		err = run(os.Args[1:], os.Getenv("RELEASE_SIGNING_KEY"), keys, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "signrelease:", err)
		os.Exit(1)
	}
}

func run(args []string, secret string, keys []ed25519.PublicKey, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("signrelease", flag.ContinueOnError)
	fs.SetOutput(stderr)
	generate := fs.String("generate", "", "make a new key, write its secret to this file, and print its public line")
	tag := fs.String("tag", "", "the release's tag, to sign the files in the directory given")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *generate != "" && *tag == "" && fs.NArg() == 0:
		return generateKey(*generate, stdout)
	case *generate == "" && *tag != "" && fs.NArg() == 1:
		return sign(fs.Arg(0), *tag, secret, keys, stderr)
	}
	fs.Usage()
	return errors.New("give -generate FILE, or -tag TAG DIR")
}

// generateKey writes a new key's seed to path, which must not exist yet, so a key is never
// overwritten.
func generateKey(path string, stdout io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(path)
		return err
	}
	_, err = fmt.Fprintln(stdout, update.FormatKey(pub))
	return err
}

func sign(dir, tag, secret string, keys []ed25519.PublicKey, stderr io.Writer) error {
	channel := update.Channel(tag)
	if channel == "" {
		return fmt.Errorf("%q is not a release tag like v1.2.3 or v1.2.3-rc.1", tag)
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		if len(keys) > 0 {
			return errors.New("RELEASE_SIGNING_KEY is not set, but this mur checks release signatures, so an unsigned release would reach none of the daemons it builds")
		}
		fmt.Fprintln(stderr, "RELEASE_SIGNING_KEY is not set, and mur has no release key yet: the release is not signed.")
		return nil
	}
	seed, err := base64.StdEncoding.DecodeString(secret)
	if err != nil || len(seed) != ed25519.SeedSize {
		return errors.New("RELEASE_SIGNING_KEY is not the base64 of a 32-byte ed25519 seed")
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	listed := false
	for _, k := range keys {
		listed = listed || k.Equal(pub)
	}
	if len(keys) > 0 && !listed {
		return fmt.Errorf("RELEASE_SIGNING_KEY's public half, %s, is not in internal/update/keys.go, so this release's own mur would refuse releases it signs", update.FormatKey(pub))
	}

	m := update.Manifest{Tag: tag, Channel: channel, Assets: map[string]update.Asset{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || e.Name() == update.ManifestName || e.Name() == update.SignatureName {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		m.Assets[e.Name()] = update.Asset{SHA256: hex.EncodeToString(sum[:])}
	}
	if len(m.Assets) == 0 {
		return fmt.Errorf("%s has no files to sign", dir)
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	manifest = append(manifest, '\n')
	sig := update.Sign(key, manifest)
	// What the updater will check, checked by its own code before anything is published.
	if err = update.Verify([]ed25519.PublicKey{pub}, manifest, sig); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, update.ManifestName), manifest, 0o644); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, update.SignatureName), sig, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Signed %s (%s, %d files) with %s:\n%s", tag, channel, len(m.Assets), update.FormatKey(pub), manifest)
	return nil
}
