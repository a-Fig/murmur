package proc

import (
	"errors"
	"os"
)

// ErrGone means End found no running process with the ID it was given.
var ErrGone = errors.New("no such process is running")

// ErrOther means End's process runs a program the caller didn't take for its own, and was left
// alone.
var ErrOther = errors.New("the process runs another program")

// ErrCantEnd means this system has no way written here to tell what program a process runs, so End
// ends nothing on it.
var ErrCantEnd = errors.New("ending a process by its ID isn't supported on this system")

// End ends the process pid if is, given the path of the program it runs, says the process is the
// caller's to end. It is how a daemon that can't be asked to stop is ended (#394): by its process,
// which sends it nothing.
//
// A process ID outlives its process only as a number, and the system gives it to another process
// later. So End never ends by the number alone: it takes hold of the process, looks at what it
// runs, and ends what it holds. On Windows that is a handle, and on Linux a pidfd, which both stay
// with the one process whatever becomes of its ID; a Linux that gives no pidfd (a kernel before
// 5.3, or a sandbox that refuses the call) ends nothing, with ErrCantEnd. macOS has neither, so there the look and the
// signal are two steps by ID, a few microseconds apart.
//
// On Unix the process is sent SIGTERM, which a daemon takes as a stop and shuts down from. Windows
// has no such signal for a process without a window, so it is terminated at once, and what it had
// not finished is lost.
//
// The path is the one the process was started from. A program replaced while it runs keeps its
// process: Linux then reports the path with " (deleted)" after it, and on Windows the installers
// rename the running file aside (mur.exe.old-...), so the path is the renamed one.
func End(pid int, is func(exe string) bool) error {
	if pid <= 1 { // 0 and negative numbers name process groups to kill(2), and 1 is init
		return ErrGone
	}
	if pid == os.Getpid() {
		return ErrOther
	}
	return end(pid, is)
}
