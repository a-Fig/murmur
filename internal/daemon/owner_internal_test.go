package daemon

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

func TestMeshOwnerOrdersTeamAdministration(t *testing.T) {
	d, _ := keyRig(t)
	owner, teammate := bus.NewSignKey(), bus.NewSignKey()
	policy := bus.RepoPolicy{EncryptionMode: "team", AllowFiles: true}
	apply := func(r bus.TeamRecord, sign ed25519.PrivateKey, device string, root bool) {
		t.Helper()
		msg, err := bus.EncodeSealed(r, r.Subject(), sign, device, brand.TeamRef)
		if err != nil {
			t.Fatal(err)
		}
		d.applyTeamRecordLocked(bus.Commit{Message: msg}, root)
	}
	epoch := func(seq uint64) bus.TeamRecord {
		t.Helper()
		r, err := bus.NewEpoch(bus.NewBoxKey(), map[string]string{d.device: bus.BoxPublic(d.boxKey)}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		r.AdminSeq = seq
		return r
	}
	genesis := epoch(1)
	genesis.Policy = &policy
	apply(genesis, owner, "owner-0001", false)
	if d.st.TeamOwner != nil || len(d.st.Epochs) != 0 {
		t.Fatal("a non-root epoch made this mesh owned")
	}
	apply(genesis, owner, "owner-0001", true)
	apply(epoch(2), teammate, "peer-0001", false)
	apply(epoch(2), teammate, "owner-0001", false)
	apply(epoch(0), owner, "owner-0001", false)
	if d.st.TeamOwner == nil || d.st.TeamOwner.Key != bus.SignPublic(owner) || len(d.st.Epochs) != 1 {
		t.Fatal("the root did not pin the owner or an unauthorized epoch was accepted")
	}
	update := bus.TeamRecord{V: bus.Version, Type: bus.TeamPolicy, AdminSeq: 2, Policy: &policy}
	apply(update, teammate, "owner-0001", false)
	if d.st.TeamOwner.Seq != 1 {
		t.Fatal("accepted a policy with the wrong owner key")
	}
	apply(update, owner, "owner-0001", false)
	apply(epoch(3), owner, "owner-0001", false)
	apply(update, owner, "owner-0001", false)
	if d.st.TeamOwner.Seq != 3 || len(d.st.Epochs) != 2 {
		t.Fatalf("after rotation and replay, owner sequence %d and %d epochs", d.st.TeamOwner.Seq, len(d.st.Epochs))
	}
}

func TestAutomaticRotationSkipsNonOwnerDevices(t *testing.T) {
	d, _ := keyRig(t)
	d.st.TeamOwnerChecked = true
	d.st.TeamOwner = &meshOwner{Device: "owner-0001", Policy: bus.RepoPolicy{EncryptionMode: "team", AllowFiles: true}}
	delay := d.checkForDepartures(t.Context(), func(context.Context, string) ([]string, error) {
		t.Fatal("a teammate's daemon called GitHub to check collaborators")
		return nil, nil
	})
	if delay != rotationCheck {
		t.Fatalf("next check in %s, want %s", delay, rotationCheck)
	}
}

func TestLegacyOwnershipKeepsEarlierEpochs(t *testing.T) {
	d, _ := keyRig(t)
	owner := bus.Seal{By: "owner-0001", Key: bus.SignPublic(bus.NewSignKey())}
	peer := bus.Seal{By: "peer-0001", Key: bus.SignPublic(bus.NewSignKey())}
	epoch := bus.TeamRecord{Type: bus.TeamEpoch}
	if !d.acceptTeamAdminLocked("first", epoch, owner, false) ||
		!d.acceptTeamAdminLocked("rotation", epoch, peer, false) {
		t.Fatal("legacy epochs were rejected")
	}
	policy := d.st.TeamOwner.Policy
	checkpoint := bus.TeamRecord{Type: bus.TeamPolicy, AdminSeq: 1, Policy: &policy}
	if d.acceptTeamAdminLocked("untrusted", checkpoint, peer, false) {
		t.Fatal("a later epoch's creator sealed the legacy checkpoint")
	}
	if !d.acceptTeamAdminLocked("checkpoint", checkpoint, owner, false) {
		t.Fatal("the first epoch's creator could not seal the legacy checkpoint")
	}
	if d.acceptTeamAdminLocked("late", epoch, peer, false) {
		t.Fatal("accepted an unsequenced rotation after sealing ownership")
	}
}
