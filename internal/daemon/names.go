package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
)

// Session names (#20). A session's name is two words (quiet-otter), unique in the repo and never
// reused. It is taken by compare-and-swap on one shared ref, refs/murmur/names: each commit on it
// records one assignment or reservation, and a name is this device's only if the leased push of
// that commit lands. A device keeps a few names set aside that way (the pool), so a new session
// takes one at once, without the remote, and the registry is told which session took it after.
// docs/VISION.md has the reasons.
//
// What a daemon holds, in state.json:
//   - Used: every name it has ever seen, from the registry's history or another device's
//     presence, with the session's address where known. It only grows, so a registry that is
//     overwritten, deleted or rewound frees no name.
//   - NameKeys: which name each of this device's session keys was given. The same key gets the same
//     name for good, across MCP server restarts, daemon restarts and resumes.
//   - NamesTip and NamesGood: the registry's tip as last examined, which is the lease for the next push, and
//     the last tip that was a commit and was read in full.
//   - NamePool, NameBinds and Reserved: the names set aside for this device that no session has
//     taken, those taken whose assignment isn't in the registry yet, and every device's names set
//     aside that no session is known to have.

const (
	// nameTries is how many pushes in a row may be refused, because another device took the
	// registry's tip first, before the assigner backs off.
	nameTries      = 5
	maxNameBackoff = 5 * time.Minute
)

// nameKey is what a session's name follows: inside T3, the T3 thread, which stays the same session
// across /clear, restarts of its MCP server and switches of Claude account (#56); else the
// client's own session ID when it gives one, so that a resume, or an MCP server restarting, is the
// same session: Claude Code's, or the thread ID Codex gives every tool call (native, #36);
// otherwise the ID Murmur gave the session. The kinds are kept apart so that no ID of one can equal
// an ID of another.
func nameKey(t3Thread, claude, native, id string) string {
	if t3Thread != "" {
		return "t3:" + t3Thread
	}
	if claude != "" {
		return "claude:" + claude
	}
	if native != "" {
		return "codex:" + native
	}
	return "session:" + id
}

// kickNames wakes the assigner.
func (d *Daemon) kickNames() {
	select {
	case d.nameKick <- struct{}{}:
	default:
	}
}

// noteNameLocked records that a name is in use, and by whom if that is known. It never takes one
// back, and an address already known is kept: the first claim a daemon saw stands. It reports
// whether anything changed. Caller holds mu.
func (d *Daemon) noteNameLocked(name, addr string) bool {
	cur, ok := d.st.Used[name]
	if ok && (cur != "" || addr == "") {
		return false
	}
	d.st.Used[name] = addr
	if addr != "" {
		delete(d.st.Reserved, name) // set aside no longer: a session has it
	}
	return true
}

// notePersonClaimLocked records that a person's name is claimed by addr. The first claim read
// stands: a later one, here or from a registry rewritten since, changes nothing. It reports whether
// anything changed. Caller holds mu.
func (d *Daemon) notePersonClaimLocked(name, addr string) bool {
	if d.st.PersonClaims[name] != "" || addr == "" {
		return false
	}
	d.st.PersonClaims[name] = addr
	d.st.People[name] = addr
	return true
}

