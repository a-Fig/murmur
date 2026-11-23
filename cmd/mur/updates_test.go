package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `mur updates` sets the machine's choice in a global git config of the test's own, says what
// applies in this repo, and refuses anything but its four words (#1045).
func TestUpdatesCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	globalPath := filepath.Join(home, ".gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", globalPath)
	t.Setenv("MURMUR_NO_UPDATE", "")
	_, clone := cloneOf(t, t.TempDir())
	ctx := context.Background()
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runUpdates(ctx, append([]string{"-C", clone}, args...), &out)
		return out.String(), err
	}
	if out, err := run(); err != nil || !strings.HasPrefix(out, "Updates are on: stable releases") {
		t.Fatalf("alone: %q, %v", out, err)
	}
	if out, err := run("nightly"); err != nil || !strings.HasPrefix(out, "Updates are on, nightly") {
		t.Fatalf("nightly: %q, %v", out, err)
	}
	if b, _ := os.ReadFile(globalPath); !strings.Contains(string(b), "channel = prerelease") {
		t.Fatalf("global config:\n%s", b)
	}
	if out, err := exec.Command("git", "-C", clone, "config", "murmur.update", "false").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "Off here: this repo's git config sets murmur.update.") {
		t.Fatalf("with the repo's override: %q, %v", out, err)
	}
	if _, err := run("back"); err == nil {
		t.Error("went back to stable from a build that isn't a prerelease")
	}
	for _, bad := range []string{"stable", "Off", "true"} {
		if _, err := run(bad); err == nil {
			t.Errorf("took %q", bad)
		}
	}
	if _, err := run("on", "off"); err == nil {
		t.Error("took two")
	}
}
