package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// The Recent page shows what agents have been doing, not only what they said (#448). What they
// did comes from two places. The daemon knows what the bus and its own sessions tell it: a session
// starting or stopping, its status or tags changing, the commits it made (Activity). GitHub knows
// the pull requests and issues (GitHubEvent), and is asked when the page is viewed, as for the
// pull request a branch heads (PR): only humans need them, so they never go on the bus.

// Activity is one thing a session did that isn't a message.
type Activity struct {
	ID   string    `json:"id"`
	Kind string    `json:"kind"` // one of the Act constants
	At   time.Time `json:"at"`   // the clock of whoever recorded it
	// Who is the session's name, and Addr its device/session, as for a message's sender.
	Who    string `json:"who"`
	Addr   string `json:"addr,omitempty"`
	Person bool   `json:"person,omitempty"`
	// Agent and Branch are what a session started as, and Branch the branch of its commits.
	Agent  string `json:"agent,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Doing is the status line the session changed to.
	Doing string `json:"doing,omitempty"`
	// Added and Removed are the tags that changed.
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Commits is how many commits the session made in one tool call, and Subject the last one's.
	Commits int    `json:"commits,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// Activity.Kind values.
const (
	ActStarted = "started"
	ActStopped = "stopped"
	ActStatus  = "status"
	ActTags    = "tags"
	ActCommits = "commits"
)

// GitHubEvent is one thing that happened to a pull request or an issue: it was opened, merged or
// closed. A pull request that was opened and then merged is two events.
type GitHubEvent struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"` // GHPullRequest or GHIssue
	What   string    `json:"what"` // GHOpened, GHMerged or GHClosed
	At     time.Time `json:"at"`
	Number int       `json:"number"`
	Title  string    `json:"title"`
	URL    string    `json:"url,omitempty"`
	// Author is the GitHub login that opened it. Agents on one machine share their user's login,
	// so the page names the session whose branch Branch is, where it knows one.
	Author string `json:"author,omitempty"`
	Branch string `json:"branch,omitempty"` // a pull request's head
}

// GitHubEvent.Kind and What values.
const (
	GHPullRequest = "pr"
	GHIssue       = "issue"
	GHOpened      = "opened"
	GHMerged      = "merged"
	GHClosed      = "closed"
)

// LookupGitHub lists the repository's recent pull request and issue events.
type LookupGitHub func(ctx context.Context) ([]GitHubEvent, error)

const (
	ghTTL     = 2 * time.Minute  // how long a listing is shown before it is redone
	ghTimeout = 20 * time.Second // one listing: two runs of gh
	ghLimit   = 30               // pull requests, and issues, asked for: the most recently updated
	maxTitle  = 200
)

// GHEvents lists them with the gh CLI, run in dir, as GH does: the thirty pull requests and the
// thirty issues updated most recently, in any state, each made into its events. Nothing of the
// page's goes on gh's command line.
func GHEvents(dir string) LookupGitHub {
	run := func(ctx context.Context, v any, args ...string) error {
		cmd := exec.CommandContext(ctx, "gh", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1")
		cmd.WaitDelay = 2 * time.Second
		hideWindow(cmd)
		out, err := cmd.Output()
		if err != nil {
			return err
		}
		if err := json.Unmarshal(out, v); err != nil {
			return fmt.Errorf("gh %s list: %w", args[0], err)
		}
		return nil
	}
	return func(ctx context.Context) ([]GitHubEvent, error) {
		limit := fmt.Sprint(ghLimit)
		var prs, issues []ghItem
		if err := run(ctx, &prs, "pr", "list", "--state", "all", "--limit", limit, "--search", "sort:updated-desc",
			"--json", "number,title,url,author,headRefName,createdAt,mergedAt,closedAt"); err != nil {
			return nil, err
		}
		// A repository with issues turned off fails here, and still has its pull requests.
		_ = run(ctx, &issues, "issue", "list", "--state", "all", "--limit", limit, "--search", "sort:updated-desc",
			"--json", "number,title,url,author,createdAt,closedAt")
		return append(ghEvents(GHPullRequest, prs), ghEvents(GHIssue, issues)...), nil
	}
}

// ghItem is a pull request or an issue as `gh ... list --json` prints it.
type ghItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	HeadRefName string     `json:"headRefName"`
	CreatedAt   time.Time  `json:"createdAt"`
	MergedAt    *time.Time `json:"mergedAt"`
	ClosedAt    *time.Time `json:"closedAt"`
}

