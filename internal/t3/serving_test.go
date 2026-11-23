package t3

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The `t3` a headless thread is reached through comes from Claude Code's parent: the parent itself
// while it is a t3 on disk, the one on PATH when it is gone or can't be told, and none for a client
// some other program started, which isn't the thread (#780).
func TestServing(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "t3")
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t3Dir := filepath.Join(dir, "sub", "t3")
	if err := os.MkdirAll(t3Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	onPath := func(string) (string, error) { return "/usr/local/bin/t3", nil }
	noPath := func(string) (string, error) { return "", exec.ErrNotFound }
	for _, c := range []struct {
		name, parent string
		read         bool
		look         func(string) (string, error)
		cli          string
		thread       bool
	}{
		{"its t3", exe, true, onPath, exe, true},
		{"directory named t3 falls back to PATH", t3Dir, true, onPath, "/usr/local/bin/t3", true},
		{"t3.exe", exe + ".exe", true, onPath, "/usr/local/bin/t3", true},
		{"its t3, deleted", exe + " (deleted)", true, onPath, "/usr/local/bin/t3", true},
		{"node, the npm t3 perhaps", "/usr/bin/node", true, onPath, "/usr/local/bin/t3", true},
		{"unreadable", "", false, onPath, "/usr/local/bin/t3", true},
		{"unreadable, none on PATH", "", false, noPath, "", true},
		{"tmux", "/usr/bin/tmux", true, onPath, "", false},
	} {
		if cli, thread := Serving(c.parent, c.read, c.look); cli != c.cli || thread != c.thread {
			t.Errorf("%s: got %q, %v; want %q, %v", c.name, cli, thread, c.cli, c.thread)
		}
	}
}
