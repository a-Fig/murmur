package web

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGH puts a gh first on PATH that notes each run's arguments in its own folder and prints
// answer. On Windows it is a .cmd file, as a shim or wrapper can be, so it runs under cmd.exe.
func fakeGH(t *testing.T, answer string) (calls func() []string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "answer.json"), []byte(answer), 0o644); err != nil {
		t.Fatal(err)
	}
	name, script := "gh", "#!/bin/sh\necho \"$*\" >> \"$(dirname \"$0\")/calls.txt\"\ncat \"$(dirname \"$0\")/answer.json\"\n"
	if runtime.GOOS == "windows" {
		name, script = "gh.cmd", "@echo off\r\necho %*>>\"%~dp0calls.txt\"\r\ntype \"%~dp0answer.json\"\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		b, _ := os.ReadFile(filepath.Join(bin, "calls.txt"))
		return strings.Fields(string(b))
	}
}

// GH asks once for every open pull request and sorts them by head branch. gh lists the newest
// first; of two with one head, this repository's is the branch's, and else the newer.
func TestGHListing(t *testing.T) {
	calls := fakeGH(t, `[
		{"number": 9, "isDraft": true,  "url": "https://github.com/o/r/pull/9", "headRefName": "feature/page-22", "isCrossRepository": false},
		{"number": 8, "isDraft": false, "url": "https://github.com/o/r/pull/8", "headRefName": "main", "isCrossRepository": true},
		{"number": 7, "isDraft": false, "url": "https://github.com/o/r/pull/7", "headRefName": "main", "isCrossRepository": true},
		{"number": 6, "isDraft": false, "url": "https://github.com/o/r/pull/6", "headRefName": "shared", "isCrossRepository": true},
		{"number": 5, "isDraft": false, "url": "https://github.com/o/r/pull/5", "headRefName": "shared", "isCrossRepository": false},
		{"number": 4, "isDraft": false, "url": "https://github.com/o/r/pull/4", "headRefName": "shared", "isCrossRepository": false},
		{"number": 0, "isDraft": false, "url": "", "headRefName": "nothing", "isCrossRepository": false},
		{"number": 3, "isDraft": false, "url": "https://github.com/o/r/pull/3", "headRefName": "", "isCrossRepository": false}
	]`)
	prs, err := GH(t.TempDir())(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]PR{
		"feature/page-22": {Number: 9, Draft: true, URL: "https://github.com/o/r/pull/9"},
		"main":            {Number: 8, URL: "https://github.com/o/r/pull/8"},
		"shared":          {Number: 5, URL: "https://github.com/o/r/pull/5"},
	}
	if !reflect.DeepEqual(prs, want) {
		t.Errorf("prs = %+v, want %+v", prs, want)
	}
	// Nothing from the bus is on gh's command line: a gh that is a batch file runs under cmd.exe,
	// which would read a branch named x&calc as a command.
	if got, want := strings.Join(calls(), " "), "pr list --state open --limit 300 --json number,isDraft,url,headRefName,isCrossRepository"; got != want {
		t.Errorf("gh ran with %q, want %q", got, want)
	}
}

// Closing the page's server ends the listing it started (it runs gh in the clone) before it
// returns, and a request after that starts none: nothing the page started outlives the daemon.
func TestCloseEndsLookups(t *testing.T) {
	started := make(chan struct{}, 4)
	var ended atomic.Int32
	s := NewWithPRs(newFake(), testToken, func(ctx context.Context) (map[string]PR, error) {
		started <- struct{}{}
		<-ctx.Done() // a gh that hangs until it is stopped
		ended.Add(1)
		return nil, ctx.Err()
	})
	s.prs.get([]string{"feat-1"})
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no listing started")
	}
	s.Close()
	if ended.Load() != 1 {
		t.Errorf("Close returned with %d of 1 listings ended", ended.Load())
	}
	s.prs.get([]string{"feat-2"})
	time.Sleep(100 * time.Millisecond)
	select {
	case <-started:
		t.Error("a listing started after Close")
	default:
	}
}