// processNames reads the registry's tip into the set of used names. sha is the tip on the remote
// and already fetched, "" if the listing didn't show the ref (which it checks again, if this
// daemon has read the registry before). Caller holds advMu.
//
// Anyone with push access can write any ref, so the tip may be junk, or a history that no longer
// holds what this daemon read before. Nothing read is ever forgotten; but the registry then no
// longer holds every name for daemons that join later, so the next commit this daemon writes there
// is a new root with a snapshot of every used name (see attemptName), and one is written without
// waiting for a session to need a name, if the registry is missing any.
func (d *Daemon) processNames(ctx context.Context, sha string) {
	d.mu.Lock()
	tip, good, checked, peopleRead := d.st.NamesTip, d.st.NamesGood, d.namesChecked, d.st.NamesPeople
	d.mu.Unlock()
	if checked && tip == sha {
		return
	}
	d.mu.Lock()
	usedBefore, reservedBefore := maps.Clone(d.st.Used), maps.Clone(d.st.Reserved)
	d.mu.Unlock()
	fail := func(err error) {
		if ctx.Err() == nil {
			d.log.Printf("reading the names registry: %v", err)
		}
	}
	if sha == "" && good != "" {
		// A registry this daemon has read or written, and a listing that says it isn't there. A
		// sync lists the remote before it takes advMu, so the listing can be older than this
		// daemon's own push of the registry, which attemptName records under advMu: the first
		// name, pushed and recorded while the sync waited for the lock. That is no deletion, and
		// taking it for one sets the registry to be repaired, which replaces its history with a
		// snapshot (#80). A listing that shows an older tip is mended by the fetch that follows
		// it, which brings the current one; a listing without the registry has nothing to
		// fetch, so ask the remote again. Anything this daemon recorded had landed before it was
		// recorded, so a registry the remote doesn't show now was deleted.
		cur, err := d.repo.RemoteRef(ctx, brand.NamesRef)
		if err != nil {
			fail(err)
			return
		}
		if cur != "" {
			if err := d.repo.Fetch(ctx, []string{brand.NamesRef}); err != nil {
				fail(err)
				return
			}
			// The fetch brings what is there now, which may be newer than what was just listed.
			local, err := d.repo.LocalRefs(ctx)
			if err != nil {
				fail(err)
				return
			}
			if sha = local[brand.NamesRef]; sha == "" {
				sha = cur
			}
		}
	}
	isCommit := false
	if sha != "" {
		ok, err := d.repo.IsCommit(ctx, sha)
		if err != nil {
			fail(err)
			return
		}
		isCommit = ok
	}
	var commits []bus.Commit
	disturbed := false
	if isCommit {
		from := ""
		if good != "" {
			// Descending from what was read, only what is new needs reading. Anything else is a
			// registry rewritten or rewound, read again from its start.
			desc, err := d.repo.IsAncestor(ctx, good, sha)
			if err != nil {
				fail(err)
				return
			}
			switch {
			case !desc:
				disturbed = true
			case peopleRead:
				from = good
			default:
				// Not damaged, but read by a daemon that passed over people's claims (see
				// NamesPeople): read again from the start, for them.
			}
		}
		var err error
		if commits, err = d.repo.Log(ctx, from, sha); err != nil {
			fail(err)
			return
		}
	} else {
		// Junk, or the registry gone after having been read.
		disturbed = sha != "" || good != ""
	}
	read := map[string]string{}
	reserved := map[string]string{} // the reservations read, name to device
	for _, c := range commits {
		var rec bus.NameRecord
		if bus.Decode(c.Message, &rec) != nil {
			continue
		}
		claims := rec.Claims()
		if name, dev, ok := rec.Reservation(); ok {
			// The first reservation of a name gives it its device; a later one counts for nothing,
			// nor does one of a name a session already has. What was learned before this read
			// counts as read, so a daemon that reads only what is new agrees with one that reads
			// the whole history. A name known with no session and no device (from a snapshot
			// that kept no devices) still gains one, as it would for a reader starting fresh.
			_, owned := reserved[name]
			if _, ok := reservedBefore[name]; ok {
				owned = true
			}
			if !owned && read[name] == "" && usedBefore[name] == "" {
				reserved[name] = dev
			}
		} else if rec.Session != "" {
			// The assignment of a name set aside: only the device it was set aside for can give
			// it to a session.
			owner, ok := reserved[rec.Name]
			if !ok {
				owner, ok = reservedBefore[rec.Name]
			}
			if _, has := claims[rec.Name]; has && ok && owner != rec.Device {
				claims[rec.Name] = ""
			}
		}
		for name, addr := range claims {
			if cur, ok := read[name]; !ok || cur == "" {
				read[name] = addr
			}
		}
	}

	d.mu.Lock()
	wasRepair := d.st.NamesRepair
	changed := d.st.NamesTip != sha
	for name, addr := range read {
		if bus.ValidPerson(name) {
			if d.notePersonClaimLocked(name, addr) {
				changed = true
			}
		} else if d.noteNameLocked(name, addr) {
			changed = true
		}
	}
	for name, dev := range reserved {
		if _, ok := d.st.Reserved[name]; !ok && d.st.Used[name] == "" {
			d.st.Reserved[name], changed = dev, true
		}
	}
	d.st.NamesTip = sha
	if isCommit {
		d.st.NamesGood = sha
		// Read in full here, or from what was: either way, by a daemon that keeps people's claims.
		changed = changed || !d.st.NamesPeople
		d.st.NamesPeople = true
	}
	if disturbed {
		// Only a history read from its start shows whether the registry holds every name. A
		// read of what was added on top of a damaged one can't, so a repair still to be made
		// stays to be made.
		d.st.NamesRepair = false
		for _, known := range []map[string]string{d.st.Used, d.st.PersonClaims} {
			for name := range known {
				if _, ok := read[name]; !ok {
					d.st.NamesRepair = true
					break
				}
			}
		}
	}
	repair := d.st.NamesRepair
	changed = changed || repair != wasRepair
	// The claim of a name from before people were in the registry waits for this first read (see
	// nextNames), and nothing else would wake the assigner for it.
	claim := !checked && d.st.Person != "" && d.st.PersonClaims[d.st.Person] == ""
	d.namesChecked = true
	if changed {
		d.saveLocked()
	}
	d.mu.Unlock()
	if repair && !wasRepair {
		d.log.Printf("the names registry lost names; writing a snapshot of every name in use")
	}
	if repair || claim {
		d.kickNames()
	}
}

