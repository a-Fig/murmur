package daemon_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/setup"
)

// Automatic rotation is off until the mesh owner turns it on (#1285): with it off, the owner's
// daemon never asks GitHub who can push. Only the owner can turn it on. The first check after that
// records who can push without making a new key, so turning it on doesn't pause the team; a later
// check that finds someone gone rotates, as `mur rotate` would.
func TestAutomaticRotationIsTheOwnersOptIn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	dir := filepath.Join(t.TempDir(), "owner")
	git(t, filepath.Dir(dir), "clone", "-q", remote, dir)
	if _, err := setup.RefsOnly(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	const addr = "github.com/example/owner-rotation"
	busAddr := "https://" + addr + ".git"
	git(t, dir, "config", "url."+filepath.ToSlash(remote)+".insteadOf", busAddr)
	git(t, dir, "remote", "set-url", "origin", busAddr)
	collab := []string{"alice", "bob"}
	listErr := errors.New("fixture API error")
	var listed atomic.Int32
	checks := make(chan string, 4)
	a := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond,
		listCollaborators: func(_ context.Context, address string) ([]string, error) {
			listed.Add(1)
			checks <- address
			return collab, listErr
		}}
	waitCheck := func(what string) {
		t.Helper()
		select {
		case address := <-checks:
			if address != addr {
				t.Fatalf("looked up %q, want %q", address, addr)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("owner did not check collaborators %s", what)
		}
	}
	setAutoRotate := func(p *page, on bool) (int, string) {
		t.Helper()
		body := `{"autoRotate":false}`
		if on {
			body = `{"autoRotate":true}`
		}
		return p.call("PATCH", "/api/repo-policy", p.token, body)
	}

	a.start()
	b := newDevice(t, remote)
	pa, pb := a.page(), b.page()
	if ids := teamIDs(t, remote); len(ids) != 1 || !b.holdsTeamKey(ids[0]) {
		t.Fatalf("fixture teammate did not receive the initial epoch: %v", ids)
	}
	eventually(t, "teammate verifies the owner", func() bool { return pb.state().MeshOwner != "" })
	if pa.state().AutoRotate || pb.state().AutoRotate {
		t.Fatal("a new mesh has automatic rotation on")
	}
	if n := listed.Load(); n != 0 {
		t.Fatalf("owner listed collaborators %d times with automatic rotation off", n)
	}
	if code, body := setAutoRotate(pb, true); code != http.StatusConflict || !strings.Contains(body, "only the mesh owner") {
		t.Fatalf("a teammate turned on automatic rotation: %d %s", code, body)
	}

	if code, body := setAutoRotate(pa, true); code != http.StatusNoContent {
		t.Fatalf("owner turning on automatic rotation: %d %s", code, body)
	}
	waitCheck("once automatic rotation was turned on")
	eventually(t, "teammate's page shows the owner's setting", func() bool { return pb.state().AutoRotate })
	b.stop()
	a.stop()
	if subjects := git(t, remote, "log", "--format=%s", brand.TeamRef); strings.Contains(subjects, "roster for ") {
		t.Fatal("a failed lookup wrote a roster")
	}

	// The setting outlasts a restart, and the first successful check only records the roster.
	listErr = nil
	a.start()
	waitCheck("at startup")
	eventually(t, "first successful check recorded a roster", func() bool {
		return strings.HasPrefix(gitOK(remote, "log", "-1", "--format=%s", brand.TeamRef), "roster for ")
	})
	if ids := teamIDs(t, remote); len(ids) != 1 {
		t.Fatalf("turning on automatic rotation made a new team key: %v", ids)
	}
	a.stop()

	tip := strings.TrimSpace(git(t, remote, "rev-parse", brand.TeamRef))
	listErr = errors.New("fixture API error")
	a.start()
	waitCheck("at startup")
	a.stop()
	if got := strings.TrimSpace(git(t, remote, "rev-parse", brand.TeamRef)); got != tip {
		t.Fatalf("a failed lookup moved the team ref to %s", got)
	}

	collab, listErr = []string{"alice"}, nil
	a.start()
	waitCheck("at startup")
	eventually(t, "owner rotated after bob lost push access", func() bool {
		return len(teamIDsOK(remote)) == 2
	})

	// Off again: no more lookups, even across a restart.
	pa = a.page()
	if code, body := setAutoRotate(pa, false); code != http.StatusNoContent {
		t.Fatalf("owner turning off automatic rotation: %d %s", code, body)
	}
	a.stop()
	before := listed.Load()
	a.start()
	pa = a.page()
	eventually(t, "owner verifies its own policy", func() bool { return pa.state().MeshOwner != "" })
	time.Sleep(time.Second)
	if pa.state().AutoRotate {
		t.Fatal("automatic rotation came back on after a restart")
	}
	if n := listed.Load(); n != before {
		t.Fatalf("owner listed collaborators with automatic rotation off (%d more)", n-before)
	}
	a.stop()
}
