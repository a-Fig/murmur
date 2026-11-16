package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/a-Fig/murmur/internal/web"
)

// Sample activity for the Recent view (#448): what a daemon would have seen of the fixture
// sessions over the last few hours, and what GitHub would say of the team's repository.

func fixtureActivity(now time.Time) []web.Activity {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	acts := []web.Activity{
		{Kind: web.ActStarted, At: ago(3 * time.Hour), Who: "tidy-badger", Agent: "claude-code", Branch: "auth-flow"},
		{Kind: web.ActStatus, At: ago(170 * time.Minute), Who: "tidy-badger", Doing: "Reading NextAuth's GitHub provider docs"},
		{Kind: web.ActStarted, At: ago(2*time.Hour + 4*time.Minute), Who: "brave-heron", Agent: "codex", Branch: "api-rate-limit"},
		{Kind: web.ActCommits, At: ago(110 * time.Minute), Who: "mellow-yak", Branch: "db-migrations", Commits: 2, Subject: "Add the transcripts table"},
		{Kind: web.ActStatus, At: ago(100 * time.Minute), Who: "tidy-badger", Doing: "Wiring GitHub sign-in through NextAuth"},
		{Kind: web.ActStarted, At: ago(95 * time.Minute), Who: "crisp-lark", Agent: "t3", Branch: "landing-page"},
		{Kind: web.ActCommits, At: ago(85 * time.Minute), Who: "brave-heron", Branch: "api-rate-limit", Commits: 1, Subject: "Count requests per user in Redis"},
		{Kind: web.ActCommits, At: ago(75 * time.Minute), Who: "brave-heron", Branch: "api-rate-limit", Commits: 2, Subject: "Answer 429 with Retry-After"},
		{Kind: web.ActStarted, At: ago(52 * time.Minute), Who: "quiet-otter", Agent: "claude-code", Branch: "demo-polish"},
		{Kind: web.ActTags, At: ago(50 * time.Minute), Who: "quiet-otter", Added: []string{"group:demo"}},
		{Kind: web.ActStopped, At: ago(47 * time.Minute), Who: "mellow-lark"},
		{Kind: web.ActStarted, At: ago(45 * time.Minute), Who: "bold-crane", Agent: "codex", Branch: "landing-page"},
		{Kind: web.ActCommits, At: ago(44 * time.Minute), Who: "crisp-lark", Branch: "landing-page", Commits: 1, Subject: "Write the hero and the feature cards"},
		{Kind: web.ActStarted, At: ago(40 * time.Minute), Who: "rapid-ibis", Agent: "claude-code", Branch: "api-rate-limit"},
		{Kind: web.ActTags, At: ago(31 * time.Minute), Who: "tidy-badger", Added: []string{"reviewing"}, Removed: []string{"do-not-disturb"}},
		{Kind: web.ActStatus, At: ago(24 * time.Minute), Who: "rapid-ibis", Doing: "Load-testing /api/summarize at 50 requests a second"},
		// Issues sessions' hooks saw them close or file (#510), which GitHub's list gives only a login
		// for; it hasn't listed #22 yet.
		{Kind: web.ActIssue, At: ago(17 * time.Minute), Who: "rapid-ibis", Repo: "example/huddle", Issue: 15, What: web.GHClosed},
		{Kind: web.ActCommits, At: ago(12 * time.Minute), Who: "quiet-otter", Branch: "demo-polish", Commits: 3, Subject: "Show a skeleton while meetings load"},
		{Kind: web.ActStatus, At: ago(6 * time.Minute), Who: "quiet-otter", Doing: "Adding empty states and a loading skeleton to the dashboard"},
		{Kind: web.ActIssue, At: ago(4 * time.Minute), Who: "quiet-otter", Repo: "example/huddle", Issue: 21, What: web.GHOpened},
		{Kind: web.ActIssue, At: ago(1 * time.Minute), Who: "brave-heron", Repo: "example/huddle", Issue: 22, What: web.GHOpened},
	}
	for i := range acts {
		acts[i].ID = "act-" + strconv.Itoa(i+1)
	}
	return acts
}

// fixtureGitHub answers as gh would, a little slowly.
func fixtureGitHub(now time.Time) web.LookupGitHub {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	ev := func(kind, what string, at time.Time, n int, title, branch, author string) web.GitHubEvent {
		url := "https://github.com/example/huddle/pull/" + strconv.Itoa(n)
		if kind == web.GHIssue {
			url = "https://github.com/example/huddle/issues/" + strconv.Itoa(n)
		}
		return web.GitHubEvent{ID: "gh-" + kind + "-" + strconv.Itoa(n) + "-" + what, Kind: kind, What: what, At: at, Number: n,
			Title: title, URL: url, Author: author, Branch: branch}
	}
	events := []web.GitHubEvent{
		ev(web.GHIssue, web.GHOpened, ago(150*time.Minute), 15, "Summaries time out on hour-long meetings", "", "jordan"),
		ev(web.GHPullRequest, web.GHOpened, ago(140*time.Minute), 12, "Sign in with GitHub", "auth-flow", "maya"),
		ev(web.GHPullRequest, web.GHOpened, ago(74*time.Minute), 17, "Rate-limit /api/summarize per user", "api-rate-limit", "tyler"),
		ev(web.GHPullRequest, web.GHMerged, ago(18*time.Minute), 17, "Rate-limit /api/summarize per user", "api-rate-limit", "tyler"),
		ev(web.GHIssue, web.GHClosed, ago(17*time.Minute), 15, "Summaries time out on hour-long meetings", "", "jordan"),
		ev(web.GHPullRequest, web.GHOpened, ago(10*time.Minute), 19, "Empty states and a loading skeleton", "demo-polish", "tyler"),
		ev(web.GHIssue, web.GHOpened, ago(4*time.Minute), 21, "Dates on meeting cards are in UTC", "", "sam"),
	}
	return func(ctx context.Context) ([]web.GitHubEvent, error) {
		select {
		case <-time.After(400 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return events, nil
	}
}

// Activity uses the count of activities seen as its cursor, as Messages does.
func (f *fixtures) Activity(ctx context.Context, cursor string) ([]web.Activity, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	from := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(f.activity) {
			return nil, "", errors.New("bad cursor")
		}
		from = n
	}
	return append([]web.Activity(nil), f.activity[from:]...), strconv.Itoa(len(f.activity)), nil
}
