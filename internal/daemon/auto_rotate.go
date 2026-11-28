package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/collaborators"
)

const (
	rotationCheck = 24 * time.Hour
	rotationRetry = time.Hour
)

// autoRotate checks at startup and daily, once the owner has turned it on in the repo policy
// (#1285; off by default). Only the verified owner's clone calls GitHub, and with it off, nothing
// does. A failed listing never replaces the last roster, and the next attempt checks again.
// rotateKick brings the next check forward when the owner or the setting changes.
func (d *Daemon) autoRotate(ctx context.Context) {
	list := d.opt.ListCollaborators
	if list == nil {
		list = collaborators.List
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-d.rotateKick:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timer.Reset(d.checkForDepartures(ctx, list))
	}
}

func (d *Daemon) kickAutoRotate() {
	select {
	case d.rotateKick <- struct{}{}:
	default:
	}
}

// checkForDepartures runs one automatic check, and says when the next is due. A check whose
// authorization or epoch moved under it (errAutoStale) starts again with a fresh lookup, as its
// sample of who can push was taken for a key or a setting that no longer holds.
func (d *Daemon) checkForDepartures(ctx context.Context, list func(context.Context, string) ([]string, error)) time.Duration {
	for attempt := 0; ; attempt++ {
		next, err := d.checkOnce(ctx, list)
		if !errors.Is(err, errAutoStale) {
			return next
		}
		d.log.Print("automatic team key check: the owner, the repo settings or the team key changed during the check, so it starts again")
		if attempt == 2 {
			return rotationRetry
		}
	}
}

func (d *Daemon) checkOnce(ctx context.Context, list func(context.Context, string) ([]string, error)) (time.Duration, error) {
	// A wake-up sent before this check reads the state is answered by it, not by another lookup.
	select {
	case <-d.rotateKick:
	default:
	}
	d.mu.Lock()
	verified := d.st.TeamOwnerChecked && d.st.TeamReplayThrough == ""
	check, enabled := d.autoCheckLocked()
	if !verified {
		d.autoWaitVerify = true // autoVerifiedLocked wakes the loop once the history is verified
	}
	// The lookup is a sample of who can push for this key. A key made while it runs, by hand with
	// mur rotate say, starts the check again (Sol's re-review of #1289): its roster was taken later,
	// and comparing an older sample with it rotated a second time.
	check.epoch, _ = d.epochRefLocked()
	lookup, cancel := context.WithCancel(ctx)
	defer cancel()
	if enabled {
		d.autoCancel = cancel
	}
	d.mu.Unlock()
	if !verified {
		return rotationRetry, nil
	}
	if !enabled {
		return rotationCheck, nil
	}
	logins, err := list(lookup, d.repoAddr)
	d.mu.Lock()
	d.autoCancel = nil
	d.mu.Unlock()
	if ctx.Err() != nil {
		return rotationRetry, ctx.Err()
	}
	if lookup.Err() != nil {
		// autoRotateChangedLocked stopped the lookup. Whatever it returned was asked for under
		// settings that no longer hold.
		return 0, errAutoStale
	}
	if err == nil {
		var note string
		note, err = d.rotateRequest(ctx, logins, false, &check)
		if errors.Is(err, errAutoStale) {
			return 0, err
		}
		if err == nil {
			d.log.Print("automatic team key check: " + note)
			return rotationCheck, nil
		}
	}
	if ctx.Err() == nil {
		d.log.Printf("automatic team key check failed: %v", err)
	}
	return rotationRetry, err
}

// autoCheck is what one automatic check was authorized by: the owner it ran as, and autoRev, which
// moves whenever the owner, the encryption mode or AutoRotate changes. epoch is the key the lookup
// was started for; once the check has decided, roster is that epoch's roster record
// (epochRefLocked) it decided on (Sol's review of #1289: a decision taken for one epoch was applied
// to the next, made by a mode change).
type autoCheck struct {
	owner, key    string
	rev           uint64
	epoch, roster string
}

// errAutoStale says what an automatic check was authorized by, or decided on, no longer holds.
var errAutoStale = errors.New("the owner, the repo settings or the team key changed during the automatic check")

