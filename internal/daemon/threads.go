package daemon

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/web"
)

// T3 threads' own state, read from T3's database, for two things:
//   - the page's "within N minutes" wake setting (#25): a message goes into a thread as a turn only
//     if the thread is busy, or has been idle no longer than that. A thread idle that briefly still
//     has its prompt cache; a turn started after the cache has gone reads the whole conversation
//     again at full price. T3 records when each run of a thread was asked for and when it ended.
//   - settled threads (#189): a thread T3 shows as settled, archived or deleted is offline. It leaves
//     presence, so `who` and the page no longer show it, a message for it is dropped, and `send`
//     refuses one, here and, through presence's Offline list, on other devices. A thread unsettled
//     again (someone typed in it) is back with the next read.
//   - delegates (#188): a thread another thread started (a T3 delegate_task child, a provider's
//     subagent thread; and a session a hook or Codex's state says another started, see codexkids.go) is
//     off the mesh in the same ways, and goes through its parent (Tyler,
//     2026-11-07): it can't send or set a status, and what it commits is credited to the session
//     of its parent thread here, if there is one. A session that wants it messages the parent. It
//     may have taken a name before its thread was read; nobody sees the name.
//
// The database is read every TitleEvery, outside every lock, one query per thread, for the threads
// of the sessions here and of those stopped whose route is kept. What was last read stands while a
// read fails.

// threadKey is a T3 thread, by T3's data directory and its ID.
type threadKey struct{ home, thread string }

