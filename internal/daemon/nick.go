package daemon

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/names"
	"github.com/a-Fig/murmur/internal/proto"
)

// A device's nickname (#190). A device's ID is its hostname and four hex digits, which says which
// machine and not whose. Its user can give it a nickname, such as their own name; presence carries
// it, so every teammate sees the same one. A device with none is called by its person's name, if
// someone chose one on the page, so most devices never need one set. It is a label and nothing
// more: who, lookup and the page show it beside the ID, and no address takes it, so whatever a
// device calls itself, no message goes anywhere else for it. docs/VISION.md ("Device nicknames")
// has the reasons.

// nickOfLocked is what device dev is called: the nickname its user gave it, else its person's
// name. "" if it has neither. Another device's person's name counts only if it reaches them
// (listedPersonNameLocked), so a presence that says its person is someone else's ada doesn't get
// its device called ada either (#350). Caller holds mu.
func (d *Daemon) nickOfLocked(dev string) string {
	if dev == d.device {
		return cmp.Or(d.st.Nick, d.st.Person)
	}
	p := d.peers[dev]
	if p == nil {
		return ""
	}
	if p.pres.Nick != "" {
		return p.pres.Nick
	}
	for _, s := range p.pres.Sessions {
		if s.ID == bus.PersonSession {
			return d.listedPersonNameLocked(bus.Addr(dev, s.ID), s.Name)
		}
	}
	return ""
}

// isDeviceLocked says id is the ID of a device this daemon knows. Caller holds mu.
func (d *Daemon) isDeviceLocked(id string) bool {
	_, peer := d.peers[id]
	return peer || id == d.device
}

// nickProblemLocked says why nick can't be this device's nickname, "" if it can: it would read
// as another device, or as the person on one. A nickname another device has is no problem, as
// one person may call both their machines the same. Caller holds mu.
func (d *Daemon) nickProblemLocked(nick string) string {
	if !bus.ValidNick(nick) {
		return fmt.Sprintf("%q can't be a nickname. Use 2 to %d lowercase letters and digits, with single dashes between words, and not a session's name.", nick, names.MaxPerson)
	}
	if d.isDeviceLocked(nick) {
		return fmt.Sprintf("%s is a device's ID here.", nick)
	}
	if addr, ok := d.resolvePersonLocked(nick); ok && addr != d.personAddr() {
		dev, _ := bus.SplitAddr(addr)
		return fmt.Sprintf("%s is the name of the person on %s.", nick, dev)
	}
	return ""
}

// nick is OpNick: it says what this device is called, gives it a nickname, or takes its nickname
// off. It waits for the first sync, as choosing a person's name does: before it, this daemon
// knows no other device, and would take another's ID or person's name for a nickname.
func (d *Daemon) nick(ctx context.Context, req proto.Request) proto.Response {
	nick := strings.TrimSpace(req.Nick)
	if nick != "" {
		d.awaitFirstSync(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch {
	case req.Clear:
		d.st.Nick = ""
	case nick != "":
		if why := d.nickProblemLocked(nick); why != "" {
			return fail(why)
		}
		d.st.Nick = nick
	}
	if req.Clear || nick != "" {
		d.saveLocked()
		d.kickPresence()
	}
	var note string
	switch called := d.nickOfLocked(d.device); {
	case d.st.Nick != "":
		note = fmt.Sprintf("This device, %s, has the nickname %s: teammates see it beside the device's ID.", d.device, called)
	case called != "":
		note = fmt.Sprintf("This device, %s, has no nickname. It is called %s, the name its person chose on %s's page.", d.device, called, brand.Product)
	default:
		note = fmt.Sprintf("This device, %s, has no nickname.", d.device)
	}
	return proto.Response{OK: true, Self: bus.Addr(d.device, proto.CLISession), Note: note}
}
