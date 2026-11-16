package daemon

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A message handed over as a turn is read when that turn starts (#611). Murmur hands it to the
// client (T3, Claude Code's socket, a client's command line) and can't see the turn start, so it is
// marked handed over. Where the hook runs, the turn's start fires UserPromptSubmit, whose prompt is
// the turn: once a prompt is the turn of a message handed to the session, the message is read. A
// prompt the user typed while the client held Murmur's turn in its queue isn't, so it doesn't
// count. Only messages that asked for receipts are remembered, as nothing else has a mark to move.

// awaitingFor is how long a message handed over as a turn waits for a prompt that holds it before
// it is forgotten, and stays handed over: a turn T3 queued behind a long one can wait hours.
const awaitingFor = 24 * time.Hour

// maxAwaiting caps the messages one session waits on so, the newest kept.
const maxAwaiting = 20

// awaitingRead is a message on its way into a session as a turn, or handed over as one, and not yet
// seen in a prompt: what markLocked needs of it, and the turn's text, which a prompt is matched by.
// The text, not the subject: a subject can start another's ("Deploy", "Deploy now") or repeat it,
// so a cancelled turn's message took the mark of the next turn whose subject began with it (#651).
// The text holds the sender, the subject and the body, so two messages share it only when nothing
// tells their turns apart, or, for turns past 4 KB, nothing in their first 4 KB (whichTurn). An
// entry saved before #651 has no text, matches no prompt, and goes after awaitingFor.
//
// Batch is the event ID of the first message of a turn that carries several (batchLocked, #699),
// "" for a turn of one. Its first entry holds the turn's text and the others none, so one prompt
// matches the turn once, and marks every message of it read.
//
// For is the session the message was handed to, where the entry has since moved to the session
// that replaced it (takeAwaitingLocked), "" where it hasn't: the mark is made as that session's,
// which is the one the sender has the message down for.
type awaitingRead struct {
	ID    string    `json:"id"`
	From  string    `json:"from"`
	Turn  string    `json:"turn,omitempty"`
	Batch string    `json:"batch,omitempty"`
	For   string    `json:"for,omitempty"`
	At    time.Time `json:"at"`
}

// awaitReadLocked remembers that message e is being handed to session id as a turn whose text is
// turn, if it asked for receipts, and says whether it did. batch is the turn's, if it carries
// several messages (see awaitingRead.Batch), and then turn may be "", for all but its first entry.
// It is done as the handoff begins, as the turn can start before the client's command returns.
// Entries past awaitingFor go, for every session, so a session that has gone leaves none behind for
// long. The caller saves the state. Caller holds mu.
func (d *Daemon) awaitReadLocked(id string, e bus.Event, turn, batch string) bool {
	turn = turnStart(turn)
	if !e.Receipts || e.Type != bus.Message || turn == "" && batch == "" {
		return false
	}
	if d.st.AwaitingRead == nil {
		d.st.AwaitingRead = map[string][]awaitingRead{}
	}
	for sess, list := range d.st.AwaitingRead {
		if list = slices.DeleteFunc(list, func(a awaitingRead) bool { return time.Since(a.At) > awaitingFor }); len(list) == 0 {
			delete(d.st.AwaitingRead, sess)
		} else {
			d.st.AwaitingRead[sess] = list
		}
	}
	list := d.st.AwaitingRead[id]
	if slices.ContainsFunc(list, func(a awaitingRead) bool { return a.ID == e.ID }) {
		return false
	}
	list = append(list, awaitingRead{ID: e.ID, From: e.From, Turn: turn, Batch: batch, At: time.Now()})
	d.st.AwaitingRead[id] = newestAwaiting(list)
	return true
}

// newestAwaiting is the newest maxAwaiting entries of list, which is in the order they were made.
func newestAwaiting(list []awaitingRead) []awaitingRead {
	over := len(list) - maxAwaiting
	if over <= 0 {
		return list
	}
	// An entry that holds a batch's text gives it to the first of its batch that stays: the rest
	// would otherwise match no prompt (Sol's review of #701).
	for _, gone := range list[:over] {
		if gone.Batch == "" || gone.Turn == "" {
			continue
		}
		if i := slices.IndexFunc(list[over:], func(a awaitingRead) bool { return a.Batch == gone.Batch }); i >= 0 {
			list[over+i].Turn = gone.Turn
		}
	}
	return list[over:]
}

// takeAwaitingLocked moves what session from waits on to session to, which replaces it, and says
// whether there was any (#744). A message handed to a T3 thread through a session in its grace, or
// a stopped one's route, starts its turn under the thread's new session, whose hook's prompt is
// looked up under that session's ID: left behind, the entry matched nothing, the message stayed
// handed over, and a sender that asked to be told when it was read heard only at the timeout. The
// inbox moves the same way (takeInboxLocked, adoptLocked). The caller saves the state. Caller
// holds mu.
func (d *Daemon) takeAwaitingLocked(to, from string) bool {
	old := d.st.AwaitingRead[from]
	if len(old) == 0 || to == from {
		return false
	}
	list := d.st.AwaitingRead[to]
	for _, a := range old {
		if slices.ContainsFunc(list, func(b awaitingRead) bool { return b.ID == a.ID }) {
			continue
		}
		if a.For == "" {
			a.For = from
		}
		list = append(list, a)
	}
	slices.SortStableFunc(list, func(a, b awaitingRead) int { return a.At.Compare(b.At) })
	delete(d.st.AwaitingRead, from)
	d.st.AwaitingRead[to] = newestAwaiting(list)
	return true
}

// forgetAwaitingLocked drops message msg from what session id waits on, as its handoff failed.
// Caller holds mu.
func (d *Daemon) forgetAwaitingLocked(id, msg string) {
	list := slices.DeleteFunc(d.st.AwaitingRead[id], func(a awaitingRead) bool { return a.ID == msg })
	if len(list) == 0 {
		delete(d.st.AwaitingRead, id)
	} else {
		d.st.AwaitingRead[id] = list
	}
}

// readOnPromptLocked marks read the message handed to session id as a turn that prompt is (whichTurn),
// or every message of it if it carried several, and forgets them. It says whether it marked one; the
// caller saves the state then. Caller holds mu.
func (d *Daemon) readOnPromptLocked(id, prompt string) bool {
	list := d.st.AwaitingRead[id]
	if prompt == "" || len(list) == 0 {
		return false
	}
	i := whichTurn(prompt, list)
	if i < 0 {
		return false
	}
	hit := list[i]
	for _, a := range slices.Clone(list) { // forgetAwaitingLocked changes list in place
		if a.ID == hit.ID || hit.Batch != "" && a.Batch == hit.Batch {
			d.markLocked(cmp.Or(a.For, id), bus.Event{Type: bus.Message, ID: a.ID, From: a.From, Receipts: true}, bus.MarkRead)
			d.forgetAwaitingLocked(id, a.ID)
		}
	}
	return true
}

// heldPrompt is a prompt the hook sent while no session of its client was connected, and when.
// claude is the conversation's ID: Claude Code's session ID, or, where agent is set, the ID of
// another agent's conversation (Codex's thread, Grok's session), which its session knows as its
// route's Native.
type heldPrompt struct {
	claude, prompt string
	pid            int
	agent          bool
	at             time.Time
}

// holdPromptLocked keeps a prompt that found no connected session of its client. A daemon that
// restarts knows a session only once its MCP server says hello again, and one in its grace isn't
// connected; a turn handed over before can start in that time, and its prompt would be lost, the
// message left handed over (#658). It is kept for SessionGrace, as long as a session has to come
// back, and matched when one of its client does (readHeldPromptsLocked). It is kept in memory
// only: a daemon that restarts again in that minute loses it. Caller holds mu.
func (d *Daemon) holdPromptLocked(req proto.Request) {
	if req.Prompt == "" || req.Subagent || (req.ClaudeSession == "" && req.ClientPID == 0) {
		return
	}
	d.heldPrompts = slices.DeleteFunc(d.heldPrompts, func(h heldPrompt) bool {
		return time.Since(h.at) > d.opt.SessionGrace
	})
	d.heldPrompts = append(d.heldPrompts, heldPrompt{claude: req.ClaudeSession, prompt: req.Prompt,
		pid: req.ClientPID, agent: req.Agent != "", at: time.Now()})
	if over := len(d.heldPrompts) - maxAwaiting; over > 0 {
		d.heldPrompts = d.heldPrompts[over:]
	}
}

// readHeldPromptsLocked matches the prompts held for session s's client, now it is connected,
// against what s waits on. One that marks a message read goes; one that marks nothing waits out
// SessionGrace, as the session it is for may not be the first of its client to come back (two
// servers in one process, or a second process resumed on the conversation). It says whether it
// marked one read; the caller saves the state then. Caller holds mu.
func (d *Daemon) readHeldPromptsLocked(s *session) bool {
	read := false
	d.heldPrompts = slices.DeleteFunc(d.heldPrompts, func(h heldPrompt) bool {
		if time.Since(h.at) > d.opt.SessionGrace {
			return true
		}
		if !h.of(s) || !d.readOnPromptLocked(s.id, h.prompt) {
			return false
		}
		read = true
		return true
	})
	return read
}

// of says whether h was sent by session s's client. Caller holds mu.
func (h heldPrompt) of(s *session) bool {
	if h.agent {
		return h.claude != "" && (h.claude == s.route.Native || h.claude == s.hookNative)
	}
	return (h.claude != "" && h.claude == s.claudeSession) || (h.pid != 0 && h.pid == s.clientPID)
}

// whichTurn is where in list the turn that prompt is stands, or -1. A prompt is one turn, so it is
// one entry at most: the oldest it holds whole, as the client takes turns in the order they were
// handed over. The hook cuts a prompt at proto.MaxPrompt bytes, and a client may put lines before
// the turn, so a cut prompt can hold only a turn's start; that counts too, but only where no other
// entry starts the same way. Two turns that start alike ("Deploy", "Deploy now"), cut before they
// part, can't be told apart, and marking the oldest could mark the wrong one (#651).
func whichTurn(prompt string, list []awaitingRead) int {
	cut := len(prompt) > proto.MaxPrompt-utf8.UTFMax
	started := -1
	for i, a := range list {
		whole, start := isTurn(prompt, a.Turn, cut)
		switch {
		case whole:
			return i
		case start && started == -1:
			started = i
		case start:
			started = -2
		}
	}
	return max(started, -1)
}

// turnStart is as much of a turn's text as a prompt can show of it: the hook sends a prompt's first
// proto.MaxPrompt bytes, so no more is kept. It is cut where a character starts, as the state is
// saved as JSON, which would change a cut character.
func turnStart(turn string) string {
	turn = strings.TrimSpace(strings.ReplaceAll(turn, "\r\n", "\n"))
	if len(turn) <= proto.MaxPrompt {
		return turn
	}
	cut := proto.MaxPrompt
	for cut > 0 && !utf8.RuneStart(turn[cut]) {
		cut--
	}
	return turn[:cut]
}

// isTurn says prompt holds the turn whose text, as turnStart kept it, is turn: whole, from where
// the turn's header line ("Message from quiet-otter via Murmur:") starts; or, for a prompt the hook
// cut, only its start, past the header. A turn turnStart cut is only ever held as its start, as two
// long turns can share their first 4 KB. The header may come after other lines, should a client put
// anything before the turn. A prompt the user typed while the client held Murmur's turn in its
// queue doesn't hold the header, so it isn't the turn.
func isTurn(prompt, turn string, cut bool) (whole, start bool) {
	header, _, _ := strings.Cut(turn, "\n")
	if header == "" || len(header) == len(turn) {
		return false, false
	}
	long := len(turn) > proto.MaxPrompt-utf8.UTFMax
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	for from := 0; ; {
		i := strings.Index(prompt[from:], header)
		if i < 0 {
			return false, start
		}
		rest := strings.TrimSpace(prompt[from+i:])
		if strings.HasPrefix(rest, turn) && !long {
			return true, false
		}
		start = start || len(rest) > len(header) && (strings.HasPrefix(rest, turn) || cut && strings.HasPrefix(turn, rest))
		from += i + len(header)
	}
}
