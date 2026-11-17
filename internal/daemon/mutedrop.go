package daemon

import (
	"fmt"
	"slices"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// A message dropped on arrival because its recipient muted its sender is listed, by ID, in the
// recipient's presence, and the sender's daemon tells the sender (#466). Its own daemon refuses a
// send to a session that muted it, but only once it has read the mute in presence: a direct
// message sent before that, and past the receiver's MuteGrace, or a message to everyone or to a
// device sent before it, is dropped with its sender believing it arrived. A sender is told once
// per message and muter, in its inbox, from Murmur, as a tell_me notice is: no turn starts, and
// nobody is woken. A sender on a daemon from before this isn't told.

// muteDropKeep is how long a dropped message stays listed, and a sent one remembered to be told
// of: a presence is read within seconds, so a day covers a device that was off for a while.
const muteDropKeep = 24 * time.Hour

// maxOutgoing caps st.Outgoing; past it the oldest is let go.
const maxOutgoing = 200

type muteDrop struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

// outgoing is a message one of this device's sessions sent, kept for muteDropKeep in case another
// device lists it as dropped for a mute. Told is the names of the muters the sender has been told
// of, or was when it sent.
type outgoing struct {
	From    string    `json:"from"`
	To      string    `json:"to"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Told    []string  `json:"told,omitempty"`
}

// mutedropLocked notes that e, from another device, was dropped for session id of this device,
// which has muted its sender, and pushes a presence that lists it. Caller holds mu.
func (d *Daemon) mutedropLocked(id string, e bus.Event) {
	if dev, _ := bus.SplitAddr(e.From); dev == d.device {
		return // this daemon refused it or named the muter when it was sent
	}
	if d.st.MuteDrops == nil {
		d.st.MuteDrops = map[string][]muteDrop{}
	}
	now := time.Now()
	drops := slices.DeleteFunc(d.st.MuteDrops[id], func(m muteDrop) bool { return m.ID == e.ID || now.Sub(m.At) > muteDropKeep })
	drops = append(drops, muteDrop{e.ID, now})
	if n := len(drops) - bus.MaxDropped; n > 0 {
		drops = slices.Delete(drops, 0, n)
	}
	d.st.MuteDrops[id] = drops
	d.saveLocked()
	d.kickPresence()
}

// mutedropsLocked is the IDs presence lists as dropped for session id. Caller holds mu.
func (d *Daemon) mutedropsLocked(id string) []string {
	var ids []string
	for _, m := range d.st.MuteDrops[id] {
		if time.Since(m.At) <= muteDropKeep {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// outgoingLocked keeps e, which session from of this device sent to to (as the sender named it),
// in case another device lists it as dropped for a mute; named is the muters its sender was told
// of already. Caller holds mu, and saves the state.
func (d *Daemon) outgoingLocked(from, to string, e bus.Event, named []string) {
	if d.st.Outgoing == nil {
		d.st.Outgoing = map[string]*outgoing{}
	}
	now := time.Now()
	for id, o := range d.st.Outgoing {
		if now.Sub(o.At) > muteDropKeep {
			delete(d.st.Outgoing, id)
		}
	}
	for len(d.st.Outgoing) >= maxOutgoing {
		oldest := ""
		for id, o := range d.st.Outgoing {
			if oldest == "" || o.At.Before(d.st.Outgoing[oldest].At) {
				oldest = id
			}
		}
		delete(d.st.Outgoing, oldest)
	}
	if to == "*" {
		to = "everyone"
	}
	d.st.Outgoing[e.ID] = &outgoing{From: from, To: to, Subject: e.Subject, At: now, Told: slices.Clone(named)}
}

// droppedThereLocked tells this device's senders of the messages another device's sessions, in
// sessions, list as dropped for a mute, each once. Caller holds mu.
func (d *Daemon) droppedThereLocked(sessions []bus.SessionInfo) {
	told := false
	for _, s := range sessions {
		for _, id := range s.Dropped {
			o := d.st.Outgoing[id]
			if o == nil || s.Name == "" || slices.Contains(o.Told, s.Name) {
				continue
			}
			o.Told = append(o.Told, s.Name)
			told = true
			addr := d.queuedAtLocked(o.From)
			dev, sess := bus.SplitAddr(addr)
			if dev != d.device || sess == "" {
				continue
			}
			subject := fmt.Sprintf("%s didn't get %q: they have muted you", s.Name, o.Subject)
			body := fmt.Sprintf("Your message %q to %s was dropped when it reached %s, who has muted you. Nothing you send them reaches them until they unmute you.", o.Subject, o.To, s.Name)
			d.addInboxLocked(sess, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
				From: bus.Addr(d.device, bus.ReceiptFrom), To: addr, At: time.Now().UTC(), Subject: subject, Body: body})
		}
	}
	if told {
		d.saveLocked()
	}
}
