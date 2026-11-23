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

// kiroShellCmd matches Node's child_process.spawn(command, {shell: true}) on Windows.
func kiroShellCmd(command string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`}
	return cmd
}
