package daemon_test

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

// In a repo set up by its refs alone (`mur init --refs-only`, #830), the team ref starts at a
// genesis commit with no key on it. The first device makes epoch 1 on top of it, as a fast-forward,
// and what the devices then write is encrypted for that key: a message goes through, and the remote
// never holds it in the clear. On main, no device made an epoch on a team ref that was there, so a
// send waited for a key that never came.
func TestFirstEpochOnARefsOnlyInit(t *testing.T) {
	remote := newRemote(t)
	initClone := filepath.Join(t.TempDir(), "init")
	git(t, filepath.Dir(initClone), "clone", "-q", remote, initClone)
	if _, err := setup.RefsOnly(context.Background(), initClone); err != nil {
		t.Fatal(err)
	}
	genesis := strings.TrimSpace(git(t, remote, "rev-parse", "refs/murmur/team"))

	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "a sees b", func() bool { return a.seesSession(bob) })
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "subject-refsonly-hidden7", Body: "body-refsonly-hidden7"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "b gets it", func() bool {
		in := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
		return len(in) == 1 && in[0].Body == "body-refsonly-hidden7"
	})

	if ids := teamIDs(t, remote); len(ids) != 1 {
		t.Errorf("the team ref holds %d epochs, want 1: %q", len(ids), ids)
	}
	if roots := strings.Fields(git(t, remote, "rev-list", "--max-parents=0", "refs/murmur/team")); !slices.Equal(roots, []string{genesis}) {
		t.Errorf("the team ref's root is %q, want the genesis commit %s: it was replaced, not built on", roots, genesis)
	}
	if e := lastEventOn(t, remote, a.self()); e.Box == nil || !slices.ContainsFunc(e.Box.Recipients(), bus.IsTeamID) {
		t.Errorf("a's message isn't encrypted for the team key: %+v", e)
	}
	if strings.Contains(remoteObjects(t, remote), "refsonly-hidden7") {
		t.Error("the remote holds the message in the clear")
	}
}
