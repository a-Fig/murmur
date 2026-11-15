package daemon_test

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// Tests of #345: a person's message says in full that they see only what the agent sends the first
// time a session has one from them, and ends as an agent's message does after that.

func longLine(who string) string {
	return who + " sees only what you `send`, not this conversation. Answer with " + brand.Product + "'s `send` to " + who + "."
}

func shortLine(who string) string {
	return "Reply with " + brand.Product + "'s `send` to " + who + " if a reply is needed."
}

// The first turn from ada explains, the second doesn't, grace's first does, and what bob has been
// told outlasts its daemon restarting.
func TestAPersonIsExplainedOnlyInTheirFirstTurn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b, c := newDevice(t, remote), newDevice(t, remote), newDevice(t, remote)
	route := proto.Route{T3CLI: exe, T3Thread: "thread-bob"}
	bob, _ := b.sessionRoute("bbbbbb", "", route)
	bobName := b.named(bob)
	pa, pc := a.page(), c.page()
	pa.choose("ada")
	pc.choose("grace")
	eventually(t, "presence", func() bool {
		_, ada := b.whoEntry(a.self() + "/" + bus.PersonSession)
		_, grace := b.whoEntry(c.self() + "/" + bus.PersonSession)
		return ada && grace && a.seesSession(bob) && c.seesSession(bob)
	})

	turn := func(n int, p *page, text string) string {
		t.Helper()
		p.sendText(bobName, text)
		eventually(t, "handoff "+text, func() bool { return len(calls()) >= n })
		return calls()[n-1].Stdin
	}
	check := func(got, who string, long bool) {
		t.Helper()
		if strings.Contains(got, longLine(who)) != long || strings.Contains(got, shortLine(who)) == long {
			t.Errorf("want the long line %v, got:\n%s", long, got)
		}
	}
	check(turn(1, pa, "first from ada"), "ada", true)
	check(turn(2, pa, "second from ada"), "ada", false)
	check(turn(3, pc, "first from grace"), "grace", true)

	b.restart()
	b.sessionRoute("bbbbbb", "", route)
	if got := b.named(bob); got != bobName {
		t.Fatalf("bob came back as %s, not %s", got, bobName)
	}
	eventually(t, "presence again", func() bool { return a.seesSession(bob) })
	check(turn(4, pa, "third from ada"), "ada", false)
}

// The hook's whole message and inbox decide by the same record: a message given whole by the hook
// counts for inbox after it, and of two messages from a person read at once, only the first
// explains.
func TestTheHookAndTheInboxExplainAPersonOnce(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "claude-bob")
	carol, _ := b.session("cccccc", "claude-carol")
	bobName, carolName := b.named(bob), b.named(carol)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) })

	// A message with no body is given whole by a hook that prints it (#355).
	pa.sendText(bobName, "one")
	var r proto.Response
	eventually(t, "the hook to give it whole", func() bool {
		r = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true})
		return len(r.Read) > 0
	})
	if len(r.Read) != 1 || r.Read[0].Explained {
		t.Fatalf("the hook's first message from ada: %+v", r.Read)
	}
	if text := view.Hook(r.Read, nil, 0); !strings.Contains(text, longLine("ada")) {
		t.Errorf("the hook's first message from ada doesn't explain:\n%s", text)
	}
	pa.sendText(bobName, "two")
	if got := readInbox(t, b, "bbbbbb", 1); !got[0].Explained {
		t.Errorf("inbox after the hook explains again: %+v", got)
	}

	pa.sendText(carolName, "three")
	pa.sendText(carolName, "four")
	got := readInbox(t, b, "cccccc", 2)
	if got[0].Explained || !got[1].Explained {
		t.Fatalf("Explained of two read at once: %+v", got)
	}
	text := view.Inbox(proto.Response{Messages: got})
	if strings.Count(text, "sees only what you send them") != 1 {
		t.Errorf("the inbox explains other than once:\n%s", text)
	}
}

// readInbox reads session sess's inbox until it has had n messages, and fails the test if it has
// more.
func readInbox(t *testing.T, d *device, sess string, n int) []proto.MessageView {
	t.Helper()
	var got []proto.MessageView
	eventually(t, "the inbox", func() bool {
		got = append(got, d.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages...)
		return len(got) >= n
	})
	if len(got) != n {
		t.Fatalf("read %d messages, want %d: %+v", len(got), n, got)
	}
	return got
}

// A person's message in a group ends with the group's line, not theirs (#484), so it doesn't count
// as explaining them: their first direct message to the same session still carries the long line,
// by a turn and by inbox.
func TestAPersonsGroupMessageLeavesThemToBeExplained(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	carol, _ := b.session("cccccc", "")
	bobName, carolName := b.named(bob), b.named(carol)
	b.tagSelf("bbbbbb", []string{"group:ui"}, nil)
	b.tagSelf("cccccc", []string{"group:ui"}, nil)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool {
		_, ada := b.whoEntry(a.self() + "/" + bus.PersonSession)
		return ada && a.seesSession(bob) && a.seesSession(carol)
	})

	pa.sendText("group:ui", "to the group")
	eventually(t, "the group's handoff", func() bool { return len(calls()) >= 1 })
	if got := calls()[0].Stdin; strings.Contains(got, longLine("ada")) || !strings.Contains(got, "You are in group:ui.") {
		t.Errorf("the group's turn:\n%s", got)
	}
	pa.sendText(bobName, "just to bob")
	eventually(t, "the direct handoff", func() bool { return len(calls()) >= 2 })
	if got := calls()[1].Stdin; !strings.Contains(got, longLine("ada")) {
		t.Errorf("the first direct turn after the group's doesn't explain ada:\n%s", got)
	}

	// Carol has no turns: she reads both with inbox, and the direct one is still the first to explain.
	var got []proto.MessageView
	eventually(t, "carol's group message", func() bool {
		got = append(got, b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages...)
		return len(got) >= 1
	})
	pa.sendText(carolName, "just to carol")
	eventually(t, "carol's direct message", func() bool {
		got = append(got, b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages...)
		return len(got) >= 2
	})
	if got[1].Explained {
		t.Errorf("carol's first direct message from ada counts her as explained already: %+v", got[1])
	}
}
