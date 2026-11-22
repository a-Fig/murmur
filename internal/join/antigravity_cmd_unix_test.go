//go:build !windows

package join

import "os/exec"

func antigravityShellCmd(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}
