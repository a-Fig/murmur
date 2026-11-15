package clientcli

import (
	"os/exec"
	"syscall"
	"unicode/utf16"
)

// createNoWindow keeps the command from opening a console window when its parent, the detached
// daemon, has none.
const createNoWindow = 0x08000000

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// maxCommandLine is the longest command line CreateProcess takes, in UTF-16 units, less some room
// for what Go adds around the program's name.
const maxCommandLine = 32767 - 64

// fits says the command line Go would build for exe and args is one Windows starts.
func fits(exe string, args []string) bool {
	n := len(utf16.Encode([]rune(syscall.EscapeArg(exe))))
	for _, a := range args {
		n += 1 + len(utf16.Encode([]rune(syscall.EscapeArg(a))))
	}
	return n <= maxCommandLine
}
