package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/a-Fig/murmur/internal/bus"
)

// meshOwner remains pinned if the team ref disappears or is rewritten.
type meshOwner struct {
	Device string         `json:"device"`
	Key    string         `json:"key"`
	Seq    uint64         `json:"seq"`
	Policy bus.RepoPolicy `json:"policy"`
}

func (d *Daemon) canAdministerLocked() bool {
	return d.st.TeamOwner == nil || d.isOwnerLocked()
}

func (d *Daemon) isOwnerLocked() bool {
	o := d.st.TeamOwner
	return o != nil && o.Device == d.device && o.Key == bus.SignPublic(d.signKey)
}

func (d *Daemon) checkOwnerLocked() error {
	if d.canAdministerLocked() {
		return nil
	}
	return fmt.Errorf("only the mesh owner (%s) can rotate the team key", d.deviceCalledLocked(d.st.TeamOwner.Device))
}

// teamContinues checks the first-parent chain that Repo.Log will read. A merge with the previous
// owner record only on its second parent can't put an older policy back into effect.
func (d *Daemon) teamContinues(ctx context.Context, from, to string) (bool, error) {
	if from == to {
		return true, nil
	}
	out, err := d.repo.Git.Run(ctx, "", "log", "--no-show-signature", "--first-parent", "--reverse", "--format=%P", to, "^"+from, "--")
	parents := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	return len(parents) > 0 && parents[0] == from, err
}

// acceptTeamAdminLocked orders owner actions independently of grants written by teammates.
// A replayed epoch or policy carries its original sequence inside its seal.
func (d *Daemon) acceptTeamAdminLocked(r bus.TeamRecord, seal bus.Seal, root bool) bool {
	o := d.st.TeamOwner
	if root && o == nil && r.Type == bus.TeamEpoch && r.AdminSeq == 1 && r.Policy != nil {
		d.st.TeamOwner = &meshOwner{Device: seal.By, Key: seal.Key, Seq: 1, Policy: *r.Policy}
		if pin := d.st.Pins[seal.By]; pin == nil || pin.Sign != seal.Key {
			d.st.Pins[seal.By] = &keyPin{Sign: seal.Key}
			delete(d.peers, seal.By)
		}
		return true
	}
	if o == nil {
		return r.AdminSeq == 0
	}
	if r.Type == bus.TeamGrant {
		return true
	}
	if r.Policy != nil && r.Type != bus.TeamPolicy || o.Device != seal.By || o.Key != seal.Key || r.AdminSeq != o.Seq+1 {
		return false
	}
	o.Seq = r.AdminSeq
	if r.Policy != nil {
		o.Policy = *r.Policy
	}
	return true
}
