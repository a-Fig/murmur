package daemon

import (
	"cmp"
	"context"
	"errors"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// The device's mode in one repo (#681, Tyler's design, 2026-11-15), set on the page's Settings and
// kept in settings.json, so it holds across restarts. docs/design/modes.md has the whole of it.
//
//   - Online is Murmur as it is otherwise.
//   - Do not disturb keeps the device and its agents in sight, but no message reaches the agents:
//     every session carries the do-not-disturb tag while it lasts (derivedTagsLocked), which holds
//     its messages in the inbox with no turn and no notice, and tells their senders why. Neither a
//     human's mark nor the tag limit can take it off a session (hasTagLocked).
//   - Invisible takes the device off Murmur, as if it were offline. Its presence is deleted, as a
//     goodbye deletes it, and nothing more is written to the bus (push refuses), nor are its
//     sessions' starts and stops kept to be written later. Its agents' Murmur tools say Murmur is
//     off here, the hook gives no notices, and no message is handed over as a turn: they wait in
//     the inbox until the device is back. The daemon keeps fetching the bus, and the page shows the
//     team without this device's agents, and sends nothing.
//
// Going back online releases what arrived since the device left online, through either mode, as
// taking the tag off does.

// ErrInvisible is what a write to the bus ends with while the device is invisible.
var ErrInvisible = errors.New("this device is invisible in this repo, so it writes nothing to " + brand.Product)

// invisibleText is what an agent's Murmur tool says while the device is invisible.
var invisibleText = brand.Product + " is off for this repo on this device: its person made it invisible. " +
	"Nothing can be seen or sent until they bring it back online, in Settings on `" + brand.Command + " ui`."

// invisibleAnswer is the daemon's answer to an agent's request while the device is invisible, and
// whether it is one: a tool call is refused. The hook's notices and a turn's end are answered in
// notices and turnEnd, which see the hook run and the turn end, so the session is idle when the
// device is back, and give nothing (Sol's review of v0.5.0: a turn's end answered here left the
// session at work for good). A registration, a ping, a stop and the CLI's own requests go on.
func (d *Daemon) invisibleAnswer(req proto.Request) (proto.Response, bool) {
	if !d.hidden.Load() {
		return proto.Response{}, false
	}
	if toolOps[req.Op] {
		return fail(invisibleText), true
	}
	return proto.Response{}, false
}

// modeView is the device's mode, as the page is told it.
func (d *Daemon) modeView() string {
	switch {
	case d.hidden.Load():
		return web.ModeInvisible
	case d.dnd.Load():
		return web.ModeDND
	}
	return web.ModeOnline
}

// setMode acts on the mode's change from was to now, once settings.json has it. away is when the
// device last left online.
func (d *Daemon) setMode(was, now string, away time.Time) {
	was, now = cmp.Or(was, web.ModeOnline), cmp.Or(now, web.ModeOnline)
	if was == web.ModeInvisible && now != web.ModeInvisible {
		// Its colour is settled by a sync begun from here on: one already under way may have listed
		// the remote before a teammate picked the colour (settleColorLocked). Stamped before the
		// device stops being hidden, which is what holds such a sync off until then.
		d.mu.Lock()
		d.colorAwayAt = time.Now()
		d.mu.Unlock()
	}
	d.hidden.Store(now == web.ModeInvisible)
	d.dnd.Store(now == web.ModeDND)
	d.log.Printf("mode: %s", now)
	if now == web.ModeInvisible {
		d.presenceGone.Store(false)
		// Its presence goes, so its colour looks free, as a stopped daemon's does: back online, it
		// settles the colour again, and gives it up to whoever picked it meanwhile (#1084).
		d.mu.Lock()
		d.colorSettled = false
		d.mu.Unlock()
	}
	if was == web.ModeInvisible {
		d.kickNames() // what waited, without its own backoff to sit out
		d.kickMarks()
		d.kickFiles() // for the prune of files refs that was passed over meanwhile (pruneFiles)
	}
	if now == web.ModeOnline {
		d.mu.Lock()
		d.releaseSinceLocked(away)
		d.mu.Unlock()
	}
	d.kickPresence() // the presence loop deletes the presence, or pushes it as it now is
	d.kickPush()
}

// releaseSinceLocked lets go what arrived in any inbox since away, as releaseLocked does for one
// session's: each counts as arriving now. So does what a session's own do not disturb held, if
// that came off since away (ownDNDLocked). Anything else from before the device left online keeps
// the fate it had, and a session still on do not disturb by its own tag keeps holding its own.
// Caller holds mu.
func (d *Daemon) releaseSinceLocked(away time.Time) {
	now := time.Now()
	for id, box := range d.st.Inbox {
		if d.dndLocked(id) {
			continue
		}
		for _, m := range box {
			if !m.Noticed && !m.Pushing && !m.Attempted && !waitedSince(m).Before(away) {
				m.Released, m.PushFailed = now, false
			}
		}
	}
	d.saveLocked()
	d.kickPush()
}

// hidePresence deletes this device's presence once after it became invisible, in turn with the
// device's other pushes and past push's refusal, as the goodbye does.
func (d *Daemon) hidePresence(ctx context.Context) error {
	if d.presenceGone.Load() {
		return nil
	}
	if !d.takePush(ctx) {
		return ctx.Err()
	}
	defer d.donePush()
	if !d.hidden.Load() {
		return nil // back online meanwhile
	}
	if err := d.repo.Push(ctx, "", d.presenceRef(), bus.FastForward, ""); err != nil {
		return err
	}
	d.presenceGone.Store(true)
	return nil
}
