// Package proc holds the operating-system specifics of the daemon's process: the lock that
// keeps it to one per clone, and starting it detached from the session that launched it.
package proc

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("locked by another process")

// Held is an exclusive lock on a file, as Lock returns it.
type Held struct{ f *os.File }

// Close releases the lock, and at once: a lock that outlived its Close would make the next Lock
// fail with ErrLocked for a daemon that has already gone. The lock is also released if the
// process exits, however it exits.
func (l *Held) Close() error {
	unlock(l.f)
	return l.f.Close()
}

// A Handover is a file a started process inherits, such as the page's listening socket (#255), and
// the environment variable that tells the process the file's descriptor (a handle on Windows)
// there. The variable is set only on a start that really hands the file over, so a process never
// takes a number that isn't its file.
type Handover struct {
	File *os.File
	Env  string
}

// Inherited is the file a Handover named env gave this process, or nil. It unsets env first, so
// the process's own children don't take the number for a file of theirs, and on Unix marks the
// descriptor close-on-exec, so they don't get the file either.
//
// It is for listening sockets, the one kind of file handed over, and on Windows returns nil for a
// handle it can't take up as one.
func Inherited(env, name string) *os.File {
	v, ok := os.LookupEnv(env)
	if !ok {
		return nil
	}
	os.Unsetenv(env)
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil
	}
	return adopt(uintptr(n), name)
}

// withEnv is env with no entry for name, and then name=value unless value is empty.
func withEnv(env []string, name, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, name) {
			out = append(out, kv)
		}
	}
	if value != "" {
		out = append(out, name+"="+value)
	}
	return out
}
