package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// releasesServer answers GitHub's releases API with tag as the latest stable release and the only
// release, or with 503 while fail is set, and counts the asks.
func releasesServer(t *testing.T, tag string) (srv *httptest.Server, asks *atomic.Int32, fail *atomic.Bool) {
	return slowReleasesServer(t, tag, 0)
}

// slowReleasesServer is releasesServer, each answer taking delay.
func slowReleasesServer(t *testing.T, tag string, delay time.Duration) (srv *httptest.Server, asks *atomic.Int32, fail *atomic.Bool) {
	asks, fail = &atomic.Int32{}, &atomic.Bool{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		time.Sleep(delay)
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		body := `{"tag_name":"` + tag + `","assets":[]}`
		if !strings.HasSuffix(r.URL.Path, "/latest") {
			body = "[" + body + "]"
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, asks, fail
}

// The first daemon to check asks GitHub and records the answer; the others take it for their hour,
// a failed ask included. A person's check asks again, and so does a check on the other channel or
// after the hour (#1177).
func TestOneAskOfGitHubPerMachine(t *testing.T) {
	t.Parallel()
	srv, asks, fail := releasesServer(t, "v1.2.3")
	shared := t.TempDir()
	o := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: shared}
	ctx := context.Background()
	ask := func(o Options, prerelease bool, want int32) {
		t.Helper()
		rel, err := pick(ctx, o, prerelease)
		if err != nil || rel == nil || rel.Tag != "v1.2.3" || rel.checked.IsZero() {
			t.Fatalf("pick: %+v, %v", rel, err)
		}
		if got := asks.Load(); got != want {
			t.Fatalf("%d asks of GitHub, want %d", got, want)
		}
	}
	ask(o, false, 1)
	ask(o, false, 1) // another daemon, within the hour
	fresh := o
	fresh.fresh = true
	ask(fresh, false, 2)
	ask(o, true, 3) // the prerelease channel's answer is its own
	ask(o, true, 3)
	soon := o
	soon.Every = time.Nanosecond // this daemon's hour has passed
	ask(soon, false, 4)

	fail.Store(true)
	if _, err := pick(ctx, fresh, false); err == nil {
		t.Fatal("a failed ask said nothing")
	}
	if _, err := pick(ctx, o, false); err == nil || asks.Load() != 5 {
		t.Fatalf("the failure wasn't taken from the record: %v, %d asks", err, asks.Load())
	}
	alone := o
	alone.Shared = ""
	fail.Store(false)
	ask(alone, false, 6) // with no shared directory, every check asks
	ask(alone, false, 7)
}

// A daemon that sees another's answer recorded checks within seconds, taking it rather than asking
// GitHub, so a setting changed on one clone's page reaches every clone (#1177).
func TestAnotherDaemonsAnswerAsksForACheck(t *testing.T) {
	was := checkGap
	checkGap = 100 * time.Millisecond
	t.Cleanup(func() { checkGap = was })
	srv, asks, _ := releasesServer(t, "v1.2.3")
	shared := t.TempDir()
	path := filepath.Join(t.TempDir(), "mur")
	if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var mu sync.Mutex
	reports := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(),
			Every: time.Hour, Watch: 20 * time.Millisecond, Shared: shared, Getenv: func(string) string { return "" },
			Report: func(Status) { mu.Lock(); reports++; mu.Unlock() }}, func() { t.Error("restarted") })
	}()
	defer func() { cancel(); <-done }()
	count := func() int { mu.Lock(); defer mu.Unlock(); return reports }
	wait := func(what string, cond func() bool) {
		t.Helper()
		for !cond() {
			if ctx.Err() != nil {
				t.Fatal("waited too long for " + what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait("the first check", func() bool { return count() == 1 })
	if asks.Load() != 1 {
		t.Fatalf("%d asks at the first check", asks.Load())
	}
	time.Sleep(2 * checkGap) // a check now comes at once
	other := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: shared, fresh: true}
	if _, err := pick(ctx, other, false); err != nil {
		t.Fatal(err)
	}
	wait("a check after the other daemon's answer", func() bool { return count() == 2 })
	if asks.Load() != 2 {
		t.Errorf("%d asks of GitHub, want the other daemon's only", asks.Load())
	}
}

// About's "Checked" is when GitHub gave the answer, which another daemon may have asked for.
func TestCheckedIsWhenGitHubAnswered(t *testing.T) {
	t.Parallel()
	gitDir := repo(t)
	at := time.Now().Add(-20 * time.Minute).Truncate(time.Second)
	recordRelease(gitDir, &release{Tag: "v0.3.0", checked: at})
	if got := Checked(gitDir); !got.Equal(at) {
		t.Errorf("checked %v, want %v", got, at)
	}
}

// Daemons that start together check together: the ones that find the lock held wait for the first
// one's answer rather than asking GitHub each (#1177).
func TestOverlappingChecksAskGitHubOnce(t *testing.T) {
	t.Parallel()
	srv, asks, _ := slowReleasesServer(t, "v1.2.3", 300*time.Millisecond)
	o := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: t.TempDir()}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, err := pick(context.Background(), o, false); err != nil || rel == nil || rel.Tag != "v1.2.3" {
				t.Errorf("pick: %+v, %v", rel, err)
			}
		}()
	}
	wg.Wait()
	if n := asks.Load(); n != 1 {
		t.Errorf("five overlapping checks asked GitHub %d times, want once", n)
	}
}

