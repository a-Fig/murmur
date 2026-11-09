package publicrepo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

func TestAddress(t *testing.T) {
	for _, url := range []string{
		"https://github.com/a-Fig/murmur.git",
		"https://github.com/a-Fig/murmur",
		"https://user:secret@GitHub.com/a-Fig/murmur.git/",
		"git@github.com:a-Fig/murmur.git",
		"ssh://git@github.com/a-Fig/murmur.git",
		"ssh://git@github.com:22/a-Fig/murmur",
		"ssh://git@ssh.github.com:443/a-Fig/murmur.git",
	} {
		if got := Address(url); got != "github.com/a-fig/murmur" {
			t.Errorf("Address(%q) = %q", url, got)
		}
	}
	for _, url := range []string{
		"https://gitlab.com/a-Fig/murmur.git",
		"https://github.com/a-Fig",
		"https://github.com/a-Fig/murmur/extra",
		`C:\repos\team.git`,
		"/srv/git/team.git",
		"",
	} {
		if got := Address(url); got != "" {
			t.Errorf("Address(%q) = %q, want none", url, got)
		}
	}
	if got := RepoKey(`C:\repos\team.git`); got != "c:/repos/team" {
		t.Errorf("a Windows path: %q", got)
	}
}

func git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remote is a bare repo standing in for github.com/<addr>; clone makes a clone whose origin is
// written as that GitHub address but reaches the bare repo, as git's insteadOf does.
func remote(t *testing.T) string {
	bare := filepath.Join(t.TempDir(), "remote.git")
	git(t, "init", "-q", "--bare", bare)
	return bare
}

func clone(t *testing.T, bare, url string) string {
	dir := t.TempDir()
	git(t, "init", "-q", dir)
	git(t, "-C", dir, "config", "remote.origin.url", url)
	git(t, "-C", dir, "config", "url."+filepath.ToSlash(bare)+".insteadOf", url)
	return filepath.Join(dir, ".git")
}

// fakes are GitHub's API and the site, counting what each was asked.
type fakes struct {
	mu        sync.Mutex
	public    bool   // whether GitHub shows the repo
	full      string // GitHub's spelling of it
	siteDown  bool
	api, site []string // paths asked of the API; bodies sent to the site
	apiURL    string
	siteURL   string
}

