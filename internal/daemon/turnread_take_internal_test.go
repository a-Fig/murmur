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
		opt: Options{StoppedInbox: time.Hour},
		st:  state{Inbox: map[string][]*inMsg{}, Stopped: map[string]time.Time{}}}
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

	// Taken over twice, the mark is still the first session's.
	turn3 := "Message from keen-pike via Murmur:\n\nthird"
	d.awaitReadLocked("aaaaaa", msg("m3"), turn3, "")
	d.takeAwaitingLocked("bbbbbb", "aaaaaa")
	d.takeAwaitingLocked("cccccc", "bbbbbb")
	if !d.readOnPromptLocked("cccccc", turn3) || read("m3") != "aaaaaa" {
		t.Errorf("after two takeovers: marks %+v", d.st.Marks)
	}
}
