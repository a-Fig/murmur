package daemon_test

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/daemon"
)

// What git writes for a loose ref is kept; anything else under the bus refs is removed (#644).
func TestRefFileOK(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{strings.Repeat("a1", 20) + "\n", true},
		{strings.Repeat("0f", 32) + "\n", true},
		{strings.Repeat("a1", 20) + "\r\n", true},
		{"ref: refs/murmur/mesh/x\n", true},
		{string(make([]byte, 41)), false},
		{"", false},
		{strings.Repeat("A1", 20) + "\n", false},
		{strings.Repeat("a1", 19) + "\n", false},
		{"ref: \n", false},
	} {
		if got := daemon.RefFileOK([]byte(c.in)); got != c.ok {
			t.Errorf("RefFileOK(%q) = %v, want %v", c.in, got, c.ok)
		}
	}
}
