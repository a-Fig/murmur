package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

// A send whose push ended unsure (unsureError: it timed out or lost its connection with the push
// under way) isn't handed back to its sender as not sent (#781). Tyler, 2026-11-16: "just keep
// retrying automatically, and dont hand the message back as unsent until we've tried alot and are
// giving up."
//
// Its commit is left pending: later writes to this device's log build on it, so one of theirs that
// lands carries it along, and pendingLoop pushes the log's tip again, the same commits, until the
// remote has them. A push that landed late is then one the remote already has, and the message is
// never on the log twice. st.Unwritten, which writes a session's starts and stops later, isn't the
// way: each try of it makes a new commit, and a first push that landed late would put the message
// on the log twice.
//
// A pending send is given up after Options.PendingGiveUp, pendingGiveUp by default, and its sender
// is told in its inbox that it wasn't sent, with why in a few plain words; until then it answered
// as a send that landed, with nothing added (Tyler, 2026-11-16). As the daemon stops, pendingLoop
// asks the remote first under one overall Options.PendingStopLimit budget (pendingStopLimit by
// default, started when bgCtx is cancelled), settling sends the remote already has as landed and
// giving up the rest even if writeMu is held; if the remote can't answer in time, every pending
// send is given up as unconfirmed and its sender is told not sent before the loop exits. Nothing
// is kept across a restart (#857).

// pendingGiveUp is how long after its first push ended unsure a pending send is given up, at the
// first retry to end past it. Tyler set it
// (2026-11-16): 30 minutes was too long for a sender to believe a message went that hadn't. With
// the backoff that is about six pushes again.
const pendingGiveUp = 3 * time.Minute

// pendingLookLimit is how long a give-up after PendingGiveUp (or consent/visibility loss) waits to
// ask the remote whether pending sends landed.
const pendingLookLimit = 30 * time.Second

// pendingStopLimit is the overall budget a stopping daemon gives to settling pending sends before
// giving them up (#857), measured from when bgCtx is cancelled. At the default 12 s it splits into
// 5 s for the remote lookup, 2 s for local git checks (IsCommit, IsAncestor), and 5 s reserved for
// gitx's cmd.WaitDelay if either the remote or a local git command is killed at its deadline.
//
// Worst-case wall time from stop start:
//   - Remote stalls: 5 s remote + up to 5 s WaitDelay + state save (~10 s).
//   - Remote answers right before 5 s, then a local git check stalls: 5 s remote + 2 s local + up
//     to 5 s WaitDelay + state save (~12 s).
//   - Stop arrives mid-retry: the stop clock still started at bgCtx cancellation, so a slow unwind
//     eats into the same 5 s remote window (skipping the remote once past 5 s); however, if the
//     in-flight retry itself takes longer than 12 s to unwind, or saving state / acquiring mu
//     stalls, pendingLoop can exceed 12 s and Run's 20 s proto.DrainTimeout caps how long exit waits
//     for it.
//
// proto.DrainTimeout (20 s) is followed by d.goodbye() (up to proto.GoodbyeTimeout, 15 s);
// client.Stop only waits 10 s for exit before returning, so this bounds the pending drain rather
// than total shutdown time. When the remote lookup fails or times out, every pending send is given
// up as unconfirmed (absent=false) and its sender is told not sent.
const pendingStopLimit = 12 * time.Second

// settleFunc is told once how a pending send ended: landed; or given up, with absent saying the
// remote, asked, doesn't have it, and err why. It may be called with or without writeMu held
// (stopPending runs without writeMu when writeMu is held), and never with pendMu or mu held.
type settleFunc func(landed, absent bool, err error)

type pendingSend struct {
	sha    string
	settle settleFunc
	// still says the send may still go out: false once the Send files switch its files need went
	// off (#765). It is nil for a send with no files.
	still func() bool
}

// pendingError is what a send's write ends with when its push ended unsure and it was left pending.
type pendingError struct{ error }

func (e pendingError) Unwrap() error { return e.error }

func (d *Daemon) hasPending() bool {
	d.pendMu.Lock()
	defer d.pendMu.Unlock()
	return len(d.pendSends) > 0
}

