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
// as a send that landed, with nothing added (Tyler, 2026-11-16). It goes as the daemon stops, too:
// nothing is kept across a restart. The remote is asked first then as well, briefly
// (pendingStopLook), so that a send it has isn't called not sent (#857); one it couldn't be asked
// about is on the remote or not as its last push left it.

// pendingGiveUp is how long after its first push ended unsure a pending send is given up, at the
// first retry to end past it. Tyler set it
// (2026-11-16): 30 minutes was too long for a sender to believe a message went that hadn't. With
// the backoff that is about six pushes again.
const pendingGiveUp = 3 * time.Minute

// pendingLook bounds asking the remote which pending sends it has, as they are given up or dropped.
// retryPending keeps it back from the push before it, so that writeMu is held no longer than
// before the asking had time of its own: a push's time and pushLook, 75 s (but for a clone that
// left or a device gone invisible, whose waiting sends are refused anyway). A send waiting on
// writeMu has 90 s (serve.go), and its own push, cut short by a longer wait, would more often end
// unsure, which the person's send from the page is told at once as not sent.
const pendingLook = 20 * time.Second

// pendingStopLook is how long the same asking may take as the daemon stops, and pendingStopBy how
// long after the stop began it may still start; later than that, the sends are given up unasked,
// as they were before #857. The stop waits up to DrainTimeout, 20 s, for its background work, this
// with it, and then releases the clone's lock. An asking begun by 5 s is over by 15 s: its 5 s, and
// 5 s more that a git cut off at its time may take to end (gitx's WaitDelay), and the milliseconds
// of a fetch's last local steps, which run past its time. So the notices it leaves are saved before
// the lock goes, and the next daemon, which an update starts at once, doesn't have its state
// written over. The 5 s is short because sends are pending mostly when the network is down, and a
// stop then shouldn't wait long for an answer that won't come: such a stop takes up to 10 s more
// than it did, within the 35 s any stop may take.
const (
	pendingStopLook = 5 * time.Second
	pendingStopBy   = 5 * time.Second
)

// pushLook is how long a push that failed may go on past its time, asking the remote where the ref
// is (bus.Repo.Push).
const pushLook = 15 * time.Second

// settleFunc is told once how a pending send ended: landed; or given up, with absent saying the
// remote, asked, doesn't have it, and err why. It is called with writeMu held, and nothing else.
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

// pendLocked leaves sha, the commit of a send whose push ended with err, unsure, pending: the
// writes after it build on it, and pendingLoop pushes it again. It says false, and leaves nothing
// pending, if the daemon is stopping and no pendingLoop runs to push it or give it up: the send then
// fails unsure, as before #781 (Sol's review of #818). Caller holds writeMu.
func (d *Daemon) pendLocked(sha string, settle settleFunc, still func() bool, err error) bool {
	if !d.pendRun && (d.bgCtx == nil || d.bgCtx.Err() != nil) {
		return false
	}
	if len(d.pendSends) == 0 {
		d.pendBase, d.pendFrom = d.ownTip, time.Now()
	}
	d.pendSends = append(d.pendSends, pendingSend{sha, settle, still})
	d.ownTip = sha
	d.log.Printf("the push of a message ended unsure (%v); it is pushed again until it lands", err)
	if !d.pendRun {
		d.pendRun = true
		d.bg.Add(1)
		go d.pendingLoop()
	}
	return true
}

