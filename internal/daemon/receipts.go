package daemon

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// Receipts (#26, docs/design/receipts.md). A message that asks for them (Event.Receipts, which a
// person's messages from the page do) gets marks back from each device it reaches: queued, handed
// over, delivered, read, per recipient session. The recipient's daemon gathers its marks for
// ReceiptGather and writes them as one encrypted receipt on its own log; the sender's daemon reads
// them like any event and keeps the furthest state per recipient. A mark for a message sent from
// this device never goes on the bus: it is applied here.
//
// An agent's message asks for receipts only when its sender asks to be told how far it got (#276,
// send's tell_me). The sender is told once, by a notice from Murmur in its inbox: when every
// recipient has got that far, or when TellWait has passed and some haven't.

// maxSent is how many of the person's messages keep their recipients and marks (Sent), the newest.
// maxTells is how many messages agents wait to be told of are kept beside them (trimSentLocked).
const (
	maxSent  = 200
	maxTells = 2000
)

// maxMarksHeld caps the marks waiting to go on the bus; past it the oldest are dropped. A device
// that can't push for a long while would otherwise keep every mark it ever made.
const maxMarksHeld = 5000

// markRetry is how long a receipt that couldn't be written first waits before it is tried again; it
// doubles with each failure in a row, up to markRetryMax.
const (
	markRetry    = 15 * time.Second
	markRetryMax = 5 * time.Minute
)

// sentRecord is one of this device's messages that asked for receipts: whom it was for when it was
// sent, and how far it got with each.
type sentRecord struct {
	At time.Time `json:"at"`
	// To is the sessions it was for, by address, as this device knew them when it was sent: the one
	// session or person it was sent to, or the members of its group, the sessions on its device, or
	// every session, for everyone.
	To []string `json:"to"`
	// Old is those of To whose device's presence, read, didn't say it writes marks
	// (bus.Presence.Receipts): their marks aren't waited for, and the page shows them as sent. A
	// device with no presence here (its daemon said goodbye, as an idle one does) or one this device
	// can't open yet isn't old, only not heard from.
	Old []string `json:"old,omitempty"`
	// Direct says it was sent to one session or person: a mark from whichever session took it, as
	// one holding the recipient's name now (forwardLocked), is that recipient's.
	Direct bool `json:"direct,omitempty"`
	// Marks is the furthest each recipient in To got, by address. A mark for an address not in To
	// is dropped: any device can write marks for its own sessions, and one the message wasn't for
	// would otherwise list itself as a reader. A session that joined a group after the message was
	// sent is dropped with it.
	Marks map[string]markRecord `json:"marks,omitempty"`
	// Tell is, for an agent's message whose sender asked to be told how far it got (#276), the
	// state it asked about: delivered or read. Asker is the sender's address, Subject the message's,
	// and Told says the sender has been told, which happens once. The page shows no ticks for such
	// a message: the page's ticks are for its person's messages.
	Tell    string `json:"tell,omitempty"`
	Asker   string `json:"asker,omitempty"`
	Subject string `json:"subject,omitempty"`
	Told    bool   `json:"told,omitempty"`
}

type markRecord struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// markLocked records that message e reached state with session id on this device, if e asked for
// receipts. The CLI's mailbox takes every broadcast and is nobody's, so it marks nothing. For a
// person, being in their inbox is being delivered: the page counts it as unread. Callers save the
// state, as they do for the change they mark. Caller holds mu.
func (d *Daemon) markLocked(id string, e bus.Event, state string) {
	if !e.Receipts || e.Type != bus.Message || id == "" || id == proto.CLISession {
		return
	}
	if id == bus.PersonSession && state == bus.MarkQueued {
		state = bus.MarkDelivered
	}
	now := time.Now().UTC()
	if dev, _ := bus.SplitAddr(e.From); dev == d.device {
		d.applyMarkLocked(e.ID, bus.Addr(d.device, id), state, now)
		return
	}
	for i, m := range d.st.Marks {
		if m.Msg == e.ID && m.To == id {
			if bus.MarkRank(state) > bus.MarkRank(m.State) {
				d.st.Marks[i].State, d.st.Marks[i].At = state, now
				d.kickMarks()
			}
			return
		}
	}
	d.st.Marks = append(d.st.Marks, bus.Mark{Msg: e.ID, To: id, State: state, At: now})
	if over := len(d.st.Marks) - maxMarksHeld; over > 0 {
		d.st.Marks = slices.Delete(d.st.Marks, 0, over)
	}
	d.kickMarks()
}

