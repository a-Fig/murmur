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

// autoRotate checks at startup and daily. Only the verified owner's clone calls GitHub.
// A failed listing never replaces the last roster, and the next attempt checks again.
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
		}
		timer.Reset(d.checkForDepartures(ctx, list))
	}
}

func (d *Daemon) checkForDepartures(ctx context.Context, list func(context.Context, string) ([]string, error)) time.Duration {
	d.mu.Lock()
	verified := d.st.TeamOwnerChecked
	enabled := d.isOwnerLocked() && d.st.TeamOwner.Policy.EncryptionMode == "team"
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
		note, err = d.rotateRequest(ctx, logins, false)
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
