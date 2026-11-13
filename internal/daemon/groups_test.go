package daemon_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
)

// Group chats (#184): a group is a group: tag, the tag is its address, a session joins by setting
// it, and an agent can add other sessions to one.

// inGroup says d's who lists the session at addr under tag group.
func (d *device) inGroup(sess, addr, group string) bool {
	r := d.do(proto.Request{Op: proto.OpWho, Session: sess, Tags: []string{group}})
	for _, dev := range r.Devices {
		for _, s := range dev.Sessions {
			if s.Addr == addr {
				return true
			}
		}
	}
	return false
}

func TestAnAgentStartsAGroupAndItsMembersTalk(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	bobName := a.named(bob) // as alice's device knows it: a name lands in presence after the session does
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })

	// Alice starts the group with herself and bob in it, by name and by address.
	r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{alice, bobName}})
	if r.Error != "" {
		t.Fatalf("starting the group: %s", r.Error)
	}
	if !strings.Contains(r.Note, bobName) || strings.Contains(r.Note, "You aren't in") {
		t.Errorf("the note is %q; want it to name bob, and not to say alice is out of the group", r.Note)
	}
	eventually(t, "bob in the group, as alice's device sees it", func() bool { return a.inGroup("aaaaaa", bob, "group:ui") })
	if b.inGroup("bbbbbb", carol, "group:ui") {
		t.Fatal("carol is in a group nobody added her to")
	}

	// A message to the group reaches its members, and not its sender or anyone else.
	sent := a.send("aaaaaa", "group:ui", "to the group", "")
	if !strings.Contains(sent.Note, bobName) {
		t.Errorf("the sender was told %q; want bob named as a member", sent.Note)
	}
	if got := inboxOf(b, "bbbbbb"); !slices.Equal(got, []string{"to the group"}) {
		t.Errorf("bob's inbox is %q; want the group's message", got)
	}
	holds(t, "nothing for carol or for alice", time.Second, func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages) == 0 &&
			len(a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages) == 0
	})

	// Bob answers the group, and alice has it, addressed to the group.
	b.send("bbbbbb", "Group:UI", "bob's answer", "")
	var answer []proto.MessageView
	eventually(t, "bob's answer for alice", func() bool {
		answer = append(answer, a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages...)
		return len(answer) > 0
	})
	if answer[0].Subject != "bob's answer" || answer[0].To != "group:ui" {
		t.Errorf("alice was given %+v; want bob's answer, to group:ui", answer[0])
	}

	// Carol joins by setting the tag herself, and bob leaves by removing it.
	b.tagSelf("cccccc", []string{"group:ui"}, nil)
	b.tagSelf("bbbbbb", nil, []string{"group:ui"})
	eventually(t, "carol in and bob out, as alice's device sees it", func() bool {
		return a.inGroup("aaaaaa", carol, "group:ui") && !a.inGroup("aaaaaa", bob, "group:ui")
	})
	a.send("aaaaaa", "group:ui", "second", "")
	if got := inboxOf(b, "cccccc"); !slices.Equal(got, []string{"second"}) {
		t.Errorf("carol's inbox is %q; want the second message", got)
	}
	holds(t, "nothing more for bob, who left", time.Second, func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 0
	})
}

func TestAnAddFollowedAtOnceByAMessageReachesTheSessionAdded(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	dave, _ := a.session("dddddd", "") // on alice's own device, which reads its own log
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// Both are on alice's log, in order, so bob's device has made him a member by the time it
	// reads the message, whatever alice's device has seen of his presence.
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:rush"}, For: []string{bob, dave}}); r.Error != "" {
		t.Fatalf("adding bob: %s", r.Error)
	} else if !strings.Contains(r.Note, "You aren't in group:rush") {
		t.Errorf("the note is %q; want alice told she isn't in the group herself", r.Note)
	}
	a.send("aaaaaa", "group:rush", "straight after", "")
	if got := inboxOf(b, "bbbbbb"); !slices.Equal(got, []string{"straight after"}) {
		t.Errorf("bob's inbox is %q; want the message sent straight after he was added", got)
	}
	if got := inboxOf(a, "dddddd"); !slices.Equal(got, []string{"straight after"}) {
		t.Errorf("dave's inbox is %q; want the message too, on the adder's own device", got)
	}
}

