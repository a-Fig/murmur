package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v1.2.4", "1.2.3", true}, {"1.10.0", "1.9.9", true}, {"2.0.0", "1.99.99", true},
		{"1.2.3", "1.2.3", false}, {"1.2.2", "1.2.3", false}, {"1.2.3", "1.2.3-rc.1", true},
		{"1.2.3-rc.10", "1.2.3-rc.2", true}, {"1.2.3-rc.1", "1.2.3", false},
		{"garbage", "1.2.3", false}, {"1.2", "1.2.3", false}, {"01.2.3", "1.2.3", false},
		{"1.2.3+build", "1.2.3", false}, {"1.2.3-", "1.2.3", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q,%q)=%v", c.a, c.b, got)
		}
	}
}
func TestAssets(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			want := "mur-" + osname + "-" + arch
			if osname == "windows" {
				want += ".exe"
			}
			if got := assetName(osname, arch); got != want {
				t.Errorf("asset=%s", got)
			}
		}
	}
	if assetName("linux", "386") != "" || assetName("plan9", "amd64") != "" {
		t.Fatal("unsupported asset")
	}
}
func TestChecksums(t *testing.T) {
	data := []byte("new binary")
	sum := fmt.Sprintf("%x", sha256.Sum256(data))
	for _, marker := range []string{" ", "*"} {
		if err := verify(data, []byte(sum+" "+marker+"mur-linux-amd64\n"), "mur-linux-amd64"); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []string{strings.Repeat("0", 64) + "  mur-linux-amd64", sum + "  other", "", "junk  mur-linux-amd64"} {
		if verify(data, []byte(s), "mur-linux-amd64") == nil {
			t.Fatalf("accepted %q", s)
		}
	}
}
func TestReplace(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		for _, failAt := range []int{0, 1, 2} {
			if goos == "linux" && failAt == 2 {
				continue
			}
			t.Run(fmt.Sprintf("%s/fail%d", goos, failAt), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "mur")
				os.WriteFile(path, []byte("old"), 0755)
				calls := 0
				err := replace(path, []byte("new"), goos, func(a, b string) error {
					calls++
					if calls == failAt {
						return errors.New("injected rename failure")
					}
					return os.Rename(a, b)
				})
				want := "new"
				if failAt > 0 {
					want = "old"
					if err == nil {
						t.Fatal("no failure")
					}
				} else if err != nil {
					t.Fatal(err)
				}
				b, e := os.ReadFile(path)
				if e != nil || string(b) != want {
					t.Fatalf("binary %q: %v, want %s", b, e, want)
				}
				entries, _ := os.ReadDir(filepath.Dir(path))
				if len(entries) != 1 {
					t.Fatalf("leftovers: %v", entries)
				}
			})
		}
	}
}
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if b, e := exec.Command("git", "init", "-q", dir).CombinedOutput(); e != nil {
		t.Fatalf("%s: %v", b, e)
	}
	return filepath.Join(dir, ".git")
}
func TestOptOut(t *testing.T) {
	gitDir := repo(t)
	ctx := context.Background()
	for _, v := range []string{"1", "true", "yes"} {
		if !Off(ctx, func(string) string { return v }, gitDir) {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"", "0", "false", "FALSE"} {
		if Off(ctx, func(string) string { return v }, gitDir) {
			t.Fatal(v)
		}
	}
	getenv := func(string) string { return "" }
	if b, e := exec.Command("git", "--git-dir="+gitDir, "config", "murmur.update", "false").CombinedOutput(); e != nil {
		t.Fatalf("%s: %v", b, e)
	}
	if !Off(ctx, getenv, gitDir) {
		t.Fatal("git opt-out ignored")
	}
}

func TestChannelConfig(t *testing.T) {
	gitDir := repo(t)
	ctx := context.Background()
	for _, c := range []struct {
		value string
		want  bool
	}{{"", false}, {"stable", false}, {"nightly", false}, {"prerelease", true}, {"Prerelease", true}} {
		args := []string{"--git-dir=" + gitDir, "config", "murmur.channel", c.value}
		if c.value == "" {
			args = []string{"--git-dir=" + gitDir, "config", "--unset-all", "murmur.channel"}
		}
		exec.Command("git", args...).Run()
		if got := Prerelease(ctx, gitDir); got != c.want {
			t.Errorf("murmur.channel %q: prerelease=%v", c.value, got)
		}
	}
}

// The stable channel reads only releases/latest, which GitHub never fills with a prerelease. The
// prerelease channel takes the highest version of all published releases, whatever order GitHub
// lists them in, and neither channel ever installs a lower version than the one running.
func TestChannels(t *testing.T) {
	type rel struct {
		tag string
		pre bool
	}
	// A full first page of newer, lower releases, with an older, higher candidate on the second.
	var long []rel
	for i := 100; i > 0; i-- {
		long = append(long, rel{fmt.Sprintf("v1.2.%d", i), false})
	}
	long = append(long, rel{"v2.0.0-rc.1", true})
	// More than ten full pages: the eleventh is never read, so nothing is chosen from the ten.
	var huge []rel
	for i := 1000; i > 0; i-- {
		huge = append(huge, rel{fmt.Sprintf("v1.3.%d", i), false})
	}
	huge = append(huge, rel{"v2.0.0", false})
	for _, c := range []struct {
		name, running string
		prerelease    bool
		latest        string // the stable release GitHub calls latest
		list          []rel  // newest first, as GitHub lists them
		want          string // the version installed, "" for none, "error" for a failed check
	}{
		{"stable channel skips a newer prerelease", "1.2.3", false, "v1.2.4", []rel{{"v1.2.5-nightly.20261108", true}, {"v1.2.4", false}}, "1.2.4"},
		{"prerelease channel takes it", "1.2.3", true, "v1.2.4", []rel{{"v1.2.5-nightly.20261108", true}, {"v1.2.4", false}}, "1.2.5-nightly.20261108"},
		{"a newer stable beats the prerelease running", "1.2.4-rc.1", true, "v1.2.4", []rel{{"v1.2.4-rc.1", true}, {"v1.2.4", false}, {"v1.2.3", false}}, "1.2.4"},
		{"version order, not list order", "1.2.3", true, "v1.3.0", []rel{{"v1.2.5-nightly.20261108", true}, {"v1.3.0", false}, {"v1.2.4", false}}, "1.3.0"},
		{"rc.10 follows rc.2", "1.2.3", true, "v1.2.3", []rel{{"v1.3.0-rc.2", true}, {"v1.3.0-rc.10", true}}, "1.3.0-rc.10"},
		{"drafts and odd tags are skipped", "1.2.3", true, "v1.2.3", []rel{{"v9.9.9", false}, {"latest", true}}, ""},
		{"no downgrade from a pruned nightly", "1.2.5-nightly.20261101", true, "v1.2.4", []rel{{"v1.2.5-nightly.20261031", true}, {"v1.2.4", false}}, ""},
		{"no downgrade after leaving the channel", "1.2.5-nightly.20261101", false, "v1.2.4", nil, ""},
		{"the stable channel leaves a nightly for the next stable", "1.2.5-nightly.20261101", false, "v1.2.5", nil, "1.2.5"},
		{"a later page is read", "1.2.3", true, "v1.2.100", long, "2.0.0-rc.1"},
		{"too many pages is an error", "1.2.3", true, "v2.0.0", huge, "error"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var url string
			var listed atomic.Bool
			assets := func(tag string) []map[string]string {
				return []map[string]string{{"name": "mur-linux-amd64", "browser_download_url": url + "/binary/" + tag}, {"name": "SHA256SUMS", "browser_download_url": url + "/sums/" + tag}}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/latest":
					json.NewEncoder(w).Encode(map[string]any{"tag_name": c.latest, "assets": assets(c.latest)})
				case r.URL.Path == "/" && r.URL.Query().Get("per_page") == "100":
					listed.Store(true)
					page, _ := strconv.Atoi(r.URL.Query().Get("page"))
					out := []map[string]any{}
					for i, x := range c.list {
						if i/100 != page-1 {
							continue
						}
						out = append(out, map[string]any{"tag_name": x.tag, "prerelease": x.pre, "draft": x.tag == "v9.9.9", "assets": assets(x.tag)})
					}
					json.NewEncoder(w).Encode(out)
				case strings.HasPrefix(r.URL.Path, "/sums/"):
					fmt.Fprintf(w, "%x  mur-linux-amd64\n", sha256.Sum256([]byte("binary "+strings.TrimPrefix(r.URL.Path, "/sums/"))))
				case strings.HasPrefix(r.URL.Path, "/binary/"):
					fmt.Fprint(w, "binary "+strings.TrimPrefix(r.URL.Path, "/binary/"))
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
			o := Options{Version: c.running, GitDir: gitDir, Executable: path, URL: url, OS: "linux", Arch: "amd64", Client: srv.Client(), Log: log.New(io.Discard, "", 0)}
			done, latest, err := attempt(context.Background(), o, original)
			if c.want == "error" {
				if err == nil || done {
					t.Fatalf("no error: done=%v", done)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			got := ""
			if done {
				got = strings.TrimPrefix(string(b), "binary v")
			} else if string(b) != "old" {
				t.Fatalf("binary changed without a restart: %s", b)
			}
			if got != c.want {
				t.Fatalf("installed %q, want %q", got, c.want)
			}
			if done && latest != got {
				t.Fatalf("installed %q, reported %q", got, latest)
			}
			if listed.Load() != c.prerelease {
				t.Fatalf("listed releases: %v on prerelease=%v", listed.Load(), c.prerelease)
			}
		})
	}
}
func TestReleases(t *testing.T) {
	for _, mode := range []string{"good", "bad", "missing", "same", "older", "prerelease", "dev", "off", "changed"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			data := []byte("new executable")
			var url string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				switch r.URL.Path {
				case "/latest":
					tag := "v1.2.4"
					if mode == "same" {
						tag = "v1.2.3"
					}
					if mode == "older" {
						tag = "v1.2.2"
					}
					json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "prerelease": mode == "prerelease", "assets": []map[string]string{
						{"name": "mur-linux-amd64", "browser_download_url": url + "/binary"}, {"name": "SHA256SUMS", "browser_download_url": url + "/sums"},
					}})
				case "/sums":
					sum := fmt.Sprintf("%x", sha256.Sum256(data))
					name := "mur-linux-amd64"
					if mode == "bad" {
						sum = strings.Repeat("0", 64)
					}
					if mode == "missing" {
						name = "other"
					}
					fmt.Fprintf(w, "%s  %s\n", sum, name)
				case "/binary":
					w.Write(data)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			url = srv.URL
			path := filepath.Join(t.TempDir(), "mur")
			os.WriteFile(path, []byte("old"), 0755)
			var logs bytes.Buffer
			o := Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: url, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: func(string) string { return "" }, Log: log.New(&logs, "", 0), Every: time.Hour}
			if mode == "dev" {
				o.Version += "-dev"
			}
			if mode == "off" {
				o.Getenv = func(string) string { return "1" }
			}
			restarted := false
			if mode == "changed" {
				original, _ := Identity(path)
				replacement := path + ".new"
				os.WriteFile(replacement, data, 0755)
				os.Rename(path, path+".old")
				os.Rename(replacement, path)
				done, _, err := attempt(context.Background(), o, original)
				if !done || err != nil || hits.Load() != 0 {
					t.Fatalf("changed: %v %v", done, err)
				}
				return
			}
			var reports []Status
			o.Report = func(s Status) { reports = append(reports, s) }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			Run(ctx, o, func() { restarted = true })
			// The page hears what each check found (#240).
			want := map[string]Status{
				"good": {Latest: "1.2.4", Installed: true}, "bad": {Latest: "1.2.4", Failed: true},
				"missing": {Latest: "1.2.4", Failed: true}, "same": {Latest: "1.2.3"}, "older": {Latest: "1.2.2"},
				"prerelease": {}, "off": {Off: true},
			}
			if w, ok := want[mode]; ok && (len(reports) != 1 || reports[0] != w) {
				t.Errorf("reports %+v, want [%+v]", reports, w)
			}
			if mode == "dev" && len(reports) != 0 {
				t.Errorf("a dev build reported %+v", reports)
			}
			b, _ := os.ReadFile(path)
			if mode == "good" {
				if !restarted || string(b) != string(data) || !strings.Contains(logs.String(), "1.2.3 -> 1.2.4") {
					t.Fatalf("update: restart=%v data=%s logs=%s", restarted, b, &logs)
				}
			} else if restarted || string(b) != "old" {
				t.Fatalf("unexpected install: %v %s", restarted, b)
			}
			if (mode == "dev" || mode == "off") && hits.Load() != 0 {
				t.Fatal("opted-out request")
			}
		})
	}
}
func TestCleanup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur.exe")
	for _, p := range []string{path, path + ".old-.mur-update-123", path + ".old-unrelated"} {
		os.WriteFile(p, nil, 0600)
	}
	cleanup(path)
	if _, err := os.Stat(path + ".old-.mur-update-123"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".old-unrelated"); err != nil {
		t.Fatal(err)
	}
}

