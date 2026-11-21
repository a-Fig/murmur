package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/clientcli"
	"github.com/a-Fig/murmur/internal/msgsock"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/view"
)

// Delivering a message as a turn (#57). A message that arrives for a session is handed over to the
// session's client at once, to become the next thing it is asked to do, the way a message
// between T3 threads is, so an idle agent starts working on it and nobody has to type. Where that
// can't be done the message waits for the hook's notice and `inbox`, as it did before.
//
// There are three ways in, by what the session's MCP server found out about its client (proto.Route):
//   - inside T3, `t3 thread send <thread>`: an idle thread starts a turn, a busy one takes the
//     message as its next turn, nothing is interrupted, and it works for a thread whose MCP server
//     T3 has stopped (idle threads lose theirs after 30 minutes), which is the thread most worth
//     waking;
//   - in Claude Code outside T3, the session's messaging socket (package msgsock), and in Pi
//     outside T3 the socket Murmur's extension opens in the session, which speaks the same lines;
//   - in Codex and OpenCode 2 outside T3, the client's own command line (package clientcli, #135):
//     `codex queue` and `opencode api session.prompt`, which do as `t3 thread send` does.
//
// A session Murmur's hook runs in (Claude Code, and Pi through its extension) is not given a turn
// while it is at work (#202): T3 would queue it behind the running turn, and nothing could take it
// back, so the agent could read it with `inbox` and then get it again as a turn. The hook tells it
// of the message after its next tool call instead, and what it hasn't read when the turn ends keeps
// the turn going: the Stop hook gives it to the agent (continueTurnLocked). A turn that ends without
// Stop, one the user interrupted in T3, has it handed over as a turn instead, and so does one whose
// Stop can't keep it going (Pi's after Escape or an error).
//
// Handing a message over is not the agent having it: it is read (it arrives whole, so it skips
// delivered) when the turn carrying it starts, which the daemon doesn't see
// (docs/design/delivery.md, "Delivered and read", #325). A message handed over is removed from the
// inbox, as the client holds it now: neither the hook nor `inbox` shows it again.
// docs/design/delivery.md has the reasons.

const (
	// maxPushes is how many handoffs run at once. Each `t3 thread send` starts a process, and a
	// broadcast reaches every session on the device.
	maxPushes = 4
	// pushMaxAge is how old a message may be and still wake a session: one that waited longer than
	// this (T3 was closed, a session was away) is left to the hook's notice, not sprung on the
	// agent as a turn.
	pushMaxAge = 15 * time.Minute
)

// errNotNow is what a handoff ends with when, by the time of the handoff, the session may no
// longer be woken (see mayWakeLocked and heldLocked).
var errNotNow = errors.New("the session may not be woken now")

// How a message goes into a session.
const (
	viaT3       = "T3"
	viaSocket   = "the messaging socket"
	viaCodex    = "Codex's queue"
	viaOpenCode = "OpenCode's server"
)

// kickPush wakes pushLoop, which finds messages to hand over. Any lock may be held.
func (d *Daemon) kickPush() {
	select {
	case d.pushKick <- struct{}{}:
	default:
	}
}

// pushLoop starts a handoff goroutine for each session that has a message to hand over and none
// running. A session's messages go one at a time, in the order they arrived; sessions go in
// parallel.
func (d *Daemon) pushLoop(ctx context.Context) {
	// Messages kept over a restart never passed through addInboxLocked, which is what kicks: look at
	// them once, before waiting for anything.
	d.kickPush()
	// And once more when sessions the last daemon's stop ended are no longer waited for
	// (awaitedLocked): a run still going then changes nothing that would kick.
	hold := time.AfterFunc(reconnectHold+time.Second, d.kickPush)
	defer hold.Stop()
	for {
		// A continuation that lapses (see continueTurnLocked), or a handoff's hold (handedHold), is
		// woken for too: nothing else may come to end it.
		var lapse <-chan time.Time
		d.mu.Lock()
		if at := d.nextLapseLocked(); !at.IsZero() {
			lapse = time.After(time.Until(at))
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-d.pushKick:
		case <-lapse:
		}
		wake := d.wakeSnapshot()
		d.mu.Lock()
		d.endLapsedTurnsLocked(wake)
		for _, id := range sortedKeys(d.st.Inbox) {
			if d.updating || d.pushing[id] || ctx.Err() != nil {
				continue
			}
			if m, _, _ := d.candidateLocked(id, wake); m == nil {
				continue
			}
			d.pushing[id] = true
			d.bg.Add(1) // pushLoop is itself in bg, so this never races bg.Wait
			go func() {
				defer d.bg.Done()
				d.pusher(ctx, id)
			}()
		}
		d.mu.Unlock()
	}
}

// pusher hands session id's messages over until it has none left to hand over.
//
// Nothing is handed over for a user who has left (docs/design/joining.md): the daemon exits
// within ConsentPoll of the consent going, and until then a message that comes in by sync waits in
// the inbox instead of being put into a thread or a socket. The handoff is the one thing the daemon does
// to the user's other programs, so it asks here, before taking a message, and again in sendTurn,
// before each attempt.
func (d *Daemon) pusher(ctx context.Context, id string) {
	for {
		if !d.consented() || d.hidden.Load() {
			d.mu.Lock()
			delete(d.pushing, id)
			d.mu.Unlock()
			return
		}
		wake := d.wakeSnapshot()
		d.mu.Lock()
		job := d.nextPushLocked(id, wake)
		d.mu.Unlock()
		if job == nil || ctx.Err() != nil {
			return
		}
		d.finishPush(job, d.handOver(ctx, job))
	}
}

// pushJob is one message on its way into a session.
type pushJob struct {
	id, addr string
	// key is the session's conversation when the job was made (see Daemon.introduced): the turn
	// goes to that one, even if the session moves to another before the handoff ends.
	key string
	// conv and explainKey are the conversation the session was in then, known for a session of T3
	// that has stopped too (where key is ""), and what that was kept by, for what it has been told
	// (see explained.go).
	conv, explainKey string
	// msgs are the messages the turn carries, in the order they arrived: one, or, for a session in
	// T3 with no hook, all it has waiting (see batchLocked).
	msgs  []*inMsg
	via   string
	route proto.Route
	text  string
}

// wakeSnapshot copies the page's settings, the wake setting among them. setMu is taken alone,
// never with mu held.
func (d *Daemon) wakeSnapshot() pageSettings {
	d.setMu.Lock()
	defer d.setMu.Unlock()
	if d.settingsMove.Load() {
		d.moveSettings()
	}
	return d.settings.clone()
}

// moveSettings moves the page's wake and busy choices for one session from the name it had
// before this clone lost its keys to the one it has now (st.SettingsMoves, #836). They are kept
// by name, as its tags are, and the tags follow it (adoptLeftLocked); left behind, a session
// someone set never to be woken came back under its new name with the device's choice, and was
// given turns. The name lands under mu, and settings are never taken inside that, so the names
// wait in the state file for the next reader of the settings, which is here; meanwhile the
// session is looked up by its old name (settingsNameLocked). A choice the new name already has
// stands. A save that fails leaves the names waiting. Caller holds setMu, and not mu.
func (d *Daemon) moveSettings() {
	d.mu.Lock()
	moves := slices.Clone(d.st.SettingsMoves)
	d.mu.Unlock()
	next := d.settings.clone()
	renamed := false
	for _, m := range moves {
		if renameSettings(&next, m[0], m[1]) {
			renamed = true
		}
	}
	if renamed {
		if err := writeJSON(d.settingsPath(), next); err != nil {
			if !d.settingsMoveFailed { // every read of the settings tries again
				d.log.Printf("moving %d sessions' settings to their new names: %v; trying again each time the settings are read", len(moves), err)
			}
			d.settingsMoveFailed = true
			return
		}
		d.settings = next
	}
	d.settingsMoveFailed = false
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.SettingsMoved == nil {
		d.st.SettingsMoved = map[string]string{}
	}
	for _, m := range moves {
		d.st.SettingsMoved[m[0]] = m[1]
	}
	// Those moved, and no others: a name may have landed since they were read.
	d.st.SettingsMoves = slices.Clone(d.st.SettingsMoves[min(len(moves), len(d.st.SettingsMoves)):])
	if len(d.st.SettingsMoves) == 0 {
		d.st.SettingsMoves = nil
		d.settingsMove.Store(false)
	}
	d.saveLocked()
}

