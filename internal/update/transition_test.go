package update

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/gitx"
)

// v053Off and v053Prerelease are v0.5.3's Off and Prerelease, copied from
// `git show v0.5.3:internal/update/update.go`: what a daemon of that release, already out, reads
// before each check. It knows nothing of murmur.updateSkip (#1197).
func v053Off(ctx context.Context, getenv func(string) string, gitDir string) bool {
	v := strings.TrimSpace(getenv("MURMUR_NO_UPDATE"))
	if v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return true
	}
	out, err := (gitx.Git{Dir: gitDir}).Run(ctx, "", "config", "--type=bool", "--get", "murmur.update")
	return err == nil && strings.TrimSpace(out) == "false"
}

func v053Prerelease(ctx context.Context, gitDir string) bool {
	out, err := (gitx.Git{Dir: gitDir}).Run(ctx, "", "config", "--get", "murmur.channel")
	return err == nil && strings.EqualFold(strings.TrimSpace(out), "prerelease")
}

// v053Installs is the release a v0.5.3 daemon's check would install, "" for none: its attempt
// picks on the channel it reads and installs only a newer version, with no hold of any kind.
func v053Installs(t *testing.T, srv interface{ Client() *http.Client }, url, gitDir string) string {
	t.Helper()
	ctx := context.Background()
	if v053Off(ctx, noEnv, gitDir) {
		return ""
	}
	o := Options{Version: "0.5.3", GitDir: gitDir, URL: url, Client: srv.Client(), Getenv: noEnv}
	rel, err := pick(ctx, o, v053Prerelease(ctx, gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || !newer(rel.Tag, o.Version) {
		return ""
	}
	return strings.TrimPrefix(rel.Tag, "v")
}

// Going back to stable from a nightly, when the latest stable release is v0.5.3, which doesn't
// know murmur.updateSkip: the setting it leaves must keep v0.5.3 from taking the nightly again, or
// the machine goes back and forth between the two at every restart (#1197). The whole sequence, as
// far as unit tests reach: the nightly goes back, v0.5.3 stays, the next stable release takes the
// machine forward, and from there the nightly channel goes on as it was.
func TestBackToAReleaseThatDoesNotKnowTheHold(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	const bad = "0.5.4-nightly.20261123"
	if _, err := Set(ctx, noEnv, gitDir, bad, ChoiceNightly); err != nil {
		t.Fatal(err)
	}
	if _, err := Back(ctx, noEnv, gitDir, bad); err != nil {
		t.Fatal(err)
	}
	// The choice still reads as nightly, held, here and to the updater.
	if s := Read(ctx, noEnv, gitDir, "0.5.3"); s.Choice != ChoiceNightly || s.Effective != ChoiceNightly || s.Hold != "On stable until the next nightly after "+bad+"." {
		t.Fatalf("after going back: %+v", s)
	}
	if !Prerelease(ctx, gitDir) {
		t.Fatal("the updater no longer reads the nightly channel")
	}
	list := []rel{{"v" + bad, true}, {"v0.5.3", false}}
	srv := releases(t, "v0.5.3", list)
	if got := installed(t, srv, gitDir, bad); got != "0.5.3" {
		t.Fatalf("going back installed %q", got)
	}
	// v0.5.3 starts, checks, and stays: it reads the stable channel, so the nightly isn't taken.
	if got := v053Installs(t, srv, srv.URL, gitDir); got != "" {
		b, _ := os.ReadFile(path)
		t.Fatalf("v0.5.3 installed %s again; global config:\n%s", got, b)
	}
	// It still takes the next stable release: going back isn't turning updates off.
	next := releases(t, "v0.5.4", append([]rel{{"v0.5.4", false}}, list...))
	if got := v053Installs(t, next, next.URL, gitDir); got != "0.5.4" {
		t.Fatalf("v0.5.3 didn't take the next stable release: %q", got)
	}
	// v0.5.4, which knows the hold, is past the nightly gone back from: the hold comes off, and the
	// channel reads prerelease as it did before, to v0.5.3 too.
	if got := installed(t, next, gitDir, "0.5.4"); got != "" {
		t.Fatalf("v0.5.4 installed %q", got)
	}
	out, _ := exec.Command("git", "config", "--file", path, "--get-all", "murmur.channel").Output()
	if strings.TrimSpace(string(out)) != "prerelease" {
		t.Fatalf("channel after the hold: %q", out)
	}
	if out, _ := exec.Command("git", "config", "--file", path, "--get", "murmur.updateSkip").Output(); len(out) != 0 {
		t.Fatalf("hold stayed: %q", out)
	}
	later := releases(t, "v0.5.4", append([]rel{{"v0.5.5-nightly.20261124", true}}, list...))
	if got := installed(t, later, gitDir, "0.5.4"); got != "0.5.5-nightly.20261124" {
		t.Fatalf("the next nightly: installed %q", got)
	}
}

// Going back from a prerelease on the stable channel (a nightly still running after a change to
// On) leaves the channel as it is: there is no nightly to keep v0.5.3 from.
func TestBackOnStableLeavesTheChannel(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	if _, err := Back(ctx, noEnv, gitDir, "0.5.4-nightly.20261123"); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("git", "config", "--file", path, "--get-all", "murmur.channel").Output(); len(out) != 0 {
		t.Fatalf("channel %q", out)
	}
	if s := Read(ctx, noEnv, gitDir, "0.5.3"); s.Choice != ChoiceOn {
		t.Fatalf("%+v", s)
	}
}

// Updates turned off while a release downloads: the release isn't installed. The setting is read
// again just before the swap, not only before the download (#1197). Update now is the exception,
// since it is a person asking for that one install with updates off.
func TestOffDuringADownload(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	srv := releases(t, "v1.2.4", nil)
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/binary/") {
			gitConfig(t, "--file", path, "murmur.update", "false")
		}
		inner.ServeHTTP(w, r)
	})
	exe := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(exe, []byte("old"), 0o755)
	var reports []Status
	restarted := false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	Run(ctx, Options{Version: "1.2.3", GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(),
		Getenv: noEnv, Every: time.Hour, Report: func(s Status) {
			reports = append(reports, s)
			cancel()
		}}, func() { restarted = true })
	if b, _ := os.ReadFile(exe); string(b) != "old" || restarted {
		t.Fatalf("installed %q over updates turned off, restarted %v", b, restarted)
	}
	if len(reports) != 1 || !reports[0].Off || reports[0].Failed || reports[0].Installed || reports[0].Latest != "1.2.4" {
		t.Fatalf("reports %+v", reports)
	}

	// Update now, asked with updates off, goes ahead though the setting is read again.
	gitConfig(t, "--file", path, "--unset-all", "murmur.update")
	o := Options{Version: "1.2.3", GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv}
	o.Log = log.New(io.Discard, "", 0)
	original, _ := Identity(exe)
	done, _, err := install(context.Background(), o, original, "1.2.4")
	if b, _ := os.ReadFile(exe); err != nil || !done || string(b) != "binary v1.2.4" {
		t.Fatalf("Update now: done %v, err %v, binary %q", done, err, b)
	}
}

