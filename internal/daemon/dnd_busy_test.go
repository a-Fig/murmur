package daemon_test

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// Do not disturb (#198) on a session the hook runs in (#202, #204): while the tag is on, a message
// stays queued, with no notice and no whole message from the hook while the session is at work,
// nothing held for its turn's end, nothing from a Stop that keeps the turn going (#213), and no
// turn when it is idle. When the tag comes off, it goes out
// once, by whichever route applies then.

// tagSelf adds and removes session id's own tags.
func (d *device) tagSelf(id string, add, remove []string) {
	d.t.Helper()
	if r := d.do(proto.Request{Op: proto.OpTags, Session: id, Add: add, Remove: remove}); r.Error != "" {
		d.t.Fatalf("tags: %s", r.Error)
	}
}

// queued says the daemon has session id's message with this subject: lookup lists what it read,
// whether or not the session has.
func (d *device) queuedFor(addr, subject string) bool {
	r := d.do(proto.Request{Op: proto.OpLookup, Name: addr, Limit: 20})
	return r.Lookup != nil && slices.ContainsFunc(r.Lookup.Messages, func(m proto.MessageView) bool { return m.Subject == subject })
}

func TestDoNotDisturbOnABusySessionHoldsEveryRouteUntilItComesOff(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	bobName := b.named(bob)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// Bob would be given messages whole while at work (#204), and may always be woken.
	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token,
		`{"wake":{"when":"always"},"sessionBusy":{"`+bobName+`":"whole"}}`); code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", code, body)
	}
	b.finishedATurn("claude-bob")
	b.tagSelf("bbbbbb", []string{"do-not-disturb"}, nil)

	// The sender sees the tag in bob's presence.
	eventually(t, "alice to see bob on do not disturb", func() bool {
		r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa", Tags: []string{"do-not-disturb"}})
		return len(r.Devices) > 0 && slices.ContainsFunc(r.Devices, func(d proto.Device) bool { return len(d.Sessions) > 0 && !d.Self })
	})

	// At work: no notice, no whole message, no count, nothing handed to T3.
	b.hookNotices("claude-bob")
	if r := a.send("aaaaaa", bob, "first", "the first body"); r.Note == "" {
		t.Error("the sender wasn't told bob is on do not disturb")
	}
	eventually(t, "first queued for bob", func() bool { return b.queuedFor(bob, "first") })
	holds(t, "nothing from the hook while on do not disturb", time.Second, func() bool {
		r := b.hookNotices("claude-bob")
		return len(r.Messages) == 0 && len(r.Read) == 0 && r.Unread == 0
	})
	// The turn ends: nothing was held for it, so nothing is handed over.
	b.turnEnd("claude-bob")
	holds(t, "no turn at the turn's end", time.Second, func() bool { return len(calls()) == 0 })

	// At work again, the tag comes off: the hook gives the message whole, once, and the turn's
	// end hands nothing over, as it is no longer queued.
	b.hookNotices("claude-bob")
	b.tagSelf("bbbbbb", nil, []string{"do-not-disturb"})
	r := b.hookNotices("claude-bob")
	if len(r.Read) != 1 || r.Read[0].Subject != "first" || r.Read[0].Body != "the first body" {
		t.Fatalf("after the tag came off, the hook was given %+v; want first, whole", r)
	}
	if r := b.hookNotices("claude-bob"); len(r.Messages) != 0 || len(r.Read) != 0 {
		t.Errorf("the next tool call was given %+v; want nothing", r)
	}
	b.turnEnd("claude-bob")
	holds(t, "no turn for a message the hook gave whole", time.Second, func() bool { return len(calls()) == 0 })

	// A Stop that can keep the turn going (#213) gives nothing while the tag is on. Taken off while
	// at work, with no tool call after, the message is given at the turn's next end, once.
	b.tagSelf("bbbbbb", []string{"do-not-disturb"}, nil)
	b.hookNotices("claude-bob")
	a.send("aaaaaa", bob, "third", "the third body")
	eventually(t, "third queued for bob", func() bool { return b.queuedFor(bob, "third") })
	if r := b.stopHook("claude-bob"); len(r.Read) != 0 {
		t.Fatalf("the Stop gave %v while on do not disturb", viewSubjects(r.Read))
	}
	holds(t, "no turn after that Stop", time.Second, func() bool { return len(calls()) == 0 })
	b.hookNotices("claude-bob")
	b.tagSelf("bbbbbb", nil, []string{"do-not-disturb"})
	if got := viewSubjects(b.stopHook("claude-bob").Read); len(got) != 1 || got[0] != "third" {
		t.Fatalf("the Stop after the tag came off gave %v; want [third]", got)
	}
	if r := b.stopHook("claude-bob"); len(r.Read) != 0 {
		t.Errorf("the next Stop gave %v; want nothing", viewSubjects(r.Read))
	}
	holds(t, "no turn for a message the Stop gave", time.Second, func() bool { return len(calls()) == 0 })

	// Idle and on do not disturb: no turn. The tag comes off: one turn.
	b.tagSelf("bbbbbb", []string{"do-not-disturb"}, nil)
	a.send("aaaaaa", bob, "second", "its body")
	eventually(t, "second queued for bob", func() bool { return b.queuedFor(bob, "second") })
	holds(t, "no turn while idle on do not disturb", time.Second, func() bool { return len(calls()) == 0 })
	b.tagSelf("bbbbbb", nil, []string{"do-not-disturb"})
	eventually(t, "one turn once the tag came off", func() bool { return len(calls()) == 1 })
	holds(t, "only one turn", time.Second, func() bool { return len(calls()) == 1 })
	if inbox, notices := b.unread("bbbbbb", "claude-bob"); len(inbox) != 0 || len(notices) != 0 {
		t.Errorf("still unread after it was handed over: inbox %v, notices %v", inbox, notices)
	}
}
