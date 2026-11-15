package daemon_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// At the end of a busy turn, the Stop hook keeps the turn going with what the session hasn't read,
// rather than the daemon handing it over as a new turn (#202, Tyler, 2026-11-08).

// stopHook is Claude Code's Stop hook at the end of a turn of Claude session claude: it can keep the
// turn going, and the reply's Read is what it gives the agent.
func (d *device) stopHook(claude string) proto.Response {
	d.t.Helper()
	r := d.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: claude, Continues: true})
	if r.Error != "" {
		d.t.Fatalf("turnend: %s", r.Error)
	}
	return r
}

// What the session was told of and what came untold during the turn are given whole at its end,
// and none goes to T3. The turn goes on, so a message that comes then waits for its next end, not
// T3's queue; once a Stop gives nothing, the session is idle and a message wakes it as before.
func TestTheStopHookKeepsTheTurnGoing(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	if r := b.stopHook("claude-bob"); len(r.Read) != 0 {
		t.Fatalf("a Stop with nothing for it gave %v", viewSubjects(r.Read))
	}
	b.hookNotices("claude-bob") // a prompt: the session is at work
	a.send("aaaaaa", bob, "told", "the body of the told one")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	a.send("aaaaaa", bob, "untold", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "untold") })

	r := b.stopHook("claude-bob")
	if got := viewSubjects(r.Read); len(got) != 2 || got[0] != "told" || got[1] != "untold" {
		t.Fatalf("the Stop gave %v; want [told untold]", got)
	}
	if r.Read[0].Body != "the body of the told one" {
		t.Errorf("the body given: %q", r.Read[0].Body)
	}
	if inbox, notices := b.unread("bbbbbb", "claude-bob"); len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("after the Stop gave them: inbox %v, notices %v", inbox, notices)
	}

	a.send("aaaaaa", bob, "in the continued turn", "its body")
	eventually(t, "the next message to reach b", func() bool { return logHas(b, "in the continued turn") })
	holds(t, "nothing handed to T3 while the turn goes on", time.Second, func() bool { return len(calls()) == 0 })
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "in the continued turn" {
		t.Fatalf("the next Stop gave %v", got)
	}

	if r := b.stopHook("claude-bob"); len(r.Read) != 0 {
		t.Fatalf("a Stop after the last message gave %v", viewSubjects(r.Read))
	}
	a.send("aaaaaa", bob, "while idle", "its body")
	eventually(t, "the idle session woken through T3", func() bool { return len(calls()) == 1 })
}

// Outside T3 with no messaging socket, the Stop hook is the one way in: what came during the turn
// is given at its end, where before it waited in the inbox.
func TestTheStopHookIsAWayInWithoutASocket(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "only way in", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "only way in" {
		t.Fatalf("the Stop gave %v", got)
	}
}

