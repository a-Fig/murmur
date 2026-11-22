package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// Session tags (#198, docs/design/tags.md). A session's own device decides its tags and
// publishes them in presence: the tags the session set for itself, those the daemon derives (the
// model, from T3's thread), and a human's marks over both. A human on another device's page sends
// the change to this device as a tags event on their own log, and this device applies it. No
// shared ref is needed: one device is the only writer of its sessions' tags, and a human beats the
// session whatever order the two arrive in.
//
// The tags are kept by the session's name, which is what stays the same while a conversation does:
// its key may change under it (a session that finds its T3 thread takes the thread's key and keeps
// its name), and a stopped session's name is known from its ID (SessionNames). A session whose
// name is still pending keeps them by its ID until the name lands (see adoptTagsLocked). So they outlast
// its MCP server restarting, a resume, and the daemon restarting.

// tagKeyOf is what the tags of a session called name, with ID id, are kept by.
func tagKeyOf(name, id string) string {
	if name != "" {
		return name
	}
	return "id:" + id
}

// keptByLocked is what session s's tags and mutes are kept by: its name, or its ID while the
// name is pending (tagKeyOf). One back on a clone that lost its keys, whose new name is pending,
// goes by the name its key had on the old device (NamesLeft, #827): it is on do not disturb and
// in its groups from the start, and what it changes meanwhile is a change to those, which the
// new name takes when it lands (adoptLeftLocked). By the key, not the ID: a session that moves
// to another conversation before then leaves them with the one they were for. Caller holds mu.
func (d *Daemon) keptByLocked(s *session) string {
	if old := d.st.NamesLeft[s.key]; s.name == "" && s.key != "" && old != "" {
		return old
	}
	return tagKeyOf(s.name, s.id)
}

// tagStateLocked is what is kept about the tags of session s, nil if nothing. Caller holds mu.
func (d *Daemon) tagStateLocked(s *session) *tags.State {
	return d.st.Tags[d.keptByLocked(s)]
}

// adoptTagsLocked moves what a session kept by its ID while its name was pending to the name,
// once the name lands, unless the name has tags already. Caller holds mu.
func (d *Daemon) adoptTagsLocked(s *session) {
	if s.name == "" {
		return
	}
	pending := tagKeyOf("", s.id)
	if st := d.st.Tags[pending]; st != nil {
		delete(d.st.Tags, pending)
		if d.st.Tags[s.name] == nil {
			d.st.Tags[s.name] = st
		}
		d.saveLocked()
	}
}

// dndLocked says session id on this device is on do not disturb: its messages stay queued in its
// inbox, none is handed over as a turn, and the hook tells of none (see heldLocked and notices). A session that
// isn't registered at the moment goes by the tags kept for its name. Caller holds mu.
func (d *Daemon) dndLocked(id string) bool { return d.dnd.Load() || d.ownDNDLocked(id) }

// heldLocked says do not disturb holds message m in session id's inbox: the session is on it
// (dndLocked), and m isn't from this device's own person. Their message goes through as usual, as
// a turn or a notice, through the device's mode and the session's own tag alike (#777, Tyler,
// 2026-11-16: "I don't see when I would want DND to block my own messages"). A person on another
// device is held as anyone is: nothing ties one person's devices together. Caller holds mu.
func (d *Daemon) heldLocked(id string, m *inMsg) bool {
	return d.dndLocked(id) && !d.passesDND(m)
}

// passesDND says do not disturb never holds message m, on any session of this device: it is from
// this device's person (#777).
func (d *Daemon) passesDND(m *inMsg) bool { return m.Event.From == d.personAddr() }

// ownDNDLocked says session id is on do not disturb by its own tags and a human's marks, whatever
// the device's mode. Taking that off is what releases its inbox (releaseLocked): one taken off
// while the device is on do not disturb counts from then, so going back online releases it
// (releaseSinceLocked). Caller holds mu.
func (d *Daemon) ownDNDLocked(id string) bool {
	st := d.st.Tags[d.tagKeyLocked(id)]
	if st == nil {
		return false
	}
	return tags.Has(tags.Effective(st.Self, d.modelTagsLocked(id), st.Human), tags.DoNotDisturb)
}

