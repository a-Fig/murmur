package daemon

import (
	"context"
	"crypto/ecdh"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// The team key (bus/team.go, #156). Every event and presence a device writes is encrypted for the
// current epoch's team key, whose public half anyone can read on the team ref, so a device can write
// before it holds any private half. A daemon that holds the team keys grants them, every one there
// has been, to each device whose sealed presence names the current epoch and lists fewer keys than
// there are: nobody runs anything. A new epoch is made by the first device to find the team ref
// empty, and by a rotation (`mur rotate`), wrapped for its writer alone; every other device gets it
// by a grant, which takes a presence that names the epoch, and so was written and pushed after it,
// by a device that could still push.

// teamEpoch is an epoch as this device has read it from the team ref, in the ref's order.
type teamEpoch struct {
	ID  string    `json:"id"`
	Pub string    `json:"pub"`
	By  string    `json:"by"`
	At  time.Time `json:"at"`
	// Roster is the last roster record written for this epoch's key, still encrypted: the repo's
	// collaborators as `mur rotate` last listed them, which any device that holds the key opens.
	Roster *bus.TeamRecord `json:"roster,omitempty"`
}

// errNoTeamKey says a record can't be encrypted yet: no epoch has been read or made. The first sync
// makes one when the team ref is empty, so this lasts seconds.
var errNoTeamKey = errors.New("the team's encryption key isn't set up yet; Murmur makes it at the first sync")

// errStalled stops reading a log at a record encrypted for a team key this device is waiting for.
var errStalled = errors.New("waiting for the team key")

// teamKeysPath is where this device keeps the team private keys it has been granted, by TeamID. Like
// keys, never committed and never on the bus; it only grows, so a rewritten team ref takes nothing
// this device could read away from it.
func teamKeysPath(dir string) string { return filepath.Join(dir, "team-keys") }

func loadTeamKeys(dir string) (bus.Keyring, error) {
	keys := bus.Keyring{}
	b, err := os.ReadFile(teamKeysPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return keys, nil
	}
	if err != nil {
		return nil, err
	}
	var raw map[string]string
	if err := json.Unmarshal(b, &raw); err != nil {
		return keys, nil // unreadable: granted again, by a teammate, like a device that is new
	}
	for id, h := range raw {
		b, err := hex.DecodeString(h)
		if err != nil {
			continue
		}
		if k, err := ecdh.X25519().NewPrivateKey(b); err == nil && bus.TeamID(bus.BoxPublic(k)) == id {
			keys[id] = k
		}
	}
	return keys, nil
}

// rereadTeamIfKeysLostLocked has the team ref read again from its start when the ref shows this
// device was given a key it doesn't hold: team-keys was lost or damaged while state.json stayed. Its
// grants are still on the ref, and open with its device key, so the keys come back with no teammate
// around. Caller holds mu, or nothing else runs yet.
func (d *Daemon) rereadTeamIfKeysLostLocked() {
	for _, id := range d.st.TeamGranted[d.device] {
		if d.teamKeys[id] == nil {
			d.log.Printf("a team key this device was granted isn't in %s; reading %s again for it", teamKeysPath(d.dir), brand.TeamRef)
			d.st.TeamTip = ""
			return
		}
	}
}

// addTeamKeysLocked keeps keys this device was granted, saving them, and reports whether any was
// new. Caller holds mu.
func (d *Daemon) addTeamKeysLocked(keys bus.Keyring) bool {
	added := false
	for id, k := range keys {
		if _, ok := d.teamKeys[id]; !ok && bus.IsTeamID(id) {
			d.teamKeys[id] = k
			added = true
		}
	}
	if !added {
		return false
	}
	d.kickHistory() // the page's earlier messages that this key opens
	raw := map[string]string{}
	for id, k := range d.teamKeys {
		raw[id] = hex.EncodeToString(k.Bytes())
	}
	b, err := json.Marshal(raw)
	if err == nil {
		err = writeFileAtomic(teamKeysPath(d.dir), append(b, '\n'))
	}
	if err != nil {
		d.log.Printf("saving the team keys: %v", err)
	}
	// What this device couldn't open before may open now: every presence is read again, and the
	// logs, which stopped at the first record for a key this device lacked, go on (readEventsLocked).
	for _, p := range d.peers {
		if p.locked {
			p.sha = ""
		}
	}
	d.kickPoll()
	d.kickPresence() // its presence lists the keys it holds, which teammates grant by
	return true
}

// keyringLocked is every key this device opens boxes with: the team keys, and its own box key for
// what was encrypted for it alone. Caller holds mu.
func (d *Daemon) keyringLocked() bus.Keyring {
	ring := bus.Keyring{d.device: d.boxKey}
	for id, k := range d.teamKeys {
		ring[id] = k
	}
	return ring
}

// epochLocked is the current epoch, the last on the team ref, or nil before there is one. Caller
// holds mu.
func (d *Daemon) epochLocked() *teamEpoch {
	if n := len(d.st.Epochs); n > 0 {
		return &d.st.Epochs[n-1]
	}
	return nil
}

// knownEpochLocked says whether id is an epoch on the team ref. Caller holds mu.
func (d *Daemon) knownEpochLocked(id string) bool {
	return slices.ContainsFunc(d.st.Epochs, func(e teamEpoch) bool { return e.ID == id })
}

// teamRecipientsLocked is what a record is encrypted for: the current team key, or, before there is
// one, this device alone, so that nothing it writes goes out in the clear. Caller holds mu.
func (d *Daemon) teamRecipientsLocked() map[string]string {
	if ep := d.epochLocked(); ep != nil {
		return map[string]string{ep.ID: ep.Pub}
	}
	return map[string]string{d.device: bus.BoxPublic(d.boxKey)}
}

// signalTeamLocked wakes what waits for the team ref to change (awaitEpoch). Caller holds mu.
func (d *Daemon) signalTeamLocked() {
	close(d.teamChanged)
	d.teamChanged = make(chan struct{})
}

// awaitEpoch waits until there is an epoch to encrypt for, or ctx ends.
func (d *Daemon) awaitEpoch(ctx context.Context) error {
	for {
		d.mu.Lock()
		ep, ch := d.epochLocked(), d.teamChanged
		d.mu.Unlock()
		if ep != nil {
			return nil
		}
		d.kickTeam()
		select {
		case <-ch:
		case <-ctx.Done():
			return errNoTeamKey
		}
	}
}

func (d *Daemon) kickTeam() {
	select {
	case d.teamKick <- struct{}{}:
	default:
	}
}

// processTeam reads the team ref up to sha: from where it last read, or from its root if the ref was
// rewritten. Caller holds advMu.
func (d *Daemon) processTeam(ctx context.Context, sha string) {
	d.mu.Lock()
	tip := d.st.TeamTip
	d.mu.Unlock()
	if sha == tip {
		return
	}
	from := tip
	if tip != "" {
		ok, err := d.repo.IsAncestor(ctx, tip, sha)
		if err != nil {
			d.log.Printf("reading %s: %v", brand.TeamRef, err)
			return
		}
		if !ok {
			// Rewritten: read it whole again. The keys this device holds stay (teamKeysPath).
			d.log.Printf("%s was rewritten; reading it from its start", brand.TeamRef)
			from = ""
		}
	}
	err := d.repo.Log(ctx, from, sha, d.maxRecord, func(commits []bus.Commit) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.applyPageLocked(func() {
			if from == "" && d.st.TeamTip == tip {
				d.st.Epochs, d.st.TeamGranted, d.st.GrantMark = nil, map[string][]string{}, map[string]time.Time{}
			}
			for _, c := range commits {
				d.applyTeamLocked(c)
			}
			d.st.TeamTip = commits[len(commits)-1].SHA
		})
		return nil
	})
	if err != nil {
		d.log.Printf("reading %s: %v", brand.TeamRef, err)
		return
	}
	d.mu.Lock()
	if d.st.TeamTip != sha {
		d.st.TeamTip = sha // a tip that isn't a commit, or holds none Murmur reads
		d.saveLocked()
	}
	d.mu.Unlock()
	d.kickTeam()
}

