package daemon_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/view"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// An add for a session on the adder's own device goes by what that device keeps of it, which is
// what the add is applied by: a tag a person turned off on the page still counts toward the 12, and
// a group a person took the session out of can't be given back by an agent (#524, #543).
func TestAnAddForASessionHereGoesByWhatItsDeviceKeeps(t *testing.T) {
	a := newDevice(t, newRemote(t))
	a.session("aaaaaa", "")
	dave, _ := a.session("dddddd", "")
	carol, _ := a.session("cccccc", "")
	pg := a.page()
	off := func(addr, tag string) {
		t.Helper()
		if code, body := pg.call("POST", "/api/tags", pg.token, `{"addr":"`+addr+`","remove":["`+tag+`"]}`); code != 200 && code != 204 {
			t.Fatalf("removing %s on the page: %d %s", tag, code, body)
		}
		eventually(t, tag+" off", func() bool { return !a.inGroup("aaaaaa", addr, tag) })
	}

	// Dave set twelve tags, and a person turned one off: he shows eleven, and has no room.
	a.tagSelf("dddddd", []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12"}, nil)
	off(dave, "t1")
	r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{dave}})
	t.Logf("adding dave: error %q, note %q", r.Error, r.Note)
	if !strings.Contains(r.Error, "no room") {
		t.Errorf("adding dave past a tag a person turned off gave %+v; want it refused, as he has no room", r)
	}

	// A person took carol out of group:ui.
	a.tagSelf("cccccc", []string{"group:ui"}, nil)
	off(carol, "group:ui")
	r = a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{carol}})
	t.Logf("adding carol: error %q, note %q", r.Error, r.Note)
	if !strings.Contains(r.Error, "a person took") {
		t.Errorf("adding carol to a group a person took her out of gave %+v; want it refused, saying so", r)
	}
}

// An add for a session on another device counts the tags its presence shows as the session's own,
// as its device counts them; a tag a person gave it isn't one (#524). The adder is told that device
// can still refuse it, for what presence doesn't show.
func TestAnAddForASessionElsewhereCountsTheTagsItSetItself(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.tagSelf("bbbbbb", []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11"}, nil)
	pg := b.page()
	if code, body := pg.call("POST", "/api/tags", pg.token, `{"addr":"`+bob+`","add":["reviewing"]}`); code != 200 && code != 204 {
		t.Fatalf("adding a tag on the page: %d %s", code, body)
	}
	eventually(t, "bob's twelve tags, as alice's device sees them", func() bool {
		return a.inGroup("aaaaaa", bob, "t11") && a.inGroup("aaaaaa", bob, "reviewing")
	})
	r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:bus"}, For: []string{bob}})
	t.Logf("adding bob: error %q, note %q", r.Error, r.Note)
	if r.Error != "" {
		t.Fatalf("adding bob, who set eleven tags himself: %s", r.Error)
	}
	if !strings.Contains(r.Note, "Another device can still refuse the add") {
		t.Errorf("the adder was told %q; want it told bob's device can still refuse the add", r.Note)
	}
	eventually(t, "bob in the group", func() bool { return a.inGroup("aaaaaa", bob, "group:bus") })
}

// The first message a session is given from a group says in full that it is in the group, how to
// answer and how to leave; later ones from that group end with one short line (Tyler, 2026-11-12).
// A second group explains itself afresh, and inbox goes by the same record as a turn.
func TestAGroupExplainsItselfOnce(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// lookup lists a session's group messages, the ones it got as well as the ones it sent (#445), on
// its own device and on another, and not for a session outside the group. Membership is by the
// session's tags now: one that left lists only what it said there, and a stopped one still lists
// the group's messages by the tags kept for its name.
func TestLookupListsTheGroupMessagesASessionGot(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDeviceWith(t, remote, func(d *device) { d.grace = 300 * time.Millisecond }) // alice stops at the end
	b := newDevice(t, remote)
	alice, aliceConn := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	carol, _ := b.session("cccccc", "")
	nameA, nameB, nameC := b.named(alice), a.named(bob), a.named(carol)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && a.seesSession(carol) && b.seesSession(alice) })
	if r := a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{alice, bob}}); r.Error != "" {
		t.Fatalf("starting the group: %s", r.Error)
	}
	eventually(t, "bob in the group, as both devices see it", func() bool {
		return a.inGroup("aaaaaa", bob, "group:ui") && b.inGroup("bbbbbb", bob, "group:ui")
	})
	a.send("aaaaaa", "group:ui", "alice to the group", "")
	// b lists messages in the order it reads them, so bob sends only once b has read alice's.
	eventually(t, "bob got alice's message", func() bool {
		inbox, _ := b.unread("bbbbbb", "")
		return slices.Contains(inbox, "alice to the group")
	})
	b.send("bbbbbb", "group:ui", "bob to the group", "")

	subjects := func(d *device, as, who string) []string {
		var out []string
		for _, m := range d.look(as, who).Messages {
			out = append(out, m.Subject)
		}
		return out
	}
	both := []string{"alice to the group", "bob to the group"}
	for _, c := range []struct {
		d       *device
		as, who string
	}{{a, "aaaaaa", nameB}, {b, "bbbbbb", nameB}, {b, "cccccc", nameA}, {a, "aaaaaa", nameA}} {
		var got []string
		eventually(t, "both group messages in the lookup of "+c.who, func() bool {
			got = subjects(c.d, c.as, c.who)
			return slices.Equal(got, both)
		})
	}
	if got := subjects(a, "aaaaaa", nameC); len(got) != 0 {
		t.Errorf("carol, who isn't in the group, has %q in lookup", got)
	}
	out := view.Lookup(proto.Response{Lookup: a.look("aaaaaa", nameB)})
	if !strings.Contains(out, nameA+" -> group:ui  \"alice to the group\"") {
		t.Errorf("lookup's text doesn't show alice's message to the group:\n%s", out)
	}
	t.Logf("lookup of bob, from alice's device:\n%s", out)

	// Bob leaves: the group's messages are no longer his conversation, on his device or alice's.
	b.tagSelf("bbbbbb", nil, []string{"group:ui"})
	for _, c := range []struct {
		d  *device
		as string
	}{{b, "bbbbbb"}, {a, "aaaaaa"}} {
		var got []string
		eventually(t, "only bob's own message in his lookup once he left", func() bool {
			got = subjects(c.d, c.as, nameB)
			return slices.Equal(got, []string{"bob to the group"})
		})
	}

	// Alice stops: her own device still lists the group's messages, by the tags kept for her name.
	aliceConn.Close()
	eventually(t, "alice stopped", func() bool { return !a.look("", nameA).Running })
	if got := subjects(a, "", nameA); !slices.Equal(got, both) {
		t.Errorf("lookup of alice, stopped, lists %q; want %q", got, both)
	}
}

