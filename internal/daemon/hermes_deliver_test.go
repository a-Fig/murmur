package daemon_test

import (
	"context"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// Hermes sessions get #202's delivery through Murmur's plugin (#1184): its hooks run `mur hook
// --agent hermes`, which registers the session as `mur tool` does (a command's hello keyed by
// Native, #975) and names it by that Native, and the plugin opens a socket for the session that
// takes Claude Code's messaging-socket lines.

// hermesHello registers a Hermes session as the plugin's `mur tool` and `mur hook` do, with the
// socket the plugin opened for it.
func hermesHello(t *testing.T, d *device, id, native string, sock *fakeSocket, token string) proto.Response {
	t.Helper()
	r := proto.Route{Native: native}
	if sock != nil {
		r.MsgSocket, r.MsgToken = sock.path, token
	}
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "hermes", Command: true, Route: r})
	if err != nil {
		t.Fatalf("hello %s: %v", id, err)
	}
	conn.Close()
	return resp
}

// A Hermes session with a socket is held while the socket answers, however long it is idle: Hermes
// can start a turn in a session for as long as a client has it open, which is as long as the plugin
// keeps its socket. Once nothing answers there, the session ends.
func TestAHermesSessionIsHeldWhileItsSocketAnswers(t *testing.T) {
	defer daemon.SetCommandHold(150*time.Millisecond, 50*time.Millisecond)()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 100 * time.Millisecond })
	hermesHello(t, b, "f1f1f1", "hermes:local-held", sock, "tok-h")
	time.Sleep(time.Second) // past the idle hold, several times
	if !registered(b, "f1f1f1") {
		t.Fatal("the session ended while its socket answered")
	}
	sock.stop()
	eventually(t, "the session to stop once its socket is gone", func() bool { return logHas(b, "/f1f1f1 stopped") })
}

// While a Hermes session works its message is a notice, at the end of its turn the message whole
// keeps it going, and once it is idle a message goes in through the plugin's socket.
func TestAHermesSessionGetsNoticesWhileBusyAndTurnsWhenIdle(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "") // the sender, on the same device
	const native = "hermes:local-turns"
	bob := hermesHello(t, b, "f2f2f2", native, sock, "tok-hermes").Self

	b.piHook(proto.OpNotices, native)
	b.piHook(proto.OpTurnEnd, native)

	b.piHook(proto.OpNotices, native) // a prompt: at work
	b.send("aaaaaa", bob, "while busy", "the body")
	var r proto.Response
	eventually(t, "the notice", func() bool { r = b.piHook(proto.OpNotices, native); return len(r.Messages) > 0 })
	if r.Messages[0].Subject != "while busy" {
		t.Errorf("the notice: %+v", r.Messages)
	}
	holds(t, "nothing on the socket while it works", time.Second, func() bool { return len(sock.lines()) == 0 })
	if got := b.piHook(proto.OpTurnEnd, native).Read; len(got) != 1 || got[0].Body != "the body" {
		t.Fatalf("the end of its turn gave %+v", got)
	}

	b.send("aaaaaa", bob, "while idle", "wake up")
	eventually(t, "the turn on the socket", func() bool { return len(sock.lines()) >= 2 })
	socketTurn(t, sock.lines(), "tok-hermes")
}

// A later call that names another socket moves the session's turns to it: the plugin opens a new
// one where the old one failed.
func TestAHermesSessionsLaterHelloMovesItsSocket(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	first, second := useFakeSocket(t), useFakeSocket(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "")
	const native = "hermes:local-moved"
	bob := hermesHello(t, b, "f3f3f3", native, first, "tok-1").Self
	hermesHello(t, b, "f3f3f3", native, second, "tok-2")
	b.piHook(proto.OpNotices, native)
	b.piHook(proto.OpTurnEnd, native)
	b.send("aaaaaa", bob, "to the new socket", "hi")
	eventually(t, "the turn on the second socket", func() bool { return len(second.lines()) >= 2 })
	socketTurn(t, second.lines(), "tok-2")
	if got := first.lines(); len(got) != 0 {
		t.Errorf("the old socket got %q", got)
	}
}