// applyTeamLocked takes one record of the team ref. Only a record sealed for the team ref, by a
// device whose pinned key it was sealed with if that device is pinned, is taken; anything else is
// junk and passed over. Caller holds mu.
func (d *Daemon) applyTeamLocked(c bus.Commit) {
	if c.Oversize {
		return
	}
	state, seal := bus.OpenSeal(c.Message, brand.TeamRef)
	if state != bus.Sealed {
		d.log.Printf("%s: passing over %s, which isn't sealed", brand.TeamRef, short(c.SHA))
		return
	}
	if pin := d.st.Pins[seal.By]; pin != nil && pin.Sign != seal.Key {
		d.log.Printf("%s: passing over %s, sealed with a key that isn't the one pinned for %s", brand.TeamRef, short(c.SHA), seal.By)
		return
	}
	var r bus.TeamRecord
	if bus.Decode(c.Message, &r) != nil || !r.Valid() {
		return
	}
	grant := func(dev string, ids []string) {
		have := d.st.TeamGranted[dev]
		for _, id := range ids {
			if !slices.Contains(have, id) {
				have = append(have, id)
			}
		}
		d.st.TeamGranted[dev] = have
	}
	switch r.Type {
	case bus.TeamEpoch:
		if d.knownEpochLocked(r.ID) {
			return
		}
		ep := teamEpoch{ID: r.ID, Pub: r.Pub, By: seal.By, At: r.At}
		for _, dev := range r.Box.Recipients() {
			grant(dev, []string{r.ID})
		}
		if keys, err := r.Open(d.device, d.boxKey); err == nil {
			d.addTeamKeysLocked(keys)
		} else if !errors.Is(err, bus.ErrNotForUs) {
			d.log.Printf("%s: epoch %s doesn't open: %v", brand.TeamRef, r.ID, err)
		}
		d.st.Epochs = append(d.st.Epochs, ep)
		d.signalTeamLocked()
		// This device's presence goes out again for the new key: before the first epoch it was
		// encrypted for this device alone, and after a rotation the old key may be one a removed
		// collaborator holds.
		d.kickPresence()
	case bus.TeamGrant:
		grant(r.To, r.IDs)
		// The device is granted again only on a presence signed after the newest this daemon holds
		// now (grantsDueLocked). A sync reads presence before the team ref, so that is the presence
		// the grant answered, or a later one.
		if pin := d.st.Pins[r.To]; pin != nil {
			d.st.GrantMark[r.To] = pin.At
		}
		if r.To != d.device {
			return
		}
		keys, err := r.Open(d.device, d.boxKey)
		if err != nil {
			d.log.Printf("%s: a grant to this device from %s doesn't open: %v", brand.TeamRef, seal.By, err)
			return
		}
		if d.addTeamKeysLocked(keys) {
			d.log.Printf("granted the team keys by %s", seal.By)
		}
	case bus.TeamRoster:
		// Kept as it was written, and opened when `mur rotate` compares: a device granted the key
		// after reading this can open it then. A later roster for the same key replaces this one.
		for i := range d.st.Epochs {
			if d.st.Epochs[i].ID == r.ID {
				d.st.Epochs[i].Roster = &r
			}
		}
	}
}

