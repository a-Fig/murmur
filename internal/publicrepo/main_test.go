package publicrepo

import (
	"os"
	"testing"

	"github.com/a-Fig/murmur/internal/testgit"
)

func TestMain(m *testing.M) {
	testgit.Isolate()
	os.Exit(m.Run())
}
