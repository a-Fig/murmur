package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
)

// maxPollBackoff is the longest a failing poll waits between tries, however short Poll is.
const maxPollBackoff = time.Minute

// nextPoll is how long to wait after a poll that failed or worked, when the wait was cur and Poll
// is base: base again once it works, and while it fails twice as long each time, to at most 20
// times base or maxPollBackoff. A remote that is down or limiting requests isn't asked every few
// seconds by every device.
func nextPoll(cur, base time.Duration, failed bool) time.Duration {
	if !failed {
		return base
	}
	return min(cur*2, 20*base, max(maxPollBackoff, base))
}

// pollRate is how the remote is looked at when all is well. every is the wait between looks; calm
// is the wait to carry into the next call, so the slow phase can double from it; quiet says no
// session is live and no page open.
type pollRate struct {
	every, calm time.Duration
	quiet       bool
}

// pollEvery says how often to look at the remote when all is well, given calm, the last wait of
// the slow phase (#93; Tyler: agents don't need real-time, a few seconds a hop is fine). It is
// read again after every poll, and a conversation that moves wakes the loop (kickPoll).
//
//   - No session live and no page open (a daemon up only to wake a T3 thread, #87): QuietPoll,
//     never faster than Poll, whatever was said lately; a session that starts makes it look at once.
//   - A conversation moved less than FastFor ago: Poll. A reply is likely, and two agents talking
//     are a hop apart each time.
//   - Otherwise a session sits open and nothing happens: double the wait each poll, 3 s, 6, 12, up
//     to PollCap. Doubling gets to the cap in three polls, so a first message waits at most PollCap
//     plus the delivery, and hours of an open session cost 240 listings an hour, not 1,200.
func (d *Daemon) pollEvery(calm time.Duration) pollRate {
	d.mu.Lock()
	defer d.mu.Unlock()
	poll := d.opt.Poll
	now := time.Now()
	switch {
	case len(d.sessions) == 0 && !d.pageOpenLocked(now):
		return pollRate{every: max(poll, d.opt.QuietPoll), calm: poll, quiet: true}
	case !d.convAt.IsZero() && now.Sub(d.convAt) < d.opt.FastFor:
		return pollRate{every: poll, calm: poll}
	}
	every := min(max(2*calm, poll), max(d.opt.PollCap, poll))
	return pollRate{every: every, calm: every}
}

// noteConversationLocked records that a conversation moved: the poll goes to Poll for FastFor,
// starting now, and a loop that was waiting longer looks at once. Presence heartbeats and status
// changes don't come here: they arrive all the time from active teammates and predict no reply.
// Caller holds mu.
func (d *Daemon) noteConversationLocked() {
	d.convAt = time.Now()
	d.kickPoll()
}

// kickPoll wakes pollLoop, which looks at the remote now if that is sooner than it would have.
// Any lock may be held.
func (d *Daemon) kickPoll() {
	select {
	case d.pollWake <- struct{}{}:
	default:
	}
}

func (d *Daemon) pollLoop(ctx context.Context) {
	t := time.NewTicker(d.opt.Poll)
	defer t.Stop()
	failing, quiet := false, false
	// wait is until the next poll; every, when all is well; calm is where the slow phase is.
	wait, every, calm := d.opt.Poll, d.opt.Poll, d.opt.Poll
	for {
		err := d.pollOnce(ctx)
		if err != nil && ctx.Err() == nil {
			// A lock left by a killed git fails every fetch of its ref, and git doesn't always
			// say so in words ("reference already exists"); clearing stale ones is cheap.
			clearStaleLocks(d.opt.GitDir, d.log)
		}
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			d.log.Printf("sync failed, retrying every %s, less often while it keeps failing: %v", d.opt.Poll, err)
		case err == nil && failing:
			d.log.Printf("sync working again")
			// A session that couldn't take its name meanwhile would otherwise wait out a retry
			// delay that grew while the remote was out of reach, up to maxNameBackoff.
			d.kickNames()
		}
		failing = err != nil
		rate := d.pollEvery(calm)
		if rate.quiet != quiet {
			if rate.quiet {
				d.log.Printf("no session is live and no page open; looking at the remote every %s, not every %s", rate.every, d.opt.Poll)
			} else {
				d.log.Printf("a session is live or a page open; looking at the remote every %s, less often while nothing is said", d.opt.Poll)
			}
			quiet = rate.quiet
		}
		every, calm = rate.every, rate.calm
		if next := nextPoll(wait, every, failing); next != wait {
			wait = next
			t.Reset(wait)
		}
	sleep:
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-d.pollWake:
				// A session, a page or a conversation's move came. If the loop was running at a
				// slower rate than that calls for, look now and from here on at the faster; if
				// not, it is as quick as it gets. A failure's backoff is not ended by it either,
				// unless the loop was quiet: a session that comes then is the reason to look now.
				if d.pollEvery(calm).every >= every || failing && !quiet {
					continue sleep
				}
			}
			break
		}
	}
}

