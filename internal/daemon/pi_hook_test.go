package daemon_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// Pi sessions get #202's delivery through Murmur's extension (#205): it runs the hook, which names
// the session by Pi's own ID for it (the session's file, Native), and opens a socket that takes
// Claude Code's messaging-socket lines.

// piHook is the hook as Pi's extension runs it, for the session whose file is file: after a prompt
// or a tool call (OpNotices), or at the end of its work (OpTurnEnd, which can keep it going).
func (d *device) piHook(op, file string) proto.Response {
	d.t.Helper()
	r := d.do(proto.Request{Op: op, Native: file, Whole: true, Continues: op == proto.OpTurnEnd})
	if r.Error != "" {
		d.t.Fatalf("%s: %s", op, r.Error)
	}
	return r
}

// While a Pi session works its message is a notice, at the end of its work the message whole keeps
// it going, and once it is idle a message goes in through the extension's socket, as for Claude Code.
func TestPiSessionGetsNoticesWhileBusyAndTurnsWhenIdle(t *testing.T) {
	remote := newRemote(t)
	sock := useFakeSocket(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	const file = "/home/u/.pi/agent/sessions/--work--/2026-11-09_pi.jsonl"
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "bbbbbb", Agent: "pi",
		Route: proto.Route{Native: file, MsgSocket: sock.path, MsgToken: "tok-pi"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	bob := resp.Self
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// One turn, so the daemon knows its end is seen.
	b.piHook(proto.OpNotices, file)
	b.piHook(proto.OpTurnEnd, file)

	b.piHook(proto.OpNotices, file) // a prompt: at work
	a.send("aaaaaa", bob, "while busy", "the body")
	var r proto.Response
	eventually(t, "the notice", func() bool { r = b.piHook(proto.OpNotices, file); return len(r.Messages) > 0 })
	if r.Messages[0].Subject != "while busy" || r.Messages[0].Body != "" || r.Unread != 1 {
		t.Errorf("the notice: %+v, unread %d", r.Messages, r.Unread)
	}
	holds(t, "nothing on the socket while it works", time.Second, func() bool { return len(sock.lines()) == 0 })

	if got := b.piHook(proto.OpTurnEnd, file).Read; len(got) != 1 || got[0].Body != "the body" {
		t.Fatalf("the end of its work gave %+v", got)
	}
	if got := b.piHook(proto.OpTurnEnd, file).Read; len(got) != 0 {
		t.Fatalf("the next end gave %v", viewSubjects(got))
	}

	a.send("aaaaaa", bob, "while idle", "wake up")
	eventually(t, "the turn on the socket", func() bool { return len(sock.lines()) >= 2 })
	socketTurn(t, sock.lines(), "tok-pi")
}

// The hook of a Pi session speaks only for the session with its file: not another agent's session,
// which has no Claude Code ID either, nor a Claude Code session.
func TestPiHookSpeaksOnlyForItsSession(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "")
	other, _ := b.sessionRoute("cccccc", "", proto.Route{Native: "codex-thread-1"})
	claude, _ := b.sessionRoute("dddddd", "claude-d", proto.Route{Native: "/some/file.jsonl"})
	eventually(t, "presence", func() bool { return a.seesSession(other) && a.seesSession(claude) })

	a.send("aaaaaa", other, "for the other", "")
	a.send("aaaaaa", claude, "for claude", "")
	eventually(t, "both to reach b", func() bool { return logHas(b, "for the other") && logHas(b, "for claude") })
	for _, file := range []string{"/some/file.jsonl", "/no/such/file.jsonl"} {
		if r := b.piHook(proto.OpNotices, file); len(r.Messages) != 0 || r.Self != "" {
			t.Errorf("a Pi hook for %s got %+v", file, r)
		}
	}
	if inbox, _ := b.unread("cccccc", ""); len(inbox) != 1 {
		t.Errorf("the other session's inbox: %v", inbox)
	}
}

// piSession registers a Pi session whose MCP server has the session's file and, unless sock is nil,
// the extension's socket.
func (d *device) piSession(id, file string, sock *fakeSocket) (string, net.Conn) {
	d.t.Helper()
	r := proto.Route{Native: file}
	if sock != nil {
		r.MsgSocket, r.MsgToken = sock.path, "tok-"+id
	}
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "pi", Route: r})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn
}