// pendingLoop pushes the tip of this device's log again while a send is pending under it, waiting
// longer each time, to a minute, until it lands or is given up.
func (d *Daemon) pendingLoop() {
	defer d.bg.Done()
	// When the stop began, as time since this loop did, so a clock that is set meanwhile changes
	// nothing: the loop may learn of the stop late, from inside a retry that it cut off.
	began := time.Now()
	var stopped atomic.Int64
	defer context.AfterFunc(d.bgCtx, func() { stopped.Store(max(1, int64(time.Since(began)))) })()
	wait := d.opt.PendingRetry
	for {
		select {
		case <-d.bgCtx.Done():
			d.writeMu.Lock()
			d.pendRun = false
			if len(d.pendSends) > 0 {
				at := time.Duration(stopped.Load()) // 0 if it stopped this instant, and isn't noted yet
				d.giveUpPending(errDaemonStopped, at == 0 || time.Since(began)-at <= pendingStopBy)
			}
			d.writeMu.Unlock()
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Minute)
		d.writeMu.Lock()
		if len(d.pendSends) > 0 {
			d.retryPending()
		}
		if len(d.pendSends) == 0 {
			d.pendRun = false
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
	// One push's time in all, as a send waiting on writeMu meanwhile has 90 s (serve.go). Every
	// retry reserves pendingLook of PendingPush (when PendingPush > pendingLook) for asking the
	// remote, whether reconciling a rejected push or checking at give-up. A retry that may end past
	// the give-up time is the last, and only it gives the sends up for their age. A last retry that
	// ends before the give-up time, as one whose push hung from 110 s to 165 s does, leaves them to
	// the next: a retry with a whole minute's push would have ended past the time and given them up
	// there, so the give-up can come one retry later than it did, about two minutes at the most.
	start, limit := time.Now(), d.opt.PendingPush
	last := time.Since(d.pendFrom)+limit+pushLook >= d.opt.PendingGiveUp
	if left := limit - pendingLook; left > 0 {
		limit = left
	}
	// room says the remote can still be asked within the push's time. A test's short push time
	// keeps none back.
	room := func() bool {
		return d.opt.PendingPush <= pendingLook || time.Since(start)+pendingLook <= d.opt.PendingPush
	}
	ctx, cancel := context.WithTimeout(d.bgCtx, limit)
	defer cancel()
	if d.dropSwitchedOff(d.bgCtx); len(d.pendSends) == 0 {
		return
	}
	ref, sha := d.meshRef(), d.ownTip
	err := d.pushFrom(ctx, d.repo, sha, ref, bus.FastForward, "", d.pendingStill)
	if errors.Is(err, errSendOff) {
		// Send files went off as the push waited its turn. If the wait left no time to ask the
		// remote, the next retry drops them as it starts; nothing is pushed until then.
		if room() {
			d.dropSwitchedOff(d.bgCtx)
		}
		return
	}
	if err == nil {
		d.log.Printf("a message whose push had ended unsure has landed")
		d.ownLanded(ctx, ref, sha, false)
		return
	}
	lookBudget := pendingLook
	if errors.Is(err, bus.ErrRejected) {
		// The remote's tip isn't under this one: an older copy of the log, or junk, to push over,
		// as appendEventIf does; or a log that went on without the pending sends. Give this look
		// its own time (pendingLook, reserved above), not what is left of the push's: if the push
		// was rejected just before its limit, an expired ctx during IsAncestor would miss landed
		// sends (#1073). Share that reserved budget with give-up below so a failed lookup never
		// adds a second pendingLook past a queued send's 90 s wait on writeMu; if a lease push runs
		// and ends unsure, give-up gets whatever remains of PendingPush + pushLook to check whether
		// that lease push landed.
		lookBudget = 0
		lctx, lcancel := context.WithTimeout(d.bgCtx, pendingLook)
		defer lcancel()
		if tip, rerr := d.repo.RemoteRef(lctx, ref); rerr == nil {
			if over, oerr := d.overRemoteTip(lctx, ref, tip); oerr == nil {
				if !over {
					if d.settlePendingOn(lctx, tip, err, false) {
						d.ownTip = tip
						return
					}
					// Unchecked sends stay pending, unless give-up below says time is up.
				} else {
					err = d.pushFrom(ctx, d.repo, sha, ref, bus.Lease, tip, d.pendingStill)
					if err == nil {
						d.ownLanded(ctx, ref, sha, false)
						return
					}
					if d.opt.PendingPush <= pendingLook {
						lookBudget = pendingLook
					} else {
						lookBudget = min(pendingLook, max(0, d.opt.PendingPush+pushLook-time.Since(start)))
					}
				}
			}
		}
	}
	// A clone that left, or a device gone invisible, pushes nothing more, and its sends are given up
	// now, however long the wait for the push's turn was: a clone that left stops within a second,
	// and a stop asks the remote only in its first seconds. A send waiting on writeMu meanwhile is
	// refused for the same reason before git starts, so the longer hold costs it nothing.
	gone := errors.Is(err, ErrNoConsent) || errors.Is(err, ErrInvisible)
	if d.bgCtx.Err() != nil {
		return // the daemon is stopping: pendingLoop gives them up, which knows how long it has
	}
	if gone || last && time.Since(d.pendFrom) >= d.opt.PendingGiveUp {
		d.giveUpPendingWith(err, lookBudget)
	}
}

// settlePendingOn settles the pending sends by tip, the remote's tip of this device's log, which
// the next write builds on: those under it landed, and the rest are given up, as nothing will push
// them. When giveUp is false (reconciling a rejected push, not giving up for good), an IsAncestor
// error settles only the confirmed landed prefix and leaves unchecked sends pending, returning
// false so the caller doesn't advance ownTip over them (#1073). Caller holds writeMu and has
// fetched tip.
func (d *Daemon) settlePendingOn(ctx context.Context, tip string, err error, giveUp bool) bool {
	if len(d.pendSends) == 0 {
		return true
	}
	if giveUp {
		pending := d.pendSends
		d.pendSends = nil
		for _, p := range pending {
			if ok, aerr := d.repo.IsAncestor(ctx, p.sha, tip); aerr == nil && ok {
				p.settle(true, false, nil)
			} else {
				p.settle(false, aerr == nil, err)
			}
		}
		return true
	}
	landed := -1
	for k, p := range d.pendSends {
		ok, aerr := d.repo.IsAncestor(ctx, p.sha, tip)
		if aerr != nil {
			if landed >= 0 {
				done := d.pendSends[:landed+1]
				d.pendBase, d.pendSends = d.pendSends[landed].sha, slices.Clone(d.pendSends[landed+1:])
				for _, s := range done {
					s.settle(true, false, nil)
				}
			}
			return false
		}
		if ok {
			landed = k
		}
	}
	pending := d.pendSends
	d.pendSends = nil
	for k, p := range pending {
		if k <= landed {
			p.settle(true, false, nil)
		} else {
			p.settle(false, true, err)
		}
	}
	return true
}

// giveUpPending gives up the pending sends, after err. With look, the remote is asked first, and
// those it has are taken as landed, and the next write builds on its tip; without, or if it can't
// be asked, on the tip the first was built on. Caller holds writeMu.
//
// The asking has its own time, pendingLook, or pendingStopLook as the daemon stops, and not what is
// left of the push's before it: a push that timed out has none left, and the remote was then never
// asked, so a send that an earlier push had landed was called not sent, and went twice if its
// sender sent it again (#951).
func (d *Daemon) giveUpPending(err error, look bool) {
	if look {
		d.giveUpPendingWith(err, pendingLook)
		return
	}
	d.giveUpPendingWith(err, 0)
}

func (d *Daemon) giveUpPendingWith(err error, look time.Duration) {
	d.log.Printf("giving up %d message(s) whose push ended unsure: %v", len(d.pendSends), err)
	ref := d.meshRef()
	if look > 0 {
		// A stopping daemon's context is done, and would fail the asking at once: it asks in a short
		// time of its own (#857).
		from, limit := d.bgCtx, look
		if from.Err() != nil {
			from, limit = context.Background(), min(look, pendingStopLook)
		}
		lctx, cancel := context.WithTimeout(from, limit)
		defer cancel()
		// A tip that isn't a commit, junk pushed over the log, is no tip to build on (Sol's review
		// of #818): that is as if the remote couldn't be asked.
		//
		// A tip this clone has already, such as a pending send's own commit, isn't fetched: over the
		// slow link that timed the push out, the fetch could outlast pendingLook.
		isCommit := func(tip string) bool { ok, _ := d.repo.IsCommit(lctx, tip); return ok }
		if tip, rerr := d.repo.RemoteRef(lctx, ref); rerr == nil && (tip == "" || isCommit(tip) || d.fetchOne(lctx, ref) == nil && isCommit(tip)) {
			if tip == "" { // no log there at all
				for _, p := range d.pendSends {
					p.settle(false, true, err)
				}
				d.pendSends, d.ownTip = nil, ""
				return
			}
			d.settlePendingOn(lctx, tip, err, true)
			d.ownTip = tip
			return
		}
	}
	for _, p := range d.pendSends {
		p.settle(false, false, err)
	}
	d.pendSends, d.ownTip = nil, d.pendBase
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
	return !slices.ContainsFunc(d.pendSends, func(p pendingSend) bool { return p.still != nil && !p.still() })
}

// dropSwitchedOff takes out of this device's log the first pending send whose Send files switch has
// gone off, and every one after it, which are built on it, and gives them up: nothing pushed after
// may carry it to the remote, its files' keys with it (Sol's review of rc.2). The log's next write
// builds on what came before it. Those the remote has already, as a push that ended unsure may
// have landed, are settled as landed first, so none is called not sent that went (Sol's re-review).
// The remote is asked in time of its own (pendingLook), bounded by both the daemon's context and
// the caller's ctx so a send doesn't hold writeMu past its own deadline (#1073). If ctx ends before
// the check completes, the sends are left for retryPending rather than dropped unchecked. Caller
// holds writeMu.
func (d *Daemon) dropSwitchedOff(ctx context.Context) {
	off := func(p pendingSend) bool { return p.still != nil && !p.still() }
	if !slices.ContainsFunc(d.pendSends, off) {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	lctx, cancel := context.WithTimeout(d.bgCtx, pendingLook)
	defer cancel()
	if ctx != nil && ctx != d.bgCtx {
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
	}
	ref := d.meshRef()
	if tip, err := d.repo.RemoteRef(lctx, ref); err == nil && tip != "" && d.fetchOne(lctx, ref) == nil {
		landed := -1
		for k, p := range d.pendSends {
			if ok, _ := d.repo.IsAncestor(lctx, p.sha, tip); ok {
				landed = k
			}
		}
		if landed >= 0 {
			done := d.pendSends[:landed+1]
			d.pendBase, d.pendSends = d.pendSends[landed].sha, slices.Clone(d.pendSends[landed+1:])
			if len(d.pendSends) == 0 {
				d.ownTip = d.pendBase
			}
			for _, p := range done {
				p.settle(true, false, nil)
			}
		}
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	i := slices.IndexFunc(d.pendSends, off)
	if i < 0 {
		return
	}
	d.log.Printf("giving up %d message(s) whose push ended unsure: sending files went off", len(d.pendSends)-i)
	dropped := d.pendSends[i:]
	if i == 0 {
		d.ownTip, d.pendSends = d.pendBase, nil
	} else {
		d.ownTip, d.pendSends = d.pendSends[i-1].sha, d.pendSends[:i]
	}
	for _, p := range dropped {
		p.settle(false, false, errSendOff)
	}
}
