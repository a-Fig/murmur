package daemon_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/setup"
)

func TestAutomaticRotationFollowsTheOwnersRoster(t *testing.T) {
	remote := newRemote(t)
	dir := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	if _, err := setup.RefsOnly(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	const addr = "github.com/example/owner-rotation"
	busAddr := "https://" + addr + ".git"
	useGitHubAddress := func(dir string) {
		git(t, dir, "config", "url."+filepath.ToSlash(remote)+".insteadOf", busAddr)
		git(t, dir, "remote", "set-url", "origin", busAddr)
	}
	useGitHubAddress(dir)
	collab := []string{"alice", "bob"}
	listErr := errors.New("fixture API error")
	checks := make(chan string, 1)
	a := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond,
		listCollaborators: func(_ context.Context, address string) ([]string, error) {
			checks <- address
			return collab, listErr
		}}
	waitCheck := func() {
		t.Helper()
		select {
		case address := <-checks:
			if address != addr {
				t.Fatalf("looked up %q, want %q", address, addr)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("owner did not check collaborators at startup")
		}
	}
	a.start()
	waitCheck()
	// The existing key can already have reached another collaborator while gh was failing.
	b := newDevice(t, remote)
	if ids := teamIDs(t, remote); len(ids) != 1 || !b.holdsTeamKey(ids[0]) {
		t.Fatalf("fixture teammate did not receive the initial epoch: %v", ids)
	}
	b.stop()
	a.stop()
	if subjects := git(t, remote, "log", "--format=%s", brand.TeamRef); strings.Contains(subjects, "roster for ") {
		t.Fatal("a failed lookup wrote a roster")
	}

	listErr = nil
	a.start()
	waitCheck()
	eventually(t, "first successful comparison recorded a fresh epoch and roster", func() bool {
		return len(teamIDsOK(remote)) == 2 &&
			strings.HasPrefix(gitOK(remote, "log", "-1", "--format=%s", brand.TeamRef), "roster for ")
	})
	a.stop()

	tip := strings.TrimSpace(git(t, remote, "rev-parse", brand.TeamRef))
	listErr = errors.New("fixture API error")
	a.start()
	waitCheck()
	a.stop()
	if got := strings.TrimSpace(git(t, remote, "rev-parse", brand.TeamRef)); got != tip {
		t.Fatalf("a failed lookup moved the team ref to %s", got)
	}

	collab, listErr = []string{"alice"}, nil
	a.start()
	waitCheck()
	eventually(t, "owner rotated after bob lost push access", func() bool {
		return len(teamIDsOK(remote)) == 3
	})
	a.stop()
}
