package daemon

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

// A device's colour (#573). The page fills every face on a device with that device's colour
// (#560). Its user can pick one on the page's Settings, from the palette's bus.Colors, as long as
// no other device holds it; presence carries the pick, inside the box, and every daemon settles
// who holds what by one rule (bus.DeviceColors), so every teammate's page shows the same colours.
// A device on a Murmur from before this sends no pick, and its colour is chosen for it.
// docs/design/presence.md ("Device colours") has the reasons.
//
// A pick that loses is given up, not kept: the device goes back to Auto, and its person is told
// who has the colour (st.ColorLostTo). It loses a race by the rule, and it loses on coming back
// to a colour picked while it was away: a daemon that stops for being idle takes its presence off
// the remote, so its colour looked free. Until its first full sync it leaves its pick out of its
// presence, and then gives it up if a peer has picked it meanwhile. Kept, a lost pick would come
// back each time the holder's daemon went idle, and two devices would swap colours day by day.

// colorHold is how long a device's pick holds its colour after its presence last changed. A daemon
// that was killed, or a clone that was abandoned, leaves its presence on the remote for good, and
// its colour could never be picked again (#581). Staleness itself, minutes, doesn't let a colour
// go: it is each reader's own clock, and two pages a few minutes apart would show different
// colours for a device that only slept. A week is long enough that the pages disagree only about
// a device that has been gone that long, and for the moment it takes each to pass the week.
const colorHold = 7 * 24 * time.Hour

// colorGoneLocked says p's presence went unchanged so long that its pick no longer holds its
// colour, as of this daemon's last successful sync, as staleLocked judges. Caller holds mu.
func (d *Daemon) colorGoneLocked(p *peer) bool {
	return !d.lastPollOK.IsZero() && d.lastPollOK.Sub(p.lastChange) > cmp.Or(d.opt.ColorHold, colorHold)
}

// colorsLocked is every device's colour, this one's included, by the devices in presence. A
// device whose presence is stale still holds its colour, until it has been gone for colorHold:
// then it is shown as though it had picked none. Caller holds mu.
func (d *Daemon) colorsLocked() map[string]bus.DeviceColor {
	color, at := d.shownColorLocked() // as the peers see it
	picks := []bus.ColorPick{{Device: d.device, Color: color, At: at}}
	for id, p := range d.peers {
		pick := bus.ColorPick{Device: id}
		if !d.colorGoneLocked(p) {
			pick.Color, pick.At = p.pres.Color, p.pres.ColorAt
		}
		picks = append(picks, pick)
	}
	return bus.DeviceColors(picks)
}

// colorAwayLocked takes this device's pick out of its presence until the next full sync has
// settled it again, if its last presence, signed at last, is older than colorHold: its teammates
// let the colour go meanwhile, and one may have picked it. A daemon that restarts starts unsettled;
// this is for one that slept through the week, as on a laptop closed that long. Caller holds mu.
func (d *Daemon) colorAwayLocked(last time.Time) {
	if d.colorSettled && !last.IsZero() && time.Since(last) > cmp.Or(d.opt.ColorHold, colorHold) {
		d.colorSettled = false
	}
}

// shownColorLocked is the pick this device's presence carries: none until its first full sync
// has settled it. Caller holds mu.
func (d *Daemon) shownColorLocked() (int, time.Time) {
	if !d.colorSettled {
		return 0, time.Time{}
	}
	return d.st.Color, d.st.ColorAt
}

// settleColorLocked gives up this device's pick if a peer holds that colour: one that picked it
// first, or, at the first full sync, one that picked it while this device was away. Called after
// each full sync. The pick stays out of presence while any peer's is locked (no team key for it
// yet), since it could hide the holder; a lost pick is still given up meanwhile. Caller holds mu.
func (d *Daemon) settleColorLocked() {
	if d.st.Color != 0 {
		for _, id := range sortedKeys(d.peers) {
			p := d.peers[id].pres
			if p.Color != d.st.Color || d.colorGoneLocked(d.peers[id]) {
				continue // another colour, or a device gone so long its pick holds none
			}
			if !d.colorSettled || p.ColorAt.Before(d.st.ColorAt) || p.ColorAt.Equal(d.st.ColorAt) && id < d.device {
				d.st.Color, d.st.ColorAt, d.st.ColorLostTo = 0, time.Time{}, d.deviceCalledLocked(id)
				d.saveLocked()
				d.kickPresence()
				break
			}
		}
	}
	for _, p := range d.peers {
		// A live holder could be hiding in it: settled once every live presence can be read. A
		// stale one may never open (written before its device read a team key), and whether to
		// hold this device's own pick back is this device's call, so its clock may judge.
		if p.locked && !d.staleLocked(p) {
			return
		}
	}
	if !d.colorSettled {
		d.colorSettled = true
		if d.st.Color != 0 {
			d.kickPresence()
		}
	}
}

// deviceCalledLocked is what the page calls device id: its nickname or person's name, else its
// owner, else its ID. Caller holds mu.
func (d *Daemon) deviceCalledLocked(id string) string {
	owner := ""
	if p := d.peers[id]; p != nil {
		owner = p.pres.Owner
	}
	return cmp.Or(d.nickOfLocked(id), owner, id)
}

// SetColor is the page picking this device's colour, 1 to bus.Colors, or with 0 letting it be
// chosen. A colour another device holds is refused. It waits for the first sync, as a nickname
// does: before it, this daemon knows no other device's colour.
func (s pageSource) SetColor(ctx context.Context, color int) (web.Account, error) {
	d := s.d
	d.mu.Lock()
	d.pageSeenLocked(ctx, time.Now())
	d.mu.Unlock()
	if color < 0 || color > bus.Colors {
		return web.Account{}, fmt.Errorf("there is no colour %d", color)
	}
	if color != 0 {
		d.awaitFirstSync(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if color != d.st.Color {
		for id, c := range d.colorsLocked() {
			if id != d.device && c.Picked && c.Color == color-1 {
				return web.Account{}, fmt.Errorf("%s has that colour", d.deviceCalledLocked(id))
			}
		}
		d.st.Color, d.st.ColorAt, d.st.ColorLostTo = color, time.Time{}, ""
		if color != 0 {
			d.st.ColorAt = time.Now().UTC()
		}
		d.saveLocked()
		d.kickPresence()
	} else if d.st.ColorLostTo != "" {
		d.st.ColorLostTo = "" // seen: picking Auto again takes the note away
		d.saveLocked()
	}
	return d.accountLocked(), nil
}
