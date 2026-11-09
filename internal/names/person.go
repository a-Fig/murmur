package names

import (
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// MaxPerson caps a person's name (#242).
const MaxPerson = 32

// reservedPeople are names a person can't take: they read as Murmur itself, as a role, or as
// everyone, in the header of a turn or a reply.
var reservedPeople = map[string]bool{
	"agent": true, "all": true, "bot": true, "claude": true, "cli": true, "codex": true, "everyone": true,
	"human": true, "me": true, "person": true, "system": true, "t3": true, "user": true,
	"you": true, brand.Name: true, brand.Command: true,
}

// ValidPerson reports whether name can be a person's: lowercase letters and digits in words joined
// by single dashes, 2 to MaxPerson characters. A session's name (an adjective and a noun from the
// lists) isn't one, so an address never reads as both. What reaches an agent's context from the bus
// is held to this too.
func ValidPerson(name string) bool {
	if len(name) < 2 || len(name) > MaxPerson || reservedPeople[name] || Valid(name) {
		return false
	}
	prev := '-' // so a leading dash fails
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && prev != '-':
		default:
			return false
		}
		prev = r
	}
	return prev != '-'
}

// PersonSlug turns a git user name into the name to suggest for its owner: Ada Lovelace into
// ada-lovelace. "" when nothing valid comes of it. Letters outside ASCII are left out, as bus
// addresses are ASCII.
func PersonSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case r < 0x80:
			dash = true
		}
	}
	out := b.String()
	if len(out) > MaxPerson {
		out = strings.TrimRight(out[:MaxPerson], "-")
	}
	if !ValidPerson(out) {
		return ""
	}
	return out
}
