package daemon

import (
	"cmp"
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// awaitPresence waits, if a session has started since the last presence was made, until a presence
// that lists it has been pushed or has failed. A device pushes one ref at a time, and a session's
// start sets off three pushes: its start on the log and its name's assignment are ready within
// milliseconds, and the presence, which waits 300 ms to gather the session's first changes, went
// last. Over GitHub, at about 1.4 s a push, the session showed in other devices' who about 3 s
// later than it needed to (measured 2026-11-09). Only the presence shows the session: its start on
// the log is a line in readers' notes, and a name from the pool is set aside for this device
// already, so readers take it from the presence before its assignment lands.
//
// It waits for the presence on its way, if there is one, and then for the next if a session has
// started since, even when the writer is for an earlier session that the first already lists. The
// gates are taken once, so that is two presences at most, not every session that starts meanwhile.
func (d *Daemon) awaitPresence(ctx context.Context) error {
	d.mu.Lock()
	gates := []chan struct{}{d.presenceSending, d.presenceFirst}
	d.mu.Unlock()
	for _, g := range gates {
		if g == nil {
			continue
		}
		select {
		case <-g:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// presenceSent lets go of what waits for the presence publishPresence took gate for, once that
// presence has been pushed or has failed.
func (d *Daemon) presenceSent(gate chan struct{}) {
	d.mu.Lock()
	if d.presenceSending == gate {
		d.presenceSending = nil
	}
	d.mu.Unlock()
	close(gate)
}

// logged names an event for the daemon's log by its type, ID and addresses, never by what a
// message says: the log is sent to whoever helps debug a device, who may not be on the team.
func logged(e bus.Event) string {
	s := e.Type + " " + e.ID + " from " + e.From
	if e.To != "" {
		s += " to " + e.To
	}
	return s
}

func (d *Daemon) kickPresence() {
	select {
	case d.kick <- struct{}{}:
	default:
	}
}

// presenceLoop pushes this device's presence when it changes, and on a heartbeat so others can
// tell it is still here.
func (d *Daemon) presenceLoop(ctx context.Context) {
	defer func() {
		// Nothing is left waiting for a presence that won't be pushed now.
		d.mu.Lock()
		if d.presenceFirst != nil {
			close(d.presenceFirst)
			d.presenceFirst = nil
		}
		d.mu.Unlock()
	}()
	heartbeat := time.NewTimer(jittered(d.opt.Heartbeat))
	defer heartbeat.Stop()
	retry := time.NewTimer(0) // publish once at start
	defer retry.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			heartbeat.Reset(jittered(d.opt.Heartbeat))
		case <-retry.C:
		case <-d.kick:
			// Let a burst of changes (a session starting and setting its status) land as one push.
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
		err := d.publishPresence(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			if !failing {
				d.log.Printf("pushing presence failed, retrying: %v", err)
			}
			retry.Reset(15 * time.Second)
		case err == nil && failing:
			d.log.Printf("presence pushed again")
		}
		failing = err != nil
		if err == nil {
			d.writeUnwritten() // what failed to be written may go now (#434)
		}
		d.pruneInboxes()
	}
}

// jittered is d give or take up to a fifth, at random. Each heartbeat waits that long, so devices
// whose daemons started together drift apart rather than push at the same moment every beat, which
// GitHub refuses now and then (see "Pushes" in docs/design/transport.md).
func jittered(d time.Duration) time.Duration {
	return d - d/5 + rand.N(2*(d/5)+1)
}

func (d *Daemon) publishPresence(ctx context.Context) error {
	// Not cut short by shutdown: a push killed mid-flight can still land, after the goodbye.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if d.hidden.Load() {
		return d.hidePresence(ctx)
	}
	d.mu.Lock()
	cwds := map[string]string{}
	for id, s := range d.sessions {
		cwds[id] = s.cwd
	}
	d.mu.Unlock()
	branches := map[string]string{}
	for id, cwd := range cwds {
		branches[id] = branchOf(ctx, cwd)
	}

	wake := d.wakeSnapshot() // setMu is taken alone, before mu
	d.mu.Lock()
	if err := d.checkOwnerHistoryLocked(); err != nil {
		d.mu.Unlock()
		return err
	}
	for id, b := range branches {
		if s := d.sessions[id]; s != nil && b != "" {
			s.branch = b
		}
	}
	d.colorAwayLocked()
	built := time.Now().Round(0) // the wall clock: time asleep counts (colorAwayLocked)
	pres := bus.Presence{V: bus.Version, Device: d.device, Host: d.host, Owner: d.owner, Nick: d.st.Nick, TeamAdmin: teamAdminAutoRotate,
		At:       d.nextPresenceAtLocked(),
		Sessions: []bus.SessionInfo{}, Groups: true, Receipts: true, Files: true, FilesOff: !d.repoAllowsFiles() || !d.agentRecv.on.Load(),
		PersonFilesOff: !d.repoAllowsFiles() || !d.pageRecv.on.Load(), DND: d.dnd.Load()}
	pres.Color, pres.ColorAt = d.shownColorLocked()
	// A name is an address, so one session at most carries it here. A session in its grace whose
	// key has come back under another ID has handed its name on: a peer sending to the name must
	// reach the live one, not the one about to stop.
	live := d.liveNamesLocked()
	pres.Offline = d.offlineIDsLocked()
	for _, s := range d.sortedSessionsLocked() {
		if d.offMeshLocked(s.id) {
			s.noWake = "" // as noWakeLocked says of it
			continue      // offline: listed in Offline instead
		}
		name, was := s.name, ""
		if !s.dropped.IsZero() && live[name] {
			// A dropped predecessor in its grace still belongs to this conversation: clear Name so
			// peers route to the live replacement, but keep Was so peers still label its messages
			// and hold mutes under that name (#875).
			name, was = "", s.name
		}
		info := bus.SessionInfo{ID: s.id, Name: name, Was: was, Agent: s.agent, Branch: s.branch, Doing: s.shownDoing(),
			Started: s.started.UTC(), Committed: d.committedLocked(s.id), Tags: d.tagsOfLocked(s), HookOff: d.hookOffLocked(s),
			TurnRoute: viaOf(s.route, s.dropped.IsZero()) != "",
			Muted:     d.mutesOfLocked(s.id), Dropped: d.mutedropsLocked(s.id), NoWake: d.noWakeLocked(s.id, wake)}
		// The name it is shown by here (lastNameLocked), unless another session on this device
		// took it, as after a resume or a handoff (#875): peers would show this session by a name
		// that reaches the other.
		if prev := d.st.SessionNames[s.id]; s.name == "" && !live[prev] && !d.nameGivenUpLocked(bus.Addr(d.device, s.id), prev) {
			info.Was = prev
		}
		s.noWake = info.NoWake
		if !s.lastActive.IsZero() {
			info.LastActive = s.lastActive.UTC()
		}
		pres.Sessions = append(pres.Sessions, info)
	}
	d.noteSessionsLocked(d.device, pres.Sessions, time.Now())
	room := bus.MaxSessions
	if d.st.Person != "" {
		room--
	}
	var cut int
	pres.Sessions, cut = capSessions(pres.Sessions, room)
	if cut != d.presenceCut {
		if cut > 0 {
			d.log.Printf("presence lists the %d most recently active sessions; %d more are left out, as readers take no more than %d",
				room, cut, bus.MaxSessions)
		}
		d.presenceCut = cut
	}
	if d.st.Person != "" {
		pres.Sessions = append(pres.Sessions, d.personInfoLocked())
	}
	pres.BoxKey = bus.BoxPublic(d.boxKey)
	// The epoch it has read and the team keys it holds: what a teammate grants by (grantsDueLocked).
	if ep := d.epochLocked(); ep != nil {
		pres.Epoch = ep.ID
	}
	pres.Holds = sortedKeys(d.teamKeys)
	mode := d.repoModeLocked()
	modeStamp := d.repoModeStamp.Load()
	team := d.teamRecipientsLocked()
	// This presence lists every session started so far, so what waits for one that does can go
	// once it is pushed, or has failed: a failing remote is no reason to hold back the rest. Until
	// then the gate stays where awaitPresence finds it, as presenceSending, so a writer that comes
	// to it while this presence is being encoded or waits for the push slot still waits.
	if first := d.presenceFirst; first != nil {
		d.presenceFirst, d.presenceSending = nil, first
		defer d.presenceSent(first)
	}
	d.mu.Unlock()

	if mode != bus.EncryptionOff {
		var err error
		pres, err = bus.EncryptPresence(pres, team)
		if err != nil {
			return err
		}
	}
	msg, err := bus.EncodeSealed(pres, bus.PresenceSubject, d.signKey, d.device, d.presenceRef())
	if err != nil {
		return err
	}
	sha, err := d.repo.NewCommit(ctx, "", msg)
	if err != nil {
		return err
	}
	ref := d.presenceRef()
	if err := d.pushFrom(ctx, d.repo, sha, ref, bus.Force, "", func() bool {
		return d.checkRepoWriteMode(modeStamp) == nil
	}); err != nil {
		if errors.Is(err, errSendOff) {
			if blocked := d.checkRepoWriteMode(modeStamp); blocked != nil {
				return blocked
			}
			return errTeamHistoryPending
		}
		return err
	}
	// Known at once, so the next poll doesn't fetch it back: updatePeer skips this device's own
	// presence, so the fetch would read nothing.
	d.mu.Lock()
	d.known[ref] = sha
	copied, fetches := d.presenceCopied, d.presenceFetches
	// When it was built, not when it landed: a push begun before a machine slept can land after
	// it wakes, with what the device knew a week before. Such a one carried the pick, so the next
	// is built at once, without it, where it would have waited for the heartbeat.
	d.presenceLanded = built
	if was := d.colorSettled; was {
		if d.colorAwayLocked(); !d.colorSettled && d.st.Color != 0 {
			d.kickPresence() // once: the next build finds it unsettled already
		}
	}
	d.mu.Unlock()
	// The clone's own copy of the ref is written once a run, with the first presence to land, and
	// again after a fetch has set it to whatever the remote had. Nothing reads it for what it
	// says: loadIdentity asks only whether it is sealed by this device, to tell that a clone whose
	// keys and state are both lost had published keys. Every presence is sealed, with keys that
	// don't change in a run, so any one of them answers that, and a git for every heartbeat was a
	// third of what a heartbeat costs (#1002).
	if !copied {
		if err := d.repo.UpdateLocal(ctx, ref, sha); err != nil {
			d.log.Printf("updating local %s: %v", ref, err)
		} else {
			d.mu.Lock()
			// Not if a fetch brought the ref meanwhile: its write may have come after this one.
			d.presenceCopied = d.presenceFetches == fetches
			d.mu.Unlock()
		}
	}
	return nil
}

// notePresencesListedLocked records, for each device's presence in a listing of the remote, the
// last good sync before its SHA was first listed (presenceListed). Caller holds mu.
func (d *Daemon) notePresencesListedLocked(remote map[string]string) {
	for ref, sha := range remote {
		dev, ok := strings.CutPrefix(ref, brand.PresencePrefix)
		if !ok || dev == d.device || !bus.ValidDevice(dev) {
			continue
		}
		if d.presenceListed == nil {
			d.presenceListed = map[string]listedPresence{}
		}
		if d.presenceListed[dev].sha != sha {
			d.presenceListed[dev] = listedPresence{sha: sha, after: d.lastPollOK}
		}
	}
}

// listedAfterLocked is this daemon's last good sync before dev's presence sha was first listed,
// or its last good sync if no listing recorded it (a read outside a sync). Caller holds mu.
func (d *Daemon) listedAfterLocked(dev, sha string) time.Time {
	if l, ok := d.presenceListed[dev]; ok && l.sha == sha {
		return l.after
	}
	return d.lastPollOK
}

// presenceFetchedLocked notes that a fetch of ref was tried, if it is this device's own presence:
// the fetch may have set the clone's copy to what the remote had, which may be junk pushed over
// the presence, so the next presence writes the copy again (publishPresence). It is called for a
// fetch that failed too, which may have written the ref before it did (#1130), and for a copy
// found broken and removed (repairRefs). Caller holds mu.
func (d *Daemon) presenceFetchedLocked(ref string) {
	if ref == d.presenceRef() {
		d.presenceFetches++
		d.presenceCopied = false
	}
}

// capSessions keeps at most n of a presence's sessions, the most recently active (by their last
// activity, else their start), in the order they came, and says how many it left out. A reader takes a
// presence over bus.MaxSessions for one it can't read, so a device with that many would vanish from
// every teammate's view; this way only its least active sessions do.
func capSessions(ss []bus.SessionInfo, n int) ([]bus.SessionInfo, int) {
	if len(ss) <= n {
		return ss, 0
	}
	at := func(s bus.SessionInfo) time.Time { return cmp.Or(s.LastActive, s.Started) }
	byActive := slices.Clone(ss)
	slices.SortStableFunc(byActive, func(a, b bus.SessionInfo) int { return at(b).Compare(at(a)) })
	keep := map[string]bool{}
	for _, s := range byActive[:n] {
		keep[s.ID] = true
	}
	kept := slices.DeleteFunc(slices.Clone(ss), func(s bus.SessionInfo) bool { return !keep[s.ID] })
	return kept, len(ss) - len(kept)
}

// liveNamesLocked is the names of the sessions that are connected, as opposed to waiting out their
// grace. Caller holds mu.
func (d *Daemon) liveNamesLocked() map[string]bool {
	live := map[string]bool{}
	for _, s := range d.sessions {
		if s.name != "" && s.dropped.IsZero() {
			live[s.name] = true
		}
	}
	return live
}
