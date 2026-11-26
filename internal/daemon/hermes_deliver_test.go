package daemon_test

import (
	"context"
	"strings"
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
		// A socket that answers is one the plugin since #1258 opens, and says so.
		r.MsgSocket, r.MsgToken, r.MsgReplies = sock.path, token, sock.answer != nil
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

// A session whose socket still answers ends once it has been idle for socketIdle: Hermes keeps a
// session after its ACP client goes for as long as the gateway runs.
func TestAHermesSessionIdleTooLongEndsThoughItsSocketAnswers(t *testing.T) {
	defer daemon.SetCommandHold(time.Hour, 50*time.Millisecond)()
	defer daemon.SetSocketIdle(400 * time.Millisecond)()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 100 * time.Millisecond })
	hermesHello(t, b, "f4f4f4", "hermes:local-idle", sock, "tok-i")
	eventually(t, "the idle session to stop", func() bool { return logHas(b, "/f4f4f4 stopped") })
}

// While a Hermes session works its message is a notice. The end of its turn keeps nothing going, as
// the plugin couldn't put the messages in without a refusal losing them: what is unread goes in
// through the plugin's socket once the session is idle, as does a message to it then.
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
	// As `mur hook --agent hermes` ends a turn: it keeps nothing going.
	if r := b.do(proto.Request{Op: proto.OpTurnEnd, Native: native}); r.Error != "" || len(r.Read) != 0 {
		t.Fatalf("the end of its turn gave %+v", r)
	}
	eventually(t, "the unread message as a turn on the socket", func() bool { return len(sock.lines()) >= 2 })
	// A Hermes session has Murmur's tools, though a command registered it: its turn says to answer
	// with the send tool, not with #913's shell command.
	turn := socketTurn(t, sock.lines()[:2], "tok-hermes")
	if !strings.Contains(turn, "the body") || !strings.Contains(turn, "Murmur's `send`") || strings.Contains(turn, "mur send") {
		t.Errorf("the turn:\n%s", turn)
	}

	b.send("aaaaaa", bob, "while idle", "wake up")
	eventually(t, "the next turn on the socket", func() bool { return len(sock.lines()) >= 4 })
	if turn := socketTurn(t, sock.lines()[2:4], "tok-hermes"); !strings.Contains(turn, "wake up") {
		t.Errorf("the next turn:\n%s", turn)
	}
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

const (
	accepted = `{"type":"accepted"}`
	refused  = `{"type":"refused"}`
)

// A plugin's socket that answers says whether Hermes took each message (#1258). One it refused
// stays in the inbox, and the session ends at once, as the plugin has closed its socket: nothing
// after it goes to that socket either. Both go in once the session has a socket again.
func TestAHermesSessionsRefusedTurnStaysInItsInbox(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	closing := useAnsweringSocket(t, func(string) string { return refused })
	var out syncBuf
	b := newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	b.session("aaaaaa", "")
	const native = "hermes:local-refused"
	bob := hermesHello(t, b, "f5f5f5", native, closing, "tok-1").Self
	b.piHook(proto.OpNotices, native)
	hermesStop(t, b, native)

	b.send("aaaaaa", bob, "the first", "refused")
	eventually(t, "the session to end on the refusal", func() bool { return strings.Contains(out.String(), "its plugin refused a turn") })
	b.send("aaaaaa", bob, "the second", "after it")
	holds(t, "nothing more on the socket that refused", time.Second, func() bool { return len(closing.lines()) == 2 })
	if strings.Contains(out.String(), "handing a message over as a turn through the messaging socket failed") {
		t.Error("a refusal was logged as a failed handoff, which leaves the message to notices")
	}

	open := useAnsweringSocket(t, func(string) string { return accepted })
	hermesHello(t, b, "f5f5f5", native, open, "tok-2")
	eventually(t, "both messages on the new socket", func() bool { return len(open.lines()) >= 4 })
	first, second := socketTurn(t, open.lines()[:2], "tok-2"), socketTurn(t, open.lines()[2:4], "tok-2")
	if !strings.Contains(first+second, "refused") || !strings.Contains(first+second, "after it") {
		t.Errorf("the turns:\n%s\n%s", first, second)
	}
	eventually(t, "the messages marked read once accepted", func() bool {
		return strings.Count(out.String(), "as a turn, through the messaging socket") >= 2
	})
}

// A socket that never answers leaves the message to the hook's notice, as a handoff that may or
// may not have gone in does: no second turn of it, and not lost.
func TestAHermesSocketThatDoesntAnswerLeavesTheMessageToNotices(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useAnsweringSocket(t, func(string) string { return `{"type":"what"}` })
	var out syncBuf
	b := newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	b.session("aaaaaa", "")
	const native = "hermes:local-mute"
	bob := hermesHello(t, b, "f6f6f6", native, sock, "tok-m").Self
	b.piHook(proto.OpNotices, native)
	hermesStop(t, b, native)
	b.send("aaaaaa", bob, "unanswered", "body")
	eventually(t, "the failed handoff", func() bool {
		return strings.Contains(out.String(), "its messages show as notices through the hook instead")
	})
	var r proto.Response
	eventually(t, "the notice at the next prompt", func() bool {
		r = b.piHook(proto.OpNotices, native)
		return len(r.Messages) > 0
	})
	if r.Messages[0].Subject != "unanswered" {
		t.Errorf("the notice: %+v", r.Messages)
	}
}

// A Hermes session is at work from the first prompt the hook sees, before any Stop: its plugin
// fires one at every turn's end (#1259). A message during that first turn is a notice, not a turn
// queued behind it, and goes in through the socket once the turn ends.
func TestAHermesSessionIsAtWorkBeforeItsFirstStop(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useAnsweringSocket(t, func(string) string { return accepted })
	b := newDevice(t, remote)
	b.session("aaaaaa", "")
	const native = "hermes:local-first"
	bob := hermesHello(t, b, "f7f7f7", native, sock, "tok-f").Self
	b.piHook(proto.OpNotices, native) // the first prompt since it registered
	b.send("aaaaaa", bob, "during the first turn", "body")
	var r proto.Response
	eventually(t, "the notice", func() bool { r = b.piHook(proto.OpNotices, native); return len(r.Messages) > 0 })
	holds(t, "nothing on the socket during the first turn", time.Second, func() bool { return len(sock.lines()) == 0 })
	hermesStop(t, b, native)
	eventually(t, "the message as a turn once the turn ended", func() bool { return len(sock.lines()) >= 2 })
}

// hermesStop is the end of a Hermes session's turn as its plugin's hook reports it: one that keeps
// nothing going.
func hermesStop(t *testing.T, d *device, native string) {
	t.Helper()
	if r := d.do(proto.Request{Op: proto.OpTurnEnd, Native: native}); r.Error != "" {
		t.Fatalf("the end of its turn: %s", r.Error)
	}
}
