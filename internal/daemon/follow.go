package daemon

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
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
// change. docs/VISION.md ("When a session stays the same session") has the reasons.

// uniqueKeyLocked is key, unless another live session has it, or has the name it would bring: two
// live sessions of one client session (two MCP servers in it) would share a name, and a name is an
// address, so the later one is a session of its own. The name matters as well as the key because a
// session that finds its T3 thread keeps the name under its old key too (see rekeyLocked), so the
// client's session ID brings the same name, though the session has another key now. Caller holds mu.
func (d *Daemon) uniqueKeyLocked(key, id string) string {
	name := d.st.NameKeys[key]
	for _, o := range d.sessions {
		if o.id != id && o.dropped.IsZero() && (o.key == key || (name != "" && o.name == name)) {
			return key + "/" + id
		}
	}
	return key
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
	d.saveLocked()
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
		if id == s.id || d.sessions[id] != nil || d.st.SessionNames[id] != name ||
			time.Since(d.st.Stopped[id]) > d.opt.StoppedInbox || len(d.st.Inbox[id]) == 0 {
			continue
		}
		for _, m := range d.st.Inbox[id] {
			if !seen[m.Event.ID] {
				seen[m.Event.ID] = true
				box = append(box, m)
			}
		}
		delete(d.st.Inbox, id)
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
	s.key = d.uniqueKeyLocked(nameKey(s.route.T3Thread, claude, s.route.Native, s.id), s.id)
	d.setNameLocked(s, d.nameForKeyLocked(s.key, s.id))
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
	at, ok := d.st.Stopped[id]
	return ok && time.Since(at) <= d.opt.StoppedInbox
}

// forwardLocked says where a message for the address to goes instead, "" if it stays. The registry
// records a name against the session it was first given to, and a session can later hold the name
// under another ID: a /resume back to an earlier conversation from a session whose ID came from
// another. A message for the first session, retired now, reached nobody once the new one stopped.
// This daemon knows who took the name last, so a message for a session here that isn't running (or
// is in its grace) goes to the session that holds, or last held, the name that session had last,
// if that session can receive it and the name is still the last it took. A holder that has moved
// to another conversation since gets nothing for the name it left (#46): the message stays in the
// kept inbox, which the next session to take the name takes (see adoptLocked). It goes by the
// address alone, which is all a message on the bus carries; docs/VISION.md ("A name held by
// another session") says what that misses. Caller holds mu.
func (d *Daemon) forwardLocked(to string) string {
	dev, id := bus.SplitAddr(to)
	if dev != d.device || id == "" || d.liveLocked(id) {
		return ""
	}
	name := d.st.SessionNames[id]
	holder := d.st.NameHolders[name]
	if holder == "" || holder == id || d.st.SessionNames[holder] != name || !d.receivesLocked(holder) {
		return ""
	}
	return bus.Addr(d.device, holder)
}

// queuedAtLocked is where a message for the address to is queued: to, or the session it is
// forwarded to. Caller holds mu.
func (d *Daemon) queuedAtLocked(to string) string {
	if f := d.forwardLocked(to); f != "" {
		return f
	}
	return to
}

// agentHookSessionsLocked finds the live sessions the hook of an agent other than Claude Code
// speaks for (req.Agent, #225): those whose conversation has the ID the hook reports, as their MCP
// server learned it (the route's Native: Codex's thread ID from its first tool call, Grok's session
// ID from its environment) or as an earlier hook of theirs taught the daemon (hookNative).
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
	if s := codexThreadSessionLocked(req, byProcess); s != nil {
		r := s.route
		r.Native = req.ClaudeSession
		s.hookNative = r.Native
		d.setRouteLocked(s, r)
		d.log.Printf("session %s is the Codex thread its hook named, by when its server started", bus.Addr(d.device, s.id))
		return []*session{s}
	}
	if len(byProcess) == 1 && byProcess[0].route.Native == "" && byProcess[0].hookNative == "" {
		byProcess[0].hookNative = req.ClaudeSession
		return byProcess
	}
	for _, s := range byProcess {
		s.hookSeen = true
	}
	return nil
}

// codexThreadWindow is how long after a Codex thread is made its MCP server may start and still be
// taken for that thread's (see codexThreadSessionLocked).
const codexThreadWindow = 3 * time.Second

// codexThreadSessionLocked is the session among a Codex hook's process's whose MCP server is the
// one Codex started for the hook's thread, where that can be told, else nil (#453). A Codex
// thread's ID is a UUIDv7, which holds when the thread was made, to the millisecond, and Codex
// starts a thread's MCP servers as it makes the thread: 0.1 to 0.5 s after it, on the laptop with
// Codex 0.164 and 0.167 (2026-11-12). So the session whose server started within
// codexThreadWindow after the thread was made is the thread's, if it is the only one of its
// process outside T3 with no thread yet that did.
//
// That covers what pairing by process alone can't: every terminal thread of Codex's shared app
// server has the same parent. A hook from a subagent can't take its parent's session: a subagent
// is made after its parent's server started, never before. A thread that was resumed, made long
// before its server started, isn't matched. Caller holds mu.
func codexThreadSessionLocked(req proto.Request, byProcess []*session) *session {
	if req.Agent != "codex" {
		return nil
	}
	made, ok := uuidV7Time(req.ClaudeSession)
	if !ok {
		return nil
	}
	var found *session
	for _, s := range byProcess {
		if s.route.Native != "" || s.route.T3CLI != "" || s.serverStarted.IsZero() {
			continue
		}
		if after := s.serverStarted.Sub(made); after < 0 || after > codexThreadWindow {
			continue
		}
		if found != nil {
			return nil // two threads made that close together: nothing tells them apart
		}
		found = s
	}
	return found
}

// uuidV7Time is when the UUIDv7 id was made, from its first 48 bits (RFC 9562).
func uuidV7Time(id string) (time.Time, bool) {
	hexOnly := strings.ReplaceAll(id, "-", "")
	if len(hexOnly) != 32 || hexOnly[12] != '7' {
		return time.Time{}, false
	}
	ms, err := strconv.ParseInt(hexOnly[:12], 16, 64)
	if err != nil || ms <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(ms), true
}

// isAgent says a session's agent, as its MCP server named it ("codex", or "codex in T3"), is agent.
func isAgent(sessionAgent, agent string) bool {
	return sessionAgent == agent || strings.HasPrefix(sessionAgent, agent+" ")
}

// toolOps are the requests an MCP server makes only when the agent calls one of Murmur's tools.
var toolOps = map[string]bool{proto.OpWho: true, proto.OpLookup: true, proto.OpStatus: true, proto.OpTags: true, proto.OpMute: true,
	proto.OpSend: true, proto.OpInbox: true}

// hookedAgents are the agents Murmur's hook runs in: those join installs it in (#202, #225), and Pi,
// whose extension join writes runs it (#205). OpenCode isn't one, though join's plugin runs the
// hook in OpenCode 2 (#431): OpenCode 1 registers under the same name and can have no plugin, so
// its sessions would be told to run join again, which changes nothing for them.
var hookedAgents = []string{"claude-code", "codex", "grok", "pi"}

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
