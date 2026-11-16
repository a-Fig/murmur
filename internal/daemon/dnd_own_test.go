package daemon_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// Do not disturb doesn't hold a message from a device's own person to a session on that device
// (#777, Tyler, 2026-11-16): through the device's mode and through the session's own tag alike, it
// goes as usual, a turn or a notice, and the page isn't told it waits. Everyone else's still waits.
func TestDoNotDisturbLetsTheDevicesOwnPersonThrough(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"device", "tag"} {
		t.Run(how, func(t *testing.T) {
			t.Parallel()
			remote := newRemote(t)
			exe, calls := useFakeT3(t)
			a, b := newDevice(t, remote), newDevice(t, remote)
			alice, _ := a.session("aaaaaa", "")
			bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
			eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
			b.putSettings(`{"wake":{"when":"always"}}`)
			pb, pa := b.page(), a.page()
			pb.choose("ada-lovelace")
			pa.choose("grace-hopper")
			b.finishedATurn("claude-bob")
			if how == "device" {
				pb.mode("dnd")
			} else {
				b.tagSelf("bbbbbb", []string{tags.DoNotDisturb}, nil)
			}
			eventually(t, "alice to see bob on do not disturb", func() bool {
				s, ok := a.whoEntry(bob)
				return ok && tags.Has(s.Tags, tags.DoNotDisturb)
			})

			// Idle: an agent's message and another device's person's wait, with no turn.
			a.send("aaaaaa", bob, "from alice", "")
			pa.sendText(bob, "from grace")
			eventually(t, "both to reach b", func() bool { return b.queuedFor(bob, "from alice") && b.queuedFor(bob, "from grace") })
			holds(t, "no turn for them", time.Second, func() bool { return len(calls()) == 0 })

			// Bob's own person: a turn, of that message alone, and the page isn't told it waits.
			if note := pb.sendNote(bob, "from ada"); strings.Contains(note, "do not disturb") {
				t.Errorf("bob's own person was told %q", note)
			}
			eventually(t, "a turn for ada's message", func() bool { return len(calls()) == 1 })
			if in := calls()[0].Stdin; !strings.Contains(in, "from ada") || strings.Contains(in, "from alice") || strings.Contains(in, "from grace") {
				t.Errorf("the turn carried %q; want ada's message alone", in)
			}
			holds(t, "no second turn", time.Second, func() bool { return len(calls()) == 1 })

			// At work: the hook tells of ada's next message, and of nothing else.
			b.handedTurn("claude-bob", calls()[0])
			b.hookNotices("claude-bob")
			pb.sendText(bob, "while at work")
			eventually(t, "the hook to tell of ada's message", func() bool {
				r := b.hookNotices("claude-bob")
				return slices.ContainsFunc(r.Messages, func(m proto.MessageView) bool { return m.Subject == "while at work" }) ||
					slices.ContainsFunc(r.Read, func(m proto.MessageView) bool { return m.Subject == "while at work" })
			})
			if r := b.hookNotices("claude-bob"); len(r.Messages) != 0 || len(r.Read) != 0 {
				t.Errorf("the hook then gave %+v; want nothing new", r)
			}
			b.turnEnd("claude-bob")
			if inbox, _ := b.unread("bbbbbb", ""); !slices.Contains(inbox, "from alice") || !slices.Contains(inbox, "from grace") {
				t.Errorf("bob's inbox is %q; want alice's and grace's messages still there", inbox)
			}
		})
	}
}
