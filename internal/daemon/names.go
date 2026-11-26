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
// docs/design/session-names.md has the reasons.
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

// notePresenceNameLocked records a session name another device's presence shows, as
// noteNameLocked does. A name this daemon has never seen may be one the registry has lost: damaged
// while the device holding the name can't push, and read here by a daemon that never knew it. Only
// a history read from its start shows that, and only one listed after this presence: a sync's fetch
// brings a presence as it is then, which can be newer than the registry it listed. So the next
// sync, which lists after this one, reads the registry again from its start (see processNames).
// Caller holds mu.
func (d *Daemon) notePresenceNameLocked(name, addr string) bool {
	if _, ok := d.st.Used[name]; !ok {
		d.st.NamesRecheck = true
	}
	return d.noteNameLocked(name, addr)
}

// lacksNamesLocked says whether a name in use here, a session's or a person's, is missing from
// held. Caller holds mu.
func (d *Daemon) lacksNamesLocked(held map[string]string) bool {
	for _, known := range []map[string]string{d.st.Used, d.st.PersonClaims} {
		for name := range known {
			if _, ok := held[name]; !ok {
				return true
			}
		}
	}
	return false
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

// notePersonLocked records that the person at addr goes by name, as their device's presence says.
// The first address seen with a name keeps it, as for a session's name (noteNameLocked), and only
// a claim in the registry, which stands over any sighting, gives it to another. A message's name
// never comes here: one device can send any number of messages, each saying another name, and
// would hold them all. A sync reads the registry before the presences, so a claim is known before
// any presence says the name. It reports whether anything changed. Caller holds mu.
func (d *Daemon) notePersonLocked(name, addr string) bool {
	cur, ok := d.st.People[name]
	switch {
	case !ok:
		d.st.People[name] = addr
		return true
	case cur == addr || d.personClashes[name]:
		return false
	}
	if d.personClashes == nil {
		d.personClashes = map[string]bool{}
	}
	d.personClashes[name] = true // logged once: a presence is read on each of its heartbeats
	d.log.Printf("%s says its person is %s, which is the name of the person at %s; that one keeps it", addr, name, cur)
	return false
}

// livePersonLocked is the name the live presence of the device at addr gives its person, "" if
// there is none or it is stale. Caller holds mu.
func (d *Daemon) livePersonLocked(addr string) string {
	dev, _ := bus.SplitAddr(addr)
	if dev == d.device {
		return d.st.Person
	}
	p := d.peers[dev]
	if p == nil || d.staleLocked(p) {
		return ""
	}
	for _, s := range p.pres.Sessions {
		if s.ID == bus.PersonSession {
			return s.Name
		}
	}
	return ""
}

// nameOnOthersLocked says a live presence of a device other than addr's says its person is name.
// Caller holds mu.
func (d *Daemon) nameOnOthersLocked(name, addr string) bool {
	dev, _ := bus.SplitAddr(addr)
	for other := range d.peers {
		if other != dev && d.livePersonLocked(bus.Addr(other, bus.PersonSession)) == name {
			return true
		}
	}
	return dev != d.device && d.st.Person == name
}

// processNames reads the registry's tip into the set of used names. sha is the tip on the remote
// and already fetched, "" if the listing didn't show the ref (which it checks again, if this
// daemon has read the registry before). Caller holds advMu.
//
// Anyone with push access can write any ref, so the tip may be junk, or a history that no longer
// holds what this daemon read before. Nothing read is ever forgotten; but the registry then no
// longer holds every name for daemons that join later, so the next commit this daemon writes there
// is a new root with a snapshot of every used name (see attemptName), and one is written without
// waiting for a session to need a name, if the registry is missing any. A presence showing a name
// never seen here has the registry read again from its start, to see whether it lost the name
// (see notePresenceNameLocked).
func (d *Daemon) processNames(ctx context.Context, sha string) {
	d.mu.Lock()
	tip, good, checked, peopleRead := d.st.NamesTip, d.st.NamesGood, d.namesChecked, d.st.NamesPeople
	recheck := d.st.NamesRecheck
	d.mu.Unlock()
	if checked && tip == sha && !recheck {
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
			// The fetch brings what is there now, which may be newer than what was just listed.
			tips, err := d.repo.Fetch(ctx, []string{brand.NamesRef})
			if err != nil {
				fail(err)
				return
			}
			sha = tips[brand.NamesRef]
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
	read := map[string]string{}
	reserved := map[string]string{} // the reservations read, name to device
	snapshotAt := ""                // the one commit whose snapshot counts, "" for none (see below)
	skipped := 0
	// noteRecords reads a page of the registry's history into read and reserved.
	noteRecords := func(commits []bus.Commit) {
		for _, c := range commits {
			var rec bus.NameRecord
			if bus.Decode(c.Message, &rec) != nil {
				continue
			}
			if len(rec.Used) > 0 && c.SHA != snapshotAt {
				// A snapshot anywhere but the root of a history read from its start isn't Murmur's (one
				// appended, or a root merged in), and could mark any number of names used, for good,
				// on every daemon that read it.
				rec.Used = nil
				skipped++
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
	}
	disturbed := false
	manyRoots := false
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
			case peopleRead && !recheck:
				from = good
			default:
				// Not damaged, but read by a daemon that passed over people's claims (see
				// NamesPeople): read again from the start, for them.
			}
		}
		// Murmur writes a snapshot only as the first commit of a new history, a repair's, which a
		// reader always reads from the start, as it doesn't descend from what was read. It writes no
		// merges, so its history has one root. A merge that brings in another root is damage, as a
		// rewrite is: no snapshot in it counts, so a reader new to it would miss a repair's names,
		// and it is read from the start and repaired. An incremental reader asks only about what is
		// new. Found before the history is read, which needs it.
		roots, err := d.repo.Roots(ctx, sha, from)
		if err != nil {
			fail(err)
			return
		}
		if len(roots) > 0 && (from != "" || len(roots) > 1) {
			d.log.Printf("the names registry has more than one root, which Murmur never writes; reading it as damaged")
			disturbed, from, manyRoots = true, "", true
		} else if from == "" && len(roots) == 1 {
			snapshotAt = roots[0]
		}
		err = d.repo.Log(ctx, from, sha, d.maxSnapshot, func(page []bus.Commit) error {
			noteRecords(page)
			return nil
		})
		if err != nil {
			fail(err)
			return
		}
	} else {
		// Junk, or the registry gone after having been read.
		disturbed = sha != "" || good != ""
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
	if disturbed || recheck {
		// Only a history read from its start shows whether the registry holds every name. A
		// read of what was added on top of a damaged one can't, so a repair still to be made
		// stays to be made. A recheck is always read from the start.
		d.st.NamesRepair = d.lacksNamesLocked(read)
	}
	if recheck {
		d.st.NamesRecheck, changed = false, true
	}
	repair := d.st.NamesRepair
	changed = changed || repair != wasRepair
	// The claim of a name from before people were in the registry waits for this first read (see
	// nextNames), and nothing else would wake the assigner for it.
	claim := !checked && len(d.unclaimedPersonNamesLocked()) > 0
	d.namesChecked = true
	if changed {
		d.saveLocked()
	}
	d.mu.Unlock()
	if skipped > 0 && !manyRoots { // a history with many roots counts none, and said so
		d.log.Printf("the names registry has %d snapshots not at its history's root; their names weren't counted", skipped)
	}
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
	// A presence is whatever its device wrote, so a device can show a name that isn't its own
	// (#604). While the device the registry has the name on is up, only its presence counts: if no
	// session there shows the name, the session has stopped, and the registry's address keeps the
	// message for it. Otherwise, of the presences that show the name, one still being written goes
	// before a stale one, and the first by device ID stands, as it always did. That is for a clone
	// that lost its keys and came back under a new device ID with its names, while the registry still
	// has them on the old one, which is gone or going stale: since #617 a daemon starts such a clone
	// with new names, but one from before it doesn't.
	holder, best := d.nameDeviceLocked(name), 0
	hp := d.peers[holder]
	holderUp := holder != "" && (holder == d.device || hp != nil && !d.staleLocked(hp))
	for _, dev := range sortedKeys(d.peers) {
		if holderUp && dev != holder {
			continue
		}
		p := d.peers[dev]
		rank := 1
		if !d.staleLocked(p) {
			rank += 2
		}
		if dev == holder {
			rank++
		}
		if rank <= best {
			continue
		}
		for _, s := range p.pres.Sessions {
			if s.Name == name {
				addr, best = bus.Addr(dev, s.ID), rank
				break
			}
		}
	}
	if best > 0 {
		return addr, true
	}
	addr = d.st.Used[name]
	return addr, addr != ""
}

// nameDeviceLocked is the device the registry, or an earlier sighting, has a session name on, or
// that has set it aside; "" if nobody is known to hold it. Caller holds mu.
func (d *Daemon) nameDeviceLocked(name string) string {
	if addr := d.st.Used[name]; addr != "" {
		dev, _ := bus.SplitAddr(addr)
		return dev
	}
	return d.st.Reserved[name]
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

// namesByAddrLocked is lastNameLocked for every address at once: the name to show for each session
// this daemon knows a name of, when it has none in presence. Caller holds mu.
func (d *Daemon) namesByAddrLocked() map[string]string {
	// Precompute the addresses currently listed without a name so the scan over Used skips
	// nameGivenUpLocked for every other address (almost all of them).
	var unnamed map[string]bool
	for _, s := range d.sessions {
		if s.name == "" {
			if unnamed == nil {
				unnamed = map[string]bool{}
			}
			unnamed[bus.Addr(d.device, s.id)] = true
		}
	}
	for dev, p := range d.peers {
		for _, s := range p.pres.Sessions {
			if s.ID != bus.PersonSession && s.Name == "" {
				if unnamed == nil {
					unnamed = map[string]bool{}
				}
				unnamed[bus.Addr(dev, s.ID)] = true
			}
		}
	}
	givenUp := func(addr, name string) bool {
		return unnamed[addr] && d.nameGivenUpLocked(addr, name)
	}

	byAddr := map[string]string{}
	for name, addr := range d.st.Used {
		if addr == "" || givenUp(addr, name) {
			continue
		}
		if cur := byAddr[addr]; cur == "" || name < cur {
			byAddr[addr] = name
		}
	}
	for addr, pn := range d.st.PeerNames { // another device's, by its presence's latest (#602)
		if dev, _ := bus.SplitAddr(addr); dev != d.device && pn.Name != "" {
			if givenUp(addr, pn.Name) {
				delete(byAddr, addr)
			} else {
				byAddr[addr] = pn.Name
			}
		}
	}
	for id, name := range d.st.SessionNames {
		if name != "" {
			addr := bus.Addr(d.device, id)
			if givenUp(addr, name) {
				delete(byAddr, addr)
			} else {
				byAddr[addr] = name
			}
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

// wanted is a session that needs a name, and has no pool to take one from. alias is its
// conversation's Claude session key when key is a T3 thread's, so the name is shared even if the
// session stops before the push lands (#874).
type wanted struct{ id, key, agent, alias string }

// pendingShare is a Claude Code conversation's (t3, claude) key pair seen by shareNameLocked while
// both keys were unnamed and an assigner push was in flight (#874).
type pendingShare struct{ key, alias, id string }

// pendingRekey is a session's move from one unnamed key to another in rekeyLocked while an
// assigner push was in flight (#874).
type pendingRekey struct{ from, to string }

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
	person  []string // this device's person's names, to claim for them (#268)
}

func (w nameWork) empty() bool {
	return len(w.binds) == 0 && len(w.wants) == 0 && w.reserve == 0 && !w.repair && len(w.person) == 0
}

// unclaimedPersonNamesLocked is this device's person's names that nobody has claimed in the
// registry: their name, and the names they had before people were in the registry, which People
// keeps for them here. #272 claims a name when it is chosen, and a renamed person keeps their old
// one; a name left from a rename before #272 was never claimed, and once the registry is read only a
// claim or a live presence holds a name (nameTakenLocked), so without a claim of its own another
// person could choose it, and messages to it would reach them. Caller holds mu.
func (d *Daemon) unclaimedPersonNamesLocked() []string {
	if d.st.Person == "" {
		return nil
	}
	var out []string
	if d.st.PersonClaims[d.st.Person] == "" {
		out = append(out, d.st.Person)
	}
	me := d.personAddr()
	for _, name := range sortedKeys(d.st.People) {
		if name != d.st.Person && d.st.People[name] == me && d.st.PersonClaims[name] == "" && bus.ValidPerson(name) {
			out = append(out, name)
		}
	}
	return out
}

// nameForKeyLocked is the name for session s of key: the name the key has, else the oldest in
// the pool, which becomes the key's for good, at once and without the remote. The registry is told
// which session took it later (see bound). "" if the key has none and the pool is empty: the
// session waits for the assigner. Caller holds mu.
func (d *Daemon) nameForKeyLocked(s *session, key string) string {
	if name := d.st.NameKeys[key]; name != "" || len(d.st.NamePool) == 0 {
		return name
	}
	id := s.id
	name := d.st.NamePool[0]
	d.st.NamePool = d.st.NamePool[1:]
	d.st.NameKeys[key] = name
	d.noteNamedInT3Locked(s.agent, key, name)
	d.st.NameBinds[name] = id
	// The session's address for the name from now on, as the registry will say once the binding
	// lands: a session that moves to another conversation before then is still found by it.
	d.noteNameLocked(name, bus.Addr(d.device, id))
	d.saveLocked()
	d.kickNames() // to put the session in the registry, and fill the pool again
	d.log.Printf("session %s is named %s, a name set aside for it", bus.Addr(d.device, id), name)
	return name
}

// noteStoppedNameLocked records name, just landed, as the last name of session id, which stopped
// while the name was pushed, as setNameLocked would have had it landed in time. A
// Pi session that moved to a new file asks for that file's name, and one that stops first, its
// Pi gone, was still recorded under the name of the file it left: what it kept was then for that
// file's next session, and a Pi on that file, once PiFiles is lost, took its ID again
// (piIDOtherLocked, Opus's review of cfa13f91). Only for a session that stopped on the key the name
// was asked for (stoppedKeys): one that moved on, or back to a file that has a name, has its last
// conversation elsewhere (Sol's and Opus's review of c9639dca). The name's holder is recorded only
// if it has none. Caller holds mu.
func (d *Daemon) noteStoppedNameLocked(id, name string) {
	if d.st.SessionNames == nil {
		d.st.SessionNames = map[string]string{}
	}
	if d.st.NameHolders == nil {
		d.st.NameHolders = map[string]string{}
	}
	if d.st.NamesSeen == nil {
		d.st.NamesSeen = map[string]time.Time{}
	}
	d.st.SessionNames[id] = name
	d.st.NamesSeen[id] = time.Now().UTC().Truncate(time.Hour)
	if d.st.NameHolders[name] == "" {
		d.st.NameHolders[name] = id
	}
}

// givePoolLocked gives names from the pool to the sessions that have none, which started while the
// pool was empty. Caller holds mu.
func (d *Daemon) givePoolLocked() {
	for _, s := range d.sortedSessionsLocked() {
		if s.name != "" {
			continue
		}
		// An earlier session in this loop (for instance a CLI session in its grace) may have just
		// named the conversation's Claude key; share it before taking a second name from the pool.
		d.shareSessionNameLocked(s)
		if s.dropped.IsZero() {
			// Another live session may already hold the name s.key just received (for instance a
			// T3 thread that did /clear while its name was pushed and its old Claude session was
			// resumed on the command line): two live sessions never share a name (#874).
			s.key = d.uniqueKeyLocked(s.key, s.id, s.clientPID)
		}
		if d.setNameLocked(s, d.nameForKeyLocked(s, s.key)); s.name != "" {
			d.kickPresence()
		}
	}
}

// applyPendingKeysLocked gives a name that just landed to any key linked to it while the
// assigner's push was in flight: Claude Code conversations' (t3, claude) pairs recorded by
// shareNameLocked, and keys a session moved to by finding its thread in rekeyLocked (#874).
// Shares run before rekeys so a name from the command line marks NamedFromCLI on the thread's key.
// Caller holds mu.
func (d *Daemon) applyPendingKeysLocked(shares []pendingShare, rekeys []pendingRekey) {
	for {
		progressed := false
		for _, sh := range shares {
			k, a := d.st.NameKeys[sh.key], d.st.NameKeys[sh.alias]
			if (k == "") != (a == "") {
				d.shareNameLocked(sh.key, sh.alias, sh.id)
				progressed = true
			}
		}
		for _, rk := range rekeys {
			if name := d.st.NameKeys[rk.from]; name != "" && d.st.NameKeys[rk.to] == "" {
				d.st.NameKeys[rk.to] = name
				progressed = true
			}
		}
		if !progressed {
			break
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
	d.pendingShares, d.pendingRekeys = nil, nil
	keys := map[string]bool{}
	for _, s := range d.sortedSessionsLocked() {
		if s.name != "" || keys[s.key] {
			continue
		}
		var alias string
		if t3, ok := strings.CutPrefix(s.key, "t3:"); ok && !strings.Contains(t3, "/") {
			alias = claudeAlias(s.agent, t3, s.claudeSession)
		}
		if alias != "" && keys[alias] {
			// Another session of the same conversation (e.g. a CLI session in its grace) is
			// already in this push: share that name instead of pushing a second one (#874).
			d.pendingShares = append(d.pendingShares, pendingShare{key: s.key, alias: alias, id: s.id})
			keys[s.key] = true
			continue
		}
		keys[s.key] = true // sessions of one key share its name: one assignment for them all
		if alias != "" {
			keys[alias] = true
		}
		w.wants = append(w.wants, wanted{id: s.id, key: s.key, agent: s.agent, alias: alias})
	}
	d.assigning = len(w.wants) > 0
	for _, name := range sortedKeys(d.st.NameBinds) {
		w.binds = append(w.binds, bound{name: name, id: d.st.NameBinds[name]})
	}
	if d.opt.NamePool > len(d.st.NamePool) {
		w.reserve = d.opt.NamePool - len(d.st.NamePool)
	}
	w.repair = d.st.NamesRepair
	if d.namesChecked {
		// Names from before people were in the registry. Only once the registry is read: until
		// then, a claim made before this one isn't known to be there.
		w.person = d.unclaimedPersonNamesLocked()
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
		// After the presence of a session just started, and before pushNames reads the tip for its
		// lease. Not when a session found the pool empty and waits on this push for its name: until
		// it lands, a presence can only show the session as pending.
		if len(w.wants) == 0 {
			if err := d.awaitPresence(ctx); err != nil {
				return err
			}
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

// pickName chooses a random name that is not known to be in use, nor in taken: after 1,000 random
// tries, the first such from a random place in the lists (see names.Unused).
func (d *Daemon) pickName(used map[string]string, taken map[string]bool) (string, error) {
	name, err := names.Unused(d.nameSrc, 1000, func(name string) bool {
		_, ok := used[name]
		return ok || taken[name]
	})
	if err == nil && name == "" {
		err = errors.New("every name is in use")
	}
	return name, err
}

// authorPrint is this clone's commit fingerprint (bus.AuthorPrint, #1244), which every registry
// entry it writes carries, or "" when the remote isn't on GitHub or git has no identity here. It is
// read again for each push, not once at start: an email git makes up from the hostname can change
// with the network, and each entry should carry the identity this clone's commits have now. The
// census keeps every fingerprint a clone has written, so commits under either address match.
//
// It is the email git would write on a commit made here: `git var GIT_AUTHOR_IDENT`, which takes
// user.email, the environment, and else the user@hostname address git makes up, which is common.
// The bus's own commits set their identity with -c on commit-tree only (bus.Repo.NewCommit), so
// this reads the user's.
func (d *Daemon) authorPrint(ctx context.Context) string {
	if d.repoAddr == "" {
		return ""
	}
	ident, err := d.repo.Git.Run(ctx, "", "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return "" // no identity git can make: such a clone's commits can't be made either
	}
	return bus.AuthorPrint(d.repoAddr, ident)
}

// pushNames pushes w to the registry as one chain of commits on the tip last read, leased on it. It
// lands only if no one else moved the registry since, which is what makes each fresh name in it
// this device's. When the tip isn't a commit, or the registry needs repairing, the chain starts
// from a new root holding a snapshot of every used name, and the names set aside as reservations
// again, leased on whatever the tip is. It returns
// bus.ErrRejected if the lease failed.
func (d *Daemon) pushNames(ctx context.Context, w nameWork) error {
	finished := false
	if len(w.wants) > 0 {
		defer func() {
			if !finished {
				d.mu.Lock()
				d.assigning, d.pendingShares, d.pendingRekeys, d.stoppedKeys = false, nil, nil, nil
				d.mu.Unlock()
			}
		}()
	}
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
	author := d.authorPrint(ctx)
	rec := func(name, session string) bus.NameRecord {
		return bus.NameRecord{V: bus.Version, Name: name, Device: d.device, Session: session, At: at, Author: author}
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
	var person []string
	for _, name := range w.person {
		if people[name] == "" { // else claimed already: by this person, or first by someone else, who keeps it
			person = append(person, name)
			recs = append(recs, rec(name, bus.PersonSession))
		}
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
	held := map[string]string{} // every name a new root holds; none if the chain isn't one
	if parent == "" || repair {
		// A new root. The names set aside go in it as reservations again, first, so a daemon that
		// joins later still knows whose they are; the rest of the used names go in the snapshot.
		var again []bus.NameRecord
		for _, name := range sortedKeys(aside) {
			again = append(again, bus.NameRecord{V: bus.Version, Name: name, Device: aside[name], At: at, Author: author})
			delete(used, name)
		}
		recs = append(again, recs...)
		if len(recs) == 0 {
			recs = append(recs, bus.NameRecord{V: bus.Version, At: at, Author: author}) // a repair on its own
		}
		for name, addr := range people {
			used[name] = addr // a person's claim survives a repair, as a session's name does
		}
		parent, recs[0].Used = "", used
		for name, addr := range used {
			held[name] = addr
		}
		for _, r := range recs {
			held[r.Name] = ""
		}
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
	finished = true
	var pendingShares []pendingShare
	var pendingRekeys []pendingRekey
	var stoppedKeys map[string]string
	if len(w.wants) > 0 {
		pendingShares, pendingRekeys, stoppedKeys = d.pendingShares, d.pendingRekeys, d.stoppedKeys
		d.assigning, d.pendingShares, d.pendingRekeys, d.stoppedKeys = false, nil, nil, nil
	}
	d.known[brand.NamesRef] = sha
	if d.st.NamesTip == tip {
		// Nothing newer was read meanwhile; this chain, on a tip that was read in full, is read. A
		// tip not yet read by a daemon that keeps people's claims isn't (see NamesPeople), unless
		// this chain is a new root, which holds every name itself.
		d.st.NamesTip = sha
		// The repair is made only if every name in use now is in the root. One can be learned while
		// the push is out, and a sync can then find the registry it read lacking it, with no newer
		// tip to show for that: clearing the flag would lose the name.
		if parent == "" && !d.lacksNamesLocked(held) {
			d.st.NamesRepair = false
		}
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
		d.noteNamedInT3Locked(s.agent, s.key, name) // its session may have gone since it asked
		if s.alias != "" {
			d.shareNameLocked(s.key, s.alias, s.id)
		}
		d.log.Printf("session %s is named %s", addr, name)
	}
	d.applyPendingKeysLocked(pendingShares, pendingRekeys)
	// Give each requesting session its landed name before givePoolLocked visits sessions in ID
	// order, so a session that rekeyed while the push was out keeps the name ahead of another
	// session that started on its old key meanwhile (for instance after /clear in T3).
	for i, s := range w.wants {
		name := given[i]
		if o := d.sessions[s.id]; o != nil && o.name == "" && d.st.NameKeys[o.key] == name {
			d.setNameLocked(o, name)
		} else if o == nil && d.st.NameKeys[s.key] == name && stoppedKeys[s.id] == s.key {
			d.noteStoppedNameLocked(s.id, name)
		}
	}
	for _, name := range person {
		d.notePersonClaimLocked(name, d.personAddr())
		d.noteLocked(at, rec(name, bus.PersonSession).Subject())
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
