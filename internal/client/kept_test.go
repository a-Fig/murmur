package client

import (
	"context"
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
	if !c.Outdated() || !strings.Contains(c.Kept(), "another binary") {
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
	if _, err := Replace(ctx, gitDir); err == nil || !strings.Contains(err.Error(), "another binary") || old.got(proto.OpStop) {
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
