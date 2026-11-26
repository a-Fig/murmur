package daemon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
//     plus the handoff, and hours of an open session cost 240 listings an hour, not 1,200.
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
// A device coming back from invisible does (setMode): it has a sync's worth to catch up on.
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
	// wait is until the next poll; calm is where the slow phase is.
	wait, calm := d.opt.Poll, d.opt.Poll
	for {
		err := d.pollOnce(ctx)
		if err != nil && ctx.Err() == nil {
			// A lock left by a killed git fails every fetch of its ref, and git doesn't always
			// say so in words ("reference already exists"); clearing stale ones is cheap.
			clearStaleLocks(d.opt.GitDir, d.log)
			d.repairRefs() // and a ref a crash left unreadable fails every fetch in the clone (#644)
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
		rate, next := d.pollNext(wait, calm, failing)
		if rate.quiet != quiet {
			if rate.quiet {
				d.log.Printf("no session is live and no page open; looking at the remote every %s, not every %s", rate.every, d.opt.Poll)
			} else {
				d.log.Printf("a session is live or a page open; looking at the remote every %s, less often while nothing is said", d.opt.Poll)
			}
			quiet = rate.quiet
		}
		calm = rate.calm
		if next != wait {
			wait = next
			t.Reset(wait)
		}
		d.health.polling(rate, wait, d.opt.Poll)
	sleep:
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-d.pollWake:
				if !d.wakeEndsWait(calm, rate, failing) {
					continue sleep
				}
			}
			break
		}
	}
}

// pollNext is what pollLoop decides after each poll: the rate the daemon's state calls for, given
// calm, and the wait until the next poll, given wait, the one before, and whether this poll failed.
func (d *Daemon) pollNext(wait, calm time.Duration, failed bool) (pollRate, time.Duration) {
	rate := d.pollEvery(calm)
	return rate, nextPoll(wait, rate.every, failed)
}

// wakeEndsWait says whether a wake (kickPoll) ends pollLoop's wait, chosen at rate. A session, a
// page or a conversation's move came. If the loop was running at a slower rate than that calls
// for, it looks now and from here on at the faster; if not, it is as quick as it gets. A failure's
// backoff is not ended by it either, unless the loop was quiet: a session that comes then is the
// reason to look now. A device back from invisible ends any wait but a failure's (lookNow).
func (d *Daemon) wakeEndsWait(calm time.Duration, rate pollRate, failing bool) bool {
	if d.lookNow.Swap(false) && !failing {
		return true
	}
	return !(d.pollEvery(calm).every >= rate.every || failing && !rate.quiet)
}

