package bus

import (
	"fmt"
	"time"

	"github.com/a-Fig/murmur/internal/names"
)

// NameRecord is one commit of the name registry, refs/murmur/names. An assignment names the session
// that took Name. A reservation names a device and no session: the device has set Name aside for a
// session it has yet to start, and gives it out without asking the remote, so a new session has its
// name at once. The device records the session that took it with an assignment later. A snapshot,
// which a new root carries after the registry was lost or overwritten, lists every name its writer
// knew to be used, so that daemons joining later learn them all. A commit can be a snapshot and
// either of the others.
//
// A reader that predates reservations takes one for a name in use by a session it doesn't know
// (Claims gives it no address), which is what keeps it from handing the name out.
//
// A person's name is claimed here too (#268), as an assignment to device/human. It can't have a
// session name's form, so a reader from before passes over it as a name names.Generate couldn't
// make, and gives it to no session.
//
// The client's own session ID is never written here: Session is the ID Murmur gave the session.
type NameRecord struct {
	V       int       `json:"v"`
	Name    string    `json:"name,omitempty"`
	Device  string    `json:"device,omitempty"`
	Session string    `json:"session,omitempty"`
	At      time.Time `json:"at"` // the writer's clock; for display, never for ordering
	// Used maps each name in a snapshot to the address of the session that has it, or "" where
	// its writer didn't know.
	Used map[string]string `json:"used,omitempty"`
}

// Subject is the record as one readable line, the commit's subject.
func (r NameRecord) Subject() string {
	switch {
	case r.Name == "":
		return fmt.Sprintf("snapshot of %d names", len(r.Used))
	case r.Session == "":
		return fmt.Sprintf("%s set aside for %s", r.Name, r.Device)
	}
	return fmt.Sprintf("%s -> %s", r.Name, Addr(r.Device, r.Session))
}

// Claims returns the names a record read from the registry holds, each with its session's address
// ("" if unknown, as for a reservation). Anyone with push access can write the registry, so a
// reader holds it to what Murmur writes: only names that names.Generate could make, and addresses
// in their own alphabets.
func (r NameRecord) Claims() map[string]string {
	claims := map[string]string{}
	if r.V != Version {
		return claims
	}
	if names.Valid(r.Name) {
		addr := Addr(r.Device, r.Session)
		if !validAddr(addr) {
			addr = ""
		}
		claims[r.Name] = addr
	} else if addr := Addr(r.Device, r.Session); ValidPerson(r.Name) && IsPerson(addr) && validAddr(addr) {
		claims[r.Name] = addr
	}
	for name, addr := range r.Used {
		switch {
		case names.Valid(name):
			if addr != "" && !validAddr(addr) {
				addr = ""
			}
		case ValidPerson(name) && IsPerson(addr) && validAddr(addr):
			// A person's, in a snapshot: always with the address, as nothing sets one aside.
		default:
			continue
		}
		if _, ok := claims[name]; !ok {
			claims[name] = addr
		}
	}
	return claims
}

// Reservation reports whether the record sets a name aside for a device, and which.
func (r NameRecord) Reservation() (name, device string, ok bool) {
	if r.V != Version || r.Session != "" || !names.Valid(r.Name) || !ValidDevice(r.Device) {
		return "", "", false
	}
	return r.Name, r.Device, true
}