// pollOnce lists the remote's bus refs and fetches and processes the ones that changed. The
// listing is one cheap round trip; objects move only when something changed.
func (d *Daemon) pollOnce(ctx context.Context) error {
	d.polls.Add(1)
	// A listing applied after a newer one would drop presence that has since appeared.
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	listed, err := d.repo.ListRemote(ctx)
	if err != nil {
		return err
	}
	remote := readable(listed)
	d.advMu.Lock()
	defer d.advMu.Unlock()

	var changed []string
	d.mu.Lock()
	if d.backlog == nil {
		// What the first listing shows is the backlog. A device that appears later is new, and
		// its log is read normally from the start.
		d.backlog, d.backlogAt = map[string]bool{}, time.Now()
		for ref := range remote {
			if _, ok := meshDevice(ref); ok && d.mode != modeNormal {
				d.backlog[ref] = true
			}
		}
	}
	for ref := range d.backlog {
		switch {
		case remote[ref] == "":
			delete(d.backlog, ref) // gone from the remote; nothing left to read
		case time.Since(d.backlogAt) > catchUpLimit:
			// Still unread after this long, it is history nobody is waiting for.
			d.log.Printf("giving up on reading the backlog of %s; skipping it", ref)
			d.st.Processed[ref] = remote[ref]
			delete(d.st.Junked, ref)
			delete(d.backlog, ref)
		}
	}
	for ref, sha := range remote {
		if d.known[ref] != sha {
			changed = append(changed, ref)
		}
	}
	d.mu.Unlock()
	// The listing worked, so a ref whose fetch fails is that ref's trouble, or a passing one;
	// the rest of the sync goes on without it, and it is fetched again next time.
	failed := d.fetch(ctx, changed, remote)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(changed) > len(failed) {
		// A ref can move between the listing and the fetch. A presence ref is force-pushed, so
		// the commit listed may never have been fetched; going by it would leave that peer
		// unread and, soon, judged stale. Go by what the fetch brought.
		local, err := d.repo.LocalRefs(ctx)
		if err != nil {
			return err
		}
		for _, ref := range changed {
			if sha, ok := local[ref]; ok && failed[ref] == nil {
				remote[ref] = sha
			}
		}
	}
	// Monotonic, like every observation staleness is judged by, so a jump of the wall clock
	// makes nobody look gone; the wall clock only goes into state.json.
	polled := time.Now()
	wall := polled.Round(0)
	d.mu.Lock()
	for _, ref := range changed {
		if failed[ref] == nil {
			d.known[ref] = remote[ref]
		}
	}
	d.mu.Unlock()
	d.clearOwnJunk(listed)

	for _, ref := range sortedKeys(remote) {
		sha := remote[ref]
		if failed[ref] != nil {
			// Not here to read, but the listing shows it moved: for a presence, that is still a
			// sign of life. Anything else waits for a fetch that works.
			if strings.HasPrefix(ref, brand.PresencePrefix) {
				d.sawPresence(strings.TrimPrefix(ref, brand.PresencePrefix), sha, false)
			}
			continue
		}
		switch {
		case ref == brand.NamesRef:
			d.processNames(ctx, sha)
		case ref == brand.ConfigRef:
			d.readConfig(ctx, sha)
		case strings.HasPrefix(ref, brand.MeshPrefix):
			if _, ok := meshDevice(ref); ok {
				d.advanceMesh(ctx, ref, sha)
			}
		case strings.HasPrefix(ref, brand.PresencePrefix):
			d.updatePeer(ctx, ref, sha)
		}
	}
	if _, ok := remote[brand.NamesRef]; !ok {
		d.processNames(ctx, "") // not on the remote
	}
	// A sync cut short is no fresh look: staleness is judged up to the last successful sync, and
	// this one didn't read every presence it listed.
	if err := ctx.Err(); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for dev := range d.peers {
		if _, ok := remote[brand.PresencePrefix+dev]; !ok {
			delete(d.peers, dev) // its daemon said goodbye
		}
	}
	d.lastPollOK = polled
	switched := false
	if d.mode != modeNormal && len(d.backlog) == 0 {
		d.mode = modeNormal
		d.st.Initialized = true
		switched = true
	}
	if switched || d.mode == modeNormal && wall.Sub(d.st.LastSync) > time.Minute {
		d.st.LastSync = wall
		d.saveLocked()
	}
	return nil
}

