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
// the remote, so its colour looked free, and so does a device that goes invisible. Until its first
// full sync, or its first after it is back online, it leaves its pick out of its presence, and
// then gives it up if a peer has picked it meanwhile. Kept, a lost pick would come back each time
// the holder's daemon went idle, and two devices would swap colours day by day.

// colorHold is how long a device's pick holds its colour after its presence last changed. A daemon
// that was killed, or a clone that was abandoned, leaves its presence on the remote for good, and
// its colour could never be picked again (#581). Staleness itself, minutes, doesn't let a colour
// go: it is each reader's own clock, and two pages a few minutes apart would show different
// colours for a device that only slept. A week is long enough that the pages disagree only about
// a device that has been gone that long, and for the time it takes each to pass the week: minutes
// between machines whose clocks agree.
const colorHold = 7 * 24 * time.Hour

// colorGoneLocked says p's presence went unchanged so long that its pick no longer holds its
// colour, as of this daemon's last successful sync. It is judged as staleLocked judges, but by the
// wall clock (Round strips the other reading): the time a machine spent asleep counts toward a
// week, where it must not toward staleness's minutes. Caller holds mu.
//
// A presence this daemon reads only after a long sleep of its own is dated from now, though its
// writer may have gone quiet a week before (updatePeer): one read whose writer's clock put it over
// colorHold ago, with nothing newer listed, holds no colour either. Otherwise the waking reader
// would show it holding its colour for another week, against teammates who let it go (#1165).
func (d *Daemon) colorGoneLocked(p *peer) bool {
	if d.lastPollOK.IsZero() {
		return false
	}
	hold, now := cmp.Or(d.opt.ColorHold, colorHold), d.lastPollOK.Round(0)
	return now.Sub(p.lastChange.Round(0)) > hold || p.writtenOf == p.listed && !p.written.IsZero() && now.Sub(p.written) > hold
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

// colorAwayLocked takes this device's pick out of its presence until a full sync begun after now
// has settled it again, if the last presence of its own to reach the remote was built longer ago
// than colorHold: its teammates let the colour go meanwhile, and one may have picked it. A daemon that
// restarts starts unsettled; this is for one that never stopped, and slept through the week on a
// laptop closed that long, or was awake and couldn't push. It is called before each presence is
// built. Caller holds mu.
func (d *Daemon) colorAwayLocked() {
	now := time.Now()
	if d.presenceLanded.IsZero() {
		d.presenceLanded = now.Round(0) // none yet: counted from the first one built
	}
	if d.colorSettled && now.Round(0).Sub(d.presenceLanded) > cmp.Or(d.opt.ColorHold, colorHold) {
		d.colorSettled, d.colorAwayAt = false, now
	}
}

// shownColorLocked is the pick this device's presence carries: none until its first full sync
// has settled it, and none while it is invisible or just back. The page shows this device's own
// faces by it too, so they are in an automatic colour meanwhile. Caller holds mu.
func (d *Daemon) shownColorLocked() (int, time.Time) {
	if !d.colorSettled {
		return 0, time.Time{}
	}
	return d.st.Color, d.st.ColorAt
}

// settleColorLocked gives up this device's pick if a peer holds that colour: one that picked it
// first, or, at the first full sync, one that picked it while this device was away. Called after
// each full sync, with when the sync began. The pick stays out of presence while any peer's is
// locked (no team key for it yet), since it could hide the holder; a lost pick is still given up
// meanwhile. Caller holds mu.
func (d *Daemon) settleColorLocked(began time.Time) {
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
		// hold this device's own pick back is this device's call, so its clock may judge. A
		// presence listed but not fetched (sawPresence) could hide one too: a teammate who picked
		// the colour while this device was away, whose newest presence this sync failed to fetch,
		// would lose the pick to this device's older one (#1165).
		if (p.locked || p.sha != p.listed) && !d.staleLocked(p) {
			return
		}
	}
	// Not while the device is invisible: it has no presence for the pick to go in, and a teammate
	// may pick the colour before it is back (#1084). And a sync that began before this device found
	// it had been away may have listed the remote before then, and knows nothing of a pick made
	// meanwhile: the next one settles.
	if !d.colorSettled && !d.hidden.Load() && (d.colorAwayAt.IsZero() || began.After(d.colorAwayAt)) {
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