// renameSettings moves old's own wake and busy choices in p to name, unless name has its own, and
// says whether old had any.
func renameSettings(p *pageSettings, old, name string) (had bool) {
	if w, ok := p.SessionWake[old]; ok {
		delete(p.SessionWake, old)
		if _, has := p.SessionWake[name]; !has {
			p.SessionWake[name] = w
		}
		had = true
	}
	if b, ok := p.SessionBusy[old]; ok {
		delete(p.SessionBusy, old)
		if _, has := p.SessionBusy[name]; !has {
			p.SessionBusy[name] = b
		}
		had = true
	}
	return had
}

// movedSettings gives the choices in p that a page sent for a name since moved from to the name
// it was moved to (st.SettingsMoved): the page sends the whole of what it read, and one that read
// before the move would put the choice back on the old name and take it off the new one, for
// good. A choice the page sent for the new name stands. Caller holds setMu, and not mu.
func (d *Daemon) movedSettings(p *pageSettings) {
	d.mu.Lock()
	moved := maps.Clone(d.st.SettingsMoved)
	d.mu.Unlock()
	for range len(moved) { // a name moved, and moved again after a second loss
		any := false
		for _, old := range sortedKeys(moved) {
			if renameSettings(p, old, moved[old]) {
				any = true
			}
		}
		if !any {
			break
		}
	}
}

// settingsNameLocked is the name the page's own choices for session id are kept by
// (pageSettings.choice): its name, or the one it had before this clone lost its keys, while its
// new one is pending or the choices haven't been moved to it yet (moveSettings). Caller holds mu.
func (d *Daemon) settingsNameLocked(id string) string {
	name := d.sessionNameLocked(id)
	if name == "" {
		if conv := d.conversationLocked(id); conv != "" {
			name = d.st.NamesLeft[conv]
		}
		if name == "" {
			return ""
		}
	}
	// Back along the moves still to be made, to the name that has the choices: lost twice with no
	// save between (a failed one), they are two names back, and the one between has none.
	for range len(d.st.SettingsMoves) {
		prev := ""
		for _, m := range d.st.SettingsMoves {
			if m[1] == name {
				prev = m[0]
			}
		}
		if prev == "" || prev == name {
			break
		}
		name = prev
	}
	return name
}

// routeOfLocked is how a message gets into session id: what its MCP server told the daemon if it
// is registered, else, for a session of T3 that has stopped, where its thread is. Caller holds mu.
func (d *Daemon) routeOfLocked(id string) proto.Route {
	if s := d.sessions[id]; s != nil {
		return s.route
	}
	if r, ok := d.st.T3Routes[id]; ok {
		return proto.Route{T3CLI: r.CLI, T3Home: r.Home, T3Thread: r.Thread}
	}
	return proto.Route{}
}

// viaOf is the way to hand a message over to a session with route r, "" if there is none. A session in T3
// is reached through T3 only: its messaging socket is T3's to use, and T3 may not take a turn it
// didn't start. live says the session's MCP server is connected, which the socket needs, as the
// token dies with the server's process. The client's command line is used only then too: a Codex
// thread nobody has open keeps a queued message until it is next resumed, which may be days on,
// and by then the message has left the inbox.
func viaOf(r proto.Route, live bool) string {
	switch {
	case r.T3CLI != "" && r.T3Thread != "":
		return viaT3
	case r.T3CLI != "" || !live:
		return ""
	case r.MsgSocket != "" && r.MsgToken != "":
		return viaSocket
	case r.CodexCLI != "" && r.Native != "":
		return viaCodex
	case r.OpenCodeCLI != "" && r.Native != "":
		return viaOpenCode
	}
	return ""
}

// candidateLocked finds the next message to hand over to session id, with how and where, or nil.
// A message that is being handed over, that the hook has told the session about, whose handoff
// failed, or that is too old, is passed over, and so is every message do not disturb holds
// (heldLocked), and every other message of a session that may not be woken now (mayWakeLocked)
// but for a person's question (#242). The session waits while another session's copy of its next message
// is on its way into the same T3 thread (see handing). A session with a message and no way in is
// logged, once. Caller holds mu.
func (d *Daemon) candidateLocked(id string, wake pageSettings) (*inMsg, string, proto.Route) {
	var next, person *inMsg
	for _, m := range d.st.Inbox[id] {
		if m.Pushing {
			// One at a time, so they arrive in order. A message on its way in that isn't this
			// session's pusher's came with the inbox of a session this one replaced (see
			// takeInboxLocked): its pusher kicks when it is done.
			return nil, "", proto.Route{}
		}
		if !pushable(m) || d.heldLocked(id, m) {
			continue
		}
		if next == nil {
			next = m
		}
		if person == nil && personAsks(m) {
			person = m
		}
	}
	if next == nil || d.hookBusyLocked(id) {
		return nil, "", proto.Route{}
	}
	if !d.mayWakeLocked(id, wake) {
		// A person asked this session something, and an answer that waits for its next prompt is no
		// answer: their message goes in past how long it has been idle, ahead of what from other
		// agents is left waiting for that (#242).
		if person == nil || !d.personWakesLocked(id, wake) {
			return nil, "", proto.Route{}
		}
		next = person
	}
	s := d.sessions[id]
	route := d.routeOfLocked(id)
	via := viaOf(route, s != nil && s.dropped.IsZero())
	if via == "" {
		if s != nil && !d.pushWarned[id] {
			d.pushWarned[id] = true
			d.log.Printf("session %s has no way to take a message as a turn (%s); its messages show as notices through the hook",
				bus.Addr(d.device, id), noWay(route, s.dropped.IsZero()))
		}
		return nil, "", proto.Route{}
	}
	if via == viaT3 && d.handing[handing{threadKey{route.T3Home, route.T3Thread}, next.Event.ID}] {
		return nil, "", proto.Route{}
	}
	return next, via, route
}

// pushable says a message may still be handed over as a turn: it isn't on its way in already, hasn't
// failed, and wasn't handed over before (Attempted: not even by a daemon that has since
// restarted). One the hook held for the end of a turn may (Held); any other the hook told the
// session of may not, nor may one too old.
func pushable(m *inMsg) bool {
	if m.Pushing || m.PushFailed || m.Attempted {
		return false
	}
	if _, sess := bus.SplitAddr(m.Event.From); sess == bus.ReceiptFrom && !m.Held {
		// Murmur's notice of how far a message got (#276): the sender asked to be told, not woken.
		// Only that comes from a receipts address to an inbox. One the hook told of while the
		// session worked goes in at the turn's end, as any message does.
		return false
	}
	return m.Held || !m.Noticed && time.Since(waitedSince(m)) <= pushMaxAge
}

// waitedSince is when message m counts as having arrived: when it came, or, for one queued
// through do not disturb, when the tag came off (#198). A message released then is as new as one
// that came then, for how old it may be to go in as a turn and for whether it came during a turn.
func waitedSince(m *inMsg) time.Time {
	if m.Released.After(m.Arrived) {
		return m.Released
	}
	return m.Arrived
}

// turnableLocked says message m may go into session id as a turn, whether or not the session is
// at work now: it is pushable, the page's wake setting lets it through, and there is a way in.
// Caller holds mu.
func (d *Daemon) turnableLocked(id string, m *inMsg, wake pageSettings) bool {
	return pushable(m) && d.wayInLocked(id, m, wake)
}

// holdableLocked says message m may be held for the end of session id's turn, which began at
// turnStart, and go in as a turn then (#202): it wasn't handed over before, the page's wake setting
// lets it through, and there is a way in. One that came during the turn may be any age, as a turn
// can take longer than pushMaxAge and the agent was at work the whole time; one from before must
// still be young enough, as one that aged during an absence stays a notice. Caller holds mu.
func (d *Daemon) holdableLocked(id string, m *inMsg, wake pageSettings, turnStart time.Time) bool {
	if m.Pushing || m.PushFailed || m.Attempted {
		return false
	}
	if at := waitedSince(m); (turnStart.IsZero() || at.Before(turnStart)) && time.Since(at) > pushMaxAge {
		return false
	}
	return d.wayInLocked(id, m, wake)
}

// wayInLocked says message m may go into session id as a turn, as far as the session goes: do not
// disturb doesn't hold it (heldLocked), and sessionWayInLocked. Caller holds mu.
func (d *Daemon) wayInLocked(id string, m *inMsg, wake pageSettings) bool {
	return !d.heldLocked(id, m) && d.sessionWayInLocked(id, wake)
}

