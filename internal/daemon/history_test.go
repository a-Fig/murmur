package daemon_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of the page's messages from before its daemon started (#138): they are read back from the
// logs the clone holds, so a conversation is whole whenever the daemon last ran.

// pageHas says pg's messages are those with these subjects, in this order.
func pageHas(pg *page, want ...string) func() bool {
	return func() bool {
		msgs, _ := pg.messages("")
		return reflect.DeepEqual(subjects(msgs), want)
	}
}

// A daemon that restarts shows the conversation it had read before: on the page, in lookup, and
// the sessions' starts in the Recent view. What comes next follows it, and nothing is shown twice.
func TestPageKeepsAConversationAcrossARestart(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	aliceName := b.named(alice)

	pb := b.page()
	a.send("aaaaaa", bob, "first", "how far along is the parser?")
	eventually(t, "the first message on b's page", pageHas(pb, "first"))
	b.send("bbbbbb", alice, "second", "done but for the errors")
	eventually(t, "the answer on b's page", pageHas(pb, "first", "second"))
	eventually(t, "alice's start on b's page", func() bool {
		acts, _ := pb.activity("")
		return hasAct(acts, web.ActStarted, alice, nil)
	})

	b.restart()
	pb = b.page()
	eventually(t, "the conversation on the page of the daemon that started", pageHas(pb, "first", "second"))
	msgs, _ := pb.messages("")
	if m := msgs[0]; m.Body != "how far along is the parser?" || m.FromAddr != alice || m.ToAddr != bob || m.From != aliceName {
		t.Errorf("the first message came back as %+v", m)
	}
	eventually(t, "alice's start in the Recent view again", func() bool {
		acts, _ := pb.activity("")
		return hasAct(acts, web.ActStarted, alice, func(x web.Activity) bool { return x.Who == aliceName })
	})
	var looked []string
	for _, m := range b.do(proto.Request{Op: proto.OpLookup, Name: aliceName, Limit: 10}).Lookup.Messages {
		looked = append(looked, m.Subject)
	}
	if !reflect.DeepEqual(looked, []string{"first", "second"}) {
		t.Errorf("lookup shows %q of alice's messages, want both", looked)
	}

	a.send("aaaaaa", "*", "third", "")
	eventually(t, "what was sent after the restart, after the rest", pageHas(pb, "first", "second", "third"))
	// A second restart reads the same commits back again, and the page still has each once.
	b.restart()
	eventually(t, "all three after another restart", pageHas(b.page(), "first", "second", "third"))
}

// A device that joins later is granted the team's keys, and its page then shows the conversation
// from before it joined, which every device may read. Its sessions are given none of it.
func TestPageOfADeviceThatJoinsLaterShowsEarlierConversations(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	a.send("aaaaaa", bob, "before c joined", "")
	eventually(t, "the first message on b's page", pageHas(b.page(), "before c joined"))
	b.send("bbbbbb", alice, "an answer, before c joined", "")
	eventually(t, "the answer on a's page", pageHas(a.page(), "before c joined", "an answer, before c joined"))

	// Not waiting for its key: the page is open, and has asked, before the grant comes, where that
	// is how the timing falls. Nothing holds the grant back until then.
	c := newDeviceWith(t, remote, func(d *device) { d.noKey = true })
	c.session("cccccc", "")
	// As the page asks: each call carries the cursor the one before gave, and a message it has is
	// known by its ID. What is read back after its first call must still reach it.
	pc := c.page()
	cursor, got := "", map[string]string{}
	eventually(t, "the earlier conversation to reach a page that holds a cursor", func() bool {
		msgs, next := pc.messages(cursor)
		cursor = next
		for _, m := range msgs {
			got[m.ID] = m.Subject
		}
		return len(got) >= 2
	})
	if len(got) != 2 {
		t.Errorf("the page was given %v", got)
	}
	eventually(t, "the earlier conversation on c's page, in order", pageHas(pc, "before c joined", "an answer, before c joined"))
	if got := c.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Errorf("a session on the device that joined was given %+v", got)
	}
}

// A daemon away for longer than it catches up on passes over what was sent meanwhile: nobody is
// given it. The page shows it all the same, after what the daemon had read before it went.
func TestPageShowsWhatWasSentWhileItsDaemonWasAway(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	eventually(t, "presence", func() bool { return a.seesDevices(2) && b.seesDevices(2) })
	a.send("aaaaaa", "*", "before b went", "")
	eventually(t, "the first message on b's page", pageHas(b.page(), "before b went"))

	b.stop()
	ageLastSync(t, b, time.Hour)
	a.send("aaaaaa", "*", "while b was away", "")
	b.start()
	b.session("cccccc", "")

	eventually(t, "both messages on b's page", pageHas(b.page(), "before b went", "while b was away"))
	// The pass that read them back ran after the sync that passed over them, so what that sync
	// queued is all there will be.
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Errorf("a session started after the outage was given %+v", got)
	}
}