// threadLoop reads the threads' state at once, then every TitleEvery.
func (d *Daemon) threadLoop(ctx context.Context) {
	t := time.NewTicker(d.opt.TitleEvery)
	defer t.Stop()
	for {
		d.refreshThreads(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshThreads reads the state of every thread a session here is in, or a stopped session whose
// inbox is kept was in. A change to whether a thread is settled is published, and any change lets
// the handoffs look again.
func (d *Daemon) refreshThreads(ctx context.Context) {
	kids := d.refreshKids(ctx) // before the inboxes and commits of delegates are seen to, below
	d.mu.Lock()
	routes := map[threadKey]t3Route{}
	// who names each thread in the log by a session in it: the thread's ID is the route, which
	// stays out of the log (see TestRouteStaysOnTheDevice).
	who := map[threadKey]string{}
	for id, s := range d.sessions {
		if r := s.route; r.T3CLI != "" && r.T3Thread != "" {
			k := threadKey{r.T3Home, r.T3Thread}
			routes[k] = t3Route{CLI: r.T3CLI, Home: r.T3Home, Thread: r.T3Thread}
			who[k] = bus.Addr(d.device, id)
		}
	}
	for id, r := range d.st.T3Routes {
		if d.sessions[id] == nil && d.receivesLocked(id) {
			k := threadKey{r.Home, r.Thread}
			routes[k] = r
			if who[k] == "" {
				who[k] = bus.Addr(d.device, id)
			}
		}
	}
	d.mu.Unlock()

	read := map[threadKey]t3db.State{}
	readAt := map[threadKey]time.Time{}
	failed := map[threadKey]error{}
	for k, r := range routes {
		if ctx.Err() != nil {
			return
		}
		readAt[k] = time.Now()
		st, err := d.opt.ThreadState(ctx, r.CLI, r.Home, r.Thread)
		if err != nil {
			failed[k] = err
			continue
		}
		read[k] = st
	}

	wake := d.wakeSnapshot() // setMu is taken alone, before mu
	d.mu.Lock()
	changed, settled := false, false
	for k, st := range read {
		d.threadRead[k] = readAt[k]
		if !st.Busy {
			// A session the hook has at work may have finished its turn without the Stop hook.
			changed = true
		}
		old, had := d.threads[k]
		if had && old == st {
			continue
		}
		changed = true
		if old.Settled != st.Settled {
			settled = true
			if st.Settled {
				d.log.Printf("the T3 thread of session %s is settled, so it is offline", who[k])
			} else if had {
				d.log.Printf("the T3 thread of session %s is no longer settled; it is online again", who[k])
			}
		}
		if old.Model != st.Model {
			settled = true // the derived model: tag goes out in presence (#198)
		}
		if (old.Parent != "") != (st.Parent != "") {
			settled = true
			if st.Parent != "" {
				d.log.Printf("session %s is in a thread another thread started, so it is off the mesh", who[k])
			}
		}
		d.threads[k] = st
		delete(d.threadWarned, k)
	}
	// A turn the hook saw start and no Stop end, as one the user interrupted, is over once a read
	// that began after the hook last ran finds no run (#202).
	for _, s := range d.sessions {
		if r := s.route; s.hooked && !s.busyAt.IsZero() && r.T3Thread != "" {
			k := threadKey{r.T3Home, r.T3Thread}
			if st, ok := read[k]; ok && !st.Busy && readAt[k].After(s.busyAt) {
				d.endTurnLocked(s, wake)
			}
		}
	}
	// A delegate's inbox stays empty however a message got in, and its commits go to its parent,
	// those made before its thread was read included.
	for _, id := range sortedKeys(d.st.Inbox) {
		d.clearChildInboxLocked(id)
	}
	if d.moveChildCommitsLocked() {
		settled = true
	}
	for k, err := range failed {
		if !d.threadWarned[k] {
			d.threadWarned[k] = true
			d.log.Printf("can't read the state of the T3 thread of session %s (%v); what was last read stands", who[k], err)
		}
	}
	for k, st := range d.threads {
		if _, ok := routes[k]; !ok {
			delete(d.threads, k)
			delete(d.threadRead, k)
			delete(d.threadWarned, k)
			off := st.Settled || st.Parent != ""
			changed = changed || off
			settled = settled || off
		}
	}
	d.mu.Unlock()
	if settled || kids {
		d.kickPresence()
	}
	if changed || kids {
		d.kickPush()
	}
}

// threadStateLocked is what T3's database last said about the thread session id is in, and false
// if the session isn't in T3 or nothing has been read yet. Caller holds mu.
func (d *Daemon) threadStateLocked(id string) (t3db.State, bool) {
	r := d.routeOfLocked(id)
	if r.T3CLI == "" || r.T3Thread == "" {
		return t3db.State{}, false
	}
	st, ok := d.threads[threadKey{r.T3Home, r.T3Thread}]
	return st, ok
}

// offMeshLocked says session id is in a T3 thread that is settled, archived or deleted, or is one
// another session started, by T3's word or another's (#452, codexkids.go): it is offline, and
// takes no messages. Caller holds mu.
func (d *Daemon) offMeshLocked(id string) bool {
	if _, kid := d.kids[id]; kid {
		return true
	}
	st, ok := d.threadStateLocked(id)
	return ok && (st.Settled || st.Parent != "")
}

// parentOfLocked says session id is in a thread another thread started, and which session here is
// in that thread: "" if none is, as when the parent isn't on the mesh here. Caller holds mu.
func (d *Daemon) parentOfLocked(id string) (parent string, child bool) {
	if k, kid := d.kids[id]; kid {
		return d.kidParentLocked(id, k), true
	}
	st, ok := d.threadStateLocked(id)
	if !ok || st.Parent == "" {
		return "", false
	}
	home := d.routeOfLocked(id).T3Home
	var dropped string
	for _, s := range d.sortedSessionsLocked() {
		if s.route.T3Thread == st.Parent && s.route.T3Home == home {
			if s.dropped.IsZero() {
				return s.id, true
			}
			dropped = s.id
		}
	}
	if dropped != "" {
		return dropped, true
	}
	for _, sid := range sortedKeys(d.st.T3Routes) {
		r := d.st.T3Routes[sid]
		if r.Thread == st.Parent && r.Home == home && d.sessions[sid] == nil && d.receivesLocked(sid) {
			return sid, true
		}
	}
	return "", true
}

// parentNameLocked is how a child session's parent is named to an agent: its name, else its
// address, else as the thread that started it. Caller holds mu.
func (d *Daemon) parentNameLocked(parent string) string {
	if parent == "" {
		return "the thread that started it"
	}
	return nameOr(d.sessionNameLocked(parent), bus.Addr(d.device, parent))
}

// clearChildInboxLocked empties session id's inbox if it is a delegate, and says whether it is one.
// What reached it before its thread was read (a message for everyone in the first seconds, or
// before a restarted MCP server found its thread) is not its to read. A message on its way in as a
// turn stays until that handoff ends; it is refused at the handoff (sendTurn), and cleared here or
// at the next read. Caller holds mu.
func (d *Daemon) clearChildInboxLocked(id string) bool {
	if _, child := d.parentOfLocked(id); !child {
		return false
	}
	box := d.st.Inbox[id]
	var left []*inMsg
	for _, m := range box {
		if m.Pushing {
			left = append(left, m)
		}
	}
	if len(left) == len(box) {
		return true
	}
	if len(left) == 0 {
		delete(d.st.Inbox, id)
	} else {
		d.st.Inbox[id] = left
	}
	d.saveLocked()
	return true
}

// moveChildCommitsLocked moves the commits credited to delegates whose parent is on the mesh here
// to the parent: those a delegate made before its thread was read. It says whether any moved.
// Caller holds mu.
func (d *Daemon) moveChildCommitsLocked() bool {
	moved := false
	for _, id := range sortedKeys(d.st.Commits) {
		parent, child := d.parentOfLocked(id)
		if !child || parent == "" {
			continue
		}
		recs := d.st.Commits[parent]
		for _, r := range d.st.Commits[id] {
			if !slices.ContainsFunc(recs, func(o commitRec) bool { return o.SHA == r.SHA }) {
				recs = append(recs, r)
			}
		}
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].At.Before(recs[j].At) })
		if len(recs) > maxCommits {
			recs = slices.Clone(recs[len(recs)-maxCommits:])
		}
		d.st.Commits[parent] = recs
		delete(d.st.Commits, id)
		d.saveLocked()
		moved = true
	}
	return moved
}