func TestAnAddPastASessionsTagsIsRefusedToTheAdder(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	full := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "group:ui"}
	b.tagSelf("bbbbbb", full, nil)
	eventually(t, "bob's twelve tags, as alice's device sees them", func() bool { return a.inGroup("aaaaaa", bob, "t11") })
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:bus"}, For: []string{bob}}); r.Error == "" {
		t.Errorf("a thirteenth tag was allowed: %+v", r)
	}
	// A group he is in already takes no room.
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{bob}}); r.Error != "" {
		t.Errorf("adding bob to a group he is in: %s", r.Error)
	}
}

func TestOnlyAGroupCanBeGivenAndNoneTakenAway(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	for name, req := range map[string]proto.Request{
		"do-not-disturb for another":  {Add: []string{"do-not-disturb"}, For: []string{bob}},
		"a model for another":         {Add: []string{"model:opus-5.5"}, For: []string{bob}},
		"a group with a slash":        {Add: []string{"group:ui/web"}, For: []string{bob}},
		"a bare prefix":               {Add: []string{"group:"}, For: []string{bob}},
		"removing another from one":   {Remove: []string{"group:ui"}, For: []string{bob}},
		"a group and a removal":       {Add: []string{"group:ui"}, Remove: []string{"group:old"}, For: []string{bob}},
		"a session nobody has":        {Add: []string{"group:ui"}, For: []string{"zz-nobody/ffffff"}},
		"a person":                    {Add: []string{"group:ui"}, For: []string{strings.SplitN(alice, "/", 2)[0] + "/human"}},
		"one good and one bad target": {Add: []string{"group:ui"}, For: []string{bob, "not a session"}},
	} {
		req.Op, req.Session = proto.OpTags, "aaaaaa"
		if r := a.do(req); r.Error == "" {
			t.Errorf("%s was allowed: %+v", name, r)
		}
	}
	holds(t, "bob has no tags", time.Second, func() bool {
		return len(b.do(proto.Request{Op: proto.OpTags, Session: "bbbbbb"}).Tags) == 0
	})

	// Only a group is an address.
	for _, to := range []string{"reviewing", "model:opus-5.5", "group:", "group:ui/web"} {
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: to, Subject: "hello"}); r.Error == "" {
			t.Errorf("send to %q was allowed", to)
		}
	}
}

func TestAHumansChoiceStandsOverAnAgentsAdd(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// The person on bob's device takes him out of the group for good.
	b.tagSelf("bbbbbb", []string{"group:ui"}, nil)
	pg := b.page()
	if code, body := pg.call("POST", "/api/tags", pg.token, `{"addr":"`+bob+`","remove":["group:ui"]}`); code != 200 && code != 204 {
		t.Fatalf("removing the tag on the page: %d %s", code, body)
	}
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{bob}}); r.Error != "" {
		t.Fatalf("adding bob: %s", r.Error)
	}
	a.send("aaaaaa", "group:ui", "for the group", "")
	a.send("aaaaaa", bob, "for bob", "")
	if got := inboxOf(b, "bbbbbb"); !slices.Equal(got, []string{"for bob"}) {
		t.Errorf("bob's inbox is %q; want only what was sent to him, as a human took him out of the group", got)
	}
}

