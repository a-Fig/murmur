package daemon

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
)

// Files that come to too much together are refused for their size before any of them is checked
// for secrets, which reads every byte (#1075). A key in the first file shows which came first: the
// refusal names the size, not the file with the key.
func TestFilesTooLargeTogetherAreRefusedBeforeTheyAreChecked(t *testing.T) {
	t.Parallel()
	key := append([]byte("AWS=AKIAIOSFODNN7EXAMPLE\n"), make([]byte, bus.MaxFile/2)...)
	half := make([]byte, bus.MaxFile-1)
	err := checkSendFiles([]sendFile{{"key.txt", key}, {"b.bin", half}, {"c.bin", half}})
	if err == nil || !strings.Contains(err.Error(), "MB together") {
		t.Errorf("files over the limit together, a key in the first: %v, want them refused for their size", err)
	}
	// Within the limits, the key is still found.
	if err := checkSendFiles([]sendFile{{"key.txt", key}}); err == nil || !strings.Contains(err.Error(), "key.txt") {
		t.Errorf("a file with a key, within the limits: %v, want it refused, naming the file", err)
	}
}
