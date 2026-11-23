//go:build !windows

package gitx

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

// noControllingTerminal starts cmd in a session of its own, which has no controlling terminal
// (Git.NoTerminal). With no terminal, an OpenSSH that has DISPLAY and SSH_ASKPASS would open a
// window to ask for the passphrase instead; SSH_ASKPASS_REQUIRE=never stops that in OpenSSH 8.4
// and later, and older ones ignore it. It is added to cmd.Env, which must be set already.
func noControllingTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.Env = append(cmd.Env, "SSH_ASKPASS_REQUIRE=never")
}

var interrupt = func(p *os.Process) error { return p.Signal(os.Interrupt) }
