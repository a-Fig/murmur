//go:build !windows

package gitx_test

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/a-Fig/murmur/internal/gitx"
)

// A NoTerminal git, and what it starts, runs in a session of its own, which has no controlling
// terminal for ssh to ask a passphrase on (#830). A new session is a new process group too, which
// ps can name on Linux and macOS alike; without NoTerminal, git's children are in this test's.
func TestNoTerminalRunsInASessionOfItsOwn(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps to ask a process's group")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	groupOf := func(g gitx.Git) int {
		t.Helper()
		out, err := g.Run(context.Background(), "", "-c", "alias.pgid=!ps -o pgid= -p $$", "pgid")
		if err != nil {
			t.Fatal(err)
		}
		pgid, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			t.Fatalf("ps printed %q", out)
		}
		return pgid
	}
	mine := syscall.Getpgrp()
	if pgid := groupOf(gitx.Git{Dir: dir}); pgid != mine {
		t.Fatalf("a plain git's child is in group %d, not this test's %d", pgid, mine)
	}
	if pgid := groupOf(gitx.Git{Dir: dir, NoTerminal: true}); pgid == mine {
		t.Errorf("a NoTerminal git's child is in this test's group, %d", pgid)
	}
}
