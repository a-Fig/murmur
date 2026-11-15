package daemon_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// The page's busy setting (#204): a session at work is given each new message by the hook either
// as a line with its subject, the default, or whole, as its turn would read. A message given whole
// is read: it leaves the inbox and the count, and doesn't go in as a turn when the turn ends.
func TestBusySessionGetsTheWholeMessageThroughTheHook(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	aliceName, bobName := a.named(alice), b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	pg := b.page()
	put := func(settings string) {
		t.Helper()
		if code, body := pg.call("PUT", "/api/settings", pg.token, settings); code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", settings, code, body)
		}
	}
	hook := func(subagent bool) proto.Response {
		return b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: subagent, Whole: true})
	}
	// waitFor calls the hook, which keeps bob at work, until it tells of something.
	waitFor := func(what string) proto.Response {
		t.Helper()
		var r proto.Response
		eventually(t, what, func() bool {
			r = hook(false)
			return len(r.Messages) > 0 || len(r.Read) > 0
		})
		return r
	}

	// Bob has finished a turn, so the daemon knows his Stop hook runs, and is at work again. With
	// the default, a message is a line with its subject, and the count.
	b.finishedATurn("claude-bob")
	hook(false)
	put(`{"wake":{"when":"always"}}`)
	a.send("aaaaaa", bob, "first", "the first body")
	r := waitFor("the notice of first")
	if len(r.Messages) != 1 || r.Messages[0].Subject != "first" || r.Messages[0].Body != "" || len(r.Read) != 0 || r.Unread != 1 {
		t.Fatalf("with the subject only, the hook was given %+v", r)
	}

	// Set for bob alone, the whole message. A subagent's tool call is told nothing (#188), and
	// takes nothing out of the inbox.
	put(`{"wake":{"when":"always"},"busy":"subject","sessionBusy":{"` + bobName + `":"whole"}}`)
	body := "Are you changing it?\nI say \"yes\" — and 100% über-sure."
	a.send("aaaaaa", bob, "auth middleware?", body)
	eventually(t, "the message to reach b", func() bool { return logHas(b, "auth middleware?") })
	if r := hook(true); len(r.Messages) != 0 || len(r.Read) != 0 {
		t.Errorf("a subagent's hook was given %+v", r)
	}
	r = waitFor("the whole message")
	if len(r.Read) != 1 || len(r.Messages) != 0 {
		t.Fatalf("with the whole message, the hook was given %+v", r)
	}
	if h, _ := pg.health(); h.Delivery.Hook.Whole != 1 || h.Delivery.Hook.Notices != 1 {
		t.Errorf("the health snapshot counts the hook's %+v, want one whole and one notice", h.Delivery.Hook)
	}
	if r.Unread != 1 {
		t.Errorf("the count is %d, want 1: first is unread, and the message given whole isn't", r.Unread)
	}
	text := view.Hook(r.Read, r.Messages, r.Unread)
	for _, want := range []string{
		"Message from " + aliceName + " (test",
		"via " + brand.Product + ":\n\nauth middleware?\n\n" + body + "\n",
		"Reply with " + brand.Product + "'s `send` to " + aliceName + " if a reply is needed.",
		"1 message unread.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the hook's text lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, view.Turn(r.Read[0])) {
		t.Errorf("the hook's text doesn't start with the message as its turn reads:\n%s", text)
	}
	eventually(t, "the log to say it was given whole", func() bool {
		return strings.Contains(out.String(), "to session "+bob+" whole through the hook")
	})
	if !logHas(b, `gave "auth middleware?" from `) || !logHas(b, " through the hook") {
		t.Errorf("the page's notes don't say the message was given whole through the hook: %+v",
			b.do(proto.Request{Op: proto.OpLog, Limit: 50}).Events)
	}
	if strings.Contains(out.String(), "über-sure") {
		t.Errorf("the log has a message's text:\n%s", out.String())
	}
	if r := hook(false); len(r.Messages) != 0 || len(r.Read) != 0 || r.Unread != 0 {
		t.Errorf("the hook told it again: %+v", r)
	}

	// The turn ends, and bob is idle: the message given whole doesn't go in as a turn, nor is it
	// in the inbox.
	b.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob"})
	holds(t, "no turn of the message given whole", 1500*time.Millisecond, func() bool {
		for _, c := range calls() {
			if strings.Contains(c.Stdin, "auth middleware?") {
				return false
			}
		}
		return true
	})
	for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
		if m.Subject == "auth middleware?" {
			t.Errorf("the message given whole is still in the inbox")
		}
	}

	// The device says whole and bob's own setting says subject: bob's own wins.
	put(`{"wake":{"when":"always"},"busy":"whole","sessionBusy":{"` + bobName + `":"subject"}}`)
	hook(false)
	a.send("aaaaaa", bob, "third", "the third body")
	r = waitFor("the notice of third")
	if len(r.Messages) != 1 || r.Messages[0].Subject != "third" || len(r.Read) != 0 {
		t.Errorf("with bob's own setting subject, the hook was given %+v", r)
	}
}

// A message whose handoff to T3 failed may be in T3's queue all the same: with whole messages set,
// it still gets only its subject, as after any failed handoff, not a second telling in full.
func TestAFailedHandoffIsNotGivenWhole(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Fail, "1")
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"always"},"sessionBusy":{"`+bobName+`":"whole"}}`); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, body)
	}

	a.send("aaaaaa", bob, "handed over", "the body")
	eventually(t, "the failed handoff", func() bool { return len(calls()) == 1 })
	var r proto.Response
	eventually(t, "the notice", func() bool {
		r = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true})
		return len(r.Messages) > 0 || len(r.Read) > 0
	})
	if len(r.Read) != 0 || len(r.Messages) != 1 || r.Messages[0].Body != "" {
		t.Errorf("after a failed handoff the hook was given %+v; want the subject only", r)
	}
}

// A hook from before Whole prints only the subjects in Messages (#229). With whole messages set, it
// still gets the subject, and the message stays in the inbox, not given whole to a hook that would
// print nothing of it.
func TestAnOlderHookGetsTheSubjectOnly(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "claude-bob")
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"always"},"sessionBusy":{"`+bobName+`":"whole"}}`); code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, body)
	}

	a.send("aaaaaa", bob, "for an older hook", "the body")
	var r proto.Response
	eventually(t, "the notice", func() bool {
		r = b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"})
		return len(r.Messages) > 0 || len(r.Read) > 0
	})
	if len(r.Read) != 0 || len(r.Messages) != 1 || r.Messages[0].Subject != "for an older hook" || r.Messages[0].Body != "" {
		t.Errorf("a hook that doesn't print whole messages was given %+v; want the subject only", r)
	}
	var kept bool
	for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
		kept = kept || m.Subject == "for an older hook"
	}
	if !kept {
		t.Errorf("the message told to an older hook left the inbox")
	}
}
