package proc

import (
	"errors"
	"math"
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
		if _, err := Exe(pid); !errors.Is(err, ErrGone) {
			t.Errorf("Exe(%d) = %v, want ErrGone", pid, err)
		}
	}
	if uint64(math.MaxInt) > math.MaxUint32 {
		if _, err := Exe(math.MaxInt); !errors.Is(err, ErrGone) && !errors.Is(err, ErrNoExe) {
			t.Errorf("Exe(%d) = %v, want ErrGone or ErrNoExe", math.MaxInt, err)
		}
	}
}

func TestParseDarwinProcArgs2(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []byte
		want    string
		wantErr error
	}{
		{"path and args", append([]byte{1, 0, 0, 0}, []byte("/usr/local/bin/t3\x00--flag\x00")...), "/usr/local/bin/t3", nil},
		{"path without trailing NUL", append([]byte{1, 0, 0, 0}, []byte("/bin/sh")...), "/bin/sh", nil},
		{"empty buffer", nil, "", ErrGone},
		{"only argc", []byte{1, 0, 0, 0}, "", ErrGone},
		{"leading NUL empty path", []byte{1, 0, 0, 0, 0, 'a'}, "", ErrGone},
	} {
		got, err := parseDarwinProcArgs2(tc.in)
		if got != tc.want || !errors.Is(err, tc.wantErr) {
			t.Errorf("%s: parseDarwinProcArgs2 = (%q, %v), want (%q, %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}