// resolveNameLocked finds the session called name: in this device's sessions, then in its record
// of who held each name last, then in other devices' presence, and last in the registry, which
// also knows a session that has since stopped.
//
// The record is for #37. The registry has the session a name was first given to, which may have
// moved to another conversation since, and another session here may have taken the name after it.
// The last holder is the answer if the name is still the last it took and it can receive, running
// or not; one that has moved to another conversation is not the name's any more (#46). Otherwise
// the registry's address goes on the bus as it is, as from any device, and the receiving daemon forwards it at
// most once (see forwardLocked): forwarding it here too would make two hops of one.
// Caller holds mu.
func (d *Daemon) resolveNameLocked(name string) (addr string, ok bool) {
	var dropped string
	for _, s := range d.sortedSessionsLocked() {
		if s.name != name {
			continue
		}
		if s.dropped.IsZero() {
			return bus.Addr(d.device, s.id), true
		}
		dropped = bus.Addr(d.device, s.id)
	}
	if dropped != "" {
		return dropped, true
	}
	if h := d.st.NameHolders[name]; h != "" && d.st.SessionNames[h] == name && d.receivesLocked(h) {
		return bus.Addr(d.device, h), true
	}
	for _, dev := range sortedKeys(d.peers) {
		for _, s := range d.peers[dev].pres.Sessions {
			if s.Name == name {
				return bus.Addr(dev, s.ID), true
			}
		}
	}
	addr = d.st.Used[name]
	return addr, addr != ""
}

// noSuchNameLocked is what to tell someone who asked for a name that resolveNameLocked finds no
// session for. A name a device has set aside is in use, but nobody can be sent to yet. Caller
// holds mu.
func (d *Daemon) noSuchNameLocked(name string) string {
	switch dev, ok := d.st.Reserved[name]; {
	case ok && dev == d.device:
		return fmt.Sprintf("no session has the name %q yet: this device has set it aside for a session it has yet to start. who shows the names in use.", name)
	case ok:
		return fmt.Sprintf("no session has the name %q yet: device %s has set it aside for a session it has yet to start. who shows the names in use.", name, dev)
	}
	return fmt.Sprintf("no session is named %q. who shows the names in use.", name)
}