// teamLoop makes the team ref's records: the first epoch, when there is none, and grants. It runs
// when kicked (after each sync, and when the team ref was read), and retries a push that fails.
func (d *Daemon) teamLoop(ctx context.Context) {
	retry := time.NewTimer(time.Hour)
	retry.Stop()
	defer retry.Stop()
	delay := d.opt.NameRetry
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.teamKick:
		case <-retry.C:
		}
		_, _, err := d.teamWork(ctx, nil)
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				d.log.Printf("writing %s failed, retrying: %v", brand.TeamRef, err)
			}
			retry.Reset(delay)
			delay = min(delay*2, maxNameBackoff)
		case err == nil:
			delay = d.opt.NameRetry
		}
		failing = err != nil
	}
}

// grantsDueLocked is the devices this one grants the team keys to now: each with a pinned box key
// whose sealed presence names the current epoch and lists fewer team keys than this device could
// give it (#156, 2026-11-10). Only a device that holds the current key grants.
//
// A presence that names an epoch was signed after the epoch was made, as an epoch's ID is the hash of
// a random key, and it sits on the device's own ref, so it was pushed after too, by someone who could
// push then. How old it is, and whether this daemon saw it arrive, doesn't matter: a device that
// joins while nobody is around is granted by the first key holder to sync, and reads the grant when
// it next runs. A collaborator removed before a rotation can't push a presence naming the new epoch.
//
// It goes by the keys the device says it holds, not by the grants the team ref shows: a device whose
// grant didn't open, or carried a key under another's ID, is granted again. A device that is away
// would be granted again on every pass, as its presence stays as it is, so a device a grant was read
// for is granted again only on a presence signed later than the one this daemon held when it read
// that grant (GrantMark): the device has run since, and still lacks a key. Both times are that
// device's own, so no two clocks are compared. Caller holds mu.
func (d *Daemon) grantsDueLocked() []string {
	ep := d.epochLocked()
	if ep == nil || d.teamKeys[ep.ID] == nil {
		return nil
	}
	var due []string
	for _, dev := range sortedKeys(d.peers) {
		p, pin := d.peers[dev], d.st.Pins[dev]
		if dev == d.device || pin == nil || pin.Box == "" || !p.sealed || p.pres.Epoch != ep.ID {
			continue
		}
		if mark, ok := d.st.GrantMark[dev]; ok && !p.pres.At.After(mark) {
			continue
		}
		lacks := false
		// Every key held, as a grant carries them all: one for an epoch a rewritten team ref no longer
		// shows is still history's key (Sol's review, 2026-11-10).
		for id := range d.teamKeys {
			if !slices.Contains(p.pres.Holds, id) {
				lacks = true
			}
		}
		if lacks {
			due = append(due, dev)
		}
	}
	return due
}

