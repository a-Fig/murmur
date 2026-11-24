package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// An MCP server that outlived a return to stable runs a newer binary than the one installed, whose
// daemon speaks an older protocol. Replacing that daemon would start the installed binary, the same
// older version, so it is kept: not stopped, worked with, and the reason is there to report (#1197).
// `mur join`, asked to replace it, says why it didn't.
func TestAnOlderDaemonIsKeptWhenTheInstalledBinaryIsAnother(t *testing.T) {
	was := launchesOwn
	launchesOwn = func() bool { return false }
	t.Cleanup(func() { launchesOwn = was })
	gitDir := stateOnly(t)
	old := startStandIn(t, gitDir, proto.Protocol-1)
	c := &Client{GitDir: gitDir, Spawn: true}
	c.launch = func() error {
		t.Error("a daemon was started")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for range 3 { // a reconnect, as after a dropped connection, doesn't try again
		if err := c.connect(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if old.got(proto.OpStop) {
		t.Fatal("the older daemon was stopped")
	}
	if !c.Outdated() || !strings.Contains(c.Kept(), "isn't this binary") {
		t.Errorf("outdated %v, kept %q", c.Outdated(), c.Kept())
	}
	conn, _, err := c.Hello(ctx, proto.Hello{ID: "aaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if resp, err := c.Do(ctx, proto.Request{Op: proto.OpRoute}); err != nil || !resp.OK || !old.got(proto.OpRoute) {
		t.Errorf("route: %v %+v", err, resp)
	}
	if _, err := Replace(ctx, gitDir); err == nil || !strings.Contains(err.Error(), "isn't this binary") || old.got(proto.OpStop) {
		t.Errorf("join's replace: %v, stopped %v", err, old.got(proto.OpStop))
	}
}

// A replacement that comes up as old as the daemon it replaced (the installed binary is older,
// though this process couldn't tell beforehand) is worked with at once, not waited on for its
// protocol until the start times out, and not replaced again at the next reconnect (#1197).
func TestAReplacementThatComesUpOlderIsNotRepeated(t *testing.T) {
	gitDir := stateOnly(t)
	old := startStandIn(t, gitDir, proto.Protocol-1)
	var launches atomic.Int32
	var fresh *standIn
	c := &Client{GitDir: gitDir, Spawn: true}
	c.launch = func() error {
		launches.Add(1)
		fresh = startStandIn(t, gitDir, proto.Protocol-1)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	began := time.Now()
	if err := c.connect(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("connect took %v", took)
	}
	if !old.got(proto.OpStop) || fresh == nil {
		t.Fatal("the first replacement didn't happen")
	}
	if err := c.connect(ctx); err != nil {
		t.Fatal(err)
	}
	if n := launches.Load(); n != 1 || fresh.got(proto.OpStop) {
		t.Errorf("launches %d, replacement stopped %v", n, fresh.got(proto.OpStop))
	}
	if !strings.Contains(c.Kept(), "older version") {
		t.Errorf("kept %q", c.Kept())
	}
}

// The check itself: the test binary is still the file this process started from.
func TestLaunchesOwnBinary(t *testing.T) {
	if ownBinary == nil || !launchesOwn() {
		t.Fatalf("own binary %v, launches own %v", ownBinary, launchesOwn())
	}
}

// The binary installed is asked about again at each connect: a moment it couldn't be read, as
// mid-swap, keeps the older daemon for that connect only, and the next one replaces it.
func TestAnotherBinaryIsAskedAgainAtEachConnect(t *testing.T) {
	own := false
	was := launchesOwn
	launchesOwn = func() bool { return own }
	t.Cleanup(func() { launchesOwn = was })
	gitDir := stateOnly(t)
	old := startStandIn(t, gitDir, proto.Protocol-1)
	var fresh *standIn
	c := &Client{GitDir: gitDir, Spawn: true}
	c.launch = func() error {
		fresh = startStandIn(t, gitDir, proto.Protocol)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.connect(ctx); err != nil || old.got(proto.OpStop) || c.Kept() == "" {
		t.Fatalf("connect %v, stopped %v, kept %q", err, old.got(proto.OpStop), c.Kept())
	}
	own = true
	if err := c.connect(ctx); err != nil || fresh == nil || c.Outdated() || c.Kept() != "" {
		t.Fatalf("connect %v, replaced %v, outdated %v, kept %q", err, fresh != nil, c.Outdated(), c.Kept())
	}
}

// With no daemon found at first, an older one found after the start may be one whose ping was
// only slow (the start then finds the lock taken): it is worked with, not kept for good, and the
// next connect replaces it.
func TestAnOlderDaemonFoundAfterAStartIsNotKeptForGood(t *testing.T) {
	gitDir := stateOnly(t)
	var older, fresh *standIn
	c := &Client{GitDir: gitDir, Spawn: true}
	c.launch = func() error {
		if older == nil {
			older = startStandIn(t, gitDir, proto.Protocol-1)
		} else {
			fresh = startStandIn(t, gitDir, proto.Protocol)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.connect(ctx); err != nil || older == nil || c.Kept() != "" {
		t.Fatalf("connect %v, kept %q", err, c.Kept())
	}
	if err := c.connect(ctx); err != nil || fresh == nil || !older.got(proto.OpStop) || c.Outdated() {
		t.Fatalf("connect %v, replaced %v, older stopped %v", err, fresh != nil, older.got(proto.OpStop))
	}
}

// fileIdentity tells a file from the one renamed over it, and stays with a file renamed away,
// which is how launchesOwn tells this binary from an update's.
func TestFileIdentityFollowsTheFile(t *testing.T) {
	dir := t.TempDir()
	path, other := filepath.Join(dir, "mur"), filepath.Join(dir, "staged")
	os.WriteFile(path, []byte("old"), 0o755)
	os.WriteFile(other, []byte("new"), 0o755)
	before, err := fileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := fileIdentity(path); !sameFile(before, again) {
		t.Fatal("the same file reads as another")
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if moved, _ := fileIdentity(path + ".old"); !sameFile(before, moved) {
		t.Error("a renamed file reads as another")
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if after, _ := fileIdentity(path); sameFile(before, after) {
		t.Error("the file renamed over it reads as the same")
	}
}
