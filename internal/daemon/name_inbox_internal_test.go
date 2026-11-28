package daemon

import (
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A session that took X, followed a /clear to Y and stopped, and then came back under its own ID as
// X, as a resume on the command line does, keeps what was sent to X. What was sent to Y waits for
// Y's next session, which takes it (#847). Before, the session took its inbox whole: the X
// conversation was handed Y's message, and Y's next session found nothing.
//
// Not moved: what a session back under the name it stopped with had unread (a daemon's restart
// stops every session), and what is for a name another device now has (Opus's review of aeab15e1).
func TestASessionBackUnderItsOwnIDLeavesAnotherNamesMessages(t *testing.T) {
	t.Parallel()
	const back, next = "aaaaaa", "cccccc"
	const x, y = "calm-otter", "brisk-finch"
	keyX, keyY := nameKey("", "conv-x", "", back), nameKey("", "conv-y", "", next)
	ids := func(box []*inMsg) map[string]bool {
		out := map[string]bool{}
		for _, m := range box {
			out[m.Event.ID] = true
		}
		return out
	}
	// start makes a daemon whose session back stopped as Y, after a /clear from X, with a message
	// for each name and one to its address, and registers back again on key.
	start := func(t *testing.T, key string, before func(d *Daemon)) (*Daemon, *session) {
		d, _ := namedDaemon(t)
		msg := func(id, toName string) *inMsg {
			return &inMsg{Event: bus.Event{ID: id, Type: bus.Message, From: "device-peer/bbbbbb", To: bus.Addr(d.device, back),
				ToName: toName, At: time.Now()}, Arrived: time.Now(), Noticed: true}
		}
		d.mu.Lock()
		if d.st.SessionNames == nil {
			d.st.SessionNames = map[string]string{}
		}
		if d.st.NameHolders == nil {
			d.st.NameHolders = map[string]string{}
		}
		d.st.NameKeys[keyX], d.st.NameKeys[keyY] = x, y
		d.st.SessionNames[back], d.st.NameHolders[y] = y, back // Y was its last name
		d.st.Stopped[back] = time.Now()
		d.st.Inbox[back] = []*inMsg{msg("to-x", x), msg("to-y", y), msg("to-address", "")}
		if before != nil {
			before(d)
		}
		s := d.newSessionLocked(&proto.Hello{ID: back}, "claude-code", "", key)
		d.sessions[back] = s
		return d, s
	}

	t.Run("resumed as the name it left", func(t *testing.T) {
		t.Parallel()
		d, s := start(t, keyX, nil)
		defer d.mu.Unlock()
		if s.name != x {
			t.Fatalf("back as %q, want %q", s.name, x)
		}
		if got := ids(d.st.Inbox[back]); !got["to-x"] || !got["to-address"] || got["to-y"] {
			t.Errorf("back as %s, its inbox has %v; want what was sent to %s and to its address, not to %s", x, got, x, y)
		}
		n := d.newSessionLocked(&proto.Hello{ID: next}, "claude-code", "", keyY)
		d.sessions[next] = n
		if n.name != y {
			t.Fatalf("the next session is %q, want %q", n.name, y)
		}
		if got := ids(d.st.Inbox[next]); !got["to-y"] || len(got) != 1 {
			t.Errorf("%s's next session has %v in its inbox, want what was sent to %s", y, got, y)
		}
	})

	t.Run("back after a daemon restart, under the name it had", func(t *testing.T) {
		t.Parallel()
		d, s := start(t, nameKey("", "conv-y", "", back), nil)
		defer d.mu.Unlock()
		if s.name != y {
			t.Fatalf("back as %q, want %q", s.name, y)
		}
		if got := ids(d.st.Inbox[back]); len(got) != 3 {
			t.Errorf("back as the name it stopped with, its inbox has %v; want all three, as it had them", got)
		}
	})

	t.Run("the name it left is on another device now", func(t *testing.T) {
		t.Parallel()
		d, _ := start(t, keyX, func(d *Daemon) {
			if d.st.Used == nil {
				d.st.Used = map[string]string{}
			}
			d.st.Used[y] = "device-other/dddddd"
		})
		defer d.mu.Unlock()
		if got := ids(d.st.Inbox[back]); !got["to-y"] {
			t.Errorf("the message for %s, whose next session may be on another device, left the inbox: %v", y, got)
		}
	})
}