// markAllLocked marks each of msgs, given to session id, as state. Caller holds mu.
func (d *Daemon) markAllLocked(id string, msgs []*inMsg, state string) {
	for _, m := range msgs {
		d.markLocked(id, m.Event, state)
	}
}

func (d *Daemon) kickMarks() {
	select {
	case d.markKick <- struct{}{}:
	default:
	}
}

// receiptLoop writes the marks this device's sessions make, ReceiptGather after the first of a
// batch, as one receipt: a message queued and handed over within that time costs one mark. A receipt
// follows the one before by ReceiptGap at least, so the delivered and read marks of a group's
// members, which come as each session gets to them, share a few receipts rather than taking one
// each. A batch that couldn't be written stays in the state, and is tried again after markRetry,
// doubling to markRetryMax, or by the next daemon.
func (d *Daemon) receiptLoop(ctx context.Context) {
	d.mu.Lock()
	waiting := len(d.st.Marks) > 0
	d.mu.Unlock()
	if waiting {
		d.kickMarks() // what the daemon before this one didn't write
	}
	var wrote time.Time
	retry := markRetry
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.markKick:
		}
		wait := max(d.opt.ReceiptGather, time.Until(wrote.Add(d.opt.ReceiptGap)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		err := d.writeMarks(ctx)
		if err == nil {
			wrote, retry = time.Now(), markRetry
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrInvisible) {
			retry = markRetry // until the device is back, which kicks this loop
			continue
		}
		d.log.Printf("writing a receipt: %v; trying again in %s", err, retry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
		retry = min(2*retry, markRetryMax)
		d.kickMarks()
	}
}

// writeMarks writes up to bus.MaxMarks of the marks waiting as one receipt, and takes off those it
// wrote, unless they moved on meanwhile.
func (d *Daemon) writeMarks(ctx context.Context) error {
	d.mu.Lock()
	batch := slices.Clone(d.st.Marks[:min(len(d.st.Marks), bus.MaxMarks)])
	d.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Receipt, From: bus.Addr(d.device, bus.ReceiptFrom),
		At: time.Now().UTC(), Marks: batch}
	if err := d.appendEvent(ctx, e); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Marks = slices.DeleteFunc(d.st.Marks, func(m bus.Mark) bool { return slices.Contains(batch, m) })
	d.saveLocked()
	if len(d.st.Marks) > 0 {
		d.kickMarks()
	}
	return nil
}

// applyReceiptLocked takes in the marks of receipt e, from another device's log. Only marks on this
// device's person's messages are kept. Caller holds mu.
func (d *Daemon) applyReceiptLocked(e bus.Event) {
	dev, _ := bus.SplitAddr(e.From)
	for _, m := range e.Marks {
		d.applyMarkLocked(m.Msg, bus.Addr(dev, m.To), m.State, m.At)
	}
}

// applyMarkLocked keeps the mark that message msg reached state with the session at addr, if msg is
// one of the person's that asked for receipts and the mark goes further than what is known. Marks
// only go forward, so one read late, or twice, changes nothing. The caller saves the state, as each
// already does for what it marks. Caller holds mu.
func (d *Daemon) applyMarkLocked(msg, addr, state string, at time.Time) {
	rec := d.st.Sent[msg]
	if rec == nil {
		return
	}
	if dev, _ := bus.SplitAddr(addr); rec.Direct && len(rec.To) == 1 {
		if to, _ := bus.SplitAddr(rec.To[0]); to == dev {
			addr = rec.To[0] // the session that holds the recipient's name now (forwardLocked)
		}
	}
	if !slices.Contains(rec.To, addr) {
		return
	}
	if cur, ok := rec.Marks[addr]; ok && bus.MarkRank(state) <= bus.MarkRank(cur.State) {
		return
	}
	if rec.Marks == nil {
		rec.Marks = map[string]markRecord{}
	}
	rec.Marks[addr] = markRecord{State: state, At: at}
	d.tellLocked(msg, rec, false)
}

// recordSentLocked keeps message e, which this device's person is sending with receipts asked for,
// with the sessions it is for. It goes in before the message is written, as a recipient on this
// device marks it while it is written. Caller holds mu.
func (d *Daemon) recordSentLocked(e bus.Event) {
	to, direct := d.recipientsOfLocked(e)
	rec := &sentRecord{At: e.At, To: to, Direct: direct}
	for _, addr := range to {
		if dev, _ := bus.SplitAddr(addr); dev != d.device {
			if p := d.peers[dev]; p != nil && !p.locked && !p.pres.Receipts {
				rec.Old = append(rec.Old, addr)
			}
		}
	}
	if d.st.Sent == nil {
		d.st.Sent = map[string]*sentRecord{}
	}
	d.st.Sent[e.ID] = rec
	d.trimSentLocked(e.ID)
	d.saveLocked()
}