// sessionWayInLocked says a message do not disturb doesn't hold may go into session id as a turn:
// the page's wake setting lets it through, and there is a way in. The Stop hook is one, for a
// session whose last Stop could keep its turn going, inside T3 or out: what came during a turn is
// given at its end, even with no socket. Caller holds mu.
func (d *Daemon) sessionWayInLocked(id string, wake pageSettings) bool {
	if !d.mayWakeLocked(id, wake) {
		return false
	}
	s := d.sessions[id]
	if s != nil && s.stopContinues {
		return true
	}
	return viaOf(d.routeOfLocked(id), s != nil && s.dropped.IsZero()) != ""
}

// queuedForPushLocked says message m of session id is on its way in as a turn, or will be: the
// hook's notice leaves it alone, as it would be a second telling of what the turn says, and
// would take the message out of the handoff for good. A session the hook has at work gets the
// notice instead (#202), unless another session's copy of m is on its way into its T3 thread
// (#99): that turn carries it. Caller holds mu.
func (d *Daemon) queuedForPushLocked(id string, m *inMsg, wake pageSettings) bool {
	if r := d.routeOfLocked(id); m.Pushing || r.T3Thread != "" && d.handing[handing{threadKey{r.T3Home, r.T3Thread}, m.Event.ID}] {
		return true
	}
	return !d.hookBusyLocked(id) && d.turnableLocked(id, m, wake)
}

// hookBusyLocked says Murmur's hook has session id at work: it ran for a prompt or a tool call,
// and not yet for the end of the turn. Such a session is told of a message by the hook after its
// next tool call, not given it as a turn (#202). Only where the end of the turn will be seen: the
// Stop hook has run for the session before, or T3's database has its thread.
//
// In T3 the database ends a turn too, as one the user interrupts fires no Stop: a read of it that
// began after the hook last ran, and finds no run of the thread queued or running, means the turn
// is over, whatever the order in which a late hook and the run's end came. A session whose
// connection dropped keeps its state through its grace, and one that reconnected after this
// daemon restarted, before its hook has run here, is at work while T3 shows its thread's run going.
// Outside T3 nothing but Stop ends a turn. Claude Code fires none when interrupted, so its session
// waits for its next prompt and the Stop after it; Pi's extension sends an interrupted Stop.
//
// In T3 a turn can also start before the hook sees it (runUnseen).
//
// A session in T3 whose hook hasn't run (none runs for OpenCode 1 there, and none for Muse before
// #461 or before its user runs join again) is at work by T3's database alone (hooklessBusy): its
// messages wait in the inbox, where `inbox` finds them, and go in together as one turn when the
// run ends (#699). Once its hook runs it is a hooked session like any other. Caller holds mu.
func (d *Daemon) hookBusyLocked(id string) bool {
	s := d.sessions[id]
	if s == nil {
		return d.awaitedLocked(id)
	}
	st, inT3 := d.threadStateLocked(id)
	r := d.routeOfLocked(id)
	read := d.threadRead[threadKey{r.T3Home, r.T3Thread}]
	if !s.hooked {
		if r.T3Thread == "" {
			return false
		}
		// Reconnected after a restart: for its first minute, at work until its thread has been read
		// at all.
		return hooklessBusy(s, st, inT3, read) || s.reconnect && !inT3 && time.Since(s.started) < reconnectHold
	}
	switch {
	case s.busyAt.IsZero():
		return r.T3Thread != "" && runUnseen(s, st, inT3, read)
	case !inT3:
		return s.stopSeen && !d.continuationLapsedLocked(s)
	}
	return runGoing(st) || !read.After(s.busyAt)
}

// runUnseen says T3 has a run of session s's thread going that the hook hasn't seen start, where
// the hook has no turn of s's going: st is what T3's database last said, inT3 that it has said
// anything, and read when that read began (#607).
//
// T3 marks a run queued or running when it takes the prompt, and Claude Code fires
// UserPromptSubmit only when it starts the turn, which can be minutes later. Between the two the
// session looked idle, and a message was handed to T3, which queued it behind the run as a turn
// of its own (docs/design/delivery.md has what T3's database showed).
//
// So a read that began after the turn last ended and finds a run going is a turn. A read from
// before the end, or within closeGrace after it, may have seen the run Stop ended, and doesn't
// count. That also covers a turn a Stop hook of the user's own kept going after Murmur's saw
// Stop, once the next read comes. A message just handed to the thread is a turn too, until a read
// that began after it says (T3 has made the run by the time `t3 thread send` returns), or
// handedHold passes with no read; sendTurn also reads the thread just before each handoff.
//
// A run no hook has shown going for unseenHold no longer counts: T3 can leave a run queued for
// days (one from 2026-11-03 was still queued on 11-15), and no hook would ever tell the session
// of its messages.
func runUnseen(s *session, st t3db.State, inT3 bool, read time.Time) bool {
	if s.handedAt.After(s.endedAt) && !read.After(s.handedAt) && time.Since(s.handedAt) < handedHold {
		return true
	}
	if !inT3 || !runGoing(st) || !read.After(s.endedAt.Add(closeGrace)) {
		return false
	}
	return !s.unseenAt.After(s.endedAt) || time.Since(s.unseenAt) < unseenHold
}

// hooklessBusy says session s, in T3 and with no hook of its own seen, is at work by T3's database
// (#699): st, what it last said, shows a run of the thread going, or the daemon has just handed the
// thread a message and no read has begun since (T3 has made the run by the time `t3 thread send`
// returns), for at most handedHold. inT3 says the database has said anything, and read is when that
// read began. A thread never read holds nothing: where T3's database can't be read, messages go to
// T3 as they did before #699, rather than each waiting out handedHold behind the last.
//
// Until 2026-11-16 nothing said such a session was at work, so each message went to `t3 thread send`
// at once and T3 queued it behind the run as a turn of its own: four messages to a busy Muse thread
// were four queued turns, and `inbox` mid-turn found none of them, as they had left it.
//
// No cap like runUnseen's unseenHold: a hook would never come to tell the session of its messages
// either way, and a message handed to T3 during a run that never ends would wait in T3's queue as
// long as it waits in the inbox, where `inbox` and the page still have it.
func hooklessBusy(s *session, st t3db.State, inT3 bool, read time.Time) bool {
	if !inT3 {
		return false
	}
	if !s.handedAt.IsZero() && !read.After(s.handedAt) && time.Since(s.handedAt) < handedHold {
		return true
	}
	return runGoing(st)
}

// runGoing says st shows a run going that a turn of the thread's session may be: runs T3 left
// queued behind later ones (Left) aren't, or every turn's end would start a run the hook hasn't
// seen, holding messages unseenHold each time. Everything that asks T3's database whether a
// session is at work asks this, not st.Busy: a run left queued for days would otherwise keep a
// reconnected session, or an interrupted turn, at work until its next hook (#655).
func runGoing(st t3db.State) bool { return st.Busy && !st.Left }

// closeGrace is how long after a turn's end a read of T3's database may still find the run Stop
// ended open: T3 closed 99% of 1,212 Claude runs within 0.4 s of the turn's end (2026-11-11 to
// 11-15). Without it, the handoff that follows Stop, read just before it, would wait for the next
// read.
const closeGrace = time.Second

// handedHold is how long a message handed to a session's T3 thread keeps the session at work
// before T3's database has been read: a read that keeps failing mustn't hold its messages for
// good.
const handedHold = time.Minute

// unseenHold is how long a run no hook has shown going counts as a turn (runUnseen). Past it the
// session's messages go to T3 as before. Claude's turn once began 27 minutes after T3 took the
// prompt (Tyler's laptop, 2026-11-13). pushMaxAge is used, as a message that waited longer than
// that stays a notice anyway.
const unseenHold = pushMaxAge

// unseenStart is when session s's current turn began before its hook saw it: when the daemon handed
// its thread a message, or a read first found the run, whichever came first since its turn last
// ended; zero if neither did.
func (s *session) unseenStart() time.Time {
	var at time.Time
	for _, t := range []time.Time{s.handedAt, s.unseenAt} {
		if t.After(s.endedAt) && (at.IsZero() || t.Before(at)) {
			at = t
		}
	}
	return at
}

