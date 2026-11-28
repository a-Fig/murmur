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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proc"
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

// TestReplace fails each call replace makes in turn: Windows links the running binary to a backup
// and renames the new one over it, Unix only renames. A failure leaves the old binary, and nothing
// beside it.
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
				call := func(op func(string, string) error) func(string, string) error {
					return func(a, b string) error {
						calls++
						if calls == failAt {
							if goos == "windows" && failAt == 1 {
								return &os.LinkError{Op: "link", Old: a, New: b, Err: errors.ErrUnsupported}
							}
							return errors.New("injected failure")
						}
						return op(a, b)
					}
				}
				err := replace(path, []byte("new"), goos, files{call(os.Rename), call(os.Link)})
				want := "new"
				if failAt > 0 {
					want = "old"
					if err == nil {
						t.Fatal("no failure")
					}
					if noLink := errors.Is(err, errNoLink); noLink != (goos == "windows" && failAt == 1) {
						t.Fatalf("errNoLink is %v: %v", noLink, err)
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

// TestLinkFailureThatMayPass: only a volume without links stops updates; a link that fails because
// mur.exe was moved for a moment (an install.ps1 from before #1198 takes no lock) leaves the next check to try again.
func TestLinkFailureThatMayPass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur.exe")
	os.WriteFile(path, []byte("old"), 0755)
	err := replace(path, []byte("new"), "windows", files{os.Rename, func(a, b string) error {
		return &os.LinkError{Op: "link", Old: a, New: b, Err: os.ErrNotExist}
	}})
	if err == nil || errors.Is(err, errNoLink) {
		t.Fatalf("got %v, want a failure that isn't errNoLink", err)
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
	}{{"", false}, {"stable", false}, {"nightly", false}, {"prerelease", true}, {"Prerelease", true}, {"prerelease-held", true}} {
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
			// Each check reports once and then waits an hour, so the run ends at the first report. The
			// timeout is only for a check that never reports: one second of it wasn't always enough
			// for a check to finish under -race on a loaded machine.
			var reports []Status
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			o.Report = func(s Status) { reports = append(reports, s); cancel() }
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
	path := filepath.Join(t.TempDir(), "mur")
	dir := filepath.Dir(path)
	// mur-nightly is another binary, which its own lock covers, though its staged names start with
	// mur's prefix.
	staged := filepath.Join(dir, ".mur-update-mur-456")
	// install.ps1's backup has eight lower-case hex digits; names that only look like one stay.
	installers := path + ".old-99e92547"
	others := []string{path + ".old-unrelated", filepath.Join(dir, ".mur-update-mur-nightly-789"),
		path + ".old-.mur-update-mur-nightly-789", path + ".old-99e9254", path + ".old-99e925470", path + ".old-99e9254g", path + "-nightly.old-99e92547"}
	for _, p := range append([]string{path + ".old-.mur-update-mur-123", path + ".old-.mur-update-321", staged, installers}, others...) {
		os.WriteFile(p, nil, 0600)
	}
	os.WriteFile(path, []byte("the binary"), 0o755)
	// Another updater's staged file is not stale.
	lock, err := proc.Lock(path + ".update.lock")
	if err != nil {
		t.Fatal(err)
	}
	cleanup(path, "windows")
	if _, err := os.Stat(staged); err != nil {
		t.Fatal("removed while another updater held the lock:", err)
	}
	lock.Close()
	// Only Windows has an install.ps1: elsewhere a file with its backup's name is somebody's own.
	cleanup(path, "linux")
	if _, err := os.Stat(installers); err != nil {
		t.Fatal("a file named as install.ps1 names a backup was removed where there is no install.ps1:", err)
	}
	cleanup(path, "windows")
	for _, p := range []string{path + ".old-.mur-update-mur-123", path + ".old-.mur-update-321", staged, installers} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal(p, err)
		}
	}
	for _, p := range others {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}

// An installer's backup that is still a second name of the binary stays (#1171, Sol's review of
// #1173): an install.ps1 from before #1198 takes no lock, and its backup is its hard link to mur.exe until it renames
// the new file over mur.exe. Taken in between, the link's loss sends the installer to a swap that can leave
// no mur.exe. Once the new binary is in place the backup is an older binary, and goes.
func TestCleanupLeavesABackupThatIsStillTheBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur.exe")
	if err := os.WriteFile(path, []byte("installed"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := path + ".old-99e92547"
	if err := os.Link(path, link); err != nil {
		t.Skipf("this folder's volume can't link: %v", err)
	}
	cleanup(path, "windows")
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("a link to the binary itself was removed, mid-install as far as cleanup can tell: %v", err)
	}

	// The installer's rename: a new file takes the binary's name, and the links are the old one.
	staged := path + ".new-99e92547"
	if err := os.WriteFile(staged, []byte("the new release"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, path); err != nil {
		t.Fatal(err)
	}
	cleanup(path, "windows")
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Errorf("%s is left after the swap: %v", filepath.Base(link), err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "the new release" {
		t.Fatalf("the binary: %q, %v", b, err)
	}
}

// With no binary at the path, as between the two steps of install.ps1's swap where it can't link,
// its backup may be the only copy there is: none of the installer's is removed.
func TestCleanupRemovesNoBackupWhileTheBinaryIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur.exe")
	backups := []string{path + ".old-99e92547", path + ".old-0a1b2c3d"}
	for _, b := range backups {
		if err := os.WriteFile(b, []byte("moved aside"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cleanup(path, "windows")
	for _, b := range backups {
		if _, err := os.Stat(b); err != nil {
			t.Fatalf("a backup was removed with no binary in place: %v", err)
		}
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

// An updater that can't write the binary's directory logs it once and checks no more, but goes on
// watching: there, after a sudo install to /usr/local/bin say, only an installer puts a new binary
// in place, and the daemon restarts onto it as any other does. A person's Update now is refused.
func TestUnwritableInstallLogsOnceAndWatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request from unwritable install")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	path := exeName(t.TempDir())
	if err := os.WriteFile(path, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	holdRunning(t, path)
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
	logs := &lockedBuf{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	install := make(chan string, 1)
	reports := make(chan Status, 16)
	restarted := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(), Every: time.Millisecond,
			Watch: 20 * time.Millisecond, Install: install, Getenv: func(string) string { return "" }, Log: log.New(logs, "", 0),
			Report: func(s Status) { reports <- s }}, func() { close(restarted) })
	}()
	for !strings.Contains(logs.String(), "disabled until restart") {
		if ctx.Err() != nil {
			t.Fatal("never stopped")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := <-reports; !s.Failed {
		t.Errorf("the failed check reported %+v", s)
	}
	install <- "1.2.4"
	if s := <-reports; !s.Failed || !s.Asked || s.Error == "" {
		t.Errorf("Update now while stopped reported %+v", s)
	}
	time.Sleep(100 * time.Millisecond) // an hourly check every millisecond would have run by now
	t.Setenv(standInEnv, "1.2.4")
	standIn(t, path)
	select {
	case <-restarted:
	case <-ctx.Done():
		t.Fatal("no restart onto the binary an installer put in place")
	}
	<-done
	if n := strings.Count(logs.String(), "disabled until restart"); n != 1 {
		t.Fatalf("logged it %d times: %s", n, logs)
	}
}

// Daemons that restart together check together. The one that loses the update lock to another
// reports no failure and doesn't wait an hour: it checks again soon (#1177).
func TestAHeldUpdateLockIsNoFailedCheck(t *testing.T) {
	was := checkGap
	checkGap = 50 * time.Millisecond
	t.Cleanup(func() { checkGap = was })
	asked := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked <- struct{}{}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := proc.Lock(path + ".update.lock") // another daemon's updater
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reports := make(chan Status, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(), Every: time.Hour,
			Getenv: func(string) string { return "" }, Report: func(s Status) { reports <- s }}, func() { t.Error("restarted") })
	}()
	time.Sleep(4 * checkGap) // a few checks, each finding the lock held
	select {
	case s := <-reports:
		t.Fatalf("reported %+v while another updater held the lock", s)
	default:
	}
	lock.Close()
	select {
	case <-asked:
	case <-ctx.Done():
		t.Fatal("no check once the lock was free")
	}
	if s := <-reports; !s.Failed {
		t.Errorf("the check GitHub failed reported %+v", s)
	}
	cancel()
	<-done
}

// lockedBuf is a log's buffer that a test can read while the logger writes.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

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

// Checked is when the clone last heard of the latest release, and zero before it has.
func TestCheckedIsWhenTheLatestWasRecorded(t *testing.T) {
	gitDir := t.TempDir()
	if got := Checked(gitDir); !got.IsZero() {
		t.Fatalf("nothing recorded, but checked at %v", got)
	}
	before := time.Now().Add(-2 * time.Second) // a file system's times can be coarse
	recordLatest(gitDir, "v0.3.0")
	if got := Checked(gitDir); got.Before(before) || got.After(time.Now().Add(2*time.Second)) {
		t.Errorf("recorded just now, but checked at %v", got)
	}
}

// A download never leaves https once the check is on https: not through a redirect, and not to an
// asset URL the API names. The binary and SHA256SUMS are fetched separately, so a plain-http hop
// would let anyone on the path swap both. Redirects that keep their scheme are followed, which is
// all GitHub's (github.com to its asset host) and the plain-http tests' do.
func TestDownloadsStayOnHTTPS(t *testing.T) {
	for _, c := range []struct {
		name          string
		tls           bool   // whether the API (and what it redirects to itself) is https
		asset, target string // where the API's asset URLs point and where those redirect: "api" or "plain"
		installs      bool
	}{
		{"https to http redirect", true, "api", "plain", false},
		{"https to https redirect", true, "api", "api", true},
		{"http to http redirect", false, "api", "api", true},
		{"https API naming an http asset", true, "plain", "plain", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			data := []byte("release 1.2.4")
			// The release's real assets, at /real/... and, on the plain server, where the API names them.
			release := func(w http.ResponseWriter, r *http.Request) bool {
				switch strings.TrimPrefix(r.URL.Path, "/real") {
				case "/sums":
					fmt.Fprintf(w, "%x  mur-linux-amd64\n", sha256.Sum256(data))
				case "/binary":
					w.Write(data)
				default:
					return false
				}
				return true
			}
			var plainHits atomic.Int32
			plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plainHits.Add(1)
				if !release(w, r) {
					http.NotFound(w, r)
				}
			}))
			defer plain.Close()
			var apiURL string
			base := func(which string) string {
				if which == "plain" {
					return plain.URL
				}
				return apiURL
			}
			api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/latest":
					json.NewEncoder(w).Encode(map[string]any{"tag_name": "v1.2.4", "assets": []map[string]string{
						{"name": "mur-linux-amd64", "browser_download_url": base(c.asset) + "/binary"},
						{"name": "SHA256SUMS", "browser_download_url": base(c.asset) + "/sums"},
					}})
				case "/binary", "/sums":
					http.Redirect(w, r, base(c.target)+"/real"+r.URL.Path, http.StatusFound)
				default:
					if !release(w, r) {
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}
			}))
			if c.tls {
				api.StartTLS()
			} else {
				api.Start()
			}
			defer api.Close()
			apiURL = api.URL
			path := filepath.Join(t.TempDir(), "mur")
			os.WriteFile(path, []byte("old"), 0755)
			original, err := Identity(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			o := Options{Version: "1.2.3", Executable: path, URL: api.URL, OS: "linux", Arch: "amd64", Client: api.Client(), Log: log.New(io.Discard, "", 0)}
			done, _, err := attempt(ctx, o, original)
			got, _ := os.ReadFile(path)
			if c.installs {
				if !done || err != nil || !bytes.Equal(got, data) {
					t.Fatalf("not installed: done=%v err=%v binary=%q", done, err, got)
				}
				return
			}
			if done || err == nil || string(got) != "old" {
				t.Fatalf("installed over plain http: done=%v err=%v binary=%q", done, err, got)
			}
			if n := plainHits.Load(); n != 0 {
				t.Fatalf("%d requests went over plain http", n)
			}
		})
	}
}

