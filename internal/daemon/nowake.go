package daemon

import (
	"fmt"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

// Telling a sender that its message won't wake the recipient (#545). The recipient's device decides
// whether a message from another agent goes into a session as a turn (mayWakeLocked); where the wake
// setting keeps it out, the message waits for the session's next turn or its inbox, which `send`
// and `lookup` say. Only the recipient's device knows its setting and how long the session has been
// idle, so its presence carries the answer for each session (bus.SessionInfo.NoWake), and a change
// is published when it happens rather than at the next heartbeat.

// maxUnwokenNamed is how many of the sessions a message to many won't wake are named; the rest are
// counted. Under the default wake setting most sessions of a large team are idle at any time.
const maxUnwokenNamed = 3

// noWakeLocked says why a message from another agent wouldn't go into session id as a turn now,
// as its presence tells other devices (#545): bus.NoWakeOff if the page's wake setting for it is
// never, bus.NoWakeIdle if it has been idle longer than the setting allows, "" if it would. Do not
// disturb isn't counted: its tag is in presence, and says more. Nor is a session off the mesh,
// which presence leaves out. Caller holds mu.
func (d *Daemon) noWakeLocked(id string, wake pageSettings) string {
	if d.offMeshLocked(id) || d.mayWakeLocked(id, wake) {
		return ""
	}
	if wake.choice(d.sessionNameLocked(id)).When == web.WakeNever {
		return bus.NoWakeOff
	}
	return bus.NoWakeIdle
}

// peerNoWakeLocked is noWakeLocked for the session at dev/sess, on this device or, as its presence
// says, another; "" for anything but a session that is running. Caller holds mu.
func (d *Daemon) peerNoWakeLocked(dev, sess string, wake pageSettings) string {
	if dev == d.device {
		if d.sessions[sess] == nil {
			return ""
		}
		return d.noWakeLocked(sess, wake)
	}
	if p := d.peers[dev]; p != nil {
		for _, s := range p.pres.Sessions {
			if s.ID == sess && sess != bus.PersonSession && (s.NoWake == bus.NoWakeOff || s.NoWake == bus.NoWakeIdle) {
				return s.NoWake
			}
		}
	}
	return ""
}

// wakeChangedLocked says whether a message would wake some session here has changed since the
// last presence said (#545). It changes with time as well as with the page: a session goes idle
// past its setting's minutes, or gets to work again, and nothing else would publish that until the
// heartbeat. Caller holds mu.
func (d *Daemon) wakeChangedLocked(wake pageSettings) bool {
	for id, s := range d.sessions {
		if d.noWakeLocked(id, wake) != s.noWake {
			return true
		}
	}
	return false
}

// unwokenLocked names the sessions a message from from to many won't wake (#545): those on device
// dev, or on every device if dev is "", and of a group only its members, by name as
// groupMembersLocked gives them. A person's message to one session wakes it however long it has
// been idle (serve.go); one to many doesn't (personAsks), so idle counts here as for an agent.
// Caller holds mu.
func (d *Daemon) unwokenLocked(from, dev string, group bool, members []string, wake pageSettings) []string {
	var out []string
	add := func(name, why string) {
		if why == "" || group && !slices.Contains(members, name) {
			return
		}
		out = append(out, name)
	}
	if dev == "" || dev == d.device {
		for _, s := range d.sortedSessionsLocked() {
			if addr := bus.Addr(d.device, s.id); addr != from {
				add(nameOr(s.name, addr), d.noWakeLocked(s.id, wake))
			}
		}
	}
	for _, id := range sortedKeys(d.peers) {
		if dev != "" && dev != id || d.staleLocked(d.peers[id]) {
			continue
		}
		for _, s := range d.peers[id].pres.Sessions {
			if s.ID != bus.PersonSession {
				add(nameOr(s.Name, bus.Addr(id, s.ID)), d.peerNoWakeLocked(id, s.ID, wake))
			}
		}
	}
	return out
}

// noWakeNote tells the sender of a message to the session called name that it won't wake it, and
// why (#545). hookOff says the session's hook isn't running, so it isn't told at its next turn
// either.
func noWakeNote(name, why string, hookOff bool) string {
	reason := "waking is off for it on " + brand.Command + " ui"
	if why == bus.NoWakeIdle {
		reason = "it has been idle longer than its wake setting allows"
	}
	if hookOff {
		return name + " won't be woken (" + reason + "), and its hook isn't running: it sees this when it reads its inbox."
	}
	return name + " won't be woken (" + reason + "): it sees this at its next turn, or when it reads its inbox."
}

// unwokenNote tells the sender of a message to many which of them it won't wake, "" if it wakes
// every one that is idle (#545).
func unwokenNote(names []string) string {
	if len(names) == 0 {
		return ""
	}
	list := names[len(names)-1]
	switch shown := names[:min(len(names), maxUnwokenNamed)]; {
	case len(names) > len(shown):
		list = fmt.Sprintf("%s and %d more", strings.Join(shown, ", "), len(names)-len(shown))
	case len(names) > 1:
		list = strings.Join(names[:len(names)-1], ", ") + " and " + list
	}
	return list + " won't be woken (waking is off for them on " + brand.Command +
		" ui, or they have been idle longer than their wake setting allows): they see this at their next turn, or when they read their inbox."
}

// sentences joins the notes that aren't empty into one.
func sentences(notes ...string) string {
	var out []string
	for _, n := range notes {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return strings.Join(out, " ")
}