// After Escape or an error the extension's Stop says the work was interrupted: the session is
// idle at once, nothing is given back, and what it was told of goes in through the socket as a
// turn, as for a T3 turn the user stopped. Before, it waited for the user's next prompt.
func TestAnInterruptedPiSessionIsIdle(t *testing.T) {
	remote := newRemote(t)
	sock := useFakeSocket(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	const file = "/s/interrupted.jsonl"
	bob, _ := b.piSession("bbbbbb", file, sock)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	b.piHook(proto.OpNotices, file)
	b.piHook(proto.OpTurnEnd, file)
	b.piHook(proto.OpNotices, file) // at work
	a.send("aaaaaa", bob, "told mid-work", "the body")
	eventually(t, "the notice", func() bool { return len(b.piHook(proto.OpNotices, file).Messages) > 0 })
	holds(t, "nothing on the socket while it works", 500*time.Millisecond, func() bool { return len(sock.lines()) == 0 })

	r := b.do(proto.Request{Op: proto.OpTurnEnd, Native: file})
	if r.Error != "" || len(r.Read) != 0 {
		t.Fatalf("an interrupted end gave %+v", r)
	}
	eventually(t, "the turn on the socket", func() bool { return len(sock.lines()) >= 2 })
	if turn := socketTurn(t, sock.lines(), "tok-bbbbbb"); !strings.Contains(turn, "told mid-work\n\nthe body") {
		t.Errorf("the turn: %s", turn)
	}
}

// A Pi session in its grace, its MCP server gone, is told nothing by its hook: as for Claude Code,
// it is told once it is back.
func TestPiHookWaitsOutTheGrace(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	const file = "/s/grace.jsonl"
	bob, conn := b.piSession("bbbbbb", file, nil)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	b.piHook(proto.OpNotices, file)
	b.piHook(proto.OpTurnEnd, file)
	b.piHook(proto.OpNotices, file)
	// A body, so the hook tells of it rather than giving it whole (#355).
	a.send("aaaaaa", bob, "while away", "the body")
	eventually(t, "the message to reach b", func() bool { return logHas(b, "while away") })

	conn.Close()
	eventually(t, "the session in its grace", func() bool {
		d := b.do(proto.Request{Op: proto.OpLookup, Name: bob}).Lookup
		return d != nil && d.Dropped
	})
	if r := b.piHook(proto.OpNotices, file); len(r.Messages) != 0 || r.Self != "" {
		t.Fatalf("in its grace the hook got %+v", r)
	}
	b.piSession("bbbbbb", file, nil)
	if r := b.piHook(proto.OpNotices, file); len(r.Messages) != 1 || r.Messages[0].Subject != "while away" {
		t.Fatalf("back, the hook got %+v", r)
	}
}

// The page's busy setting gives a Pi session its message whole while it works, as it does Claude
// Code (#204): read, and out of the inbox.
func TestBusyPiSessionGetsTheWholeMessage(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	const file = "/s/whole.jsonl"
	bob, _ := b.piSession("bbbbbb", file, nil)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })
	pg := b.page()
	if code, body := pg.call("PUT", "/api/settings", pg.token, `{"wake":{"when":"always"},"busy":"whole"}`); code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", code, body)
	}

	b.piHook(proto.OpNotices, file)
	b.piHook(proto.OpTurnEnd, file)
	b.piHook(proto.OpNotices, file)
	a.send("aaaaaa", bob, "whole", "all of it")
	var r proto.Response
	eventually(t, "the message", func() bool { r = b.piHook(proto.OpNotices, file); return len(r.Read) > 0 })
	if r.Read[0].Body != "all of it" || len(r.Messages) != 0 {
		t.Errorf("the hook was given %+v", r)
	}
	if inbox, _ := b.unread("bbbbbb", ""); len(inbox) != 0 {
		t.Errorf("still in the inbox: %v", inbox)
	}
}

// A Pi session in a T3 thread (#462) is told of a message by the extension's hook while it works,
// as Pi is in a terminal, and not given it as a turn T3 would queue behind its work. The message
// arrives once: whole at the end of the work, which reads it, and never as a T3 turn after. Once
// the session is idle a message goes in through T3, not the extension's socket, which is there
// under T3 too and unused. T3's database says the thread is at work for as long as Pi is, the
// work its end goes on with included, as T3 ends a Pi run only when Pi has settled.
func TestPiSessionInT3GetsNoticesWhileBusy(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	sock := useFakeSocket(t)
	threads := &fakeThreads{st: map[string]t3db.State{"thread-pi": {LastRun: time.Now()}}}
	a, b := newDevice(t, remote), newT3Device(t, remote, threads)
	alice, _ := a.session("aaaaaa", "")
	const file = `C:\Users\u\.t3\userdata\pi\sessions\thread-pi.jsonl`
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "bbbbbb", Agent: "pi in T3",
		Route: proto.Route{T3CLI: exe, T3Thread: "thread-pi", Native: file, MsgSocket: sock.path, MsgToken: "tok-pi"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	bob := resp.Self
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	// One turn, so the daemon knows its end is seen.
	b.piHook(proto.OpNotices, file)
	b.piHook(proto.OpTurnEnd, file)

	threads.set("thread-pi", t3db.State{Busy: true, LastRun: time.Now()})
	b.piHook(proto.OpNotices, file) // a prompt: at work
	a.send("aaaaaa", bob, "while busy", "the body")
	var r proto.Response
	eventually(t, "the notice", func() bool { r = b.piHook(proto.OpNotices, file); return len(r.Messages) > 0 })
	if r.Messages[0].Subject != "while busy" || r.Messages[0].Body != "" || r.Unread != 1 {
		t.Errorf("the notice: %+v, unread %d", r.Messages, r.Unread)
	}
	holds(t, "no turn through T3 or the socket while it works", time.Second, func() bool {
		return len(calls()) == 0 && len(sock.lines()) == 0
	})

	if got := b.piHook(proto.OpTurnEnd, file).Read; len(got) != 1 || got[0].Body != "the body" {
		t.Fatalf("the end of its work gave %+v", got)
	}
	// The message kept its work going; the end after that finds nothing more, and it is idle.
	if got := b.piHook(proto.OpTurnEnd, file).Read; len(got) != 0 {
		t.Fatalf("the next end gave %v", viewSubjects(got))
	}
	threads.set("thread-pi", t3db.State{LastRun: time.Now()})
	holds(t, "the message it has read isn't sent again as a turn", 2*time.Second, func() bool {
		return len(calls()) == 0 && len(sock.lines()) == 0
	})

	a.send("aaaaaa", bob, "while idle", "wake up")
	eventually(t, "the turn through T3", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; !strings.Contains(strings.Join(c.Args, " "), "thread-pi") || !strings.Contains(c.Stdin, "wake up") {
		t.Errorf("the T3 call: %+v", c)
	}
	if n := len(sock.lines()); n != 0 {
		t.Errorf("%d lines went to the extension's socket, which T3 sessions don't use", n)
	}
}