// Newer asks git what is held back only for a prerelease, the one kind that can be: for a stable
// release, the usual answer, `mur version` and the page start no process (#1164).
func TestNewerStartsGitOnlyForAPrerelease(t *testing.T) {
	gitDir := t.TempDir()
	var started int
	was := gitx.Observe
	gitx.Observe = func(string, time.Time, time.Duration) { started++ }
	t.Cleanup(func() { gitx.Observe = was })

	recordLatest(gitDir, "v0.3.0")
	if got := Newer(gitDir, "0.2.0"); got != "0.3.0" || started != 0 {
		t.Errorf("a stable release: %q, with %d git started; want 0.3.0 and none", got, started)
	}
	recordLatest(gitDir, "v0.4.0-rc.1")
	if got := Newer(gitDir, "0.3.0"); got != "0.4.0-rc.1" || started == 0 {
		t.Errorf("a prerelease: %q, with %d git started; want 0.4.0-rc.1 and git asked what is held back", got, started)
	}
}

// A backup that couldn't go when the daemon started, as a process still ran it, goes at a later
// check, once nothing does (#1171): the updater looks at every check, with updates off too, which
// asks GitHub nothing.
func TestBackupsAreClearedAtEveryCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	checks := make(chan struct{}, 100)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Options{Version: "1.2.3", GitDir: t.TempDir(), Executable: path, URL: "http://127.0.0.1:1", OS: "windows", Arch: "amd64",
			Getenv: func(k string) string { return map[string]string{"MURMUR_NO_UPDATE": "1"}[k] }, Every: 20 * time.Millisecond,
			Report: func(Status) {
				select {
				case checks <- struct{}{}:
				default:
				}
			}}, func() {})
	}()
	defer func() { cancel(); <-done }()
	<-checks // the daemon's start, with nothing to clear
	left := []string{path + ".old-.mur-update-mur-123", path + ".old-99e92547"}
	for _, p := range left {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		gone := true
		for _, p := range left {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				gone = false
			}
		}
		if gone {
			break
		}
		if time.Now().After(end) {
			t.Fatal("backups left beside the binary after the daemon's start weren't cleared at a later check")
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the binary itself: %v", err)
	}
}
