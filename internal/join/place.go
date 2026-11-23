// Package join is what makes Murmur start in a repo: the user's consent, kept per clone, and the
// per-user setup that goes with it (registering the MCP server with Claude Code and Codex, and the
// notice hook for this repo only). Nothing here is committed, and nothing starts without consent:
// the MCP server is registered for the user in every repo they open, so what keeps it from
// publishing their presence into repos that didn't invite it is Place.Active.
package join

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/setup"
)

// consentFile is kept in the clone's murmur directory inside .git, which no commit, checkout or
// other user's clone has: joining is this user's, in this clone.
const consentFile = "joined.json"

// Place is a clone, as seen from one of its working trees.
type Place struct {
	Top    string // the root of this working tree
	GitDir string // the clone's git common dir, shared by all its worktrees
}

// Locate finds the clone containing dir.
func Locate(ctx context.Context, dir string) (Place, error) {
	top, err := setup.Top(ctx, dir)
	if err != nil {
		return Place{}, err
	}
	gitDir, err := client.FindGitDir(ctx, dir)
	if err != nil {
		return Place{}, err
	}
	return Place{Top: top, GitDir: gitDir}, nil
}

func (p Place) consentPath() string { return filepath.Join(client.StateDir(p.GitDir), consentFile) }

// Marked reports whether this working tree says the repo uses Murmur.
func (p Place) Marked() bool { return setup.Marked(p.Top) }

// Joined reports whether the user joined Murmur in this clone.
func (p Place) Joined() bool {
	if p.GitDir == "" {
		return false // no clone: the path below would be relative to wherever the process is
	}
	_, err := os.Stat(p.consentPath())
	return err == nil
}

// Joined reports whether the user joined Murmur in the clone with this git common dir. The daemon
// asks it, having no working tree of its own.
func Joined(gitDir string) bool { return Place{GitDir: gitDir}.Joined() }

// Active reports whether Murmur may run for this working tree: the repo was set up for it, and this
// user accepted. Set up means the section is here or on the default branch (Marked), or this user's
// join found the bus refs on the remote, in a repo set up by its refs alone (#830). Both are read
// from files alone, with no network, so it is cheap to ask again and again: the MCP server asks it
// every 2 seconds.
//
// Join's record stands in for the refs because a fresh clone has none of its own until its daemon's
// first fetch, and the daemon is started only where this says yes. Checking the clone's own
// refs/murmur/* instead would also take a git process each time.
func (p Place) Active() bool {
	if !p.Joined() {
		return false
	}
	return p.consent().ByRefs != "" || p.Marked()
}

// consent is the record RecordConsent writes: when the user joined, and, for a join that found the
// repo set up by its refs alone, when it found them.
type consent struct {
	Joined string `json:"joined"`
	ByRefs string `json:"byRefs,omitempty"`
}

// consent reads the record; one that can't be read has neither time.
func (p Place) consent() consent {
	var c consent
	if b, err := os.ReadFile(p.consentPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	return c
}

// RecordConsent notes that the user joined; it reports whether that was news. An existing record is
// left as it is, so joining again changes nothing.
func (p Place) RecordConsent() (bool, error) { return p.recordConsent(false) }

// RecordConsentByRefs is RecordConsent for a join that found the repo set up by its bus refs alone:
// the record says so, for Active. A record that doesn't yet say so gains it.
func (p Place) RecordConsentByRefs() (bool, error) { return p.recordConsent(true) }

func (p Place) recordConsent(byRefs bool) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	c, news := consent{Joined: now}, true
	if p.Joined() {
		if c = p.consent(); !byRefs || c.ByRefs != "" {
			return false, nil
		}
		if c.Joined == "" {
			c.Joined = now
		}
		news = false
	}
	if byRefs {
		c.ByRefs = now
	}
	if err := os.MkdirAll(filepath.Dir(p.consentPath()), 0o700); err != nil {
		return false, err
	}
	b, _ := json.Marshal(c)
	return news, os.WriteFile(p.consentPath(), append(b, '\n'), 0o600)
}

// ForgetConsent removes the record, and reports whether there was one.
func (p Place) ForgetConsent() (bool, error) {
	err := os.Remove(p.consentPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
