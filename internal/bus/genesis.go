package bus

import (
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

// A repo set up by refs alone (`mur init --refs-only`, #830) gets the two shared bus refs in their
// empty state, as one root commit each on the empty tree. A ref must point at a commit, so "empty"
// is a commit that every reader takes as nothing.

// NamesGenesis is the message of the names registry's first commit in such a repo: a snapshot of no
// names. It is the commit a daemon writes as a repair of a registry that held none, so every
// daemon, released ones included, reads it as an empty registry and writes the first name on top.
func NamesGenesis(at time.Time) (string, error) {
	r := NameRecord{V: Version, At: at.UTC()}
	return Encode(r, r.Subject())
}

// teamGenesis is the message of the team ref's first commit in such a repo. It isn't sealed, so a
// daemon passes over it as a record, and it holds no epoch: the team has no key yet. A daemon from
// #830 on makes the first epoch on top of it, as it does on a team ref that isn't there
// (daemon/team.go). One from before makes none on it.
var teamGenesis = "No team key yet\n\nThe first device to join makes it, on top of this commit. Written by `" +
	brand.Command + " init --refs-only`.\n"

// TeamGenesis is the team ref's first commit's message, for init to write.
func TeamGenesis() string { return teamGenesis }

// IsTeamGenesis reports whether a commit message is the team ref's empty state.
func IsTeamGenesis(msg string) bool { return strings.TrimSpace(msg) == strings.TrimSpace(teamGenesis) }
