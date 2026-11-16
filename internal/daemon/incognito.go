package daemon

import (
	"context"
	"errors"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// Incognito (#681, Tyler's design, 2026-11-15) takes this device off Murmur in one repo for a
// while, as if it were offline, and turning it off brings the device back as it was.
//
//   - Teammates: turning it on deletes the device's presence, as a goodbye does, so other devices
//     drop it at their next sync rather than see it frozen at a last sighting. Nothing more is
//     written to the bus (push refuses), and its sessions' starts and stops aren't kept to be
//     written later, which would tell afterwards when they worked.
//   - Its agents: every Murmur tool says Murmur is off here and how to turn it back on, the hook
//     gives no notices, and no message is handed over as a turn. Messages that came before the
//     switch, or come after it to a name someone remembers, wait in the inbox until incognito is
//     off: nothing is lost.
//   - The person: the daemon keeps fetching the bus and serving the page, which shows the team
//     without this device's agents, and sends nothing.
//
// It is a page setting, kept in settings.json, so it holds across restarts.

// ErrIncognito is what a write to the bus ends with while incognito is on.
var ErrIncognito = errors.New("this device is incognito in this repo, so it writes nothing to " + brand.Product)

// incognitoText is what an agent's Murmur tool says while incognito is on.
var incognitoText = brand.Product + " is off for this repo on this device: its person turned on incognito. " +
	"Nothing can be seen or sent until they turn it off, in Settings on `" + brand.Command + " ui`."

// incognitoAnswer is the daemon's answer to an agent's request while incognito is on, and whether
// it is one: a tool call is refused, and a turn's end is answered with nothing and changes nothing
// the daemon keeps. The hook's notices are answered in notices, once the hook is seen to run.
// A registration, a ping, a stop and the CLI's own requests go on.
func (d *Daemon) incognitoAnswer(req proto.Request) (proto.Response, bool) {
	if !d.hidden.Load() {
		return proto.Response{}, false
	}
	switch {
	case toolOps[req.Op]:
		return fail(incognitoText), true
	case req.Op == proto.OpTurnEnd:
		return proto.Response{OK: true}, true
	}
	return proto.Response{}, false
}

// setIncognito acts on the setting's change, once settings.json has it.
func (d *Daemon) setIncognito(on bool) {
	d.hidden.Store(on)
	if on {
		d.log.Printf("incognito: on, so this device leaves %s and writes nothing", brand.Product)
		d.presenceGone.Store(false)
	} else {
		d.log.Printf("incognito: off, so this device is back")
		d.kickNames() // what waited, without its own backoff to sit out
		d.kickMarks()
	}
	d.kickPresence() // the presence loop deletes the presence, or pushes it again
}

// hidePresence deletes this device's presence once after incognito was turned on, in turn with the
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
		return nil // turned off again meanwhile
	}
	if err := d.repo.Push(ctx, "", d.presenceRef(), bus.FastForward, ""); err != nil {
		return err
	}
	d.presenceGone.Store(true)
	return nil
}