// A listing that fails leaves the PRs known before. One listing answers for every branch, so
// counting a failure as no PRs would take every branch's off the page at once.
func TestFailedListingKeepsWhatWasKnown(t *testing.T) {
	var fail atomic.Bool
	c := newPRCache(func(ctx context.Context) (map[string]PR, error) {
		if fail.Load() {
			return nil, errors.New("gh: offline")
		}
		return map[string]PR{"feat": {Number: 1, URL: "https://github.com/o/r/pull/1"}}, nil
	})
	defer c.close()
	list := func() { // one listing, from its start to its end
		t.Helper()
		c.mu.Lock()
		c.fetched = time.Time{} // as if the last were old
		c.mu.Unlock()
		c.get([]string{"feat"})
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			c.mu.Lock()
			done := !c.inflight && !c.fetched.IsZero()
			c.mu.Unlock()
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the listing never ended")
			}
		}
	}
	list()
	if got := c.get([]string{"feat"}); got["feat"].Number != 1 {
		t.Fatalf("after a listing: %+v", got)
	}
	fail.Store(true)
	list()
	if got := c.get([]string{"feat"}); got["feat"].Number != 1 {
		t.Errorf("after a failed listing: %+v, want what was known", got)
	}
}

// In a clone whose remote isn't on a GitHub host, gh says so (gh 2.105.0, against a Gitea, #926),
// and the page asks again hourly, not every minute: each ask started a gh for nothing.
func TestARemoteGhDoesntKnowIsAskedHourly(t *testing.T) {
	bin := t.TempDir()
	said := "none of the git remotes configured for this repository point to a known GitHub host. To tell gh about a new GitHub host, please use `gh auth login`"
	name, script := "gh", "#!/bin/sh\necho x >> \"$(dirname \"$0\")/calls.txt\"\necho '"+said+"' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		name, script = "gh.cmd", "@echo off\r\necho x>>\"%~dp0calls.txt\"\r\necho "+strings.ReplaceAll(said, "`", "")+" 1>&2\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := GH(t.TempDir())(context.Background()); !errors.Is(err, errNotGitHub) {
		t.Fatalf("pull requests: %v, want errNotGitHub", err)
	}
	if _, err := GHEvents(t.TempDir())(context.Background()); !errors.Is(err, errNotGitHub) {
		t.Fatalf("events: %v, want errNotGitHub", err)
	}

	var calls atomic.Int32
	fail := errNotGitHub
	c := newPRCache(func(ctx context.Context) (map[string]PR, error) {
		calls.Add(1)
		return nil, fail
	})
	defer c.close()
	settle := func() {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			c.mu.Lock()
			done := !c.inflight
			c.mu.Unlock()
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the listing never ended")
			}
		}
	}
	c.get([]string{"feat"})
	settle()
	c.mu.Lock()
	wait := time.Until(c.fetched.Add(prTTL))
	c.mu.Unlock()
	if wait < notGitHubWait-time.Minute || wait > notGitHubWait {
		t.Errorf("the next listing is due in %s, want about %s", wait, notGitHubWait)
	}
	c.get([]string{"feat"})
	settle()
	if n := calls.Load(); n != 1 {
		t.Errorf("%d listings, want the one", n)
	}
	// Any other failure, as offline, is asked again at the usual time.
	fail = errors.New("gh: offline")
	c.mu.Lock()
	c.fetched = time.Time{}
	c.mu.Unlock()
	c.get([]string{"feat"})
	settle()
	c.mu.Lock()
	wait = time.Until(c.fetched.Add(prTTL))
	c.mu.Unlock()
	if wait > prTTL {
		t.Errorf("after another failure the next listing is due in %s, want within %s", wait, prTTL)
	}
}
