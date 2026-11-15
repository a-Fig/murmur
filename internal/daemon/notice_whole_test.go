package daemon_test

import (
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// A message with no body is all in the hook's notice of it. An agent that has it doesn't call
// inbox, so it stayed unread and came a second time, as a turn, when the turn ended (#355).

// With the default busy setting, the hook gives a message that has no body whole: it is read, and
// is neither counted, nor in the inbox, nor a turn when the turn ends.
func TestAMessageWithNoBodyIsReadWhenTheHookShowsIt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	aliceName := a.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob") // a prompt: the session is at work
	a.send("aaaaaa", bob, "wait for me", " \n")
	var r proto.Response
	eventually(t, "the hook to give it", func() bool {
		r = b.hookNotices("claude-bob")
		return len(r.Messages) > 0 || len(r.Read) > 0
	})
	if got := viewSubjects(r.Read); len(got) != 1 || got[0] != "wait for me" || len(r.Messages) != 0 || r.Unread != 0 {
		t.Fatalf("the hook was given read %v, notices %v, %d unread; want it whole, and nothing unread",
			got, viewSubjects(r.Messages), r.Unread)
	}
	text := view.Hook(r.Read, r.Messages, r.Unread)
	if !strings.Contains(text, "Message from "+aliceName) || !strings.Contains(text, "\n\nwait for me\n") || strings.Contains(text, "unread") {
		t.Errorf("the hook's text:\n%s", text)
	}
	if inbox, notices := b.unread("bbbbbb", "claude-bob"); len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("after the hook gave it: inbox %v, notices %v", inbox, notices)
	}

	b.turnEnd("claude-bob")
	holds(t, "no turn of a message already read", 1500*time.Millisecond, func() bool { return len(calls()) == 0 })
}

// A hook from before Whole prints only the notice, so the message stays unread for it, as before.
func TestAnOlderHookKeepsAMessageWithNoBodyUnread(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "for an older hook", "")
	var r proto.Response
	eventually(t, "the notice", func() bool {
		r = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"})
		return len(r.Messages) > 0 || len(r.Read) > 0
	})
	if len(r.Read) != 0 || len(r.Messages) != 1 || r.Unread != 1 {
		t.Errorf("a hook that doesn't print whole messages was given %+v; want the notice", r)
	}
	if msgs := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages; len(msgs) != 1 {
		t.Errorf("the inbox has %v; want the message", viewSubjects(msgs))
	}
}

// A message with a body is told of by its subject, and given whole when the turn ends. That turn
// says a notice told of it, so the agent doesn't take it for a second message.
func TestATurnSaysItsMessageWasToldOfBefore(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	const told = ", which a notice told you of "

	// Handed over by T3 when the turn ends.
	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "told first", "and the rest of it")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	b.turnEnd("claude-bob")
	eventually(t, "the turn", func() bool { return len(calls()) == 1 })
	if in := calls()[0].Stdin; !strings.Contains(in, told) || !strings.Contains(in, "and the rest of it") {
		t.Errorf("the turn doesn't say a notice told of it:\n%s", in)
	}

	// Given by the Stop hook, with one no notice told of.
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "told again", "with a body")
	eventually(t, "the second notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	a.send("aaaaaa", bob, "untold", "with a body too")
	eventually(t, "the untold one to reach b", func() bool { return logHas(b, "untold") })
	r := b.stopHook("claude-bob")
	if got := viewSubjects(r.Read); len(got) != 2 || got[0] != "told again" || got[1] != "untold" {
		t.Fatalf("the Stop gave %v; want [told again untold]", got)
	}
	if first, second := view.Turn(r.Read[0]), view.Turn(r.Read[1]); !strings.Contains(first, told) || strings.Contains(second, told) {
		t.Errorf("want only the first to say a notice told of it:\n%s\n%s", first, second)
	}
}