// offlineWhyLocked says why the session at dev/sess, called shown, takes no messages, for a sender
// whose message is refused. Caller holds mu.
func (d *Daemon) offlineWhyLocked(dev, sess, shown string) string {
	if dev == d.device {
		if parent, child := d.parentOfLocked(sess); child {
			return fmt.Sprintf("%s is a delegate that another thread started, and a delegate is reached through the "+
				"thread that started it. Message %s instead.", shown, d.parentNameLocked(parent))
		}
		return fmt.Sprintf("%s is a T3 thread that is settled, so it is offline and takes no messages. "+
			"It is back once someone opens the thread again.", shown)
	}
	return fmt.Sprintf("%s is offline and takes no messages: its T3 thread is settled, or it is a delegate "+
		"another thread started, which is reached through that thread.", shown)
}

// childRefusal is why session id may not send or set a status, "" if it may: a delegate goes
// through the thread that started it (#188).
func (d *Daemon) childRefusal(id string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	parent, child := d.parentOfLocked(id)
	if !child {
		return ""
	}
	return fmt.Sprintf("you are a delegate that another thread started, and only that thread is on %s: "+
		"tell it what you would send or what you are doing, and it decides what goes out. It is %s.",
		brand.Product, d.parentNameLocked(parent))
}

// offlineLocked says the session at dev/sess is off the mesh: here by T3's database (settled, or a
// child), on another device by that device's presence. Caller holds mu.
func (d *Daemon) offlineLocked(dev, sess string) bool {
	if sess == "" {
		return false
	}
	if dev == d.device {
		return d.offMeshLocked(sess)
	}
	p := d.peers[dev]
	return p != nil && !d.staleLocked(p) && slices.Contains(p.pres.Offline, sess)
}

// offlineIDsLocked lists, for presence, the sessions here that are off the mesh: running, or
// stopped with their inbox kept, and the earlier IDs of their names that a message is forwarded
// from (see forwardLocked). Another device may still know a name by such an ID: one a thread had
// before its MCP server restarted under a new one. Caller holds mu.
func (d *Daemon) offlineIDsLocked() []string {
	set := map[string]bool{}
	for id := range d.sessions {
		if d.offMeshLocked(id) {
			set[id] = true
		}
	}
	for id := range d.st.T3Routes {
		if d.sessions[id] == nil && d.receivesLocked(id) && d.offMeshLocked(id) {
			set[id] = true
		}
	}
	for id := range d.st.SessionNames {
		if f := d.forwardLocked(bus.Addr(d.device, id)); f != "" {
			if _, holder := bus.SplitAddr(f); d.offMeshLocked(holder) {
				set[id] = true
			}
		}
	}
	return sortedKeys(set)
}

