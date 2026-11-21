//go:build windows

package join

import (
	"os/exec"
	"syscall"
)

func antigravityShellCmd(command string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd.exe /d /c " + command}
	return cmd
}