func TestFailedChecksArePeriodicAndQuiet(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 3 {
			defer cancel()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(), Every: 20 * time.Millisecond, Getenv: func(string) string { return "" }, Log: log.New(&logs, "", 0)}, func() { t.Error("restarted after failed check") })
	if hits.Load() < 2 {
		t.Fatalf("only %d checks", hits.Load())
	}
	if logs.Len() != 0 {
		t.Fatalf("network failure logged: %s", &logs)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "old" {
		t.Fatalf("binary changed: %s %v", b, err)
	}
}

func TestUnwritableInstallLogsOnceAndStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request from unwritable install")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".update.lock"
	if err := os.WriteFile(lockPath, nil, 0444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(lockPath, 0600)
	// Some test environments run as root and can write despite the permission bits.
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0600)
	if err == nil {
		f.Close()
		t.Skip("permissions do not prevent writing")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(), Every: time.Millisecond, Getenv: func(string) string { return "" }, Log: log.New(&logs, "", 0)}, func() { t.Error("restarted") })
	if ctx.Err() != nil {
		t.Fatal("retried until context expired")
	}
	if strings.Count(logs.String(), "disabled until restart") != 1 {
		t.Fatalf("log: %s", &logs)
	}
}

func TestManualReinstallDuringDownload(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mur")
			if err := os.WriteFile(path, []byte("original"), 0755); err != nil {
				t.Fatal(err)
			}
			original, err := Identity(path)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("release 1.2.4")
			manual := []byte("manual install 1.2.5")
			name := assetName(goos, "amd64")
			var url string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest":
					json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.4", "assets": []map[string]string{
						{"name": name, "browser_download_url": url + "/binary"}, {"name": "SHA256SUMS", "browser_download_url": url + "/sums"},
					}})
				case "/sums":
					fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(data), name)
				case "/binary":
					// Complete a manual reinstall while the updater is waiting for the asset.
					// Neither install script takes the updater's lock.
					for _, err := range []error{os.WriteFile(path+".manual", manual, 0755), os.Rename(path, path+".old"), os.Rename(path+".manual", path)} {
						if err != nil {
							t.Error(err)
							http.Error(w, "manual reinstall failed", 500)
							return
						}
					}
					w.Write(data)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			url = srv.URL
			var logs bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done, _, err := attempt(ctx, Options{Version: "1.2.3", Executable: path, URL: url, OS: goos, Arch: "amd64", Client: srv.Client(), Log: log.New(&logs, "", 0)}, original)
			if err != nil || !done {
				t.Fatalf("should restart onto manual install: done=%v err=%v", done, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, manual) {
				t.Fatalf("manual install overwritten: %q (%v)", got, err)
			}
			if logs.Len() != 0 {
				t.Fatalf("claimed an update it did not install: %s", &logs)
			}
		})
	}
}

