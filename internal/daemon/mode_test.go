package daemon_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/view"
)

// mode sets the device's mode on the page: online, dnd or invisible (#681).
func (p *page) mode(mode string) {
	p.t.Helper()
	body := `{"wake":{"when":"within"},"busy":"subject","mode":"` + mode + `"}`
	if code, b := p.call("PUT", "/api/settings", p.token, body); code != http.StatusOK {
		p.t.Fatalf("mode %s: %d %s", mode, code, b)
	}
}

// Invisible takes a device off Murmur in a repo as if it were offline (#681): teammates drop it,
// its agents can see and send nothing, the hook gives nothing, and the page shows none of its
// agents and sends nothing. A message that came before waits in the inbox, and going back online
// brings it and the device back as they were.
func TestInvisibleTakesTheDeviceOffAndBringsItBack(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-a")
	bob, _ := b.session("bbbbbb", "claude-b")
	eventually(t, "b to see alice", func() bool { _, ok := b.whoEntry(alice); return ok })
	eventually(t, "a to see bob", func() bool { _, ok := a.whoEntry(bob); return ok })
	b.send("bbbbbb", alice, "before the switch", "")
	eventually(t, "the message to reach a", func() bool { return a.queuedFor(alice, "before the switch") })

	p := a.page()
	p.mode("invisible")
	eventually(t, "b to drop a", func() bool { return !b.lists(a.self()) })

	for _, req := range []proto.Request{
		{Op: proto.OpWho, Session: "aaaaaa"},
		{Op: proto.OpLookup, Session: "aaaaaa", Name: bob},
		{Op: proto.OpSend, Session: "aaaaaa", To: bob, Subject: "hidden"},
		{Op: proto.OpInbox, Session: "aaaaaa"},
		{Op: proto.OpWho}, // `mur who` too
	} {
		if r := a.do(req); !strings.Contains(r.Error, "invisible") || len(r.Messages) > 0 || len(r.Devices) > 0 {
			t.Errorf("%s while invisible: %+v", req.Op, r)
		}
	}
	if r := a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-a", Whole: true}); r.Error != "" || len(r.Messages) > 0 || r.Unread > 0 {
		t.Errorf("the hook's notices while invisible: %+v", r)
	}
	if r := a.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-a", Continues: true}); len(r.Read) > 0 {
		t.Errorf("a turn's end while invisible gave %+v", r.Read)
	}
	st := p.state()
	if st.Mode != "invisible" || len(st.Devices) == 0 || len(st.Devices[0].Sessions) != 0 {
		t.Errorf("the page's state while invisible: %+v", st)
	}
	if code, body := p.call("POST", "/api/send", p.token, `{"to":"`+bob+`","text":"hi"}`); code == http.StatusOK || !strings.Contains(body, "invisible") {
		t.Errorf("the page sent while invisible: %d %s", code, body)
	}
	if b.lists(a.self()) {
		t.Error("b sees a again while it is invisible")
	}

	p.mode("online")
	eventually(t, "b to see alice again", func() bool { _, ok := b.whoEntry(alice); return ok })
	if inbox, _ := a.unread("aaaaaa", ""); !slices.Contains(inbox, "before the switch") {
		t.Errorf("the message from before the switch is gone: %q", inbox)
	}
	if r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}); r.Error != "" {
		t.Errorf("who after going back online: %s", r.Error)
	}
}

// Do not disturb keeps a device and its agents in sight but lets no message reach them (#681):
// every session carries the do-not-disturb tag, so what comes waits in the inbox with no notice,
// a sender's who says the device is on do not disturb, and going back online lets it through.
func TestDoNotDisturbHoldsEveryMessageToTheDevice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-a")
	b.session("bbbbbb", "claude-b")
	eventually(t, "b to see alice", func() bool { _, ok := b.whoEntry(alice); return ok })
	a.finishedATurn("claude-a") // the hook runs, so a message could be told of after a tool call

	p := a.page()
	p.mode("dnd")
	eventually(t, "b to see a on do not disturb", func() bool {
		s, ok := b.whoEntry(alice)
		return ok && tags.Has(s.Tags, tags.DoNotDisturb)
	})
	if who := view.Who(b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"})); !strings.Contains(who, "ON DO NOT DISTURB") {
		t.Errorf("b's who doesn't say a is on do not disturb:\n%s", who)
	}
	if st := p.state(); st.Mode != "dnd" || len(st.Devices[0].Sessions) == 0 || !st.Devices[0].DND {
		t.Errorf("the page's state on do not disturb: %+v", st)
	}
	// Its agents still use Murmur.
	if r := a.do(proto.Request{Op: proto.OpWho, Session: "aaaaaa"}); r.Error != "" {
		t.Errorf("who on do not disturb: %s", r.Error)
	}

	// A person taking the tag off the session doesn't beat the device's mode.
	if code, body := p.call("POST", "/api/tags", p.token, `{"addr":"`+alice+`","remove":["`+tags.DoNotDisturb+`"]}`); code != 200 && code != 204 {
		t.Fatalf("taking the tag off alice: %d %s", code, body)
	}

	b.send("bbbbbb", alice, "held", "")
	eventually(t, "the message to reach a", func() bool { return a.queuedFor(alice, "held") })
	if r := a.hookNotices("claude-a"); len(r.Messages) > 0 || r.Unread > 0 {
		t.Errorf("the hook told of a message on do not disturb: %+v", r)
	}

	// Through invisible on the way back, what waited is still let go once online.
	p.mode("invisible")
	eventually(t, "b to drop a", func() bool { return !b.lists(a.self()) })
	p.mode("online")
	eventually(t, "b to see a online", func() bool {
		s, ok := b.whoEntry(alice)
		return ok && !tags.Has(s.Tags, tags.DoNotDisturb)
	})
	if _, notices := a.unread("aaaaaa", "claude-a"); !slices.Contains(notices, "held") {
		t.Errorf("the held message isn't told of once back online: %q", notices)
	}
}

// A turn that ends while the device is invisible ends: back online, the session is idle, and a
// message goes in as a turn, not held for a turn's end that already came (Sol's review of v0.5.0).
func TestATurnEndedWhileInvisibleLeavesTheSessionIdle(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.finishedATurn("claude-bob")
	b.hookNotices("claude-bob") // a prompt: the session is at work
	p := b.page()
	p.mode("invisible")
	if r := b.do(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob", Continues: true}); r.Error != "" || len(r.Read) > 0 {
		t.Fatalf("a turn's end while invisible: %+v", r)
	}
	p.mode("online")
	eventually(t, "a to see bob again", func() bool { return a.seesSession(bob) })

	a.send("aaaaaa", bob, "after", "its body")
	eventually(t, "the message as a turn", func() bool { return len(calls()) == 1 })
	if !strings.Contains(calls()[0].Stdin, "after") {
		t.Errorf("the turn: %q", calls()[0].Stdin)
	}
}
