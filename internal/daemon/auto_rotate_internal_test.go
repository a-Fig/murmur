package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// autoRig is a daemon that owns a Team mesh with automatic rotation on, its history verified, and
// one epoch whose key it holds.
func autoRig(t *testing.T) *Daemon {
	t.Helper()
	d, _ := keyRig(t)
	d.signKey = bus.NewSignKey()
	d.st.TeamOwnerChecked = true
	d.st.TeamOwner = &meshOwner{Device: d.device, Key: bus.SignPublic(d.signKey), Seq: 1,
		Policy: bus.RepoPolicy{EncryptionMode: bus.EncryptionTeam, AllowFiles: true, AutoRotate: true}}
	d.rotateKick = make(chan struct{}, 1)
	withEpoch(d, true)
	return d
}

// ownerRecord applies r as the owner's next admin action, sealed with its key.
func ownerRecord(t *testing.T, d *Daemon, r bus.TeamRecord) {
	t.Helper()
	r.V, r.AdminSeq, r.At = bus.Version, d.st.TeamOwner.Seq+1, time.Now().UTC()
	if !d.acceptTeamAdminLocked("sha-"+r.Type, r, bus.Seal{By: d.device, Key: bus.SignPublic(d.signKey)}, false) {
		t.Fatalf("the owner's %s was refused", r.Type)
	}
}

// A collaborator lookup that is in flight when the owner turns automatic rotation off, switches out
// of Team mode, or hands the mesh to another device, is stopped, and what it returns writes nothing:
// the check starts again under the new settings, which don't allow one (Sol's review of #1289).
func TestAnAutomaticCheckInFlightStopsWhenItsAuthorizationChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, d *Daemon)
	}{
		{"turned off", func(t *testing.T, d *Daemon) {
			ownerRecord(t, d, bus.TeamRecord{Type: bus.TeamPolicy, Policy: &bus.RepoPolicy{EncryptionMode: bus.EncryptionTeam, AllowFiles: true}})
		}},
		{"out of Team mode", func(t *testing.T, d *Daemon) {
			ownerRecord(t, d, bus.TeamRecord{Type: bus.TeamPolicy, Policy: &bus.RepoPolicy{EncryptionMode: bus.EncryptionPrivate, AllowFiles: true, AutoRotate: true}})
		}},
		{"transferred", func(t *testing.T, d *Daemon) {
			ownerRecord(t, d, bus.TeamRecord{Type: bus.TeamTransfer, To: "peer-0001", NextOwnerKey: bus.SignPublic(bus.NewSignKey())})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := autoRig(t)
			lookups := 0
			var lookup context.Context
			delay := d.checkForDepartures(t.Context(), func(ctx context.Context, _ string) ([]string, error) {
				lookups++
				if lookups > 1 {
					t.Fatal("looked up collaborators again after the change")
				}
				lookup = ctx
				d.mu.Lock()
				tc.change(t, d)
				d.mu.Unlock()
				return []string{"alice"}, nil // a fixture that ignores the cancellation
			})
			if lookups != 1 || lookup.Err() == nil {
				t.Fatalf("%d lookups; the lookup in flight was stopped: %v", lookups, lookup.Err() != nil)
			}
			if delay != rotationCheck || len(d.st.Epochs) != 1 || d.st.Epochs[0].Roster != nil {
				t.Fatalf("next check in %s, %d epochs, roster %v", delay, len(d.st.Epochs), d.st.Epochs[0].Roster != nil)
			}
		})
	}
}

