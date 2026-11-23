package proc

import (
	"bytes"
	"errors"
)

// ErrNoExe means this system has no way written here to tell what program a process runs.
var ErrNoExe = errors.New("finding a process's program isn't supported on this system")

// Exe is the path of the program process pid runs, as the system has it: the path it was started
// from (see End for what a replaced program's reads as). It is how a session's MCP server finds
// the client that started it, to run that same program later (package clientcli): the name on
// PATH may be another version, or on Windows a .cmd shim, which can't be passed a message's text.
func Exe(pid int) (string, error) {
	if pid <= 1 {
		return "", ErrGone
	}
	return exeOf(pid)
}

// parseDarwinProcArgs2 extracts the executable path from macOS's kern.procargs2 buffer: a 4-byte
// argc followed by a NUL-terminated executable path.
func parseDarwinProcArgs2(b []byte) (string, error) {
	if len(b) <= 4 {
		return "", ErrGone // what a process that is exiting leaves
	}
	b = b[4:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	if len(b) == 0 {
		return "", ErrGone
	}
	return string(b), nil
}