// hasTagLocked says session id on this device has tag name. A session that isn't registered at
// the moment goes by the tags kept for its name. While the device is on do not disturb (mode.go)
// every session has that tag, whatever a human marked and however many tags it has. Caller holds mu.
func (d *Daemon) hasTagLocked(id, name string) bool {
	if name == tags.DoNotDisturb && d.dnd.Load() {
		return true
	}
	if s := d.sessions[id]; s != nil {
		return tags.Has(d.tagsOfLocked(s), name)
	}
	key := d.tagKeyLocked(id)
	st := d.st.Tags[key]
	if key == "" || st == nil {
		return false
	}
	return tags.Has(tags.Effective(st.Self, d.derivedTagsLocked(id), st.Human), name)
}

// peerDNDLocked says the session at dev/sess is on do not disturb: by this device's own tags, or
// by its device's presence, which says so for every session while the device is (#681). Caller
// holds mu.
func (d *Daemon) peerDNDLocked(dev, sess string) bool {
	if sess == "" || sess == proto.CLISession {
		return false
	}
	if dev == d.device {
		return d.dndLocked(sess)
	}
	p := d.peers[dev]
	return p != nil && (p.pres.DND || tags.Has(peerTagsLocked(p, sess), tags.DoNotDisturb))
}

// releaseLocked lets what stayed queued in the inbox of the sessions kept by key go out by whichever
// route applies now that do-not-disturb has come off: a turn if the session is idle, the hook's
// notice or whole message if it is at work (held for its turn's end, #202). Each message counts as
// arriving now (see waitedSince), so one that waited longer than
// a turn's limit (pushMaxAge) still goes in as a turn, and one whose delivery failed earlier, but
// provably never reached the session (not Attempted), is tried again. Caller holds mu.
func (d *Daemon) releaseLocked(key string) {
	now := time.Now()
	for id, box := range d.st.Inbox {
		if d.tagKeyLocked(id) != key {
			continue
		}
		for _, m := range box {
			if !m.Noticed && !m.Pushing && !m.Attempted {
				m.Released, m.PushFailed = now, false
			}
		}
	}
	d.saveLocked()
	d.kickPush()
}

// derivedTagsLocked is the tags the daemon works out for session id: its model, where T3 records
// the thread's. Caller holds mu.
func (d *Daemon) derivedTagsLocked(id string) []string {
	var out []string
	if d.dnd.Load() {
		out = append(out, tags.DoNotDisturb) // the device is on do not disturb (mode.go)
	}
	return append(out, d.modelTagsLocked(id)...)
}

// modelTagsLocked is session id's model tag, where T3 records the thread's model. Caller holds mu.
func (d *Daemon) modelTagsLocked(id string) []string {
	if st, ok := d.threadStateLocked(id); ok {
		if t := tags.ModelTag(st.Model); t != "" {
			return []string{t}
		}
	}
	return nil
}

// tagsOfLocked is the tags session s has. Caller holds mu.
func (d *Daemon) tagsOfLocked(s *session) []tags.Tag {
	var self []string
	var human map[string]tags.Mark
	if st := d.tagStateLocked(s); st != nil {
		self, human = st.Self, st.Human
	}
	ts := tags.Effective(self, d.derivedTagsLocked(s.id), human)
	if d.dnd.Load() && !tags.Has(ts, tags.DoNotDisturb) {
		// A human's mark can't take the device's do not disturb off a session, nor can the tag limit
		// (hasTagLocked), so who, lookup and the page show it as send goes by it. First, and within
		// the limit, as a peer reads no more than that many.
		ts = append([]tags.Tag{{Name: tags.DoNotDisturb, Src: tags.SrcDerived}}, ts...)
		ts = ts[:min(len(ts), tags.MaxPerSession)]
	}
	return ts
}

// peerTagsLocked is the tags of session sess in device p's presence, nil if it isn't there.
func peerTagsLocked(p *peer, sess string) []tags.Tag {
	for _, s := range p.pres.Sessions {
		if s.ID == sess {
			return s.Tags
		}
	}
	return nil
}