// namesByAddrLocked inverts the registry: the name of each session whose address it knows.
// Caller holds mu.
func (d *Daemon) namesByAddrLocked() map[string]string {
	byAddr := map[string]string{}
	for name, addr := range d.st.Used {
		if addr != "" {
			byAddr[addr] = name
		}
	}
	return byAddr
}

// nameLoop gives names to the sessions that have none, and repairs the registry when it has lost
// names. A failure to reach the remote, or a registry too busy to take a push, leaves the
// sessions as they are, addressed as device/session, and is tried again after a delay that
// doubles up to maxNameBackoff.
func (d *Daemon) nameLoop(ctx context.Context) {
	retry := time.NewTimer(0) // after a restart, there may be a repair to finish
	defer retry.Stop()
	delay := d.opt.NameRetry
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.nameKick:
		case <-retry.C:
		}
		err := d.assignPending(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				d.log.Printf("taking a session name failed, retrying: %v", err)
			}
			retry.Reset(delay)
			delay = min(delay*2, maxNameBackoff)
		case err == nil:
			if failing {
				d.log.Printf("session names working again")
			}
			delay = d.opt.NameRetry
		}
		failing = err != nil
	}
}

// wanted is a session that needs a name, and has no pool to take one from.
type wanted struct{ id, key string }

// bound is a name a session took from the pool, which the registry doesn't have down for it yet.
type bound struct{ name, id string }

// nameWork is what the assigner has to push to the registry, all in one push: the assignments of
// names sessions took from the pool, fresh names for sessions that found the pool empty, names to
// set aside to fill the pool again, and a repair. One push carries them as a chain of commits, one
// record each, so a reader reads them as it reads any history.
type nameWork struct {
	binds   []bound
	wants   []wanted
	reserve int
	repair  bool
	person  string // this device's person's name, to claim for them (#268)
}

func (w nameWork) empty() bool {
	return len(w.binds) == 0 && len(w.wants) == 0 && w.reserve == 0 && !w.repair && w.person == ""
}

// nameForKeyLocked is the name for a session of key, id: the name the key has, else the oldest in
// the pool, which becomes the key's for good, at once and without the remote. The registry is told
// which session took it later (see bound). "" if the key has none and the pool is empty: the
// session waits for the assigner. Caller holds mu.
func (d *Daemon) nameForKeyLocked(key, id string) string {
	if name := d.st.NameKeys[key]; name != "" || len(d.st.NamePool) == 0 {
		return name
	}
	name := d.st.NamePool[0]
	d.st.NamePool = d.st.NamePool[1:]
	d.st.NameKeys[key] = name
	d.st.NameBinds[name] = id
	// The session's address for the name from now on, as the registry will say once the binding
	// lands: a session that moves to another conversation before then is still found by it.
	d.noteNameLocked(name, bus.Addr(d.device, id))
	d.saveLocked()
	d.kickNames() // to put the session in the registry, and fill the pool again
	d.log.Printf("session %s is named %s, a name set aside for it", bus.Addr(d.device, id), name)
	return name
}

// givePoolLocked gives names from the pool to the sessions that have none, which started while the
// pool was empty. Caller holds mu.
func (d *Daemon) givePoolLocked() {
	for _, s := range d.sortedSessionsLocked() {
		if s.name != "" {
			continue
		}
		if d.setNameLocked(s, d.nameForKeyLocked(s.key, s.id)); s.name != "" {
			d.kickPresence()
		}
	}
}

