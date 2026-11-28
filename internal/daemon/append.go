package daemon

import (
	"context"
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
	stillAll := func() bool {
		return d.checkRepoWriteMode(modeStamp) == nil && (still == nil || still()) && d.pendingStill()
	}
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
			if blocked := d.checkRepoWriteMode(modeStamp); blocked != nil {
				return blocked
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
