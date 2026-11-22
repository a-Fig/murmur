package web

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// LookupPRs lists the repository's open pull requests, by the branch each heads.
type LookupPRs func(ctx context.Context) (map[string]PR, error)

// GH lists pull requests with the gh CLI, run in dir so it finds the repository from the clone's
// remotes and uses the user's own GitHub login.
//
// It asks for every open pull request at once, not for each session's branch (#43). Branch names
// come from the bus, so anyone with push access chooses how many there are: asked one by one, a
// presence listing thousands would use up the viewer's GitHub quota, and an honest team's thirty
// branches cost thirty runs of gh a minute. A listing costs the same however many sessions there
// are, and no branch name goes on gh's command line, where cmd.exe would read one as commands if
// gh is a batch file. The costs: with more than prLimit pull requests open, the oldest show no
// state on the page, and a branch new to the bus waits for the next listing, up to prTTL, where it
// was looked up at once.
func GH(dir string) LookupPRs {
	return func(ctx context.Context) (map[string]PR, error) {
		var prs []struct {
			Number            int    `json:"number"`
			IsDraft           bool   `json:"isDraft"`
			URL               string `json:"url"`
			HeadRefName       string `json:"headRefName"`
			IsCrossRepository bool   `json:"isCrossRepository"`
		}
		if err := ghJSON(ctx, dir, &prs, "pr", "list", "--state", "open", "--limit", fmt.Sprint(prLimit),
			"--json", "number,isDraft,url,headRefName,isCrossRepository"); err != nil {
			return nil, err
		}
		// A fork's pull requests are listed too: a teammate may push their branches to a fork, and
		// then theirs is the session's (most of this repository's open ones were, 2026-11-15). But a
		// fork's branch can share a name with one here, main above all, so of two pull requests with
		// one head, this repository's is the branch's, and else the newer: gh lists the newest first.
		out, fork := map[string]PR{}, map[string]bool{}
		for _, p := range prs {
			if p.Number <= 0 || p.HeadRefName == "" {
				continue
			}
			if _, ok := out[p.HeadRefName]; ok && (p.IsCrossRepository || !fork[p.HeadRefName]) {
				continue
			}
			out[p.HeadRefName] = PR{Number: p.Number, Draft: p.IsDraft, URL: p.URL}
			fork[p.HeadRefName] = p.IsCrossRepository
		}
		return out, nil
	}
}

// ghJSON runs gh in dir with args and reads the JSON it prints into v.
func ghJSON(ctx context.Context, dir string, v any, args ...string) error {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	// The daemon runs detached, with nobody to answer a prompt or read an update notice.
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

const (
	prTTL     = time.Minute      // how long a listing is shown before it is redone
	prTimeout = 15 * time.Second // one listing, however slow GitHub is
	prLimit   = 300              // open pull requests asked for, the newest: three requests of gh's
)

// prCache answers PR state from memory and refreshes it in the background, so a page poll never
// waits on GitHub. A failed listing leaves what was known before, as ghCache does: one listing
// answers for every branch now, so counting a failure as no PRs would blank them all at once.
//
// A listing runs gh in the clone, so one still running when the page's server stops would outlive
// it there: close cancels the one in flight, waits for it, and starts no more.
type prCache struct {
	lookup LookupPRs
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	prs      map[string]PR // by the branch each heads
	fetched  time.Time     // zero until the first listing ends
	inflight bool
	closed   bool
}

func newPRCache(lookup LookupPRs) *prCache {
	ctx, cancel := context.WithCancel(context.Background())
	return &prCache{lookup: lookup, ctx: ctx, cancel: cancel}
}

// close cancels the listing in flight and returns once it has ended. Later gets start none.
func (c *prCache) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// get returns what is known of each branch's PR, leaving out branches with none, and starts a
// listing if the last is old. With no branch to show a PR for, it asks GitHub nothing.
func (c *prCache) get(branches []string) map[string]PR {
	out := map[string]PR{}
	wanted := false
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range branches {
		if b == "" {
			continue
		}
		wanted = true
		if pr, ok := c.prs[b]; ok {
			out[b] = pr
		}
	}
	if wanted && !c.closed && !c.inflight && time.Since(c.fetched) >= prTTL {
		c.inflight = true
		c.wg.Add(1) // under mu, which close takes before it waits, so never after the wait began
		go c.refresh()
	}
	return out
}

func (c *prCache) refresh() {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(c.ctx, prTimeout)
	defer cancel()
	listed, err := c.lookup(ctx)
	prs := make(map[string]PR, len(listed))
	for b, pr := range listed {
		if !strings.HasPrefix(pr.URL, "https://") {
			pr.URL = ""
		}
		prs[b] = pr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil { // gh missing, logged out, offline: what was known stays
		c.prs = prs
	}
	c.fetched, c.inflight = time.Now(), false
}
