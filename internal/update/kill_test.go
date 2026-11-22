package update

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestKilledUpdateLeavesAWorkingBinary kills a running binary while it replaces itself, before each
// rename or link replace makes and after the last, and checks that its path still runs, as the old
// release or the whole new one (#944). The binary is a copy of this test binary, so on Windows it
// is a running image, which Windows won't let anything rename over or delete.
func TestKilledUpdateLeavesAWorkingBinary(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	// Loaders ignore bytes after the image, so this runs as well and still tells the two apart.
	newer := append(slices.Clip(old), "\nthe new release\n"...)
	newFile := filepath.Join(t.TempDir(), "new")
	if err := os.WriteFile(newFile, newer, 0o600); err != nil {
		t.Fatal(err)
	}
	name := "mur"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for point := 0; ; point++ {
		if point > 8 {
			t.Fatal("replace never finished")
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, old, 0o755); err != nil {
			t.Fatal(err)
		}
		finished := killAt(t, path, newFile, point)
		b, err := os.ReadFile(path)
		switch {
		case err != nil:
			t.Fatalf("killed at point %d: %v", point, err)
		case finished && !bytes.Equal(b, newer):
			t.Fatalf("finished, but %s isn't the new release", name)
		case !bytes.Equal(b, old) && !bytes.Equal(b, newer):
			t.Fatalf("killed at point %d: %s is neither release (%d bytes)", point, name, len(b))
		}
		if runtime.GOOS != "darwin" { // macOS checks signatures; the bytes compare above suffices there
			if out, err := exec.Command(path, "-test.run=^$").CombinedOutput(); err != nil {
				t.Fatalf("killed at point %d: %s doesn't run: %v\n%s", point, name, err, out)
			}
		}
		cleanup(path)
		entries, _ := os.ReadDir(filepath.Dir(path))
		var left []string
		for _, e := range entries {
			if e.Name() != name && e.Name() != name+".update.lock" {
				left = append(left, e.Name())
			}
		}
		if len(left) > 0 {
			t.Fatalf("killed at point %d, cleanup left %v", point, left)
		}
		if finished {
			if point < 2 {
				t.Fatalf("replace finished after only %d points", point)
			}
			return
		}
	}
}

// killAt runs the copy at path as TestReplaceKillHelper and kills it when it reaches point, or
// reports that it finished first.
func killAt(t *testing.T, path, newFile string, point int) (finished bool) {
	t.Helper()
	cmd := exec.Command(path, "-test.run=^TestReplaceKillHelper$")
	cmd.Env = append(os.Environ(), "MUR_TEST_REPLACE_KILL="+strconv.Itoa(point),
		"MUR_TEST_REPLACE_PATH="+path, "MUR_TEST_REPLACE_NEW="+newFile)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(time.Minute, func() { cmd.Process.Kill() })
	defer timer.Stop()
	var seen []string
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		line := lines.Text()
		seen = append(seen, line)
		if line == fmt.Sprintf("point %d", point) {
			cmd.Process.Kill()
			break
		}
		if rest, ok := strings.CutPrefix(line, "finished: "); ok {
			if rest != "<nil>" {
				t.Fatalf("replace failed: %s", rest)
			}
			finished = true
			break
		}
	}
	cmd.Wait()
	if !finished && !slices.Contains(seen, fmt.Sprintf("point %d", point)) {
		t.Fatalf("helper stopped before point %d: %q", point, seen)
	}
	return finished
}

// TestReplaceKillHelper is the binary TestKilledUpdateLeavesAWorkingBinary kills: it replaces
// itself, saying each point it reaches, and waits at the one it is to be killed at.
func TestReplaceKillHelper(t *testing.T) {
	kill := os.Getenv("MUR_TEST_REPLACE_KILL")
	if kill == "" {
		t.Skip("run by TestKilledUpdateLeavesAWorkingBinary")
	}
	at, _ := strconv.Atoi(kill)
	data, err := os.ReadFile(os.Getenv("MUR_TEST_REPLACE_NEW"))
	if err != nil {
		t.Fatal(err)
	}
	point := 0
	reach := func() {
		fmt.Println("point", point)
		if point == at {
			time.Sleep(time.Hour)
		}
		point++
	}
	step := func(op func(string, string) error) func(string, string) error {
		return func(a, b string) error {
			reach()
			return op(a, b)
		}
	}
	err = replace(os.Getenv("MUR_TEST_REPLACE_PATH"), data, runtime.GOOS, files{step(os.Rename), step(os.Link)})
	if err == nil {
		reach()
	}
	fmt.Println("finished:", err)
}
