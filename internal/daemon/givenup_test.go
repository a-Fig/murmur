package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// A peer is sent a message by a session that has just given its name to another session, and waits
// for a new one. The peer should not show the message as from the name the other session holds
// (#875). sessions starts the two sessions on b, of processes 700 and 800, and moves the
// conversation to the second while no name is to be had.
func peerShowsTheGiverAs(t *testing.T, sessions func(a, b *device, g *pushGate) (giver, taker string)) {
	remote := newRemote(t)
	a := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	g := newPushGate(true)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	alice, _ := a.session("aaaaaa", "")
	giver, taker := sessions(a, b, g)
	name := b.named(taker)
	eventually(t, "a to see "+name+" on the new holder", func() bool { return a.nameOf(taker) == name })
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "xxxxxx", To: alice, Subject: "from the one that gave its name up"}); r.Error != "" {
		t.Fatal(r.Error)
	}
	if m := inboxMessage(t, a, "aaaaaa"); m.FromName == name {
		t.Errorf("a shows %s's message as from %s, which %s holds", giver, name, taker)
	}
	g.set(true)
}

// Claude Code (#843, #875): T3 goes on with a conversation the command line has open, while no
// pool name is to be had.
func TestAPeerDoesntShowAClaudeSessionByTheNameT3Took(t *testing.T) {
	t.Parallel()
	peerShowsTheGiverAs(t, func(a, b *device, g *pushGate) (string, string) {
		cli := hello(t, b, proto.Hello{ID: "xxxxxx", Agent: "claude-code", ClaudeSession: "conv-5", ClientPID: 700})
		name := b.named(cli)
		eventually(t, "a to see "+name+" on the command line first", func() bool { return a.nameOf(cli) == name })
		g.set(false)
		t3 := hello(t, b, proto.Hello{ID: "tttttt", Agent: "claude-code in T3", ClaudeSession: "conv-5", ClientPID: 800,
			Route: proto.Route{T3Thread: "thread-5", T3CLI: "/opt/t3/bin/t3"}, ByThread: true})
		return cli, t3
	})
}

// Codex (#799, #875): the app goes on with a thread the command line has open, while no pool name
// is to be had.
func TestAPeerDoesntShowACodexSessionByTheNameTheAppTook(t *testing.T) {
	t.Parallel()
	peerShowsTheGiverAs(t, func(a, b *device, g *pushGate) (string, string) {
		cli := hello(t, b, proto.Hello{ID: "xxxxxx", Agent: "codex", ClientPID: 700})
		b.do(proto.Request{Op: proto.OpRoute, Session: "xxxxxx", Route: &proto.Route{Native: "thread-c"}})
		name := b.named(cli)
		eventually(t, "a to see "+name+" on the command line first", func() bool { return a.nameOf(cli) == name })
		app := hello(t, b, proto.Hello{ID: "tttttt", Agent: "codex", ClientPID: 800})
		b.named(app)
		g.set(false)
		b.do(proto.Request{Op: proto.OpRoute, Session: "tttttt", Route: &proto.Route{Native: "thread-c"}})
		return cli, app
	})
}

// A mute set on a name stays with the session that holds the name after a handoff, and does not
// follow the session that gave the name up and waits for a new one — both from another device and
// from the same device (#875).
func TestAMuteStaysWithTheNameAfterHandoff(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	g := newPushGate(true)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.session("bbbbbb", "")
	aliceName := b.named(alice)
	bobName := b.named(bob)

	cli := hello(t, b, proto.Hello{ID: "xxxxxx", Agent: "claude-code", ClaudeSession: "conv-m", ClientPID: 700})
	name := b.named(cli)
	eventually(t, "a to see "+name+" on the command line", func() bool { return a.nameOf(cli) == name })

	if r := a.mute("aaaaaa", name, false); r.Error != "" {
		t.Fatalf("alice mutes %s: %s", name, r.Error)
	}
	if r := b.mute("bbbbbb", name, false); r.Error != "" {
		t.Fatalf("bob mutes %s: %s", name, r.Error)
	}
	// Wait until b has read alice's mute from presence.
	eventually(t, "cli's send to alice to be refused", func() bool {
		return b.do(proto.Request{Op: proto.OpSend, Session: "xxxxxx", To: aliceName, Subject: "before handoff"}).Error != ""
	})

	// Handoff to T3 while no pool name is available: cli gives name up and waits for a new one.
	g.set(false)
	t3 := hello(t, b, proto.Hello{ID: "tttttt", Agent: "claude-code in T3", ClaudeSession: "conv-m", ClientPID: 800,
		Route: proto.Route{T3Thread: "thread-m", T3CLI: "/opt/t3/bin/t3"}, ByThread: true})
	b.nameIs(t3, name)
	eventually(t, "a to see "+name+" on T3", func() bool { return a.nameOf(t3) == name })

	// The mute still applies to T3 (which holds the name): direct sends are refused, and a
	// broadcast is dropped for both alice and bob.
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "tttttt", To: aliceName, Subject: "from taker to alice"}); r.Error == "" {
		t.Errorf("taker %s (%s) wasn't refused sending to %s who muted %s", t3, name, aliceName, name)
	}
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "tttttt", To: bobName, Subject: "from taker to bob"}); r.Error == "" {
		t.Errorf("taker %s (%s) wasn't refused sending to %s who muted %s", t3, name, bobName, name)
	}
	b.send("tttttt", "*", "broadcast from taker", "")

	// The mute must NOT follow cli (which gave the name up): direct sends to alice and bob succeed
	// and arrive without being labeled as name.
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "xxxxxx", To: alice, Subject: "from giver to alice"}); r.Error != "" {
		t.Fatalf("giver %s was refused sending to alice: %s", cli, r.Error)
	}
	if m := inboxMessage(t, a, "aaaaaa"); m.Subject != "from giver to alice" || m.FromName == name {
		t.Errorf("alice got %q from %q (addr %s), want giver's message not labeled %s", m.Subject, m.FromName, m.From, name)
	}
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "xxxxxx", To: bob, Subject: "from giver to bob"}); r.Error != "" {
		t.Fatalf("giver %s was refused sending to bob: %s", cli, r.Error)
	}
	if m := inboxMessage(t, b, "bbbbbb"); m.Subject != "from giver to bob" || m.FromName == name {
		t.Errorf("bob got %q from %q (addr %s), want giver's message not labeled %s", m.Subject, m.FromName, m.From, name)
	}
	g.set(true)
}

func hello(t *testing.T, d *device, h proto.Hello) string {
	t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), h)
	if err != nil {
		t.Fatalf("hello %s: %v", h.ID, err)
	}
	t.Cleanup(func() { conn.Close() })
	return resp.Self
}
