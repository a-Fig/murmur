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
func TestASessionBackUnderItsOwnIDLeavesAnotherNamesMessages(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	const back, next = "aaaaaa", "cccccc"
	const x, y = "calm-otter", "brisk-finch"
	keyX, keyY := nameKey("", "conv-x", "", back), nameKey("", "conv-y", "", next)
	msg := func(id, toName string) *inMsg {
		return &inMsg{Event: bus.Event{ID: id, Type: bus.Message, From: "device-peer/bbbbbb", To: bus.Addr(d.device, back),
			ToName: toName, At: time.Now()}, Arrived: time.Now()}
	}
	ids := func(box []*inMsg) map[string]bool {
		out := map[string]bool{}
		for _, m := range box {
			out[m.Event.ID] = true
		}
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
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

	s := d.newSessionLocked(&proto.Hello{ID: back}, "claude-code", "", keyX)
	d.sessions[back] = s
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
}
