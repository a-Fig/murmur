package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// global gives the test a global git config of its own, with one unrelated key in it, so that
// nothing here can read or write the real user's: HOME, XDG_CONFIG_HOME and GIT_CONFIG_GLOBAL all
// point into a temporary directory.
func global(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	path := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(path, []byte("[user]\n\tname = Ada\n[murmur]\n\tother = kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	return path
}

func gitConfig(t *testing.T, args ...string) {
	t.Helper()
	if b, err := exec.Command("git", append([]string{"config"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git config %v: %s: %v", args, b, err)
	}
}

func noEnv(string) string { return "" }

// Read says what the global git config chooses and what applies in the clone, and why they
// differ when they do, and what it says applies is what the updater does (Off, Prerelease).
func TestReadSetting(t *testing.T) {
	for _, c := range []struct {
		name            string
		global, local   [][2]string
		env             string
		choice, applies string
		why             string
	}{
		{name: "nothing set", choice: "on", applies: "on"},
		{name: "off", global: [][2]string{{"murmur.update", "false"}}, choice: "off", applies: "off"},
		{name: "off as git spells a bool", global: [][2]string{{"murmur.update", "no"}}, choice: "off", applies: "off"},
		{name: "nightly", global: [][2]string{{"murmur.channel", "prerelease"}}, choice: "nightly", applies: "nightly"},
		{name: "off beats a channel", global: [][2]string{{"murmur.update", "false"}, {"murmur.channel", "prerelease"}}, choice: "off", applies: "off"},
		{name: "the repo turns it off", local: [][2]string{{"murmur.update", "false"}}, choice: "on", applies: "off",
			why: "Off here: this repo's git config sets murmur.update."},
		{name: "the repo turns it on", global: [][2]string{{"murmur.update", "false"}}, local: [][2]string{{"murmur.update", "true"}}, choice: "off", applies: "on",
			why: "On here: this repo's git config sets murmur.update."},
		{name: "the repo takes nightlies", local: [][2]string{{"murmur.channel", "prerelease"}}, choice: "on", applies: "nightly",
			why: "Nightly here: this repo's git config sets murmur.channel."},
		{name: "the repo stays stable", global: [][2]string{{"murmur.channel", "prerelease"}}, local: [][2]string{{"murmur.channel", "stable"}}, choice: "nightly", applies: "on",
			why: "On here: this repo's git config sets murmur.channel."},
		{name: "the repo agrees", global: [][2]string{{"murmur.update", "false"}}, local: [][2]string{{"murmur.update", "false"}}, choice: "off", applies: "off"},
		{name: "MURMUR_NO_UPDATE", env: "1", global: [][2]string{{"murmur.channel", "prerelease"}}, choice: "nightly", applies: "off",
			why: "Off here: MURMUR_NO_UPDATE is set."},
		{name: "MURMUR_NO_UPDATE=0", env: "0", choice: "on", applies: "on"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := global(t)
			gitDir := repo(t)
			for _, kv := range c.global {
				gitConfig(t, "--file", path, kv[0], kv[1])
			}
			for _, kv := range c.local {
				gitConfig(t, "--file", filepath.Join(gitDir, "config"), kv[0], kv[1])
			}
			getenv := func(k string) string {
				if k == "MURMUR_NO_UPDATE" {
					return c.env
				}
				return ""
			}
			ctx := context.Background()
			s := Read(ctx, getenv, gitDir, "1.2.3")
			if s.Choice != c.choice || s.Effective != c.applies || s.Why != c.why {
				t.Fatalf("got %+v, want choice %s, applies %s, why %q", s, c.choice, c.applies, c.why)
			}
			if off := Off(ctx, getenv, gitDir); off != (s.Effective == ChoiceOff) {
				t.Errorf("Off says %v where Read says %s", off, s.Effective)
			}
			if pre := Prerelease(ctx, gitDir); !Off(ctx, getenv, gitDir) && pre != (s.Effective == ChoiceNightly) {
				t.Errorf("Prerelease says %v where Read says %s", pre, s.Effective)
			}
		})
	}
}

// Set writes the global git config's murmur.update and murmur.channel, and nothing else: not
// another key, and not the repo's own config, whose override Read still reports.
func TestSetSetting(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	gitConfig(t, "--file", filepath.Join(gitDir, "config"), "murmur.channel", "prerelease")
	localBefore, _ := os.ReadFile(filepath.Join(gitDir, "config"))
	ctx := context.Background()
	for _, choice := range []string{ChoiceOff, ChoiceNightly, ChoiceOn, ChoiceOff, ChoiceOn} {
		s, err := Set(ctx, noEnv, gitDir, "1.2.3", choice)
		if err != nil {
			t.Fatalf("%s: %v", choice, err)
		}
		if s.Choice != choice {
			t.Fatalf("set %s, read %+v", choice, s)
		}
		b, _ := os.ReadFile(path)
		text := string(b)
		if !strings.Contains(text, "name = Ada") || !strings.Contains(text, "other = kept") {
			t.Fatalf("%s took another key with it:\n%s", choice, text)
		}
		wantUpdate, wantChannel := choice == ChoiceOff, choice == ChoiceNightly
		if strings.Contains(text, "update = false") != wantUpdate || strings.Contains(text, "channel = prerelease") != wantChannel {
			t.Fatalf("%s wrote:\n%s", choice, text)
		}
		if choice == ChoiceOn && s.Why != "Nightly here: this repo's git config sets murmur.channel." {
			t.Errorf("the repo's override went unsaid: %+v", s)
		}
	}
	if localAfter, _ := os.ReadFile(filepath.Join(gitDir, "config")); string(localAfter) != string(localBefore) {
		t.Fatalf("the repo's config changed:\n%s", localAfter)
	}
	before, _ := os.ReadFile(path)
	for _, bad := range []string{"", "Off", "stable", "true", "nightly\n", "core.hooksPath"} {
		if _, err := Set(ctx, noEnv, gitDir, "1.2.3", bad); err == nil {
			t.Errorf("Set took %q", bad)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("a refused choice wrote:\n%s", after)
	}
}

// A choice another global file overrides is reported as not made, not as done.
func TestSetSettingOverriddenByAnInclude(t *testing.T) {
	path := global(t)
	included := filepath.Join(t.TempDir(), "more")
	gitConfig(t, "--file", included, "murmur.update", "false")
	gitConfig(t, "--file", path, "include.path", included)
	if s, err := Set(context.Background(), noEnv, repo(t), "1.2.3", ChoiceOn); err == nil || s.Choice != ChoiceOff {
		t.Fatalf("got %+v, %v", s, err)
	}
}

// Going back to stable is offered only from a prerelease, with updates on, and holds updates until
// a newer release than that prerelease, which the page and `mur updates` say.
func TestBack(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	const nightly = "1.2.5-nightly.20261101"
	for _, v := range []string{"1.2.4", "1.2.4-dev", "0.1.0-dev"} {
		if s := Read(ctx, noEnv, gitDir, v); s.CanGoBack {
			t.Errorf("offered going back from %s", v)
		}
		if _, err := Back(ctx, noEnv, gitDir, v); err == nil {
			t.Errorf("went back from %s", v)
		}
	}
	gitConfig(t, "--file", path, "murmur.update", "false")
	if s := Read(ctx, noEnv, gitDir, nightly); s.CanGoBack {
		t.Error("offered going back with updates off")
	}
	if _, err := Back(ctx, noEnv, gitDir, nightly); err == nil {
		t.Error("went back with updates off, where nothing would install")
	}
	if _, err := Set(ctx, noEnv, gitDir, nightly, ChoiceNightly); err != nil {
		t.Fatal(err)
	}
	if s := Read(ctx, noEnv, gitDir, nightly); !s.CanGoBack || s.Hold != "" {
		t.Fatalf("before going back: %+v", s)
	}
	s, err := Back(ctx, noEnv, gitDir, nightly)
	if err != nil || s.CanGoBack || s.Skip != nightly || s.Hold != "Going back to the latest stable release." {
		t.Fatalf("going back: %+v, %v", s, err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "updateSkip = "+nightly) {
		t.Fatalf("global config:\n%s", b)
	}
	if s := Read(ctx, noEnv, gitDir, "1.2.4"); s.Hold != "On stable until the next nightly after "+nightly+"." {
		t.Errorf("on nightly: %q", s.Hold)
	}
	if _, err := Set(ctx, noEnv, gitDir, "1.2.4", ChoiceOn); err != nil {
		t.Fatal(err)
	}
	if s := Read(ctx, noEnv, gitDir, "1.2.4"); s.Hold != "On stable until the next stable release after "+nightly+"." || s.Skip != nightly {
		t.Errorf("on stable: %+v", s)
	}
	if s := Read(ctx, noEnv, gitDir, "1.2.5"); s.Hold != "" || s.Skip != "" {
		t.Errorf("a newer release still held: %+v", s)
	}
}

type rel struct {
	tag string
	pre bool
}

// releases serves a release list as GitHub's API does, the latest stable release apart, with each
// release's binary reading "binary <tag>".
func releases(t *testing.T, latest string, list []rel) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	assets := func(tag string) []map[string]string {
		return []map[string]string{{"name": "mur-linux-amd64", "browser_download_url": srv.URL + "/binary/" + tag}, {"name": "SHA256SUMS", "browser_download_url": srv.URL + "/sums/" + tag}}
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": latest, "assets": assets(latest)})
		case r.URL.Path == "/":
			out := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for _, x := range list {
					out = append(out, map[string]any{"tag_name": x.tag, "prerelease": x.pre, "assets": assets(x.tag)})
				}
			}
			json.NewEncoder(w).Encode(out)
		case strings.HasPrefix(r.URL.Path, "/sums/"):
			fmt.Fprintf(w, "%x  mur-linux-amd64\n", sha256.Sum256([]byte("binary "+strings.TrimPrefix(r.URL.Path, "/sums/"))))
		case strings.HasPrefix(r.URL.Path, "/binary/"):
			fmt.Fprint(w, "binary "+strings.TrimPrefix(r.URL.Path, "/binary/"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// installed runs one check of the updater's on a binary of running, with the clone's config as
// it is, and returns the version it installed, "" for none.
func installed(t *testing.T, srv *httptest.Server, gitDir, running string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(path, []byte("old"), 0o755)
	original, _ := Identity(path)
	o := Options{Version: running, GitDir: gitDir, Executable: path, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(), Log: log.New(io.Discard, "", 0)}
	done, _, err := attempt(context.Background(), o, original)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		return ""
	}
	b, _ := os.ReadFile(path)
	return strings.TrimPrefix(string(b), "binary v")
}

// The updater does what each choice says: nothing when off, the latest stable release when on,
// and the newest prerelease or nightly when on nightly. Each is read at the check, so a change
// needs no restart.
func TestUpdaterObeysEachChoice(t *testing.T) {
	global(t)
	gitDir := repo(t)
	srv := releases(t, "v1.2.4", []rel{{"v1.2.5-nightly.20261108", true}, {"v1.2.4", false}})
	ctx := context.Background()
	for _, c := range []struct{ choice, want string }{{ChoiceOff, ""}, {ChoiceOn, "1.2.4"}, {ChoiceNightly, "1.2.5-nightly.20261108"}} {
		if _, err := Set(ctx, noEnv, gitDir, "1.2.3", c.choice); err != nil {
			t.Fatal(err)
		}
		var reports []Status
		path := filepath.Join(t.TempDir(), "mur")
		os.WriteFile(path, []byte("old"), 0o755)
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		restarted := false
		Run(ctx, Options{Version: "1.2.3", GitDir: gitDir, Executable: path, URL: srv.URL, OS: "linux", Arch: "amd64", Client: srv.Client(),
			Getenv: noEnv, Every: time.Hour, Report: func(s Status) { reports = append(reports, s); cancel() }}, func() { restarted = true })
		cancel()
		b, _ := os.ReadFile(path)
		got := strings.TrimPrefix(string(b), "binary v")
		if c.want == "" {
			if restarted || string(b) != "old" || len(reports) != 1 || !reports[0].Off {
				t.Errorf("off: installed %q, reports %+v", b, reports)
			}
			continue
		}
		if !restarted || got != c.want {
			t.Errorf("%s: installed %q, want %s", c.choice, got, c.want)
		}
	}
}

// Going back to stable installs the latest stable release in place of the prerelease, older as it
// is, and never one older than that. Then each channel holds until a release newer than the
// prerelease, and the hold is taken off once one is in place.
func TestBackAndHold(t *testing.T) {
	path := global(t)
	gitDir := repo(t)
	ctx := context.Background()
	const bad = "1.2.5-nightly.20261101"
	held := func() string {
		out, _ := exec.Command("git", "config", "--file", path, "--get", "murmur.updateSkip").Output()
		return strings.TrimSpace(string(out))
	}
	if _, err := Set(ctx, noEnv, gitDir, bad, ChoiceNightly); err != nil {
		t.Fatal(err)
	}
	if _, err := Back(ctx, noEnv, gitDir, bad); err != nil {
		t.Fatal(err)
	}
	list := []rel{{"v" + bad, true}, {"v1.2.4", false}, {"v1.2.3", false}}
	// The downgrade, on the nightly channel: the stable release, not the nightly again or an
	// older stable.
	if got := installed(t, releases(t, "v1.2.4", list), gitDir, bad); got != "1.2.4" {
		t.Fatalf("going back installed %q", got)
	}
	if held() != bad {
		t.Fatal("the hold went with the downgrade")
	}
	// Held on nightly: the same nightly isn't installed again.
	if got := installed(t, releases(t, "v1.2.4", list), gitDir, "1.2.4"); got != "" {
		t.Fatalf("held on nightly, installed %q", got)
	}
	// A daemon still running the bad build, in another clone, goes back too, and not to it.
	if got := installed(t, releases(t, "v1.2.4", list), gitDir, bad); got != "1.2.4" {
		t.Fatalf("another daemon on %s installed %q", bad, got)
	}
	// Held on stable: a stable release newer than the one running but not than the nightly waits.
	if _, err := Set(ctx, noEnv, gitDir, "1.2.4", ChoiceOn); err != nil {
		t.Fatal(err)
	}
	gitConfig(t, "--file", path, "murmur.updateSkip", "1.3.0-rc.1")
	if got := installed(t, releases(t, "v1.2.6", nil), gitDir, "1.2.4"); got != "" {
		t.Fatalf("held on stable, installed %q", got)
	}
	// The next stable after it installs, and takes the hold off.
	if got := installed(t, releases(t, "v1.3.0", nil), gitDir, "1.2.4"); got != "1.3.0" || held() != "" {
		t.Fatalf("after the hold: installed %q, hold %q", got, held())
	}
	// On nightly, the next nightly installs, and takes the hold off.
	gitConfig(t, "--file", path, "murmur.updateSkip", bad)
	if _, err := Set(ctx, noEnv, gitDir, "1.2.4", ChoiceNightly); err != nil {
		t.Fatal(err)
	}
	next := append([]rel{{"v1.2.5-nightly.20261102", true}}, list...)
	if got := installed(t, releases(t, "v1.2.4", next), gitDir, "1.2.4"); got != "1.2.5-nightly.20261102" || held() != "" {
		t.Fatalf("next nightly: installed %q, hold %q", got, held())
	}
	// A hold a newer version has passed, installed by hand, is taken off at the next check.
	gitConfig(t, "--file", path, "murmur.updateSkip", bad)
	installed(t, releases(t, "v1.2.4", list), gitDir, "1.2.6")
	if held() != "" {
		t.Fatalf("hold %q stayed under a newer version", held())
	}
}

// A changed setting asks for a check soon, not at the next hour, and checks asked for close
// together wait checkGap apart.
func TestCheckSoonAfterAChange(t *testing.T) {
	was := checkGap
	checkGap = 200 * time.Millisecond
	t.Cleanup(func() { checkGap = was })
	global(t)
	gitDir := repo(t)
	gitConfig(t, "--global", "murmur.update", "false") // so no check reaches the network
	path := filepath.Join(t.TempDir(), "mur")
	os.WriteFile(path, []byte("old"), 0o755)
	check := make(chan struct{}, 1)
	var reports atomic.Int32
	times := make(chan time.Time, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go Run(ctx, Options{Version: "1.2.3", GitDir: gitDir, Executable: path, URL: "http://127.0.0.1:1", OS: "linux", Arch: "amd64", Getenv: noEnv,
		Every: time.Hour, Check: check, Report: func(Status) { reports.Add(1); times <- time.Now() }}, func() {})
	first := <-times
	check <- struct{}{}
	second := <-times
	if gap := second.Sub(first); gap < checkGap/2 || gap > 10*time.Second {
		t.Fatalf("the asked-for check came %s after the first", gap)
	}
	time.Sleep(checkGap) // past the gap, a check comes at once
	asked := time.Now()
	check <- struct{}{}
	if third := <-times; third.Sub(asked) > 5*time.Second {
		t.Fatalf("a check asked for past the gap took %s", third.Sub(asked))
	}
}