// meshDevice names the device a mesh ref belongs to, and whether Murmur reads it at all: not one
// under a name no device can have, such as Murmur's own, which could only impersonate it.
func meshDevice(ref string) (string, bool) {
	dev := strings.TrimPrefix(ref, brand.MeshPrefix)
	return dev, bus.ValidDevice(dev)
}

// readable keeps the refs Murmur reads: each device's log and presence, and the names registry.
// Anything else under the bus's
// root stays on the remote, unfetched: a name this machine can't store (one Windows forbids, or
// one clashing with a local ref, as presence/<device>/x does with a device's old presence) would
// otherwise fail every fetch, and with it every sync.
func readable(listed map[string]string) map[string]string {
	refs := map[string]string{}
	for ref, sha := range listed {
		_, mesh := meshDevice(ref)
		presence := strings.HasPrefix(ref, brand.PresencePrefix) &&
			bus.ValidDevice(strings.TrimPrefix(ref, brand.PresencePrefix))
		if mesh || presence || ref == brand.NamesRef || ref == brand.ConfigRef {
			refs[ref] = sha
		}
	}
	return refs
}

// fetch fetches refs in one go, or, if that fails, one by one, so that a ref git can't store here
// costs that ref and not the rest. It returns the refs that failed and why, logging each once per
// listed SHA. Caller holds advMu.
func (d *Daemon) fetch(ctx context.Context, refs []string, listed map[string]string) map[string]error {
	failed := map[string]error{}
	// A ref that failed before, at the same SHA, is fetched on its own, so it can't fail the
	// rest, and at most once a minute, so it doesn't cost every poll.
	var batch, alone []string
	d.mu.Lock()
	for _, ref := range refs {
		f, ok := d.unfetched[ref]
		switch {
		case !ok || f.sha != listed[ref]:
			batch = append(batch, ref)
		case time.Since(f.at) < time.Minute:
			failed[ref] = errors.New("failed less than a minute ago")
		default:
			alone = append(alone, ref)
		}
	}
	d.mu.Unlock()
	if len(batch) > 0 && d.repo.Fetch(ctx, batch) != nil {
		alone = append(alone, batch...)
	}
	for _, ref := range alone {
		if err := d.repo.Fetch(ctx, []string{ref}); err != nil {
			failed[ref] = err
		}
	}
	if ctx.Err() != nil {
		return failed // cut short, which says nothing about the refs
	}
	retry := time.Now()
	removed := len(failed) > 0 && clearStaleLocks(d.opt.GitDir, d.log)
	d.mu.Lock()
	defer d.mu.Unlock()
	if removed {
		// A lock left by a killed git fails its ref's fetch until removed; with one gone, try
		// every ref that failed again at the next poll rather than in a minute. The lock is
		// usually found stale a poll or more after the fetch it failed.
		retry = time.Time{}
		for ref, f := range d.unfetched {
			f.at = retry
			d.unfetched[ref] = f
		}
	}
	for _, ref := range alone {
		if err := failed[ref]; err == nil {
			delete(d.unfetched, ref)
		} else {
			if d.unfetched[ref].sha != listed[ref] {
				d.log.Printf("fetching %s: %v", ref, err)
			}
			d.unfetched[ref] = fetchFail{sha: listed[ref], at: retry}
		}
	}
	for _, ref := range batch {
		if failed[ref] == nil {
			delete(d.unfetched, ref)
		}
	}
	return failed
}

// staleLocked says whether a peer's presence went unchanged past the stale limit, as of this
// daemon's last successful sync. A daemon that hasn't heard from the remote for a while (asleep,
// offline) has no grounds to call anyone else gone. Caller holds mu.
func (d *Daemon) staleLocked(p *peer) bool {
	return !d.lastPollOK.IsZero() && d.lastPollOK.Sub(p.lastChange) > d.opt.StaleAfter
}

// firstSight is when to treat a presence as last changed when this daemon has no observation of
// its own: when its tip commit was written, by the writer's clock. Every device's ref is new to a
// daemon that has just started, so dating them all from now would show a device that went quiet
// hours ago as alive for the first StaleAfter, exactly when a session that has just started is
// most likely to ask who is on (#66, seen live 2026-10-30). A clock that runs ahead can't make a
// device look newer than now. One that runs behind makes a live device look stale until its next
// heartbeat, at most a heartbeat later, when the daemon sees the presence change and times it
// itself.
func firstSight(written time.Time) time.Time {
	now := time.Now()
	return now.Add(-max(now.Sub(written), 0)) // from now, to keep a monotonic reading
}

