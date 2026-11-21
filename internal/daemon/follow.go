package daemon

import (
	"cmp"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// Following Claude Code's session ID (#9), and finding the holder of a name (#37).
//
// A Claude Code session's ID changes under it on /clear, /branch and a switch with /resume, and an
// MCP server's copy never does, so the daemon learns the live ID from the hook, which always has
// it, and pairs the hook with its sessions by process: the hook's CLAUDE_PID is the MCP server's
// parent. A session that follows keeps its Murmur ID and its inbox; its key, and so its name,
// change. docs/design/session-names.md ("When a session stays the same session") has the reasons.

// uniqueKeyLocked is key, unless another live session has it, or has the name it would bring: two
// live sessions of one client session (two MCP servers in it) would share a name, and a name is an
// address, so the later one is a session of its own. The name matters as well as the key because a
// session that finds its T3 thread keeps the name under its old key too (see rekeyLocked), so the
// client's session ID brings the same name, though the session has another key now.
//
// The later of two servers of one client process stays a session of its own for that key (Later,
// #38). Which of the two is the later was decided each time by who was live, so after both had
// dropped, or the daemon had restarted, the one that came back first took the plain key and its
// sibling's name, and the sibling, back to find them taken, got a new one: both names changed,
// and a message to either went to the other or to nobody. Only siblings, which pid, the client's
// process, tells from a second program on the same conversation: that one takes the plain key
// and the conversation's name once the first has gone, as before. Caller holds mu.
func (d *Daemon) uniqueKeyLocked(key, id string, pid int) string {
	if d.st.ThreadLeft[id] == key {
		return key + "/" + id // a thread it gave up (yieldThreadLocked)
	}
	if slices.Contains(d.st.Later[id], key) {
		return key + "/" + id // the later of two servers of its client, once
	}
	name := d.st.NameKeys[key]
	taken, sibling := false, false
	for _, o := range d.sessions {
		if o.id != id && o.dropped.IsZero() && (o.key == key || (name != "" && o.name == name)) {
			taken = true
			sibling = sibling || (o.key == key && o.clientPID == pid)
		}
	}
	if !taken {
		return key
	}
	if sibling {
		if d.st.Later == nil {
			d.st.Later = map[string][]string{}
		}
		// Each conversation it was the later on, not only the last: the two move together on a
		// /clear and a /resume, and back in one they left, it is still the later there.
		d.st.Later[id] = d.trimLaterLocked(id, append(d.st.Later[id], key))
		d.saveLocked()
	}
	return key + "/" + id
}

// laterKeys is how many conversations a session is remembered as the later of two on whose own
// key (key/id) has no name yet.
const laterKeys = 16

// trimLaterLocked is keys, session id's Later, with the oldest past laterKeys dropped, of those
// whose own key has no name. One that has a name is kept: NameKeys keeps it for good, and without
// the record the later, back first in that conversation, would take the plain key and its
// sibling's name. Caller holds mu.
func (d *Daemon) trimLaterLocked(id string, keys []string) []string {
	unnamed := 0
	for _, k := range keys {
		if d.st.NameKeys[k+"/"+id] == "" {
			unnamed++
		}
	}
	return slices.DeleteFunc(keys, func(k string) bool {
		if unnamed <= laterKeys || d.st.NameKeys[k+"/"+id] != "" {
			return false
		}
		unnamed--
		return true
	})
}

// yieldThreadLocked makes the live Codex sessions of other processes that hold key, a Codex
// thread's, give it up to session id of process pid, which has just shown the thread is its
// own, and returns their IDs. Codex keeps a thread's ID when another program resumes it, as the
// app does a conversation begun in the command line, and each program starts an MCP server of
// its own for it. With the first still open, the two were two live sessions of one key, and the
// later one kept the name it had registered with: the conversation had a new name for having
// moved (#568). The one that showed the thread last is where the conversation is, since a tool
// call carried the thread there, so it takes the name. The other is a session of its own from
// then on, with a new name, and stays one (ThreadLeft): it never says it has the thread again,
// having said so once.
//
// A Claude Code conversation does the same between the command line and T3 (#643), in either
// direction: T3 imports a command-line conversation and resumes its Claude session, and `claude
// --resume` goes on with one T3 had. Its keys differ in the two, the T3 thread's and the Claude
// session's, so it is known by its Claude session ID, claude, instead (and shareNameLocked gives
// the two keys one name).
//
// Only between processes: two servers of one Codex process on one thread are two MCP servers
// in one client session, and the later is the session of its own, as before. A session with no
// PID, or of an agent other than Codex outside T3 or Claude Code (a Codex one in T3 that hasn't
// found its T3 thread has a Codex key meanwhile), is left as it was. The caller, once id is the
// thread's session, gives it what the others had unread (takeInboxLocked). Caller holds mu.
func (d *Daemon) yieldThreadLocked(id, agent string, pid int, key, claude string) []string {
	codex := strings.HasPrefix(key, "codex:") && agent == "codex"
	claude = cmp.Or(claude, "-") // no session ID matches none
	// One that gave its conversation up never takes it back, having shown it once; a Claude
	// session's record goes at its server's next first registration (hello).
	if pid == 0 || !codex && !isClaudeCode(agent) || codex && d.st.ThreadLeft[id] == key || !codex && d.st.ThreadLeft[id] != "" ||
		slices.Contains(d.st.Later[id], key) { // the later of two servers on it won't take its plain key (uniqueKeyLocked)
		return nil
	}
	var left []string
	for _, o := range d.sortedSessionsLocked() {
		if o.id == id || !o.dropped.IsZero() || o.clientPID == 0 || o.clientPID == pid {
			continue
		}
		// A Claude Code conversation is the same one under either key it can have, its T3 thread's
		// or its Claude session's, as Claude keeps its session ID where T3 imports a conversation from
		// the command line or the command line resumes one T3 had (#643).
		if codex && (o.key != key || o.agent != "codex") || !codex && (o.claudeSession != claude || !isClaudeCode(o.agent) || d.st.ThreadLeft[o.id] != "") {
			continue // a Claude session that gave the conversation up once has it no more
		}
		if !codex && slices.Contains(d.st.Later[o.id], strings.TrimSuffix(o.key, "/"+o.id)) {
			// The later of two MCP servers of one client process, a session of its own with its own
			// name (Later, #38), has nothing to give up (Sol's review of 7e4ead79). Another session
			// of its own on the conversation, such as one resumed on the command line while T3 had
			// the name, is the conversation's only holder once T3 has stopped, and gives it up as any
			// holder does (Sol's review of 8d031b55).
			continue
		}
		if d.st.ThreadLeft == nil {
			d.st.ThreadLeft = map[string]string{}
		}
		gave := o.key
		d.st.ThreadLeft[o.id] = gave
		from := o.name
		o.key = d.uniqueKeyLocked(gave, o.id, o.clientPID)
		d.setNameLocked(o, d.nameForKeyLocked(o, o.key))
		if o.name == "" {
			d.kickNames()
		}
		addr := bus.Addr(d.device, o.id)
		d.noteLocked(time.Now(), fmt.Sprintf("%s's conversation went on in another program: %s -> %s", addr, from, cmp.Or(o.name, "(no name yet)")))
		d.log.Printf("session %s's conversation went on in another program, as session %s; it had the name %s, and has %s now",
			addr, bus.Addr(d.device, id), from, cmp.Or(o.name, "none yet"))
		left = append(left, o.id)
	}
	if len(left) > 0 {
		d.saveLocked()
		d.kickPresence()
	}
	return left
}

// isClaudeCode says agent is Claude Code, in T3 or not.
func isClaudeCode(agent string) bool {
	return agent == "claude-code" || agent == "claude-code in T3"
}

// shareNameLocked gives a Claude Code conversation's two name keys one name (#643): key, the one it
// registers with, and alias, its Claude session's, where key is its T3 thread's. T3 imports a
// conversation from the command line and resumes its Claude session; `claude --resume` goes on
// with one T3 had. Each key had a name of its own, so the conversation was named anew whichever
// way it moved. Where one key has a name and the other none, the other takes it; where both have
// one, each keeps its own, as the registry has them. id is the session sharing them. Caller holds
// mu.
func (d *Daemon) shareNameLocked(key, alias, id string) {
	if alias == "" || alias == key {
		return
	}
	k, a := d.st.NameKeys[key], d.st.NameKeys[alias]
	switch {
	case k == "" && a != "":
		d.st.NameKeys[key] = a
		// From the command line, unless the name is at session id's own address and was given to a
		// session in T3: one that registered before it knew its thread, and has just found it, gave
		// the name itself, and its thread's next session is to take that address (threadIDLocked,
		// Opus's review). Its ID is made from its Claude session, as the command line's is, so a
		// name the command line gave, which T3 imported, is at that address too. Where nothing says
		// which, as for a name from before #643, the thread keeps an address of its own: taking the
		// command line's would have the two ask for one ID (Sol's reviews).
		if d.st.Used[a] != bus.Addr(d.device, id) || !d.st.NamedInT3[a] {
			if d.st.NamedFromCLI == nil {
				d.st.NamedFromCLI = map[string]bool{}
			}
			d.st.NamedFromCLI[key] = true
		}
	case a == "" && k != "":
		d.st.NameKeys[alias] = k
	case k == "" && a == "":
		if d.assigning {
			d.pendingShares = append(d.pendingShares, pendingShare{key: key, alias: alias, id: id})
		}
		return
	default:
		return
	}
	d.saveLocked()
}

// shareSessionNameLocked gives s's name to the other key of its conversation, if it is a Claude Code
// one in a T3 thread: wherever its key is named, in hello, by the pool or the assigner, or by a
// hook's correction of its Claude session (#643). Caller holds mu.
func (d *Daemon) shareSessionNameLocked(s *session) {
	if t3, ok := strings.CutPrefix(s.key, "t3:"); ok && !strings.Contains(t3, "/") {
		d.shareNameLocked(s.key, claudeAlias(s.agent, t3, s.claudeSession), s.id)
	}
}

// claudeAlias is the other name key of a conversation whose key is from t3Thread: its Claude
// session's, if it is a Claude Code one in a T3 thread (see shareNameLocked), else "".
func claudeAlias(agent, t3Thread, claude string) string {
	if t3Thread == "" || claude == "" || !isClaudeCode(agent) {
		return ""
	}
	return "claude:" + claude
}

// noteNamedInT3Locked records that name was given first as the name of key to a session of agent,
// if that is a Claude Code session in T3 that hasn't found its thread, its key its Claude
// session's (see NamedInT3). The agent is the one the session had when it asked for a name: the
// assigner's name can land after the session has gone. The caller saves. Caller holds mu.
func (d *Daemon) noteNamedInT3Locked(agent, key, name string) {
	if agent != "claude-code in T3" || !strings.HasPrefix(key, "claude:") {
		return
	}
	if d.st.NamedInT3 == nil {
		d.st.NamedInT3 = map[string]bool{}
	}
	d.st.NamedInT3[name] = true
}

// keptForLocked is the name that what is sent to session id, which isn't running, is for: the
// last it took, unless it gave its Codex thread up to another program (yieldThreadLocked). The
// name it was given then was for a session that ends with its server, while the registry still
// has the thread's name at its address, which is where another device sends a message for the
// thread once no presence shows the name. Such a message is the thread's (#568). Caller holds mu.
func (d *Daemon) keptForLocked(id string) string {
	if name := d.st.NameKeys[d.st.ThreadLeft[id]]; name != "" {
		return name
	}
	return d.st.SessionNames[id]
}

// threadIDLocked is the session ID for a hello: h.ID, unless the server made it from its T3 thread
// (h.ByThread, #642) and the thread already has a name, which the registry has at a session of this
// device. That session's ID is the thread's then. A thread named before servers took their ID from
// it has its name at the address of the session that first had it, a Claude-derived one, and every
// device names that address and no other: another would show as a bare address on other devices
// once it stopped. The server takes the ID the reply's Self names. Caller holds mu.
func (d *Daemon) threadIDLocked(h *proto.Hello) string {
	if !h.ByThread || h.T3Thread == "" {
		return h.ID
	}
	key := nameKey(h.T3Thread, "", "", "")
	name := d.st.NameKeys[key]
	if name == "" || d.st.NamedFromCLI[key] {
		return h.ID // a name from the command line is at its session's address, which it goes on using (#643)
	}
	dev, id := bus.SplitAddr(d.st.Used[name])
	if dev != d.device || !validName(id) || id == proto.CLISession || id == bus.PersonSession {
		return h.ID
	}
	if last, ok := d.st.SessionNames[id]; ok && last != name {
		return h.ID // that session went on to another conversation, whose inbox it may hold
	}
	return id
}

// setNameLocked gives s its name, "" if it has none yet. A name that lands is also recorded as
// held by s, the last to take it, and as s's own last: that is how a message for the session the
// registry has the name down for finds s (see forwardLocked). s also takes what was kept for the
// name (see adoptLocked). Caller holds mu.
func (d *Daemon) setNameLocked(s *session, name string) {
	s.name = name
	if name == "" {
		return
	}
	d.adoptLeftLocked(s) // first: what it kept before stands over what it kept by its ID since
	d.shareSessionNameLocked(s)
	d.adoptTagsLocked(s)
	d.adoptMutesLocked(s)
	d.adoptExplainedLocked(s)
	adopted := d.adoptLocked(s, name)
	if !adopted && d.st.NameHolders[name] == s.id && d.st.SessionNames[s.id] == name {
		return
	}
	if d.st.NameHolders == nil {
		d.st.NameHolders = map[string]string{}
	}
	if d.st.SessionNames == nil {
		d.st.SessionNames = map[string]string{}
	}
	d.st.NameHolders[name], d.st.SessionNames[s.id] = s.id, name
	if d.st.NamesSeen == nil {
		d.st.NamesSeen = map[string]time.Time{}
	}
	d.st.NamesSeen[s.id] = time.Now().UTC().Truncate(time.Hour)
	d.saveLocked()
}

// adoptLeftLocked moves to s's name, now that it has landed, what its key kept under the name it
// had before this clone lost its keys and became a new device (NamesLeft, #827): the tags, unless
// the new name has some already, the mutes, added to its own up to what a session may have, and
// who has been explained to it. They are kept by name, and the old name stays on the old device
// (#617), so without this the session came back with none of them. Until now it went by the old
// name's (keptByLocked). The mutes other sessions here made of the old name become mutes of the
// new one: a session muted on this clone stays muted. Caller holds mu.
func (d *Daemon) adoptLeftLocked(s *session) {
	old := d.st.NamesLeft[s.key]
	if s.key == "" || old == "" || s.name == "" {
		return
	}
	delete(d.st.NamesLeft, s.key)
	defer d.saveLocked()
	if old == s.name {
		return
	}
	if st := d.st.Tags[old]; st != nil {
		delete(d.st.Tags, old)
		if d.st.Tags[s.name] == nil {
			d.st.Tags[s.name] = st
		}
	}
	if m := d.st.Mutes[old]; m != nil {
		delete(d.st.Mutes, old)
		have := d.st.Mutes[s.name]
		if have == nil {
			d.st.Mutes[s.name], m = m, nil
		}
		for _, name := range sortedKeys(m) {
			if _, ok := have[name]; ok {
				continue
			}
			if len(have) >= bus.MaxMuted {
				d.log.Printf("session %s muted %s as %s, on this clone's old device, and has %d mutes already; not kept",
					bus.Addr(d.device, s.id), name, old, len(have))
				continue
			}
			have[name] = m[name]
		}
	}
	if people, ok := d.st.Explained[old]; ok {
		delete(d.st.Explained, old)
		for _, p := range people {
			d.noteExplainedAtLocked(s.name, p)
		}
	}
	for _, by := range sortedKeys(d.st.Mutes) {
		m := d.st.Mutes[by]
		at, muted := m[old]
		if !muted {
			continue
		}
		delete(m, old)
		// Not a mute of itself, which it could have made of its old name while this one was pending.
		if _, ok := m[s.name]; !ok && by != s.name && by != tagKeyOf("", s.id) {
			m[s.name] = at
		}
		if len(m) == 0 {
			delete(d.st.Mutes, by)
		}
	}
	// The page's own choices for it follow, once nothing holds mu (moveSettings, #836).
	d.st.SettingsMoves = append(d.st.SettingsMoves, [2]string{old, s.name})
	d.settingsMove.Store(true)
	d.log.Printf("session %s, now %s, has what it kept as %s on this clone's old device", bus.Addr(d.device, s.id), s.name, old)
}

// sessionNameKeep is how long the name of a session that is gone is remembered once nothing else
// points at it: long enough that the page's history, and a teammate's agent that kept its address,
// have moved on.
const sessionNameKeep = 30 * 24 * time.Hour

// pruneSessionNamesLocked forgets the name each long-gone session took last (#747), and says
// whether it forgot any. One is kept while its session runs or its inbox is kept, while it is the
// name's holder (NameHolders), and while it is the session the registry has the name down for,
// which forwardLocked starts from. Past that, the name only labels the session's old messages on
// the page and in lookup, and forwards a message sent to its old address: after sessionNameKeep
// those show the address, and such a message is dropped as one to any session that is gone. A
// session with no time noted, from a state.json older than NamesSeen, counts from now. So does one
// kept for any of those reasons, whose time is renewed about once a day while it is: the 30 days
// run from when the last of them ended, not from when a session that then ran for weeks took its
// name. Caller holds mu.
func (d *Daemon) pruneSessionNamesLocked(now time.Time) bool {
	changed := false
	for id := range d.st.NamesSeen {
		if _, ok := d.st.SessionNames[id]; !ok {
			delete(d.st.NamesSeen, id)
			changed = true
		}
	}
	if d.st.NamesSeen == nil {
		d.st.NamesSeen = map[string]time.Time{}
	}
	for id, name := range d.st.SessionNames {
		_, stopped := d.st.Stopped[id]
		kept := d.sessions[id] != nil || stopped || d.st.NameHolders[name] == id || d.st.Used[name] == bus.Addr(d.device, id)
		seen, ok := d.st.NamesSeen[id]
		switch {
		case !ok, kept && now.Sub(seen) > 24*time.Hour:
			d.st.NamesSeen[id] = now.UTC().Truncate(time.Hour)
		case !kept && now.Sub(seen) > sessionNameKeep:
			delete(d.st.SessionNames, id)
			delete(d.st.NamesSeen, id)
		default:
			continue
		}
		changed = true
	}
	for k, ph := range d.st.PeerHolders {
		if now.Sub(ph.At) > sessionNameKeep {
			delete(d.st.PeerHolders, k)
			changed = true
		}
	}
	return changed
}

// adoptLocked moves into s's inbox the inboxes kept for sessions stopped within StoppedInbox whose
// last name was name, and says whether there were any. What is sent to a name whose sessions have
// all stopped waits in a stopped session's inbox: the registry's address, or the last holder's,
// to which forwardLocked sends it. But a conversation comes back as whichever session takes its
// name next, which need be neither: the registry's own ID when Claude Code resumes it from the
// command line (the server's ID is derived from the conversation's), or a third session that a
// hook moves to it. Without this, what was forwarded to the last holder would wait for a session
// that never returns. Caller holds mu.
func (d *Daemon) adoptLocked(s *session, name string) bool {
	box := d.st.Inbox[s.id]
	seen := map[string]bool{}
	for _, m := range box {
		seen[m.Event.ID] = true
	}
	var from []string
	for _, id := range sortedKeys(d.st.Stopped) {
		if id == s.id || d.sessions[id] != nil || !d.keptLocked(id) {
			continue
		}
		last := d.keptForLocked(id)
		// A message handed over as a turn has left the inbox, and its turn may start under s.
		if last == name && d.takeAwaitingLocked(s.id, id) {
			d.saveLocked()
		}
		// A message that says which name it was sent to waits for that name, which the session may
		// have left by a /clear since (#45); one that doesn't, for the name the session had last.
		var left []*inMsg
		took := false
		for _, m := range d.st.Inbox[id] {
			if nameOr(m.Event.ToName, last) != name {
				left = append(left, m)
				continue
			}
			took = true
			// It was queued under the mutes of the name the session had last; one for another name
			// is the new session's only if that name hasn't muted its sender (Sol's review). s may
			// not be among the sessions yet, so its mutes are found by the name it takes.
			if last != name && d.mutedHereKeyLocked(tagKeyOf(name, s.id), m.Event) {
				d.log.Printf("message %s is from %s, which %s has muted; dropped", m.Event.ID, m.Event.From, name)
				d.mutedropLocked(s.id, m.Event)
				continue
			}
			if !seen[m.Event.ID] {
				seen[m.Event.ID] = true
				box = append(box, m)
			}
		}
		if !took {
			continue
		}
		if len(left) == 0 {
			delete(d.st.Inbox, id)
		} else {
			d.st.Inbox[id] = left
		}
		from = append(from, bus.Addr(d.device, id))
	}
	if len(from) == 0 {
		return false
	}
	sort.SliceStable(box, func(i, j int) bool { return box[i].Arrived.Before(box[j].Arrived) })
	if len(box) > maxInbox {
		box = box[len(box)-maxInbox:]
	}
	d.st.Inbox[s.id] = box
	d.noteConversationLocked() // messages waiting for a conversation reached its live session
	d.log.Printf("session %s, taking the name %s, took what was kept for it at %v", bus.Addr(d.device, s.id), name, from)
	return true
}

// hookSessionsLocked finds the live sessions a hook speaks for, given the Claude Code session ID it
// reports and, if it has one, the PID of the Claude Code process it runs under.
//
// The sessions whose MCP servers that process started are the hook's, whatever ID they hold: the
// hook's is the live one, so each moves to it (followLocked). The PID is what pairs them, and it
// moves nothing between processes: only a session whose own PID is the hook's is touched. Where no
// session is the process's, because the hook has no PID, the session's server gave none, or a
// wrapper between them made the server's parent another process, the sessions with the hook's
// Claude ID are the hook's, as they were before PIDs were known. A session in its grace is
// neither: it is told of its notices once it is back. Another agent's hook is found by
// agentHookSessionsLocked.
//
// A subagent's hook (req.Subagent) reports the Claude ID the session had when the subagent started,
// which a /clear since may have left behind: its process's sessions are still its own, for what it
// commits, but none follows it, as that would take the session back to the conversation it left,
// with that one's name, tags and holds (#231). Caller holds mu.
func (d *Daemon) hookSessionsLocked(req proto.Request) []*session {
	if req.Child {
		d.noteHookChildLocked(req.Native, req.ParentNative)
	}
	if req.Native != "" && req.ClaudeSession == "" {
		return d.nativeSessionsLocked(req.Native) // Pi's, by its session's file (#205)
	}
	if req.Agent != "" {
		return d.agentHookSessionsLocked(req)
	}
	claude, pid, subagent := req.ClaudeSession, req.ClientPID, req.Subagent
	var ofProcess, ofID []*session
	for _, s := range d.sortedSessionsLocked() {
		if !s.dropped.IsZero() {
			continue
		}
		if pid != 0 && s.clientPID == pid {
			ofProcess = append(ofProcess, s)
		}
		if s.claudeSession == claude {
			ofID = append(ofID, s)
		}
	}
	if len(ofProcess) == 0 {
		return ofID
	}
	for _, s := range ofProcess {
		if s.claudeSession != claude && !subagent {
			d.followLocked(s, claude)
		}
	}
	return ofProcess
}

// nativeSessionsLocked finds the live sessions the hook of an agent other than Claude Code speaks
// for: those whose MCP server registered with native, the client's own ID for its conversation
// (Pi's session file, which Murmur's extension gives both, #205). Nothing follows: a Pi session
// that moves to another file (/new, /resume) starts its server again with the new one, a session
// of its own. Caller holds mu.
func (d *Daemon) nativeSessionsLocked(native string) []*session {
	if native == "" {
		return nil
	}
	var out []*session
	for _, s := range d.sortedSessionsLocked() {
		if s.dropped.IsZero() && s.claudeSession == "" && s.route.Native == native {
			out = append(out, s)
		}
	}
	return out
}

// followLocked moves s to the Claude Code session ID claude, which a hook of its process reported.
// Its key follows, and with it its name: the name that key has had, if it has had one (a /resume
// back to an earlier conversation), otherwise a new one, taken as any is. Everyone else is told by
// presence. Nothing goes on the bus: the session has not stopped or started. Caller holds mu.
func (d *Daemon) followLocked(s *session, claude string) {
	from := s.name
	s.claudeSession = claude
	s.key = d.uniqueKeyLocked(nameKey(s.route.T3Thread, claude, s.route.Native, s.id), s.id, s.clientPID)
	d.setNameLocked(s, d.nameForKeyLocked(s, s.key))
	if s.name == "" {
		d.kickNames()
	}
	d.kickPresence()

	// The log is local, so it can say the names but not the Claude IDs, which are never published.
	addr := bus.Addr(d.device, s.id)
	shown := func(name string) string {
		if name == "" {
			return "(no name yet)"
		}
		return name
	}
	d.noteLocked(time.Now(), fmt.Sprintf("%s followed Claude Code to another conversation: %s -> %s",
		addr, shown(from), shown(s.name)))
	d.log.Printf("session %s followed Claude Code to another conversation (%s -> %s)", addr, shown(from), shown(s.name))
}

// receivesLocked says whether a message can be put in id's inbox: it is registered here, or it
// stopped within StoppedInbox. Caller holds mu.
func (d *Daemon) receivesLocked(id string) bool {
	if d.sessions[id] != nil {
		return true
	}
	return d.keptLocked(id)
}

// forwardLocked says where a message for the address to goes instead, "" if it stays. The registry
// records a name against the session it was first given to, and a session can later hold the name
// under another ID: a /resume back to an earlier conversation from a session whose ID came from
// another. A message for the first session, retired now, reached nobody once the new one stopped.
// This daemon knows who took the name last, so a message for a session here that isn't running (or
// is in its grace) goes to the session that holds, or last held, the name that session had last,
// if that session can receive it and the name is still the last it took. A holder that has moved
// to another conversation since gets nothing for the name it left (#46): the message stays in the
// kept inbox, which the next session to take the name takes (see adoptLocked). Caller holds mu.
func (d *Daemon) forwardLocked(to string) string { return d.forwardNamedLocked(to, "") }

// forwardNamedLocked is forwardLocked for a message sent to the name name, "" if it wasn't sent to
// one or its sender didn't say (#45). The registry has a name at the address of the session that
// first took it, which may hold another name since, after a /clear: a message sent to a name it has
// left goes by that name, to the session that holds it, rather than by the name the address has
// now, even while the session at the address runs. If no other session has taken the name since,
// the message stays at the address. Caller holds mu.
func (d *Daemon) forwardNamedLocked(to, name string) string {
	dev, id := bus.SplitAddr(to)
	if dev != d.device || id == "" {
		return ""
	}
	if id == bus.PersonSession || id == proto.CLISession {
		name = "" // a person's or the command line's messages are theirs, whatever name they carry
	}
	if name == "" {
		if d.liveLocked(id) {
			return ""
		}
		name = d.keptForLocked(id)
	} else if s := d.sessions[id]; s != nil && s.dropped.IsZero() && s.name == name {
		return "" // it holds the name now, though it may not have saved that yet
	}
	holder := d.st.NameHolders[name]
	if holder == "" || holder == id || d.st.SessionNames[holder] != name || !d.receivesLocked(holder) {
		return ""
	}
	return bus.Addr(d.device, holder)
}

// queuedAtLocked is where a message for the address to is queued: to, or the session it is
// forwarded to. Caller holds mu.
func (d *Daemon) queuedAtLocked(to string) string { return d.queuedAtNamedLocked(to, "") }

// queuedAtNamedLocked is queuedAtLocked for a message sent to the name name (see
// forwardNamedLocked). Caller holds mu.
func (d *Daemon) queuedAtNamedLocked(to, name string) string {
	if f := d.forwardNamedLocked(to, name); f != "" {
		return f
	}
	return to
}

// agentHookSessionsLocked finds the live sessions the hook of an agent other than Claude Code
// speaks for (req.Agent, #225): those whose conversation has the ID the hook reports, as their MCP
// server learned it (the route's Native: Codex's thread ID from its first tool call, Grok's session
// ID and Muse's from their environment) or as an earlier hook of theirs taught the daemon
// (hookNative).
//
// Codex's MCP server learns its thread's ID only from a call to one of Murmur's tools, so a session
// whose agent hasn't called one is found by its process instead: the agent that ran the hook started
// the session's MCP server, and is among the hook's ancestors. Where that process has one session
// of the agent, with no ID yet, it takes the hook's. Where it has several (T3 runs every Codex
// thread in one app server), nothing tells the hook's apart, even when only one has no ID: the hook
// speaks for none of them, but each is marked as having its hook run, which is true of every
// session of that process. An ID the MCP server learns overrides one taken from a hook. Caller
// holds mu.
func (d *Daemon) agentHookSessionsLocked(req proto.Request) []*session {
	var byID, byProcess []*session
	for _, s := range d.sortedSessionsLocked() {
		if !s.dropped.IsZero() || !isAgent(s.agent, req.Agent) {
			continue
		}
		if s.route.Native != "" && s.hookNative != s.route.Native {
			s.hookNative = "" // the session's own ID overrides one taken from a hook, which may be wrong
		}
		switch {
		case s.route.Native == req.ClaudeSession || s.route.Native == "" && s.hookNative == req.ClaudeSession:
			byID = append(byID, s)
		case s.clientPID != 0 && slices.Contains(req.Ancestors, s.clientPID):
			byProcess = append(byProcess, s)
		}
	}
	if len(byID) > 0 {
		return byID
	}
	if len(byProcess) == 1 && byProcess[0].route.Native == "" && byProcess[0].hookNative == "" {
		s := byProcess[0]
		s.hookNative = req.ClaudeSession
		if isAgent(s.agent, "antigravity") && s.hookNative != "" && s.route.T3CLI != "" && s.route.T3Thread == "" {
			d.kickThreads()
		}
		return byProcess
	}
	for _, s := range byProcess {
		s.hookSeen = true
	}
	return nil
}

// isAgent says a session's agent, as its MCP server named it ("codex", or "codex in T3"), is agent.
func isAgent(sessionAgent, agent string) bool {
	return sessionAgent == agent || strings.HasPrefix(sessionAgent, agent+" ")
}

// toolOps are the requests an MCP server makes only when the agent calls one of Murmur's tools.
var toolOps = map[string]bool{proto.OpWho: true, proto.OpLookup: true, proto.OpStatus: true, proto.OpTags: true, proto.OpMute: true,
	proto.OpSend: true, proto.OpInbox: true}

// hookedAgents are the agents Murmur's hook runs in: those join installs it in (#202, #225, #461),
// and Pi, whose extension join writes runs it (#205). OpenCode isn't one, though join's plugin runs the
// hook in OpenCode 2 and, outside T3, OpenCode 1 (#431, #446): join writes no plugin for an
// OpenCode whose version it can't learn, and OpenCode 1's plugin does nothing in T3, so a session
// there would be told to run join again, which changes nothing for it.
var hookedAgents = []string{"claude-code", "codex", "grok", "pi", "antigravity", "muse"}

// hookOffLocked says Murmur's hook doesn't run for session s (#225): its agent is one join installs
// the hook in, it called one of Murmur's tools more than hookGrace ago, and no hook of it has
// reported since this daemon started. The hook runs after every tool call and at every prompt, so
// one that never reports isn't running: the agent never approved it, its approval broke (Codex's,
// which join writes, if Codex changed how it hashes a hook), or it fails. It is told from this side
// because the trust join writes can break with no error anywhere. Caller holds mu.
func (d *Daemon) hookOffLocked(s *session) bool {
	if s.hookSeen || s.toolCalled.IsZero() || time.Since(s.toolCalled) < hookGrace {
		return false
	}
	for _, a := range hookedAgents {
		if isAgent(s.agent, a) {
			return true
		}
	}
	return false
}

// hookGrace is how long after a session's first call to one of Murmur's tools its hook may take to
// report before the session counts as having none: the hook runs right after the call, but the
// session's first call may come at the start of a turn whose prompt preceded the hook's install.
const hookGrace = time.Minute

// peerHookOffLocked is hookOffLocked for the session at dev/sess, on this device or, as its
// presence says, another, and the session's agent. Caller holds mu.
func (d *Daemon) peerHookOffLocked(dev, sess string) (off bool, agent string) {
	if dev == d.device {
		if s := d.sessions[sess]; s != nil {
			return d.hookOffLocked(s), s.agent
		}
		return false, ""
	}
	if p := d.peers[dev]; p != nil {
		for _, s := range p.pres.Sessions {
			if s.ID == sess {
				return s.HookOff, s.Agent
			}
		}
	}
	return false, ""
}
