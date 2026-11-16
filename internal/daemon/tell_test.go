package daemon_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// A sender can ask to be told how far its message got (#276): once, in its inbox, from Murmur.

// awaitTold reads session sess's inbox until it holds a notice from Murmur, and returns it. Anything
// else in the inbox fails the test.
func (d *device) awaitTold(sess string) proto.MessageView {
	d.t.Helper()
	var told proto.MessageView
	eventually(d.t, "the notice from "+brand.Product, func() bool {
		for _, m := range d.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages {
			if !m.Receipt {
				d.t.Fatalf("%s's inbox holds a message that isn't a notice: %+v", sess, m)
			}
			told = m
		}
		return told.ID != ""
	})
	return told
}

// A message asked about is told of once its recipient on another device has read it, and the
// notice says so by name, from Murmur rather than from anyone who could be answered.
func TestASenderIsToldOnceItsMessageIsRead(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "claude-ada")
	bob, _ := b.session("bbbbbb", "claude-bob")
	bobName := b.named(bob)
	eventually(t, "a knows bob's name", func() bool { return a.seesSession(bob) && a.nameOf(bob) == bobName })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "Take #12 over?",
		Body: "Branch fix-12.", TellMe: bus.MarkRead})
	if r.Error != "" || !r.Telling || !strings.Contains(r.Note, "You'll be told, in your inbox, once it is read") {
		t.Fatalf("the send: %+v; want it sent, Telling, and a note saying so", r)
	}
	eventually(t, "bob reads it", func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 1
	})

	told := a.awaitTold("aaaaaa")
	t.Logf("send's result:\n%s\nthe inbox:\n%s\nthe hook's notice:\n%s\nas a turn:\n%s", view.Sent(r),
		view.Inbox(proto.Response{Messages: []proto.MessageView{told}}), view.Notices([]proto.MessageView{told}, 1), view.Turn(told))
	if want := bobName + ` has read your message "Take #12 over?"`; told.Subject != want {
		t.Errorf("the notice's subject is %q, want %q", told.Subject, want)
	}
	if !strings.HasPrefix(told.Body, "- "+bobName+": read, ") {
		t.Errorf("the notice's body is %q, want bob's line saying read", told.Body)
	}
	if told.FromName != brand.Product || told.FromOwner != "" {
		t.Errorf("the notice is from %q (%q), want %s", told.FromName, told.FromOwner, brand.Product)
	}
	// Nothing on the bus is the notice: it is this device's own.
	if log := git(t, remote, "log", "--format=%s", "refs/murmur/mesh/"+a.self()); strings.Contains(log, told.ID) {
		t.Errorf("the notice went on the bus:\n%s", log)
	}
	// Once only, and an agent's message shows no ticks on the page.
	if got := a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages; len(got) != 0 {
		t.Errorf("a second notice: %+v", got)
	}
	if got := a.page().state().Receipts[r.Messages[0].ID]; got != nil {
		t.Errorf("the page shows ticks for an agent's message: %+v", got)
	}
}

// A message handed to an idle T3 thread as a turn is read once the turn's prompt comes (#611), so a
// sender asking for read is told then, not at the handoff, while T3 may still hold the turn in its
// queue (#654). The sender is an idle T3 thread too, and the notice waits in its inbox rather than
// starting a turn: it asked to be told, not woken.
func TestASenderIsToldItsTurnWasReadOnceItStarts(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.sessionRoute("aaaaaa", "claude-ada", proto.Route{T3CLI: exe, T3Thread: "thread-ada"})
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "a knows bob's name", func() bool { return a.seesSession(bob) && a.nameOf(bob) == bobName })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "Rerun CI?", TellMe: bus.MarkRead}); !r.Telling {
		t.Fatalf("the send: %+v", r)
	}
	eventually(t, "the handoff", func() bool { return len(calls()) > 0 })
	// Longer than a receipt takes to reach a, so a notice at the handoff would show.
	holds(t, "nothing told while the turn waits", 6*time.Second, func() bool {
		return len(a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages) == 0
	})

	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: calls()[0].Stdin})
	told := a.awaitTold("aaaaaa")
	if want := bobName + ` has read your message "Rerun CI?"`; told.Subject != want {
		t.Errorf("the notice's subject is %q, want %q", told.Subject, want)
	}
	if !strings.HasPrefix(told.Body, "- "+bobName+": read, ") {
		t.Errorf("the notice's body is %q", told.Body)
	}
	for _, c := range calls() {
		if slices.Contains(c.Args, "thread-ada") {
			t.Errorf("the notice was handed to the sender's thread as a turn: %+v", c)
		}
	}
}