// autoCheckLocked is the authorization for an automatic check, if one may run now: the team history
// is verified, and this device owns a mesh in Team mode with AutoRotate on. Caller holds mu.
func (d *Daemon) autoCheckLocked() (autoCheck, bool) {
	o := d.st.TeamOwner
	if !d.st.TeamOwnerChecked || d.st.TeamReplayThrough != "" || !d.isOwnerLocked() ||
		o.Policy.EncryptionMode != bus.EncryptionTeam || !o.Policy.AutoRotate {
		return autoCheck{}, false
	}
	return autoCheck{owner: o.Device, key: o.Key, rev: d.autoRev}, true
}

// autoAuthHoldsLocked says c's authorization is still the current one. Caller holds mu.
func (d *Daemon) autoAuthHoldsLocked(c autoCheck) bool {
	cur, ok := d.autoCheckLocked()
	return ok && cur.owner == c.owner && cur.key == c.key && cur.rev == c.rev
}

// autoCheckHoldsLocked says c's authorization, and the epoch and roster it decided on, still hold.
// teamWork asks before every automatic write. Caller holds mu.
func (d *Daemon) autoCheckHoldsLocked(c autoCheck) bool {
	epoch, roster := d.epochRefLocked()
	return d.autoAuthHoldsLocked(c) && epoch == c.epoch && roster == c.roster
}

// epochRefLocked names the current epoch and its roster record, "" for none. Caller holds mu.
func (d *Daemon) epochRefLocked() (epoch, roster string) {
	ep := d.epochLocked()
	if ep == nil {
		return "", ""
	}
	if ep.Roster != nil {
		b, _ := json.Marshal(ep.Roster)
		sum := sha256.Sum256(b)
		roster = hex.EncodeToString(sum[:])
	}
	return ep.ID, roster
}

// autoRotateChangedLocked is called when the owner, the encryption mode or AutoRotate changes. A
// check in flight was authorized by what was there before: its lookup is stopped, and its writes
// will be refused, and the loop is woken to check again by what is there now. A check waiting for
// keys waits no longer: the check this wakes waits again if it still needs them. Caller holds mu.
func (d *Daemon) autoRotateChangedLocked() {
	d.autoRev++
	d.autoWaitKeys = false
	if d.autoCancel != nil {
		d.autoCancel()
		d.autoCancel = nil
	}
	d.kickAutoRotate()
}

// autoVerifiedLocked is called when the team history has been verified to its tip. A check put off
// for that runs now, if one may: the wake-up that would have started it can come, and be spent,
// between two pages of the replay (Sol's review of #1289). Caller holds mu.
func (d *Daemon) autoVerifiedLocked() {
	if d.autoWaitVerify {
		d.autoWaitVerify = false
		if _, ok := d.autoCheckLocked(); ok {
			d.kickAutoRotate()
		}
	}
}

// autoKeysLocked is called when this device is granted team keys. A check that would have rotated
// without every key runs now, if this device holds them all and a check may still run: a new owner,
// just handed the mesh, can be a grant behind the key it was made for, and waits for that grant, not
// the hour's retry (test-linux on 8034d2c9). Only keys end that wait. A replay of anything else on
// the team ref, a grant to another device say, leaves it, or a busy ref looked up the collaborators
// again on every grant while the key was missing (Sol's third review of #1289). Caller holds mu.
func (d *Daemon) autoKeysLocked() {
	if !d.autoWaitKeys || d.missingTeamKeyLocked() {
		return
	}
	d.autoWaitKeys = false
	if !d.st.TeamOwnerChecked || d.st.TeamReplayThrough != "" {
		d.autoWaitVerify = true // a grant comes in a replay, and the check waits for its end
		return
	}
	if _, ok := d.autoCheckLocked(); ok {
		d.kickAutoRotate()
	}
}

// missingTeamKeyLocked says this device lacks the key of some epoch in the team history, so it can't
// rotate: the new key's grants would leave that one out. Caller holds mu.
func (d *Daemon) missingTeamKeyLocked() bool {
	for _, e := range d.st.Epochs {
		if d.teamKeys[e.ID] == nil {
			return true
		}
	}
	return false
}
