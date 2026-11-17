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
// goes as usual, a turn or a notice, to the session or to a group it is in, and the page isn't told
// it waits. Everyone else's still waits, and goes in once do not disturb comes off.
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

			// got is every subject bob has been given, by any route: each turn handed to T3, and what
			// the hook gives during that turn. A turn is seen to run, and to end, so the next can go.
			got, seen := "", 0
			deliver := func(what string, want ...string) {
				t.Helper()
				eventually(t, what, func() bool {
					for ; seen < len(calls()); seen++ {
						c := calls()[seen]
						got += c.Stdin
						r := b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: c.Stdin})
						got += strings.Join(viewSubjects(append(r.Read, r.Messages...)), " ")
						b.turnEnd("claude-bob")
					}
					return !slices.ContainsFunc(want, func(w string) bool { return !strings.Contains(got, w) })
				})
			}

			// Idle: an agent's message and another device's person's wait, with no turn.
			a.send("aaaaaa", bob, "alice-msg", "")
			pa.sendText(bob, "grace-msg")
			eventually(t, "both to reach b", func() bool { return b.queuedFor(bob, "alice-msg") && b.queuedFor(bob, "grace-msg") })
			holds(t, "no turn for them", time.Second, func() bool { return len(calls()) == 0 })

			// Bob's own person: a turn, and the page isn't told it waits.
			if note := pb.sendNote(bob, "ada-first"); strings.Contains(note, "do not disturb") {
				t.Errorf("bob's own person was told %q", note)
			}
			deliver("a turn for ada's message", "ada-first")

			// At work: the hook tells of ada's next message, and of nothing else.
			b.hookNotices("claude-bob")
			pb.sendText(bob, "ada-at-work")
			eventually(t, "the hook to tell of ada's message", func() bool {
				r := b.hookNotices("claude-bob")
				return slices.Contains(viewSubjects(append(r.Read, r.Messages...)), "ada-at-work")
			})
			if r := b.hookNotices("claude-bob"); len(r.Messages) != 0 || len(r.Read) != 0 {
				t.Errorf("the hook then gave %+v; want nothing new", r)
			}
			b.turnEnd("claude-bob")

			// Ada's message to a group bob is in goes through as well.
			b.tagSelf("bbbbbb", []string{"group:pack"}, nil)
			pb.sendText("group:pack", "ada-group")
			deliver("ada's group message to go in", "ada-group")
			if strings.Contains(got, "alice-msg") || strings.Contains(got, "grace-msg") {
				t.Fatalf("a held message went in while bob was on do not disturb: %q", got)
			}

			// Do not disturb comes off: alice's and grace's go in, and ada's first message not again.
			if how == "device" {
				pb.mode("online")
			} else {
				b.tagSelf("bbbbbb", nil, []string{tags.DoNotDisturb})
			}
			deliver("alice's and grace's to go in once it came off", "alice-msg", "grace-msg")
			if n := strings.Count(got, "ada-first"); n != 1 {
				t.Errorf("ada's first message went in %d times; want once", n)
			}
		})
	}
}