// Every automatic write is checked, under teamMu, against what authorized the check and the epoch
// and roster it decided on. A decision for one epoch is never applied to the next.
func TestAnAutomaticWriteIsRefusedOnceItsDecisionIsStale(t *testing.T) {
	decided := func(d *Daemon) autoCheck {
		t.Helper()
		d.mu.Lock()
		defer d.mu.Unlock()
		c, ok := d.autoCheckLocked()
		if !ok {
			t.Fatal("the fixture can't run an automatic check")
		}
		c.epoch, c.roster = d.epochRefLocked()
		return c
	}
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, d *Daemon)
	}{
		{"setting off and on again", func(t *testing.T, d *Daemon) {
			on := d.st.TeamOwner.Policy
			off := on
			off.AutoRotate = false
			ownerRecord(t, d, bus.TeamRecord{Type: bus.TeamPolicy, Policy: &off})
			ownerRecord(t, d, bus.TeamRecord{Type: bus.TeamPolicy, Policy: &on})
		}},
		{"a new epoch", func(t *testing.T, d *Daemon) { withEpoch(d, true) }},
		{"a new roster", func(t *testing.T, d *Daemon) {
			ep := d.epochLocked()
			r, err := bus.NewRoster(ep.ID, ep.Pub, []string{"alice"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			ep.Roster = &r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := autoRig(t)
			c := decided(d)
			d.mu.Lock()
			if !d.autoCheckHoldsLocked(c) {
				t.Fatal("the decision was stale before anything changed")
			}
			tc.change(t, d)
			holds := d.autoCheckHoldsLocked(c)
			d.mu.Unlock()
			if holds {
				t.Fatal("the decision still holds after the change")
			}
			for _, ask := range []*teamAsk{{rotate: true, auto: &c}, {collaborators: []string{"alice"}, firstRoster: true, auto: &c}} {
				if _, pushed, err := d.teamWork(t.Context(), ask); !errors.Is(err, errAutoStale) || pushed {
					t.Fatalf("pushed %v, %v; want errAutoStale", pushed, err)
				}
			}
		})
	}
}

// A check put off while the team history was being verified runs once it is verified: the wake-up
// that the owner's change sent can be spent between two pages of the replay.
func TestACheckPutOffDuringVerificationRunsAfterIt(t *testing.T) {
	d := autoRig(t)
	d.st.TeamReplayThrough = "target"
	if delay := d.checkForDepartures(t.Context(), func(context.Context, string) ([]string, error) {
		t.Fatal("looked up collaborators before the history was verified")
		return nil, nil
	}); delay != rotationRetry {
		t.Fatalf("next check in %s, want %s", delay, rotationRetry)
	}
	select {
	case <-d.rotateKick:
		t.Fatal("woken before the history was verified")
	default:
	}
	d.st.TeamReplayThrough = ""
	d.teamVerifiedLocked()
	select {
	case <-d.rotateKick:
	default:
		t.Fatal("the check put off during verification wasn't woken")
	}
	d.teamVerifiedLocked()
	select {
	case <-d.rotateKick:
		t.Fatal("woken twice for one check put off")
	default:
	}
}

// AutoRotate is part of the owner's signed policy: a teammate can't turn it on, a seal doesn't
// survive the field being changed, and a replay of the history from its root reads it back.
func TestAutoRotateIsSignedByTheOwner(t *testing.T) {
	d, _ := keyRig(t)
	owner, teammate := bus.NewSignKey(), bus.NewSignKey()
	var history []bus.Commit
	seal := func(r bus.TeamRecord, sign ed25519.PrivateKey, device string) string {
		t.Helper()
		msg, err := bus.EncodeSealed(r, r.Subject(), sign, device, brand.TeamRef)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	apply := func(msg string, root bool) {
		c := bus.Commit{SHA: "c" + string(rune('0'+len(history))), Message: msg}
		history = append(history, c)
		d.applyTeamRecordLocked(c, root)
	}
	genesis, err := bus.NewEpoch(bus.NewBoxKey(), map[string]string{d.device: bus.BoxPublic(d.boxKey)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	genesis.AdminSeq, genesis.Policy = 1, &bus.RepoPolicy{EncryptionMode: bus.EncryptionTeam, AllowFiles: true}
	apply(seal(genesis, owner, "owner-0001"), true)
	on := bus.TeamRecord{V: bus.Version, Type: bus.TeamPolicy, AdminSeq: 2,
		Policy: &bus.RepoPolicy{EncryptionMode: bus.EncryptionTeam, AllowFiles: true, AutoRotate: true}}

	apply(seal(on, teammate, "peer-0001"), false)
	apply(seal(on, teammate, "owner-0001"), false)
	if d.st.TeamOwner.Policy.AutoRotate || d.st.TeamOwner.Seq != 1 {
		t.Fatal("a policy sealed by someone other than the owner turned automatic rotation on")
	}
	off := on
	off.Policy = &bus.RepoPolicy{EncryptionMode: bus.EncryptionTeam, AllowFiles: true}
	signed := seal(off, owner, "owner-0001")
	tampered := strings.Replace(signed, `"allowFiles":true`, `"allowFiles":true,"autoRotate":true`, 1)
	if tampered == signed {
		t.Fatal("the fixture didn't find the policy in the record")
	}
	apply(tampered, false)
	if d.st.TeamOwner.Policy.AutoRotate || d.st.TeamOwner.Seq != 1 {
		t.Fatal("a policy changed after it was sealed turned automatic rotation on")
	}
	apply(seal(on, owner, "owner-0001"), false)
	if !d.st.TeamOwner.Policy.AutoRotate || d.st.TeamOwner.Seq != 2 {
		t.Fatal("the owner's policy didn't turn automatic rotation on")
	}

	// A replay from the root, as after a cache that dropped the field, reads it back.
	d.st.TeamOwner, d.st.Epochs = nil, nil
	for i, c := range history {
		d.applyTeamRecordLocked(c, i == 0)
	}
	if o := d.st.TeamOwner; o == nil || !o.Policy.AutoRotate || o.Seq != 2 {
		t.Fatalf("after a replay, owner %+v", o)
	}
}
