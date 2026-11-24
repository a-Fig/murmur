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

	"github.com/a-Fig/murmur/internal/clones"
)

// globalValues is every value key has in the global config file at path.
func globalValues(path, key string) string {
	out, _ := exec.Command("git", "config", "--file", path, "--get-all", key).Output()
	return strings.TrimSpace(string(out))
}

// An updater that picked a nightly before a return to stable from that very nightly, made in
// another install, doesn't install it once the download is done (#1212). The check before the
// swap compared the hold with the version running, and read prerelease-held as the nightly
// channel, so the nightly went in. Update now, asked for the nightly the page showed, is refused
// too.
func TestHoldIsReadAgainstTheDownload(t *testing.T) {
	const nightly = "1.2.5-nightly.20261108"
	for _, asked := range []string{"", nightly} {
		t.Run("asked "+asked, func(t *testing.T) {
			path := global(t)
			gitDir := repo(t)
			gitConfig(t, "--file", path, "murmur.channel", "prerelease")
			srv := releases(t, "v1.2.4", []rel{{"v" + nightly, true}, {"v1.2.4", false}})
			inner := srv.Config.Handler
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/binary/") {
					if _, err := Back(r.Context(), noEnv, gitDir, nightly); err != nil {
						t.Error(err)
					}
				}
				inner.ServeHTTP(w, r)
			})
			exe := filepath.Join(t.TempDir(), "mur")
			os.WriteFile(exe, []byte("old"), 0o755)
			original, _ := Identity(exe)
			o := Options{Version: "1.2.4", GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv, Log: log.New(io.Discard, "", 0)}
			done, _, err := install(context.Background(), o, original, asked)
			if b, _ := os.ReadFile(exe); done || string(b) != "old" {
				t.Fatalf("installed %q, the nightly gone back from (err %v)", b, err)
			}
			if !errors.Is(err, errNotAsked) || !strings.Contains(err.Error(), "held back") {
				t.Errorf("err %v", err)
			}
		})
	}
}

// Going back from a prerelease of v0.5.4 or earlier may land on v0.5.3, which takes a prerelease
// wherever `git config --get murmur.channel` reads "prerelease" in its clone. Back holds the global
// value; one it can't, in this repo's config or another clone's on this machine's record, would
// send v0.5.3 to the nightly again, so Back refuses and names it, and writes nothing (#1212). From
// a later prerelease, whose stable release knows the hold, it goes back.
func TestBackRefusesAChannelV053WouldStillRead(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	const bad = "0.5.4-nightly.20261123"
	gitConfig(t, "--file", path, "murmur.channel", "prerelease")
	gitConfig(t, "--file", filepath.Join(gitDir, "config"), "murmur.channel", "prerelease")
	if _, err := Back(ctx, noEnv, gitDir, bad); err == nil || !strings.Contains(err.Error(), "this repo's git config sets murmur.channel prerelease") || !strings.Contains(err.Error(), "git config --unset murmur.channel") {
		t.Fatalf("Back: %v", err)
	}
	if ch, skip := globalValues(path, "murmur.channel"), globalValues(path, "murmur.updateSkip"); ch != "prerelease" || skip != "" {
		t.Fatalf("a refused Back wrote: channel %q, hold %q", ch, skip)
	}
	if _, err := Back(ctx, noEnv, gitDir, "0.5.5-nightly.20261124"); err != nil {
		t.Fatalf("Back from a nightly whose stable knows the hold: %v", err)
	}
	gitConfig(t, "--file", path, "--unset-all", "murmur.updateSkip")
	gitConfig(t, "--file", path, "murmur.channel", "prerelease")

	// Another clone of this machine's, which one binary serves too.
	gitConfig(t, "--file", filepath.Join(gitDir, "config"), "--unset-all", "murmur.channel")
	other := repo(t)
	gitConfig(t, "--file", filepath.Join(other, "config"), "murmur.channel", "prerelease")
	dir := t.TempDir()
	t.Setenv(clones.DirEnv, dir)
	if err := clones.Add(dir, clones.Clone{GitDir: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := Back(ctx, noEnv, gitDir, bad); err == nil || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), "take it out there") {
		t.Fatalf("Back with another clone's prerelease: %v", err)
	}
	if ch, skip := globalValues(path, "murmur.channel"), globalValues(path, "murmur.updateSkip"); ch != "prerelease" || skip != "" {
		t.Fatalf("a refused Back wrote: channel %q, hold %q", ch, skip)
	}

	// An include of the global file, which shows only once the file itself is held: refused, and
	// both writes taken out again.
	t.Setenv(clones.DirEnv, t.TempDir())
	included := filepath.Join(t.TempDir(), "more")
	gitConfig(t, "--file", included, "murmur.channel", "prerelease")
	gitConfig(t, "--file", path, "--unset-all", "murmur.channel")
	gitConfig(t, "--file", path, "murmur.channel", "prerelease")
	gitConfig(t, "--file", path, "include.path", included)
	if _, err := Back(ctx, noEnv, gitDir, bad); err == nil || !strings.Contains(err.Error(), "another global git config file") {
		t.Fatalf("Back with an include: %v", err)
	}
	if ch, skip := globalValues(path, "murmur.channel"), globalValues(path, "murmur.updateSkip"); ch != "prerelease" || skip != "" {
		t.Fatalf("a refused Back wrote: channel %q, hold %q", ch, skip)
	}
	// With the channel in the include only, the refused Back leaves none in the file itself.
	gitConfig(t, "--file", path, "--unset-all", "murmur.channel")
	if _, err := Back(ctx, noEnv, gitDir, bad); err == nil {
		t.Fatal("Back with the channel in an include only went ahead")
	}
	if out, _ := exec.Command("git", "config", "--file", path, "--no-includes", "--get-all", "murmur.channel").Output(); len(out) != 0 {
		t.Fatalf("a refused Back left the channel %q in the file", out)
	}
}

