package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func public(key ed25519.PrivateKey) ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }

func TestShippedKeysParse(t *testing.T) {
	for _, line := range shipped {
		if _, err := ParseKey(line); err != nil {
			t.Errorf("keys.go: %q: %v", line, err)
		}
	}
}

// A key line that doesn't parse stops the updater, rather than leaving it checking no signature.
func TestMalformedKeyDisablesUpdates(t *testing.T) {
	releaseKeys = []string{"ssh-ed25519 not-base64"}
	defer func() { releaseKeys = nil }()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked for %s with a malformed key", r.URL)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(path, []byte("old"), 0755)
	var logs bytes.Buffer
	Run(context.Background(), Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(),
		Getenv: func(string) string { return "" }, Log: log.New(&logs, "", 0)}, func() { t.Error("restarted") })
	if !strings.Contains(logs.String(), "self-update disabled: release key") {
		t.Fatalf("log: %s", &logs)
	}
}

func TestKeyLines(t *testing.T) {
	key := public(testKey(t))
	line := FormatKey(key)
	for _, l := range []string{line, line + " a comment", "  " + line + "\n"} {
		got, err := ParseKey(l)
		if err != nil || !got.Equal(key) {
			t.Errorf("ParseKey(%q) = %x, %v", l, got, err)
		}
	}
	for _, l := range []string{"", "ssh-ed25519", "ssh-rsa " + strings.Fields(line)[1], "ssh-ed25519 !!!", "ssh-ed25519 " + strings.Fields(line)[1][:20]} {
		if _, err := ParseKey(l); err == nil {
			t.Errorf("ParseKey(%q) accepted", l)
		}
	}
}

func TestSignatures(t *testing.T) {
	key, other := testKey(t), testKey(t)
	keys := []ed25519.PublicKey{public(other), public(key)}
	msg := []byte(`{"tag":"v1.2.4"}`)
	sig := Sign(key, msg)
	if err := Verify(keys, msg, sig); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		keys     []ed25519.PublicKey
		msg, sig []byte
	}{
		"another key":       {[]ed25519.PublicKey{public(other)}, msg, sig},
		"no keys":           {nil, msg, sig},
		"a changed message": {keys, []byte(`{"tag":"v1.2.5"}`), sig},
		"not armored":       {keys, msg, []byte("hello")},
		"bad base64":        {keys, msg, []byte(armorBegin + "\n!!!\n" + armorEnd)},
		"empty":             {keys, msg, []byte(armorBegin + "\n" + armorEnd)},
		"overlapping lines": {keys, msg, []byte("-----BEGIN SSH SIGNATURE-----END SSH SIGNATURE-----")},
		// The same key's signature of the same bytes, made for something other than a release.
		"another namespace": {keys, msg, sign(key, "file", msg)},
		"trailing bytes":    {keys, msg, rearmor(t, sig, func(b []byte) []byte { return append(b, 0) })},
		"truncated":         {keys, msg, rearmor(t, sig, func(b []byte) []byte { return b[:len(b)-1] })},
		"a flipped bit":     {keys, msg, rearmor(t, sig, func(b []byte) []byte { b[len(b)-1] ^= 1; return b })},
	} {
		if err := Verify(c.keys, c.msg, c.sig); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// rearmor edits a signature's blob and armors it again.
func rearmor(t *testing.T, sig []byte, edit func([]byte) []byte) []byte {
	t.Helper()
	text := strings.TrimSpace(string(sig))
	blob, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(text[len(armorBegin):len(text)-len(armorEnd)]), ""))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(armorBegin + "\n" + base64.StdEncoding.EncodeToString(edit(blob)) + "\n" + armorEnd + "\n")
}