// advanceMesh processes a device's log up to sha and delivers its messages, as the sync mode
// says. Caller holds advMu.
func (d *Daemon) advanceMesh(ctx context.Context, ref, sha string) {
	d.mu.Lock()
	prev := d.st.Processed[ref]
	mode := modeNormal
	if d.backlog[ref] {
		mode = d.mode
	}
	hidden, junked := d.st.Junked[ref]
	d.mu.Unlock()
	settled := func() {
		d.mu.Lock()
		delete(d.backlog, ref)
		d.mu.Unlock()
	}
	if prev == sha {
		settled()
		return
	}
	if ok, err := d.repo.IsCommit(ctx, sha); err != nil {
		d.log.Printf("reading %s: %v", ref, err)
		return
	} else if !ok {
		// Junk pushed over a log. Its owner builds over it with its next event, which then
		// reads as the next commit after the last one read here. On a log this device has
		// never read, the junk hides history from before its first look, which mustn't read as
		// new then: what was sent before that look (by the sender's clock, with a minute's
		// allowance) is passed over, the rest read as usual. Saved, as a restart doesn't
		// change what the history is.
		d.mu.Lock()
		if _, ok := d.st.Junked[ref]; !ok && prev == "" && d.backlog[ref] {
			d.st.Junked[ref] = d.backlogAt.Round(0)
			d.saveLocked()
		}
		delete(d.backlog, ref)
		d.mu.Unlock()
		return
	}
	skipTo := func() {
		d.mu.Lock()
		d.st.Processed[ref] = sha
		delete(d.backlog, ref)
		delete(d.st.Junked, ref)
		d.saveLocked()
		d.mu.Unlock()
	}
	if mode == modeSkip {
		// Nothing is delivered from the backlog, unless the first sync failed and sessions have
		// registered since (what was sent after they started is for them), or an inbox is kept for
		// a stopped session (what was sent after it stopped is for it, #30).
		d.mu.Lock()
		waiting := len(d.sessions) > 0 || d.keptInboxLocked()
		d.mu.Unlock()
		if !waiting {
			skipTo()
			return
		}
	}
	if prev != "" {
		ok, err := d.repo.IsAncestor(ctx, prev, sha)
		if err != nil {
			d.log.Printf("reading %s: %v", ref, err)
			return
		}
		if !ok {
			back, err := d.repo.IsAncestor(ctx, sha, prev)
			if err != nil {
				d.log.Printf("reading %s: %v", ref, err)
				return
			}
			if back {
				settled() // an older copy of a log already read past
				return
			}
			d.log.Printf("%s was rewritten; skipping its history rather than replaying it", ref)
			skipTo()
			return
		}
	}
	commits, err := d.repo.Log(ctx, prev, sha)
	if err != nil {
		d.log.Printf("reading %s: %v", ref, err)
		return
	}
	dev := strings.TrimPrefix(ref, brand.MeshPrefix)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range commits {
		var e bus.Event
		if bus.Decode(c.Message, &e) != nil {
			continue
		}
		if e.Clean(); !e.Valid() {
			continue
		}
		if from, _ := bus.SplitAddr(e.From); from != dev {
			continue // a device's log carries only its own sessions' events
		}
		if !d.sealOKLocked(dev, ref, c) {
			continue
		}
		if e.Type == bus.Encrypted {
			in, err := bus.DecryptEvent(e, d.device, d.boxKey)
			if err != nil {
				if !errors.Is(err, bus.ErrNotForUs) {
					d.log.Printf("%s: %s doesn't decrypt: %v", ref, short(c.SHA), err)
				}
				continue
			}
			if in.Clean(); !in.Valid() {
				continue
			}
			e = in
		}
		if junked && e.At.Before(hidden.Add(-time.Minute)) {
			continue // history the junk hid when this device first looked
		}
		if mode != modeSkip {
			d.noteLocked(e.At, e.Line())
		}
		if e.Type == bus.Message {
			// A skip passes over history, but a ref read in one only after sessions registered
			// (the first sync failed) also holds what was sent since this daemon started, which
			// is new to it: by the sender's clock, with the minute deliverLocked allows. What a
			// skip gave a kept inbox (#30) was read too, so the page lists it.
			kept := d.deliverLocked(e, mode)
			if mode != modeSkip || kept || !e.At.Before(d.started.Add(-time.Minute)) {
				d.recordMessageLocked(e)
			}
		}
	}
	d.st.Processed[ref] = sha
	delete(d.backlog, ref)
	delete(d.st.Junked, ref)
	d.saveLocked()
}

