package daemon

import (
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// pruneInboxes drops the inboxes of sessions stopped longer than StoppedInbox, with what is kept
// for them, and those of sessions neither registered nor stopped. A daemon that stops records a
// stop for each of its sessions (#736), and the next daemon does it for one that was killed
// (#762), so those are what a state file from before either holds.
func (d *Daemon) pruneInboxes() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.started) > catchUpWindow {
		d.pending = nil
		d.dropHeldLocked()
	}
	for id, at := range d.st.Stopped {
		if d.stopSpentLocked(id, at) {
			d.dropStoppedLocked(id)
			d.saveLocked()
		}
	}
	for id := range d.pushedAt {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.pushedAt, id)
		}
	}
	if time.Since(d.started) < sweepAfter {
		return
	}
	if d.pruneSessionNamesLocked(time.Now()) {
		d.saveLocked()
	}
	// Nobody an earlier daemon's stop ended is waited for any longer (awaitedLocked). Only those:
	// this runs after a push that the daemon's own stop cut short too, and the marks that stop is
	// writing are for the next daemon.
	for id := range d.st.Downed {
		if d.st.Stopped[id].Before(d.started) {
			delete(d.st.Downed, id)
			d.saveLocked()
		}
	}
	for id := range d.st.Inbox {
		if _, stopped := d.st.Stopped[id]; id != proto.CLISession && id != bus.PersonSession && d.sessions[id] == nil && !stopped {
			delete(d.st.Inbox, id)
			d.saveLocked()
		}
	}
	// Credited commits are kept as long as the session's inbox is.
	for id := range d.st.Commits {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.st.Commits, id)
			d.saveLocked()
		}
	}
	// A thread is kept for a session while it can have messages: it is running or stopped within
	// StoppedInbox. One with no stop recorded, from a state file older than #736 and #762, has none.
	for id := range d.st.T3Routes {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.st.T3Routes, id)
			d.saveLocked()
		}
	}
	// So is the Codex thread it gave up, which says whose its messages are (keptForLocked).
	for id := range d.st.ThreadLeft {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.st.ThreadLeft, id)
			d.saveLocked()
		}
	}
	// And the key it was the later of two servers on (uniqueKeyLocked).
	for id := range d.st.Later {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.st.Later, id)
			d.saveLocked()
		}
	}
	// The tags and mutes it kept by its ID, if its name never landed, go with its inbox as well.
	gone := func(key string) {
		id, pending := strings.CutPrefix(key, tagKeyOf("", ""))
		if _, stopped := d.st.Stopped[id]; pending && d.sessions[id] == nil && !stopped {
			d.dropPendingKeyLocked(id)
			d.saveLocked()
		}
	}
	for key := range d.st.Tags {
		gone(key)
	}
	for key := range d.st.Mutes {
		gone(key)
	}
}

// sweepAfter is how long after it starts a daemon leaves what belongs to sessions it knows
// nothing of, and to those the last daemon went down with: their servers are reconnecting.
const sweepAfter = 2 * time.Minute

// stopSpentLocked says what is kept for session id, stopped at at, has had its time: StoppedInbox
// since the stop. Not yet, whatever the stop's age, for a session the last daemon went down
// with (Downed) while this one is younger than sweepAfter: its server may be reconnecting, and
// one back in that time kept what it had before a stop was recorded for it (#762). A killed
// daemon's stop is dated by its last write, which can be days before a laptop is opened again,
// and a daemon that stopped as a machine shut down for the weekend is as old. Caller holds mu.
func (d *Daemon) stopSpentLocked(id string, at time.Time) bool {
	if d.st.Downed[id] && time.Since(d.started) < sweepAfter {
		return false
	}
	return time.Since(at) > d.opt.StoppedInbox
}

// keptLocked says session id is stopped and its inbox is kept: its stop hasn't had its time
// (stopSpentLocked). It is the one test of that, for whether a message is queued for the
// session, forwarded to it or adopted from it, and whether its thread is read: a session spared
// by the sweep and counted as gone by the rest would have its inbox kept and nothing put in it.
// Caller holds mu.
func (d *Daemon) keptLocked(id string) bool {
	at, ok := d.st.Stopped[id]
	return ok && !d.stopSpentLocked(id, at)
}

// dropStoppedLocked lets go of what is kept for session id, stopped past StoppedInbox: its inbox,
// the commits credited to it and its thread, and the tags and mutes it kept by its ID if its name
// never landed. The stop runs out in pruneInboxes, or in newSessionLocked if the session comes back
// first; both come here, so what it gets back doesn't depend on which ran first. Caller holds mu.
func (d *Daemon) dropStoppedLocked(id string) {
	delete(d.st.Stopped, id)
	delete(d.st.Downed, id)
	delete(d.st.Inbox, id)
	delete(d.st.Commits, id)
	delete(d.st.T3Routes, id)
	delete(d.st.ThreadLeft, id)
	delete(d.st.Later, id)
	d.dropPendingKeyLocked(id)
}

// dropPendingKeyLocked drops the tags and mutes session id kept by its ID while its name was
// pending (tagKeyLocked), once it has no inbox to come back to: nothing else would ever drop them.
// A name's are kept for good. Caller holds mu.
func (d *Daemon) dropPendingKeyLocked(id string) {
	if d.sessions[id] != nil {
		return
	}
	delete(d.st.Tags, tagKeyOf("", id))
	delete(d.st.Mutes, tagKeyOf("", id))
}
