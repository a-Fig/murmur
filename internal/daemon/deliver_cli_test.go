package daemon_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// Tests of handing a message over to a Codex or OpenCode session outside T3, through the client's
// own command line (#135). The client is the stand-in `t3` is, which records how it was run.

// A message for a Codex session outside T3 runs `codex queue` for its thread, in its Codex home,
// with the message framed as a turn. It is then no longer unread.
func TestMessageForACodexSessionGoesThroughItsQueue(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	home := filepath.Join(t.TempDir(), "codex home")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{CodexCLI: exe, Native: "0199aaaa-thread-bob", CodexHome: home})
	aliceName := a.named(alice)
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	body := "Are you changing it?\nI say \"yes\" — and 100% über-sure."
	a.send("aaaaaa", bob, "auth middleware?", body)
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })

	c := calls()[0]
	if len(c.Args) != 4 || strings.Join(c.Args[:3], " ") != "queue --thread 0199aaaa-thread-bob" || !strings.HasPrefix(c.Args[3], "--message=") {
		t.Fatalf("codex was run as %q", c.Args)
	}
	if c.CodexHome != home {
		t.Errorf("CODEX_HOME was %q, want the session's, %q", c.CodexHome, home)
	}
	turn := strings.TrimPrefix(c.Args[3], "--message=")
	for _, want := range []string{
		"Message from " + aliceName + " (test",
		"via " + brand.Product + ":\n\nauth middleware?\n\n" + body + "\n",
		"Reply with " + brand.Product + "'s `send` to " + aliceName + " if a reply is needed.",
	} {
		if !strings.Contains(turn, want) {
			t.Errorf("the turn lacks %q:\n%s", want, turn)
		}
	}
	eventually(t, "the message to be marked read", func() bool { return strings.Contains(out.String(), "through Codex's queue") })
	if h, _ := b.page().health(); h.Delivery.CLI != (web.HandoffHealth{Tried: 1, Handed: 1}) || h.Delivery.T3.Tried != 0 || h.Delivery.Socket.Tried != 0 {
		t.Errorf("the health snapshot counts the handoffs %+v", h.Delivery)
	}
	holds(t, "one handoff, and nothing unread", 1500*time.Millisecond, func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 0 && len(calls()) == 1
	})
	if strings.Contains(out.String(), "über-sure") {
		t.Errorf("the log has the message:\n%s", out.String())
	}
}

// A message for an OpenCode session outside T3 runs `opencode api session.prompt` for its session,
// with a body that holds the turn and asks for it to wait behind a running one.
func TestMessageForAnOpenCodeSessionGoesThroughItsServer(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{OpenCodeCLI: exe, Native: "ses_bob123"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "auth middleware?", "Are you \"changing\" it?\nSay so.")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })

	c := calls()[0]
	if len(c.Args) != 6 || strings.Join(c.Args[:5], " ") != "api session.prompt --param sessionID=ses_bob123 -d" {
		t.Fatalf("opencode was run as %q", c.Args)
	}
	var sent struct{ Text, Delivery string }
	if err := json.Unmarshal([]byte(c.Args[5]), &sent); err != nil {
		t.Fatalf("the body %q: %v", c.Args[5], err)
	}
	if sent.Delivery != "queue" || !strings.Contains(sent.Text, "auth middleware?\n\nAre you \"changing\" it?\nSay so.\n") {
		t.Errorf("the body is %+v", sent)
	}
	eventually(t, "the message to be marked read", func() bool { return strings.Contains(out.String(), "through OpenCode's server") })
	holds(t, "one handoff, and nothing unread", 1500*time.Millisecond, func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 0 && len(calls()) == 1
	})
}

// The command line is run only for a conversation the client has named, and only while the
// session's MCP server is connected: a Codex thread nobody has open would keep the message in its
// queue, out of the inbox, until it is next resumed.
func TestClientCommandLineNeedsItsConversationAndARunningSession(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	alice, _ := a.session("aaaaaa", "")
	// Codex's server learns the thread from its first tool call: until then there is nowhere to queue.
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{CodexCLI: exe})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "before the thread is known", "hello")
	holds(t, "no handoff without a thread", 700*time.Millisecond, func() bool { return len(calls()) == 0 })

	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "bbbbbb", Route: &proto.Route{CodexCLI: exe, Native: "thread-bob"}}); r.Error != "" {
		t.Fatal(r.Error)
	}
	eventually(t, "the handoff once the thread is known", func() bool { return len(calls()) == 1 })

	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 && !a.listsSession(bob) })
	a.send("aaaaaa", bob, "while you were away", "wake up")
	holds(t, "nothing queued for a session that has gone", time.Second, func() bool { return len(calls()) == 1 })
}

// A session in T3 is reached through T3, whatever else its route names.
func TestSessionInT3IsNotReachedByItsClientCommandLine(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob", CodexCLI: exe, Native: "0199-thread"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "hi", "there")
	eventually(t, "the handoff", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-bob -" {
		t.Errorf("the handoff ran %q, want t3's thread send", c.Args)
	}
}

// A command that fails leaves the message to the inbox and the hook's notice, and the log says so
// once, without the message.
func TestFailedClientCommandFallsBackToTheNotice(t *testing.T) {
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	setFakeT3(t, exe, fakeT3Fail, "1")
	var out syncBuf
	a, b := newDevice(t, remote), newDeviceWith(t, remote, func(d *device) { d.logTo = &out })
	alice, _ := a.session("aaaaaa", "")
	bob, _ := b.sessionRoute("bbbbbb", "", proto.Route{OpenCodeCLI: exe, Native: "ses_bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) && b.seesSession(alice) })

	a.send("aaaaaa", bob, "first", "body one 7c1e")
	eventually(t, "the failed handoff", func() bool {
		return strings.Contains(out.String(), "handing a message over as a turn through OpenCode's server failed")
	})
	holds(t, "one try", time.Second, func() bool { return len(calls()) == 1 })
	if got := inboxOf(b, "bbbbbb"); len(got) != 1 || got[0] != "first" {
		t.Errorf("the inbox has %q, want the message that wasn't handed over", got)
	}
	if h, _ := b.page().health(); h.Delivery.CLI != (web.HandoffHealth{Tried: 1, Failed: 1}) {
		t.Errorf("the health snapshot counts the handoffs %+v", h.Delivery)
	}
	if strings.Contains(out.String(), "7c1e") {
		t.Errorf("the log has the message:\n%s", out.String())
	}
}
