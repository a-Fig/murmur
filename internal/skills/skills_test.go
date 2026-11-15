package skills

import (
	"bytes"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// Each skill has the frontmatter agents require (name matching its folder, a description under
// OpenCode's 1024-character limit), carries the marker join recognises, and is named after the
// brand, so a rename that misses them fails here.
func TestSkills(t *testing.T) {
	all := All()
	if len(all) != 3 {
		t.Fatalf("got %d skills, want 3", len(all))
	}
	for _, s := range all {
		if !strings.HasPrefix(s.Name, brand.Name) {
			t.Errorf("%s: not named after %s", s.Name, brand.Name)
		}
		head, _, ok := strings.Cut(strings.TrimPrefix(string(s.Text), "---\n"), "\n---\n")
		if !ok || !bytes.HasPrefix(s.Text, []byte("---\n")) {
			t.Fatalf("%s: no frontmatter", s.Name)
		}
		var name, desc string
		for _, line := range strings.Split(head, "\n") {
			if v, ok := strings.CutPrefix(line, "name: "); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(line, "description: "); ok {
				desc = v
			}
		}
		if name != s.Name {
			t.Errorf("%s: frontmatter name %q", s.Name, name)
		}
		if desc == "" || len(desc) > 1024 || strings.Contains(desc, ": ") {
			t.Errorf("%s: description must be 1-1024 characters, with no \": \" that would break plain YAML: %q", s.Name, desc)
		}
		if !bytes.Contains(s.Text, []byte(Marker)) {
			t.Errorf("%s: lacks the marker", s.Name)
		}
	}
}
