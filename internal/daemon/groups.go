package daemon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// Group chats (#184, docs/design/tags.md, "Groups"). A group is a tag that starts with group:, its
// members are the sessions that have the tag, and the tag is the group's address. A session joins
// one by setting the tag, as it sets any tag, and leaves by taking it off. An agent can also add
// other sessions to a group, which no other tag allows: the add goes to each session's device as a
// group.add event on the adder's log, and that device makes the tag the session's own.
//
// Nothing holds a group's members but the sessions' tags. A message to a group goes out addressed
// to the tag, and each daemon gives it to those of its sessions that have the tag when it reads the
// message (addressedLocked), so no sender's view of who is in can be out of date.

// maxGroupAdd caps the sessions one call adds to groups: each is an event, and a push.
const maxGroupAdd = 12

// groupAddMaxAge is how old an add can be, by its sender's clock, and still be applied. An add is
// read late when its log waited for a team key, or was caught up on after a restart, and the
// session may have left the group since: an add carries no order against the session's own
// changes, so one from long ago is taken as done with.
const groupAddMaxAge = time.Hour

// groupMembersLocked is who a message to group would reach, as far as this device can see, less the
// session at except: the names (or addresses) of its sessions here and in live peers' presence that
// have the tag. old lists those of them on a device whose daemon is from before groups, which won't
// give them the message. Caller holds mu.
func (d *Daemon) groupMembersLocked(group, except string) (members, old []string) {
	for _, s := range d.sortedSessionsLocked() {
		addr := bus.Addr(d.device, s.id)
		if addr != except && !d.offMeshLocked(s.id) && tags.Has(d.tagsOfLocked(s), group) {
			members = append(members, nameOr(s.name, addr))
		}
	}
	for _, dev := range sortedKeys(d.peers) {
		p := d.peers[dev]
		if d.staleLocked(p) {
			continue
		}
		for _, s := range p.pres.Sessions {
			if !tags.Has(s.Tags, group) {
				continue
			}
			who := nameOr(s.Name, bus.Addr(dev, s.ID))
			members = append(members, who)
			if !p.pres.Groups {
				old = append(old, who)
			}
		}
	}
	return members, old
}

// groupNoteLocked is what the sender at from is told of a message to group: who it reaches, who it
// can't, and whether the group's answers will reach the sender. Caller holds mu.
func (d *Daemon) groupNoteLocked(group, from string) string {
	members, old := d.groupMembersLocked(group, from)
	_, sess := bus.SplitAddr(from)
	var parts []string
	if len(members) == 0 {
		parts = append(parts, fmt.Sprintf("Nobody else is in %s right now, so nobody may read this. "+
			"A session joins a group by adding its tag with the tags tool, and you can add sessions to one with that tool's for.", group))
	} else {
		parts = append(parts, fmt.Sprintf("%s has %d other member(s) right now: %s.", group, len(members), strings.Join(members, ", ")))
	}
	if len(old) > 0 {
		parts = append(parts, fmt.Sprintf("%s won't get it: that device's %s is from before groups, and gives its sessions nothing sent to one. Send to them by name.",
			strings.Join(old, ", "), brand.Product))
	}
	if s := d.sessions[sess]; s != nil && !tags.Has(d.tagsOfLocked(s), group) {
		parts = append(parts, fmt.Sprintf("You aren't in %s, so answers sent to the group won't reach you: add %s to your tags to follow it.", group, group))
	}
	return strings.Join(parts, " ")
}

