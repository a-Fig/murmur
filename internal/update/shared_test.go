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
	asks, fail = &atomic.Int32{}, &atomic.Bool{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
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
	go Run(ctx, Options{Version: "1.2.3", GitDir: repo(t), Executable: path, URL: srv.URL, Client: srv.Client(),
		Every: time.Hour, Watch: 20 * time.Millisecond, Shared: shared, Getenv: func(string) string { return "" },
		Report: func(Status) { mu.Lock(); reports++; mu.Unlock() }}, func() { t.Error("restarted") })
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