// A message handed over as a turn has been delivered: the client holds the turn whole. A sender
// asking for delivered is told at the handoff, and the notice says how it got there.
func TestASenderIsToldItsMessageWasHandedOverAsATurn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "claude-ada")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "a knows bob's name", func() bool { return a.seesSession(bob) && a.nameOf(bob) == bobName })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "Rerun CI?", TellMe: bus.MarkDelivered}); !r.Telling {
		t.Fatalf("the send: %+v", r)
	}
	eventually(t, "the handoff", func() bool { return len(calls()) > 0 })
	told := a.awaitTold("aaaaaa")
	if want := bobName + ` has been handed your message "Rerun CI?" as a turn`; told.Subject != want {
		t.Errorf("the notice's subject is %q, want %q", told.Subject, want)
	}
	if !strings.HasPrefix(told.Body, "- "+bobName+": handed over as a turn") {
		t.Errorf("the notice's body is %q", told.Body)
	}
}

// A message not read within TellWait is told of then, with where it is, and only then.
func TestASenderIsToldItsMessageIsNotReadYet(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.tellWait = 2 * time.Second })
	b := newDevice(t, remote)
	a.session("aaaaaa", "claude-ada")
	bob, _ := b.session("bbbbbb", "claude-bob")
	bobName := b.named(bob)
	eventually(t, "a knows bob's name", func() bool { return a.seesSession(bob) && a.nameOf(bob) == bobName })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "Review #9?", TellMe: bus.MarkRead})
	if r.Error != "" || !r.Telling || !strings.Contains(r.Note, "or in 2s with where it is then") {
		t.Fatalf("the send: %+v", r)
	}
	told := a.awaitTold("aaaaaa")
	t.Logf("the inbox:\n%s", view.Inbox(proto.Response{Messages: []proto.MessageView{told}}))
	if want := bobName + ` hasn't read your message "Review #9?" yet`; told.Subject != want {
		t.Errorf("the notice's subject is %q, want %q", told.Subject, want)
	}
	// Queued, or not heard from if bob's mark hasn't come back yet on a loaded machine.
	if !strings.Contains(told.Body, "- "+bobName+": queued in its inbox, not yet seen") &&
		!strings.Contains(told.Body, "- "+bobName+": not heard from") ||
		!strings.Contains(told.Body, "You won't be told again") {
		t.Errorf("the notice's body is %q", told.Body)
	}

	// Bob reading it now tells nobody: the sender has been told.
	b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	time.Sleep(time.Second)
	if got := a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages; len(got) != 0 {
		t.Errorf("a second notice: %+v", got)
	}
}

// tell_me is delivered or read, never for everyone, and a message with nobody to wait for says so at
// once.
func TestTellMeRefusalsAndNobodyToWaitFor(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "claude-ada")
	eventually(t, "b's presence", func() bool {
		for _, dv := range a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}).Devices {
			if dv.ID == b.self() {
				return true
			}
		}
		return false
	})

	for _, c := range []struct{ to, tell, want string }{
		{"*", bus.MarkRead, "tell_me can't be asked of a message to everyone"},
		{b.self(), "seen", `tell_me is "delivered" or "read", not "seen"`},
	} {
		r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: c.to, Subject: "hi", TellMe: c.tell})
		if !strings.Contains(r.Error, c.want) {
			t.Errorf("to %s, tell_me %q: error %q, want %q", c.to, c.tell, r.Error, c.want)
		}
	}
	// b has no sessions, so a message to its device is for nobody, and nothing is waited for.
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: b.self(), Subject: "anyone?", TellMe: bus.MarkRead})
	t.Logf("send's result to an empty device:\n%s", view.Sent(r))
	if r.Error != "" || r.Telling || !strings.Contains(r.Note, "You won't be told how far it gets") {
		t.Errorf("the send to an empty device: %+v", r)
	}
}

// A sender replaced before it is told, as a T3 thread's session is when T3 starts a new one for it,
// is told in the session that holds its name now (#659): the old one's inbox is never read again.
func TestAReplacedSenderIsToldInItsNewSession(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, _ := useFakeT3(t)
	// Long enough for the new session to take the name first on a loaded runner.
	a := newDeviceWith(t, remote, func(d *device) { d.tellWait = 10 * time.Second })
	b := newDevice(t, remote)
	route := proto.Route{T3CLI: exe, T3Thread: "thread-ada"}
	ada, conn := a.sessionRoute("aaaaaa", "claude-1", route)
	adaName := a.named(ada)
	bob, _ := b.session("bbbbbb", "claude-bob")
	bobName := b.named(bob)
	eventually(t, "a knows bob's name", func() bool { return a.seesSession(bob) && a.nameOf(bob) == bobName })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: bobName, Subject: "Review #9?", TellMe: bus.MarkRead})
	if r.Error != "" || !r.Telling {
		t.Fatalf("the send: %+v", r)
	}
	conn.Close()
	time.Sleep(500 * time.Millisecond) // the daemon has seen it go
	a.sessionRoute("cccccc", "claude-2", route)
	eventually(t, "the new session holds ada's name", func() bool { return a.nameOf(a.self()+"/cccccc") == adaName })

	told := a.awaitTold("cccccc")
	if !strings.Contains(told.Subject, "Review #9?") {
		t.Errorf("the notice: %+v", told)
	}
}
