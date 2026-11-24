package update

import (
	"os"
	"testing"

	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/testgit"
)

func TestMain(m *testing.M) {
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