// noteUnseenLocked keeps, for each session whose hook has no turn going, what the latest read of
// its T3 thread says of a run the hook hasn't seen start. Caller holds mu.
//
// A read that began after the turn ended (and closeGrace) and finds a run going dates the run
// (unseenAt) if it isn't yet: from when T3 took the prompt, its LastRun while the run goes, else
// from the read. A message that came after that came during the turn.
//
// A read that began after the run was seen or a message handed over, and finds no run, ends it, as
// Stop would: the run was cancelled, failed, or was the one Stop ended. Otherwise its date would
// stand until the next Stop, which may be hours on: the cap (unseenHold) would have lapsed for a
// run that starts then, and the turn would be dated from long before.
func (d *Daemon) noteUnseenLocked() {
	for _, s := range d.sessions {
		r := s.route
		if !s.hooked || !s.busyAt.IsZero() || r.T3Thread == "" {
			continue
		}
		k := threadKey{r.T3Home, r.T3Thread}
		read, st := d.threadRead[k], d.threads[k]
		if !read.After(s.endedAt.Add(closeGrace)) {
			continue
		}
		start := s.unseenStart()
		switch {
		case !runGoing(st) && !start.IsZero() && read.After(start) && read.After(s.handedAt):
			s.endedAt, s.unseenAt = read, time.Time{}
		case runGoing(st) && !s.unseenAt.After(s.endedAt):
			s.unseenAt = read
			if st.LastRun.After(s.endedAt) && st.LastRun.Before(read) {
				s.unseenAt = st.LastRun
			}
		}
	}
}

// noteHooklessLocked keeps, for each session in T3 whose hook hasn't run, when the run its thread
// has going began (runSeenAt), and, once a read finds that run over, holds the messages that came
// during it, to go in as a turn however long the run took, as endTurnLocked does at the end of a
// hooked session's turn (#699). Without that, a message would age past pushMaxAge in a long turn
// and stay in the inbox, where nothing tells the session of it; handed to T3, as before #699, it
// went in at the turn's end whatever its age. The run is dated from when T3 took its prompt
// (LastRun, while it goes), else from the read that first found it.
//
// A receipt (#276) isn't held: the sender of the message it is about asked to be told, not woken,
// and an idle session's receipt waits in the inbox too. The caller kicks the handoffs. Caller holds
// mu.
func (d *Daemon) noteHooklessLocked(wake pageSettings) {
	held := false
	for _, s := range d.sessions {
		r := s.route
		if s.hooked || r.T3Thread == "" {
			continue
		}
		k := threadKey{r.T3Home, r.T3Thread}
		st, ok := d.threads[k]
		if !ok {
			continue
		}
		read := d.threadRead[k]
		switch {
		case runGoing(st) && s.runSeenAt.IsZero():
			s.runSeenAt = read
			if !st.LastRun.IsZero() && st.LastRun.Before(read) {
				s.runSeenAt = st.LastRun
			}
		case !runGoing(st) && !s.runSeenAt.IsZero() && read.After(s.runSeenAt):
			for _, m := range d.st.Inbox[s.id] {
				if _, from := bus.SplitAddr(m.Event.From); from == bus.ReceiptFrom || m.Held {
					continue
				}
				if !waitedSince(m).Before(s.runSeenAt) && d.holdableLocked(s.id, m, wake, s.runSeenAt) {
					m.Held, held = true, true
				}
			}
			s.runSeenAt = time.Time{}
		}
	}
	if held {
		d.saveLocked()
	}
}

// turnStartLocked is when the hook first saw session id's current turn, zero if it isn't at work
// as far as the hook knows. Caller holds mu.
func (d *Daemon) turnStartLocked(id string) time.Time {
	if s := d.sessions[id]; s != nil {
		return s.turnStart
	}
	return time.Time{}
}

// awaitedLocked says a session of T3 that isn't registered is one the last daemon's stop, or its
// being killed, ended (st.Downed), whose server may be about to reconnect (#736, #762): for reconnectHold after this daemon
// started, while its thread hasn't been read or T3 shows a run of it going. Its kept inbox is left alone
// then, as a message handed over would be queued behind the run as a turn of its own (#607), or go
// into a thread not yet known for a delegate's. Once registered it is held as any reconnected
// session is (hookBusyLocked). Caller holds mu.
func (d *Daemon) awaitedLocked(id string) bool {
	if !d.st.Downed[id] || d.routeOfLocked(id).T3Thread == "" || time.Since(d.started) >= reconnectHold {
		return false
	}
	st, read := d.threadStateLocked(id)
	return !read || runGoing(st)
}

// reconnectHold is how long a session that reconnected after a restart, whose hook hasn't run
// here yet, counts as at work before its T3 thread has been read: a read that keeps failing
// mustn't hold its messages for good.
const reconnectHold = time.Minute

// endTurnLocked ends session s's turn (#202): the messages that came during it and that the hook
// never told it of are held, to go in as a turn with those it was told of, however long the turn
// took. Stop ends a turn, and so does a read of T3's database that finds the run over. Caller holds
// mu.
func (d *Daemon) endTurnLocked(s *session, wake pageSettings) {
	held := false
	for _, m := range d.st.Inbox[s.id] {
		if !m.Noticed && !m.Held && !s.turnStart.IsZero() && !waitedSince(m).Before(s.turnStart) && d.holdableLocked(s.id, m, wake, s.turnStart) {
			m.Held, held = true, true
		}
	}
	s.busyAt, s.turnStart, s.endedAt = time.Time{}, time.Time{}, time.Now()
	if held {
		d.saveLocked()
	}
	d.kickPush()
}

// continueTurnLocked takes what keeps session s's turn going, at its end (#202, Tyler,
// 2026-11-08): the messages that would otherwise be handed over as the next turn, those the hook
// told it of and it hasn't read and those that came during the turn untold, for the Stop hook to
// give it. The turn goes on with them, so the session is at work again: a message that comes now
// is held for the turn's next end, not queued behind it in T3. Called after endTurnLocked, so
// held messages count. The caller takes them out of the inbox and saves. Caller holds mu.
//
// None are taken once Claude Code would refuse the continuation (stopBlockCap): it would end the
// turn with them gone. They are then handed over as a turn, as after a turn that ends without Stop.
// None that do not disturb holds (#198) are taken either (heldLocked): a session on it takes only
// its own person's (#777).
//
// The reply carries them whole, so they are taken, oldest first, only while they fit room (see
// wholeRoom); the rest stay in the inbox for the turn's next end. After the first that doesn't fit,
// only a message the reply already carries is taken: one for everyone, given to another session
// the hook speaks for, would otherwise stay here and be given again (Sol's review of #376). A
// session left with messages the reply had no room for goes on with the turn all the same, as it
// does: it is the same agent, and the reply has the others' (the reply carries something, since
// any one message fits an empty room). Otherwise its messages would be handed to T3 while the
// agent is at work, and its count of continuations would fall behind Claude Code's.
func (d *Daemon) continueTurnLocked(s *session, wake pageSettings, room *wholeRoom) []*inMsg {
	if s.continuations >= stopBlockCap || !d.sessionWayInLocked(s.id, wake) {
		return nil
	}
	var out []*inMsg
	waiting, full := false, false
	for _, m := range d.st.Inbox[s.id] {
		if !pushable(m) || d.heldLocked(s.id, m) {
			continue
		}
		waiting = true
		if full && !room.has(m) {
			continue
		}
		if !room.take(m) {
			full = true
			continue
		}
		out = append(out, m)
	}
	if waiting {
		now := time.Now()
		s.busyAt, s.turnStart, s.continuedAt = now, now, now
		s.continuations++
	}
	return out
}

// stopBlockCap is how many times in a row Claude Code lets Stop hooks keep a turn going: it
// overrides the next block and ends the turn, and counts afresh after each tool call (its hooks
// reference, 2026-11-08). CLAUDE_CODE_STOP_HOOK_BLOCK_CAP raises it, and a user's own Stop hooks
// that block count too, which this daemon doesn't see: a continuation they used up loses its
// messages.
const stopBlockCap = 8

// continuationLapsedLocked says session s's last continuation was never seen to go on, and it no
// longer counts as at work for it: no other hook of the session came within ContinueHold. The Stop
// hook's answer can be lost (it gave up waiting just before it came): the agent then stopped, and
// outside T3 nothing else would end the turn, so its messages would wait for the user's next
// prompt. In T3 the database ends such a turn. Caller holds mu.
func (d *Daemon) continuationLapsedLocked(s *session) bool {
	return !s.continuedAt.IsZero() && time.Since(s.continuedAt) >= d.opt.ContinueHold
}