// deliverLocked puts a message in the inbox of each session here it is addressed to. A session
// gets only what arrives while it runs, or, if it stops, within StoppedInbox of that, in case it
// comes back (see recipientsLocked). What arrived while the daemon was down (modeCatchUp)
// goes only to sessions that were running before and reconnect, and in modeSkip to nobody, except
// the sessions whose inbox is kept, for what was sent since they stopped (see inTimeLocked); in
// both, what was sent after a session here started is for it. Everything
// is held for catchUpWindow after the daemon starts, for sessions still reconnecting (see
// catchUpLocked). A message forwarded to the session that holds its recipient's name (see
// forwardLocked) is the holder's if it is new enough for either: a backlog read after the holder
// started still gives it what the recipient's kept inbox would have caught up on. Caller holds mu.
func (d *Daemon) deliverLocked(e bus.Event, mode syncMode) (delivered bool) {
	held := mode == modeCatchUp || mode == modeNormal && time.Since(d.started) <= catchUpWindow
	if held {
		d.pending = append(d.pending, e)
	}
	dev, sess := bus.SplitAddr(e.To)
	fwd := d.forwardLocked(e.To)
	for _, id := range d.recipientsLocked() {
		if !d.addressedLocked(e, id) {
			continue
		}
		if !d.inTimeLocked(e, id, mode) &&
			!(fwd == bus.Addr(d.device, id) && d.receivesLocked(sess) && d.inTimeLocked(e, sess, mode)) {
			continue
		}
		d.addInboxLocked(id, e)
		delivered = true
		if id != proto.CLISession {
			d.noteConversationLocked() // a message arrived for a session here; its answer may soon follow
		}
	}
	_, stopped := d.st.Stopped[sess]
	if mode == modeNormal && !held && dev == d.device && sess != "" && sess != proto.CLISession &&
		d.sessions[sess] == nil && !stopped && fwd == "" {
		d.log.Printf("message %s is for %s, which isn't running here; dropped", e.ID, e.To)
	}
	return delivered
}