// A member on a device whose Murmur is from before groups (#445): its presence, encrypted as every
// presence has been since the bus was, says nothing of groups, and its daemon passes over what is
// sent to one. The sender of a group message is told that member won't get it, and an add of a
// session there is refused, both by the presence alone.
func TestAMemberOnADeviceFromBeforeGroups(t *testing.T) {
	remote := newRemote(t)
	a := newDevice(t, remote)
	a.session("aaaaaa", "")
	a.tagSelf("aaaaaa", []string{"group:ui"}, nil)
	eventually(t, "a's presence on the remote", func() bool {
		return gitOK(remote, "rev-parse", "--verify", "-q", "refs/murmur/presence/"+a.self()) != "" &&
			presenceOn(t, remote, a.self()).BoxKey != ""
	})
	toA := map[string]string{a.self(): presenceOn(t, remote, a.self()).BoxKey}

	const old = "oldhost-0002"
	member, other := old+"/oooooo", old+"/pppppp"
	sign, box := bus.NewSignKey(), bus.NewBoxKey()
	pushOld := func() {
		t.Helper()
		now := time.Now().UTC()
		pres := bus.Presence{V: bus.Version, Device: old, Host: "oldhost", At: now, BoxKey: bus.BoxPublic(box),
			Sessions: []bus.SessionInfo{
				{ID: "oooooo", Agent: "claude", Started: now, Tags: []tags.Tag{{Name: "group:ui", Src: tags.SrcSession}}},
				{ID: "pppppp", Agent: "claude", Started: now},
			}} // and no Groups, as that daemon writes it
		enc, err := bus.EncryptPresence(pres, toA)
		if err != nil {
			t.Fatal(err)
		}
		ref := "refs/murmur/presence/" + old
		msg, err := bus.EncodeSealed(enc, bus.PresenceSubject, sign, old, ref)
		if err != nil {
			t.Fatal(err)
		}
		pushMessage(t, remote, ref, msg)
	}
	// Pushed again before each check: one presence is fresh for three seconds here, and the older
	// device's sessions count only while it is.
	eventually(t, "a sees the older device's sessions", func() bool { pushOld(); return a.seesSession(member) && a.seesSession(other) })

	var note string
	eventually(t, "the sender told the older device's member won't get it", func() bool {
		pushOld()
		note = a.send("aaaaaa", "group:ui", "to the group", "").Note
		return strings.Contains(note, member+" won't get it") && strings.Contains(note, "from before groups")
	})
	t.Logf("the sender's note: %s", note)

	var r proto.Response
	eventually(t, "an add of the older device's session refused for being from before groups", func() bool {
		pushOld()
		r = a.do(proto.Request{Op: proto.OpTags, Session: "aaaaaa", Add: []string{"group:ui"}, For: []string{other}})
		return strings.Contains(r.Error, "from before groups")
	})
	if r.OK || !strings.HasPrefix(r.Error, "nobody added: "+other) {
		t.Errorf("the add of %s gave %+v; want it refused, naming the session", other, r)
	}
	t.Logf("the refused add: %s", r.Error)
}
