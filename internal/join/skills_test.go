package join

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/skills"
)

// installSkills writes each skill once, rewrites one an older mur wrote, and leaves a user's own
// skill of the same name alone.
func TestInstallSkills(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	did, notes, err := installSkills(dir, "Claude Code")
	if err != nil || len(did) != 1 || len(notes) != 0 {
		t.Fatalf("first: did=%q notes=%q err=%v", did, notes, err)
	}
	if did, notes, err := installSkills(dir, "Claude Code"); err != nil || len(did) != 0 || len(notes) != 0 {
		t.Errorf("second: did=%q notes=%q err=%v", did, notes, err)
	}

	old := filepath.Join(dir, "murmur", "SKILL.md")
	write(t, old, "---\nname: murmur\n---\n"+skills.Marker+" an older version -->\n")
	mine := filepath.Join(dir, "murmur-pro", "SKILL.md")
	write(t, mine, "---\nname: murmur-pro\n---\nmy own\n")
	did, notes, err = installSkills(dir, "Claude Code")
	if err != nil || len(did) != 1 || !strings.Contains(did[0], "updated the murmur skills") || len(notes) != 1 || !strings.Contains(notes[0], mine) {
		t.Errorf("did=%q notes=%q err=%v", did, notes, err)
	}
	if read(t, mine) != "---\nname: murmur-pro\n---\nmy own\n" {
		t.Error("overwrote the user's own skill")
	}
	if r := skillsRemoval(agents[0], agentHome{Dir: filepath.Dir(dir)}); !strings.Contains(r, filepath.Join(dir, "murmur")) || strings.Contains(r, "murmur-pro") {
		t.Errorf("removal names only Murmur's own: %q", r)
	}
}

// Agents with no documented skills folder get none.
func TestSkillsDirOnlyForAgentsThatReadThem(t *testing.T) {
	for _, a := range agents {
		got := skillsDir(a, agentHome{Dir: "/x"})
		want := map[string]bool{"claudeAgent": true, "codex": true, "opencode": true, "pi": true, "antigravity": true, "cursor": true}[a.driver]
		if (got != "") != want {
			t.Errorf("%s: %q", a.driver, got)
		}
	}
	// T3 links its Antigravity homes' skills folder to the user's, and only where none is there.
	if got := skillsDir(agents[agentIndex("antigravity")], agentHome{Dir: "/x", T3Own: true}); got != "" {
		t.Errorf("a T3 Antigravity home: %q", got)
	}
}