// addToGroups is OpTags with For: session id adds the sessions named there to the groups in Add.
// Each add goes out as a group.add event, which the session's device applies when it reads it,
// this device included. The caller is added too only if For names it.
func (d *Daemon) addToGroups(ctx context.Context, id string, req proto.Request) proto.Response {
	from := bus.Addr(d.device, id)
	if len(req.Remove) > 0 {
		return fail("you can add other sessions to a group, not take them out: a member leaves by removing the tag from itself, and a human can remove it on the page")
	}
	groups, err := tags.Groups(req.Add)
	if err != nil {
		return fail(err.Error())
	}
	if len(groups) == 0 {
		return fail("name the group to add them to, such as " + tags.GroupPrefix + "ui")
	}
	if len(req.For) > maxGroupAdd {
		return fail(fmt.Sprintf("at most %d sessions at a time", maxGroupAdd))
	}
	d.awaitFirstSync(ctx) // the names it resolves, and its pushes, as send
	d.mu.Lock()
	s := d.sessions[id]
	if s == nil {
		d.mu.Unlock()
		return fail("the CLI has no tags, and adds nobody to a group")
	}
	var self, far bool
	var addrs, shown []string
	for _, raw := range req.For {
		who := strings.TrimSpace(raw)
		addr := who
		if names.Valid(who) {
			a, ok := d.resolveNameLocked(who)
			if !ok {
				why := d.noSuchNameLocked(who)
				d.mu.Unlock()
				return fail("nobody added: " + why)
			}
			addr = d.queuedAtLocked(a)
		}
		dev, sess := bus.SplitAddr(addr)
		var why string
		switch {
		case !strings.Contains(addr, "/") || !bus.ValidDevice(dev) || !bus.ValidName(sess):
			why = fmt.Sprintf("%q isn't a session: give each as its name from who, or its device/session address", raw)
		case addr == from:
			self = true
			continue
		case bus.IsPerson(addr) || sess == proto.CLISession:
			why = who + " isn't an agent's session, and only those are in groups"
		case d.offlineLocked(dev, sess):
			why = d.offlineWhyLocked(dev, sess, who)
		case !d.addressKnownLocked(dev, sess):
			why = who + " isn't in any device's presence right now, so it can't be added. Check `who`."
		case dev != d.device && !d.peers[dev].pres.Groups:
			why = fmt.Sprintf("%s is on a device whose %s is from before groups, so it can't be added to one until that device updates", who, brand.Product)
		case d.hasMutedLocked(dev, sess, from):
			// Its device wouldn't apply the add (applyGroupAddLocked); the adder learns of the mute
			// here, as a send to it does.
			why = who + " has muted you, so you can't add it to a group"
		default:
			why = d.groupAddWhyLocked(dev, sess, who, groups)
			if dev != d.device {
				far = true
			}
		}
		if why != "" {
			d.mu.Unlock()
			return fail("nobody added: " + why)
		}
		if !slices.Contains(addrs, addr) {
			addrs, shown = append(addrs, addr), append(shown, who)
		}
	}
	d.mu.Unlock()

	var notes []string
	if self {
		r := d.setOwnTags(id, proto.Request{Add: groups})
		if r.Error != "" {
			return r
		}
		if r.Note != "tags set" {
			notes = append(notes, r.Note+".")
		}
	}
	list := strings.Join(groups, ", ")
	for i, addr := range addrs {
		e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.GroupAdd, From: from, At: time.Now().UTC(), To: addr, Add: groups}
		if err := d.appendEvent(ctx, e); err != nil {
			done := "nobody else"
			if i > 0 {
				done = strings.Join(shown[:i], ", ")
			}
			return fail(fmt.Sprintf("adding %s to %s failed (%s was added): %v", shown[i], list, done, err))
		}
	}
	if len(addrs) > 0 {
		notes = append(notes, fmt.Sprintf("Added %s to %s. Each is in it once its device reads this, usually within seconds, "+
			"and learns of it from the group's first message: send to %s to reach them all.", strings.Join(shown, ", "), list, groups[0]))
		if far {
			// What groupAddWhyLocked can't see from presence (#524, #543).
			notes = append(notes, fmt.Sprintf("Another device can still refuse the add, for a session a person took out of the group on the page "+
				"or one with no room for another tag: who with %s shows who is in.", groups[0]))
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var mine []tags.Tag
	if s := d.sessions[id]; s != nil {
		mine = d.tagsOfLocked(s)
	}
	for _, g := range groups {
		if !tags.Has(mine, g) {
			notes = append(notes, fmt.Sprintf("You aren't in %s yourself, so what is sent to it won't reach you.", g))
		}
	}
	return proto.Response{OK: true, Self: from, Note: strings.TrimSuffix(strings.Join(notes, " "), "."), Tags: mine}
}

// applyGroupAddLocked applies an agent's add of a session here to groups, read in a sync of this
// mode: the tags become the session's own, so it can take them off again, and a human's mark still
// stands over them. An add older than groupAddMaxAge isn't applied, and from history a skip passes
// over, only one sent since the session started, as a message from there would be queued for it.
// Caller holds mu.
func (d *Daemon) applyGroupAddLocked(e bus.Event, mode syncMode) {
	dev, sess := bus.SplitAddr(e.To)
	if dev != d.device {
		return
	}
	ago := time.Since(e.At)
	if old := ago > groupAddMaxAge; old || mode == modeSkip && !d.sentSinceLocked(e, sess, mode) {
		// The add's time is by its sender's clock and the age by this one's, so a clock far off
		// on either side shows in the line (#543). One refused in a skip may be younger than
		// groupAddMaxAge, or ahead of this clock, so the line says which rule refused it.
		why := "too long ago to apply"
		if !old {
			why = "read after a long gap and not shown to be sent since the session started; not applied"
		}
		d.log.Printf("%s added %s to %s at %s by its clock, %s ago by this device's, %s",
			e.From, e.To, strings.Join(e.Add, ", "), e.At.Format(time.RFC3339), ago.Round(time.Second), why)
		return
	}
	key := d.tagKeyLocked(sess)
	if key == "" && d.holdForLocked(e) {
		return
	}
	if key == "" || d.offMeshLocked(sess) {
		d.log.Printf("%s added %s to %s, but that session isn't known here; not applied", e.From, e.To, strings.Join(e.Add, ", "))
		return
	}
	// A session the target has muted can't put it in a group (#483): what it sent there would be
	// dropped anyway, and each add would use up one of the target's tags. From the first moment,
	// with no grace: nobody is told an add arrived.
	if _, muted := d.mutedAtLocked(sess, e.From); muted {
		d.log.Printf("%s added %s to %s, but that session has muted it; not applied", e.From, e.To, strings.Join(e.Add, ", "))
		return
	}
	next := d.st.Tags[key].Clone()
	given, out, err := next.Given(e.Add)
	if err != nil {
		d.log.Printf("%s added %s to %s; not applied: %v", e.From, e.To, strings.Join(e.Add, ", "), err)
		return
	}
	if len(out) > 0 {
		d.log.Printf("%s added %s to %s, but a person took that session out of it; not applied", e.From, e.To, strings.Join(out, ", "))
	}
	if len(given) == 0 {
		return
	}
	d.putTagStateLocked(key, next)
	d.kickPresence()
	d.log.Printf("%s gave session %s the tags %s", e.From, e.To, strings.Join(given, ", "))
}

// groupAddWhyLocked is why the session at dev/sess, shown as who, can't be added to groups, as far
// as this device can tell, or "" if it can't tell. The session's own device decides, by
// applyGroupAddLocked. For a session here that is the same rule on the same state, so it refuses
// every add its device would refuse or leave a group out of. An add of several groups is refused
// whole when one would be left out, though the device would give the rest: an add is all or
// nothing, and the reply names the group, so the adder can add the rest again. For one on another
// device it is that device's presence, which shows the tags the session set itself but not those a
// person turned off, nor a person's choice that it be out of a group: what the add is sure to fail
// on is refused, and the reply says the rest may be (#524, #543). Caller holds mu.
func (d *Daemon) groupAddWhyLocked(dev, sess, who string, groups []string) string {
	full := func(own, off int) string {
		has := fmt.Sprintf("%s has %d tags of its own", who, own)
		if off > 0 {
			has += fmt.Sprintf(", %d of them taken off by a person on the page but still counted", off)
		}
		return fmt.Sprintf("%s, and a session has at most %d, so it has no room for more: it can drop one, or join the group itself", has, tags.MaxPerSession)
	}
	if dev == d.device {
		var st *tags.State
		if t := d.sessions[sess]; t != nil {
			st = d.tagStateLocked(t)
		}
		next := st.Clone()
		_, out, err := next.Given(groups)
		switch {
		case len(out) > 0:
			return fmt.Sprintf("a person took %s out of %s on the page, and only a person can put it back", who, strings.Join(out, ", "))
		case err != nil:
			// Self holds those a person turned off too: they aren't shown, and still count.
			off := 0
			for _, t := range next.Self {
				if m, ok := next.Human[t]; ok && !m.On {
					off++
				}
			}
			return full(len(next.Self), off)
		}
		return ""
	}
	p := d.peers[dev]
	// The add's time is by this device's clock, and its device drops one more than groupAddMaxAge
	// old by its own. lastChange is this clock when the presence was read, after it was written. The
	// writer's clock then is the earlier of two times: pres.At can be pinned ahead of it, as a device
	// whose clock went back signs just after its last (nextPresenceAtLocked), and the commit's time,
	// which git stamps from the clock as it writes, can't be. So the gap is at most how far that
	// clock runs ahead of this one, and over groupAddMaxAge the add is sure to be dropped. Anyone who
	// can push could wrap the presence in a commit dated otherwise, but pres.At is in the seal, so
	// that can only make the gap smaller, and miss a refusal rather than make one. The commit's time
	// is pinned only by a GIT_COMMITTER_DATE in the daemon's environment, and is then a false
	// refusal only with pres.At pinned ahead too.
	at := p.pres.At
	if p.written.Before(at) {
		at = p.written
	}
	if ahead := at.Sub(p.lastChange); ahead > groupAddMaxAge {
		return fmt.Sprintf("%s's device's clock runs at least %d minutes ahead of this one's, so it would take the add as over an hour old and drop it: one of the two clocks is wrong",
			who, int(ahead/time.Minute))
	}
	// The tags the session is sure to hold as its own are those presence shows as its own, and the
	// groups it would gain those it isn't shown in. A tag from a person or the daemon isn't its own,
	// and doesn't count.
	has := peerTagsLocked(p, sess)
	own := 0
	for _, t := range has {
		if t.Src == tags.SrcSession {
			own++
		}
	}
	n := own
	for _, g := range groups {
		if !tags.Has(has, g) {
			n++
		}
	}
	if n > tags.MaxPerSession {
		return full(own, 0)
	}
	return ""
}