// ghEvents makes each item into its events: opened, and then merged or closed if it was. A merged
// pull request has both times, and is one event, merged.
func ghEvents(kind string, items []ghItem) []GitHubEvent {
	var out []GitHubEvent
	for _, it := range items {
		if it.Number <= 0 {
			continue
		}
		ev := func(what string, at time.Time) {
			if at.IsZero() {
				return
			}
			url := it.URL
			if !strings.HasPrefix(url, "https://") {
				url = ""
			}
			out = append(out, GitHubEvent{ID: fmt.Sprintf("gh-%s-%d-%s", kind, it.Number, what), Kind: kind, What: what, At: at,
				Number: it.Number, Title: oneLine(it.Title, maxTitle), URL: url, Author: oneLine(it.Author.Login, maxNameLen),
				Branch: oneLine(it.HeadRefName, maxBranch)})
		}
		ev(GHOpened, it.CreatedAt)
		switch {
		case it.MergedAt != nil:
			ev(GHMerged, *it.MergedAt)
		case it.ClosedAt != nil:
			ev(GHClosed, *it.ClosedAt)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// oneLine is s on one line, without control characters, at most max bytes.
func oneLine(s string, max int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }), " ")
	if len(s) > max {
		s = s[:max]
		for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 {
			s = s[:len(s)-1]
		}
		if len(s) > 0 && s[len(s)-1] >= 0xC0 {
			s = s[:len(s)-1]
		}
	}
	return s
}

// ghCache answers from memory and refreshes in the background, as prCache does, so a page's poll
// never waits on GitHub. A failed listing leaves what was known before.
type ghCache struct {
	lookup LookupGitHub
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	events   []GitHubEvent
	fetched  time.Time
	inflight bool
	closed   bool
}

func newGHCache(lookup LookupGitHub) *ghCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &ghCache{lookup: lookup, ctx: ctx, cancel: cancel}
}

func (c *ghCache) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// get returns the events known, oldest first, and starts a listing if they are old.
func (c *ghCache) get() []GitHubEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lookup != nil && !c.closed && !c.inflight && time.Since(c.fetched) >= ghTTL {
		c.inflight = true
		c.wg.Add(1)
		go c.refresh()
	}
	return c.events
}

func (c *ghCache) refresh() {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(c.ctx, ghTimeout)
	defer cancel()
	events, err := c.lookup(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil { // gh missing, logged out, offline: what was known stays
		c.events = events
	}
	c.fetched, c.inflight = time.Now(), false
}

type activityResponse struct {
	Activity []Activity    `json:"activity"`
	Cursor   string        `json:"cursor"`
	GitHub   []GitHubEvent `json:"github"`
}

// getActivity answers with the daemon's activity after the cursor, and with every GitHub event
// known: those are few, and one that was open may since have merged.
func (s *Server) getActivity(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > maxCursor {
		writeError(w, http.StatusBadRequest, "cursor too long")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sourceTimeout)
	defer cancel()
	acts, next, err := s.src.Activity(ctx, cursor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if acts == nil {
		acts = []Activity{}
	}
	gh := s.gh.get()
	if gh == nil {
		gh = []GitHubEvent{}
	}
	writeJSON(w, activityResponse{Activity: acts, Cursor: next, GitHub: gh})
}