// The channel changed to stable while a nightly downloads: the nightly isn't installed, and a
// check soon after takes what the stable channel has. Update now, asked for the nightly the page
// showed before the change, isn't installed either: the channel is read again for it too.
func TestStableDuringANightlyDownload(t *testing.T) {
	for _, asked := range []string{"", "1.2.5-nightly.20261108"} {
		t.Run("asked "+asked, func(t *testing.T) {
			path := global(t)
			gitDir := repo(t)
			gitConfig(t, "--file", path, "murmur.channel", "prerelease")
			srv := releases(t, "v1.2.4", []rel{{"v1.2.5-nightly.20261108", true}, {"v1.2.4", false}})
			inner := srv.Config.Handler
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/binary/") {
					exec.Command("git", "config", "--file", path, "--unset-all", "murmur.channel").Run()
				}
				inner.ServeHTTP(w, r)
			})
			exe := filepath.Join(t.TempDir(), "mur")
			os.WriteFile(exe, []byte("old"), 0o755)
			original, _ := Identity(exe)
			o := Options{Version: "1.2.3", GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv, Log: log.New(io.Discard, "", 0)}
			done, _, err := install(context.Background(), o, original, asked)
			if b, _ := os.ReadFile(exe); done || string(b) != "old" {
				t.Fatalf("installed %q after the change to stable (err %v)", b, err)
			}
			if asked != "" && (err == nil || !strings.Contains(err.Error(), "stable")) {
				t.Errorf("Update now's answer: %v", err)
			}
			if asked == "" && err != nil && !errors.Is(err, errNotAsked) {
				t.Errorf("err %v", err)
			}
		})
	}
}