// nextLapseLocked is when the first hold now pending lapses, zero if none is: a continuation's, or
// a handoff's to a T3 thread whose database hasn't been read since (handedHold), or a run's no hook
// has shown going (unseenHold), after which the session's messages may go. Caller holds mu.
func (d *Daemon) nextLapseLocked() time.Time {
	var next time.Time
	soonest := func(at time.Time) {
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	for _, s := range d.sessions {
		if !s.continuedAt.IsZero() {
			soonest(s.continuedAt.Add(d.opt.ContinueHold))
		}
		if at := s.handedAt.Add(handedHold); !s.handedAt.IsZero() && time.Now().Before(at) {
			soonest(at)
		}
		if at := s.unseenAt.Add(unseenHold); s.busyAt.IsZero() && s.unseenAt.After(s.endedAt) && time.Now().Before(at) {
			soonest(at)
		}
	}
	return next
}

// endLapsedTurnsLocked ends the turn of each session whose continuation lapsed outside T3, as Stop
// would have: what came in the meantime is held, and goes in as a turn by the session's socket.
// In T3 the database decides, as a continued turn can run longer than ContinueHold without a tool
// call. A session is in T3 by its route, not by whether its thread has been read: a read that keeps
// failing mustn't make a live turn end. Caller holds mu.
func (d *Daemon) endLapsedTurnsLocked(wake pageSettings) {
	for _, s := range d.sessions {
		if !d.continuationLapsedLocked(s) {
			continue
		}
		s.continuedAt = time.Time{}
		if d.routeOfLocked(s.id).T3Thread == "" {
			d.endTurnLocked(s, wake)
		}
	}
}

// noWay says why a message can't be handed over by a route, without anything of it that is secret.
func noWay(r proto.Route, live bool) string {
	switch {
	case r.T3CLI != "":
		return "inside T3, but its thread isn't known"
	case (r.CodexCLI != "" || r.OpenCodeCLI != "") && r.Native == "":
		return "its client hasn't named its conversation yet"
	case r.CodexCLI != "" || r.OpenCodeCLI != "":
		return "its MCP server has gone"
	case r.MsgSocket == "" || r.MsgToken == "":
		return "not inside T3, and no messaging socket or client command line"
	case !live:
		return "its MCP server has gone"
	}
	return "no route"
}

// sessionNameLocked is the name session id has, or last had if it has stopped. Caller holds mu.
func (d *Daemon) sessionNameLocked(id string) string {
	if s := d.sessions[id]; s != nil {
		return s.name
	}
	return d.st.SessionNames[id]
}

// nextPushLocked takes the next message for session id and marks it as being handed over, or, if
// there is none, ends the session's handoff goroutine. Caller holds mu.
func (d *Daemon) nextPushLocked(id string, wake pageSettings) *pushJob {
	m, via, route := d.candidateLocked(id, wake)
	if m == nil {
		delete(d.pushing, id)
		return nil
	}
	msgs := d.batchLocked(id, m, via, wake)
	views := d.messageViewsLocked(msgs, true)
	key := d.keyOfLocked(id)
	// What the turn's first message from a sender, person or group says needn't be said again in
	// the same turn.
	introduced, explained := map[string]bool{}, map[string]bool{}
	texts := make([]string, len(msgs))
	for i, m := range msgs {
		m.Pushing = true
		if via == viaT3 {
			d.handing[handing{threadKey{route.T3Home, route.T3Thread}, m.Event.ID}] = true
		}
		e := m.Event
		views[i].Introduced = introduced[e.From] || key != "" && d.introduced[key][e.From]
		if bus.IsPerson(e.From) {
			views[i].Explained = explained[e.From] || d.explainedLocked(id, e.From) // recorded once it is handed over
		}
		views[i].GroupKnown = bus.IsGroup(e.To) && (explained[e.To] || d.explainedLocked(id, e.To)) // likewise
		views[i].ByCommand = d.byCommandLocked(id)
		introduced[e.From] = true
		if !bus.IsGroup(e.To) {
			explained[e.From] = true
		}
		explained[e.To] = true
		texts[i] = view.Turn(views[i])
	}
	text := strings.Join(texts, "\n")
	// Remembered before the handoff, not after: the turn can start, and its prompt reach the daemon,
	// while the client's command is still returning (#611). A handoff that fails forgets it. The
	// messages of one turn are read together, by the one prompt that turn is (see awaitingRead.Batch).
	if len(msgs) == 1 {
		d.awaitReadLocked(id, msgs[0].Event, text, "")
	} else {
		turn := text
		for _, m := range msgs {
			if d.awaitReadLocked(id, m.Event, turn, msgs[0].Event.ID) {
				turn = "" // the first that waits holds the turn's text
			}
		}
	}
	return &pushJob{id: id, addr: bus.Addr(d.device, id), key: key, conv: d.conversationLocked(id), explainKey: d.explainKeyLocked(id), msgs: msgs, via: via,
		route: route, text: text}
}

// maxBatch is how much text one turn of several messages may carry (batchLocked): a message past
// it waits for the next turn. A turn of one message carries it however long; a body is cut at
// bus.MaxBody anyway.
const maxBatch = 256 << 10

// batchLocked is what goes into session id as one turn, starting with first, its next message
// (candidateLocked): first alone, unless the session is in T3, with its thread read, and no hook of
// its own seen. Such a
// session is told of nothing while it works, so what came meanwhile waited for the run to end
// (hooklessBusy), and goes in now as one turn, with what else may go: four messages are one turn,
// not four in T3's queue (#699). A hooked session gets the rest by its hook, at its turn's end.
// Every message after first is one candidateLocked would hand over in turn: pushable, and a person's
// question if only those may wake the session. They go while they fit maxBatch, in the order they
// came. Caller holds mu.
func (d *Daemon) batchLocked(id string, first *inMsg, via string, wake pageSettings) []*inMsg {
	out := []*inMsg{first}
	s := d.sessions[id]
	if _, inT3 := d.threadStateLocked(id); via != viaT3 || s == nil || s.hooked || !inT3 {
		// A thread never read held nothing (hooklessBusy), and its messages go one a turn, as
		// before #699.
		return out
	}
	personOnly := !d.mayWakeLocked(id, wake)
	r := d.routeOfLocked(id)
	thread := threadKey{r.T3Home, r.T3Thread} // another session's copy on its way in waits for that (see handing)
	size := len(first.Event.Subject) + len(first.Event.Body)
	for _, m := range d.st.Inbox[id] {
		if m == first || !pushable(m) || d.heldLocked(id, m) || personOnly && !personAsks(m) || d.handing[handing{thread, m.Event.ID}] {
			continue
		}
		if size += len(m.Event.Subject) + len(m.Event.Body); size > maxBatch {
			break
		}
		out = append(out, m)
	}
	return out
}

// handOver passes the message to the session's client, once. It is tried a second time, after PushRetry, only
// if the first attempt never reached the way in (notReached): `t3 thread send` takes no key that
// would make a repeat harmless, and the socket answers nothing, so a call that failed after it
// started may have handed the message over, and a second one would make a second turn of it. A message
// whose handoff failed that way is left to the hook's notice, which the agent still gets, and stays
// so over a daemon restart (sendTurn marks it Attempted, and saves that, before the handoff).
func (d *Daemon) handOver(ctx context.Context, j *pushJob) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			d.log.Printf("session %s: nothing was sent to hand a message over through %s (%v); trying once more in %s",
				j.addr, j.via, err, d.opt.PushRetry)
			select {
			case <-time.After(d.opt.PushRetry):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		select {
		case d.pushSem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		err = d.sendTurn(ctx, j)
		<-d.pushSem
		if err == nil || errors.Is(err, ErrNoConsent) || !notReached(err) {
			return err
		}
	}
	return err
}

// notReached says an attempt failed before it could have put the message anywhere: the command
// didn't start, or the socket couldn't be connected to.
func notReached(err error) bool {
	return errors.Is(err, t3.ErrNotStarted) || errors.Is(err, msgsock.ErrNotConnected) || errors.Is(err, clientcli.ErrNotStarted)
}

// sendTurn makes one attempt, by the route's way in, unless the user has left. No lock is held: it
// starts a process or writes to a pipe.
func (d *Daemon) sendTurn(ctx context.Context, j *pushJob) error {
	if !d.consented() {
		return ErrNoConsent
	}
	if d.hidden.Load() {
		return errNotNow // invisible: the message waits in the inbox until the device is back
	}
	// A job can wait for a slot (pushSem) or for its retry, and in that time the thread can be
	// settled, its session idle past its wake setting, or at work again: asked again before each
	// handoff.
	wake := d.wakeSnapshot()
	d.mu.Lock()
	// A person's question wakes the session past its wake setting, but carries nothing else with it:
	// a turn of several goes only if each of its messages may (batchLocked chose them under the
	// settings then).
	allAsk := !slices.ContainsFunc(j.msgs, func(m *inMsg) bool { return !personAsks(m) })
	held := slices.ContainsFunc(j.msgs, func(m *inMsg) bool { return d.heldLocked(j.id, m) })
	ok := !held && (d.mayWakeLocked(j.id, wake) || allAsk && d.personWakesLocked(j.id, wake)) && !d.hookBusyLocked(j.id)
	d.mu.Unlock()
	if !ok {
		return errNotNow
	}
	if j.via == viaT3 && d.t3RunGoing(ctx, j, wake) {
		return errNotNow
	}
	switch j.via {
	case viaT3:
		return d.attempt(j.msgs, func() error {
			d.mu.Lock()
			if s := d.sessions[j.id]; s != nil {
				s.sending, s.sendingSeen = turnStart(j.text), false
			}
			d.mu.Unlock()
			return t3.Send(ctx, j.route.T3CLI, j.route.T3Home, j.route.T3Thread, j.text)
		})
	case viaSocket:
		return d.attempt(j.msgs, func() error {
			return msgsock.Send(ctx, j.route.MsgSocket, j.route.MsgToken, j.text)
		})
	case viaCodex:
		return d.attempt(j.msgs, func() error {
			return codexQueue(ctx, j.route.CodexCLI, j.route.CodexHome, j.route.Native, j.text)
		})
	case viaOpenCode:
		return d.attempt(j.msgs, func() error {
			return openCodePrompt(ctx, j.route.OpenCodeCLI, j.route.OpenCodeState, j.route.Native, j.text)
		})
	}
	return fmt.Errorf("no way to hand a message over by %s", j.via)
}

// t3RunGoing reads the T3 thread of job j's session just before the handoff, and says the session
// is now at work by it (runUnseen, hooklessBusy): the reads every TitleEvery leave up to that long
// after T3 takes a prompt, and a message handed over then would be queued behind the run (#607,
// #699). The read is kept, as any other. A read that fails says nothing. No lock is held during the
// read.
func (d *Daemon) t3RunGoing(ctx context.Context, j *pushJob, wake pageSettings) bool {
	readAt := time.Now()
	st, err := d.opt.ThreadState(ctx, j.route.T3CLI, j.route.T3Home, j.route.T3Thread)
	if err != nil || !runGoing(st) {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	k := threadKey{j.route.T3Home, j.route.T3Thread}
	if readAt.After(d.threadRead[k]) {
		// Only what says whether a run goes: a change to the rest (settled, model, parent) is left
		// for refreshThreads to see, as it acts on it.
		cur := d.threads[k]
		cur.Busy, cur.Left, cur.LastRun = st.Busy, st.Left, st.LastRun
		d.threads[k], d.threadRead[k] = cur, readAt
	}
	d.noteUnseenLocked()
	d.noteHooklessLocked(wake)
	return d.hookBusyLocked(j.id)
}

// The client's command lines, as variables so that tests needn't have a Codex or an OpenCode.
var (
	codexQueue     = clientcli.CodexQueue
	openCodePrompt = clientcli.OpenCodePrompt
)

// attempt runs send, the handoff of messages msgs, after marking them Attempted and saving the mark:
// the daemon may stop at any point of the call, and the next one has to know the messages may be
// there. If the mark can't be saved nothing is sent, and the messages are left to the hook's notice,
// as after any failed handoff. The mark comes off again if send never reached the way in
// (notReached), as nothing was sent. The save is to the file, which a restart of the process finds;
// a power cut may lose it.
func (d *Daemon) attempt(msgs []*inMsg, send func() error) error {
	if err := d.markAttempted(msgs, true); err != nil {
		return err
	}
	err := send()
	if notReached(err) {
		d.markAttempted(msgs, false)
	}
	return err
}

// markAttempted sets Attempted to v on msgs and saves it. A mark that couldn't be saved is not
// kept, and is an error; a mark that couldn't be taken off is logged, and left in the file, which
// errs the safe way (a notice after a restart).
func (d *Daemon) markAttempted(msgs []*inMsg, v bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range msgs {
		m.Attempted = v
	}
	err := writeJSON(d.statePath(), d.st)
	switch {
	case err == nil:
	case v:
		for _, m := range msgs {
			m.Attempted = false
		}
		return fmt.Errorf("not handing the message over, as the mark that it was handed over can't be saved: %w", err)
	default:
		d.log.Printf("saving state: %v", err)
	}
	return nil
}

// finishPush records how a handoff went. A message handed over leaves the inbox (see
// outOfInboxLocked); one that failed is the hook's again, and the first failure for a session is
// logged.
func (d *Daemon) finishPush(j *pushJob, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	seen := false // a prompt that is this turn came while the send ran (sendingSeen)
	if s := d.sessions[j.id]; s != nil && j.via == viaT3 {
		seen = s.sendingSeen
		s.sending, s.sendingSeen = "", false
	}
	for _, m := range j.msgs {
		m.Pushing = false
		if j.via == viaT3 {
			delete(d.handing, handing{threadKey{j.route.T3Home, j.route.T3Thread}, m.Event.ID})
		}
		if err != nil {
			d.forgetAwaitingLocked(j.id, m.Event.ID) // not handed over, so no turn holds it (#611)
		}
	}
	if errors.Is(err, ErrNoConsent) || errors.Is(err, errNotNow) {
		// The user left, or the session may no longer be woken, on its way: it waits in the inbox,
		// not handed over, and not a failure. Unless the session turned out to be a delegate, whose
		// inbox stays empty (#188).
		d.clearChildInboxLocked(j.id)
		if errors.Is(err, errNotNow) {
			// A session that took this inbox over may still be woken, and its kick came while this
			// message was on its way. The refused session itself gets no new job: the scan asks it
			// the same question.
			d.kickPush()
		}
		return
	}
	d.kickPush() // what waited behind this message, in an inbox that took it over, may go now
	d.health.handedOff(j.via, err)
	if err != nil {
		for _, m := range j.msgs {
			m.PushFailed = true
		}
		if !d.pushWarned[j.id] {
			d.pushWarned[j.id] = true
			d.log.Printf("session %s: handing a message over as a turn through %s failed (%v); its messages show as notices through the hook instead",
				j.addr, j.via, err)
		}
		return
	}
	how := "as a turn, through " + j.via
	if len(j.msgs) > 1 {
		how = fmt.Sprintf("as one turn with %d messages, through %s", len(j.msgs), j.via)
	}
	for _, m := range j.msgs {
		d.outOfInboxLocked(j.id, m, "handed", how)
		if j.via == viaT3 {
			d.noteHandedLocked(j.route, m.Event.ID)
		}
		d.introduceLocked(j.key, m.Event.From)
		if !bus.IsGroup(m.Event.To) { // a group's line stands in for a person's (#484), so they are still to be explained
			d.noteExplainedAtLocked(d.explainKeyOfPushLocked(j), m.Event.From)
		}
		d.noteExplainedAtLocked(d.explainKeyOfPushLocked(j), m.Event.To)
	}
	// A turn the hook hasn't seen start (runUnseen, hooklessBusy), unless it has: T3 can take a while
	// to answer, and the turn can begin, and even end, before the send returns (#677). Holding the
	// session after that held its next message for handedHold, a turn it had already had, wherever
	// T3's database couldn't be read. Only a prompt that is this turn says so (seen): a tool
	// call or a Stop may be of an earlier turn T3's database had ended, and a read of the database
	// that ended a turn may have seen an older run go before T3 queued this one. A client whose hook
	// sends no prompt keeps the hold.
	//
	// The prompt is told by its text, which a turn needn't have alone: an earlier turn of the same
	// text, a person pasting the whole turn, or turns alike in their first MaxPrompt bytes, starting
	// while the send runs with this one queued behind, are taken for it (Sol's review). The next
	// message may then queue behind this one in T3, as before #607. Telling them apart for certain
	// would take an ID in the turn's text, which agents read.
	if s := d.sessions[j.id]; s != nil && j.via == viaT3 && !seen {
		s.handedAt = time.Now()
	}
	d.saveLocked()
	delete(d.pushWarned, j.id)
}

// maxWholeReply is how much of one reply to the hook its messages may take, whole or as notices. A
// client reads at most 64 MB of a reply (#371), and the hook's speaks for every session of its
// agent's process, each with an inbox of up to maxInbox messages, which the daemon takes out of the
// inbox, or marks told, before it replies: a reply the client then refused would lose them. Half
// the client's limit leaves room for the rest of the reply. A variable for the test.
var maxWholeReply = 32 << 20

// wholeRoom is what is left of one reply's maxWholeReply. A message for everyone is in each inbox
// the hook speaks for and is in the reply once, so it is counted once.
type wholeRoom struct {
	left  int
	taken map[string]bool
}

func newWholeRoom() *wholeRoom { return &wholeRoom{left: maxWholeReply, taken: map[string]bool{}} }

// has says the reply already carries message m whole.
func (r *wholeRoom) has(m *inMsg) bool { return r.taken["whole "+m.Event.ID] }

// take reports whether message m fits in what is left whole, and if so counts it, at its longest:
// every character escaped six times over (`<` is `<`), and a kilobyte for the rest of its
// view. One the reply already carries whole costs nothing more.
func (r *wholeRoom) take(m *inMsg) bool {
	return r.fit("whole "+m.Event.ID, 6*(len(m.Event.Body)+len(m.Event.Subject))+1<<10)
}

// takeNotice is take for a notice of message m, which has its subject and not its body.
func (r *wholeRoom) takeNotice(m *inMsg) bool {
	return r.fit("notice "+m.Event.ID, 6*len(m.Event.Subject)+1<<10)
}

func (r *wholeRoom) fit(key string, cost int) bool {
	if r.taken[key] {
		return true
	}
	if cost > r.left {
		return false
	}
	r.left -= cost
	r.taken[key] = true
	return true
}

// delivery is a message, msg, given to session id.
type delivery struct {
	id  string
	msg *inMsg
}

// keyOfLocked is the conversation session id is in now (see Daemon.introduced), "" if it isn't
// registered. Caller holds mu.
func (d *Daemon) keyOfLocked(id string) string {
	if s := d.sessions[id]; s != nil {
		return s.key
	}
	return ""
}

// introduceLocked records that conversation key has had a turn from sender from. Caller holds mu.
func (d *Daemon) introduceLocked(key, from string) {
	if key == "" {
		return
	}
	if d.introduced[key] == nil {
		d.introduced[key] = map[string]bool{}
	}
	d.introduced[key][from] = true
}

// wholeViewsLocked renders msgs, given whole to the sessions in given, and records the sessions as
// introduced to their senders, and as told of the people among them (see explained.go). A message
// is rendered as from a sender already introduced only if every session it was given to had been:
// the hook speaks for several sessions at once, and shows a message for everyone once. The caller
// saves the state. Caller holds mu.
func (d *Daemon) wholeViewsLocked(msgs []*inMsg, given []delivery) []proto.MessageView {
	views := d.messageViewsLocked(msgs, true)
	for i := range views {
		views[i].Introduced = true
		for _, g := range given {
			if key := d.keyOfLocked(g.id); g.msg.Event.ID == views[i].ID && (key == "" || !d.introduced[key][views[i].From]) {
				views[i].Introduced = false
			}
		}
	}
	for _, g := range given {
		d.introduceLocked(d.keyOfLocked(g.id), g.msg.Event.From)
	}
	d.explainViewsLocked(views, func(i int) []string {
		var ids []string
		for _, g := range given {
			if g.msg.Event.ID == views[i].ID {
				ids = append(ids, g.id)
			}
		}
		return ids
	})
	return views
}

// outOfInboxLocked records that message m has gone to session id: it leaves the inbox, wherever it
// is by now (a session that takes a name takes the inboxes kept for it), and the page's notes and
// the log say so. verb is "handed" for a handoff to the session's client, where the agent reads it
// when the turn carrying it starts, and "gave" for the hook's whole message, which is read: it is
// in the agent's context (#325); how ends the line. The caller saves the state. Caller holds mu.
func (d *Daemon) outOfInboxLocked(id string, m *inMsg, verb, how string) {
	for inbox, box := range d.st.Inbox {
		for i, o := range box {
			if o == m {
				if box = append(box[:i:i], box[i+1:]...); len(box) == 0 {
					delete(d.st.Inbox, inbox)
				} else {
					d.st.Inbox[inbox] = box
				}
				break
			}
		}
	}
	d.pushedAt[id] = time.Now()
	d.health.outOfInbox(verb)
	switch verb {
	case "handed":
		d.markLocked(id, m.Event, bus.MarkHanded) // Murmur can't see the turn start (#325), but the hook may (#611)
	case "gave":
		d.markLocked(id, m.Event, bus.MarkRead)
	}
	d.noteConversationLocked() // the client has the message for the agent; its answer may soon follow
	e, addr := m.Event, bus.Addr(d.device, id)
	d.noteLocked(time.Now(), fmt.Sprintf("%s %q from %s to %s %s", verb, e.Subject, e.From, addr, how))
	if verb == "handed" {
		how += "; it is read when that turn starts"
	}
	d.log.Printf("%s message %s to session %s %s", verb, e.ID, addr, how)
}

// rememberRouteLocked keeps where a session in T3 is, so a message for it still reaches the
// thread after its MCP server stops. Caller holds mu.
func (d *Daemon) rememberRouteLocked(s *session) {
	r := s.route
	if r.T3CLI == "" || r.T3Thread == "" {
		// A session outside T3 reusing a Claude-derived ID a T3 thread once had no longer routes
		// to that thread (#873). A T3 session that hasn't found its thread yet keeps the stored
		// route until it finds it or another session takes the thread.
		if r.T3CLI == "" && r.T3Thread == "" && !strings.HasSuffix(s.agent, " in T3") {
			if _, ok := d.st.T3Routes[s.id]; ok {
				delete(d.st.T3Routes, s.id)
				d.saveLocked()
			}
		}
		return
	}
	want := t3Route{CLI: r.T3CLI, Home: r.T3Home, Thread: r.T3Thread, Command: s.byCommand}
	d.retireRoutesLocked(s, want)
	d.dropHandedLocked(s.id)
	if d.st.T3Routes[s.id] != want {
		d.st.T3Routes[s.id] = want
		d.saveLocked()
	}
}

// retireRoutesLocked takes away the way into thread want from every other session that has it
// and isn't running, and clears any stale stored route for want whose session ID is now held by a
// live session of another route (such as the command line reusing a Claude-derived ID, #873)
// without touching that session's live route or inbox. A thread's MCP server is a new session,
// with an ID of its own, each time T3 restarts it (after 30 idle minutes) or a connection is made
// again, and the one it replaces, in its grace or stopped, still holds the thread. A message for
// everyone, or for the device, goes into both inboxes, and would be handed to the one thread
// twice, or, from the new session, back to itself. What the old session had unread goes to the
// new one, which has the thread: with its route gone it could no longer be handed over, and a
// session in its grace is not one whose inbox the name's next holder adopts (see adoptLocked).
// A session a shell command holds (#913) is let go the same way when one with a server of its own
// finds the thread: the thread is that one's now, and its own tools stand over the commands.
// Caller holds mu.
func (d *Daemon) retireRoutesLocked(s *session, want t3Route) {
	for id, r := range d.st.T3Routes {
		if id == s.id || r.Thread != want.Thread || r.Home != want.Home {
			continue
		}
		if o := d.sessions[id]; o == nil || !o.dropped.IsZero() || o.hold != nil && s.hold == nil {
			delete(d.st.T3Routes, id)
			if o != nil {
				d.releaseHoldLocked(o, false)
				o.route = proto.Route{}
			}
			d.takeInboxLocked(s, id)
			d.saveLocked()
		} else if o.route.T3Thread != want.Thread || o.route.T3Home != want.Home {
			delete(d.st.T3Routes, id)
			d.saveLocked()
		}
	}
}

// takeInboxLocked moves the messages session from has unread into s's inbox, in the order they
// arrived. A message s has a copy of (one for everyone is in both inboxes) stays once, as s's copy,
// unless from's is on its way in as a turn: then that one stays, so it is handed over once and is
// removed from wherever it is when it has been. A message s never needs from the other session
// stays behind (see hadCopyLocked). Caller holds mu.
func (d *Daemon) takeInboxLocked(s *session, from string) {
	d.takeFilesWaitLocked(s.id, from)
	d.takeAwaitingLocked(s.id, from) // what was handed over as a turn, and may start under s
	old := d.st.Inbox[from]
	if len(old) == 0 {
		return
	}
	box := d.st.Inbox[s.id]
	at := map[string]int{}
	for i, m := range box {
		at[m.Event.ID] = i
	}
	for _, m := range old {
		if i, ok := at[m.Event.ID]; ok {
			if m.Pushing {
				box[i] = m
			}
			continue
		}
		if !m.Pushing && d.hadCopyLocked(s, m) {
			continue
		}
		at[m.Event.ID] = len(box)
		box = append(box, m)
	}
	sort.SliceStable(box, func(i, j int) bool { return box[i].Arrived.Before(box[j].Arrived) })
	if len(box) > maxInbox {
		box = box[len(box)-maxInbox:]
	}
	delete(d.st.Inbox, from)
	if len(box) == 0 {
		delete(d.st.Inbox, s.id)
	} else {
		d.st.Inbox[s.id] = box
	}
	d.log.Printf("session %s, taking over the thread of %s, took what it had unread", bus.Addr(d.device, s.id), bus.Addr(d.device, from))
	d.kickPush()
}

// hadCopyLocked says message m, in the inbox of a session that s replaces, is not for s to take:
// s sent it itself (a message for everyone goes to every session but its sender, and until s found
// its thread the other session was another one), or it came while s was running and was for s too,
// so s was given a copy of its own, which s may have read since: its copy would not be in its inbox
// to be told from this one. Caller holds mu.
func (d *Daemon) hadCopyLocked(s *session, m *inMsg) bool {
	if m.Event.From == bus.Addr(d.device, s.id) {
		return true
	}
	return !m.Arrived.Before(s.started) && d.addressedLocked(m.Event, s.id)
}

// A thread is one recipient of a message, whichever session's copy reaches it (#70). A thread's new
// session registers before it has found its thread (#56), and a message for everyone that comes in
// that second is in both sessions' inboxes: the old session, in its grace, still has the thread and
// hands its copy over at once, and the new one's copy waits until the thread is found and the route
// moves to it. takeInboxLocked can't tell that copy from a message the thread never had, as the old
// copy has left the inbox by then. So the daemon remembers, by event ID, what it handed to each
// thread, and takes another copy out of the inbox of any session of that thread: the thread has the
// message, and the session is its thread. Left in, the copy would be handed over as a second turn,
// or told again by the hook. Taking it out is what a handoff does with the copy it hands over, so
// nothing tries it again or shows it unread. A message only the new session was sent is another
// event, and goes in, and so does each of two messages that are both for the thread.
//
// Two sessions of a thread can also both hold its route when the copies come (#99): a new session
// that registers before the daemon has seen the old one's connection close finds the old one still
// running, so retireRoutesLocked leaves it its route, and it keeps it through its grace. Both copies
// can then be taken for a handoff at once, before either has been handed over. So the daemon also
// knows what is on its way into each thread (handing), and a session whose next message is on its
// way in through another session's copy waits: once that copy has been handed over, its own is
// taken out as above, and if that handoff failed, its own goes in, as it would have. That is a second
// try after a failure that may have reached T3, which handOver doesn't make, but the failed copy may
// be the old session's, back in an inbox no hook reads now its connection is gone. The route isn't
// taken from the old session when its drop is seen: tried for #99 (2026-10-30), that could leave a
// message to the old session's name or address with no way in.
//
// What is remembered is in memory only, as introduced is: a daemon that restarts forgets it, and a
// repeat in that window is what there was before.

// handed is a message handed to a thread as a turn: its event ID, and when.
type handed struct {
	id string
	at time.Time
}

// handing is a message on its way into a T3 thread as a turn: the thread, and the event ID.
type handing struct {
	thread threadKey
	id     string
}

const (
	// handedFor is how long a message handed to a thread is remembered. The copy that matters waits
	// only until its session finds its thread: a second or so for Claude Code in T3, the first tool
	// call for Codex. A copy older than pushMaxAge is no longer handed over as a turn, unless it was
	// held for the end of one; twice that is past the window, and bounds what is kept in time.
	handedFor = 2 * pushMaxAge
	// maxHanded is how many are remembered for one thread, the most one inbox holds, so a thread
	// handed a burst of messages keeps the newest.
	maxHanded = maxInbox
)

// noteHandedLocked records that the message with event ID id was handed to the thread of route r,
// forgets what is older than handedFor, and takes any other copy of the message out of the inbox of
// each session of that thread. Caller holds mu.
func (d *Daemon) noteHandedLocked(r proto.Route, id string) {
	if r.T3Thread == "" {
		return
	}
	now := time.Now()
	for k, list := range d.handedTo {
		for len(list) > 0 && now.Sub(list[0].at) > handedFor {
			list = list[1:]
		}
		if len(list) == 0 {
			delete(d.handedTo, k)
		} else {
			d.handedTo[k] = list
		}
	}
	k := threadKey{r.T3Home, r.T3Thread}
	list := append(d.handedTo[k], handed{id: id, at: now})
	if len(list) > maxHanded {
		list = list[len(list)-maxHanded:]
	}
	d.handedTo[k] = list
	for _, sid := range sortedKeys(d.st.Inbox) {
		d.dropHandedLocked(sid)
	}
}

// dropHandedLocked takes out of session id's inbox each message its T3 thread was already handed,
// through another session's copy. A copy on its way in stays: it is the one being handed over (see
// takeInboxLocked), and leaves the inbox when it has been; a second copy for the same thread waits
// for it (see handing). Caller holds mu.
func (d *Daemon) dropHandedLocked(id string) {
	r := d.routeOfLocked(id)
	list := d.handedTo[threadKey{r.T3Home, r.T3Thread}]
	if r.T3Thread == "" || len(list) == 0 {
		return
	}
	got := map[string]bool{}
	for _, h := range list {
		if time.Since(h.at) <= handedFor {
			got[h.id] = true
		}
	}
	box := d.st.Inbox[id]
	var keep []*inMsg
	for _, m := range box {
		if m.Pushing || !got[m.Event.ID] {
			keep = append(keep, m)
			continue
		}
		d.log.Printf("session %s: message %s is in its T3 thread already, handed over as another session's copy; not handing it over again",
			bus.Addr(d.device, id), m.Event.ID)
	}
	if len(keep) == len(box) {
		return
	}
	if len(keep) == 0 {
		delete(d.st.Inbox, id)
	} else {
		d.st.Inbox[id] = keep
	}
	d.saveLocked()
}

// setRouteLocked takes what a session's MCP server has learned about reaching its client since it
// registered: the T3 thread, once it finds it, or its client's own thread ID (Codex's). The session's name follows it from then on
// (see rekeyLocked). Caller holds mu.
func (d *Daemon) setRouteLocked(s *session, r proto.Route) {
	if titleSource(r) != titleSource(s.route) && s.title != "" {
		// Another conversation: the title of the last one must not stay if this one's can't be read.
		s.title = ""
		d.kickPresence()
	}
	if live := s.dropped.IsZero(); viaOf(r, live) != viaOf(s.route, live) {
		// Another way in, or a first one: what the log said of the last no longer holds, and a
		// failure of this one is worth its own line.
		delete(d.pushWarned, s.id)
		d.kickPresence()
	}
	s.route = r
	d.rememberRouteLocked(s)
	if d.routeChildLocked(s) {
		d.kickPresence()
	}
	base := nameKey(r.T3Thread, s.claudeSession, r.Native, s.id)
	left := d.yieldThreadLocked(s.id, s.agent, s.clientPID, base, s.claudeSession)
	d.shareNameLocked(base, claudeAlias(s.agent, r.T3Thread, s.claudeSession), s.id)
	if key := d.uniqueKeyLocked(base, s.id, s.clientPID); key != s.key {
		d.rekeyLocked(s, key)
	}
	for _, id := range left {
		d.takeInboxLocked(s, id) // sent to the conversation, which is here now
	}
	if len(left) > 0 {
		d.saveLocked()
	}
	d.kickPush()
}

// rekeyLocked moves s to another name key: its client's session ID, its Codex thread or its T3
// thread became known.
// A key that has never had a name takes the one s has, so the name doesn't change under whoever
// has just learned it; a key that has one gives s that, which is the point of keying on the
// thread: the name follows the thread across /clear, restarts and account switches. Nothing goes
// on the bus. Caller holds mu.
func (d *Daemon) rekeyLocked(s *session, key string) {
	from, oldKey := s.name, s.key
	s.key = key
	if d.st.NameKeys[key] == "" && s.name != "" {
		d.st.NameKeys[key] = s.name
		d.saveLocked()
	} else if d.st.NameKeys[key] == "" && s.name == "" && d.assigning {
		d.pendingRekeys = append(d.pendingRekeys, pendingRekey{from: oldKey, to: key})
	}
	d.setNameLocked(s, d.nameForKeyLocked(s, key))
	if s.name == "" {
		d.kickNames()
	}
	d.kickPresence()
	addr := bus.Addr(d.device, s.id)
	if s.name != from {
		d.noteLocked(time.Now(), fmt.Sprintf("%s found its thread: %s -> %s", addr, from, s.name))
		d.log.Printf("session %s found its thread; its name is %s now, not %s", addr, s.name, from)
	} else {
		d.log.Printf("session %s found its thread", addr)
	}
}