// An include of the global file that says prerelease, made after Back, is found as the updater
// goes back: it holds the file first, as Back did, and then looks (Opus review).
func TestGoingBackRefusesAnIncludeMadeSince(t *testing.T) {
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
	included := filepath.Join(t.TempDir(), "more")
	gitConfig(t, "--file", included, "murmur.channel", "prerelease")
	gitConfig(t, "--file", path, "include.path", included)
	srv := releases(t, "v0.5.3", []rel{{"v" + bad, true}, {"v0.5.3", false}})
	exe := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(exe, []byte("old"), 0o755)
	original, _ := Identity(exe)
	o := Options{Version: bad, GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv, Log: log.New(io.Discard, "", 0)}
	done, _, err := attempt(ctx, o, original)
	if b, _ := os.ReadFile(exe); done || string(b) != "old" || err == nil || !strings.Contains(err.Error(), "another global git config file") {
		t.Fatalf("installed %q, err %v", b, err)
	}
	if s := Read(ctx, noEnv, gitDir, bad); !strings.HasPrefix(s.Hold, "Can't go back for good: another global git config file") {
		t.Errorf("hold %q", s.Hold)
	}
	// The global file itself saying prerelease again, with no include, is no reason: the updater
	// holds it again before it goes back.
	gitConfig(t, "--file", path, "--unset-all", "include.path")
	gitConfig(t, "--file", path, "--replace-all", "murmur.channel", "prerelease")
	if s := Read(ctx, noEnv, gitDir, bad); s.Hold != "Going back to the latest stable release." {
		t.Errorf("hold with the file's own prerelease: %q", s.Hold)
	}
}

// An override made after Back, before the release gone back to installs, is caught there: the
// updater doesn't go back to a release that would install the nightly again, writes no record, and
// the page says why (#1212).
func TestGoingBackRefusesAnOverrideMadeSince(t *testing.T) {
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
	gitConfig(t, "--file", filepath.Join(gitDir, "config"), "murmur.channel", "prerelease")
	srv := releases(t, "v0.5.3", []rel{{"v" + bad, true}, {"v0.5.3", false}})
	exe := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(exe, []byte("old"), 0o755)
	original, _ := Identity(exe)
	o := Options{Version: bad, GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv, Log: log.New(io.Discard, "", 0)}
	var downloads int
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/binary/") {
			downloads++
		}
		inner.ServeHTTP(w, r)
	})
	done, _, err := attempt(ctx, o, original)
	if b, _ := os.ReadFile(exe); done || string(b) != "old" || err == nil || !strings.Contains(err.Error(), "this repo's git config") {
		t.Fatalf("installed %q, err %v", b, err)
	}
	if downloads != 0 {
		t.Errorf("downloaded %d times for a going back it refuses", downloads)
	}
	if rec := globalValues(path, "murmur.updateBack"); rec != "" {
		t.Errorf("recorded %q", rec)
	}
	if s := Read(ctx, noEnv, gitDir, bad); !strings.HasPrefix(s.Hold, "Can't go back for good: this repo's git config") {
		t.Errorf("hold %q", s.Hold)
	}
}

// The record of going back is in place before the swap, so a kill just after the swap can't lose
// it (#1212), and a swap that fails takes it out again.
func TestGoingBackIsRecordedBeforeTheSwap(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	const bad = "0.5.4-nightly.20261123"
	if _, err := Back(ctx, noEnv, gitDir, bad); err != nil {
		t.Fatal(err)
	}
	srv := releases(t, "v0.5.3", []rel{{"v" + bad, true}, {"v0.5.3", false}})
	exe := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(exe, []byte("old"), 0o755)
	original, _ := Identity(exe)
	was := osFiles
	t.Cleanup(func() { osFiles = was })
	var during string
	osFiles = files{rename: func(_, _ string) error {
		during = globalValues(path, "murmur.updateBack")
		return errors.New("the swap failed")
	}, link: os.Link}
	o := Options{Version: bad, GitDir: gitDir, Executable: exe, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Getenv: noEnv, Log: log.New(io.Discard, "", 0)}
	if done, _, err := attempt(ctx, o, original); done || err == nil {
		t.Fatalf("done %v, err %v", done, err)
	}
	if during != bad+" "+exe {
		t.Errorf("record during the swap: %q", during)
	}
	if after := globalValues(path, "murmur.updateBack"); after != "" {
		t.Errorf("record after a failed swap: %q", after)
	}
	osFiles = was
	if got := installedAt(t, srv, gitDir, bad, exe); got != "0.5.3" {
		t.Fatalf("the next check went back to %q", got)
	}
	if rec := globalValues(path, "murmur.updateBack"); rec != bad+" "+exe {
		t.Errorf("record after going back: %q", rec)
	}
}
