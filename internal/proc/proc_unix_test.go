//go:build !windows

package proc

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
)

// A flock belongs to the open file description, and a child process has a copy of the descriptor
// until it execs. Closing the lock has to release it anyway: the daemon's next start comes right
// after its last stop, with other children (git, run by other daemons in the same test process)
// forked in between.
func TestCloseReleasesTheLockWhileAChildStillHoldsTheDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	l, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	child.ExtraFiles = append(child.ExtraFiles, l.f) // the child keeps the open file description
	if err := child.Start(); err != nil {
		l.Close()
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()

	if _, err := Lock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("a second Lock while the first is held: %v; want ErrLocked", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Lock(path)
	if err != nil {
		t.Fatalf("Lock after Close, with a child still holding a copy of the descriptor: %v", err)
	}
	l2.Close()
}

// The same, as it happens in the daemon tests: processes starting all the time in the same process
// as the lock that is taken, closed and taken again.
func TestLockCanBeTakenAgainAtOnceWhileOtherProcessesStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	stop := make(chan struct{})
	var spawners sync.WaitGroup
	for range 3 {
		spawners.Add(1)
		go func() {
			defer spawners.Done()
			for {
				select {
				case <-stop:
					return
				default:
					exec.Command("true").Run()
				}
			}
		}()
	}
	defer func() { close(stop); spawners.Wait() }()

	for i := 0; i < 300; i++ {
		l, err := Lock(path)
		if err != nil {
			t.Fatalf("round %d: Lock: %v", i, err)
		}
		l.Close()
	}
}

// An inherited socket goes no further than the process it was handed to: the processes that one
// starts (the daemon's git and gh) don't get it (#255).
func TestInheritedIsNotPassedOn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// A descriptor as a process inherits it: not close-on-exec.
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	const env = "MURMUR_PROC_TEST_INHERITED"
	t.Setenv(env, strconv.Itoa(fd))
	got := Inherited(env, "page")
	if got == nil {
		t.Fatal("nothing inherited")
	}
	defer got.Close()
	if v, ok := syscall.Getenv(env); ok {
		t.Fatalf("%s still set to %q", env, v)
	}
	path := fmt.Sprintf("/dev/fd/%d", fd)
	if err := exec.Command("sh", "-c", "test -e "+path).Run(); err == nil {
		t.Fatalf("a child process has the inherited descriptor %d", fd)
	}
	if err := exec.Command("sh", "-c", "test -e /dev/fd/0").Run(); err != nil {
		t.Fatalf("the check can't see a child's descriptors: %v", err)
	}
}