// A message the agent read with inbox during the turn isn't given again at its end, and the page's
// never-wake setting keeps the rest in the inbox, as it keeps them from being turns.
func TestTheStopHookGivesWhatATurnWould(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "read mid-turn", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 0 {
		t.Fatalf("the Stop gave %v, read already", got)
	}

	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"never"}}`); code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", code, body)
	}
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "never wake", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 0 {
		t.Fatalf("the Stop gave %v under never", got)
	}
	if inbox, _ := b.unread("bbbbbb", "claude-bob"); len(inbox) != 1 {
		t.Errorf("inbox under never: %v; want the message kept", inbox)
	}
}

// A message the session was told of before its first Stop, when there was no known way in, is
// given at that Stop: the Stop is the way in.
func TestTheFirstStopGivesWhatWasToldBeforeIt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "told in the first turn", "its body")
	eventually(t, "the notice", func() bool { return len(b.hookNotices("claude-bob").Messages) > 0 })
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "told in the first turn" {
		t.Fatalf("the first Stop gave %v", got)
	}
}

// Claude Code ends a turn after eight continuations in a row, whatever the hook says: the ninth
// Stop with no tool call between gives nothing, and its message is handed over as a turn instead.
// A tool call starts the count again.
func TestContinuationsStopAtClaudeCodesCap(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	for i := 0; i < 9; i++ {
		subject := fmt.Sprintf("message %d", i+1)
		a.send("aaaaaa", bob, subject, "its body")
		eventually(t, subject+" to reach b", func() bool { return logHas(b, subject) })
		got := viewSubjects(b.stopHook("claude-bob").Read)
		switch {
		case i < 8 && (len(got) != 1 || got[0] != subject):
			t.Fatalf("Stop %d gave %v", i+1, got)
		case i == 8 && len(got) != 0:
			t.Fatalf("the ninth Stop in a row gave %v", got)
		}
	}
	eventually(t, "the ninth handed over as a turn", func() bool { return len(calls()) == 1 })

	b.hookNotices("claude-bob") // a new turn
	a.send("aaaaaa", bob, "after a tool call", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "after a tool call") })
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 {
		t.Fatalf("a Stop after the count started again gave %v", got)
	}
}

// Outside T3, a continuation no other hook shows went on lapses after ContinueHold: the Stop's
// answer may have been lost, and the agent stopped. A message that came meanwhile then goes in by
// the session's socket, with nothing else to wake the daemon for it.
func TestALapsedContinuationHandsOverWhatCameMeanwhile(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	a := newDevice(t, remote)
	const hold = 8 * time.Second
	b := newDeviceWith(t, remote, func(d *device) { d.continueHold = hold })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{MsgSocket: sock.path, MsgToken: "tok-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.hookNotices("claude-bob")
	b.stopHook("claude-bob")
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "continues the turn", "its body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "continues the turn") })
	// The continuation starts during this Stop, so it holds until at least hold after stopped.
	// Whether the socket is still empty once the next message has reached b says nothing when the
	// tests' load delays that past the hold, as it did on CI (#387); when the handoff came does. The
	// check is as tight as the Stop's round trip: a hold shorter by less than that would pass.
	stopped := time.Now()
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 {
		t.Fatalf("the Stop gave %v", got)
	}
	if took := time.Since(stopped); took > time.Second {
		t.Logf("the Stop took %s, so the check below can't tell a hold that much shorter", took)
	}
	a.send("aaaaaa", bob, "after the lost answer", "its body")
	eventually(t, "the next message to reach b", func() bool { return logHas(b, "after the lost answer") })
	if late := time.Since(stopped); late >= hold {
		t.Logf("the message reached b %s after the Stop, once the continuation had lapsed", late)
	}
	eventually(t, "the handoff once the continuation lapsed", func() bool { return len(sock.lines()) >= 2 })
	if at := sock.firstAt().Sub(stopped); at < hold {
		t.Fatalf("the socket got the handoff %s after the Stop, while the %s continuation held", at, hold)
	}
	if turn := socketTurn(t, sock.lines(), "tok-bob"); !strings.Contains(turn, "after the lost answer") {
		t.Errorf("the turn: %q", turn)
	}
}

// The Stop hook describes a sender only in a conversation's first turn from it (#216). A /clear
// keeps the session's ID but starts a conversation that has seen nothing, so it is described again
// there; a /resume goes back to a conversation that has met it.
func TestTheStopHookDescribesASenderOncePerConversation(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// given is one message, sent while conversation claude is at work and given at its turn's end.
	given := func(claude, subject string) proto.MessageView {
		t.Helper()
		b.hook(claude, procP) // a prompt: at work, and in this conversation
		a.send("aaaaaa", bob, subject, "its body")
		b.noticed(claude, procP, subject)
		r := b.stopHook(claude)
		if len(r.Read) != 1 || r.Read[0].Subject != subject {
			t.Fatalf("the Stop in %s gave %v; want [%s]", claude, viewSubjects(r.Read), subject)
		}
		return r.Read[0]
	}
	b.hookNotices("claude-a")
	b.stopHook("claude-a")
	for _, step := range []struct {
		claude, subject string
		introduced      bool
	}{
		{"claude-a", "first in A", false},
		{"claude-a", "second in A", true},
		{"claude-b", "first after /clear", false},
		{"claude-a", "after /resume", true},
	} {
		if got := given(step.claude, step.subject); got.Introduced != step.introduced {
			t.Errorf("%q: Introduced is %v, want %v", step.subject, got.Introduced, step.introduced)
		}
	}
}