// setOwnTags is OpTags: session id adds and removes its own tags. A human's mark on a tag stands
// over the session's change, and the reply says so, except that a session can always leave a
// group (see tags.State.Left).
func (d *Daemon) setOwnTags(id string, req proto.Request) proto.Response {
	from := bus.Addr(d.device, id)
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.sessions[id]
	if s == nil {
		return proto.Response{OK: true, Self: from, Note: "the CLI has no tags"}
	}
	st := d.tagStateLocked(s)
	if st == nil {
		st = &tags.State{}
	}
	wasDND := d.ownDNDLocked(id)
	next := *st
	if err := next.SetSelf(req.Add, req.Remove); err != nil {
		return fail(err.Error())
	}
	var held []string
	added, _ := tags.Clean(req.Add)
	removed, _ := tags.Clean(req.Remove)
	// A session leaves a group a human put it in (#539): the line every group message ends with
	// says removing the tag leaves, and only a person on the page could take it out otherwise.
	next.Left(removed, time.Now().UTC())
	for _, t := range append(added, removed...) {
		if m, ok := next.Human[t]; ok {
			held = append(held, fmt.Sprintf("%s (a human set it %s)", t, map[bool]string{true: "on", false: "off"}[m.On]))
		}
	}
	d.putTagStateLocked(d.keptByLocked(s), &next)
	d.kickPresence()
	if wasDND && !d.ownDNDLocked(id) {
		d.releaseLocked(d.keptByLocked(s))
	}
	note := "tags set"
	if len(held) > 0 {
		note = "tags set, but a human's choice on the page stands over yours for " + strings.Join(held, ", ")
	}
	// The reply lists the tags the session can change, so not its device's group (#463), which who and
	// lookup show.
	return proto.Response{OK: true, Self: from, Note: note, Tags: d.tagsOfLocked(s)}
}

// putTagStateLocked keeps st as the tag state of key, or drops it when there is nothing in it,
// and saves. Caller holds mu.
func (d *Daemon) putTagStateLocked(key string, st *tags.State) {
	if st.Empty() {
		delete(d.st.Tags, key)
	} else {
		d.st.Tags[key] = st
	}
	d.saveLocked()
}

// tagKeyLocked is what session id's tags and mutes are kept by: a running session's own, else the
// name it took last, so a change for a session that isn't registered at the moment (its daemon
// restarted, and reads what was sent meanwhile before the session is back) waits for it. "" for a
// session this device knows nothing of. Caller holds mu.
func (d *Daemon) tagKeyLocked(id string) string {
	if s := d.sessions[id]; s != nil {
		return d.keptByLocked(s)
	}
	if name := d.st.SessionNames[id]; name != "" {
		return name
	}
	// One that stopped while its new name was pending, on a clone that lost its keys, still goes
	// by the name its conversation had (keptByLocked), as explainKeyLocked has it.
	if conv := d.conversationLocked(id); conv != "" && d.st.NamesLeft[conv] != "" {
		return d.st.NamesLeft[conv]
	}
	// One whose name never landed keeps what it has by its ID: one that stopped, whose inbox is
	// kept in case it comes back, and one that set tags or muted someone before it stopped or the
	// daemon restarted. Its mutes count as much as its tags (#503).
	pending := tagKeyOf("", id)
	if _, stopped := d.st.Stopped[id]; stopped || d.st.Tags[pending] != nil || d.st.Mutes[pending] != nil {
		return pending
	}
	return ""
}

// holdForLocked keeps e, a person's change to the tags of a session of this device that it knows
// nothing of, or an agent's add of one to a group, if the daemon has just started (#524): the
// session may be one whose name is pending and that kept nothing here, and is still reconnecting.
// It is applied when the session is back (catchUpLocked), before the messages that waited, so a
// group's message read with its add reaches it. Past catchUpWindow nothing is held, as no message
// is: a session back later isn't caught up. It says whether e was kept. Caller holds mu.
func (d *Daemon) holdForLocked(e bus.Event) bool {
	if time.Since(d.started) > catchUpWindow {
		return false
	}
	d.heldTags = append(d.heldTags, e)
	return true
}

// applyHeldLocked applies what holdForLocked kept for session id, now that it is back. Caller
// holds mu.
func (d *Daemon) applyHeldLocked(id string) {
	addr := bus.Addr(d.device, id)
	held, kept := d.heldTags, []bus.Event(nil)
	for _, e := range held {
		switch {
		case e.To != addr:
			kept = append(kept, e)
		case e.Type == bus.GroupAdd:
			d.applyGroupAddLocked(e, modeCatchUp)
		default:
			d.applyTagsEventLocked(e)
		}
	}
	d.heldTags = kept
}

// dropHeldLocked lets go of what holdForLocked kept for sessions that didn't come back within the
// catch-up window. Caller holds mu.
func (d *Daemon) dropHeldLocked() {
	for _, e := range d.heldTags {
		d.log.Printf("%s's %s for %s, read as this daemon started, wasn't applied: that session isn't known here, and didn't come back",
			e.From, e.Type, e.To)
	}
	d.heldTags = nil
}

