package daemon

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/a-Fig/murmur/internal/attrib"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/web"
)

// What sessions did, other than send messages, for the page's Recent view (#448). Nothing new goes
// on the bus for it. A session starting or stopping is an event every device already writes. A
// status line or a tag changing is seen as the difference between two presences of its device,
// this one's own included, so it is known only from when this daemon started reading, and at
// most one change per presence write. Commits are known with their count only for this device's
// sessions, which the hook brackets (commits.go); another device's presence says which branches a
// session committed to, so a branch new to that list is shown without a count.
//
// All of it is kept in memory, the newest maxActs, and is gone with the daemon: it is a feed of
// what happened lately, and the bus and git hold what must last. The starts and stops are on the
// bus, so a daemon reads those back when it starts (history.go); the rest it can't.

// maxActs is how many the daemon keeps.
const maxActs = 600

// pageAct is one kept activity and its number, as pageMsg is for a message. Its session's name is
// put in when the page asks.
type pageAct struct {
	seq uint64
	a   web.Activity
}

// sessSeen is what a session's presence last said of it, to tell what changed.
type sessSeen struct {
	doing     string
	tags      []string
	committed []string
}

// recordActLocked keeps a for the page, with a number of its own. An ID given twice is kept once:
// a session's start is read from the bus on every sync that passes it. Caller holds mu.
func (d *Daemon) recordActLocked(a web.Activity) {
	if a.ID != "" && d.actIDs[a.ID] {
		return
	}
	d.actSeq++
	if a.ID == "" {
		a.ID = "act-" + strconv.FormatInt(d.started.UnixNano(), 36) + "-" + strconv.FormatUint(d.actSeq, 10)
	}
	if a.At.IsZero() {
		a.At = time.Now()
	}
	a.At = a.At.UTC()
	if len(d.acts) >= maxActs {
		delete(d.actIDs, d.acts[0].a.ID)
		d.acts = slices.Delete(d.acts, 0, 1)
	}
	d.acts = append(d.acts, pageAct{d.actSeq, a})
	if d.actIDs == nil { // a Daemon a test built by hand
		d.actIDs = map[string]bool{}
	}
	d.actIDs[a.ID] = true
}

// recordSessionEventLocked keeps a session's start or stop, as the bus carried it. Caller holds mu.
func (d *Daemon) recordSessionEventLocked(e bus.Event) {
	if a, ok := sessionAct(e); ok {
		d.recordActLocked(a)
	}
}

// sessionAct is a session's start or stop as the Recent view shows it. A person's account has
// neither.
func sessionAct(e bus.Event) (web.Activity, bool) {
	if bus.IsPerson(e.From) {
		return web.Activity{}, false
	}
	switch e.Type {
	case bus.SessionStarted:
		return web.Activity{ID: e.ID, Kind: web.ActStarted, At: e.At, Addr: e.From, Agent: e.Agent, Branch: e.Branch}, true
	case bus.SessionStopped:
		return web.Activity{ID: e.ID, Kind: web.ActStopped, At: e.At, Addr: e.From}, true
	}
	return web.Activity{}, false
}

// noteSessionsLocked compares the sessions device dev's presence lists with what its presence
// before listed, and keeps what changed: a status line, tags, and, for another device, a branch a
// session newly committed to. A session seen for the first time has changed nothing. at is when
// the presence was written, by its writer's clock. Caller holds mu.
func (d *Daemon) noteSessionsLocked(dev string, sessions []bus.SessionInfo, at time.Time) {
	listed := map[string]bool{}
	for _, s := range sessions {
		if s.ID == bus.PersonSession {
			continue
		}
		addr := bus.Addr(dev, s.ID)
		listed[addr] = true
		now := sessSeen{doing: s.Doing, committed: s.Committed}
		for _, t := range s.Tags {
			now.tags = append(now.tags, t.Name)
		}
		sort.Strings(now.tags)
		was, known := d.actSeen[addr]
		if d.actSeen == nil {
			d.actSeen = map[string]sessSeen{}
		}
		d.actSeen[addr] = now
		if !known {
			continue
		}
		if now.doing != was.doing && now.doing != "" {
			d.recordActLocked(web.Activity{Kind: web.ActStatus, At: at, Addr: addr, Doing: now.doing})
		}
		added, removed := without(now.tags, was.tags), without(was.tags, now.tags)
		if len(added)+len(removed) > 0 {
			d.recordActLocked(web.Activity{Kind: web.ActTags, At: at, Addr: addr, Added: added, Removed: removed})
		}
		if dev != d.device {
			for _, b := range without(now.committed, was.committed) {
				d.recordActLocked(web.Activity{Kind: web.ActCommits, At: at, Addr: addr, Branch: b})
			}
		}
	}
	prefix := dev + "/"
	for addr := range d.actSeen {
		if len(addr) > len(prefix) && addr[:len(prefix)] == prefix && !listed[addr] {
			delete(d.actSeen, addr)
		}
	}
}

// without is the items of a that aren't in b, in a's order.
func without(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// recordCommitsLocked keeps the commits one tool call of session id made, as one activity for
// each branch: how many, and the last one's subject. Caller holds mu.
func (d *Daemon) recordCommitsLocked(id string, commits []attrib.Commit, at time.Time) {
	type group struct {
		n       int
		subject string
	}
	var order []string
	groups := map[string]*group{}
	for _, c := range commits {
		g := groups[c.Branch]
		if g == nil {
			g = &group{}
			groups[c.Branch] = g
			order = append(order, c.Branch)
		}
		g.n++
		g.subject = bus.OneLine(c.Subject, 120)
	}
	for _, b := range order {
		d.recordActLocked(web.Activity{Kind: web.ActCommits, At: at, Addr: bus.Addr(d.device, id), Branch: b,
			Commits: groups[b].n, Subject: groups[b].subject})
	}
}

// labelerLocked gives the name to show for an address: the session's or the person's, as known
// now, else the name the registry last had for it, else the address. Caller holds mu, for as
// long as it uses what this returns.
func (d *Daemon) labelerLocked() func(addr string) string {
	var byAddr map[string]string // the registry's names, for a session no longer in presence
	return func(addr string) string {
		if name, _, _ := d.sessionInfoLocked(addr); name != "" {
			return name
		}
		if name := d.personNameOfLocked(addr); name != "" {
			return name
		}
		if byAddr == nil {
			byAddr = d.namesByAddrLocked()
		}
		return nameOr(byAddr[addr], addr)
	}
}

// Activity returns what sessions did after cursor, oldest first as this daemon learned of it.
// The cursor is a message's kind (see cursor).
func (s pageSource) Activity(ctx context.Context, cursor string) ([]web.Activity, string, error) {
	d := s.d
	after, err := d.cursorSeq(cursor)
	if err != nil {
		return nil, "", err
	}
	d.awaitHistory(ctx)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pageSeenLocked(ctx, time.Now())

	after = min(after, d.actSeq)
	first := sort.Search(len(d.acts), func(i int) bool { return d.acts[i].seq > after })
	label := d.labelerLocked()
	out := make([]web.Activity, 0, len(d.acts)-first)
	for _, p := range d.acts[first:] {
		a := p.a
		a.Who = label(a.Addr)
		out = append(out, a)
	}
	return out, d.cursor(d.actSeq), nil
}