// teamAsk is what a person asked of the team ref with `mur rotate`: a new epoch if rotate, and a
// roster of collaborators, for the new epoch or else the current one.
type teamAsk struct {
	rotate        bool
	collaborators []string
	// basis is the roster a roster written without a rotation was decided against: nobody on it had
	// left. If the roster is another by the time of the push, the push isn't made (errRosterMoved).
	basis []string
}

// errRosterMoved says the current epoch's roster is no longer the one a request compared against:
// another device wrote one, or rotated. Writing over it could take off someone who has since left
// with no new key made, and their leaving would never be seen (Sol's review, 2026-11-10).
var errRosterMoved = errors.New("the roster changed meanwhile")

// errKeysMissing says this device can't rotate: the devices it would grant to get the keys it
// holds, and history must stay readable to them.
var errKeysMissing = errors.New("this device hasn't been granted every team key yet, so it can't rotate; run it on a teammate's device, or wait for the grant")

// teamWork pushes what the team ref needs from this device, all in one leased push: a new epoch, if
// there is none or ask wants one, a roster if ask carries one, and a grant for each device that
// needs one. A push refused because the ref moved is left to the next sync, which reads what moved
// it, and decides again. It says whether its push landed, and the epoch it made, if it made one: a
// caller can't tell by looking at the epochs before and after, as a sync may apply an earlier push's
// while this waits its turn.
func (d *Daemon) teamWork(ctx context.Context, ask *teamAsk) (epoch string, pushed bool, err error) {
	d.teamMu.Lock()
	defer d.teamMu.Unlock()
	d.mu.Lock()
	tip := d.st.TeamTip
	_, listed := d.known[brand.TeamRef]
	synced := !d.lastPollOK.IsZero()
	ep := d.epochLocked()
	var recs []bus.TeamRecord
	var newKey *ecdh.PrivateKey
	now := time.Now()
	rotate := ask != nil && ask.rotate
	if rotate {
		// Checked on the epochs this very push builds on: another device may have rotated since the
		// request came in, and this one would then grant a history with that key missing.
		for _, e := range d.st.Epochs {
			if d.teamKeys[e.ID] == nil {
				d.mu.Unlock()
				return "", false, errKeysMissing
			}
		}
	}
	if ask != nil && !rotate && ask.collaborators != nil {
		if cur, ok := d.rosterLocked(); !ok || !slices.Equal(cur, ask.basis) {
			d.mu.Unlock()
			return "", false, errRosterMoved
		}
	}
	rosterID, rosterPub := "", ""
	if ep != nil {
		rosterID, rosterPub = ep.ID, ep.Pub
	}
	if rotate || ep == nil && synced && !listed && tip == "" {
		// The epoch is wrapped for this device alone. Every other device gets it by a grant, once it
		// has pushed a presence encrypted for this key: what a collaborator who has lost push access
		// can't do.
		newKey = bus.NewBoxKey()
		r, err := bus.NewEpoch(newKey, map[string]string{d.device: bus.BoxPublic(d.boxKey)}, now)
		if err != nil {
			d.mu.Unlock()
			return "", false, err
		}
		recs = append(recs, r)
		rosterID, rosterPub, epoch = r.ID, r.Pub, r.ID
	}
	if ask != nil && ask.collaborators != nil && rosterID != "" {
		r, err := bus.NewRoster(rosterID, rosterPub, ask.collaborators, now)
		if err != nil {
			d.mu.Unlock()
			return "", false, err
		}
		recs = append(recs, r)
	}
	for _, dev := range d.grantsDueLocked() {
		r, err := bus.NewGrant(dev, d.st.Pins[dev].Box, d.teamKeys, now)
		if err != nil {
			d.mu.Unlock()
			return "", false, err
		}
		recs = append(recs, r)
	}
	d.mu.Unlock()
	if len(recs) == 0 {
		return "", false, nil
	}
	parent := tip
	if parent != "" {
		if ok, err := d.repo.IsCommit(ctx, parent); err != nil {
			return "", false, err
		} else if !ok {
			parent = ""
		}
	}
	sha := parent
	var shas, msgs []string
	for _, r := range recs {
		msg, err := bus.EncodeSealed(r, r.Subject(), d.signKey, d.device, brand.TeamRef)
		if err != nil {
			return "", false, err
		}
		if sha, err = d.repo.NewCommit(ctx, sha, msg); err != nil {
			return "", false, err
		}
		shas, msgs = append(shas, sha), append(msgs, msg)
	}
	if err := d.push(ctx, sha, brand.TeamRef, bus.Lease, tip); err != nil {
		if errors.Is(err, bus.ErrRejected) {
			d.kickPoll() // someone else wrote first: read it, then decide again
			return "", false, nil
		}
		return "", false, err
	}
	if err := d.repo.UpdateLocal(ctx, brand.TeamRef, sha); err != nil {
		d.log.Printf("updating local %s: %v", brand.TeamRef, err)
	}
	d.advMu.Lock()
	defer d.advMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.known[brand.TeamRef] = sha
	if d.st.TeamTip != tip {
		return epoch, true, nil // read past meanwhile: the sync that did has what was pushed, or will
	}
	if newKey != nil {
		d.addTeamKeysLocked(bus.Keyring{recs[0].ID: newKey})
	}
	for i, r := range recs {
		d.applyTeamLocked(bus.Commit{SHA: shas[i], Message: msgs[i]})
		switch r.Type {
		case bus.TeamEpoch:
			d.log.Printf("made team key %s", r.ID)
		case bus.TeamGrant:
			d.log.Printf("granted the team keys to %s", r.To)
		}
	}
	d.st.TeamTip = sha
	d.saveLocked()
	return epoch, true, nil
}

