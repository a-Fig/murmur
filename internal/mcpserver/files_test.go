package mcpserver

import (
	"path/filepath"
	"testing"
)

// A path a send names goes to the daemon as a full one. On Windows, Git Bash's name for a drive is
// put back, and a path from a root with no drive is left for the daemon to refuse: Windows would
// read /tmp/shot.png as a file on the current drive, which may be another session's.
func TestFullPath(t *testing.T) {
	here, err := filepath.Abs("shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if got := fullPath("shot.png", false); got != here {
		t.Errorf("a relative path: %q, want %q", got, here)
	}
	if got := fullPath("shot.png", true); got != here {
		t.Errorf("a relative path on Windows: %q, want %q", got, here)
	}
	for in, want := range map[string]string{
		"/tmp/shot.png":      "/tmp/shot.png",
		`\tmp\shot.png`:      `\tmp\shot.png`,
		"/cc/Users/shot.png": "/cc/Users/shot.png",
	} {
		if got := fullPath(in, true); got != want {
			t.Errorf("on Windows, %q: %q, want %q", in, got, want)
		}
	}
	// Abs leaves a drive's path as it is only where there are drives, so the rest is the same on
	// every system: the drive letter and the colon.
	if got := fullPath("/c/Users/ada/shot.png", true); filepath.ToSlash(got)[len(filepath.ToSlash(got))-len("c:/Users/ada/shot.png"):] != "c:/Users/ada/shot.png" {
		t.Errorf("Git Bash's /c/Users/ada/shot.png: %q", got)
	}
}
