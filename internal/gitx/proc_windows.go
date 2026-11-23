package gitx

import (
	"os"
	"os/exec"
	"syscall"
)

// createNoWindow keeps each git child from opening a console window when its parent (the
// detached daemon) has none.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// Windows has no interrupt to send another process; a cancelled git is killed, and the daemon
// clears the locks such a kill can leave (see daemon.clearStaleLocks).
var interrupt func(*os.Process) error

// noControllingTerminal does nothing on Windows, where hideWindow already gives git no console.
func noControllingTerminal(*exec.Cmd) {}
