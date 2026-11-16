package join

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/skills"
)

// skillsDir is where agent home h reads user skills, each a folder with a SKILL.md, "" for an
// agent with no documented place. The four were taken from each agent's documentation on
// 2026-11-08, not tried in a session: Claude Code and Pi read <dir>/skills, Codex
// $CODEX_HOME/skills, OpenCode ~/.config/opencode/skills. Muse Code and Grok document none.
func skillsDir(a agent, h agentHome) string {
	switch a.driver {
	case "claudeAgent", "codex", "opencode", "pi":
		if h.Dir != "" {
			return filepath.Join(h.Dir, "skills")
		}
	case "antigravity":
		// T3 links each instance's config/skills to the user's, ~/.gemini/config/skills, but only
		// where it finds no folder there: one join made would cut the instance off from the user's
		// own skills, so the skills go in the user's only.
		if h.Dir != "" && !h.T3Own {
			return filepath.Join(h.Dir, "config", "skills")
		}
	}
	return ""
}

// joinSkillsDir is skillsDir for join: none for Antigravity's homes unless Antigravity is
// installed in one of them, homes, as ~/.gemini is Gemini CLI's too (#741). Any of them will do:
// T3's instances read the skills in ~/.gemini through T3's link, and a T3 that runs Antigravity
// says nothing of ~/.gemini itself. Uninstall still uses skillsDir.
func (e Env) joinSkillsDir(a agent, h agentHome, homes []agentHome) string {
	if a.driver == "antigravity" && !slices.ContainsFunc(homes, e.hasAntigravity) {
		return ""
	}
	return skillsDir(a, h)
}

// installSkills writes Murmur's skills into dir, an agent's skills folder, and updates ones an
// older mur wrote. A SKILL.md of the same name without skills.Marker is a user's own, and is left
// alone with a note. Only called for an agent that is installed, whose home exists.
func installSkills(dir, label string) (did, notes []string, err error) {
	var wrote []string
	verb := "installed"
	defer func() {
		if len(wrote) > 0 {
			did = append(did, label+": "+verb+" the "+strings.Join(wrote, " and ")+" skills ("+dir+")")
		}
	}()
	for _, s := range skills.All() {
		path := filepath.Join(dir, s.Name, "SKILL.md")
		old, rerr := os.ReadFile(path)
		switch {
		case rerr == nil && bytes.Equal(old, s.Text):
			continue
		case rerr == nil && !bytes.Contains(old, []byte(skills.Marker)):
			notes = append(notes, label+": left "+path+" alone: a skill of that name is there that isn't "+brand.Product+"'s")
			continue
		case rerr == nil:
			verb = "updated"
		case !errors.Is(rerr, os.ErrNotExist):
			return did, notes, fmt.Errorf("%s: %w", path, rerr)
		}
		if err := writeAtomic(path, s.Text, 0o644); err != nil {
			return did, notes, fmt.Errorf("%s: %w", path, err)
		}
		wrote = append(wrote, s.Name)
	}
	return did, notes, nil
}

// skillsRemoval is how to take Murmur's skills out of agent home h by hand, "" where none are.
func skillsRemoval(a agent, h agentHome) string {
	dir := skillsDir(a, h)
	if dir == "" {
		return ""
	}
	var there []string
	for _, s := range skills.All() {
		if b, err := os.ReadFile(filepath.Join(dir, s.Name, "SKILL.md")); err == nil && bytes.Contains(b, []byte(skills.Marker)) {
			there = append(there, filepath.Join(dir, s.Name))
		}
	}
	if len(there) == 0 {
		return ""
	}
	return "delete " + strings.Join(there, " and ")
}
