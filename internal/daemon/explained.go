package daemon

import (
	"slices"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A person's message explains, in full, that the person sees only what the agent sends, and how to
// answer, the first time a session is given one from that person (#249). After that it ends with
// the line an agent's message does (#345, Tyler, 2026-11-09): the agent has been told, and a long
// line on every message is noise. Every route that gives a session a message whole asks
// explainedLocked: a turn by T3 or the messaging socket, the hook's whole message, the Stop hook's,
// and inbox. A notice gives the subject alone, with no line, and doesn't count.
//
// What a session has been told is kept in state.json by the session's name, as its tags are (see
// tags.go), so it outlasts the session's MCP server restarting, a resume, and the daemon
// restarting. A session whose name is pending keeps it by its conversation (the name key, "conv:"
// and the key) until the name lands, not by its ID as its tags are: a session that /clears into
// another conversation keeps its ID, and that conversation hasn't been told anything.
//
// A message counts once it has reached the session: a turn whose handoff failed, and comes back by
// the hook, explains again. So does another message from the same person that inbox or the hook
// gives while a turn's handoff that explains is under way, as the handoff is recorded only once it
// ends. An agent told twice is the cost, and the safe way to err, where recording first would leave
// one whose handoff failed told never. It is rare: a handoff goes to a session that was idle when
// it began, and typically takes seconds, though T3's can take up to 30.
//
// In a group, a person's message ends with the group's line, not their own (#484), so it doesn't
// count as explaining them: their first direct message still does.
//
// A group's messages are explained the same way (#184, Tyler, 2026-11-12): the first a session is
// given from a group says in full that it is in the group, how to answer the group or the sender,
// and how to leave; later ones end with one short line. What is kept is the group's tag, beside the
// people's addresses, which it can't be taken for. A session that leaves a group and joins it again
// has been told.

// conversationLocked is the name key of the conversation session id is in: a registered session's
// own, else, for a session of T3 that has stopped, its thread's; "" if neither is known. Caller
// holds mu.
func (d *Daemon) conversationLocked(id string) string {
	if s := d.sessions[id]; s != nil {
		return s.key
	}
	if r, ok := d.st.T3Routes[id]; ok && r.Thread != "" {
		return nameKey(r.Thread, "", "", id)
	}
	return ""
}

// explainKeyLocked is what the people session id has been told of are kept by: its name, else its
// conversation's while the name is pending, else its ID; "" for the command line, which keeps
// nothing. Caller holds mu.
func (d *Daemon) explainKeyLocked(id string) string {
	if id == "" || id == proto.CLISession {
		return ""
	}
	if s := d.sessions[id]; s != nil && s.name != "" {
		return s.name
	}
	conv := d.conversationLocked(id)
	if name := d.st.NameKeys[conv]; conv != "" && name != "" {
		return name
	}
	if d.sessions[id] == nil {
		if name := d.st.SessionNames[id]; name != "" {
			return name
		}
	}
	if conv != "" {
		return "conv:" + conv
	}
	return tagKeyOf("", id)
}

// explains says what is a person's address or a group's tag: the two things a session is told of
// in full once.
func explains(what string) bool { return bus.IsPerson(what) || bus.IsGroup(what) }

// explainedLocked says session id has had a message from person from, or sent to group from, so
// another needs no more than the short line. Always true for anything else: an agent's own line is
// short already. Caller holds mu.
func (d *Daemon) explainedLocked(id, from string) bool {
	if !explains(from) {
		return true
	}
	key := d.explainKeyLocked(id)
	return key != "" && slices.Contains(d.st.Explained[key], from)
}

// noteExplainedLocked records that session id has had a message from from, if from is a person.
// The caller saves the state. Caller holds mu.
func (d *Daemon) noteExplainedLocked(id, from string) {
	d.noteExplainedAtLocked(d.explainKeyLocked(id), from)
}

// noteExplainedAtLocked records that the session kept by key has had a message from from, if
// from is a person, or sent to it, if it is a group. The caller saves the state. Caller holds mu.
func (d *Daemon) noteExplainedAtLocked(key, from string) {
	if key == "" || !explains(from) || slices.Contains(d.st.Explained[key], from) {
		return
	}
	if d.st.Explained == nil {
		d.st.Explained = map[string][]string{}
	}
	d.st.Explained[key] = append(d.st.Explained[key], from)
}

// explainKeyOfPushLocked is what a turn's handoff, made for session j.id while it was in
// conversation j.conv and was kept by j.explainKey, records against when it succeeds: always that
// conversation, which the turn went to, even if the session has /cleared or /resumed into another
// since. By the conversation's name, if that has landed meanwhile; else as it was kept then, which
// the name adopts when it lands (see adoptExplainedLocked). Caller holds mu.
func (d *Daemon) explainKeyOfPushLocked(j *pushJob) string {
	if d.conversationLocked(j.id) == j.conv {
		return d.explainKeyLocked(j.id) // the same conversation, by what it is kept by now
	}
	if name := d.st.NameKeys[j.conv]; j.conv != "" && name != "" {
		return name
	}
	return j.explainKey
}

// explainViewsLocked sets each of views' Explained, in order, and records the sessions as told: a
// message is rendered as from a person already explained only if every session it is given to has
// been (to says which those are), since the hook speaks for several sessions at once and shows a
// message once, as wholeViewsLocked does for Introduced. A second message from the same person in
// views is explained already by the first. The caller saves the state. Caller holds mu.
func (d *Daemon) explainViewsLocked(views []proto.MessageView, to func(i int) []string) {
	// known says every session of ids has been told of what, and records them as told.
	known := func(ids []string, what string) bool {
		all := true
		for _, id := range ids {
			if !d.explainedLocked(id, what) {
				all = false
			}
		}
		for _, id := range ids {
			d.noteExplainedLocked(id, what)
		}
		return all
	}
	for i := range views {
		// An agent's line is the short one already, and in a group the group's line stands in for
		// a person's (#484): the person is still to be explained in their first direct message.
		if bus.IsPerson(views[i].From) && !bus.IsGroup(views[i].To) {
			views[i].Explained = known(to(i), views[i].From)
		}
		if bus.IsGroup(views[i].To) {
			views[i].GroupKnown = known(to(i), views[i].To)
		}
	}
}

// adoptExplainedLocked moves what session s's conversation kept while its name was pending, and
// what s kept by its ID with no conversation known, to the name once it lands. Caller holds mu.
func (d *Daemon) adoptExplainedLocked(s *session) {
	if s.name == "" {
		return
	}
	moved := false
	for _, pending := range []string{"conv:" + s.key, tagKeyOf("", s.id)} {
		people, ok := d.st.Explained[pending]
		if !ok {
			continue
		}
		delete(d.st.Explained, pending)
		for _, p := range people {
			d.noteExplainedAtLocked(s.name, p)
		}
		moved = true
	}
	if moved {
		d.saveLocked()
	}
}
