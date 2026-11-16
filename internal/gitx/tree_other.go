//go:build !windows

package gitx

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// A tree is a process group, which git leads: its children join it, and a signal to the group
// reaches every one. A child that starts a group of its own leaves it (none of git's do).
type tree struct{ pgid int }

func startTree(cmd *exec.Cmd) (*tree, error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &tree{pgid: cmd.Process.Pid}, nil
}

// end kills the group and waits until nothing is in it. git itself, if it is still running, is
// reaped by its Wait, which RunTree is in; until then it is in the group as a zombie, so end waits
// for at most treeGone.
func (t *tree) end() {
	syscall.Kill(-t.pgid, syscall.SIGKILL)
	deadline := time.Now().Add(treeGone)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-t.pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// outputFile is a file for a command's output that has no name: removed at once, it stays while
// a process has it open.
func outputFile() (*os.File, error) {
	f, err := os.CreateTemp("", "mur-git-*")
	if err != nil {
		return nil, err
	}
	os.Remove(f.Name())
	return f, nil
}
