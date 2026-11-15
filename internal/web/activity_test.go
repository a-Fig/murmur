package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Tests of the Recent view's activity (#448): the daemon's, passed through, and GitHub's, made
// from what gh lists.

func TestActivityIsServedWithItsCursor(t *testing.T) {
	src := newFake()
	h := start(t, src, noPRs)
	got := decode[activityResponse](t, h.ok("GET", "/api/activity?cursor=abc", ""))
	src.mu.Lock()
	cursor := src.actCursor
	src.mu.Unlock()
	if cursor != "abc" || got.Cursor != "a-c1" || len(got.Activity) != 1 || got.Activity[0].Kind != ActStatus {
		t.Errorf("activity: source saw cursor %q, page got %+v", cursor, got)
	}
	if got.GitHub == nil || len(got.GitHub) != 0 {
		t.Errorf("with nothing to ask GitHub by, its events are %v, want an empty list", got.GitHub)
	}
	host := "127.0.0.1:" + h.port
	if resp := h.do("GET", "/api/activity?cursor="+strings.Repeat("x", 300), host, testToken, ""); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("long cursor: %d, want 400", resp.StatusCode)
	}
	if resp := h.do("GET", "/api/activity", host, "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without the token: %d, want 401", resp.StatusCode)
	}
}

// What gh lists becomes events: each item opened, and merged or closed if it was, oldest first. A
// title is one line, a URL that isn't https is dropped, and an item without a number is passed over.
func TestGitHubItemsBecomeEvents(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	var prs []ghItem
	if err := json.Unmarshal([]byte(`[
	 {"number":32,"title":"Remove\nclaims","url":"https://github.com/o/r/pull/32","author":{"login":"tyler"},"headRefName":"remove-claims",
	  "createdAt":"2026-11-11T10:00:00Z","mergedAt":"2026-11-11T11:00:00Z","closedAt":"2026-11-11T11:00:00Z"},
	 {"number":33,"title":"Wake ups","url":"javascript:alert(1)","author":{"login":"maya"},"headRefName":"wake-ups",
	  "createdAt":"2026-11-11T10:30:00Z","mergedAt":null,"closedAt":"2026-11-11T12:00:00Z"},
	 {"number":36,"title":"Settings","url":"https://github.com/o/r/pull/36","author":{"login":"tyler"},"headRefName":"web-page",
	  "createdAt":"2026-11-11T12:30:00Z","mergedAt":null,"closedAt":null},
	 {"number":0,"title":"junk","createdAt":"2026-11-11T12:40:00Z"}
	]`), &prs); err != nil {
		t.Fatal(err)
	}
	got := ghEvents(GHPullRequest, prs)
	type row struct {
		id, what string
		at       time.Time
	}
	want := []row{
		{"gh-pr-32-opened", GHOpened, at("2026-11-11T10:00:00Z")},
		{"gh-pr-33-opened", GHOpened, at("2026-11-11T10:30:00Z")},
		{"gh-pr-32-merged", GHMerged, at("2026-11-11T11:00:00Z")},
		{"gh-pr-33-closed", GHClosed, at("2026-11-11T12:00:00Z")},
		{"gh-pr-36-opened", GHOpened, at("2026-11-11T12:30:00Z")},
	}
	if len(got) != len(want) {
		t.Fatalf("%d events, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if g := got[i]; g.ID != w.id || g.What != w.what || !g.At.Equal(w.at) || g.Kind != GHPullRequest {
			t.Errorf("event %d is %+v, want %+v", i, g, w)
		}
	}
	if got[0].Title != "Remove claims" || got[0].Branch != "remove-claims" || got[0].Author != "tyler" || got[0].URL == "" {
		t.Errorf("the first event is %+v", got[0])
	}
	if got[1].URL != "" {
		t.Errorf("a URL that isn't https was kept: %q", got[1].URL)
	}
}

// The page's poll never waits on GitHub: the first answer has no events and starts a listing, a
// later one has them, a failed listing leaves what was known, and Close ends one in flight.
func TestGitHubEventsComeFromACache(t *testing.T) {
	var calls atomic.Int32
	fail := atomic.Bool{}
	lookup := func(ctx context.Context) ([]GitHubEvent, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("gh: not logged in")
		}
		return []GitHubEvent{{ID: "gh-pr-1-opened", Kind: GHPullRequest, What: GHOpened, Number: 1, Title: "One"}}, nil
	}
	s := NewWithGitHub(newFake(), testToken, noPRs, lookup)
	defer s.Close()
	srv := httptest.NewServer(s)
	defer srv.Close()
	get := func() activityResponse {
		t.Helper()
		req, err := http.NewRequest("GET", srv.URL+"/api/activity", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/activity: %d", resp.StatusCode)
		}
		var r activityResponse
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := get(); len(r.GitHub) != 0 {
		t.Errorf("the first answer waited for GitHub: %+v", r.GitHub)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(get().GitHub) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the listing never showed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("GitHub was asked %d times within one listing's life, want once", n)
	}
	// An old listing is redone, and one that fails leaves the last.
	fail.Store(true)
	s.gh.mu.Lock()
	s.gh.fetched = time.Now().Add(-2 * ghTTL)
	s.gh.mu.Unlock()
	get()
	for calls.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("an old listing wasn't redone")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.gh.wg.Wait()
	if r := get(); len(r.GitHub) != 1 {
		t.Errorf("a failed listing dropped what was known: %+v", r.GitHub)
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"plain":            "plain",
		"two\nlines\there": "two lines here",
		"\x00a\x1bb":       "a b",
		"":                 "",
	} {
		if got := oneLine(in, 100); got != want {
			t.Errorf("oneLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := oneLine("aé日本", 4); got != "aé" {
		t.Errorf("a cut inside a character left %q", got)
	}
}
