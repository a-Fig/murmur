package daemon

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/view"
)

// Telling a sender how far its message got (#276, docs/design/receipts.md). An agent's send can ask,
// with tell_me, to be told once its message is delivered or read. The message then asks for
// receipts, as a person's from the page does, and is kept in Sent with whom it was for. When every
// recipient whose device writes marks has got that far, or TellWait after the send, the sender gets
// one notice from Murmur in its inbox, saying where the message is with each recipient.

// ValidTell says s is a state a sender may ask to be told of: delivered or read.
func ValidTell(s string) bool { return s == bus.MarkDelivered || s == bus.MarkRead }

// isTellNotice says e is a notice this device wrote for one of its sessions about how far a message
// got (#276). It comes from this device's receipts address, which no session holds, and is never on
// the bus.
func (d *Daemon) isTellNotice(e bus.Event) bool {
	return e.Type == bus.Message && e.From == bus.Addr(d.device, bus.ReceiptFrom)
}

// waitedFor is those of rec's recipients whose marks are waited for: all but those whose device
// writes none.
func waitedFor(rec *sentRecord) []string {
	return slices.DeleteFunc(slices.Clone(rec.To), func(a string) bool { return slices.Contains(rec.Old, a) })
}

// tellLocked tells the sender of rec, if it asked to be told (#276) and hasn't been: once every
// recipient whose device writes marks has got as far as it asked, or, with late, wherever they have
// got to. The notice goes in the sender's inbox, from Murmur, and never starts a turn of its own
// (pushable): the sender asked to know, not to be woken. Message msg is then let go of, as nothing
// reads its record again, and it would take a place among the person's 200. The caller saves the
// state. Caller holds mu.
func (d *Daemon) tellLocked(msg string, rec *sentRecord, late bool) {
	if rec.Tell == "" || rec.Told {
		return
	}
	waited := waitedFor(rec)
	short := 0
	for _, addr := range waited {
		if !reached(rec.Marks[addr].State, rec.Tell) {
			short++
		}
	}
	if short > 0 && !late {
		return
	}
	rec.Told = true
	delete(d.st.Sent, msg)
	// The sender may have been replaced since it sent (a T3 thread's new session, #659): the notice
	// goes to the session that holds its name now, as a message for it would.
	to := d.queuedAtLocked(rec.Asker)
	dev, sess := bus.SplitAddr(to)
	if dev != d.device || sess == "" {
		return
	}
	subject, body := tellText(rec, waited, short, d.labelerLocked())
	d.addInboxLocked(sess, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: bus.Addr(d.device, bus.ReceiptFrom), To: to, At: time.Now().UTC(), Subject: subject, Body: body})
}

// reached says a recipient at state has got as far as want. A message handed over as a turn counts
// as delivered: the client has the turn whole, and it is the way an idle session in T3 gets a
// message. It doesn't count as read: where the hook sends the turn's prompt, the mark moves on to
// read once the turn starts (turnread.go), and a turn T3 holds in its queue hasn't been read (#654).
// A recipient that never sends one stays handed over, so a sender asking it for read is told at
// TellWait, with the message handed over as a turn: Pi, OpenCode and Grok, whose hooks send no
// prompt, a session with no hook, and one on a device whose daemon is older than #611.
func reached(state, want string) bool {
	if state == bus.MarkHanded && want == bus.MarkDelivered {
		return true
	}
	return state != "" && bus.MarkRank(state) >= bus.MarkRank(want)
}

// tellText is the subject and body of the notice tellLocked gives: whether the message got as far
// as its sender asked with every recipient, and then where it is with each, by name.
func tellText(rec *sentRecord, waited []string, short int, label func(string) string) (subject, body string) {
	verb := map[string]string{bus.MarkDelivered: "been told of", bus.MarkRead: "read"}[rec.Tell]
	if short == 0 && slices.ContainsFunc(waited, func(a string) bool { return rec.Marks[a].State == bus.MarkHanded }) {
		verb = "got" // some as a turn, which the body says; only for delivered (reached)
	}
	about := "your message " + strconv.Quote(bus.OneLine(rec.Subject, 60))
	switch {
	case short == 0 && len(rec.To) == 1 && rec.Marks[rec.To[0]].State == bus.MarkHanded:
		subject = label(rec.To[0]) + " has been handed " + about + " as a turn"
	case short == 0 && len(rec.To) == 1:
		subject = label(rec.To[0]) + " has " + verb + " " + about
	case short == 0 && len(waited) == len(rec.To):
		subject = fmt.Sprintf("All %d recipients have %s %s", len(rec.To), verb, about)
	case short == 0:
		subject = fmt.Sprintf("All %d recipients that send receipts have %s %s", len(waited), verb, about)
	case len(rec.To) == 1:
		subject = label(rec.To[0]) + " hasn't " + verb + " " + about + " yet"
	default:
		subject = fmt.Sprintf("%d of %d recipients haven't %s %s yet", short, len(waited), verb, about)
	}
	var b strings.Builder
	for _, addr := range sortedBy(rec.To, label) {
		fmt.Fprintf(&b, "- %s: %s\n", label(addr), tellState(rec, addr))
	}
	if short > 0 {
		b.WriteString("You won't be told again about this message.\n")
	}
	return subject, strings.TrimRight(b.String(), "\n")
}

// tellState says where message rec is with the recipient at addr, and since when.
func tellState(rec *sentRecord, addr string) string {
	if slices.Contains(rec.Old, addr) {
		return "its " + brand.Product + " is too old to send receipts"
	}
	m, ok := rec.Marks[addr]
	if !ok {
		return "not heard from: its device may be down, or hasn't fetched the message yet"
	}
	words := map[string]string{
		bus.MarkQueued:    "queued in its inbox, not yet seen",
		bus.MarkHanded:    "handed over as a turn, which " + brand.Product + " can't see start",
		bus.MarkDelivered: "told of it, not yet read",
		bus.MarkRead:      "read",
	}[m.State]
	if words == "" {
		words = m.State
	}
	return words + ", " + view.Ago(m.At)
}

// minutes says how long d is, in whole minutes where it is one or more.
func minutes(d time.Duration) string {
	switch m := int(d.Minutes()); {
	case m == 1:
		return "1 minute"
	case m > 1:
		return fmt.Sprintf("%d minutes", m)
	}
	return d.String()
}

// sortedBy is addrs sorted by label, then by address.
func sortedBy(addrs []string, label func(string) string) []string {
	out := slices.Clone(addrs)
	slices.SortStableFunc(out, func(a, b string) int {
		if c := strings.Compare(label(a), label(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return out
}

// tellLoop tells each sender whose message has waited TellWait without getting as far as it asked
// (#276). It looks once a minute, or every TellWait if that is shorter, as in tests.
func (d *Daemon) tellLoop(ctx context.Context) {
	tick := time.NewTicker(min(time.Minute, d.opt.TellWait))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		d.mu.Lock()
		told := false
		for id, rec := range d.st.Sent { // tellLocked deletes as it goes, which a range allows
			if rec.Tell != "" && !rec.Told && time.Since(rec.At) >= d.opt.TellWait {
				d.tellLocked(id, rec, true)
				told = true
			}
		}
		if told {
			d.saveLocked()
		}
		d.mu.Unlock()
	}
}