// trimSentLocked lets go of the oldest sent records past maxSent, and of those an agent still
// waits to be told of past maxTells. The two are counted apart (#748): sharing the 200, a busy
// half hour of the person's messages and other agents' pushed out a record whose sender was still
// waiting, and it never heard, not even at the timeout. One that is waited on goes when its sender
// is told, TellWait after it was sent at the latest (tellLoop), so maxTells is only a backstop,
// and one it lets go of is told then, early, where its message is: none goes unheard.
// just is the record being added, which the caller may yet mark as waited on: it is kept. Caller
// holds mu.
func (d *Daemon) trimSentLocked(just string) {
	var kept, waiting []string
	for id, rec := range d.st.Sent {
		switch {
		case id == just:
		case rec.Tell != "" && !rec.Told:
			waiting = append(waiting, id)
		default:
			kept = append(kept, id)
		}
	}
	oldest := func(ids []string, room int) []string {
		over := len(ids) - room
		if over <= 0 {
			return nil
		}
		sort.Slice(ids, func(i, j int) bool { return d.st.Sent[ids[i]].At.Before(d.st.Sent[ids[j]].At) })
		return ids[:over]
	}
	for _, id := range oldest(kept, maxSent-1) {
		delete(d.st.Sent, id)
	}
	for _, id := range oldest(waiting, maxTells) {
		d.tellLocked(id, d.st.Sent[id], true) // tells its sender, and lets go of it
	}
}

// recipientsOfLocked is whom message e is for, as this device knows the mesh now, by address, and
// whether it went to one session or person. A device decides for itself which of its sessions get a
// message (addressedLocked), so for a group, a device or everyone this is the sessions this device
// sees there: those in presence, less offline ones and those that muted the sender. People get only
// what is sent to them, never a broadcast. Caller holds mu.
func (d *Daemon) recipientsOfLocked(e bus.Event) (to []string, direct bool) {
	if strings.Contains(e.To, "/") {
		return []string{e.To}, true
	}
	sender := d.muteNamesLocked(e.From)
	want := func(dev string, s bus.SessionInfo) bool {
		if s.ID == bus.PersonSession || s.ID == proto.CLISession || bus.Addr(dev, s.ID) == e.From {
			return false
		}
		if bus.IsGroup(e.To) && !tags.Has(s.Tags, e.To) {
			return false
		}
		return e.To == "*" || bus.IsGroup(e.To) || e.To == dev
	}
	for _, s := range d.sortedSessionsLocked() {
		info := bus.SessionInfo{ID: s.id, Name: s.name, Tags: d.tagsOfLocked(s)}
		if !d.offMeshLocked(s.id) && want(d.device, info) && !d.hasMutedLocked(d.device, s.id, sender) {
			to = append(to, bus.Addr(d.device, s.id))
		}
	}
	for _, dev := range sortedKeys(d.peers) {
		p := d.peers[dev]
		if d.staleLocked(p) {
			continue
		}
		for _, s := range p.pres.Sessions {
			if want(dev, s) && !d.hasMutedLocked(dev, s.ID, sender) {
				to = append(to, bus.Addr(dev, s.ID))
			}
		}
	}
	return to, false
}

// receiptsLocked is, for each of the person's messages that asked for receipts, how far it got with
// each recipient, for the page. Caller holds mu.
func (d *Daemon) receiptsLocked() map[string][]web.Receipt {
	if len(d.st.Sent) == 0 {
		return nil
	}
	label := d.labelerLocked()
	out := make(map[string][]web.Receipt, len(d.st.Sent))
	for id, rec := range d.st.Sent {
		if rec.Tell != "" {
			continue // an agent's message (#276)
		}
		rs := make([]web.Receipt, 0, len(rec.To))
		for _, addr := range rec.To { // only whom it was sent to (see sentRecord.Marks)
			r := web.Receipt{Addr: addr, Name: label(addr), Person: bus.IsPerson(addr)}
			if m, ok := rec.Marks[addr]; ok {
				r.State, r.At = m.State, m.At
			} else if slices.Contains(rec.Old, addr) {
				r.State = web.ReceiptNone
			}
			rs = append(rs, r)
		}
		slices.SortStableFunc(rs, func(a, b web.Receipt) int { return strings.Compare(a.Name, b.Name) })
		out[id] = rs
	}
	return out
}
