package proc

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// end takes a pidfd before it looks at what the process runs, and signals through it, so the
// signal goes to the process looked at or to none. os.FindProcess isn't used for this: where the
// kernel has no pidfd, or a sandbox refuses one, it quietly gives a process known only by its
// number, and a signal through that goes to whatever has the number by then (Sol's review of
// #425). Without a pidfd nothing is ended.
func end(pid int, is func(exe string) bool) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrGone
		}
		return fmt.Errorf("%w: no pidfd for process %d: %v", ErrCantEnd, pid, err)
	}
	defer unix.Close(fd)
	// /proc/<pid>/exe is where the path of the program is; Linux adds " (deleted)" to the path of
	// a file that has been removed or replaced since. It is read by number, so it may be another
	// process's if this one has gone; the signal then finds the pidfd's process gone.
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			return ErrGone
		}
		return err
	}
	if !is(exe) {
		return ErrOther
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGTERM, nil, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return ErrGone
		}
		return err
	}
	return nil
}
