package daemon_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// #51: a Claude Code session's ID was the start of Claude Code's own, and is a hash of it now, so a
// conversation from the command line that is resumed after the upgrade comes back at another
// address. It keeps its name, which follows Claude Code's ID rather than the address, and what is
// sent to its old address, from another device as from this one, is the new session's.
func TestAConversationResumedAtAHashedIDKeepsItsNameAndMessages(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	// Before the upgrade: the ID is Claude Code's first six characters.
	old, conn, _ := b.sessionPID("0a1b2c", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procP)
	name := b.named(old)
	conn.Close()
	eventually(t, "the old session to stop", func() bool { return busLines(b, old, "stopped") == 1 })

	// After it: the same conversation, resumed, at its hashed ID.
	resumed, _, _ := b.sessionPID("f88c42", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ)
	b.nameIs(resumed, name)
	eventually(t, "presence", func() bool { return !a.listsSession(old) && a.nameOf(resumed) == name })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: old, Subject: "from another device"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	b.noticed("0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ, "from another device")
	if r := b.do(proto.Request{Op: proto.OpSend, To: old, Subject: "from this device"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	b.noticed("0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff", procQ, "from this device")
}

// #51 with the resume inside the old session's grace, as it may be right after the upgrade (Opus's
// review): the new session took the name while the old one was still among the sessions, so its
// unread messages weren't adopted then. They are when its grace runs out.
func TestAResumeInTheOldSessionsGraceTakesItsInbox(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 2*time.Second, 0)
	const claude = "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff"
	old, conn, _ := b.sessionPID("0a1b2c", claude, procP)
	name := b.named(old)
	if r := b.do(proto.Request{Op: proto.OpSend, To: old, Subject: "unread"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "unread") })
	conn.Close()

	resumed, _, _ := b.sessionPID("f88c42", claude, procQ)
	b.nameIs(resumed, name)
	if n := busLines(b, old, "stopped"); n != 0 {
		t.Fatalf("the old session stopped before the resume; the test needs it in its grace")
	}
	b.noticed(claude, procQ, "unread")
}

// And a resumed session with a way to be handed a message is handed what it took when the old
// one's grace ran out (Sol's review): nothing else may come to wake delivery then. The grace is an
// hour, and the test ends it once the new session holds the name, so a slow machine can't run it
// out first. Not parallel: the hook that ends it is the package's, and another test's daemon
// would read it as this one sets it.
func TestAResumeInTheOldSessionsGraceIsHandedItsInbox(t *testing.T) {
	const oldID, claude = "0a1b2c", "0a1b2c3d-aaaa-bbbb-cccc-ddddeeeeffff"
	dropped, over := make(chan struct{}), make(chan struct{})
	t.Cleanup(daemon.SetGraceBegins(func(id string) <-chan struct{} {
		if id != oldID {
			return nil
		}
		close(dropped)
		return over
	}))
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	b := newTimedDevice(t, remote, time.Hour, 0)
	old, conn, _ := b.sessionPID(oldID, claude, procP)
	name := b.named(old)
	if r := b.do(proto.Request{Op: proto.OpSend, To: old, Subject: "unread"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "unread") })
	conn.Close()
	select { // the daemon has it as dropped, so the resume takes its name
	case <-dropped:
	case <-time.After(30 * time.Second):
		t.Fatal("the old session's drop was never seen")
	}

	c, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "f88c42", Agent: "test", ClaudeSession: claude,
		ClientPID: procQ, Route: proto.Route{CodexCLI: exe, Native: "0199aaaa-thread-bob"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	b.nameIs(resp.Self, name)
	if len(calls()) != 0 {
		t.Fatalf("codex was run before the old session's grace ended: %q", calls()[0].Args)
	}
	close(over)
	eventually(t, "the old session to stop", func() bool { return busLines(b, old, "stopped") == 1 })
	// Soon after, and not at the daemon's kick a minute after it started, which is what took it
	// before: the stop wakes nothing else.
	for end := time.Now().Add(15 * time.Second); len(calls()) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("what the session took was not handed over")
		}
	}
	if args := calls()[0].Args; len(args) != 4 || !strings.Contains(args[3], "unread") {
		t.Errorf("codex was run as %q", args)
	}
}
