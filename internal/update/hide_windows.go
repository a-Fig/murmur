package update

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps the binary asked its version from opening a console window: the daemon, which
// asks, is detached and has none.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