// The first message a session is given from a group says in full that it is in the group, how to
// answer and how to leave; later ones from that group end with one short line (Tyler, 2026-11-12).
// A second group explains itself afresh, and inbox goes by the same record as a turn.
func TestAGroupExplainsItselfOnce(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	carol, _ := b.session("cccccc", "")
	aliceName := b.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })
	b.tagSelf("bbbbbb", []string{"group:ui", "group:bus"}, nil)
	b.tagSelf("cccccc", []string{"group:ui"}, nil)

	long := func(g string) string {
		return "You are in " + g + ". Reply with Murmur's `send` to " + g + ", or to " + aliceName + " alone, if a reply is needed. Remove the tag " + g + " from yourself to leave the group."
	}
	short := func(g string) string {
		return "Reply with Murmur's `send` to " + g + " or " + aliceName + ", if a reply is needed."
	}
	turn := func(n int, group, subject string) string {
		t.Helper()
		a.send("aaaaaa", group, subject, "")
		eventually(t, "handoff of "+subject, func() bool { return len(calls()) >= n })
		return calls()[n-1].Stdin
	}
	check := func(got, group string, wantLong bool) {
		t.Helper()
		if !strings.HasPrefix(got, "Message sent in "+group+" from "+aliceName) {
			t.Errorf("the turn doesn't start with the group:\n%s", got)
		}
		if strings.Contains(got, long(group)) != wantLong || strings.Contains(got, short(group)) == wantLong {
			t.Errorf("want the long line %v, got:\n%s", wantLong, got)
		}
	}
	check(turn(1, "group:ui", "first to ui"), "group:ui", true)
	check(turn(2, "group:ui", "second to ui"), "group:ui", false)
	check(turn(3, "group:bus", "first to bus"), "group:bus", true)

	// Carol has no turns: she reads all of group:ui's with inbox, and only the first explains.
	var got []proto.MessageView
	eventually(t, "carol's two messages", func() bool {
		got = append(got, b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages...)
		return len(got) >= 2
	})
	if got[0].GroupKnown || !got[1].GroupKnown {
		t.Errorf("carol's messages say the group is known %v, then %v; want false, then true", got[0].GroupKnown, got[1].GroupKnown)
	}
}

// #539. A person starts a group on the page, which adds each member through /api/tags, a human's
// mark. Each member is told that removing the tag leaves the group, and it does, for a member on
// the page's device and one on another; a human's other marks still stand over the session.
func TestASessionLeavesAGroupAPersonStarted(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })

	pg := a.page()
	for _, addr := range []string{alice, bob} {
		if code, body := pg.call("POST", "/api/tags", pg.token, `{"addr":"`+addr+`","add":["group:a-b","reviewing"]}`); code != 200 && code != 204 {
			t.Fatalf("adding %s on the page: %d %s", addr, code, body)
		}
	}
	eventually(t, "both in the group", func() bool { return b.inGroup("cccccc", alice, "group:a-b") && b.inGroup("cccccc", bob, "group:a-b") })

	for _, m := range []struct {
		d    *device
		sess string
	}{{a, "aaaaaa"}, {b, "bbbbbb"}} {
		r := m.d.do(proto.Request{Op: proto.OpTags, Session: m.sess, Remove: []string{"group:a-b", "reviewing"}})
		if r.Error != "" {
			t.Fatalf("%s leaving: %s", m.sess, r.Error)
		}
		if strings.Contains(r.Note, "group:a-b") || !strings.Contains(r.Note, "reviewing") {
			t.Errorf("%s was told %q; want a human's choice to stand over reviewing only", m.sess, r.Note)
		}
		if slices.ContainsFunc(r.Tags, func(tg tags.Tag) bool { return tg.Name == "group:a-b" }) {
			t.Errorf("%s still has the group's tag: %+v", m.sess, r.Tags)
		}
	}
	eventually(t, "both out of the group", func() bool {
		return !b.inGroup("cccccc", alice, "group:a-b") && !b.inGroup("cccccc", bob, "group:a-b")
	})
	b.send("cccccc", "group:a-b", "after they left", "")
	holds(t, "nothing for alice or bob, who left", time.Second, func() bool {
		return len(a.do(proto.Request{Op: proto.OpInbox, Session: "aaaaaa"}).Messages) == 0 &&
			len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 0
	})
	if !b.inGroup("cccccc", bob, "reviewing") {
		t.Error("bob took off a tag a human gave him that isn't a group")
	}

	// The person can still put a member back.
	if code, body := pg.call("POST", "/api/tags", pg.token, `{"addr":"`+bob+`","add":["group:a-b"]}`); code != 200 && code != 204 {
		t.Fatalf("adding bob again: %d %s", code, body)
	}
	eventually(t, "bob back in the group", func() bool { return a.inGroup("aaaaaa", bob, "group:a-b") })
}
