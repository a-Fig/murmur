package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A reparse point that leads nowhere, as a OneDrive file not yet downloaded is, is the file or
// folder it stands for; a link or a junction, which leads somewhere, is a link.
func TestLinkIn(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}
	if linkIn(info, func() error { return nil }) || !plainFile(info) {
		t.Errorf("a plain file: link %v, plain %v", linkIn(info, func() error { return nil }), plainFile(info))
	}
	irregular := fakeInfo{info, os.ModeIrregular}
	fails := func() error { return os.ErrInvalid }
	reads := func() error { return nil }
	if runtime.GOOS == "windows" {
		if linkIn(irregular, fails) || !plainFile(irregular) {
			t.Error("a reparse point that leads nowhere isn't taken for the file it holds")
		}
		if !linkIn(irregular, reads) {
			t.Error("a junction isn't taken for a link")
		}
		if dirIrregular := (fakeInfo{info, os.ModeIrregular | os.ModeDir}); plainFile(dirIrregular) {
			t.Error("a folder that is a reparse point is taken for a file")
		}
	} else if linkIn(irregular, reads) || plainFile(irregular) {
		t.Error("off Windows an irregular entry is neither a link nor a file")
	}
}

// fakeInfo is info with mode in place of its own.
type fakeInfo struct {
	os.FileInfo
	mode os.FileMode
}

func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (f fakeInfo) IsDir() bool       { return f.mode.IsDir() }
