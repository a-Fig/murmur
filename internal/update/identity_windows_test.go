package update

import (
	"os"
	"path/filepath"
	"testing"
)

// Another daemon's install renames a new binary over the path while this one looks at it.
func TestARenameOverTheBinaryIsntRefusedWhileItIsLookedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mur.exe")
	if err := os.WriteFile(path, []byte("running"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := openIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", []byte("another clone's install"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatalf("the rename was refused while the binary was open: %v", err)
	}
	after, err := Identity(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Error("the file renamed into place is the same file")
	}
}
