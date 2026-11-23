package daemon_test

import (
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// The hook's reply speaks for every session of its agent's process, and the messages it gives whole
// leave their inboxes, and those it tells of are marked told, before it goes. A reply longer than a
// client reads (#371) would lose them, so the reply carries only what fits (maxWholeReply), and the
// rest waits for the hook's next call. These tests are serial: they replace a package variable.

// oneMessagesRoom is room in a reply for one short message, whole or as a notice, and not two.
const oneMessagesRoom = 1500

// A Stop gives only what fits, and the rest at the next one.
//
// One device (#1010): the sender is a session beside bob, as nothing here turns on where a
// message came from. deliver_repeat_test.go says why that is enough.
func TestTheStopHookGivesOnlyWhatFitsAReply(t *testing.T) {
	t.Cleanup(daemon.SetMaxWholeReply(oneMessagesRoom))
	remote := newRemote(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	bob, _ := b.session("bbbbbb", "claude-bob")

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob") // a prompt: the session is at work
	b.send("aaaaaa", bob, "first", "its body")
	eventually(t, "the first to reach b", func() bool { return logHas(b, "first") })
	b.send("aaaaaa", bob, "second", "its body")
	eventually(t, "the second to reach b", func() bool { return logHas(b, "second") })

	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "first" {
		t.Fatalf("the Stop gave %v; want [first], all that fits", got)
	}
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "second" {
		t.Fatalf("the next Stop gave %v; want [second], kept for it", got)
	}
}

// Two sessions of one agent (two MCP servers of one Claude Code) end their turn with one Stop. One
// whose message the reply had no room for goes on with the turn too: its message isn't handed to T3
// while the agent works, and comes with the next Stop (Sol's review of #376).
//
// One device (#1010): the sender is a session beside bob, as nothing here turns on where a
// message came from. deliver_repeat_test.go says why that is enough.
func TestASessionWhoseMessageDidntFitGoesOnWithTheTurn(t *testing.T) {
	t.Cleanup(daemon.SetMaxWholeReply(oneMessagesRoom))
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bob2, _ := b.sessionRoute("bbbbb2", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob") // a prompt: both are at work
	b.send("aaaaaa", bob, "to one", "its body")
	b.send("aaaaaa", bob2, "to two", "its body")
	eventually(t, "both to reach b", func() bool { return logHas(b, "to one") && logHas(b, "to two") })

	first := viewSubjects(b.stopHook("claude-bob").Read)
	if len(first) != 1 {
		t.Fatalf("the Stop gave %v; want one, all that fits", first)
	}
	holds(t, "nothing handed to T3 while the turn goes on", time.Second, func() bool { return len(calls()) == 0 })
	second := viewSubjects(b.stopHook("claude-bob").Read)
	if got := append(first, second...); len(got) != 2 || !slices.Contains(got, "to one") || !slices.Contains(got, "to two") {
		t.Fatalf("the two Stops gave %v and %v; want each message once", first, second)
	}
}

// A message for everyone is in the inbox of each session the hook speaks for, and goes once: also
// when, in one of them, an older message the reply had no room for comes before it (Sol's review
// of #376).
//
// One device (#1010): the sender is a session beside bob, as nothing here turns on where a
// message came from. deliver_repeat_test.go says why that is enough.
func TestAMessageForEveryoneBehindOneThatDidntFitIsGivenOnce(t *testing.T) {
	t.Cleanup(daemon.SetMaxWholeReply(oneMessagesRoom))
	remote := newRemote(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	b.session("bbbbbb", "claude-bob")
	bob2, _ := b.session("bbbbb2", "claude-bob")

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	b.send("aaaaaa", bob2, "older", "its body")
	eventually(t, "the older to reach b", func() bool { return logHas(b, "older") })
	b.send("aaaaaa", "*", "everyone", "its body")
	eventually(t, "the one for everyone to reach b", func() bool { return logHas(b, "everyone") })

	var got []string
	for range 3 {
		got = append(got, viewSubjects(b.stopHook("claude-bob").Read)...)
	}
	slices.Sort(got)
	if len(got) != 2 || got[0] != "everyone" || got[1] != "older" {
		t.Fatalf("three Stops gave %v; want each message once", got)
	}
}

// A notice takes room too: one that doesn't fit isn't told of yet, and is at the hook's next call,
// counted as unread meanwhile. A message for everyone is told of once.
//
// One device (#1010): the sender is a session beside bob, as nothing here turns on where a
// message came from. deliver_repeat_test.go says why that is enough.
func TestTheHookIsToldOfOnlyWhatFitsAReply(t *testing.T) {
	t.Cleanup(daemon.SetMaxWholeReply(oneMessagesRoom))
	remote := newRemote(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	bob, _ := b.session("bbbbbb", "claude-bob")
	b.session("bbbbb2", "claude-bob")

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	b.send("aaaaaa", "*", "everyone", "its body")
	b.send("aaaaaa", bob, "to bob", "its body")
	eventually(t, "both to reach b", func() bool { return logHas(b, "everyone") && logHas(b, "to bob") })

	var told []string
	for range 3 {
		r := b.hookNotices("claude-bob")
		if len(r.Messages) > 1 {
			t.Fatalf("one reply told of %v; there is room for one", viewSubjects(r.Messages))
		}
		told = append(told, viewSubjects(r.Messages)...)
	}
	slices.Sort(told)
	if len(told) != 2 || told[0] != "everyone" || told[1] != "to bob" {
		t.Fatalf("the hook was told of %v; want each message once", told)
	}
}
