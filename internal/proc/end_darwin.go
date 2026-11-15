package proc

import (
	"bytes"
	"errors"
	"io/fs"

	"golang.org/x/sys/unix"
)

// end looks at what the process runs and then signals it, both by its number: macOS has nothing
// that holds on to a process as a handle or a pidfd does, so the two are a few microseconds apart.
func end(pid int, is func(exe string) bool) error {
	exe, err := exeOf(pid)
	if err != nil {
		// The kernel says EINVAL, not ESRCH, of an ID no process has (seen on GitHub's macOS
		// runners, 2026-11-11), and of other things too, so the process is asked for by a signal
		// that does nothing.
		gone := errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) ||
			(errors.Is(err, unix.EINVAL) && errors.Is(unix.Kill(pid, 0), unix.ESRCH))
		if gone {
			return ErrGone
		}
		return err
	}
	if !is(exe) {
		return ErrOther
	}
	if err := unix.Kill(pid, unix.SIGTERM); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrGone
		}
		return err
	}
	return nil
}

// exeOf is the path of the program pid runs, from the kernel's copy of the process's arguments:
// a count of them in the first four bytes, then the path the program was started from, ended by a
// zero byte. The kernel gives it only for the user's own processes.
func exeOf(pid int) (string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", err
	}
	if len(b) <= 4 {
		return "", fs.ErrNotExist // what a process that is exiting leaves
	}
	b = b[4:]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b), nil
}
