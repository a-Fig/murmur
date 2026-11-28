package daemon

import (
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// queueLocked puts a message in the inbox of each session here it is addressed to. That queues it;
// it is delivered once the agent is told of it, and read once it has it (docs/design/delivery.md,
// "Delivered and read", #325). A session gets only what arrives while it runs, or, if it stops,
// within StoppedInbox of that, in case it comes back (see recipientsLocked). What arrived while the
// daemon was down (modeCatchUp) goes only to sessions that were running before and reconnect, and
// in modeSkip to nobody, except the sessions whose inbox is kept, for what was sent since they
// stopped (see inTimeLocked); in both, what was sent after a session here started is for it.
// Everything is held for catchUpWindow after the daemon starts, for sessions still reconnecting
// (see catchUpLocked). A message forwarded to the session that holds its recipient's name (see
// forwardLocked) is the holder's if it is new enough for either: a backlog read after the holder
// started still gives it what the recipient's kept inbox would have caught up on. Caller holds mu.
func (d *Daemon) queueLocked(e bus.Event, mode syncMode) (queued bool) {
	held := mode == modeCatchUp || mode == modeNormal && time.Since(d.started) <= catchUpWindow
	if held {
		d.pending = append(d.pending, e)
	}
	dev, sess := bus.SplitAddr(e.To)
	fwd := d.forwardNamedLocked(e.To, e.ToName)
	for _, id := range d.recipientsLocked() {
		if !d.addressedLocked(e, id) {
			continue
		}
		if d.offMeshLocked(id) {
			// Offline (#189): nothing is kept for it, and it is told nothing.
			if mode == modeNormal && !held && (e.To == bus.Addr(d.device, id) || fwd == bus.Addr(d.device, id)) {
				d.log.Printf("message %s is for %s, whose T3 thread is settled; dropped", e.ID, bus.Addr(d.device, id))
			}
			continue
		}
		if d.mutedHereLocked(id, e) {
			if mode == modeNormal && !held {
				d.log.Printf("message %s is from %s, which session %s has muted; dropped", e.ID, e.From, id)
			}
			if mode == modeNormal {
				d.mutedropLocked(id, e) // its sender is told (#466)
			}
			continue
		}
		if !d.inTimeLocked(e, id, mode) &&
			!(fwd == bus.Addr(d.device, id) && d.receivesLocked(sess) && d.inTimeLocked(e, sess, mode)) {
			continue
		}
		d.addInboxLocked(id, e)
		queued = true
		if id != proto.CLISession {
			d.noteConversationLocked() // a message arrived for a session here; its answer may soon follow
		}
	}
	_, stopped := d.st.Stopped[sess]
	if mode == modeNormal && !held && dev == d.device && sess != "" && sess != proto.CLISession &&
		d.sessions[sess] == nil && !stopped && fwd == "" {
		d.log.Printf("message %s is for %s, which isn't running here; dropped", e.ID, e.To)
	}
	return queued
}

// recipientsLocked lists the sessions a message can be queued for: those registered, which
// includes any in their grace, those stopped whose inbox is kept (keptLocked), and the CLI.
// Caller holds mu.
func (d *Daemon) recipientsLocked() []string {
	ids := sortedKeys(d.sessions)
	for _, id := range sortedKeys(d.st.Stopped) {
		if d.sessions[id] == nil && d.keptLocked(id) {
			ids = append(ids, id)
		}
	}
	if d.st.Person != "" {
		ids = append(ids, bus.PersonSession)
	}
	return append(ids, proto.CLISession)
}

// inTimeLocked says whether e, read in a sync of this mode, is new enough for session id: anything
// in a normal sync; from a backlog, what was sent after id started, and in a catch-up also what
// arrived while the daemon was down, for a session that was running before and reconnected, or
// one stopped, which may come back. A skip gives a stopped session only what was sent to it while
// its inbox was kept (see keptSinceLocked): it can't take all the daemon was down for, as that may
// be the whole history of a ref. Caller holds mu.
func (d *Daemon) inTimeLocked(e bus.Event, id string, mode syncMode) bool {
	if id == bus.PersonSession {
		// A person's daemon goes away when their page closes, and an answer can take hours: what was
		// sent to them is theirs, however long the daemon was down. What they already have is known
		// by which message it is (msgKey), so a read of old history adds nothing.
		return !d.msgKeys[msgKey(e)]
	}
	if mode == modeNormal || d.sentSinceLocked(e, id, mode) {
		return true
	}
	s := d.sessions[id] // nil for a stopped session
	if mode == modeSkip {
		return s == nil && d.keptSinceLocked(e, id)
	}
	return mode == modeCatchUp && id != proto.CLISession && (s == nil || s.reconnect)
}

// keptInboxLocked says some stopped session has its inbox kept (keptLocked) and has not
// registered again. Caller holds mu.
func (d *Daemon) keptInboxLocked() bool {
	for id := range d.st.Stopped {
		if d.sessions[id] == nil && d.keptLocked(id) {
			return true
		}
	}
	return false
}

// keptSinceLocked says whether e was sent while the inbox of stopped session id was kept, by the
// sender's clock: after the session stopped (with a minute's allowance, as sentSinceLocked has)
// and before StoppedInbox had run out. Caller holds mu.
func (d *Daemon) keptSinceLocked(e bus.Event, id string) bool {
	at, ok := d.st.Stopped[id]
	return ok && !e.At.Before(at.Add(-time.Minute)) && !e.At.After(at.Add(d.opt.StoppedInbox))
}

// sentSinceLocked says whether e was sent after session id started, by the sender's clock, with a
// minute's allowance for it. In a catch-up, a session back from stopped counts from when it
// stopped, which is what its kept inbox would have held had it not registered first. A skip
// keeps to when it started, so what a long outage skips doesn't depend on which came first.
// Caller holds mu.
func (d *Daemon) sentSinceLocked(e bus.Event, id string, mode syncMode) bool {
	s := d.sessions[id]
	if s == nil {
		return false
	}
	from := s.started
	if mode == modeCatchUp && !s.since.IsZero() {
		from = s.since
	}
	return !e.At.Before(from.Add(-time.Minute))
}

// addressedLocked says whether e is for session id on this device: sent to it, to this device, to
// a group it is in, or to everyone, and not by it. Who is in a group is decided here, by the tags
// the session has when the message is read: its own device is the one that knows them (#184). A message to a session that no longer holds its name is for the
// session that does instead (see forwardLocked). Caller holds mu.
func (d *Daemon) addressedLocked(e bus.Event, id string) bool {
	addr := bus.Addr(d.device, id)
	if e.From == addr {
		return false
	}
	if id == bus.PersonSession {
		return e.To == addr // agents' broadcasts show in their group chats, not as a person's unread
	}
	if bus.IsGroup(e.To) {
		return d.hasTagLocked(id, e.To)
	}
	if e.To == addr {
		return d.forwardNamedLocked(e.To, e.ToName) == ""
	}
	return e.To == "*" || e.To == d.device || d.forwardNamedLocked(e.To, e.ToName) == addr
}

// addInboxLocked queues message e for session id, once its files, if it carries any, are saved
// here (holdForFilesLocked). Caller holds mu.
func (d *Daemon) addInboxLocked(id string, e bus.Event) {
	for _, m := range d.st.Inbox[id] {
		if m.Event.ID == e.ID {
			return
		}
	}
	if d.holdForFilesLocked(id, e) {
		return
	}
	d.queueInboxLocked(id, e)
}

// queueInboxLocked puts message e in session id's inbox. Caller holds mu.
func (d *Daemon) queueInboxLocked(id string, e bus.Event) {
	for _, m := range d.st.Inbox[id] {
		if m.Event.ID == e.ID {
			return
		}
	}
	box := append(d.st.Inbox[id], &inMsg{Event: e, Arrived: time.Now()})
	if len(box) > maxInbox {
		box = box[len(box)-maxInbox:]
	}
	d.st.Inbox[id] = box
	d.markLocked(id, e, bus.MarkQueued)
	d.kickPush()
}

// catchUpLocked gives a session that reconnects after a restart what arrived for it while the
// daemon was down, less what a session it has muted sent, as queueLocked leaves that out (#482),
// and the changes to its tags that waited for it (holdForLocked). Caller holds mu.
func (d *Daemon) catchUpLocked(id string) {
	if time.Since(d.started) > catchUpWindow || d.caughtUp[id] {
		return
	}
	d.caughtUp[id] = true
	d.applyHeldLocked(id) // first: a group's message waiting for it goes by the tags they give it
	for _, e := range d.pending {
		if !d.addressedLocked(e, id) || d.offMeshLocked(id) {
			continue
		}
		if d.mutedHereLocked(id, e) {
			d.mutedropLocked(id, e) // what arrived while the daemon was down: its sender is told (#466)
			continue
		}
		d.addInboxLocked(id, e)
	}
	d.saveLocked()
}
