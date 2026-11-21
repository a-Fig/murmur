package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Mutes (#27, docs/design/mutes.md). A session mutes another by its name, and from then on
// gets nothing the other sends: not a message to it, to its device, or to everyone. Its own device
// decides, by not queueing the message, and publishes the mutes in presence, so they are on the
// bus for anyone on the team to see, a human on the page among them. The muted session isn't
// told when it happens. It learns of it when it next sends to the session that muted it: its own
// daemon, which has read that presence, refuses the send and says why. That is the tool's answer,
// from Murmur and no agent, so it starts no turn anywhere.
//
// Mutes are kept as tags are, by the muting session's name, which stays the same while its
// conversation does, and name the muted session by its name, which the registry never gives out
// twice: a mute can't land on another session.

// mutesOfLocked is the names session id of this device has muted, in order. Caller holds mu.
func (d *Daemon) mutesOfLocked(id string) []string {
	return sortedKeys(d.st.Mutes[d.tagKeyLocked(id)])
}

// adoptMutesLocked moves the mutes a session made while its name was pending to the name, once
// it lands. Where the name has mutes already, from before a resume, they are added to those:
// tags kept by the ID give way to the name's (adoptTagsLocked), but a mute dropped lets through
// what the session asked not to get (#483). The name's own stay as they are, and no more are
// added than a session may have. Caller holds mu.
func (d *Daemon) adoptMutesLocked(s *session) {
	pending := tagKeyOf("", s.id)
	m := d.st.Mutes[pending]
	if s.name == "" || m == nil {
		return
	}
	delete(d.st.Mutes, pending)
	have := d.st.Mutes[s.name]
	if have == nil {
		d.st.Mutes[s.name] = m
	} else {
		for _, name := range sortedKeys(m) {
			if _, ok := have[name]; ok {
				continue
			}
			if len(have) >= bus.MaxMuted {
				d.log.Printf("session %s muted %s before its name %s landed, and the name has %d mutes already; not kept",
					bus.Addr(d.device, s.id), name, s.name, len(have))
				continue
			}
			have[name] = m[name]
		}
	}
	d.saveLocked()
}

// nameOfLocked is the name of the session at addr: the one it has in presence, else the one shown
// for it when it has none there (lastNameLocked). "" for an address with none, as the CLI's or a
// person's. Caller holds mu.
func (d *Daemon) nameOfLocked(addr string) string {
	if bus.IsPerson(addr) {
		return ""
	}
	if name, _, _ := d.sessionInfoLocked(addr); name != "" {
		return name
	}
	return d.lastNameLocked(addr)
}

type peerNameKey struct{ dev, name string }

// nameGivenUpLocked reports whether addr is a session currently listed without a name whose name
// has moved to another session on the same device: a session that gave its name up and waits for
// a new one (#875). Neither its label nor a mute of that name follows it once another session on
// its device holds the name. A session that has left presence is not one: its old messages still
// go by the name its conversation resumed under. Caller holds mu.
func (d *Daemon) nameGivenUpLocked(addr, name string) bool {
	if name == "" {
		return false
	}
	dev, id := bus.SplitAddr(addr)
	if dev == "" || id == "" || id == bus.PersonSession {
		return false
	}
	if dev == d.device {
		s := d.sessions[id]
		if s == nil || s.name != "" {
			return false
		}
		if h := d.st.NameHolders[name]; h != "" && h != id {
			return true
		}
		for _, o := range d.sessions {
			if o.id != id && o.dropped.IsZero() && o.name == name {
				return true
			}
		}
		return false
	}
	p := d.peers[dev]
	if p == nil {
		return false
	}
	unnamed, heldByOther := false, false
	for _, s := range p.pres.Sessions {
		if s.ID == bus.PersonSession {
			continue
		}
		if s.ID == id {
			if s.Name == "" {
				unnamed = true
			}
		} else if s.Name == name {
			heldByOther = true
		}
	}
	if !unnamed {
		return false
	}
	if heldByOther {
		return true
	}
	h := d.peerHolders[peerNameKey{dev, name}]
	return h != "" && h != id
}

// lastNameLocked is the name a session is shown by when it has none in presence: it has left, or
// its new name hasn't landed yet (a follow to a new conversation, as after /clear, leaves it with
// none until the name is pushed). It is the same every time and in every place: the inbox,
// activity, lookup and the mute checks (#502), so the name an agent replies to or mutes is the one
// it was shown. One of this device's goes by the name it took last (SessionNames), which this
// device knows; for one waiting on a new name, that is its previous one, unless another session on
// this device holds that name now (#875). Another device's goes by the name its presence last
// showed it by (PeerNames, #602), or, never seen there, the first by spelling of the names the
// registry gave its address, as the registry keeps no order of them — again leaving out a name
// that has moved to another session on the same device while this one waits unnamed (#875).
// namesByAddrLocked gives the same for every address at once. "" for an address with none. Caller
// holds mu.
func (d *Daemon) lastNameLocked(addr string) string {
	dev, id := bus.SplitAddr(addr)
	if dev == d.device && d.st.SessionNames[id] != "" {
		if name := d.st.SessionNames[id]; !d.nameGivenUpLocked(addr, name) {
			return name
		}
		return ""
	}
	// Another device's, by the name its presence last showed it by: the name its own device shows
	// it by, so every device agrees (#602).
	if dev != d.device && d.st.PeerNames[addr].Name != "" {
		if name := d.st.PeerNames[addr].Name; !d.nameGivenUpLocked(addr, name) {
			return name
		}
		return ""
	}
	if names := d.heldNamesLocked(addr); len(names) > 0 {
		return names[0]
	}
	return ""
}

// heldNamesLocked is every name the session at addr is known to have held, sorted by spelling:
// those the registry gave the address, and for one of this device's, the name it took last, which
// the registry may have down for another session ID (a conversation resumed under a new one). An
// address holds more than one when its client started a new conversation in the same session
// (Claude Code's /clear). A name that has moved to another session on the same device while addr
// is listed without one is left out (#875). Caller holds mu.
func (d *Daemon) heldNamesLocked(addr string) []string {
	var names []string
	for name, a := range d.st.Used {
		if a == addr && !d.nameGivenUpLocked(addr, name) {
			names = append(names, name)
		}
	}
	if dev, id := bus.SplitAddr(addr); dev == d.device {
		if name := d.st.SessionNames[id]; name != "" && !d.nameGivenUpLocked(addr, name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// mutedAtLocked says session id of this device has muted whoever is at addr, and when. A sender
// in presence goes by the name it has now. One that isn't goes by every name its address has
// held: which of them it had when it sent can't be known, and a map's order must not decide
// (#483), so a mute of any of them holds. The cost is a conversation nobody muted, at an address
// whose earlier conversation was: what it sends is dropped if it has left presence by the time
// this device reads it, and its sender is told it was muted (#466, docs/design/mutes.md). Caller
// holds mu.
func (d *Daemon) mutedAtLocked(id, addr string) (at time.Time, muted bool) {
	return d.mutedAtKeyLocked(d.tagKeyLocked(id), addr)
}

// mutedAtKeyLocked is mutedAtLocked for the session whose mutes are kept under key (tagKeyOf).
// Caller holds mu.
func (d *Daemon) mutedAtKeyLocked(key, addr string) (at time.Time, muted bool) {
	m := d.st.Mutes[key]
	if len(m) == 0 || bus.IsPerson(addr) {
		return at, false
	}
	if name, _, _ := d.sessionInfoLocked(addr); name != "" {
		at, muted = m[name]
		return at, muted
	}
	// One here whose new name is pending is still the session muted under its old one (#827).
	if old := d.leftNameLocked(addr); old != "" {
		if at, muted = m[old]; muted {
			return at, true
		}
	}
	for _, name := range d.heldNamesLocked(addr) {
		// The latest mute of them, so the grace is that one's.
		if t, ok := m[name]; ok && (!muted || t.After(at)) {
			at, muted = t, true
		}
	}
	return at, muted
}

// mutedHereLocked says session id of this device has muted whoever sent e, so e isn't queued for
// it.
//
// A message sent to the session itself still reaches it for MuteGrace after the mute (#466,
// Tyler, 2026-11-11). The sender's daemon refuses such a message once it has read of the mute in
// presence, which takes seconds; one sent before that would otherwise be dropped here with its
// sender sure it had arrived. The grace is longer than a presence takes to be pushed and read,
// and goes by this device's clock alone: when the mute was made, and now. After it, the mute
// holds against every sender, whatever its daemon knows or its version. A message to everyone or
// to the device is dropped from the first moment: its sender is never refused, so there is
// nothing for a grace to keep true. Caller holds mu.
func (d *Daemon) mutedHereLocked(id string, e bus.Event) bool {
	return d.mutedHereKeyLocked(d.tagKeyLocked(id), e)
}

// mutedHereKeyLocked is mutedHereLocked for the session whose mutes are kept under key (tagKeyOf).
// Caller holds mu.
func (d *Daemon) mutedHereKeyLocked(key string, e bus.Event) bool {
	at, muted := d.mutedAtKeyLocked(key, e.From)
	if !muted {
		return false
	}
	direct := strings.Contains(e.To, "/")
	return !direct || time.Since(at) >= d.opt.MuteGrace
}

// leftNameLocked is the name the session at addr had before this clone lost its keys, if it is
// one of this device's whose new name is pending (NamesLeft, #827), and "" otherwise. The
// sessions here that muted it under that name have still muted it. Caller holds mu.
func (d *Daemon) leftNameLocked(addr string) string {
	dev, id := bus.SplitAddr(addr)
	if s := d.sessions[id]; dev == d.device && s != nil && s.name == "" && s.key != "" {
		return d.st.NamesLeft[s.key]
	}
	return ""
}

// muteNamesLocked is the names a mute of the session at addr may be under, as a receiving device
// tells (mutedAtLocked): the name it has in presence, or, with none, as after /clear before its
// new name lands, every name its address has held. A sender is refused, or told who didn't get its
// message, by these, so it isn't sent a message the receiver then drops unseen (#602). Caller
// holds mu.
//
// here says the receiving device is this one, whose mutedAtLocked holds the name its device took
// last (SessionNames) against the address, and, for a session here whose new name is pending
// after its clone lost its keys, the name it had before (leftNameLocked, #827). Another device
// holds only the names the registry gave it, so a name the registry has given another address
// since, as a resume does, is left out for one there: a refusal by it would be wrong (Opus's
// reviews of #840). No other device's mute of the old name follows the session either.
func (d *Daemon) muteNamesLocked(addr string, here bool) []string {
	if bus.IsPerson(addr) {
		return nil
	}
	if name, _, _ := d.sessionInfoLocked(addr); name != "" {
		return []string{name}
	}
	names := d.heldNamesLocked(addr)
	if here {
		if old := d.leftNameLocked(addr); old != "" && !slices.Contains(names, old) {
			names = append(names, old)
		}
		return names
	}
	return slices.DeleteFunc(names, func(n string) bool {
		a := d.st.Used[n]
		return a != "" && a != addr
	})
}

// hasMutedLocked says the session at dev/sess, here or in another device's presence, has muted
// the session at from, by the names that device holds against it (muteNamesLocked). Caller holds
// mu.
func (d *Daemon) hasMutedLocked(dev, sess, from string) bool {
	names := d.muteNamesLocked(from, dev == d.device)
	if len(names) == 0 || sess == "" {
		return false
	}
	if dev == d.device {
		m := d.st.Mutes[d.tagKeyLocked(sess)]
		return slices.ContainsFunc(names, func(n string) bool { _, ok := m[n]; return ok })
	}
	if p := d.peers[dev]; p != nil {
		for _, s := range p.pres.Sessions {
			if s.ID == sess {
				return slices.ContainsFunc(names, func(n string) bool { return slices.Contains(s.Muted, n) })
			}
		}
	}
	return false
}

// mutersLocked lists, by name, the sessions that have muted the session at from (hasMutedLocked):
// on device dev, or on every device this daemon knows if dev is "". Caller holds mu.
func (d *Daemon) mutersLocked(from, dev string) []string {
	var out []string
	if dev == "" || dev == d.device {
		for _, s := range d.sortedSessionsLocked() {
			if d.hasMutedLocked(d.device, s.id, from) {
				out = append(out, nameOr(s.name, bus.Addr(d.device, s.id)))
			}
		}
	}
	for _, id := range sortedKeys(d.peers) {
		if dev != "" && dev != id {
			continue
		}
		for _, s := range d.peers[id].pres.Sessions {
			if d.hasMutedLocked(id, s.ID, from) {
				out = append(out, nameOr(s.Name, bus.Addr(id, s.ID)))
			}
		}
	}
	return out
}

type peerName struct {
	Name string    `json:"name"`
	At   time.Time `json:"at"` // to the hour
}

// notePeerNameLocked records name as the one another device's session at addr is shown by, and
// lets go of those not seen for sessionNameKeep. It says whether the state changed. Caller holds
// mu.
func (d *Daemon) notePeerNameLocked(addr, name string) bool {
	if name == "" {
		return false
	}
	now := time.Now().UTC().Truncate(time.Hour)
	if pn, ok := d.st.PeerNames[addr]; ok && pn.Name == name && pn.At.Equal(now) {
		return false
	}
	if d.st.PeerNames == nil {
		d.st.PeerNames = map[string]peerName{}
	}
	for a, pn := range d.st.PeerNames {
		if now.Sub(pn.At) > sessionNameKeep {
			delete(d.st.PeerNames, a)
		}
	}
	d.st.PeerNames[addr] = peerName{name, now}
	return true
}

// mutersNote tells the sender of a message to many which of them didn't get it, "" if all did.
func mutersNote(muters []string) string {
	switch len(muters) {
	case 0:
		return ""
	case 1:
		return muters[0] + " did not get this message: they have muted you."
	}
	return strings.Join(muters[:len(muters)-1], ", ") + " and " + muters[len(muters)-1] +
		" did not get this message: they have muted you."
}

// mute is OpMute: session id mutes the session called req.Name, or with Unmute undoes that. With
// no name it only lists whom it has muted.
func (d *Daemon) mute(id string, req proto.Request) proto.Response {
	from := bus.Addr(d.device, id)
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil {
		return fail("the CLI mutes nobody: a mute is a session's")
	}
	key := d.keptByLocked(s)
	name := req.Name
	var note string
	switch {
	case name == "":
	case !names.Valid(name):
		return fail(fmt.Sprintf("%q isn't a session's name. A mute takes a name from who, like quiet-otter; a person can't be muted.", name))
	case req.Unmute:
		if _, muted := d.st.Mutes[key][name]; !muted {
			return fail("you haven't muted " + name)
		}
		d.dropMuteLocked(key, name)
		d.log.Printf("session %s unmuted %s", from, name)
		note = "You have unmuted " + name + ". What it sends from now on will reach you."
	case name == s.name || name == key: // key: the name it had before this clone lost its keys
		return fail("that is your own name")
	default:
		if _, ok := d.resolveNameLocked(name); !ok {
			return fail(d.noSuchNameLocked(name))
		}
		if _, muted := d.st.Mutes[key][name]; !muted && len(d.st.Mutes[key]) >= bus.MaxMuted {
			return fail(fmt.Sprintf("you have muted %d sessions, which is the most a session can; unmute one first", bus.MaxMuted))
		}
		if d.st.Mutes == nil {
			d.st.Mutes = map[string]map[string]time.Time{}
		}
		if d.st.Mutes[key] == nil {
			d.st.Mutes[key] = map[string]time.Time{}
		}
		if _, muted := d.st.Mutes[key][name]; !muted {
			d.st.Mutes[key][name] = time.Now().UTC()
			d.saveLocked()
			d.kickPresence()
			d.log.Printf("session %s muted %s", from, name)
		}
		// The words are Tyler's (2026-11-11): the first version also said who is told and who can
		// undo it, which was more than the caller needs.
		note = "You have muted " + name + ". Nothing it sends will reach you, until you unmute it."
	}
	return proto.Response{OK: true, Self: from, Note: note, Muted: sortedKeys(d.st.Mutes[key])}
}

// dropMuteLocked takes the mute of name off the session whose mutes are kept by key. Caller
// holds mu.
func (d *Daemon) dropMuteLocked(key, name string) {
	delete(d.st.Mutes[key], name)
	if len(d.st.Mutes[key]) == 0 {
		delete(d.st.Mutes, key)
	}
	d.saveLocked()
	d.kickPresence()
}

// muteViewsLocked is every mute this daemon knows, for the page: this device's sessions', which
// its human can undo, then those in other devices' presences. Caller holds mu.
func (d *Daemon) muteViewsLocked() []web.Mute {
	out := []web.Mute{}
	for _, by := range sortedKeys(d.st.Mutes) {
		if !names.Valid(by) {
			continue // a session whose name is pending: the page goes by names
		}
		for _, muted := range sortedKeys(d.st.Mutes[by]) {
			out = append(out, web.Mute{By: by, Muted: muted, At: d.st.Mutes[by][muted], CanUnmute: true})
		}
	}
	for _, dev := range sortedKeys(d.peers) {
		for _, s := range d.peers[dev].pres.Sessions {
			for _, muted := range s.Muted {
				if s.Name != "" {
					out = append(out, web.Mute{By: s.Name, Muted: muted})
				}
			}
		}
	}
	return out
}

// Unmute is the page's: this device's human undoes the mute by the session called by, one of
// this device's, of the session called muted. Another device's mutes are its own human's to undo.
func (s pageSource) Unmute(ctx context.Context, by, muted string) error {
	d := s.d
	d.pageCalled(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.st.Mutes[by][muted]; !ok || !names.Valid(by) {
		return errors.New(by + " hasn't muted " + muted + " on this device")
	}
	d.dropMuteLocked(by, muted)
	d.log.Printf("the page unmuted %s for session %s", muted, by)
	return nil
}
