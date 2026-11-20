package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
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
// as a send that landed, with nothing added (Tyler, 2026-11-16). It goes as the daemon stops, too,
// and is then on the remote or not as its last push left it: nothing is kept across a restart.

// pendingGiveUp is how long after its first push ended unsure a pending send is given up, at the
// first retry to end past it. Tyler set it
// (2026-11-16): 30 minutes was too long for a sender to believe a message went that hadn't. With
// the backoff that is about six pushes again.
const pendingGiveUp = 3 * time.Minute

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
	wait := d.opt.PendingRetry
	for {
		select {
		case <-d.bgCtx.Done():
			d.writeMu.Lock()
			d.pendRun = false
			if len(d.pendSends) > 0 {
				d.giveUpPending(context.Background(), errDaemonStopped, false)
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
	// One push's time in all, as a send waiting on writeMu meanwhile has 90 s (serve.go).
	ctx, cancel := context.WithTimeout(d.bgCtx, pushTimeout)
	defer cancel()
	if d.dropSwitchedOff(ctx); len(d.pendSends) == 0 {
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
					d.settlePendingOn(ctx, tip, err)
					d.ownTip = tip
					return
				}
				if err = d.pushFrom(ctx, d.repo, sha, ref, bus.Lease, tip, d.pendingStill); err == nil {
					d.ownLanded(ctx, ref, sha, false)
					return
				}
			}
		}
	}
	if errors.Is(err, ErrNoConsent) || errors.Is(err, ErrInvisible) || time.Since(d.pendFrom) >= d.opt.PendingGiveUp {
		d.giveUpPending(ctx, err, true)
	}
}

// settlePendingOn settles the pending sends by tip, the remote's tip of this device's log, which
// the next write builds on: those under it landed, and the rest are given up, as nothing will push
// them. Caller holds writeMu and has fetched tip.
func (d *Daemon) settlePendingOn(ctx context.Context, tip string, err error) {
	if len(d.pendSends) == 0 {
		return
	}
	pending := d.pendSends
	d.pendSends = nil
	for _, p := range pending {
		if ok, aerr := d.repo.IsAncestor(ctx, p.sha, tip); aerr == nil && ok {
			p.settle(true, false, nil)
		} else {
			p.settle(false, aerr == nil, err)
		}
	}
}

// giveUpPending gives up the pending sends, after err. With look, the remote is asked first, and
// those it has are taken as landed, and the next write builds on its tip; without, or if it can't
// be asked, on the tip the first was built on. Caller holds writeMu.
func (d *Daemon) giveUpPending(ctx context.Context, err error, look bool) {
	d.log.Printf("giving up %d message(s) whose push ended unsure: %v", len(d.pendSends), err)
	ref := d.meshRef()
	if look {
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// A tip that isn't a commit, junk pushed over the log, is no tip to build on (Sol's review
		// of #818): that is as if the remote couldn't be asked.
		if tip, rerr := d.repo.RemoteRef(lctx, ref); rerr == nil && (tip == "" || d.repo.Fetch(lctx, []string{ref}) == nil && func() bool { ok, _ := d.repo.IsCommit(lctx, tip); return ok }()) {
			if tip == "" { // no log there at all
				for _, p := range d.pendSends {
					p.settle(false, true, err)
				}
				d.pendSends, d.ownTip = nil, ""
				return
			}
			d.settlePendingOn(lctx, tip, err)
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
// agent nothing (Tyler, 2026-11-16). When the remote refused git credentials, retrying won't help
// until the user fixes their login (#858).
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
	case isAuthError(err):
		return "the remote refused git credentials", "Ask your user to fix their git login before sending again."
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
// Caller holds writeMu.
func (d *Daemon) dropSwitchedOff(ctx context.Context) {
	off := func(p pendingSend) bool { return p.still != nil && !p.still() }
	if !slices.ContainsFunc(d.pendSends, off) {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ref := d.meshRef()
	if tip, err := d.repo.RemoteRef(lctx, ref); err == nil && tip != "" && d.repo.Fetch(lctx, []string{ref}) == nil {
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