// touchLocked notes that session s is at work now. A message held back because s had been idle
// longer than its wake setting allows may go in as a turn now, so the handoffs look again; the
// hook's notice leaves alone what they will hand over (see queuedForPushLocked). Caller holds mu.
func (d *Daemon) touchLocked(s *session, now time.Time) {
	s.lastActive = now
	if len(d.st.Inbox[s.id]) > 0 {
		d.kickPush()
	}
}

// idleSinceLocked is when session id was last at work, as far as is known, and whether it is at
// work now. A session the hook has at work is (see hookBusyLocked). A session in T3 goes by T3's
// database: busy while a run is queued or running, else
// idle since its latest run. Any other session, or one in T3 whose thread hasn't been read, goes
// by the session: its start, its last request or tool call, or when it stopped. A message
// handed over as a turn counts too. Zero if nothing says. Caller holds mu.
func (d *Daemon) idleSinceLocked(id string) (since time.Time, busy bool) {
	if d.hookBusyLocked(id) {
		return time.Time{}, true
	}
	since = d.pushedAt[id]
	if st, ok := d.threadStateLocked(id); ok {
		if st.Busy {
			return time.Time{}, true
		}
		return later(since, st.LastRun), false
	}
	if s := d.sessions[id]; s != nil {
		since = later(since, s.started, s.lastActive)
	}
	if at, ok := d.st.Stopped[id]; ok && d.sessions[id] == nil {
		since = later(since, at)
	}
	return since, false
}

// later is the latest of ts.
func later(ts ...time.Time) time.Time {
	var out time.Time
	for _, t := range ts {
		if t.After(out) {
			out = t
		}
	}
	return out
}

// choice is the page's wake setting for the session called name: its own, if it has one, else the
// device's.
func (p pageSettings) choice(name string) web.Wake {
	if o, ok := p.SessionWake[name]; ok && name != "" {
		return o
	}
	return p.Wake
}

// busyChoice is the page's setting for what session name is given by the hook while it is busy
// (#204): its own, if it has one, else the device's, else the subject only.
func (p pageSettings) busyChoice(name string) string {
	if o, ok := p.SessionBusy[name]; ok && name != "" {
		return o
	}
	return cmp.Or(p.Busy, web.BusySubject)
}

// wakesLocked says whether a message may go into session id as a turn now. Not if it is off the
// mesh, nor if the page says never. Always: yes. Within N minutes (the default, and what an
// unset choice means): if the session is busy, which needs no waking, or was last at work no
// more than N minutes ago, 0 standing for defaultWakeMinutes. A session nothing is known of yet
// waits; the read of its thread lets the handoffs look again. Nor while it is on do not disturb
// (#198). Caller holds mu.
func (d *Daemon) wakesLocked(id string, wake pageSettings) bool {
	return !d.dndLocked(id) && d.mayWakeLocked(id, wake)
}

// mayWakeLocked is wakesLocked without do not disturb: whether the session could be woken once the
// tag comes off. The daemon stays up for a thread it may wake so (see holdsRouteLocked): it is
// the one to read the tag's removal from another device's page, and to hand over what waited.
// Caller holds mu.
func (d *Daemon) mayWakeLocked(id string, wake pageSettings) bool {
	if d.offMeshLocked(id) {
		return false
	}
	w := wake.choice(d.sessionNameLocked(id))
	switch w.When {
	case web.WakeNever:
		return false
	case web.WakeAlways:
		return true
	}
	since, busy := d.idleSinceLocked(id)
	if busy {
		return true
	}
	limit := time.Duration(cmp.Or(w.Minutes, defaultWakeMinutes)) * time.Minute
	return !since.IsZero() && time.Since(since) <= limit
}
