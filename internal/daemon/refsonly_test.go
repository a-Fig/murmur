package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

// A refs-only init publishes the first team key before the owner starts its daemon. The owner can
// come online later and grant the key to teammates, who can use it to send messages.
func TestFirstEpochOnARefsOnlyInit(t *testing.T) {
	remote := newRemote(t)
	initClone := filepath.Join(t.TempDir(), "init")
	git(t, filepath.Dir(initClone), "clone", "-q", remote, initClone)
	if _, err := setup.RefsOnly(context.Background(), initClone); err != nil {
		t.Fatal(err)
	}
	genesis := strings.TrimSpace(git(t, remote, "rev-parse", "refs/murmur/team"))

	a := &device{t: t, gitDir: filepath.Join(initClone, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	a.start()
	b := newDevice(t, remote)
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
	if r := a.do(proto.Request{Op: proto.OpOwner}); r.Note != "This clone is the mesh owner." {
		t.Fatalf("owner sees %+v", r)
	}
	b.stop()
	statePath := filepath.Join(b.gitDir, brand.StateDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var oldState map[string]json.RawMessage
	if err := json.Unmarshal(data, &oldState); err != nil {
		t.Fatal(err)
	}
	if len(oldState["teamTip"]) == 0 || len(oldState["epochs"]) == 0 {
		t.Fatal("test needs a completed team sync before the simulated upgrade")
	}
	delete(oldState, "teamOwner")
	delete(oldState, "teamOwnerChecked")
	data, err = json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()
	if r := b.do(proto.Request{Op: proto.OpRotate, Force: true}); !strings.Contains(r.Error, "only the mesh owner") {
		t.Fatalf("teammate rotation: %+v", r)
	}
	b.stop()
	if r := a.do(proto.Request{Op: proto.OpRotate, Force: true}); r.Error != "" {
		t.Fatalf("owner rotation: %+v", r)
	}
	if ids := teamIDs(t, remote); len(ids) != 2 {
		t.Fatalf("rotated team ref holds %d epochs, want 2: %q", len(ids), ids)
	}
	// A reader may pass over junk commits. Restoring the last valid history must still let
	// the owner write again.
	valid := strings.TrimSpace(git(t, remote, "rev-parse", brand.TeamRef))
	repo := bus.Repo{Git: gitx.Git{Dir: remote}, Name: "test", Email: "test@example.invalid"}
	junk, err := repo.NewCommit(t.Context(), valid, "junk record\n")
	if err != nil {
		t.Fatal(err)
	}
	git(t, remote, "update-ref", brand.TeamRef, junk, valid)
	eventually(t, "owner reads past the junk", func() bool {
		data, err := os.ReadFile(filepath.Join(a.gitDir, brand.StateDir, "state.json"))
		return err == nil && strings.Contains(string(data), junk)
	})
	git(t, remote, "update-ref", brand.TeamRef, valid, junk)
	if r := a.do(proto.Request{Op: proto.OpRotate, Force: true}); r.Error != "" {
		t.Fatalf("owner rotation after restoring the ref: %+v", r)
	}
}

func TestMeshOwnerTransfersToAVerifiedClone(t *testing.T) {
	remote := newRemote(t)
	initClone := filepath.Join(t.TempDir(), "init")
	git(t, filepath.Dir(initClone), "clone", "-q", remote, initClone)
	if _, err := setup.RefsOnly(t.Context(), initClone); err != nil {
		t.Fatal(err)
	}
	a := &device{t: t, gitDir: filepath.Join(initClone, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	a.start()
	b := newDevice(t, remote)
	if r := b.do(proto.Request{Op: proto.OpTransferOwner, To: a.self()}); !strings.Contains(r.Error, "only the mesh owner") {
		t.Fatalf("non-owner transfer: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpTransferOwner, To: b.self()}); r.Error != "" {
		t.Fatalf("transfer by the mesh owner: %+v", r)
	}
	eventually(t, "new owner's clone sees the transfer", func() bool {
		return b.do(proto.Request{Op: proto.OpOwner}).Note == "This clone is the mesh owner."
	})
	if r := a.do(proto.Request{Op: proto.OpRotate, Force: true}); !strings.Contains(r.Error, "only the mesh owner") {
		t.Fatalf("previous owner rotation: %+v", r)
	}
	if r := b.do(proto.Request{Op: proto.OpRotate, Force: true}); r.Error != "" {
		t.Fatalf("new owner rotation: %+v", r)
	}
}
