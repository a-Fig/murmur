package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// meshOwner remains pinned if the team ref disappears or is rewritten.
type meshOwner struct {
	Device string         `json:"device"`
	Key    string         `json:"key"`
	Seq    uint64         `json:"seq"`
	Tip    string         `json:"tip"` // most recent verified owner action
	Policy bus.RepoPolicy `json:"policy"`
}

const teamAdminTransfer = 1

func (d *Daemon) canAdministerLocked() bool {
	return d.st.TeamOwner == nil || d.isOwnerLocked()
}

func (d *Daemon) isOwnerLocked() bool {
	o := d.st.TeamOwner
	return o != nil && o.Device == d.device && o.Key == bus.SignPublic(d.signKey)
}

func (d *Daemon) checkOwnerLocked() error {
	if err := d.checkOwnerHistoryLocked(); err != nil {
		return err
	}
	if d.canAdministerLocked() {
		return nil
	}
	return fmt.Errorf("only the mesh owner (%s) can rotate the team key", d.deviceCalledLocked(d.st.TeamOwner.Device))
}

func (d *Daemon) checkOwnerHistoryLocked() error {
	if d.st.TeamTip != "" && !d.st.TeamOwnerChecked || d.st.TeamReplayThrough != "" {
		return errors.New("Murmur is still verifying the team history. Try again after it syncs.")
	}
	return nil
}

// teamContinues checks the first-parent chain that Repo.Log will read. A merge with the previous
// owner record only on its second parent can't put an older policy back into effect.
func (d *Daemon) teamContinues(ctx context.Context, from, to string) (bool, error) {
	if from == to {
		return true, nil
	}
	out, err := d.repo.Git.Run(ctx, "", "log", "--no-show-signature", "--no-notes", "--no-color", "--first-parent", "--reverse", "--format=%P", to, "^"+from, "--")
	parents := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	return len(parents) > 0 && parents[0] == from, err
}

// acceptTeamAdminLocked orders owner actions independently of grants written by teammates.
// A replayed epoch or policy carries its original sequence inside its seal.
func (d *Daemon) acceptTeamAdminLocked(sha string, r bus.TeamRecord, seal bus.Seal, root bool) bool {
	o := d.st.TeamOwner
	if root && o == nil && r.Type == bus.TeamEpoch && r.AdminSeq == 1 && r.Policy != nil {
		d.st.TeamOwner = &meshOwner{Device: seal.By, Key: seal.Key, Seq: 1, Tip: sha, Policy: *r.Policy}
		d.pinOwnerLocked(seal.By, seal.Key)
		return true
	}
	if o == nil {
		if r.Type == bus.TeamEpoch && r.AdminSeq == 0 {
			// The first legacy epoch signs the creator's device ID. A Git author name
			// and a presence label can match without authenticating either one.
			d.st.TeamOwner = &meshOwner{Device: seal.By, Key: seal.Key, Tip: sha,
				Policy: bus.RepoPolicy{EncryptionMode: "team", AllowFiles: true}}
			d.pinOwnerLocked(seal.By, seal.Key)
		}
		return r.AdminSeq == 0
	}
	if r.Type == bus.TeamGrant {
		return true
	}
	if o.Seq == 0 {
		// Legacy epochs and rosters precede the first policy checkpoint. The first epoch's
		// device is the only one that can seal the checkpoint and start the admin sequence.
		if r.AdminSeq == 0 {
			o.Tip = sha
			return true
		}
		if r.Type != bus.TeamPolicy {
			return false
		}
	}
	if r.Policy != nil && r.Type != bus.TeamPolicy || o.Device != seal.By || o.Key != seal.Key || r.AdminSeq != o.Seq+1 {
		return false
	}
	if r.Type == bus.TeamTransfer {
		if r.To == o.Device || r.NextOwnerKey == o.Key {
			return false
		}
		o.Device, o.Key = r.To, r.NextOwnerKey
		d.pinOwnerLocked(o.Device, o.Key)
	}
	o.Seq, o.Tip = r.AdminSeq, sha
	if r.Policy != nil {
		o.Policy = *r.Policy
	}
	return true
}

func (d *Daemon) pinOwnerLocked(device, key string) {
	if pin := d.st.Pins[device]; pin == nil || pin.Sign != key {
		d.st.Pins[device] = &keyPin{Sign: key}
		delete(d.peers, device)
		delete(d.refusedSHA, device)
	}
}

// ownerTransferLocked binds the recipient's current verified device key to the transfer.
// The receiving daemons use that key even if they have never seen the recipient's presence.
func (d *Daemon) ownerTransferLocked(to string, at time.Time) (bus.TeamRecord, error) {
	if err := d.checkOwnerHistoryLocked(); err != nil {
		return bus.TeamRecord{}, err
	}
	if d.st.TeamOwner == nil {
		return bus.TeamRecord{}, errors.New("this mesh has no owner to transfer")
	}
	if !d.isOwnerLocked() {
		return bus.TeamRecord{}, errors.New("only the mesh owner can transfer ownership")
	}
	if to == d.device {
		return bus.TeamRecord{}, errors.New("this device already owns the mesh")
	}
	pin := d.st.Pins[to]
	if pin == nil || pin.Box == "" {
		return bus.TeamRecord{}, errors.New("Murmur has no verified key for that device. Ask its user to come online first.")
	}
	for _, dev := range sortedKeys(d.peers) {
		p := d.peers[dev]
		if (dev == to || !d.staleLocked(p)) && p.pres.TeamAdmin < teamAdminTransfer {
			return bus.TeamRecord{}, fmt.Errorf("update Murmur on %s before transferring ownership", d.deviceCalledLocked(dev))
		}
	}
	if d.peers[to] == nil {
		return bus.TeamRecord{}, errors.New("ask the new owner to come online in Murmur before transferring ownership")
	}
	return bus.TeamRecord{V: bus.Version, Type: bus.TeamTransfer, To: to, NextOwnerKey: pin.Sign, At: at}, nil
}

func (d *Daemon) transferOwner(ctx context.Context, to string) error {
	if !bus.ValidDevice(to) {
		return errors.New("choose the new owner's device ID from mur who")
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := d.pollOnce(ctx); err != nil {
			return err
		}
		_, pushed, err := d.teamWork(ctx, &teamAsk{transferTo: to})
		if err != nil || pushed {
			return err
		}
	}
	return fmt.Errorf("%s kept moving. Try again.", brand.TeamRef)
}
