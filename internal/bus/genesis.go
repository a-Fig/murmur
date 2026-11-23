package bus

import (
	"context"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

// Every `mur init` makes the two shared bus refs on the remote in their empty state, as one root
// commit each on the empty tree (#830): they are what join looks for. A ref must point at a commit,
// so "empty" is a commit that every reader takes as nothing.

// NamesGenesis is the message of the names registry's first commit: a snapshot of no names. It is
// the commit a daemon writes as a repair of a registry that held none, so every daemon, released
// ones included, reads it as an empty registry and writes the first name on top.
func NamesGenesis(at time.Time) (string, error) {
	r := NameRecord{V: Version, At: at.UTC()}
	return Encode(r, r.Subject())
}

// teamGenesis is the message of the team ref's first commit. It isn't sealed, so a daemon passes
// over it as a record, and it holds no epoch: the team has no key yet. A daemon from #830 on makes
// the first epoch on top of it, as it does on a team ref that isn't there (daemon/team.go). One
// from before makes none on it.
var teamGenesis = "No team key yet\n\nThe first device to join makes it, on top of this commit. Written by `" +
	brand.Command + " init`.\n"

// TeamGenesis is the team ref's first commit's message, for init to write.
func TeamGenesis() string { return teamGenesis }

// IsTeamGenesisMessage reports whether a commit message is the team genesis's. The message alone
// says nothing of where the commit sits; IsTeamGenesis does.
func IsTeamGenesisMessage(msg string) bool {
	return strings.TrimSpace(msg) == strings.TrimSpace(teamGenesis)
}

// IsTeamGenesis reports whether the commit sha is the team ref's empty state: a commit with no
// parent, on the empty tree, with the genesis message. A commit with that message on top of
// others is not, so a team ref whose history this device couldn't read is never taken for empty.
func (r *Repo) IsTeamGenesis(ctx context.Context, sha string) (bool, error) {
	c, err := r.ReadCommit(ctx, sha, 4<<10)
	if err != nil || !IsTeamGenesisMessage(c.Message) {
		return false, err
	}
	empty, err := r.EmptyTree(ctx)
	if err != nil {
		return false, err
	}
	out, err := r.Git.Run(ctx, "", "log", "-1", "--format=%T %P", sha, "--")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == empty, nil // the tree, and no parents after it
}
