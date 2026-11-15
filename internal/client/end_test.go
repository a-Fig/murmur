package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of ending a daemon from before proof by its process (#394): what `mur stop` and `mur join`
// do for someone who installed 0.3 over a running 0.2, whose daemon can be sent nothing.

// endsWith makes end what ends a process, in place of proc.End, and returns the process IDs it was
// asked to end.
func endsWith(t *testing.T, end func(pid int, is func(string) bool) error) *[]int {
	t.Helper()
	was := endProcess
	t.Cleanup(func() { endProcess = was })
	var pids []int
	endProcess = func(pid int, is func(string) bool) error {
		pids = append(pids, pid)
		return end(pid, is)
	}
	return &pids
}

// sentNothing fails the test if the stand-in was sent a request with its token.
func sentNothing(t *testing.T, d *standIn) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.ops) > 0 {
		t.Errorf("the older daemon was sent %v", d.ops)
	}
}

// `mur stop` ends a daemon from before proof by the process daemon.json names, and sends it
// nothing. The daemon.json it leaves, as a killed process does, goes too.
func TestStopEndsADaemonFromBeforeProofByItsProcess(t *testing.T) {
	gitDir := stateOnly(t)
	old := startLegacyStandIn(t, gitDir)
	pids := endsWith(t, func(pid int, is func(string) bool) error {
		if !is(filepath.Join(t.TempDir(), "mur")) {
			t.Error("the check End is given doesn't take mur for this command")
		}
		if is(os.Args[0]) {
			t.Errorf("the check End is given takes %s for this command", os.Args[0])
		}
		old.die()
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopped, err := Stop(ctx, gitDir)
	if !stopped || err != nil {
		t.Fatalf("stop: stopped %v, err %v", stopped, err)
	}
	if len(*pids) != 1 || (*pids)[0] != os.Getpid() { // the stand-in's daemon.json names this process
		t.Errorf("ended processes %v, want only %d", *pids, os.Getpid())
	}
	sentNothing(t, old)
	if _, err := os.Stat(filepath.Join(StateDir(gitDir), "daemon.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the ended daemon's daemon.json is still there (%v)", err)
	}
	if stopped, err := Stop(ctx, gitDir); stopped || err != nil {
		t.Errorf("a second stop: stopped %v, err %v, want no daemon", stopped, err)
	}
}

// Where the process daemon.json names isn't this command, or is gone, nothing is ended, and the
// client says what it said before: that the daemon is older, and how to end it by hand.
func TestADaemonFromBeforeProofThatCanNotBeEndedIsLeftWithTheOldAnswer(t *testing.T) {
	for _, why := range []error{proc.ErrOther, proc.ErrGone, proc.ErrCantEnd} {
		gitDir := stateOnly(t)
		old := startLegacyStandIn(t, gitDir)
		endsWith(t, func(int, func(string) bool) error { return why })
		stopped, err := Stop(context.Background(), gitDir)
		if !stopped || !errors.Is(err, errOlderDaemon) || !errors.Is(err, why) {
			t.Errorf("%v: stopped %v, err %v, want errOlderDaemon and why", why, stopped, err)
		} else if !strings.Contains(err.Error(), fmt.Sprint(os.Getpid())) {
			t.Errorf("%v: the error doesn't name the process: %v", why, err)
		}
		sentNothing(t, old)
		if !Running(context.Background(), gitDir) {
			t.Errorf("%v: the daemon is no longer found", why)
		}
		old.exit()
	}
}

// The real proc.End leaves this test's process alone: it isn't mur, and it is the caller. So the
// tests whose stand-in names this process can run Stop against it.
func TestStopNeverEndsAProcessThatIsNotThisCommand(t *testing.T) {
	gitDir := stateOnly(t)
	old := startLegacyStandIn(t, gitDir)
	if _, err := Stop(context.Background(), gitDir); !errors.Is(err, errOlderDaemon) || !errors.Is(err, proc.ErrOther) {
		t.Errorf("stop: %v, want errOlderDaemon and proc.ErrOther", err)
	}
	sentNothing(t, old)
}

// `mur join` (Replace) ends a daemon from before proof and starts a current one in its place.
func TestReplaceEndsADaemonFromBeforeProofAndStartsACurrentOne(t *testing.T) {
	gitDir := stateOnly(t)
	old := startLegacyStandIn(t, gitDir)
	pids := endsWith(t, func(int, func(string) bool) error { old.die(); return nil })
	var fresh *standIn
	c := &Client{GitDir: gitDir, Spawn: true, EndOlder: true}
	c.launch = func() error {
		fresh = startStandIn(t, gitDir, proto.Protocol)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.connect(ctx); err != nil {
		t.Fatal(err)
	}
	if fresh == nil {
		t.Fatal("no new daemon was started")
	}
	if len(*pids) != 1 {
		t.Errorf("ended processes %v, want one", *pids)
	}
	sentNothing(t, old)
	if c.Outdated() {
		t.Error("the client still takes its daemon to be older")
	}
	if resp, err := c.Do(ctx, proto.Request{Op: proto.OpRoute}); err != nil || !resp.OK || !fresh.got(proto.OpRoute) {
		t.Errorf("route: %v %+v, to the new daemon: %v", err, resp, fresh.got(proto.OpRoute))
	}
}

// A session's client, which may start daemons too, still ends nothing: only Stop and Replace do.
func TestASessionsClientDoesNotEndADaemonFromBeforeProof(t *testing.T) {
	gitDir := stateOnly(t)
	startLegacyStandIn(t, gitDir)
	pids := endsWith(t, func(int, func(string) bool) error { return nil })
	c := &Client{GitDir: gitDir, Spawn: true}
	c.launch = func() error {
		t.Error("a daemon was started beside the older one")
		return nil
	}
	if err := c.connect(context.Background()); !errors.Is(err, errOlderDaemon) {
		t.Errorf("connect: %v, want errOlderDaemon", err)
	}
	if len(*pids) > 0 {
		t.Errorf("processes %v were ended", *pids)
	}
}

// A daemon that was ended and still holds the clone is an error, and no daemon is started beside it.
func TestAnEndedDaemonThatStillHoldsTheCloneIsAnError(t *testing.T) {
	wait := replaceWait
	replaceWait = 300 * time.Millisecond
	t.Cleanup(func() { replaceWait = wait })
	gitDir := stateOnly(t)
	startLegacyStandIn(t, gitDir)
	endsWith(t, func(int, func(string) bool) error { return nil }) // it takes the signal and stays
	c := &Client{GitDir: gitDir, Spawn: true, EndOlder: true}
	c.launch = func() error {
		t.Error("a daemon was started while the older one holds the lock")
		return nil
	}
	if err := c.connect(context.Background()); err == nil || !strings.Contains(err.Error(), "still holds") {
		t.Errorf("connect: %v", err)
	}
}

// Another client, a session's server reconnecting or a second join, may start a current daemon the
// moment the old one lets go. That daemon holds the lock as the old one did, and isn't taken for it
// (Sol's review of #425): the stop has done its work, and the new daemon's daemon.json stays.
func TestADaemonStartedAsTheOldOneGoesIsNotTakenForIt(t *testing.T) {
	wait := replaceWait
	replaceWait = 2 * time.Second
	t.Cleanup(func() { replaceWait = wait })
	gitDir := stateOnly(t)
	old := startLegacyStandIn(t, gitDir)
	var fresh *standIn
	endsWith(t, func(int, func(string) bool) error {
		old.die()
		os.Remove(filepath.Join(StateDir(gitDir), "daemon.json")) // as a daemon does once it holds the lock
		fresh = startStandIn(t, gitDir, proto.Protocol)
		return nil
	})
	if stopped, err := Stop(context.Background(), gitDir); !stopped || err != nil {
		t.Fatalf("stop: stopped %v, err %v", stopped, err)
	}
	sentNothing(t, old)
	if info, ok := (&Client{GitDir: gitDir}).load(); !ok || info.Token != fresh.token {
		t.Errorf("the new daemon's daemon.json is gone or changed: %+v", info)
	}
	if !Running(context.Background(), gitDir) {
		t.Error("the new daemon is no longer found")
	}
}

// A daemon that started in the ended one's place keeps its daemon.json.
func TestEndingADaemonLeavesANewDaemonsFileAlone(t *testing.T) {
	gitDir := stateOnly(t)
	old := startLegacyStandIn(t, gitDir)
	info, ok := (&Client{GitDir: gitDir}).load()
	if !ok {
		t.Fatal("no daemon.json")
	}
	file := filepath.Join(StateDir(gitDir), "daemon.json")
	endsWith(t, func(int, func(string) bool) error {
		old.die()
		return os.WriteFile(file, []byte(`{"addr":"127.0.0.1:1","token":"another","pid":1}`), 0o600)
	})
	if err := endOlder(context.Background(), gitDir, info); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("another daemon's daemon.json was removed: %v", err)
	}
}

func TestIsCommand(t *testing.T) {
	yes := []string{
		"/home/u/.local/bin/mur",
		"/home/u/.local/bin/mur (deleted)",
		"/home/u/.local/bin/mur.old-.mur-update-123 (deleted)",
		"mur",
	}
	no := []string{
		"", "/usr/bin/git", "/home/u/mur/bin/node", "/home/u/.local/bin/murmur", "/home/u/.local/bin/mur.sh",
		"/home/u/.local/bin/mur-old", "/home/u/bin/client.test",
	}
	if runtime.GOOS == "windows" {
		yes = append(yes,
			`C:\Users\u\.local\bin\mur.exe`,
			`C:\Users\u\.local\bin\MUR.EXE`,
			`C:\Users\u\.local\bin\mur.exe.old-1a2b3c4d`,
			`C:\Users\u\.local\bin\mur.exe.old-.mur-update-123`,
		)
		no = append(no, `C:\Users\u\mur\node.exe`, `C:\Users\u\.local\bin\murmur.exe`, `C:\Users\u\client.test.exe`)
	} else {
		yes = append(yes, "/home/u/.local/bin/mur.exe") // harmless, and one rule for every system
		no = append(no, "/home/u/.local/bin/MUR")
	}
	for _, exe := range yes {
		if !isCommand(exe) {
			t.Errorf("isCommand(%q) is false", exe)
		}
	}
	for _, exe := range no {
		if isCommand(exe) {
			t.Errorf("isCommand(%q) is true", exe)
		}
	}
}
