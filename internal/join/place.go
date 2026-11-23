// Package join is what makes Murmur start in a repo: the user's consent, kept per clone, and the
// per-user setup that goes with it (registering the MCP server with Claude Code and Codex, and the
// notice hook for this repo only). Nothing here is committed, and nothing starts without consent:
// the MCP server is registered for the user in every repo they open, so what keeps it from
// publishing their presence into repos they didn't join is Place.Joined.
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

// Joined reports whether the user joined Murmur in this clone, which is what lets Murmur run in every
// working tree of it. Join records the consent only once it has found the repo's bus refs on the
// remote, so the record is the proof that the repo was set up too (#830). It is read from a file
// alone, so it is cheap to ask again and again: the MCP server asks it every 2 seconds.
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

// RecordConsent notes that the user joined; it reports whether that was news. An existing record
// is left as it is, so joining again changes nothing.
func (p Place) RecordConsent() (bool, error) {
	if p.Joined() {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p.consentPath()), 0o700); err != nil {
		return false, err
	}
	b, _ := json.Marshal(map[string]string{"joined": time.Now().UTC().Format(time.RFC3339)})
	return true, os.WriteFile(p.consentPath(), append(b, '\n'), 0o600)
}

// ForgetConsent removes the record, and reports whether there was one.
func (p Place) ForgetConsent() (bool, error) {
	err := os.Remove(p.consentPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
