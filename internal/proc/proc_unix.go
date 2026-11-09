//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Lock takes an exclusive lock on path, held until it is closed or the process exits, however it
// exits.
func Lock(path string) (*Held, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Held{f}, nil
}

// unlock releases the flock itself instead of leaving it to the descriptor's close. A flock belongs
// to the open file description, not to the descriptor, so closing the descriptor releases it only
// if no other descriptor refers to the same description. A process that forks (to run git, say)
// gives its child a copy of every descriptor, and the copy lives until the child's exec closes it,
// which can be a while on a busy machine. A lock closed in that window stays held, and a daemon
// started a moment later is refused with ErrLocked: the daemon tests restart one device while the
// other devices' daemons, in the same process, keep running git (the macOS runner, 2026-10-30).
// LOCK_UN on any one descriptor releases the lock for all of them.
func unlock(f *os.File) {
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// adopt takes up an inherited descriptor. An inherited descriptor isn't close-on-exec, so every
// process this one starts (git, gh, and what they start) would get it too, and one that outlived
// the daemon would keep the page's port bound, with the power to accept the page's requests.
func adopt(fd uintptr, name string) *os.File {
	syscall.CloseOnExec(int(fd))
	return os.NewFile(fd, name)
}

// Unpoll is for Windows, where a socket stays tied to the completion port of the process that
// first served it. A descriptor here takes no such tie with it.
func Unpoll(*os.File) error { return nil }

// StartDetached starts a program in its own session, so it outlives the process that started
// it and has no terminal to prompt on. A hand, if not nil, is passed on as descriptor 3.
func StartDetached(path string, args []string, dir string, out *os.File, hand *Handover) (*os.Process, error) {
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if hand != nil {
		cmd.ExtraFiles = []*os.File{hand.File}
		cmd.Env = withEnv(os.Environ(), hand.Env, "3")
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
