package proc

import (
	"os"
	"runtime"
	"testing"
)

// Exe names this very process's program, the one case every system's answer can be checked for.
func TestExeOfThisProcess(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "linux", "darwin":
	default:
		t.Skip("no way to find a process's program on " + runtime.GOOS)
	}
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Exe(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(got)
	if err != nil {
		t.Fatalf("Exe gave %q, which can't be read: %v", got, err)
	}
	if !os.SameFile(a, b) {
		t.Errorf("Exe = %q, want the file %q", got, want)
	}
}

func TestExeOfNoProcess(t *testing.T) {
	for _, pid := range []int{0, 1, -5} {
		if _, err := Exe(pid); err == nil {
			t.Errorf("Exe(%d) gave no error", pid)
		}
	}
}