// A reinstall between the process starting and the updater starting (it waits for the daemon's first
// sync) is not this process's binary to replace, even with a release newer than this process.
func TestReinstallBeforeTheUpdaterStarted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("running"), 0755); err != nil {
		t.Fatal(err)
	}
	running, err := Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	manual := []byte("manual prerelease")
	for _, err := range []error{os.WriteFile(path+".manual", manual, 0755), os.Rename(path, path+".old"), os.Rename(path+".manual", path)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("asked for %s instead of restarting onto the reinstall", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	restarted := false
	Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, Running: running, URL: srv.URL, Client: srv.Client(),
		Getenv: func(string) string { return "" }}, func() { restarted = true })
	if !restarted {
		t.Error("no restart onto the reinstall")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, manual) {
		t.Errorf("reinstall overwritten: %q (%v)", got, err)
	}
}

// The latest release on the clone's channel is kept, so `mur version` can say when a newer one is
// out; a malformed tag is not kept.
func TestNewerFromRecordedLatest(t *testing.T) {
	gitDir := t.TempDir()
	if got := Newer(gitDir, "0.2.0"); got != "" {
		t.Fatalf("nothing recorded: %q", got)
	}
	recordLatest(gitDir, "v0.3.0")
	for v, want := range map[string]string{"0.2.0": "0.3.0", "0.3.0": "", "0.4.0": "", "0.3.0-dev": "0.3.0"} {
		if got := Newer(gitDir, v); got != want {
			t.Errorf("Newer(%q) = %q, want %q", v, got, want)
		}
	}
	recordLatest(gitDir, "not-a-version")
	if got := Newer(gitDir, "0.2.0"); got != "0.3.0" {
		t.Errorf("a malformed tag replaced the record: %q", got)
	}
}
