package daemon_test

import (
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Receipts (#26): a person's message gets marks back from the devices it reaches, and the page shows
// how far it got with each recipient.

// receipt is how far message id got with the recipient at addr, as the page's state says.
func (p *page) receipt(id, addr string) (web.Receipt, bool) {
	for _, r := range p.state().Receipts[id] {
		if r.Addr == addr {
			return r, true
		}
	}
	return web.Receipt{}, false
}

// stateIs says the page shows message id at state with the recipient at addr.
func (p *page) stateIs(id, addr, state string) func() bool {
	return func() bool {
		r, ok := p.receipt(id, addr)
		return ok && r.State == state
	}
}

// The marks cross from the recipient's device to the sender's, one state at a time, and travel
// encrypted like every other event.
func TestAPersonSeesHowFarTheirMessageGotOnAnotherDevice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "claude-bob")
	bobName := b.named(bob)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })

	id := pa.sendText(bobName, "Did the build pass?\nThe one on main.")
	r, ok := pa.receipt(id, bob)
	if !ok || r.Name != bobName || r.State == web.ReceiptNone {
		t.Fatalf("the receipt as sent: %+v, %v; want %s waiting for marks", r, ok, bobName)
	}
	eventually(t, "queued: it reached bob's device", pa.stateIs(id, bob, bus.MarkQueued))

	b.hookNotices("claude-bob") // the hook tells bob a message is waiting
	eventually(t, "delivered: bob knows it is waiting", pa.stateIs(id, bob, bus.MarkDelivered))

	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages; len(got) != 1 {
		t.Fatalf("bob's inbox: %+v", got)
	}
	eventually(t, "read: bob has it", pa.stateIs(id, bob, bus.MarkRead))
	if r, _ := pa.receipt(id, bob); r.At.IsZero() {
		t.Errorf("the read mark has no time: %+v", r)
	}

	// Nothing on bob's log says, readably, which message was marked.
	if log := git(t, remote, "log", "--format=%B", "refs/murmur/mesh/"+b.self()); strings.Contains(log, id) || strings.Contains(log, bus.MarkRead) {
		t.Errorf("bob's log carries the marks in the clear:\n%s", log)
	}
	// A message from an agent asks for none, and gets none.
	a.session("aaaaaa", "")
	sent := a.send("aaaaaa", bobName, "From an agent", "body")
	if got := pa.state().Receipts[sent.Messages[0].ID]; got != nil {
		t.Errorf("an agent's message has receipts: %+v", got)
	}
}

// A message to a group shows each member, on this device and another: marks for a member here never
// go over the bus, and one member reading doesn't mark the others.
func TestAPersonSeesEachGroupMembersReceipt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "claude-alice")
	bob, _ := b.session("bbbbbb", "claude-bob")
	carol, _ := b.session("cccccc", "claude-carol")
	a.tagSelf("aaaaaa", []string{"group:build"}, nil)
	b.tagSelf("bbbbbb", []string{"group:build"}, nil)
	b.tagSelf("cccccc", []string{"group:build"}, nil)
	pa := a.page()
	pa.choose("ada")
	eventually(t, "the group's members on ada's device", func() bool {
		return a.inGroup("aaaaaa", bob, "group:build") && a.inGroup("aaaaaa", carol, "group:build")
	})

	id := pa.sendText("group:build", "Who has the release?\nI need the notes.")
	if got := pa.state().Receipts[id]; len(got) != 3 {
		t.Fatalf("receipts for the group: %+v, want alice, bob and carol", got)
	}
	eventually(t, "alice's mark, made here", pa.stateIs(id, alice, bus.MarkQueued))
	eventually(t, "bob's and carol's, from their device", func() bool {
		return pa.stateIs(id, bob, bus.MarkQueued)() && pa.stateIs(id, carol, bus.MarkQueued)()
	})

	b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"})
	eventually(t, "bob and alice read it", func() bool {
		return pa.stateIs(id, bob, bus.MarkRead)() && pa.stateIs(id, alice, bus.MarkRead)()
	})
	if r, _ := pa.receipt(id, carol); r.State != bus.MarkQueued {
		t.Errorf("carol, who read nothing: %+v", r)
	}
}
