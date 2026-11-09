package testgit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A setting in the global config is seen before Isolate, and not after it.
func TestIsolateHidesTheGlobalConfig(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[murmur]\n\tchannel = prerelease\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "")
	read := func() (string, error) {
		cmd := exec.Command("git", "config", "--get", "murmur.channel")
		cmd.Dir = t.TempDir()
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if got, err := read(); err != nil || got != "prerelease" {
		t.Fatalf("before Isolate: %q, %v, want the global setting", got, err)
	}
	Isolate()
	got, err := read()
	if got != "" {
		t.Errorf("after Isolate git still reads %q", got)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("after Isolate: %v, want git's exit 1 for a key that isn't set", err)
	}
}