// The format is OpenSSH's, so `ssh-keygen -Y verify` checks a release by hand (docs/releasing.md),
// and a signature ssh-keygen makes verifies here.
func TestSSHKeygen(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows' ssh-keygen wants private key files ACLed to their owner")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	msg := filepath.Join(dir, ManifestName)
	os.WriteFile(msg, []byte(`{"tag":"v1.2.4"}`+"\n"), 0o600)
	run := func(stdin string, args ...string) {
		t.Helper()
		cmd := exec.Command("ssh-keygen", args...)
		if stdin != "" {
			f, err := os.Open(stdin)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			cmd.Stdin = f
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	key := testKey(t)
	b, _ := os.ReadFile(msg)
	os.WriteFile(msg+".sig", Sign(key, b), 0o600)
	allowed := filepath.Join(dir, "allowed_signers")
	os.WriteFile(allowed, []byte(fmt.Sprintf("murmur namespaces=%q %s\n", Namespace, FormatKey(public(key)))), 0o600)
	run(msg, "-Y", "verify", "-f", allowed, "-I", "murmur", "-n", Namespace, "-s", msg+".sig")

	os.Remove(msg + ".sig")
	run("", "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", filepath.Join(dir, "key"))
	run("", "-Y", "sign", "-q", "-f", filepath.Join(dir, "key"), "-n", Namespace, msg)
	line, _ := os.ReadFile(filepath.Join(dir, "key.pub"))
	pub, err := ParseKey(string(line))
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := os.ReadFile(msg + ".sig")
	if err := Verify([]ed25519.PublicKey{pub}, b, sig); err != nil {
		t.Fatalf("ssh-keygen's signature: %v\n%s", err, sig)
	}
}

// What the updater refuses once it has a key, and that without one it does what it always did.
func TestSignedReleases(t *testing.T) {
	// The daemon trusts key and spare (a list, as during a rotation); other is anyone else's.
	key, spare, other := testKey(t), testKey(t), testKey(t)
	const stable, nightly = "v1.2.4", "v1.2.5-nightly.20261109"
	sum := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	for _, c := range []struct {
		name, tag  string
		prerelease bool // the daemon's channel
		unsigned   bool // the daemon has no key
		edit       func(*Manifest)
		serve      func(manifest, sig []byte) ([]byte, []byte) // what the release publishes; nil is no asset
		want       string                                      // the version installed; "" none; "refused"; "error"
		why        string                                      // in the error, for "refused" and "error"
	}{
		{name: "a valid signature installs", tag: stable, want: "1.2.4"},
		{name: "a signed prerelease on the prerelease channel", tag: nightly, prerelease: true, want: "1.2.5-nightly.20261109"},
		{name: "a stable release on the prerelease channel", tag: stable, prerelease: true, want: "1.2.4"},
		{name: "signed by another key", tag: stable, want: "refused", why: "not one this mur trusts",
			serve: func(m, _ []byte) ([]byte, []byte) { return m, Sign(other, m) }},
		{name: "release.json changed after signing", tag: stable, want: "refused", why: "does not verify",
			serve: func(m, s []byte) ([]byte, []byte) {
				return bytes.Replace(m, []byte(sum("binary v1.2.4")), []byte(sum("evil")), 1), s
			}},
		{name: "no release.json.sig", tag: stable, want: "refused", why: "not signed",
			serve: func(m, _ []byte) ([]byte, []byte) { return m, nil }},
		{name: "no release.json", tag: stable, want: "refused", why: "not signed",
			serve: func(_, s []byte) ([]byte, []byte) { return nil, s }},
		{name: "an old signed release under a new tag", tag: stable, want: "refused", why: "not this release's tag",
			edit: func(m *Manifest) { m.Tag, m.Assets["mur-linux-amd64"] = "v1.2.2", Asset{sum("binary v1.2.4")} }},
		{name: "a nightly with GitHub's prerelease flag cleared", tag: nightly, want: "refused", why: "takes stable releases"},
		{name: "a channel that doesn't match the tag", tag: nightly, prerelease: true, want: "refused", why: "does not match its tag",
			edit: func(m *Manifest) { m.Channel = "stable" }},
		{name: "a release.json for other assets", tag: stable, want: "refused", why: "lists no mur-linux-amd64",
			edit: func(m *Manifest) { m.Assets = map[string]Asset{"mur-darwin-arm64": {sum("binary v1.2.4")}} }},
		{name: "a binary that doesn't match release.json", tag: stable, want: "error", why: "checksum mismatch",
			edit: func(m *Manifest) { m.Assets["mur-linux-amd64"] = Asset{sum("another binary")} }},
		{name: "an older release isn't fetched", tag: "v1.2.2", want: ""},
		{name: "no key: SHA256SUMS only, as before", tag: stable, unsigned: true, want: "1.2.4",
			serve: func([]byte, []byte) ([]byte, []byte) { return nil, nil }},
		{name: "no key: a bad signature is ignored", tag: stable, unsigned: true, want: "1.2.4",
			serve: func(m, _ []byte) ([]byte, []byte) { return m, Sign(other, m) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := Manifest{Tag: c.tag, Channel: Channel(c.tag), Assets: map[string]Asset{
				"mur-linux-amd64": {sum("binary " + c.tag)}, "SHA256SUMS": {sum("sums")},
			}}
			if c.edit != nil {
				c.edit(&m)
			}
			manifest, _ := json.MarshalIndent(m, "", "  ")
			sig := Sign(key, manifest)
			if c.serve != nil {
				manifest, sig = c.serve(manifest, sig)
			}
			var url string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assets := []map[string]string{
					{"name": "mur-linux-amd64", "browser_download_url": url + "/binary"},
					{"name": "SHA256SUMS", "browser_download_url": url + "/sums"},
				}
				if manifest != nil {
					assets = append(assets, map[string]string{"name": ManifestName, "browser_download_url": url + "/manifest"})
				}
				if sig != nil {
					assets = append(assets, map[string]string{"name": SignatureName, "browser_download_url": url + "/sig"})
				}
				switch r.URL.Path {
				case "/latest": // the flag cleared, as a writer could
					json.NewEncoder(w).Encode(map[string]any{"tag_name": c.tag, "assets": assets})
				case "/":
					json.NewEncoder(w).Encode([]map[string]any{{"tag_name": c.tag, "prerelease": Channel(c.tag) == "prerelease", "assets": assets}})
				case "/sums":
					fmt.Fprintf(w, "%s  mur-linux-amd64\n", sum("binary "+c.tag))
				case "/binary":
					fmt.Fprint(w, "binary "+c.tag)
				case "/manifest":
					w.Write(manifest)
				case "/sig":
					w.Write(sig)
				default:
					t.Errorf("unexpected request %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			url = srv.URL
			gitDir := repo(t)
			if c.prerelease {
				if b, e := exec.Command("git", "--git-dir="+gitDir, "config", "murmur.channel", "prerelease").CombinedOutput(); e != nil {
					t.Fatalf("%s: %v", b, e)
				}
			}
			path := filepath.Join(t.TempDir(), "mur")
			os.WriteFile(path, []byte("old"), 0755)
			original, _ := Identity(path)
			var logs bytes.Buffer
			o := Options{Version: "1.2.3", GitDir: gitDir, Executable: path, URL: url, OS: "linux", Arch: "amd64", Client: srv.Client(), Log: log.New(&logs, "", 0),
				refused: map[string]bool{}}
			if !c.unsigned {
				o.Keys = []ed25519.PublicKey{public(spare), public(key)}
			}
			done, latest, err := attempt(context.Background(), o, original)
			b, _ := os.ReadFile(path)
			switch c.want {
			case "refused", "error":
				if err == nil || done || string(b) != "old" || !strings.Contains(err.Error(), c.why) {
					t.Fatalf("done=%v err=%v binary=%q, want an error saying %q", done, err, b, c.why)
				}
				refused := strings.Contains(logs.String(), "self-update: refused release")
				if refused != (c.want == "refused") {
					t.Fatalf("logged %q for %v", &logs, err)
				}
				// The page isn't told of a release nobody signed.
				if c.want == "refused" && (latest != "" || Newer(gitDir, "1.2.3") != "") {
					t.Fatalf("recorded a refused release: %q, %q", latest, Newer(gitDir, "1.2.3"))
				}
				// The next hour's check refuses it again, without another line in the log.
				before := logs.Len()
				if _, _, err := attempt(context.Background(), o, original); err == nil || logs.Len() != before {
					t.Fatalf("second check: err=%v, logged %q", err, logs.String()[before:])
				}
			case "":
				if err != nil || done || string(b) != "old" {
					t.Fatalf("installed: done=%v err=%v binary=%q", done, err, b)
				}
			default:
				if err != nil || !done || string(b) != "binary v"+c.want || latest != c.want {
					t.Fatalf("not installed: done=%v err=%v binary=%q latest=%q log=%s", done, err, b, latest, &logs)
				}
				if Newer(gitDir, "1.2.3") != c.want {
					t.Fatalf("recorded %q", Newer(gitDir, "1.2.3"))
				}
			}
		})
	}
}

// The updater has checked the release's tag is newer than the running version before it asks for
// release.json, so this check of the signed version can only be reached on its own.
func TestSignedVersionMustBeNewer(t *testing.T) {
	key := testKey(t)
	m, _ := json.Marshal(Manifest{Tag: "v1.2.4", Channel: "stable", Assets: map[string]Asset{"mur-linux-amd64": {strings.Repeat("a", 64)}}})
	keys := []ed25519.PublicKey{public(key)}
	if _, err := checkRelease(keys, m, Sign(key, m), "v1.2.4", "1.2.3", "mur-linux-amd64", false); err != nil {
		t.Fatal(err)
	}
	for _, running := range []string{"1.2.4", "1.2.5", "1.3.0-rc.1"} {
		if _, err := checkRelease(keys, m, Sign(key, m), "v1.2.4", running, "mur-linux-amd64", false); err == nil {
			t.Errorf("v1.2.4 accepted by %s", running)
		}
	}
}

func TestChannel(t *testing.T) {
	for tag, want := range map[string]string{"v1.2.3": "stable", "1.2.3": "stable", "v1.2.3-rc.1": "prerelease",
		"v1.2.4-nightly.20261109": "prerelease", "latest": "", "v1.2": ""} {
		if got := Channel(tag); got != want {
			t.Errorf("Channel(%q) = %q, want %q", tag, got, want)
		}
	}
}
