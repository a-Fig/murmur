package daemon

import (
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
type awaitingRead struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	Turn string    `json:"turn,omitempty"`
	At   time.Time `json:"at"`
}

// awaitReadLocked remembers that message e is being handed to session id as a turn whose text is
// turn, if it asked for receipts. It is done as the handoff begins, as the turn can start before
// the client's command returns. Entries past awaitingFor go, for every session, so a session that
// has gone leaves none behind for long. The caller saves the state. Caller holds mu.
func (d *Daemon) awaitReadLocked(id string, e bus.Event, turn string) {
	turn = turnStart(turn)
	if !e.Receipts || e.Type != bus.Message || turn == "" {
		return
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
		return
	}
	list = append(list, awaitingRead{ID: e.ID, From: e.From, Turn: turn, At: time.Now()})
	if over := len(list) - maxAwaiting; over > 0 {
		list = list[over:]
	}
	d.st.AwaitingRead[id] = list
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
// and forgets it. It says whether it marked one; the caller saves the state then. Caller holds mu.
func (d *Daemon) readOnPromptLocked(id, prompt string) bool {
	list := d.st.AwaitingRead[id]
	if prompt == "" || len(list) == 0 {
		return false
	}
	i := whichTurn(prompt, list)
	if i < 0 {
		return false
	}
	a := list[i]
	d.markLocked(id, bus.Event{Type: bus.Message, ID: a.ID, From: a.From, Receipts: true}, bus.MarkRead)
	d.forgetAwaitingLocked(id, a.ID)
	return true
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