// pollOnce lists the remote's bus refs and fetches and processes the ones that changed. The
// listing is one cheap round trip; objects move only when something changed.
func (d *Daemon) pollOnce(ctx context.Context) (err error) {
	d.polls.Add(1)
	// A listing applied after a newer one would drop presence that has since appeared.
	d.syncMu.Lock()
	defer d.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	defer func() { d.health.synced(ctx, err) }() // before cancel, so ctx still says if its time ran out
	// Before the remote is listed: what this sync reads is no older (settleColorLocked), and it is
	// the one a device back from invisible wanted (lookNow).
	d.lookNow.Store(false)
	began := time.Now()
	listed, err := d.repo.ListRemote(ctx)
	if err != nil {
		return err
	}
	remote := readable(listed)
	d.mu.Lock()
	d.notePresencesListedLocked(remote)
	d.syncing = true // an append may leave its tip to this sync from here on (leaveOwnTip)
	d.mu.Unlock()
	d.advMu.Lock()
	defer d.advMu.Unlock()
	defer func() {
		// Not through a panic, which may have left mu locked: the daemon should die of it, not
		// hang here holding advMu.
		if p := recover(); p != nil {
			panic(p)
		}
		d.readOwnLater(ctx)
	}()

	var changed []string
	d.mu.Lock()
	d.syncSeq++
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
			// Still unread after this long, its messages are history nobody is waiting for. Its tags
			// events don't expire, so they are read once the fetch lands (see readGivenUpTags). An
			// earlier give-up's cursor stands, as it reaches further back.
			d.log.Printf("giving up on the messages in the backlog of %s; skipping them", ref)
			d.health.gaveUp()
			_, kept := d.st.TagsFrom[ref]
			if !kept && d.st.Processed[ref] != remote[ref] && (len(d.sessions) > 0 || d.tagsKnownLocked()) {
				d.st.TagsFrom[ref], kept = d.st.Processed[ref], true
			}
			d.st.Processed[ref] = remote[ref]
			if !kept {
				delete(d.st.Junked, ref) // otherwise readGivenUpTags still goes by it
			}
			delete(d.backlog, ref)
			if d.historyOwed == nil {
				d.historyOwed = map[string]bool{}
			}
			d.historyOwed[ref] = true // the page still shows them, once they are here (advanceMesh)
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
	failed, tips := d.fetch(ctx, changed, remote)
	// Noted before a sync that ran out of time returns: a fetch may have set the clone's copy by
	// now. Every changed ref is noted, whether its fetch failed or wasn't tried at all: one that
	// fails may have written the ref first, as when the git that sets it is stopped at its
	// deadline, and a note that wasn't needed costs one update-ref at the next heartbeat.
	d.mu.Lock()
	for _, ref := range changed {
		d.presenceFetchedLocked(ref)
	}
	d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A ref can move between the listing and the fetch. A presence ref is force-pushed, so the
	// commit listed may never have been fetched; going by it would leave that peer unread and,
	// soon, judged stale. Go by what the fetch brought, as it says, not by the local ref, which
	// another fetch may have set since (#979).
	for ref, sha := range tips {
		if failed[ref] == nil && sha != "" {
			remote[ref] = sha
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

	// The registry and every presence are read first (see readOrder), within a limit of their own
	// (see readFirstContext), so that a stall there leaves the logs the time they need: the logs
	// were fetched already, and they hold the messages. A registry missing from the listing is
	// asked of the remote again, which can stall like any request to it.
	readFrom := time.Now()
	first, cancelFirst := readFirstContext(ctx, d.opt.ReadFirstLimit)
	// The limit it had, which is less than ReadFirstLimit when the sync has little time left: the
	// first sync's 10 s, after a slow listing and fetch, left it 2 or 3 s (#372).
	firstEnd, _ := first.Deadline()
	if _, ok := remote[brand.NamesRef]; !ok {
		d.processNames(first, "") // not on the remote
	}
	var logs []string
	for _, ref := range readOrder(remote) {
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
			d.processNames(first, sha)
		case strings.HasPrefix(ref, brand.MeshPrefix):
			if _, ok := meshDevice(ref); ok {
				logs = append(logs, ref)
			}
		case strings.HasPrefix(ref, brand.PresencePrefix):
			d.updatePeer(first, ref, sha)
		case ref == brand.TeamRef:
			d.processTeam(first, sha)
		}
	}
	firstErr := first.Err()
	cancelFirst()
	d.mu.Lock()
	if tip, ok := remote[brand.TeamRef]; !ok || failed[brand.TeamRef] == nil && d.st.TeamTip == tip {
		d.teamReadSeq = d.syncSeq // every epoch the listing could show has been read (openEventLocked)
	}
	d.mu.Unlock()
	for _, ref := range logs {
		d.advanceMesh(ctx, ref, remote[ref], listed[ref])
	}
	if err := cutShort(ctx, firstErr, firstEnd.Sub(readFrom)); err != nil {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for dev := range d.peers {
		if _, ok := remote[brand.PresencePrefix+dev]; !ok {
			delete(d.peers, dev) // its daemon said goodbye
		}
	}
	for dev := range d.presenceListed {
		if _, ok := remote[brand.PresencePrefix+dev]; !ok {
			delete(d.presenceListed, dev)
		}
	}
	d.lastPollOK = polled
	d.settleColorLocked(began)
	d.kickTeam() // a device to grant the team keys to, or a team ref still empty
	switched := false
	if d.mode != modeNormal && len(d.backlog) == 0 {
		if d.mode == modeSkip {
			d.kickHistory() // the page still shows what was passed over as history
		}
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

// cutShort says why a sync whose reads are done is no fresh look, or nil if it is one. Staleness
// is judged up to the last successful sync, and one cut short didn't read every presence it
// listed: its time ran out (ctx), or the registry and presence ran past their own limit, firstFor,
// and firstErr says so.
func cutShort(ctx context.Context, firstErr error, firstFor time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if firstErr != nil {
		return fmt.Errorf("reading the names registry and presence took over %s: %w", firstFor.Round(100*time.Millisecond), firstErr)
	}
	return nil
}

// meshDevice names the device a mesh ref belongs to, and whether Murmur reads it at all: not one
// under a name no device can have, such as Murmur's own, which could only impersonate it.
func meshDevice(ref string) (string, bool) {
	dev := strings.TrimPrefix(ref, brand.MeshPrefix)
	return dev, bus.ValidDevice(dev)
}

// readFirstContext is what a sync reads the registry and presence on: limit from now, but no more
// than half of what is left of the sync, so that a stall there leaves the logs at least as much,
// however long the listing and fetches took, less the up to five seconds a cancelled git takes to
// exit (see gitx.run). Half rather than a fixed reserve, since a sync's deadline isn't always a
// minute: the first, which registrations wait for, has FirstSyncLimit, 10 s, and must read presence
// too, or a new session could take a name a peer's presence shows.
func readFirstContext(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	now := time.Now()
	end := now.Add(limit)
	if dl, ok := ctx.Deadline(); ok {
		if half := now.Add(dl.Sub(now) / 2); half.Before(end) {
			end = half
		}
	}
	return context.WithDeadline(ctx, end)
}

// readOrder is the order a sync reads the refs it fetched: the names registry, then every
// presence, then the logs (#71). A message's handoff starts as soon as its log is read, and writes
// the turn from what this daemon knows of the sender then, so what was fetched with the message
// about its sender is read first. In the refs' own order, mesh/ before names and presence/, a turn
// could go out without a name the same sync had brought (docs/design/delivery.md, "Who it
// is from, as the receiver knows it"). The registry before presence also keeps a presence's new
// name from being checked against a listing older than it: the recheck it asks for is made by the
// next sync's registry, listed after the presence was read (#40, see notePresenceNameLocked).
func readOrder(remote map[string]string) []string {
	rank := func(ref string) int {
		switch {
		case ref == brand.NamesRef:
			return 0
		case strings.HasPrefix(ref, brand.PresencePrefix):
			return 1
		case ref == brand.TeamRef:
			return 2 // after presence, which pins the keys its records are sealed with
		}
		return 3
	}
	refs := sortedKeys(remote)
	slices.SortStableFunc(refs, func(a, b string) int { return rank(a) - rank(b) })
	return refs
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
		if mesh || presence || ref == brand.NamesRef || ref == brand.TeamRef {
			refs[ref] = sha
		}
	}
	return refs
}

// fetch fetches refs in one go, or, if that fails, one by one, so that a ref git can't store here
// costs that ref and not the rest. It returns the refs that failed and why, logging each once per
// listed SHA, and what was brought for the rest. Once ctx is done, no more fetches start: the
// sync has run out of time, and the rest wait for the next (#978). Caller holds advMu.
//
// Each fetch gets FetchLimit of the sync's minute, so a ref too large to fetch in time can't use up
// the minute and fail the sync. One that runs past the limit on its own is set aside, to be fetched
// outside the syncs (see startSlowFetchLocked). Only time can tell which ref that is: a listing gives
// no sizes, and a filter isn't an option, since one fetch with --filter makes the user's own clone a
// partial clone for all its later fetches.
func (d *Daemon) fetch(ctx context.Context, refs []string, listed map[string]string) (map[string]error, map[string]string) {
	failed, tips := map[string]error{}, map[string]string{}
	// A ref that failed before, at the same SHA, is fetched on its own, so it can't fail the
	// rest, and at most once a minute, so it doesn't cost every poll.
	var batch, alone []string
	d.mu.Lock()
	running := d.slowRef
	for _, ref := range refs {
		f, ok := d.unfetched[ref]
		switch {
		case f.landed:
			// Its fetch outside the syncs brought it. pollOnce reads what that was, though the ref
			// may have moved since: a fetch now could run past the limit in its turn, and a ref
			// that kept doing so would never be read.
			tips[ref] = f.got
			delete(d.unfetched, ref)
		case ref == running && f.sha != listed[ref]:
			alone = append(alone, ref) // moved during its slow fetch, and may well still be slow
		case !ok || f.sha != listed[ref]:
			batch = append(batch, ref)
		case f.slow:
			failed[ref] = errors.New("too slow to fetch within a sync; being fetched apart")
		case time.Since(f.at) < fetchRetry:
			failed[ref] = errors.New("failed less than a minute ago")
		default:
			alone = append(alone, ref)
		}
	}
	d.mu.Unlock()
	timedOut := false
	// try says whether the fetch itself ran past the limit, as opposed to the sync running out.
	try := func(refs []string) (late bool, err error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		fctx, cancel := context.WithTimeout(ctx, d.opt.FetchLimit)
		defer cancel()
		got, err := d.repo.Fetch(fctx, refs)
		if d.testFetched != nil {
			d.testFetched(refs)
		}
		late = err != nil && fctx.Err() != nil && ctx.Err() == nil
		timedOut = timedOut || late
		for ref, sha := range got {
			tips[ref] = sha
		}
		return late, err
	}
	if len(batch) > 0 {
		if _, err := try(batch); err != nil {
			alone = append(alone, batch...)
		}
	}
	slow := map[string]bool{}
	for _, ref := range alone {
		late, err := try([]string{ref})
		if err != nil {
			failed[ref] = err
			slow[ref] = late
		}
	}
	if timedOut {
		clearStaleTemp(d.opt.GitDir, d.log) // a git killed at the limit can leave its download behind
	}
	cut := ctx.Err() != nil // a failure the end of the sync caused says nothing about the ref
	retry := time.Now()
	removed := false
	if !cut && len(failed) > 0 {
		// Both are tried: each failure fails every fetch until it is gone.
		locks, broken := clearStaleLocks(d.opt.GitDir, d.log), d.repairRefs()
		removed = locks || broken
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if removed {
		// A lock or a broken ref left by a killed git fails fetches until removed; with one gone, try
		// every ref that failed again at the next poll rather than in a minute. The lock is
		// usually found stale a poll or more after the fetch it failed.
		retry = time.Time{}
		for ref, f := range d.unfetched {
			f.at = retry
			d.unfetched[ref] = f
		}
	}
	for _, ref := range alone {
		err := failed[ref]
		switch {
		case err == nil:
			delete(d.unfetched, ref)
		case slow[ref]:
			d.log.Printf("fetching %s took over %s; fetching it apart from the syncs", ref, d.opt.FetchLimit)
			d.unfetched[ref] = fetchFail{sha: listed[ref], slow: true}
		case cut:
		default:
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
	// A slow fetch goes on when its ref merely moves, since what it brings shortens the next one: a
	// long log on a slow link, whose owner keeps writing, would otherwise never land. It stops once
	// the ref is gone, or its new tip has been fetched without it.
	if r := d.slowRef; r != "" && (listed[r] == "" || slices.Contains(refs, r) && failed[r] == nil) {
		d.slowCancel()
	}
	d.startSlowFetchLocked(listed)
	return failed, tips
}

// startSlowFetchLocked starts the fetch of one ref set aside as slow, if one is due and none is
// running. It runs outside the syncs and holds no lock, so the rest of the bus goes on meanwhile. It
// has SlowFetchLimit, as git keeps nothing from a fetch that doesn't finish, so a shorter limit could
// keep a ref out for good. Each failure doubles the wait before the next try, since a try can cost as
// much bandwidth as its limit allows. Caller holds mu.
func (d *Daemon) startSlowFetchLocked(listed map[string]string) {
	if d.slowRef != "" || d.bgCtx == nil || d.bgCtx.Err() != nil {
		return // on the first sync, nothing runs in the background yet; the next poll starts it
	}
	now := time.Now()
	for _, ref := range sortedKeys(d.unfetched) {
		f := d.unfetched[ref]
		if !f.slow || f.landed || f.sha != listed[ref] || now.Before(f.at) {
			continue
		}
		ctx, cancel := context.WithTimeout(d.bgCtx, d.opt.SlowFetchLimit)
		d.slowRef, d.slowCancel = ref, cancel
		d.bg.Add(1)
		go func() {
			defer d.bg.Done()
			defer cancel()
			tips, err := d.repo.Fetch(ctx, []string{ref})
			d.endSlowFetch(ref, f.sha, tips[ref], err)
		}()
		return
	}
}

// endSlowFetch records how a slow fetch of ref at sha ended, bringing got. Once it has landed, the
// next sync reads what it brought, even if the ref has moved since (see fetch).
func (d *Daemon) endSlowFetch(ref, sha, got string, err error) {
	if err != nil {
		clearStaleTemp(d.opt.GitDir, d.log)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.kickPoll()
	d.slowRef, d.slowCancel = "", nil
	d.presenceFetchedLocked(ref) // also if it failed: it may have written the ref first
	f, ok := d.unfetched[ref]
	if !ok || !f.slow {
		return
	}
	if err == nil {
		d.log.Printf("fetched %s, which took longer than a sync allows", ref)
		f.landed, f.got = true, got
		d.unfetched[ref] = f
		return
	}
	if f.sha != sha {
		return // it moved meanwhile, so this says nothing about the new tip
	}
	f.tries++
	wait := min(time.Minute<<min(f.tries-1, 6), time.Hour)
	f.at = time.Now().Add(wait)
	d.unfetched[ref] = f
	if d.bgCtx.Err() == nil {
		d.log.Printf("fetching %s apart from the syncs: %v; trying again in %s", ref, err, wait)
	}
}

// clearStaleTemp removes what a git killed mid-download left behind, on Windows, the one platform
// where a cancelled git is killed rather than interrupted (see gitx.run). It leaves files as big as
// what it had downloaded, tmp_pack_* in objects/pack, one per try, and git itself removes them only
// in a gc, two weeks on. Elsewhere it does nothing: an interrupted git cleans up after itself, and
// a file a live git has open can be deleted there, which would fail that git when it finishes.
func clearStaleTemp(gitDir string, l *log.Logger) {
	if runtime.GOOS == "windows" {
		removeStaleTemp(gitDir, l)
	}
}

// removeStaleTemp removes git's temporary downloads that nothing has written to for staleTempAge:
// tmp_* in objects/pack, and beside the loose objects, where a blob over core.bigFileThreshold is
// written. Other gits may be fetching into this clone; a delete fails on a file one has open.
func removeStaleTemp(gitDir string, l *log.Logger) {
	objects := filepath.Join(gitDir, "objects")
	dirs, err := os.ReadDir(objects)
	if err != nil {
		return
	}
	for _, dir := range dirs {
		if !dir.IsDir() || dir.Name() != "pack" && len(dir.Name()) != 2 {
			continue
		}
		files, err := os.ReadDir(filepath.Join(objects, dir.Name()))
		if err != nil {
			continue
		}
		for _, file := range files {
			if !strings.HasPrefix(file.Name(), "tmp_") {
				continue
			}
			path := filepath.Join(objects, dir.Name(), file.Name())
			if fi, err := file.Info(); err == nil && !fi.IsDir() && time.Since(fi.ModTime()) > staleTempAge && os.Remove(path) == nil {
				l.Printf("removed %s, left by a git that was stopped", path)
			}
		}
	}
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

// advanceMesh processes a device's log up to sha and queues its messages, as the sync mode
// says. listed is the tip the remote is known to have, which a fetch can land short of. Caller holds
// advMu.
func (d *Daemon) advanceMesh(ctx context.Context, ref, sha, listed string) {
	if !d.readGivenUpTags(ctx, ref, sha, listed) {
		return
	}
	d.mu.Lock()
	prev := d.st.Processed[ref]
	mode := modeNormal
	if d.backlog[ref] {
		mode = d.mode
	}
	hidden, junked := d.st.Junked[ref]
	// A backlog given up on before it could be fetched is here once the cursor, which went on to
	// the tip the remote had, is what was fetched or comes before it: the page gets its messages.
	owed := d.historyOwed[ref]
	d.mu.Unlock()
	settled := func() {
		d.mu.Lock()
		delete(d.backlog, ref)
		d.mu.Unlock()
	}
	arrived := func() {
		if owed {
			d.mu.Lock()
			delete(d.historyOwed, ref)
			d.mu.Unlock()
			d.kickHistory()
		}
	}
	if prev == sha {
		settled()
		arrived()
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
	tagsOnly := false
	if mode == modeSkip {
		// Nothing is queued from the backlog, unless the first sync failed and sessions have
		// registered since (what was sent after they started is for them), or an inbox is kept for
		// a stopped session (what was sent after it stopped is for it, #30).
		d.mu.Lock()
		waiting := len(d.sessions) > 0 || d.keptInboxLocked() || d.st.Person != ""
		known := d.tagsKnownLocked()
		d.mu.Unlock()
		if !waiting && !known {
			skipTo()
			return
		}
		// Tags don't expire as messages do: a human's change made while this device was away is
		// applied however long ago it was made (#230), so the log is read for those alone.
		tagsOnly = !waiting
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
			d.kickHistory() // what it holds now is passed over for the sessions, and still shown on the page
			return
		}
		arrived()
	}
	// A page at a time, with mu let go between pages, so a long log (a device back after days away)
	// doesn't hold up the sessions, the hook and the page while it is read. Each page moves the cursor
	// to its last commit, so a read cut short goes on from there.
	dev := strings.TrimPrefix(ref, brand.MeshPrefix)
	stalled := false
	err := d.repo.Log(ctx, prev, sha, d.maxRecord, func(commits []bus.Commit) error {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.applyPageLocked(func() {
			n := d.readEventsLocked(ref, dev, commits, mode, tagsOnly, junked, hidden)
			if n > 0 {
				d.st.Processed[ref] = commits[n-1].SHA
			}
			stalled = n < len(commits)
		})
		if stalled {
			return errStalled
		}
		return nil
	})
	if stalled {
		// The cursor stays at the record this device can't open yet. The log counts as read for the
		// backlog: a grant, not time, is what it waits for.
		d.mu.Lock()
		delete(d.backlog, ref)
		d.mu.Unlock()
		return
	}
	if err != nil {
		d.log.Printf("reading %s: %v", ref, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.backlog, ref)
	if _, ok := d.st.Junked[ref]; ok || d.st.Processed[ref] != sha {
		d.st.Processed[ref] = sha
		delete(d.st.Junked, ref)
		d.saveLocked()
	}
}

// applyPageLocked runs apply, which applies a page of a log, with saveLocked put off, and then saves
// once. Caller holds mu, as it must throughout: no save is left out that someone else is owed.
func (d *Daemon) applyPageLocked(apply func()) {
	d.savesHeld = true
	defer func() {
		d.savesHeld = false
		d.saveLocked()
	}()
	apply()
}

// readEventsLocked applies a page of commits read from ref, dev's log, in the mode advanceMesh read
// it in. A commit over the size read is passed over, as any other junk is. Caller holds mu.
func (d *Daemon) readEventsLocked(ref, dev string, commits []bus.Commit, mode syncMode, tagsOnly, junked bool, hidden time.Time) int {
	for i, c := range commits {
		if c.Oversize {
			d.log.Printf("reading %s: commit %s is %d bytes, over the %d read; passed over", ref, c.SHA, c.Size, d.maxRecord)
			continue
		}
		e, ok, wait := d.logEventWaitLocked(c, dev, ref)
		if wait {
			// Encrypted for a team key this device will be granted: read on from here then.
			if !d.waitWarned[ref] {
				d.log.Printf("%s: waiting for a teammate to grant this device the team key to read on from %s", ref, short(c.SHA))
				d.waitWarned[ref] = true
			}
			return i
		}
		if !ok {
			continue
		}
		d.noteOthersFilesLocked(e, dev) // history too: a gone device's refs are known only from it
		if junked && e.At.Before(hidden.Add(-time.Minute)) {
			continue // history the junk hid when this device first looked
		}
		if tagsOnly && e.Type != bus.Tags && e.Type != bus.Receipt {
			continue // a receipt, like a tag, is news however late it is read
		}
		if mode != modeSkip && e.Type != bus.Receipt { // a receipt is bookkeeping, not news for the log
			d.noteLocked(e.At, e.Line())
		}
		if e.Type == bus.Tags {
			d.applyTagsEventLocked(e)
		}
		if (e.Type == bus.SessionStarted || e.Type == bus.SessionStopped) && (mode != modeSkip || !e.At.Before(d.started.Add(-time.Minute))) {
			d.recordSessionEventLocked(e) // for the page's Recent view; history from before this run isn't
		}
		if e.Type == bus.GroupAdd {
			d.applyGroupAddLocked(e, mode) // unless it is old: the session may have left the group since
		}
		if e.Type == bus.Receipt {
			d.applyReceiptLocked(e) // in any mode: a mark is news to its sender however late it is read
		}
		if e.Type == bus.Message {
			// The name a message carries is shown only if it is its sender's (personSenderLocked),
			// and never recorded: see notePersonLocked.
			//
			// A skip passes over history, but a ref read in one only after sessions registered
			// (the first sync failed) also holds what was sent since this daemon started, which
			// is new to it: by the sender's clock, with the minute queueLocked allows. What a
			// skip gave a kept inbox (#30) was read too, so the page lists it.
			kept := d.queueLocked(e, mode)
			if mode != modeSkip || kept || !e.At.Before(d.started.Add(-time.Minute)) || d.ownPersonMsgLocked(e) {
				d.recordMessageLocked(e)
			}
		}
	}
	delete(d.waitWarned, ref)
	return len(commits)
}

// logEventWaitLocked decodes commit c of dev's log, read from ref, opening it if it is encrypted, and
// says whether it is an event to read: one this device can open, and a device's log carries only
// its own sessions' events. It also says whether the event is one to wait for: encrypted for a team
// key this device may yet be granted (openEventLocked). Caller holds mu.
func (d *Daemon) logEventWaitLocked(c bus.Commit, dev, ref string) (bus.Event, bool, bool) {
	var e bus.Event
	if bus.Decode(c.Message, &e) != nil {
		return e, false, false
	}
	e, ok, wait := d.openEventLocked(e, dev, ref, c.SHA)
	if !ok {
		return e, false, wait
	}
	if e.Clean(); !e.Valid() {
		return e, false, false
	}
	from, _ := bus.SplitAddr(e.From)
	return e, from == dev, false
}

// tagsKnownLocked says whether this device has a name or tags for any session: a tags event applies
// only to a session it has (see tagKeyLocked), so with neither, as on a new clone, nothing in a
// log's history is for it but a change for a session still reconnecting (holdForLocked), which a
// skip with nothing else to read the log for lets go. Caller holds mu.
func (d *Daemon) tagsKnownLocked() bool {
	return len(d.st.SessionNames) > 0 || len(d.st.Tags) > 0
}

// readGivenUpTags applies the tags events of the stretch of ref's log that a give-up passed over
// (see state.TagsFrom), now that sha has been fetched. The stretch ends at Processed, the tip when
// it was given up on; what came later is read as usual, messages too. listed is the tip the sync's
// listing shows. It says false if the log has to wait for the next poll: git failed, as with any
// failure reading a log, or that tip hasn't come yet. Caller holds advMu.
func (d *Daemon) readGivenUpTags(ctx context.Context, ref, sha, listed string) bool {
	d.mu.Lock()
	from, given := d.st.TagsFrom[ref]
	to := d.st.Processed[ref]
	hidden, junked := d.st.Junked[ref]
	d.mu.Unlock()
	if !given {
		return true
	}
	failed := func(err error) bool {
		d.log.Printf("reading %s: %v", ref, err)
		return false
	}
	ok, err := d.repo.IsCommit(ctx, to)
	if err != nil {
		return failed(err)
	}
	if !ok {
		// The tip given up on isn't here, or it was junk, so sha doesn't hold it. A fetch that started
		// earlier may have landed, as a slow one does: a later fetch brings that tip, and meanwhile
		// advanceMesh would take sha for a rewrite and move Processed back past it. A fetch that went
		// past the listing, as the peer wrote between the two, is no older, and has settled it.
		if sha != listed {
			current, err := d.repo.IsAncestor(ctx, listed, sha)
			if err != nil {
				return failed(err)
			}
			if !current {
				return false
			}
		}
		isCommit, err := d.repo.IsCommit(ctx, sha)
		if err != nil {
			return failed(err)
		}
		if !isCommit {
			return true // junk, which advanceMesh passes over; its owner builds over it
		}
		// The remote is at sha. It can't be told from here whether the log was rewritten or built
		// over junk, so it gets what a rewritten log gets: its history isn't replayed.
	} else if from != "" {
		if ok, err = d.repo.IsAncestor(ctx, from, to); err != nil {
			return failed(err)
		}
	}
	if ok {
		// A page at a time, as advanceMesh reads, the cursor moving with each.
		dev := strings.TrimPrefix(ref, brand.MeshPrefix)
		stalled := false
		err := d.repo.Log(ctx, from, to, d.maxRecord, func(commits []bus.Commit) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.applyPageLocked(func() {
				for i, c := range commits {
					if c.Oversize {
						continue
					}
					e, ok, wait := d.logEventWaitLocked(c, dev, ref)
					if wait {
						// As readEventsLocked does: the stretch is read on from here once this device
						// holds the key, so no tags in it are lost.
						if i > 0 {
							d.st.TagsFrom[ref] = commits[i-1].SHA
						}
						stalled = true
						return
					}
					if ok {
						d.noteOthersFilesLocked(e, dev) // as readEventsLocked does
					}
					if ok && e.Type == bus.Tags && !(junked && e.At.Before(hidden.Add(-time.Minute))) {
						d.applyTagsEventLocked(e)
					}
					if ok && e.Type == bus.Receipt {
						d.applyReceiptLocked(e) // as readEventsLocked does
					}
				}
				d.st.TagsFrom[ref] = commits[len(commits)-1].SHA
			})
			if stalled {
				return errStalled
			}
			return nil
		})
		if stalled {
			// A grant, not time, is what the log waits for: it counts as read for the backlog, as in
			// advanceMesh.
			d.mu.Lock()
			delete(d.backlog, ref)
			d.mu.Unlock()
			return false
		}
		if err != nil {
			return failed(err)
		}
	} else {
		d.log.Printf("%s was rewritten; skipping its history rather than replaying its tags", ref)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.st.TagsFrom, ref)
	delete(d.st.Junked, ref)
	d.saveLocked()
	d.kickHistory() // the messages given up on are here now, and the page still shows them
	return true
}

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

// updatePeer reads another device's presence. A presence that moved shows that its device is
// alive even when it can't be read or understood (a passing git failure, a newer version, junk
// pushed by hand): the peer's last change is still counted, so a reader never takes a device for
// gone because of what it couldn't read, and never fails a sync over it. The exception is a presence
// that isn't the device's own (checkPresenceLocked): another key, or no seal from a device that
// seals. That one counts for nothing. Caller holds advMu.
func (d *Daemon) updatePeer(ctx context.Context, ref, sha string) {
	dev := strings.TrimPrefix(ref, brand.PresencePrefix)
	if dev == d.device || !bus.ValidDevice(dev) {
		return
	}
	d.mu.Lock()
	p := d.peers[dev]
	refused := d.refusedSHA[dev] == sha
	d.mu.Unlock()
	if p != nil && p.sha == sha || refused {
		return
	}
	c, err := d.repo.ReadCommit(ctx, sha, d.maxRecord)
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
	// Names, beside a box, are taken as used for good just as sessions are, so they get the same cap.
	// The sessions inside a box are counted again once it is open, below.
	if n := max(len(pres.Sessions), len(pres.Names)); n > bus.MaxSessions {
		d.log.Printf("reading %s: it lists %d sessions, over the %d a presence may; passed over", ref, n, bus.MaxSessions)
		d.sawPresence(dev, sha, true)
		return
	}
	d.mu.Lock()
	ok, sealed := d.checkPresenceLocked(dev, ref, c, pres)
	if !ok {
		// Not dev's: nothing in it is taken, not even as a sign of life, and it isn't read again.
		d.refusedSHA[dev] = sha
		d.mu.Unlock()
		return
	}
	if !sealed {
		pres.TeamAdmin = 0
	}
	locked, opened := false, true
	// Trust epoch and key announcements in a sealed plaintext presence or inside the box.
	if !sealed || pres.Box != nil {
		pres.Epoch, pres.Holds = "", nil
	}
	if pres.Box != nil {
		pres.Groups, pres.Receipts, pres.Files, pres.FilesOff, pres.PersonFilesOff = false, false, false, false, false
		open, err := bus.DecryptPresence(pres, d.keyringLocked())
		if err != nil {
			// Encrypted for a team key this device hasn't been granted yet, or for its writer alone
			// before the team key was made. The device is there, with nothing about it readable
			// here; it is read again once this device holds the key (addTeamKeysLocked).
			opened = false
			if errors.Is(err, bus.ErrNotForUs) {
				locked = true
			} else {
				d.log.Printf("%s: %s doesn't open: %v", ref, short(sha), err)
			}
			open = bus.Presence{V: pres.V, Device: pres.Device, At: pres.At, BoxKey: pres.BoxKey, Names: pres.Names, TeamAdmin: pres.TeamAdmin}
		}
		pres = open
	}
	d.mu.Unlock()
	if n := len(pres.Sessions); n > bus.MaxSessions {
		d.log.Printf("reading %s: it lists %d sessions, over the %d a presence may; passed over", ref, n, bus.MaxSessions)
		d.sawPresence(dev, sha, true)
		return
	}
	pres.Clean()
	changed := time.Now()
	switch {
	case p == nil:
		changed = firstSight(c.Time) // no change of ours to time it by
	case p.listed == sha:
		// The SHA it last had, read again because a key came that opens it (addTeamKeysLocked), or
		// because its fetch landed after failing (sawPresence): no new heartbeat, so it keeps the
		// time it changed, as sawPresence does. A fresh time made a stale device look reachable in
		// who and lookup (#871).
		changed = p.lastChange
	}
	d.mu.Lock()
	if sealed {
		d.newPresenceLocked(dev, pres.At) // the high-water mark of its presence times
	}
	var read *readFiles
	if opened {
		read = &readFiles{files: pres.Files, off: pres.FilesOff, personOff: pres.PersonFilesOff}
	} else if old := d.peers[dev]; old != nil {
		read = old.read // a box that doesn't open here says nothing new of files
	}
	d.peers[dev] = &peer{pres: pres, sha: sha, listed: sha, lastChange: changed, written: c.Time, writtenOf: sha, colorFrom: colorDate(c.Time, d.listedAfterLocked(dev, sha)), sealed: sealed, locked: locked, read: read}
	if !locked {
		d.noteSessionsLocked(dev, pres.Sessions, pres.At)
		d.droppedThereLocked(dev, pres.Sessions)
	}
	// A name in another device's presence is in use, whether or not the registry shows it yet.
	seen := false
	var liveOnDev map[string]bool
	for _, s := range pres.Sessions {
		if s.ID == bus.PersonSession {
			// A person's name is claimed in the registry under its own key (PersonClaims), not Used.
			if d.notePersonLocked(s.Name, bus.Addr(dev, s.ID)) {
				seen = true
			}
			continue
		}
		if s.Name != "" {
			if liveOnDev == nil {
				liveOnDev = map[string]bool{}
			}
			liveOnDev[s.Name] = true
			if d.notePresenceNameLocked(s.Name, bus.Addr(dev, s.ID)) {
				seen = true
			}
			if d.notePeerHolderLocked(dev, s.Name, s.ID) {
				seen = true
			}
		}
		if d.notePeerNameLocked(bus.Addr(dev, s.ID), cmp.Or(s.Name, s.Was)) {
			seen = true
		}
	}
	for _, s := range pres.Sessions {
		if s.ID != bus.PersonSession && s.Name == "" && s.Was != "" && !liveOnDev[s.Was] {
			if d.notePeerHolderLocked(dev, s.Was, s.ID) {
				seen = true
			}
		}
	}
	for name, id := range pres.Names { // the same, from a presence this device can't open
		if d.notePresenceNameLocked(name, bus.Addr(dev, id)) {
			seen = true
		}
	}
	if seen {
		d.saveLocked()
	}
	d.mu.Unlock()
}

// peerFilesLocked is whether the session at addr, on the device p is, takes files, as lookup and
// send's note give it (#859): proto.FilesOn, FilesOff, or "" for unknown. It goes by the last
// presence read with its box open, which stands while a newer one waits to be read, doesn't open
// or is refused: a device that has files never flickers to unknown as it republishes, and a switch
// turned shows a fetch late. A presence without files is unknown, not taken for an old daemon's:
// each way of telling an old daemon from one just upgraded, its new presence not read yet, had a
// gap (Sol's reviews of #865), and daemons update themselves within the hour. Caller holds mu.
func (d *Daemon) peerFilesLocked(p *peer, addr string) string {
	switch r := p.read; {
	case r == nil || !r.files:
		return ""
	case bus.IsPerson(addr):
		return onOff(!r.personOff) // a person's own switch, not their agents'
	default:
		return onOff(!r.off)
	}
}

// sawPresence records that a device's presence moved to sha without being read: tried and found
// unreadable (tried), or not fetched at all. Either way the device is alive. A device never read
// shows as present, with no sessions, until it is read or goes stale. One read before keeps what it
// said; peerFilesLocked judges what it said of files. Caller holds advMu.
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

// leaveOwnTip takes advMu for an append to read sha, the tip of this device's log that it just
// pushed, and says false; or, if a sync still holds advMu after OwnTipWait, leaves the tip for
// that sync to read at its end, and says true. The wait is longer than a sync takes when nothing
// is slow, so a send to a session on this device has nearly always queued its message by the time
// it answers.
//
// A sync holds advMu through its fetches, which a slow ref stretches to most of a minute, and the
// append holds writeMu: waiting that long kept a send that had already landed from answering its
// agent, past the client's deadline at worst, with every other write to the log behind it (#738).
// Reading the tip only queues the event for this device's own sessions, which the sync does as
// well, later. Another holder of advMu has it briefly, and is waited for.
func (d *Daemon) leaveOwnTip(sha string) bool {
	for end := time.Now().Add(d.opt.OwnTipWait); ; time.Sleep(2 * time.Millisecond) {
		d.mu.Lock()
		// With mu held, so that a sync can't end between the two looks; TryLock never waits, so
		// the lock order (advMu, then mu) isn't at stake.
		if d.advMu.TryLock() {
			d.mu.Unlock()
			return false
		}
		// A tip already left says this sync is a slow one: the writes behind it don't each wait
		// their own OwnTipWait to learn that again.
		if d.syncing && (d.ownLater != "" || time.Now().After(end)) {
			if d.ownLater == "" {
				d.log.Printf("the sync is taking long: it reads this device's own writes when it ends")
			}
			d.ownLater = sha // read before syncing goes false (readOwnLater)
			d.mu.Unlock()
			return true
		}
		d.mu.Unlock()
	}
}

// readOwnLater reads the tips of this device's log that appends left for this sync, at its end,
// and lets the next append read its own again. It has a time of its own: the sync's may have run
// out, and the events are on the remote already. Caller holds advMu.
func (d *Daemon) readOwnLater(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	ref := d.meshRef()
	for {
		d.mu.Lock()
		sha := d.ownLater
		d.ownLater = ""
		if sha == "" {
			d.syncing = false
			d.mu.Unlock()
			return
		}
		d.known[ref] = sha
		d.mu.Unlock()
		d.advanceMesh(ctx, ref, sha, sha)
	}
}

// appendEvent adds an event to this device's log on the remote. It returns once the push has
// landed, so a sender knows whether its message went out.
func (d *Daemon) appendEvent(ctx context.Context, e bus.Event) error {
	return d.appendEventIf(ctx, e, nil, nil)
}

// appendEventIf is appendEvent, asking still once each push's turn is had: a message with files
// isn't sent if Send files went off while it waited (#765). With settle, a push that ends unsure
// leaves the event pending rather than failed, and appendEventIf returns a pendingError: it is
// pushed again until it lands or is given up, and settle is told which (pending.go, #781).
func (d *Daemon) appendEventIf(ctx context.Context, e bus.Event, still func() bool, settle settleFunc) error {
	if err := d.awaitEpoch(ctx); err != nil {
		return err
	}
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	ref := d.meshRef()
	d.mu.Lock()
	boxed, err := d.encryptEventLocked(e)
	modeStamp := d.repoModeStamp.Load()
	d.mu.Unlock()
	if err != nil {
		return err
	}
	msg, err := bus.Encode(boxed, bus.EventSubject)
	if err != nil {
		return err
	}
	// A push carries the sends pending under it, so it goes out only if they still may: a send
	// whose Send files switch went off is taken out of the log first (dropSwitchedOff).
	modeStill := func() bool { return d.repoModeStamp.Load() == modeStamp }
	stillAll := func() bool { return modeStill() && (still == nil || still()) && d.pendingStill() }
	lease := ""
	reconciled := false
	for {
		d.dropSwitchedOff(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		sha, err := d.repo.NewCommit(ctx, d.ownTip, msg)
		if err != nil {
			return err
		}
		if lease == "" {
			err = d.pushFrom(ctx, d.repo, sha, ref, bus.FastForward, "", stillAll)
		} else {
			err = d.pushFrom(ctx, d.repo, sha, ref, bus.Lease, lease, stillAll)
		}
		if errors.Is(err, errSendOff) {
			if !modeStill() {
				return errRepoModeChanged
			}
			if still == nil || still() {
				continue // a pending send's policy changed as this waited: build without it
			}
		}
		if err != nil && !pushRefused(err) {
			err = unsureError{err}
			if settle != nil && d.pendLocked(sha, settle, still, modeStamp, err) {
				return pendingError{err}
			}
		}
		if err == nil {
			d.ownLanded(ctx, ref, sha, e.Type == bus.Message)
			return nil
		}
		if !errors.Is(err, bus.ErrRejected) || reconciled {
			return err
		}
		reconciled = true
		rejected := err
		tip, err := d.repo.RemoteRef(ctx, ref)
		if err != nil {
			return err
		}
		over, err := d.overRemoteTip(ctx, ref, tip)
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
		// first sync, or a copied .git. Build on the remote's tip instead. A send pending on the
		// tip that was given up for it is settled by whether the remote's has it.
		if !d.settlePendingOn(ctx, tip, rejected, false) {
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			return rejected
		}
		d.ownTip = tip
	}
}

// ownLanded records sha, the tip of this device's log just pushed, as landed, and with it every
// send pending under it; message says the write was a message, after which an answer may come
// soon. Caller holds writeMu.
func (d *Daemon) ownLanded(ctx context.Context, ref, sha string, message bool) {
	d.ownTip = sha
	if err := d.repo.UpdateLocal(ctx, ref, sha); err != nil {
		d.log.Printf("updating local %s: %v", ref, err)
	}
	landed := d.pendSends
	d.pendSends = nil
	d.mu.Lock()
	if message || len(landed) > 0 {
		d.noteConversationLocked() // a message went out; the answer may come soon
	}
	d.mu.Unlock()
	for _, p := range landed {
		p.settle(true, false, nil)
	}
	if d.leaveOwnTip(sha) {
		return
	}
	d.mu.Lock()
	d.known[ref] = sha
	d.mu.Unlock()
	d.advanceMesh(ctx, ref, sha, sha) // queues for sessions on this device
	d.advMu.Unlock()
}

// pushRefused says a push that ended with err is known not to have landed: the remote refused it,
// or it was never sent.
func pushRefused(err error) bool {
	return errors.Is(err, bus.ErrRejected) || errors.Is(err, bus.ErrRefused) ||
		errors.Is(err, ErrNoConsent) || errors.Is(err, ErrInvisible) || errors.As(err, new(notPushedError))
}

// unsureError is what a write to this device's log ends with when its push ended badly without
// the remote having refused it: it may land yet. The push looks at the remote before it gives up
// (bus.Repo.Push), but that look can fail as the push did, and on Windows a push killed at its
// time limit leaves its children running (internal/gitx/tree_windows.go says so of a fetch), so
// it can land after the look. What was made for the event, its files and its receipt record, is
// kept then, as the event may be on the bus (#745).
type unsureError struct{ error }

func (e unsureError) Unwrap() error { return e.error }

// overRemoteTip says whether tip, the remote's tip of this device's log, which refused a write, is
// something to replace rather than build on (overOwnLog). A tip that is a commit here already is
// looked at as it is, unfetched: this clone's own from before a restart, or anything an earlier
// fetch brought. One that isn't here is fetched, for Options.OwnFetchLimit at most, rather than for
// the send's whole time with every other write behind writeMu (#222).
//
// A fetch that runs out of that time fails the write: it says nothing of what the tip is. A slow
// link times out as a large object does, and the tip may hold this device's own history that this
// clone lacks, as after a re-clone or a restore that kept the device's identity; replacing it would
// lose that history (Sol's review of #845). The sends then fail quickly until the sync's background
// fetch of the log (#15) brings the tip, after which it is looked at as any tip here is. Caller
// holds writeMu.
func (d *Daemon) overRemoteTip(ctx context.Context, ref, tip string) (bool, error) {
	if tip == "" {
		return false, nil
	}
	here, err := d.repo.IsCommit(ctx, tip)
	if err != nil {
		return false, err
	}
	if !here {
		fctx, cancel := context.WithTimeout(ctx, d.opt.OwnFetchLimit)
		err := d.fetchOwn(fctx, ref)
		late := err != nil && fctx.Err() != nil && ctx.Err() == nil
		cancel()
		if late {
			clearStaleTemp(d.opt.GitDir, d.log) // a git killed at the limit can leave its download behind
			return false, fmt.Errorf("the tip of this device's log on the remote couldn't be fetched in %v; it is fetched in the background, and sends work again once it is here", d.opt.OwnFetchLimit)
		}
		if err != nil {
			return false, err
		}
	}
	return d.overOwnLog(ctx, tip)
}

// fetchOwn fetches ref, this device's own log; a test can stand in for it (testFetch).
func (d *Daemon) fetchOwn(ctx context.Context, ref string) error {
	if d.testFetch != nil {
		return d.testFetch(ctx)
	}
	return d.fetchOne(ctx, ref)
}

// fetchOne fetches one ref, for a caller that goes by the ref's commit as the remote lists it.
func (d *Daemon) fetchOne(ctx context.Context, ref string) error {
	_, err := d.repo.Fetch(ctx, []string{ref})
	return err
}

// tidyLoop removes the refs that fetches leave (bus.Repo.Tidy), every TidyEvery, apart from the
// syncs: a delete can wait for packed-refs, which no sync should (#978). A bus with nothing to
// fetch has them removed too, such as those a daemon stopped mid-fetch left (#982). Every one there
// is looked for on the first try and an hour apart, as well as the refs of fetches this daemon
// made; a try that fails is made again after twice the wait, up to an hour. It starts before the
// first sync, so that a daemon stopped during it still removes what that sync's fetches left.
func (d *Daemon) tidyLoop(ctx context.Context) {
	wait, swept, logged := d.opt.TidyEvery, time.Time{}, false
	for {
		select {
		case <-ctx.Done():
			// What the last fetches leave goes now, if it goes quickly.
			tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			d.repo.Tidy(tctx, false)
			cancel()
			return
		case <-time.After(wait):
		}
		all := swept.IsZero() || time.Since(swept) > time.Hour
		// Not cut short when the daemon stops: on Windows a git stopped is killed, and one killed
		// while it holds packed-refs.lock leaves it, failing the user's own git (Opus review).
		// Tidy has git wait a second for that lock at most, so it is quick.
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := d.repo.Tidy(tctx, all)
		cancel()
		if err != nil {
			if !logged && ctx.Err() == nil {
				d.log.Printf("removing the refs fetches leave: %v%s; trying again later", err, packedRefsLocked(d.opt.GitDir))
				logged = true
			}
			wait = min(wait*2, time.Hour)
			continue
		}
		if all {
			swept = time.Now()
		}
		wait, logged = d.opt.TidyEvery, false
	}
}

// packedRefsLocked says, for a log line, that packed-refs is locked and since when, as a delete of a
// ref waits for that lock, and a git killed while it held it leaves it for good; or "".
func packedRefsLocked(gitDir string) string {
	fi, err := os.Stat(filepath.Join(gitDir, "packed-refs.lock"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf(" (%s has been there for %s; a git that was killed can leave it, and git then fails to delete refs until it is removed)",
		filepath.Join(gitDir, "packed-refs.lock"), time.Since(fi.ModTime()).Round(time.Second))
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

// maxUnwritten bounds st.Unwritten: past it the oldest is let go, and logged.
const maxUnwritten = 100

// appendSoon appends a session's start or stop to this device's log in the background: nobody
// waits on it. It goes after the presence of a session just started (see awaitPresence). It is
// kept in the state file until it lands, and a write that fails, offline or as the daemon stops,
// is tried again after the next presence that is pushed, in this run or the next (#434). Nothing
// else would put it on the bus: later events build on the log's last tip that landed. Every
// device's Recent view shows it, and its notes the line. A write reported as failed may have
// landed all the same (see Repo.Push), so a start or stop can be on the log twice: the Recent view
// keeps one by its ID (recordActLocked), and the notes show the line twice.
func (d *Daemon) appendSoon(e bus.Event) {
	if d.hidden.Load() {
		return // invisible: never written, then or later (mode.go)
	}
	d.mu.Lock()
	d.st.Unwritten = append(d.st.Unwritten, e)
	if n := len(d.st.Unwritten) - maxUnwritten; n > 0 {
		for _, old := range d.st.Unwritten[:n] {
			d.log.Printf("letting %s go: more than %d starts and stops are waiting to be written", logged(old), maxUnwritten)
		}
		d.st.Unwritten = slices.Delete(d.st.Unwritten, 0, n)
	}
	d.saveLocked()
	d.mu.Unlock()
	d.writeUnwritten()
}

// writeUnwritten writes st.Unwritten to this device's log in the background, oldest first, one
// writer at a time. It stops at the first write that fails, for the next presence pushed to start
// it again (presenceLoop). Each is taken out once it has landed.
func (d *Daemon) writeUnwritten() {
	d.mu.Lock()
	if d.unwriting || len(d.st.Unwritten) == 0 || d.bgCtx == nil || d.bgCtx.Err() != nil || d.hidden.Load() {
		d.mu.Unlock()
		return
	}
	d.unwriting = true
	d.bg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.bg.Done()
		for {
			d.mu.Lock()
			if len(d.st.Unwritten) == 0 {
				d.unwriting = false
				d.mu.Unlock()
				return
			}
			e := d.st.Unwritten[0]
			d.mu.Unlock()
			// Readers take only their own version (Event.Valid), and one held from a run before an
			// update may carry the old one.
			e.V = bus.Version
			ctx, cancel := context.WithTimeout(d.bgCtx, time.Minute)
			err := d.awaitPresence(ctx)
			if err == nil {
				err = d.appendEvent(ctx, e)
			}
			cancel()
			d.mu.Lock()
			if err != nil {
				d.unwriting = false
				warned := d.unwrittenWarned
				d.unwrittenWarned = true
				d.mu.Unlock()
				if !warned {
					d.log.Printf("writing %s: %v; it is tried again after the next presence pushed", logged(e), err)
				}
				return
			}
			// Let go as maxUnwritten ran over, it isn't at the head any more.
			if len(d.st.Unwritten) > 0 && d.st.Unwritten[0].ID == e.ID {
				d.st.Unwritten = slices.Delete(d.st.Unwritten, 0, 1)
			}
			if d.unwrittenWarned {
				d.unwrittenWarned = false
				d.log.Printf("wrote %s, which had failed", logged(e))
			}
			d.saveLocked()
			d.mu.Unlock()
		}
	}()
}

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
	pres := bus.Presence{V: bus.Version, Device: d.device, Host: d.host, Owner: d.owner, Nick: d.st.Nick, TeamAdmin: teamAdminModes,
		At:       d.nextPresenceAtLocked(),
		Sessions: []bus.SessionInfo{}, Groups: true, Receipts: true, Files: true, FilesOff: !d.agentRecv.on.Load(), PersonFilesOff: !d.pageRecv.on.Load(), DND: d.dnd.Load()}
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
		return d.repoModeStamp.Load() == modeStamp
	}); err != nil {
		if errors.Is(err, errSendOff) {
			return errRepoModeChanged
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