func (d *Daemon) takePending() []pendingSend {
	d.pendMu.Lock()
	pending := d.pendSends
	d.pendSends = nil
	d.pendMu.Unlock()
	return pending
}

// pendLocked leaves sha, the commit of a send whose push ended with err, unsure, pending: the
// writes after it build on it, and pendingLoop pushes it again. It says false, and leaves nothing
// pending, if the daemon is stopping and no pendingLoop runs to push it or give it up: the send then
// fails unsure, as before #781 (Sol's review of #818). Caller holds writeMu.
func (d *Daemon) pendLocked(sha string, settle settleFunc, still func() bool, err error) bool {
	d.pendMu.Lock()
	if !d.pendRun && (d.bgCtx == nil || d.bgCtx.Err() != nil) {
		d.pendMu.Unlock()
		return false
	}
	if len(d.pendSends) == 0 {
		d.pendBase, d.pendFrom = d.ownTip, time.Now()
	}
	d.pendSends = append(d.pendSends, pendingSend{sha, settle, still})
	d.ownTip = sha
	start := !d.pendRun
	if start {
		d.pendRun = true
		d.bg.Add(1)
	}
	d.pendMu.Unlock()
	d.log.Printf("the push of a message ended unsure (%v); it is pushed again until it lands", err)
	if start {
		go d.pendingLoop()
	}
	return true
}

