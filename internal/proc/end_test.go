package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// endChildEnv marks this test binary started again as a process for End to end.
const endChildEnv = "MURMUR_TEST_END_CHILD"

func TestEndChild(t *testing.T) {
	if os.Getenv(endChildEnv) == "" {
		t.Skip("only as the child of another test")
	}
	time.Sleep(time.Minute)
}

// startEndChild starts the child, and returns it with a channel closed when it has exited.
func startEndChild(t *testing.T) (*exec.Cmd, chan struct{}) {
	t.Helper()
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("End isn't written for this system")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEndChild$")
	cmd.Env = append(os.Environ(), endChildEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	return cmd, done
}

// A process whose program the caller takes for its own is ended, and End is given the path it runs.
func TestEndEndsAProcessOfTheCallersProgram(t *testing.T) {
	cmd, done := startEndChild(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	same := func(exe string) bool {
		seen = exe
		return strings.EqualFold(filepath.Base(exe), filepath.Base(self))
	}
	if err := End(cmd.Process.Pid, same); err != nil {
		t.Fatalf("End: %v (it saw the program %q)", err, seen)
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("the process was not ended (it runs %q)", seen)
	}
	if !filepath.IsAbs(seen) {
		t.Errorf("End named the program %q, want its whole path", seen)
	}
}

// A process that runs some other program is left alone: its ID may have been the daemon's once.
func TestEndLeavesAnotherProgramAlone(t *testing.T) {
	cmd, done := startEndChild(t)
	err := End(cmd.Process.Pid, func(string) bool { return false })
	if !errors.Is(err, ErrOther) {
		t.Fatalf("End: %v, want ErrOther", err)
	}
	select {
	case <-done:
		t.Fatal("the process ended")
	case <-time.After(300 * time.Millisecond):
	}
}

// An ID no process has, this process's own, and the numbers that name more than one process to the
// system all end nothing.
func TestEndEndsNothingByABadID(t *testing.T) {
	cmd, done := startEndChild(t)
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	<-done // reaped, so the ID names no process
	yes := func(string) bool { return true }
	no := func(string) bool { return false } // so that an ID the system has given out again ends nothing
	if err := End(pid, no); !errors.Is(err, ErrGone) && !errors.Is(err, ErrOther) {
		t.Errorf("a process that exited: %v, want ErrGone", err)
	}
	for _, pid := range []int{0, -1, 1} {
		if err := End(pid, yes); !errors.Is(err, ErrGone) {
			t.Errorf("End(%d): %v, want ErrGone", pid, err)
		}
	}
	if err := End(os.Getpid(), yes); !errors.Is(err, ErrOther) {
		t.Errorf("this process: %v, want ErrOther", err)
	}
}
