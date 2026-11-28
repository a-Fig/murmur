package update

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/testgit"
)

func TestMain(m *testing.M) {
	if v := os.Getenv(standInEnv); v != "" && len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println("mur " + v) // a stand-in for a release binary (standIn)
		os.Exit(0)
	}
	testgit.Isolate()
	// The clones on this machine's record are read when going back (unheld): never the real one.
	dir, err := os.MkdirTemp("", "update-clones-")
	if err != nil {
		panic(err)
	}
	os.Setenv(clones.DirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// standInEnv is the version a stand-in binary prints (standIn); with it unset, a stand-in runs the
// tests, as a binary that says no version.
const standInEnv = "MUR_UPDATE_TEST_STAND_IN"

// standIn puts at path a binary that runs, a copy of this test binary, which prints
// "mur <version>" for `version` as a release does, by t.Setenv(standInEnv, version). It goes in
// beside path and is renamed into place, as an installer does.
func standIn(t *testing.T, path string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	tmp := path + ".new"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// exeName is a binary's file name in dir: Windows runs only a file with an extension it knows.
func exeName(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "mur.exe")
	}
	return filepath.Join(dir, "mur")
}