// rotate makes a new epoch now, with a roster of collaborators if there is a list, and waits for it
// to land. Only a device that holds every team key may (errKeysMissing).
func (d *Daemon) rotate(ctx context.Context, collaborators []string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		id, pushed, err := d.teamWork(ctx, &teamAsk{rotate: true, collaborators: collaborators})
		if err != nil {
			return "", err
		}
		if pushed {
			return id, nil
		}
		// Refused: another device wrote first. Read what it wrote, and try again on it.
		if err := d.pollOnce(ctx); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("%s kept moving; try again", brand.TeamRef)
}

// rosterLocked is the collaborators last recorded for the current epoch, and whether this device
// could read them: there is a roster, and it holds the key. Caller holds mu.
func (d *Daemon) rosterLocked() ([]string, bool) {
	ep := d.epochLocked()
	if ep == nil || ep.Roster == nil {
		return nil, false
	}
	got, err := ep.Roster.OpenRoster(d.teamKeys)
	return got, err == nil
}

// recordRoster writes a roster of collaborators for the current epoch, in place of basis, and waits
// for it to land. It fails with errRosterMoved if the roster is no longer basis.
func (d *Daemon) recordRoster(ctx context.Context, collaborators, basis []string) error {
	for attempt := 0; attempt < 3; attempt++ {
		_, pushed, err := d.teamWork(ctx, &teamAsk{collaborators: collaborators, basis: basis})
		if err != nil {
			return err
		}
		if pushed {
			return nil
		}
		// Refused: another device wrote first. Read what it wrote, and try again on it.
		if err := d.pollOnce(ctx); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s kept moving; try again", brand.TeamRef)
}

// rotateRequest is OpRotate: a new team key if anyone on the current epoch's roster is no longer in
// collab, the people who can push now, or if force. With no roster this device can read there is
// nothing to compare against, so it rotates, and the new epoch gets a roster for next time. When
// nobody has left and someone has joined, the roster is written again with them on it, so that their
// leaving is seen later. Someone who joined and left between two runs was never on a roster, and
// only --force covers that. It returns what it did, for the person who asked.
func (d *Daemon) rotateRequest(ctx context.Context, collab []string, force bool) (string, error) {
	if collab != nil {
		collab = slices.Clone(collab)
		slices.Sort(collab)
		collab = slices.Compact(collab)
	}
	var gone []string
	// Decided again if the roster changes before a roster-only write is pushed: a teammate's device
	// may have recorded someone this one never saw, who has since left.
	for attempt := 0; ; attempt++ {
		d.mu.Lock()
		prev, known := d.rosterLocked()
		d.mu.Unlock()
		gone = nil
		for _, login := range prev {
			if !slices.Contains(collab, login) {
				gone = append(gone, login)
			}
		}
		if force {
			break
		}
		if collab == nil {
			return "", errors.New("couldn't list the repo's collaborators, so there is nothing to compare; --force makes a new team key anyway")
		}
		if !known || len(gone) > 0 {
			break
		}
		if slices.Equal(prev, collab) {
			return "Nobody has left since the team key was made, so it stays as it is.", nil
		}
		err := d.recordRoster(ctx, collab, prev)
		if errors.Is(err, errRosterMoved) && attempt < 3 {
			continue
		}
		if err != nil {
			return "", err
		}
		return "Nobody has left since the team key was made, so it stays as it is. Recorded who has joined.", nil
	}
	id, err := d.rotate(ctx, collab)
	if err != nil {
		return "", err
	}
	note := "Made a new team key, " + id + "."
	if len(gone) > 0 {
		note += " No longer collaborators: " + strings.Join(gone, ", ") + "."
	}
	return note + " Teammates' devices that can still push get it within a couple of minutes of running, and keep " +
		"every key before it; a device that can't push never gets it, and reads nothing written from now on. " +
		"Whatever can still push gets it: remove any deploy key, team access or App someone who left still has.", nil
}
