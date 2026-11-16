package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
)

// A daemon still starting holds the clone's lock before it writes daemon.json (#668). StopWaiting,
// `mur stop`'s, waits for it rather than say no daemon is running: here it lets go without ever being reachable, as one
// whose start failed does, and Stop then says there was none; one that holds on past the wait is an
// error, never "no daemon".
func TestStopWaitsForADaemonStillStarting(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.MkdirAll(StateDir(gitDir), 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(StateDir(gitDir), "daemon.lock")
	starting, err := proc.Lock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	const hold = 400 * time.Millisecond
	go func() { time.Sleep(hold); starting.Close() }()
	start := time.Now()
	stopped, err := StopWaiting(context.Background(), gitDir)
	if err != nil || stopped {
		t.Fatalf("stopped=%v err=%v", stopped, err)
	}
	if took := time.Since(start); took < hold {
		t.Errorf("Stop returned after %v, before the starting daemon let go of the lock", took)
	}

	held, err := proc.Lock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	old := startWait
	startWait = 300 * time.Millisecond
	defer func() { startWait = old }()
	if stopped, err := StopWaiting(context.Background(), gitDir); !errors.Is(err, ErrStillRunning) || stopped {
		t.Errorf("a daemon that stays starting: stopped=%v err=%v", stopped, err)
	}
}

// One that becomes reachable while StopWaiting waits is stopped: here a stand-in daemon whose
// daemon.json appears only after a while, as a starting daemon writes it after its first read.
func TestStopWaitingStopsADaemonOnceItCanBeReached(t *testing.T) {
	gitDir := stateOnly(t)
	d := startStandIn(t, gitDir, proto.Protocol)
	info := filepath.Join(StateDir(gitDir), "daemon.json")
	b, err := os.ReadFile(info)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(info)
	go func() { time.Sleep(300 * time.Millisecond); os.WriteFile(info, b, 0o600) }()
	stopped, err := StopWaiting(context.Background(), gitDir)
	if err != nil || !stopped || !d.got(proto.OpStop) {
		t.Errorf("stopped=%v err=%v, and the daemon was asked to stop: %v", stopped, err, d.got(proto.OpStop))
	}
}