// recipientsLocked lists the sessions a message can be delivered to: those registered, which
// includes any in their grace, those stopped within StoppedInbox, and the CLI. Caller holds mu.
func (d *Daemon) recipientsLocked() []string {
	ids := sortedKeys(d.sessions)
	for _, id := range sortedKeys(d.st.Stopped) {
		if d.sessions[id] == nil && time.Since(d.st.Stopped[id]) <= d.opt.StoppedInbox {
			ids = append(ids, id)
		}
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
	if mode == modeNormal || d.sentSinceLocked(e, id, mode) {
		return true
	}
	s := d.sessions[id] // nil for a stopped session
	if mode == modeSkip {
		return s == nil && d.keptSinceLocked(e, id)
	}
	return mode == modeCatchUp && id != proto.CLISession && (s == nil || s.reconnect)
}

// keptInboxLocked says some stopped session has its inbox kept: it stopped within StoppedInbox
// and has not registered again. Caller holds mu.
func (d *Daemon) keptInboxLocked() bool {
	for id, at := range d.st.Stopped {
		if d.sessions[id] == nil && time.Since(at) <= d.opt.StoppedInbox {
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

// addressedLocked says whether e is for session id on this device: sent to it, to this device,
// or to everyone, and not by it. A message to a session that no longer holds its name is for the
// session that does instead (see forwardLocked). Caller holds mu.
func (d *Daemon) addressedLocked(e bus.Event, id string) bool {
	addr := bus.Addr(d.device, id)
	if e.From == addr {
		return false
	}
	if e.To == addr {
		return d.forwardLocked(e.To) == ""
	}
	return e.To == "*" || e.To == d.device || d.forwardLocked(e.To) == addr
}

func (d *Daemon) addInboxLocked(id string, e bus.Event) {
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
	d.kickPush()
}

// catchUpLocked gives a session that reconnects after a restart what arrived for it while the
// daemon was down. Caller holds mu.
func (d *Daemon) catchUpLocked(id string) {
	if time.Since(d.started) > catchUpWindow || d.caughtUp[id] {
		return
	}
	d.caughtUp[id] = true
	for _, e := range d.pending {
		if d.addressedLocked(e, id) {
			d.addInboxLocked(id, e)
		}
	}
	d.saveLocked()
}

// updatePeer reads another device's presence. A presence that moved shows that its device is
// alive even when it can't be read or understood (a passing git failure, a newer version, junk
// pushed by hand): the peer's last change is still counted, so a reader never takes a device for
// gone because of what it couldn't read, and never fails a sync over it. Caller holds advMu.
func (d *Daemon) updatePeer(ctx context.Context, ref, sha string) {
	dev := strings.TrimPrefix(ref, brand.PresencePrefix)
	if dev == d.device || !bus.ValidDevice(dev) {
		return
	}
	d.mu.Lock()
	p := d.peers[dev]
	d.mu.Unlock()
	if p != nil && p.sha == sha {
		return
	}
	c, err := d.repo.ReadCommit(ctx, sha)
	if err != nil {
		if ctx.Err() != nil {
			return // cut short, not unreadable: read it next time
		}
		d.log.Printf("reading %s: %v", ref, err)
		d.sawPresence(dev, sha, true)
		return
	}
	var pres bus.Presence
	if bus.Decode(c.Message, &pres) != nil || pres.V != bus.Version || pres.Device != dev {
		d.sawPresence(dev, sha, true)
		return
	}
	d.mu.Lock()
	// A presence has no parent, so a seal can't pin its place: an old one of the device's own,
	// pushed again, would seal as well as it did. It is refused for being no newer, by the
	// device's own clock, than the last one taken.
	replayed := false
	if ok := d.sealOKLocked(dev, ref, c); ok && !d.unsealed[dev] {
		last, seen := d.presAt[dev]
		if replayed = seen && !pres.At.After(last); !replayed {
			d.presAt[dev] = pres.At
		}
	} else if !ok {
		replayed = true
	}
	if replayed {
		// Not the device's own now, so no sign that it is alive: what it last said stands.
		if p != nil {
			p.sha, p.listed = sha, sha
		}
		d.mu.Unlock()
		return
	}
	// A box key counts only from a sealed presence, which only its device can write, and only
	// as long as its device goes on publishing it: one gone means a Murmur that can't decrypt.
	// One that isn't a usable key is never stored, or every box made for all would fail on it.
	if !d.unsealed[dev] {
		k := pres.BoxKey
		if k != "" && !bus.ValidBoxKey(k) {
			d.log.Printf("%s: %s publishes a box key that isn't one; leaving it out", ref, dev)
			k = ""
		}
		if d.st.BoxKeys[dev] != k {
			if k == "" {
				delete(d.st.BoxKeys, dev)
			} else {
				d.st.BoxKeys[dev] = k
			}
			d.saveLocked()
		}
	}
	d.mu.Unlock()
	if pres.Enc != nil {
		open, err := bus.DecryptPresence(pres, d.device, d.boxKey)
		if err != nil {
			// Encrypted before this device's key was known there: its sessions show at the
			// device's next heartbeat, which is encrypted for this one too.
			open = bus.Presence{V: pres.V, Device: dev, At: pres.At, Host: "encrypted", Sessions: []bus.SessionInfo{},
				BoxKey: pres.BoxKey}
		}
		pres = open
	}
	pres.Clean()
	changed := time.Now()
	if p == nil {
		changed = firstSight(c.Time) // no change of ours to time it by
	}
	d.mu.Lock()
	d.peers[dev] = &peer{pres: pres, sha: sha, listed: sha, lastChange: changed}
	// A name in another device's presence is in use, whether or not the registry shows it yet.
	seen := false
	for _, s := range pres.Sessions {
		if s.Name != "" && d.noteNameLocked(s.Name, bus.Addr(dev, s.ID)) {
			seen = true
		}
	}
	if seen {
		d.saveLocked()
	}
	d.mu.Unlock()
}

// sawPresence records that a device's presence moved to sha without being read: tried and found
// unreadable (tried), or not fetched at all. Either way the device is alive. A device never read
// shows as present, with no sessions, until it is read or goes stale. Caller holds advMu.
func (d *Daemon) sawPresence(dev, sha string, tried bool) {
	if dev == d.device || !bus.ValidDevice(dev) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.peers[dev]
	if p == nil {
		p = &peer{pres: bus.Presence{V: bus.Version, Device: dev, Host: "presence unreadable"}}
		d.peers[dev] = p
	}
	if tried {
		p.sha = sha // not worth reading again until it moves
	}
	if p.listed != sha {
		p.listed, p.lastChange = sha, time.Now()
	}
}

// appendEvent adds an event to this device's log on the remote. It returns once the push has
// landed, so a sender knows whether its message went out.
func (d *Daemon) appendEvent(ctx context.Context, e bus.Event) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	ref := d.meshRef()
	d.cutMu.RLock()
	cut := true
	defer func() {
		if cut {
			d.cutMu.RUnlock()
		}
	}()
	d.mu.Lock()
	wire, err := d.encryptEventLocked(e)
	d.mu.Unlock()
	if err != nil {
		return err
	}
	lease := ""
	for attempt := 0; ; attempt++ {
		sha, err := d.repo.NewRecord(ctx, ref, d.ownTip, wire, wire.Line())
		if err != nil {
			return err
		}
		if lease == "" {
			err = d.push(ctx, sha, ref, bus.FastForward, "")
		} else {
			err = d.push(ctx, sha, ref, bus.Lease, lease)
		}
		if err == nil {
			d.cutMu.RUnlock() // landed; what follows takes advMu
			cut = false
			d.ownTip = sha
			if err := d.repo.UpdateLocal(ctx, ref, sha); err != nil {
				d.log.Printf("updating local %s: %v", ref, err)
			}
			d.advMu.Lock()
			d.mu.Lock()
			d.known[ref] = sha
			if e.Type == bus.Message {
				d.noteConversationLocked() // a message went out; the answer may come soon
			}
			d.mu.Unlock()
			d.advanceMesh(ctx, ref, sha) // delivers to sessions on this device
			d.advMu.Unlock()
			return nil
		}
		if !errors.Is(err, bus.ErrRejected) || attempt > 0 {
			return err
		}
		tip, err := d.repo.RemoteRef(ctx, ref)
		if err != nil {
			return err
		}
		if err := d.repo.Fetch(ctx, []string{ref}); err != nil {
			return err
		}
		over, err := d.overOwnLog(ctx, tip)
		if err != nil {
			return err
		}
		if over {
			// Junk pushed over this log, or an older copy of it: replace it with the next event
			// on top of what this daemon wrote, which readers take as a plain next commit.
			lease = tip
			continue
		}
		// The remote's copy of this log is ahead of what this daemon knew: a restart before the
		// first sync, or a copied .git. Build on the remote's tip instead.
		d.ownTip = tip
	}
}

// overOwnLog says whether the remote's tip of this device's log is something to replace rather
// than build on: not a commit, or one this daemon's own tip already contains. A failure to tell
// is an error, so junk is never taken for a tip to build on. Caller holds writeMu.
func (d *Daemon) overOwnLog(ctx context.Context, tip string) (bool, error) {
	if tip == "" {
		return false, nil
	}
	ok, err := d.repo.IsCommit(ctx, tip)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil // even with nothing written yet: junk is never something to build on
	}
	if d.ownTip == "" {
		return false, nil
	}
	return d.repo.IsAncestor(ctx, tip, d.ownTip)
}

// clearOwnJunk deletes, in the background, refs on the remote that keep this device's own refs
// off it, since the remote refuses a ref whose name clashes with another's: refs under this
// device's names (presence/<device>/..., mesh/<device>/...), refs under the registry's
// (names/...), and a ref at presence or mesh itself, which blocks every device. Nothing Murmur
// writes lives at any of them. Each is tried at
// most once a minute, so a delete the remote refuses isn't pushed, and logged, at every poll.
func (d *Daemon) clearOwnJunk(listed map[string]string) {
	if d.bgCtx == nil {
		return // the first sync, before anything runs in the background; the next poll does it
	}
	var junk []string
	d.mu.Lock()
	for ref := range d.junkTried {
		if _, ok := listed[ref]; !ok {
			delete(d.junkTried, ref)
		}
	}
	for ref := range listed {
		blocking := ref+"/" == brand.PresencePrefix || ref+"/" == brand.MeshPrefix ||
			strings.HasPrefix(ref, brand.NamesRef+"/") ||
			strings.HasPrefix(ref, brand.PresencePrefix+d.device+"/") || strings.HasPrefix(ref, brand.MeshPrefix+d.device+"/")
		if blocking && time.Since(d.junkTried[ref]) >= time.Minute {
			d.junkTried[ref] = time.Now()
			junk = append(junk, ref)
		}
	}
	if len(junk) == 0 || d.bgCtx.Err() != nil {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		ctx, cancel := context.WithTimeout(d.bgCtx, time.Minute)
		defer cancel()
		for _, ref := range junk {
			if err := d.push(ctx, "", ref, bus.FastForward, ""); err != nil {
				d.log.Printf("deleting %s, which blocks the bus's own refs: %v", ref, err)
			} else {
				d.log.Printf("deleted %s, which blocked the bus's own refs", ref)
				d.kickPresence() // a presence push it blocked waits for its next retry otherwise
			}
		}
	}()
}

// appendSoon appends an event in the background, for events nobody waits on.
func (d *Daemon) appendSoon(e bus.Event) {
	if d.bgCtx.Err() != nil {
		return
	}
	d.bg.Add(1)
	go func() {
		defer d.bg.Done()
		ctx, cancel := context.WithTimeout(d.bgCtx, time.Minute)
		defer cancel()
		if err := d.appendEvent(ctx, e); err != nil {
			d.log.Printf("writing %q: %v", e.Line(), err)
		}
	}()
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
	heartbeat := time.NewTicker(d.opt.Heartbeat)
	defer heartbeat.Stop()
	retry := time.NewTimer(0) // publish once at start
	defer retry.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
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
		d.pruneInboxes()
	}
}

func (d *Daemon) publishPresence(ctx context.Context) error {
	// Not cut short by shutdown: a push killed mid-flight can still land, after the goodbye.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	d.cutMu.RLock()
	defer d.cutMu.RUnlock()
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

	d.mu.Lock()
	for id, b := range branches {
		if s := d.sessions[id]; s != nil && b != "" {
			s.branch = b
		}
	}
	pres := bus.Presence{V: bus.Version, Device: d.device, Host: d.host, Owner: d.owner, At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{}}
	// A name is an address, so one session at most carries it here. A session in its grace whose
	// key has come back under another ID has handed its name on: a peer sending to the name must
	// reach the live one, not the one about to stop.
	live := d.liveNamesLocked()
	for _, s := range d.sortedSessionsLocked() {
		name := s.name
		if !s.dropped.IsZero() && live[name] {
			name = ""
		}
		info := bus.SessionInfo{ID: s.id, Name: name, Agent: s.agent, Branch: s.branch, Doing: s.shownDoing(),
			Started: s.started.UTC(), Committed: d.committedLocked(s.id)}
		if !s.lastActive.IsZero() {
			info.LastActive = s.lastActive.UTC()
		}
		pres.Sessions = append(pres.Sessions, info)
	}
	pres.BoxKey = bus.BoxPublic(d.boxKey)
	subject := fmt.Sprintf("%s: %d sessions", d.device, len(pres.Sessions))
	if d.st.Encrypted {
		subject = d.device + " (encrypted)"
	}
	pres, err := d.encryptPresenceLocked(pres)
	d.mu.Unlock()
	if err != nil {
		return err
	}

	sha, err := d.repo.NewRecord(ctx, d.presenceRef(), "", pres, subject)
	if err != nil {
		return err
	}
	return d.push(ctx, sha, d.presenceRef(), bus.Force, "")
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

// pruneInboxes drops the inboxes of sessions stopped longer than StoppedInbox, and those left by
// sessions that didn't come back after a restart.
func (d *Daemon) pruneInboxes() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.started) > catchUpWindow {
		d.pending = nil
	}
	for id, at := range d.st.Stopped {
		if time.Since(at) > d.opt.StoppedInbox {
			delete(d.st.Stopped, id)
			delete(d.st.Inbox, id)
			delete(d.st.Commits, id)
			delete(d.st.T3Routes, id)
			d.saveLocked()
		}
	}
	if time.Since(d.started) < 2*time.Minute {
		return
	}
	for id := range d.st.Inbox {
		if _, stopped := d.st.Stopped[id]; id != proto.CLISession && d.sessions[id] == nil && !stopped {
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
	// StoppedInbox. One that went down with a daemon and never came back has none.
	for id := range d.st.T3Routes {
		if _, stopped := d.st.Stopped[id]; d.sessions[id] == nil && !stopped {
			delete(d.st.T3Routes, id)
			d.saveLocked()
		}
	}
}

func (d *Daemon) sortedSessionsLocked() []*session {
	list := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		list = append(list, s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].started.Before(list[j].started) })
	return list
}

// worktreeOf names the folder of the worktree a session works in: the last part of its top
// directory, never the path, which holds the user's name. "" if cwd isn't in a worktree.
func worktreeOf(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := gitx.RunIn(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	top := filepath.Base(filepath.FromSlash(strings.TrimSpace(out)))
	if top == "." || top == string(filepath.Separator) {
		return ""
	}
	return bus.OneLine(top, 100)
}

// branchOf reads the branch checked out where a session works. It reads HEAD only; nothing
// touches that worktree's index.
func branchOf(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if out, err := gitx.RunIn(ctx, cwd, "symbolic-ref", "--short", "-q", "HEAD"); err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	if out, err := gitx.RunIn(ctx, cwd, "rev-parse", "--short", "HEAD"); err == nil {
		return "detached at " + strings.TrimSpace(out)
	}
	return ""
}