// humanTagsLocked applies a human's change to the tags of session id on this device. by is the
// person, or their device. Caller holds mu.
func (d *Daemon) humanTagsLocked(id string, add, remove []string, by string, at time.Time) error {
	key := d.tagKeyLocked(id)
	if key == "" {
		return fmt.Errorf("session %s isn't known here", bus.Addr(d.device, id))
	}
	wasDND := d.ownDNDLocked(id)
	// Nothing is later than now, whatever a clock ahead or a forged commit says. So a change dated
	// later can't hold off the next, and a click on this device's page, dated now, is the newest.
	now := time.Now().UTC()
	if at.After(now) {
		at = now
	}
	next := d.st.Tags[key].Clone()
	next.CapTimes(now)
	capped := next.Clone()
	if err := next.SetHuman(tags.Base(next.Self, d.derivedTagsLocked(id)), add, remove, by, at); err != nil {
		return err
	}
	if next.SameChoices(capped) {
		// Kept all the same: a date capped only for now would go on refusing changes, as it is capped
		// afresh, at a later now, each time.
		d.putTagStateLocked(key, next)
		d.log.Printf("%s's change to the tags of session %s, from %s, is older than a human's later choice; not applied: %s",
			by, bus.Addr(d.device, id), at.Format(time.RFC3339), tagChange(add, remove))
		return nil
	}
	d.putTagStateLocked(key, next)
	d.kickPresence()
	if wasDND && !d.ownDNDLocked(id) {
		d.releaseLocked(key)
	}
	d.log.Printf("%s changed the tags of session %s: %s", by, bus.Addr(d.device, id), tagChange(add, remove))
	return nil
}

func tagChange(add, remove []string) string {
	var ch []string
	for _, t := range add {
		ch = append(ch, "+"+t)
	}
	for _, t := range remove {
		ch = append(ch, "-"+t)
	}
	return strings.Join(ch, " ")
}

// applyTagsEventLocked applies a human's change made on another device's page, if it is for a
// session here. Caller holds mu.
func (d *Daemon) applyTagsEventLocked(e bus.Event) {
	dev, sess := bus.SplitAddr(e.To)
	if dev != d.device {
		return
	}
	from, _ := bus.SplitAddr(e.From)
	by := from
	if p := d.peers[from]; p != nil && p.pres.Owner != "" {
		by = p.pres.Owner + " on " + from
	}
	if d.tagKeyLocked(sess) == "" && d.holdForLocked(e) {
		return
	}
	if err := d.humanTagsLocked(sess, e.Add, e.Remove, by, e.At); err != nil {
		d.log.Printf("tags from %s for %s not applied: %v", e.From, e.To, err)
	}
}

// SetTags is the page's change to the tags of the session at addr: applied here for a session of
// this device, sent to its device as a tags event otherwise.
func (s pageSource) SetTags(ctx context.Context, addr string, add, remove []string) error {
	d := s.d
	d.pageCalled(ctx)
	add, err := tags.Clean(add)
	if err != nil {
		return err
	}
	remove, err = tags.Clean(remove)
	if err != nil {
		return err
	}
	if len(add)+len(remove) == 0 {
		return errors.New("no tag to add or remove")
	}
	if len(add) > tags.MaxPerSession || len(remove) > tags.MaxPerSession {
		return fmt.Errorf("at most %d tags at a time", tags.MaxPerSession)
	}
	dev, sess := bus.SplitAddr(addr)
	if !bus.ValidDevice(dev) || !bus.ValidName(sess) {
		return errors.New("not a session's address")
	}
	d.mu.Lock()
	if dev == d.device {
		by := d.owner
		if by == "" {
			by = d.device
		}
		err := d.humanTagsLocked(sess, add, remove, by, time.Now().UTC())
		d.mu.Unlock()
		return err
	}
	p := d.peers[dev]
	known := p != nil && d.addressKnownLocked(dev, sess)
	d.mu.Unlock()
	if !known {
		return fmt.Errorf("%s isn't in its device's presence", addr)
	}
	// The page is served before Run's first sync. This append reads its own log under advMu, which
	// Run's sync, the one a stop cuts short, would then wait for; it waits for that sync instead, as
	// the page's send does (Sol's review of #377).
	d.awaitFirstSync(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Tags, From: bus.Addr(d.device, proto.CLISession),
		At: time.Now().UTC(), To: addr, Add: add, Remove: remove}
	return d.appendEvent(ctx, e)
}
