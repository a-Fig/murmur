package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/a-Fig/murmur/internal/bus"
)

// A policy change names only the setting the owner asked to change. The daemon merges it with
// the policy read at the current team tip, so a retry preserves other settings changed meanwhile.
type repoPolicyChange struct {
	mode       *string
	allowFiles *bool
}

func (d *Daemon) setRepoMode(ctx context.Context, mode string) error {
	if !bus.ValidEncryptionMode(mode) {
		return fmt.Errorf("%q is not a repo encryption mode. Choose team, private or off.", mode)
	}
	return d.writeTeamAdmin(ctx, &teamAsk{policy: &repoPolicyChange{mode: &mode}})
}

func (d *Daemon) setRepoFilesAllowed(ctx context.Context, allow bool) error {
	return d.writeTeamAdmin(ctx, &teamAsk{policy: &repoPolicyChange{allowFiles: &allow}})
}

func (d *Daemon) nextRepoPolicyLocked(change repoPolicyChange) (bus.RepoPolicy, error) {
	if err := d.checkOwnerHistoryLocked(); err != nil {
		return bus.RepoPolicy{}, err
	}
	if !d.isOwnerLocked() {
		return bus.RepoPolicy{}, errors.New("only the mesh owner can change repo settings")
	}
	policy := d.st.TeamOwner.Policy
	if change.mode != nil {
		policy.EncryptionMode = *change.mode
	}
	if change.allowFiles != nil {
		policy.AllowFiles = *change.allowFiles
	}
	return policy, nil
}

// repoModeLocked defaults to team encryption until this device has an owner policy.
func (d *Daemon) repoModeLocked() string {
	if o := d.st.TeamOwner; o != nil {
		return o.Policy.EncryptionMode
	}
	return bus.EncryptionTeam
}