// lockWriteUntil acquires writeMu while ctx is live, returning false if ctx ends first.
func (d *Daemon) lockWriteUntil(ctx context.Context) bool {
	for {
		if d.writeMu.TryLock() {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		wait := 10 * time.Millisecond
		if dl, ok := ctx.Deadline(); ok {
			if left := time.Until(dl); left <= 0 {
				return false
			} else if left < wait {
				wait = left
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// stopPending settles pending sends as the daemon stops under one overall PendingStopLimit budget
// measured from started (when bgCtx was cancelled), reserving time for local git checks and gitx's
// 5 s WaitDelay. Reading the remote tip and checking local ancestry do not write the bus, so when
// writeMu is already held by another goroutine stopPending checks the remote without waiting for
// writeMu (skipping only the d.ownTip update, which writeMu guards).
func (d *Daemon) stopPending(started time.Time, holdingWrite bool) {
	limit := d.opt.PendingStopLimit
	waitReserve := min(5*time.Second, limit/2)
	localReserve := min(2*time.Second, (limit-waitReserve)/3)
	stopDeadline := started.Add(limit)
	localDeadline := stopDeadline.Add(-waitReserve)
	remoteDeadline := localDeadline.Add(-localReserve)

	localCtx, localCancel := context.WithDeadline(context.Background(), localDeadline)
	defer localCancel()
	remoteCtx, remoteCancel := context.WithDeadline(localCtx, remoteDeadline)
	defer remoteCancel()

	if !holdingWrite {
		if d.writeMu.TryLock() {
			holdingWrite = true
			defer d.writeMu.Unlock()
		}
	}
	d.pendMu.Lock()
	d.pendRun = false
	has := len(d.pendSends) > 0
	d.pendMu.Unlock()
	if has {
		d.giveUpPending(localCtx, remoteCtx, errDaemonStopped, true, holdingWrite)
	}
}

// pendingLoop pushes the tip of this device's log again while a send is pending under it, waiting
// longer each time, to a minute, until it lands or is given up.
func (d *Daemon) pendingLoop() {
	defer d.bg.Done()
	// Record when stop begins so an interrupted retry's unwind counts against PendingStopLimit.
	var stopAt atomic.Int64
	stopWatch := context.AfterFunc(d.bgCtx, func() {
		stopAt.Store(time.Now().UnixNano())
	})
	defer stopWatch()
	stopStart := func() time.Time {
		if ns := stopAt.Load(); ns != 0 {
			return time.Unix(0, ns)
		}
		return time.Now()
	}

	wait := d.opt.PendingRetry
	for {
		select {
		case <-d.bgCtx.Done():
			d.stopPending(stopStart(), false)
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Minute)
		if !d.lockWriteUntil(d.bgCtx) {
			d.stopPending(stopStart(), false)
			return
		}
		if d.hasPending() && d.bgCtx.Err() == nil {
			d.retryPending()
		}
		d.pendMu.Lock()
		empty := len(d.pendSends) == 0
		if empty {
			d.pendRun = false
		}
		d.pendMu.Unlock()
		if empty {
			d.writeMu.Unlock()
			return
		}
		if d.bgCtx.Err() != nil {
			d.stopPending(stopStart(), true)
			d.writeMu.Unlock()
			return
		}
		d.writeMu.Unlock()
	}
}

// retryPending pushes the tip of this device's log, with the sends pending under it, again, and
// gives them up once they have been tried for PendingGiveUp, or can't be pushed at all. Caller
// holds writeMu.
func (d *Daemon) retryPending() {
	// One push's time in all, as a send waiting on writeMu meanwhile has 90 s (serve.go).
	ctx, cancel := context.WithTimeout(d.bgCtx, pushTimeout)
	defer cancel()
	if d.dropSwitchedOff(ctx); !d.hasPending() {
		return
	}
	ref, sha := d.meshRef(), d.ownTip
	err := d.pushFrom(ctx, d.repo, sha, ref, bus.FastForward, "", d.pendingStill)
	if errors.Is(err, errSendOff) {
		d.dropSwitchedOff(ctx) // Send files went off as the push waited its turn
		return
	}
	if err == nil {
		d.log.Printf("a message whose push had ended unsure has landed")
		d.ownLanded(ctx, ref, sha, false)
		return
	}
	if errors.Is(err, bus.ErrRejected) {
		// The remote's tip isn't under this one: an older copy of the log, or junk, to push over,
		// as appendEventIf does; or a log that went on without the pending sends.
		if tip, rerr := d.repo.RemoteRef(ctx, ref); rerr == nil {
			if over, oerr := d.overRemoteTip(ctx, ref, tip); oerr == nil {
				if !over {
					d.settlePendingOn(ctx, tip, err, true)
					if !d.hasPending() {
						d.ownTip = tip
					}
					return
				}
				if err = d.pushFrom(ctx, d.repo, sha, ref, bus.Lease, tip, d.pendingStill); err == nil {
					d.ownLanded(ctx, ref, sha, false)
					return
				}
			}
		}
	}
	if d.bgCtx.Err() != nil {
		return // stopping: pendingLoop settles against the remote under its stop bound
	}
	if errors.Is(err, ErrNoConsent) || errors.Is(err, ErrInvisible) || time.Since(d.pendFrom) >= d.opt.PendingGiveUp {
		rctx, rcancel := context.WithTimeout(ctx, pendingLookLimit)
		defer rcancel()
		d.giveUpPending(ctx, rctx, err, false, true)
	}
}

// settlePendingOn settles pending sends by tip, the remote's tip of this device's log, which the
// next write builds on: those under it landed, and the rest are given up, as nothing will push
// them. Every send's ancestry is decided before any settle callback runs so a stop triggered in an
// earlier callback can't cancel later checks; when leaveOnStop is true and a stop cancels ctx
// mid-check, undecided sends stay pending for stopPending to settle under its budget (#857).
// Caller has tip locally.
func (d *Daemon) settlePendingOn(ctx context.Context, tip string, err error, leaveOnStop bool) {
	d.pendMu.Lock()
	snap := slices.Clone(d.pendSends)
	d.pendMu.Unlock()
	if len(snap) == 0 {
		return
	}
	type verdict struct {
		landed, absent bool
	}
	verdicts := make(map[string]verdict, len(snap))
	for _, p := range snap {
		ok, aerr := d.repo.IsAncestor(ctx, p.sha, tip)
		if aerr != nil && leaveOnStop && ctx.Err() != nil && d.bgCtx != nil && d.bgCtx.Err() != nil {
			break
		}
		verdicts[p.sha] = verdict{landed: aerr == nil && ok, absent: aerr == nil}
	}
	if len(verdicts) == 0 {
		return
	}
	d.pendMu.Lock()
	var settled, keep []pendingSend
	for _, p := range d.pendSends {
		if _, ok := verdicts[p.sha]; ok {
			settled = append(settled, p)
		} else {
			keep = append(keep, p)
		}
	}
	d.pendSends = keep
	d.pendMu.Unlock()
	for _, p := range settled {
		v := verdicts[p.sha]
		if v.landed {
			p.settle(true, false, nil)
		} else {
			p.settle(false, v.absent, err)
		}
	}
}

// giveUpPending asks the remote (bounded by remoteCtx) which pending sends landed, settles those as
// landed, and gives up the rest after err; when holdingWrite is true, the next write builds on the
// remote's tip. Local git checks run under localCtx. If the remote can't be asked, times out, or
// points to junk, every pending send is given up as unconfirmed (absent=false) and (when
// holdingWrite) the next write builds on the tip the first was built on — except when !forStop and
// the daemon began stopping during the lookup, in which case the sends are left pending for
// stopPending to settle under its own budget (#857).
func (d *Daemon) giveUpPending(localCtx, remoteCtx context.Context, err error, forStop, holdingWrite bool) {
	ref := d.meshRef()
	if remoteCtx.Err() == nil {
		if tip, rerr := d.repo.RemoteRef(remoteCtx, ref); rerr == nil {
			if tip == "" { // no log there at all
				pending := d.takePending()
				if len(pending) > 0 {
					d.log.Printf("giving up %d message(s) whose push ended unsure: %v", len(pending), err)
				}
				for _, p := range pending {
					p.settle(false, true, err)
				}
				if holdingWrite {
					d.ownTip = ""
				}
				return
			}
			// A tip that is already a commit here (the usual case when this clone's own push landed
			// or didn't) needs no second round-trip to fetch. One that isn't is fetched within
			// remoteCtx; a tip that still isn't a commit, junk pushed over the log, is no tip to
			// build on (Sol's review of #818): that is as if the remote couldn't be asked.
			ok, _ := d.repo.IsCommit(localCtx, tip)
			if !ok && remoteCtx.Err() == nil {
				var ferr error
				if holdingWrite {
					ferr = d.fetchOwn(remoteCtx, ref)
				} else {
					ferr = d.repo.Fetch(remoteCtx, []string{ref})
				}
				if ferr != nil && remoteCtx.Err() != nil && localCtx.Err() == nil {
					clearStaleTemp(d.opt.GitDir, d.log)
				}
				if ferr == nil {
					ok, _ = d.repo.IsCommit(localCtx, tip)
				}
			}
			if ok {
				if !forStop && d.bgCtx != nil && d.bgCtx.Err() != nil {
					return // stopping mid-lookup: leave pending for stopPending under its stop budget
				}
				d.settlePendingOn(localCtx, tip, err, !forStop)
				if holdingWrite && !d.hasPending() {
					d.ownTip = tip
				}
				return
			}
		}
	}
	if !forStop && d.bgCtx != nil && d.bgCtx.Err() != nil {
		return // stopping mid-lookup: leave pending for stopPending under its stop budget
	}
	pending := d.takePending()
	if len(pending) > 0 {
		d.log.Printf("giving up %d message(s) whose push ended unsure: %v", len(pending), err)
	}
	for _, p := range pending {
		p.settle(false, false, err)
	}
	if holdingWrite {
		d.ownTip = d.pendBase
	}
}

// errDaemonStopped is what a pending send is given up with as the daemon stops.
var errDaemonStopped = errors.New(brand.Product + " stopped before it landed")

// givenUpLocked tells from, in its inbox, that its message about subject to to was given up after
// err. It is called not sent, though it may land yet if the remote couldn't be asked: after minutes
// of failures that is the useful thing to say (Tyler, 2026-11-16). The notice comes from this
// device's receipts address, as a tell_me notice does, and never starts a turn. Caller holds mu and
// saves the state.
func (d *Daemon) givenUpLocked(from, subject, to string, err error) {
	addr := d.queuedAtLocked(from)
	dev, sess := bus.SplitAddr(addr)
	if dev != d.device || sess == "" {
		return
	}
	why, next := givenUpWhy(err)
	body := fmt.Sprintf("Your message to %s couldn't be pushed, and was given up: %s. %s", to, why, next)
	d.addInboxLocked(sess, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: bus.Addr(d.device, bus.ReceiptFrom), To: addr, At: time.Now().UTC(),
		Subject: fmt.Sprintf("Not sent: %q", subject), Body: body})
}

// givenUpWhy says, in a few plain words, why a pending send was given up, and what the sender
// should do next: git's own words, such as "context deadline exceeded: signal: killed", tell an
// agent nothing (Tyler, 2026-11-16). When git couldn't log in to the remote or access the
// repository, retrying won't help until the user fixes it (#858).
func givenUpWhy(err error) (why, next string) {
	next = "Send it again if it still matters."
	switch {
	case errors.Is(err, errDaemonStopped):
		return err.Error(), next
	case errors.Is(err, errSendOff):
		return "sending files was turned off", next
	case errors.Is(err, ErrNoConsent):
		return "this clone left " + brand.Product, next
	case errors.Is(err, ErrInvisible):
		return "this device went invisible", next
	case errors.Is(err, bus.ErrRejected), errors.Is(err, bus.ErrRefused):
		return "the remote refused it", next
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "the push kept timing out", next
	case err != nil && errorClass(err, errorText(err), false) == web.ErrAuth:
		return "git couldn't log in to the remote or access the repository", "Ask your user to check their git login and access before sending again."
	}
	return "the push kept failing", next
}

// pendingStill says every pending send may still go out: no Send files switch one's files need has
// gone off since it was sent. Caller holds writeMu.
func (d *Daemon) pendingStill() bool {
	d.pendMu.Lock()
	defer d.pendMu.Unlock()
	return !slices.ContainsFunc(d.pendSends, func(p pendingSend) bool { return p.still != nil && !p.still() })
}

// dropSwitchedOff takes out of this device's log the first pending send whose Send files switch has
// gone off, and every one after it, which are built on it, and gives them up: nothing pushed after
// may carry it to the remote, its files' keys with it (Sol's review of rc.2). The log's next write
// builds on what came before it. Those the remote has already, as a push that ended unsure may
// have landed, are settled as landed first, so none is called not sent that went (Sol's re-review).
// Caller holds writeMu.
func (d *Daemon) dropSwitchedOff(ctx context.Context) {
	off := func(p pendingSend) bool { return p.still != nil && !p.still() }
	d.pendMu.Lock()
	snap := slices.Clone(d.pendSends)
	d.pendMu.Unlock()
	if !slices.ContainsFunc(snap, off) {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ref := d.meshRef()
	if tip, err := d.repo.RemoteRef(lctx, ref); err == nil && tip != "" && d.repo.Fetch(lctx, []string{ref}) == nil {
		landed := -1
		for k, p := range snap {
			if ok, _ := d.repo.IsAncestor(lctx, p.sha, tip); ok {
				landed = k
			}
		}
		if landed >= 0 {
			d.pendMu.Lock()
			var done []pendingSend
			if len(d.pendSends) > landed {
				done = d.pendSends[:landed+1]
				d.pendBase, d.pendSends = d.pendSends[landed].sha, slices.Clone(d.pendSends[landed+1:])
				if len(d.pendSends) == 0 {
					d.ownTip = d.pendBase
				}
			}
			d.pendMu.Unlock()
			for _, p := range done {
				p.settle(true, false, nil)
			}
		}
	}
	d.pendMu.Lock()
	i := slices.IndexFunc(d.pendSends, off)
	if i < 0 {
		d.pendMu.Unlock()
		return
	}
	dropped := slices.Clone(d.pendSends[i:])
	if i == 0 {
		d.ownTip, d.pendSends = d.pendBase, nil
	} else {
		d.ownTip, d.pendSends = d.pendSends[i-1].sha, d.pendSends[:i]
	}
	d.pendMu.Unlock()
	d.log.Printf("giving up %d message(s) whose push ended unsure: sending files went off", len(dropped))
	for _, p := range dropped {
		p.settle(false, false, errSendOff)
	}
}
