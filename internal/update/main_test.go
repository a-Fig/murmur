package update

import (
	"os"
	"testing"

	"github.com/a-Fig/murmur/internal/testgit"
)

// shipped is keys.go's list. The tests run without it, so a key added there doesn't change what a
// test of something else sees; the signed tests pass keys of their own.
var shipped = releaseKeys

func TestMain(m *testing.M) {
	testgit.Isolate()
	releaseKeys = nil
	os.Exit(m.Run())
}
