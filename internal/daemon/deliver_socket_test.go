package daemon_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of handing a message over to a Claude Code session outside T3, through the messaging
// socket Claude Code offers (#57). The socket is a stand-in, a unix socket that records the lines
// written to it; the stand-in `t3` of deliver_test.go shows nothing is run for these sessions.

// fakeSocket records every line written to it, from every connection, and when the first came.
type fakeSocket struct {
	path  string
	mu    sync.Mutex
	got   []string
	first time.Time
}

func useFakeSocket(t *testing.T) *fakeSocket {
	t.Helper()
	dir, err := os.MkdirTemp("", "murs") // short: macOS limits the path of a unix socket
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeSocket{path: filepath.Join(dir, "s")}
	ln, err := net.Listen("unix", f.path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				sc.Buffer(nil, 1<<21)
				for sc.Scan() {
					f.mu.Lock()
					if len(f.got) == 0 {
						f.first = time.Now()
					}
					f.got = append(f.got, sc.Text())
					f.mu.Unlock()
				}
			}()
		}
	}()
	return f
}

func (f *fakeSocket) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

// firstAt is when the socket got its first line, zero if it has none.
func (f *fakeSocket) firstAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.first
}

// socketTurn reads what a session got on its socket as the daemon's one handoff: the auth line
// with the token, then the user line, whose content it returns.
func socketTurn(t *testing.T, lines []string, token string) string {
	t.Helper()
	if len(lines) != 2 {
		t.Fatalf("the socket got %d lines, want the auth line and the message: %q", len(lines), lines)
	}
	var auth map[string]string
	if err := json.Unmarshal([]byte(lines[0]), &auth); err != nil || auth["type"] != "auth" || auth["token"] != token {
		t.Fatalf("auth line %q: %v", lines[0], err)
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &msg); err != nil || msg.Type != "user" || msg.Message.Role != "user" {
		t.Fatalf("user line %q: %v", lines[1], err)
	}
	return msg.Message.Content
}

// A message for a Claude Code session outside T3 is written to its messaging socket: the auth line
// with the token the MCP server passed on, then the user message, framed as a turn. It is then no
// longer unread, and the log has neither the token nor the socket's address.
func TestMessageForAClaudeCodeSessionGoesThroughItsMessagingSocket(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	_, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{MsgSocket: sock.path, MsgToken: "tok-bob-1"})
	aliceName := a.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	body := "Are you changing it?\nI say \"yes\" — and 100% über-sure."
	a.send("aaaaaa", bob, "auth middleware?", body)
	eventually(t, "the handoff", func() bool { return len(sock.lines()) >= 2 })

	turn := socketTurn(t, sock.lines(), "tok-bob-1")
	for _, want := range []string{
		"Message from " + aliceName + " (test",
		"via " + brand.Product + ":\n\nauth middleware?\n\n" + body + "\n",
		"Reply with " + brand.Product + "'s `send` to " + aliceName + " if a reply is needed.",
	} {
		if !strings.Contains(turn, want) {
			t.Errorf("the turn lacks %q:\n%s", want, turn)
		}
	}
	eventually(t, "the message to be marked read", func() bool { return strings.Contains(out.String(), "through the messaging socket") })
	if h, _ := b.page().health(); h.Delivery.Socket != (web.HandoffHealth{Tried: 1, Handed: 1}) || h.Delivery.T3.Tried != 0 {
		t.Errorf("the health snapshot counts the handoffs %+v", h.Delivery)
	}
	holds(t, "one handoff, and nothing unread", 1500*time.Millisecond, func() bool {
		inbox, notices := b.unread("bbbbbb", "claude-bob")
		return len(inbox) == 0 && len(notices) == 0 && len(sock.lines()) == 2
	})
	if n := len(calls()); n != 0 {
		t.Errorf("t3 was run %d times for a session that isn't in T3", n)
	}
	for _, secret := range []string{"tok-bob-1", sock.path} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the log has %q:\n%s", secret, out.String())
		}
	}
}

// A session in T3 is reached through T3 even if its environment also had a messaging socket: the
// socket is T3's, and T3 may not take a turn it didn't start.
func TestSessionInT3IsNotWrittenToItsSocket(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", MsgSocket: sock.path, MsgToken: "tok"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "hi", "there")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })
	holds(t, "nothing on the socket", 500*time.Millisecond, func() bool { return len(sock.lines()) == 0 })
	pg := b.page()
	eventually(t, "the handoff counted", func() bool {
		h, _ := pg.health()
		return h.Delivery.T3.Handed == 1
	})
	if h, _ := pg.health(); h.Delivery.T3 != (web.HandoffHealth{Tried: 1, Handed: 1}) || h.Delivery.Socket.Tried != 0 {
		t.Errorf("the health snapshot counts the handoffs %+v", h.Delivery)
	}
}

// The socket belongs to a running session: once its MCP server is gone, so is its token, and a
// message sent then is not written to it.
func TestMessagingSocketNeedsTheSessionToBeRunning(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	sock := useFakeSocket(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	alice, _ := a.session("aaaaaa", "")
	bob, conn := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{MsgSocket: sock.path, MsgToken: "tok"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "while you are here", "hello")
	eventually(t, "the handoff", func() bool { return len(sock.lines()) == 2 })

	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 && !a.listsSession(bob) })
	a.send("aaaaaa", bob, "while you were away", "wake up")
	holds(t, "nothing more on the socket", time.Second, func() bool { return len(sock.lines()) == 2 })
}

// Where the socket can't be written, the message is the hook's notice after one more try, and the
// log says so once, without the socket's address, its token or the message.
func TestFailedSocketHandoffFallsBackToTheNotice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	dir, err := os.MkdirTemp("", "murs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	missing := filepath.Join(dir, "gone-socket-5d2e")
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "claude-bob", proto.Route{MsgSocket: missing, MsgToken: "tok-secret-5d2e"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "body one")
	a.send("aaaaaa", bob, "second", "body two")
	var notices []string
	eventually(t, "both notices", func() bool {
		notices = append(notices, b.hook("claude-bob", 0)...)
		return len(notices) >= 2
	})
	if notices[0] != "first" {
		t.Errorf("notices: %q", notices)
	}
	if h, _ := b.page().health(); h.Delivery.Socket.Failed < 1 || h.Delivery.Socket.Handed != 0 {
		t.Errorf("the health snapshot counts the handoffs %+v", h.Delivery.Socket)
	}
	if n := strings.Count(out.String(), "through the messaging socket failed"); n != 1 {
		t.Errorf("the log says it %d times, want once:\n%s", n, out.String())
	}
	for _, secret := range []string{"tok-secret-5d2e", "gone-socket-5d2e", "body one"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("the log has %q:\n%s", secret, out.String())
		}
	}
}
