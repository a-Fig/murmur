package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Windows refuses a rename over a file another process has open, as each daemon's watch has the
// binary for a moment every few seconds: the install's rename is tried again until it is let go.
func TestARenameOverAnOpenBinaryIsTriedAgain(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows refuses a rename over an open file")
	}
	path := filepath.Join(t.TempDir(), "mur.exe")
	if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", []byte("another clone's install"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) // another daemon's look
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err == nil || !busy(err) {
		f.Close()
		t.Fatalf("a rename over the open file: %v, want it refused, or this tests nothing", err)
	}
	time.AfterFunc(300*time.Millisecond, func() { f.Close() })
	if err := renameRetried(path+".new", path); err != nil {
		t.Fatalf("the rename wasn't tried until the file was let go: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "another clone's install" {
		t.Errorf("in place: %q", b)
	}
}
