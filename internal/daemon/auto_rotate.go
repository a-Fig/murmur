package daemon

import (
	"context"
	"time"

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

func (d *Daemon) checkForDepartures(ctx context.Context, list func(context.Context, string) ([]string, error)) time.Duration {
	d.mu.Lock()
	verified := d.st.TeamOwnerChecked && d.st.TeamReplayThrough == ""
	enabled := d.isOwnerLocked() && d.st.TeamOwner.Policy.EncryptionMode == "team" && d.st.TeamOwner.Policy.AutoRotate
	d.mu.Unlock()
	if !verified {
		return rotationRetry
	}
	if !enabled {
		return rotationCheck
	}
	logins, err := list(ctx, d.repoAddr)
	if err == nil {
		var note string
		note, err = d.rotateRequest(ctx, logins, false, true)
		if err == nil {
			d.log.Print("automatic team key check: " + note)
			return rotationCheck
		}
	}
	if ctx.Err() == nil {
		d.log.Printf("automatic team key check failed: %v", err)
	}
	return rotationRetry
}
