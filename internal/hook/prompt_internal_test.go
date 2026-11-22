package hook

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/proto"
)

// The hook sends the start of a prompt (#611), never past proto.MaxPrompt and never mid-character.
func TestPromptStart(t *testing.T) {
	if got := promptStart("short"); got != "short" {
		t.Errorf("promptStart(short) = %q", got)
	}
	long := strings.Repeat("a", proto.MaxPrompt-1) + "é" + strings.Repeat("b", 100)
	got := promptStart(long)
	if len(got) > proto.MaxPrompt || !utf8.ValidString(got) || got != strings.Repeat("a", proto.MaxPrompt-1) {
		t.Errorf("promptStart cut %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}
