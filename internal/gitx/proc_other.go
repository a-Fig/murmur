//go:build !windows

package gitx

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// noControllingTerminal starts cmd in a session of its own, which has no controlling terminal
// (Git.NoTerminal).
func noControllingTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

var interrupt = func(p *os.Process) error { return p.Signal(os.Interrupt) }
