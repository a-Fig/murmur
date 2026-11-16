package daemon

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
)

// A message handed to a session as a turn is read when the turn starts under the session that
// replaced it (#744): what the old session waits on moves with its inbox, and the mark is made as
// the old session's, which is the one the sender has the message down for. Left behind, the new
// session's prompt matched nothing.
func TestATurnThatStartsUnderTheSessionThatReplacedItsOwnIsRead(t *testing.T) {
	t.Parallel()
	d := &Daemon{device: "here-0001", log: log.New(io.Discard, "", 0), sessions: map[string]*session{},
		opt: Options{StoppedInbox: time.Hour, SessionGrace: time.Minute}, dir: t.TempDir(),
		st: state{Inbox: map[string][]*inMsg{}, Stopped: map[string]time.Time{}}}
	d.mu.Lock()
	defer d.mu.Unlock()
	msg := func(id string) bus.Event {
		return bus.Event{Type: bus.Message, ID: id, From: "there-0002/ssssss", Receipts: true}
	}
	read := func(id string) string {
		for _, m := range d.st.Marks {
			if m.Msg == id && m.State == bus.MarkRead {
				return m.To
			}
		}
		return ""
	}

	// A T3 thread's new session takes over from the one in its grace.
	turn := "Message from keen-pike via Murmur:\n\nfirst"
	d.awaitReadLocked("oldold", msg("m1"), turn, "")
	if d.readOnPromptLocked("newnew", turn) {
		t.Fatal("the new session's prompt matched before it took anything over")
	}
	d.takeInboxLocked(&session{id: "newnew"}, "oldold")
	if !d.readOnPromptLocked("newnew", turn) || read("m1") != "oldold" {
		t.Errorf("after the takeover: marks %+v, waiting %+v", d.st.Marks, d.st.AwaitingRead)
	}
	if len(d.st.AwaitingRead) != 0 {
		t.Errorf("still waiting on %+v", d.st.AwaitingRead)
	}

	// A stopped session's name is taken by another: its inbox is empty, as the message was
	// handed over, but what it waits on is taken all the same.
	turn2 := "Message from keen-pike via Murmur:\n\nsecond"
	d.awaitReadLocked("stopped", msg("m2"), turn2, "")
	d.st.Stopped["stopped"] = time.Now()
	d.st.SessionNames = map[string]string{"stopped": "quiet-otter"}
	d.adoptLocked(&session{id: "thirdd"}, "quiet-otter")
	if !d.readOnPromptLocked("thirdd", turn2) || read("m2") != "stopped" {
		t.Errorf("after the name was taken: marks %+v, waiting %+v", d.st.Marks, d.st.AwaitingRead)
	}

	// The prompt comes first, and the takeover after it: a Codex server in T3 learns its thread
	// only at its first tool call.
	turn4 := "Message from keen-pike via Murmur:\n\nfourth"
	late := &session{id: "lateee"}
	d.sessions["lateee"] = late
	d.awaitReadLocked("before", msg("m4"), turn4, "")
	if d.readOnPromptLocked("lateee", turn4) {
		t.Fatal("the prompt matched before the takeover")
	}
	d.missedPromptLocked("lateee", turn4)
	d.takeInboxLocked(late, "before")
	if read("m4") != "before" || late.missedPrompt != "" {
		t.Errorf("a prompt from before the takeover: marks %+v, waiting %+v", d.st.Marks, d.st.AwaitingRead)
	}

	// A handoff that fails after the takeover forgets the entry where it went, and the message,
	// handed over again by the new session, is that session's: its own turn's text and its mark.
	d.awaitReadLocked("firstt", msg("m5"), "the first session's turn text", "")
	d.takeAwaitingLocked("second", "firstt")
	d.forgetAwaitingLocked("firstt", "m5")
	if len(d.st.AwaitingRead["second"]) != 0 {
		t.Errorf("a failed handoff's entry stayed with the session that took it: %+v", d.st.AwaitingRead["second"])
	}
	d.awaitReadLocked("firstt", msg("m6"), "the first session's turn text", "")
	d.takeAwaitingLocked("second", "firstt")
	turn6 := "Message from keen-pike via Murmur:\n\nsixth"
	d.awaitReadLocked("second", msg("m6"), turn6, "")
	if !d.readOnPromptLocked("second", turn6) || read("m6") != "second" {
		t.Errorf("handed over again by the new session: marks %+v, waiting %+v", d.st.Marks, d.st.AwaitingRead)
	}

	// Taken over twice, the mark is still the first session's.
	turn3 := "Message from keen-pike via Murmur:\n\nthird"
	d.awaitReadLocked("aaaaaa", msg("m3"), turn3, "")
	d.takeAwaitingLocked("bbbbbb", "aaaaaa")
	d.takeAwaitingLocked("cccccc", "bbbbbb")
	if !d.readOnPromptLocked("cccccc", turn3) || read("m3") != "aaaaaa" {
		t.Errorf("after two takeovers: marks %+v", d.st.Marks)
	}
}