// A failed ask is another daemon's answer for a few minutes, not the hour: a blip at GitHub
// doesn't stop every clone's updates until the next hour (#1177).
func TestARecordedFailureIsShortLived(t *testing.T) {
	t.Parallel()
	srv, asks, _ := releasesServer(t, "v1.2.3")
	shared := t.TempDir()
	o := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: shared}
	for _, c := range []struct {
		age  time.Duration
		asks int32
	}{{time.Minute, 0}, {sharedFailLife + time.Minute, 1}} {
		writeAnswers(shared, answers{Answers: map[string]answer{srv.URL + " stable": {At: time.Now().Add(-c.age).UTC(), Error: "503"}}})
		_, err := pick(context.Background(), o, false)
		if got := asks.Load(); got != c.asks {
			t.Errorf("a failure %v old: %d asks of GitHub, want %d", c.age, got, c.asks)
		}
		if (err != nil) != (c.asks == 0) {
			t.Errorf("a failure %v old: %v", c.age, err)
		}
	}
}

// Whoever can write the record could name another binary to download: a release whose assets
// aren't GitHub's own downloads of it is no answer, and GitHub is asked instead (#1177).
func TestARecordedReleaseDownloadsOnlyFromItsRelease(t *testing.T) {
	t.Parallel()
	const base = "https://github.com/a-Fig/murmur/releases/download/v1.2.3/"
	rel := func(tag string, urls ...string) *release {
		r := &release{Tag: tag}
		for _, u := range urls {
			name := u[strings.LastIndex(u, "/")+1:]
			r.Assets = append(r.Assets, struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			}{name, u})
		}
		return r
	}
	for _, c := range []struct {
		api  string
		rel  *release
		want bool
	}{
		{githubReleases, rel("v1.2.3", base+"mur-linux-amd64", base+"SHA256SUMS"), true},
		{githubReleases, rel("v1.2.3"), true},
		{githubReleases, rel("v1.2.3", "https://example.com/mur-linux-amd64"), false},
		{githubReleases, rel("v1.2.3", "https://github.com/a-Fig/murmur/releases/download/v1.2.4/mur-linux-amd64"), false},
		{githubReleases, rel("v1.2.3", "https://github.com/someone/murmur/releases/download/v1.2.3/mur-linux-amd64"), false},
		{githubReleases, rel("v1.2.3", "http://github.com/a-Fig/murmur/releases/download/v1.2.3/mur-linux-amd64"), false},
		{githubReleases, rel("../x", "https://github.com/a-Fig/murmur/releases/download/../x/mur-linux-amd64"), false},
		{"http://127.0.0.1:5555/releases", rel("v1.2.3", "http://127.0.0.1:5555/binary/mur-linux-amd64"), true},
		{"http://127.0.0.1:5555/releases", rel("v1.2.3", "http://127.0.0.1:6666/binary/mur-linux-amd64"), false},
	} {
		if got := recordedAssetsOK(c.api, c.rel); got != c.want {
			t.Errorf("%s %+v: %v, want %v", c.api, c.rel, got, c.want)
		}
	}

	srv, asks, _ := releasesServer(t, "v1.2.3")
	shared := t.TempDir()
	writeAnswers(shared, answers{Answers: map[string]answer{srv.URL + " stable": {At: time.Now().UTC(),
		Release: rel("v1.2.3", "https://example.com/mur-linux-amd64")}}})
	o := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: shared}
	if r, err := pick(context.Background(), o, false); err != nil || r == nil || len(r.Assets) != 0 || asks.Load() != 1 {
		t.Errorf("took a record that downloads from elsewhere: %+v, %v, %d asks", r, err, asks.Load())
	}
}

// A daemon's own write of the record isn't news to it, but another's, even one made while it was
// asking GitHub, still is (Run checks again for it).
func TestAnotherDaemonsWriteDuringACheckIsSeen(t *testing.T) {
	t.Parallel()
	srv, _, _ := releasesServer(t, "v1.2.3")
	shared := t.TempDir()
	var seen int64
	o := Options{Version: "1.2.3", URL: srv.URL, Client: srv.Client(), Every: time.Hour, Shared: shared, fresh: true, seen: &seen}
	if _, err := pick(context.Background(), o, false); err != nil {
		t.Fatal(err)
	}
	if seen != 1 || sharedSeq(shared) != 1 {
		t.Fatalf("after its own write: seen %d, record %d", seen, sharedSeq(shared))
	}
	other := o
	other.seen = nil
	if _, err := pick(context.Background(), other, true); err != nil { // another daemon
		t.Fatal(err)
	}
	if _, err := pick(context.Background(), o, false); err != nil {
		t.Fatal(err)
	}
	if seen != 1 || sharedSeq(shared) != 3 {
		t.Errorf("after another's write and its own: seen %d, record %d; want 1, 3", seen, sharedSeq(shared))
	}
}