func newFakes(t *testing.T) *fakes {
	f := &fakes{public: true, full: "Acme/Widget"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.api = append(f.api, r.URL.Path)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("GitHub was sent credentials")
		}
		if !f.public {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"full_name":"`+f.full+`","private":false}`)
	}))
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.site = append(f.site, string(b))
		if f.siteDown {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(api.Close)
	t.Cleanup(site.Close)
	f.apiURL, f.siteURL = api.URL+"/repos/", site.URL
	return f
}

func (f *fakes) counts() (api, site int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.api), len(f.site)
}

func (f *fakes) opts(gitDir string, now time.Time) Options {
	return Options{Version: "1.2.3", GitDir: gitDir, URL: f.siteURL, API: f.apiURL, Now: func() time.Time { return now }}
}

var day1 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// A public repo is reported once, by its first clone, with its address and nothing else; every other
// clone finds the ref on the bus and asks nobody anything.
func TestPublicRepoIsReportedOnce(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	bare := remote(t)
	a := clone(t, bare, "git@github.com:acme/widget.git")
	res, err := Once(ctx, f.opts(a, day1))
	if err != nil || res != Reported {
		t.Fatalf("first clone: %v, %v", res, err)
	}
	if f.api[0] != "/repos/acme/widget" {
		t.Errorf("asked GitHub about %q", f.api[0])
	}
	if len(f.site) != 1 || f.site[0] != `{"repo":"github.com/Acme/Widget"}` {
		t.Fatalf("sent %q", f.site)
	}
	if git(t, "--git-dir", bare, "for-each-ref", "--format=%(refname)", brand.ReportedRef) != brand.ReportedRef {
		t.Fatalf("no %s on the remote", brand.ReportedRef)
	}
	if res, _ := Once(ctx, f.opts(a, day1.Add(48*time.Hour))); res != Known {
		t.Errorf("the same clone again: %v", res)
	}
	b := clone(t, bare, "https://github.com/acme/widget")
	if res, err := Once(ctx, f.opts(b, day1)); err != nil || res != Known {
		t.Errorf("second clone: %v, %v", res, err)
	}
	if api, site := f.counts(); api != 1 || site != 1 {
		t.Errorf("after the first report, GitHub was asked %d times and the site %d", api, site)
	}
}

// A private repo sends us nothing; it is looked at again the next day, not the same day.
func TestPrivateRepoSendsNothing(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	f.public = false
	gitDir := clone(t, remote(t), "https://github.com/acme/secret.git")
	for _, now := range []time.Time{day1, day1.Add(time.Hour)} {
		if res, err := Once(ctx, f.opts(gitDir, now)); err != nil || (res != Private && res != Waiting) {
			t.Fatalf("%v: %v, %v", now, res, err)
		}
	}
	if api, site := f.counts(); api != 1 || site != 0 {
		t.Fatalf("on day 1, GitHub was asked %d times and the site %d", api, site)
	}
	f.public = true // it went public
	if res, err := Once(ctx, f.opts(gitDir, day1.Add(24*time.Hour))); err != nil || res != Reported {
		t.Fatalf("the day after it went public: %v, %v", res, err)
	}
}

// A report the site didn't store is sent again at the next look, and nothing is written to the bus.
func TestUnstoredReportIsRetried(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	f.siteDown = true
	bare := remote(t)
	gitDir := clone(t, bare, "https://github.com/acme/widget.git")
	if res, err := Once(ctx, f.opts(gitDir, day1)); err == nil || res != Waiting {
		t.Fatalf("site down: %v, %v", res, err)
	}
	if out := git(t, "--git-dir", bare, "for-each-ref", brand.ReportedRef); out != "" {
		t.Fatalf("recorded on the bus a report that wasn't stored: %s", out)
	}
	f.siteDown = false
	if res, err := Once(ctx, f.opts(gitDir, day1.Add(time.Hour))); err != nil || res != Reported {
		t.Fatalf("site back: %v, %v", res, err)
	}
}

// A development build, and a repo that isn't on GitHub, ask nobody anything.
func TestNothingSent(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	gitDir := clone(t, remote(t), "https://github.com/acme/widget.git")
	o := f.opts(gitDir, day1)
	o.Version = "1.2.3-dev"
	if res, err := Once(ctx, o); err != nil || res != Dev || !res.Done() {
		t.Errorf("dev build: %v, %v", res, err)
	}
	other := clone(t, remote(t), "https://gitlab.com/acme/widget.git")
	if res, err := Once(ctx, f.opts(other, day1)); err != nil || res != NotGitHub {
		t.Errorf("gitlab: %v, %v", res, err)
	}
	if api, site := f.counts(); api != 0 || site != 0 {
		t.Errorf("GitHub was asked %d times and the site %d", api, site)
	}
}

// What join tells the agent, and init prints, makes the promise in words: the address is the only
// thing sent, only for a public repo, and why (Tyler, 2026-11-08).
func TestStatementMakesThePromise(t *testing.T) {
	for _, want := range []string{"only thing", "this repo's address", "only if the repo is public", "count how many repos and people", "private repo sends nothing"} {
		if !strings.Contains(Statement, want) {
			t.Errorf("Statement doesn't say %q:\n%s", want, Statement)
		}
	}
	for _, want := range []string{"only thing", "once per repo", "count how many repos and people"} {
		if !strings.Contains(Told, want) {
			t.Errorf("Told doesn't say %q:\n%s", want, Told)
		}
	}
}

// A report the site stored but whose ref couldn't be pushed isn't sent again: later looks push only
// the ref, and a daemon's loop keeps going until it is there (Sol's review of #239).
func TestStoredReportOnlyRetriesTheRef(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	bare := remote(t)
	// The remote refuses pushes while a file named deny is in it.
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntest -f deny && exit 1\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	deny := filepath.Join(bare, "deny")
	os.WriteFile(deny, nil, 0o644)
	gitDir := clone(t, bare, "https://github.com/acme/widget.git")
	res, err := Once(ctx, f.opts(gitDir, day1))
	if err == nil || res.Done() {
		t.Fatalf("push refused: %v, %v", res, err)
	}
	os.Remove(deny)
	if res, err := Once(ctx, f.opts(gitDir, day1.Add(time.Hour))); err != nil || res != Reported {
		t.Fatalf("push allowed: %v, %v", res, err)
	}
	if api, site := f.counts(); api != 1 || site != 1 {
		t.Errorf("GitHub was asked %d times and the site %d; want once each", api, site)
	}
	if git(t, "--git-dir", bare, "for-each-ref", "--format=%(refname)", brand.ReportedRef) != brand.ReportedRef {
		t.Fatalf("no %s on the remote", brand.ReportedRef)
	}
}

// A clone whose remote is changed to another repo, after the first was reported, reports the new
// one: what it knows is kept by address (Sol's second review of #239).
func TestChangedRemoteIsLookedAtAfresh(t *testing.T) {
	ctx := context.Background()
	f := newFakes(t)
	gitDir := clone(t, remote(t), "https://github.com/acme/widget.git")
	if res, err := Once(ctx, f.opts(gitDir, day1)); err != nil || res != Reported {
		t.Fatalf("first repo: %v, %v", res, err)
	}
	if res, _ := Once(ctx, f.opts(gitDir, day1)); res != Known {
		t.Fatalf("first repo again: %v", res)
	}
	other := remote(t)
	url := "https://github.com/acme/gadget.git"
	git(t, "--git-dir", gitDir, "config", "remote.origin.url", url)
	git(t, "--git-dir", gitDir, "config", "url."+filepath.ToSlash(other)+".insteadOf", url)
	f.full = "Acme/Gadget"
	if res, err := Once(ctx, f.opts(gitDir, day1)); err != nil || res != Reported {
		t.Fatalf("second repo: %v, %v", res, err)
	}
	if len(f.site) != 2 || f.site[1] != `{"repo":"github.com/Acme/Gadget"}` {
		t.Errorf("sent %q", f.site)
	}
}
