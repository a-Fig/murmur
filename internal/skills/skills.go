// Package skills holds the agent skills `mur join` installs for each agent that reads them: a basic
// one on when Murmur is worth a turn, a pro one on coordinating over several messages, and a
// manager one, followed only when the user makes the session the manager of a team of agents. The
// tools' descriptions and the MCP server's instructions say what each tool does; these carry the
// judgment of when to use them, loaded only when an agent needs it.
package skills

import (
	"embed"
	"path"
)

//go:embed */SKILL.md
var files embed.FS

// Marker is in every skill Murmur installs, so join can tell its own SKILL.md from a user's skill
// that happens to have the same name, which it leaves alone.
const Marker = "<!-- Installed by `mur join`"

// Skill is one skill: Name is its folder, Text its SKILL.md.
type Skill struct {
	Name string
	Text []byte
}

// All lists the skills, by name.
func All() []Skill {
	dirs, err := files.ReadDir(".")
	if err != nil {
		panic(err) // embedded at build time
	}
	var out []Skill
	for _, d := range dirs {
		b, err := files.ReadFile(path.Join(d.Name(), "SKILL.md"))
		if err != nil {
			panic(err)
		}
		out = append(out, Skill{Name: d.Name(), Text: b})
	}
	return out
}
