package desktop

import (
	"strings"
	"testing"
)

// macOS locks the main thread at start only for `mur app`, wherever -C puts it.
func TestAppCommand(t *testing.T) {
	for args, want := range map[string]bool{
		"app":             true,
		"app --wait":      true,
		"-C dir app":      true,
		"-C=dir -C x app": true,
		"--C dir app":     true,
		"hook":            false,
		"daemon":          false,
		"-C app":          false,
		"-C":              false,
		"":                false,
		"ui app":          false,
	} {
		if got := appCommand(strings.Fields(args)); got != want {
			t.Errorf("%q: %v, want %v", args, got, want)
		}
	}
}
