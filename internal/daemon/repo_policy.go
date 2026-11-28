package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/a-Fig/murmur/internal/bus"
)

const teamAdminModes = 2

// teamAdminAutoRotate says a device enforces the policy's AutoRotate (#1285). Ownership goes only to
// a device that does: an owner from before it ignores Off, and re-signing the policy drops On.
const teamAdminAutoRotate = teamAdminFiles + 1

// teamPolicyVersion is the RepoPolicy fields this daemon caches (state.TeamPolicyV): 1 is AutoRotate.
const teamPolicyVersion = 1

var errRepoModeChanged = errors.New("the repo's encryption mode changed. Send the message again.")

// A policy change names only the setting the owner asked to change. The daemon merges it with
// the policy read at the current team tip, so a retry preserves other settings changed meanwhile.
type repoPolicyChange struct {
	mode       *string
	allowFiles *bool
	autoRotate *bool
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

func (d *Daemon) setRepoAutoRotate(ctx context.Context, on bool) error {
	return d.writeTeamAdmin(ctx, &teamAsk{policy: &repoPolicyChange{autoRotate: &on}})
}

func (d *Daemon) nextRepoPolicyLocked(change repoPolicyChange) (bus.RepoPolicy, error) {
	if err := d.checkOwnerHistoryLocked(); err != nil {
		return bus.RepoPolicy{}, err
	}
	if !d.isOwnerLocked() {
		return bus.RepoPolicy{}, errors.New("only the mesh owner can change repo settings")
	}
	policy := d.st.TeamOwner.Policy
	if change.mode != nil && *change.mode != policy.EncryptionMode {
		for _, dev := range sortedKeys(d.peers) {
			p := d.peers[dev]
			if !d.staleLocked(p) && p.pres.TeamAdmin < teamAdminModes {
				return bus.RepoPolicy{}, fmt.Errorf("update Murmur on %s before switching encryption modes", d.deviceCalledLocked(dev))
			}
		}
		policy.EncryptionMode = *change.mode
	}
	if change.allowFiles != nil {
		policy.AllowFiles = *change.allowFiles
	}
	if change.autoRotate != nil {
		policy.AutoRotate = *change.autoRotate
	}
	return policy, nil
}

// checkRepoWriteMode is used at the push slot, after a record was encoded.
func (d *Daemon) checkRepoWriteMode(stamp uint64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.repoModeStamp.Load() != stamp {
		return errRepoModeChanged
	}
	return d.checkOwnerHistoryLocked()
}

// repoModeLocked defaults to team encryption until this device has an owner policy.
func (d *Daemon) repoModeLocked() string {
	if o := d.st.TeamOwner; o != nil {
		return o.Policy.EncryptionMode
	}
	return bus.EncryptionTeam
}