// nextNames hands out what the pool has to sessions that have no name, and says what is left for
// the registry.
func (d *Daemon) nextNames() nameWork {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.givePoolLocked()
	var w nameWork
	keys := map[string]bool{}
	for _, s := range d.sortedSessionsLocked() {
		if s.name == "" && !keys[s.key] {
			keys[s.key] = true // sessions of one key share its name: one assignment for them all
			w.wants = append(w.wants, wanted{id: s.id, key: s.key})
		}
	}
	for _, name := range sortedKeys(d.st.NameBinds) {
		w.binds = append(w.binds, bound{name: name, id: d.st.NameBinds[name]})
	}
	if d.opt.NamePool > len(d.st.NamePool) {
		w.reserve = d.opt.NamePool - len(d.st.NamePool)
	}
	w.repair = d.st.NamesRepair
	if d.st.Person != "" && d.st.PersonClaims[d.st.Person] == "" && d.namesChecked {
		// A name from before people were in the registry. Only once the registry is read: until
		// then, a claim made before this one isn't known to be there.
		w.person = d.st.Person
	}
	return w
}

// assignPending pushes what nextNames finds until there is nothing left: names for the sessions
// that have none, the pool's names in the registry, and a repair if the registry needs one. A push
// refused because another device moved the registry first is tried again with fresh names, after
// reading what is new, up to nameTries times in a row.
func (d *Daemon) assignPending(ctx context.Context) error {
	refused := 0
	for ctx.Err() == nil {
		w := d.nextNames()
		if w.empty() {
			return nil
		}
		err := d.pushNames(ctx, w)
		switch {
		case err == nil:
			refused = 0
		case errors.Is(err, bus.ErrRejected):
			if refused++; refused >= nameTries {
				return fmt.Errorf("the registry moved under %d pushes in a row", nameTries)
			}
			// Fetch, and merge what is new into the used names; the next try takes fresh names.
			if err := d.pollOnce(ctx); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return ctx.Err()
}

// pickName chooses a random name that is not known to be in use, nor in taken.
func (d *Daemon) pickName(used map[string]string, taken map[string]bool) (string, error) {
	for range 1000 {
		name, err := names.Generate(d.nameSrc)
		if err != nil {
			return "", err
		}
		if _, ok := used[name]; !ok && !taken[name] {
			return name, nil
		}
	}
	return "", errors.New("no unused name found")
}

// pushNames pushes w to the registry as one chain of commits on the tip last read, leased on it. It
// lands only if no one else moved the registry since, which is what makes each fresh name in it
// this device's. When the tip isn't a commit, or the registry needs repairing, the chain starts
// from a new root holding a snapshot of every used name, and the names set aside as reservations
// again, leased on whatever the tip is. It returns
// bus.ErrRejected if the lease failed.
func (d *Daemon) pushNames(ctx context.Context, w nameWork) error {
	// The repair flag is read with the tip it goes with: a sync between nextNames and here may have
	// found the registry rewound, and building on the new tip without the snapshot would lose names.
	d.mu.Lock()
	tip, repair := d.st.NamesTip, d.st.NamesRepair
	used := maps.Clone(d.st.Used)
	people := maps.Clone(d.st.PersonClaims)
	aside := map[string]string{} // names set aside that no session is known to have, to their device
	for name, dev := range d.st.Reserved {
		if used[name] == "" {
			aside[name] = dev
		}
	}
	d.mu.Unlock()

	at := time.Now().UTC()
	rec := func(name, session string) bus.NameRecord {
		return bus.NameRecord{V: bus.Version, Name: name, Device: d.device, Session: session, At: at}
	}
	var recs []bus.NameRecord
	for _, b := range w.binds {
		recs = append(recs, rec(b.name, b.id))
	}
	taken := map[string]bool{}
	pick := func() (string, error) {
		name, err := d.pickName(used, taken)
		taken[name] = true
		return name, err
	}
	given := make([]string, len(w.wants))
	for i, s := range w.wants {
		name, err := pick()
		if err != nil {
			return err
		}
		given[i] = name
		recs = append(recs, rec(name, s.id))
	}
	var reserved []string
	for range w.reserve {
		name, err := pick()
		if err != nil {
			return err
		}
		reserved = append(reserved, name)
		recs = append(recs, rec(name, ""))
	}
	person := w.person
	if people[person] != "" {
		person = "" // claimed already: by this person, or first by someone else, who keeps it
	}
	if person != "" {
		recs = append(recs, rec(person, bus.PersonSession))
	}
	parent := tip
	if parent != "" {
		ok, err := d.repo.IsCommit(ctx, parent)
		if err != nil {
			return err
		}
		if !ok {
			parent = "" // junk is never something to build on
		}
	}
	if parent == "" || repair {
		// A new root. The names set aside go in it as reservations again, first, so a daemon that
		// joins later still knows whose they are; the rest of the used names go in the snapshot.
		var again []bus.NameRecord
		for _, name := range sortedKeys(aside) {
			again = append(again, bus.NameRecord{V: bus.Version, Name: name, Device: aside[name], At: at})
			delete(used, name)
		}
		recs = append(again, recs...)
		if len(recs) == 0 {
			recs = append(recs, bus.NameRecord{V: bus.Version, At: at}) // a repair on its own
		}
		for name, addr := range people {
			used[name] = addr // a person's claim survives a repair, as a session's name does
		}
		parent, recs[0].Used = "", used
	}
	if len(recs) == 0 {
		return nil // nothing to record: a repair that a sync has meanwhile found unneeded
	}
	sha := parent
	for _, r := range recs {
		msg, err := bus.Encode(r, r.Subject())
		if err != nil {
			return err
		}
		if sha, err = d.repo.NewCommit(ctx, sha, msg); err != nil {
			return err
		}
	}
	if err := d.push(ctx, sha, brand.NamesRef, bus.Lease, tip); err != nil {
		return err
	}

	if err := d.repo.UpdateLocal(ctx, brand.NamesRef, sha); err != nil {
		d.log.Printf("updating local %s: %v", brand.NamesRef, err)
	}
	d.advMu.Lock()
	defer d.advMu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.known[brand.NamesRef] = sha
	if d.st.NamesTip == tip {
		// Nothing newer was read meanwhile; this chain, on a tip that was read in full, is read. A
		// tip not yet read by a daemon that keeps people's claims isn't (see NamesPeople), unless
		// this chain is a new root, which holds every name itself.
		d.st.NamesTip, d.st.NamesRepair = sha, false
		if d.st.NamesPeople || parent == "" {
			d.st.NamesGood, d.st.NamesPeople = sha, true
		}
	}
	for _, b := range w.binds {
		d.noteNameLocked(b.name, bus.Addr(d.device, b.id))
		if d.st.NameBinds[b.name] == b.id {
			delete(d.st.NameBinds, b.name)
		}
		d.noteLocked(at, rec(b.name, b.id).Subject())
	}
	for i, s := range w.wants {
		name, addr := given[i], bus.Addr(d.device, s.id)
		d.noteNameLocked(name, addr)
		d.noteLocked(at, rec(name, s.id).Subject())
		if d.st.NameKeys[s.key] != "" {
			// The key found a name of its own while this was pushed (its session moved to a
			// thread that has one): this name is the session's in the registry, and that's all.
			continue
		}
		d.st.NameKeys[s.key] = name
		for _, o := range d.sessions {
			if o.key == s.key && o.name == "" {
				d.setNameLocked(o, name)
			}
		}
		d.log.Printf("session %s is named %s", addr, name)
	}
	if person != "" {
		d.notePersonClaimLocked(person, d.personAddr())
		d.noteLocked(at, rec(person, bus.PersonSession).Subject())
	}
	for _, name := range reserved {
		d.noteNameLocked(name, "")
		d.st.Reserved[name] = d.device
		d.st.NamePool = append(d.st.NamePool, name)
	}
	if len(reserved) > 0 {
		d.log.Printf("set aside for sessions to come: %s", strings.Join(reserved, ", "))
	}
	d.givePoolLocked() // to sessions that started while this was pushed, and found the pool empty
	d.saveLocked()
	if len(w.wants) > 0 {
		d.kickPresence()
	}
	return nil
}
