package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of a session following Claude Code's session ID (#9) and of a name found at the session
// that holds it (#37). Claude Code changes a conversation's session ID on /clear, /branch and a
// /resume of another; the MCP server keeps the one it started with, and only the hook sees the
// live one. The hook's CLAUDE_PID is its Claude Code process, which is the MCP server's parent.

// sessionPID is session for an MCP server whose parent process is pid. It returns the whole reply.
func (d *device) sessionPID(id, claude string, pid int) (string, net.Conn, proto.Response) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(),
		proto.Hello{ID: id, Agent: "test", ClaudeSession: claude, ClientPID: pid})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn, resp
}

// helloPID is a server of process pid registering the session id, again: it retries while the
// daemon hasn't yet seen the previous connection close.
func (d *device) helloPID(id, claude string, pid int) (net.Conn, proto.Response) {
	d.t.Helper()
	var conn net.Conn
	var resp proto.Response
	eventually(d.t, "a session to register again", func() bool {
		c, r, err := d.c.Hello(context.Background(),
			proto.Hello{ID: id, Agent: "test", ClaudeSession: claude, ClientPID: pid, Reconnect: true})
		if err != nil {
			return false
		}
		d.t.Cleanup(func() { c.Close() })
		conn, resp = c, r
		return true
	})
	return conn, resp
}

// hook is what the Claude Code hook asks the daemon: the notices of conversation claude, running
// in process pid.
func (d *device) hook(claude string, pid int) []string {
	d.t.Helper()
	var subjects []string
	for _, m := range d.do(proto.Request{Op: proto.OpNotices, ClaudeSession: claude, ClientPID: pid}).Messages {
		subjects = append(subjects, m.Subject)
	}
	return subjects
}

// noticed waits for the hook of (claude, pid) to be told of the message with this subject.
func (d *device) noticed(claude string, pid int, subject string) {
	d.t.Helper()
	eventually(d.t, "a notice of "+subject, func() bool {
		for _, s := range d.hook(claude, pid) {
			if s == subject {
				return true
			}
		}
		return false
	})
}

// nameHeld waits for d's who to show the session at addr under a name other than the ones given,
// and returns it.
func (d *device) nameHeld(addr string, not ...string) string {
	d.t.Helper()
	var name string
	eventually(d.t, "a new name for "+addr, func() bool {
		name = d.nameOf(addr)
		if name == "" {
			return false
		}
		for _, n := range not {
			if name == n {
				return false
			}
		}
		return true
	})
	return name
}

// nameIs waits for d's who to show the session at addr under exactly this name, on a device that
// isn't stale, as seesSession does.
func (d *device) nameIs(addr, want string) {
	d.t.Helper()
	eventually(d.t, addr+" to be called "+want, func() bool {
		name, _, fresh := d.whoShows(addr)
		return name == want && fresh
	})
}

const (
	procP = 4242 // the Claude Code process of the sessions under test
	procQ = 4343 // another one
	procR = 4444 // a third
	procS = 4545 // a fourth
)

// A /clear in a session: the MCP server still says conversation A, the hook says B. The session
// follows: it shows another name to everyone, notices for B reach it, and a /resume back to A
// brings A's name back, with nothing but presence telling the others.
func TestSwitchFollowsTheHooksClaudeID(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	sender, _ := a.session("aaaaaa", "claude-sender")
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)
	eventually(t, "presence", func() bool { return a.nameOf(bob) == nameA && b.seesSession(sender) })

	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameA, Subject: "for A"})
	b.noticed("claude-a", procP, "for A")

	// /clear: the hook is the first to hear of the new conversation.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	nameB := b.nameHeld(bob, nameA)
	a.nameIs(bob, nameB)

	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameB, Subject: "for B"})
	b.noticed("claude-b", procP, "for B")
	// What the notices only announced is in the inbox, under the session's one inbox.
	if got := inboxOf(b, "bbbbbb"); len(got) != 2 {
		t.Fatalf("inbox after the switch: %v, want both messages", got)
	}

	// /resume back to A.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-a", ClientPID: procP})
	b.nameIs(bob, nameA)
	a.nameIs(bob, nameA)
	if !logHas(b, "followed Claude Code") {
		t.Fatalf("the switch is not in the log: %+v", b.do(proto.Request{Op: proto.OpLog, Limit: 50}).Events)
	}
	// Nothing started or stopped: it is the same session.
	if n := busLines(b, bob, "started"); n != 1 {
		t.Fatalf("the switch wrote %d starts for the session", n)
	}
	if n := busLines(b, bob, "stopped"); n != 0 {
		t.Fatalf("the switch wrote a stop for the session")
	}
	noNameTwice(t, readRegistry(t, remote))
}

// A conversation Claude Code starts and throws away (the one it opens with before a /resume) gets
// a name that is never used again; the session then follows to the conversation that matters, which
// is a new one (a new name, as #20 assigns) or one named in an earlier run, whose name it gets back.
func TestSwitchAfterAThrowawayStart(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)

	// An earlier run of conversation X: named, then stopped.
	old, oldConn, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(old)
	oldConn.Close()
	eventually(t, "the earlier run to stop", func() bool { return busLines(b, old, "stopped") == 1 })

	// A new process opens on a throwaway conversation T.
	bob, _, _ := b.sessionPID("dddddd", "claude-throwaway", procQ)
	nameT := b.named(bob)
	if nameT == nameX {
		t.Fatalf("a new conversation took the name %s of another", nameX)
	}

	// The user resumes X: the hook reports X for process Q.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(bob, nameX)

	// And a conversation never seen before gets a name of its own.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procQ})
	b.nameHeld(bob, nameX, nameT)
	noNameTwice(t, readRegistry(t, remote))
}

// Hooks and servers from before PIDs, which send none: a hook without CLAUDE_PID finds a session
// by its Claude ID and moves nothing with a new one, and a session whose server gave no PID is
// found by its Claude ID, whatever PID a hook has, and never moved. Taken from the issue9-haiku
// build's TestHookWithNoMatchMovesNothing, with the server's half added.
func TestWithoutPIDsNothingMoves(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	old, _, _ := b.sessionPID("bbbbbb", "claude-old-server", 0)
	mine, _, _ := b.sessionPID("cccccc", "claude-mine", procP)
	nameOld, nameMine := b.named(old), b.named(mine)
	b.do(proto.Request{Op: proto.OpSend, To: nameOld, Subject: "for the old server"})
	b.do(proto.Request{Op: proto.OpSend, To: nameMine, Subject: "for mine"})

	b.noticed("claude-old-server", procQ, "for the old server")
	b.noticed("claude-mine", 0, "for mine")
	for _, h := range []struct {
		claude string
		pid    int
	}{{"claude-elsewhere", procQ}, {"claude-elsewhere", 0}, {"claude-moved", 0}} {
		if got := b.hook(h.claude, h.pid); len(got) != 0 {
			t.Fatalf("hook %s from %d was told %v", h.claude, h.pid, got)
		}
	}
	holds(t, "both names", time.Second, func() bool { return b.nameOf(old) == nameOld && b.nameOf(mine) == nameMine })

	// The same session, from its own process, with a new ID: it moves.
	b.hook("claude-moved", procP)
	b.nameHeld(mine, nameMine, nameOld)
	if got := b.nameOf(old); got != nameOld {
		t.Fatalf("the old server's session was renamed to %s", got)
	}
}

// A hook's ID and PID that match no session change nothing, and a PID never moves a session that
// belongs to another process.
func TestSwitchNeverTakesAnotherSession(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	sender, _ := a.session("aaaaaa", "claude-sender")
	p, _, _ := b.sessionPID("bbbbbb", "claude-p", procP)
	q, _, _ := b.sessionPID("cccccc", "claude-q", procQ)
	nameP, nameQ := b.named(p), b.named(q)
	eventually(t, "presence", func() bool { return a.nameOf(p) == nameP && a.nameOf(q) == nameQ && b.seesSession(sender) })

	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameP, Subject: "for P"})
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameQ, Subject: "for Q"})
	eventually(t, "the message to be queued", func() bool { return logHas(b, "for P") && logHas(b, "for Q") })

	// A hook with an ID and a process nobody has: nothing changes and nothing is told.
	if got := b.hook("claude-stranger", 999); len(got) != 0 {
		t.Fatalf("a stranger's hook got %v", got)
	}
	// A hook of process Q with a conversation of its own: it moves Q's session, and P's notices
	// are not Q's.
	if got := b.hook("claude-q2", procQ); len(got) != 1 || got[0] != "for Q" {
		t.Fatalf("Q's hook got %v", got)
	}
	nameQ2 := b.nameHeld(q, nameQ)
	if got := b.nameOf(p); got != nameP {
		t.Fatalf("a hook of process Q renamed P's session from %s to %s", nameP, got)
	}
	// A hook of process Q that names P's conversation: the ID matches P's session, but the process
	// is Q's, so P's notices are not told to it and P keeps its name.
	if got := b.hook("claude-p", procQ); len(got) != 0 {
		t.Fatalf("Q's hook, with P's conversation ID, got %v", got)
	}
	// P's session still answers to its own conversation and process.
	if got := b.hook("claude-p", procP); len(got) != 1 || got[0] != "for P" {
		t.Fatalf("P's hook got %v", got)
	}
	// The stranger, once more: no one's name moved.
	b.hook("claude-stranger", 999)
	holds(t, "P's name", time.Second, func() bool { return b.nameOf(p) == nameP })
	b.nameHeld(q, nameQ, nameQ2, nameP) // Q's session has moved on twice, to names of its own
	if n := busLines(b, p, "stopped") + busLines(b, q, "stopped"); n != 0 {
		t.Fatalf("a session stopped")
	}
}

// The MCP server of a session that switched registers again, as a restarted one does, with the ID
// its environment still has. The session keeps the new ID and its name, which the reply says so the
// server can use it from then on; a server of another process has no such claim and is taken as it says.
func TestReconnectAfterASwitchKeepsTheLiveID(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 10*time.Second, 0)
	bob, conn, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)

	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	nameB := b.nameHeld(bob, nameA)

	// Any call tells the server the live ID.
	if got := b.do(proto.Request{Op: proto.OpWho, Session: "bbbbbb"}).ClaudeSession; got != "claude-b" {
		t.Fatalf("the daemon says the live ID is %q", got)
	}

	// A restarted server, still with A in its environment: the same process, so the daemon's ID stands.
	conn.Close()
	conn, resp := b.helloPID("bbbbbb", "claude-a", procP)
	if resp.ClaudeSession != "claude-b" || resp.Name != nameB {
		t.Fatalf("a restarted server got ID %q and name %q, want claude-b and %s", resp.ClaudeSession, resp.Name, nameB)
	}
	holds(t, "the new name", time.Second, func() bool { return b.nameOf(bob) == nameB })

	// The server, now knowing B, says so: the same.
	conn.Close()
	conn, resp = b.helloPID("bbbbbb", "claude-b", procP)
	if resp.ClaudeSession != "claude-b" || resp.Name != nameB {
		t.Fatalf("a server that knew got ID %q and name %q, want claude-b and %s", resp.ClaudeSession, resp.Name, nameB)
	}

	// A server of another process is trusted: that is a conversation on its own.
	conn.Close()
	_, resp = b.helloPID("bbbbbb", "claude-a", procQ)
	if resp.ClaudeSession != "claude-a" || resp.Name != nameA {
		t.Fatalf("a server of another process got ID %q and name %q, want claude-a and %s", resp.ClaudeSession, resp.Name, nameA)
	}
}

// A daemon that restarts after a switch hears the live ID from the server, which learned it from
// the daemon before. Later the server restarts within the grace (an /mcp reconnect), with the stale
// ID still in its environment. The daemon's ID for a session of the same process stands, though no
// hook has spoken since the restart.
func TestStaleTakeoverAfterADaemonRestartKeepsTheLiveID(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 10*time.Second, 0)
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	nameB := b.nameHeld(bob, nameA)

	b.restart()
	conn, resp := b.helloPID("bbbbbb", "claude-b", procP)
	if resp.Name != nameB {
		t.Fatalf("back after the restart as %q, want %s", resp.Name, nameB)
	}
	conn.Close()
	eventually(t, "the drop", func() bool {
		return b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b"}).Self == ""
	})
	_, resp = b.helloPID("bbbbbb", "claude-a", procP)
	if resp.ClaudeSession != "claude-b" || resp.Name != nameB {
		t.Fatalf("a stale server got ID %q and name %q, want claude-b and %s", resp.ClaudeSession, resp.Name, nameB)
	}
	holds(t, "the live name", time.Second, func() bool { return b.nameOf(bob) == nameB })
}

// Two MCP servers in one Claude Code process, which is what a project and a user config for Murmur
// make: both move to the conversation the hook reports, and since a name is an address the two do
// not share one.
func TestTwoServersInOneProcessSwitchTogether(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	first, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	second, _, _ := b.sessionPID("cccccc", "claude-a", procP)
	n1, n2 := b.named(first), b.named(second)
	if n1 == n2 {
		t.Fatalf("two live sessions of one conversation both called %s", n1)
	}
	eventually(t, "presence", func() bool { return a.nameOf(first) == n1 && a.nameOf(second) == n2 })

	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	m1, m2 := b.nameHeld(first, n1, n2), b.nameHeld(second, n1, n2)
	if m1 == m2 {
		t.Fatalf("both servers of conversation B are called %s", m1)
	}
	eventually(t, "presence", func() bool { return a.nameOf(first) == m1 && a.nameOf(second) == m2 })

	// And back: the names each had before.
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-a", ClientPID: procP})
	b.nameIs(first, n1)
	b.nameIs(second, n2)
	noNameTwice(t, readRegistry(t, remote))
}

// A message for a name a session had before it moved to another conversation reaches the session,
// which is the same session with the same inbox, and the sender is told it is called something
// else now. A send to the new name has no such note.
func TestSendToAnOldNameReachesTheSession(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	sender, _ := a.session("aaaaaa", "claude-sender")
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)
	eventually(t, "presence", func() bool { return a.nameOf(bob) == nameA && b.seesSession(sender) })

	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	nameB := b.nameHeld(bob, nameA)
	a.nameIs(bob, nameB)

	// From another device, which reads the old name from the registry.
	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameA, Subject: "to the old name"})
	if r.Error != "" || !strings.Contains(r.Note, nameA) || !strings.Contains(r.Note, nameB) {
		t.Fatalf("send to the old name: %+v", r)
	}
	b.noticed("claude-b", procP, "to the old name")

	// From the same device.
	r = b.do(proto.Request{Op: proto.OpSend, To: nameA, Subject: "to the old name, here"})
	if r.Error != "" || !strings.Contains(r.Note, nameB) {
		t.Fatalf("send to the old name on its own device: %+v", r)
	}
	b.noticed("claude-b", procP, "to the old name, here")

	r = a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameB, Subject: "to the new name"})
	if r.Error != "" || r.Note != "" {
		t.Fatalf("send to the current name: %+v", r)
	}
}

// #37: a /resume back to an earlier conversation puts its name on a session whose ID is not the one
// the registry has the name down for. When that session stops too, a send by name must not go to
// the first session's inbox, which is dead; it goes to the one that holds the name now.
func TestNameOfAResumedConversationFindsTheSessionThatHoldsIt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	// Conversation X runs as session one and stops.
	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	a.nameIs(first, nameX)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	// X, resumed, is a new session under a new ID, and gets the name back.
	second, conn2, _ := b.sessionPID("dddddd", "claude-x", procQ)
	b.nameIs(second, nameX)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	// The sender no longer sees either in presence, so it has only the registry's word for the name.
	eventually(t, "presence to empty", func() bool { return !a.listsSession(first) && !a.listsSession(second) })

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "to a resumed conversation"})
	if r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "to a resumed conversation") })

	// Both come back, to read what they were sent.
	b.sessionPID("dddddd", "claude-x", procQ)
	if got := inboxOf(b, "dddddd"); len(got) != 1 || got[0] != "to a resumed conversation" {
		t.Fatalf("the session that holds %s got %v", nameX, got)
	}
	b.sessionPID("cccccc", "claude-z", procP)
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Fatalf("the retired session got %+v", got)
	}
}

// #37 with a switch: a session that followed Claude Code to an earlier conversation holds that
// conversation's name, and a message for the session the registry has it down for (its address, or
// its name once the holder has gone) is the holder's.
func TestMessageForARetiredIDGoesToTheHolder(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	// A session that began on something else and was moved to X by a /resume.
	second, _, _ := b.sessionPID("dddddd", "claude-other", procQ)
	b.named(second)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)

	// The first session's address is a retired one: what is sent to it is the holder's.
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: first, Subject: "to the retired address"})
	b.noticed("claude-x", procQ, "to the retired address")

	// The retired session, back under another conversation, has nothing waiting.
	b.sessionPID("cccccc", "claude-z", procP)
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Fatalf("the retired session got %+v", got)
	}
}

// #37 the other way back. Conversation X's name went to another session, which stopped too, and a
// message sent to X's name meanwhile went on to that session's kept inbox. X is then resumed from
// the command line, so its server has X in its environment and X's own Murmur ID again, or a
// session opened on something else follows a hook to X. Whichever takes X's name takes what was
// kept for it.
func TestTheSessionThatTakesANameTakesWhatWasKeptForIt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	a.nameIs(first, nameX)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	// /resume X in a process opened on a throwaway conversation; that session stops too.
	second, conn2, _ := b.sessionPID("dddddd", "claude-throwaway", procQ)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	eventually(t, "presence to empty", func() bool { return !a.listsSession(first) && !a.listsSession(second) })

	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "while X was gone"})
	eventually(t, "the message to be queued", func() bool { return logHas(b, "while X was gone") })

	// claude --resume X.
	_, conn1, _ = b.sessionPID("cccccc", "claude-x", procP)
	b.nameIs(first, nameX)
	b.noticed("claude-x", procP, "while X was gone")
	if got := inboxOf(b, "cccccc"); len(got) != 1 || got[0] != "while X was gone" {
		t.Fatalf("X, back under its own ID, has %v", got)
	}

	// It stops again, and a message waits in its own inbox. A third process, opened on a throwaway
	// and resumed to X, takes the name and the message.
	conn1.Close()
	eventually(t, "the first to stop again", func() bool { return busLines(b, first, "stopped") == 2 })
	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "while X was gone again"})
	eventually(t, "the message to be queued", func() bool { return logHas(b, "while X was gone again") })
	third, _, _ := b.sessionPID("eeeeee", "claude-throwaway-2", procQ)
	// The hook that moves it is told at once.
	if told := b.hook("claude-x", procQ); len(told) != 1 || told[0] != "while X was gone again" {
		t.Fatalf("the hook that moved the third session to X was told %v", told)
	}
	b.nameIs(third, nameX)
}

// #37 when the registry's session for a name has moved on. Conversation X's first session followed
// a /clear to Y and stopped, so the registry has X down for a session whose last name is Y. X was
// resumed in another session, which stopped too. A send to X from this device goes to that one,
// whose inbox the next session to take X takes, and not to the first, which keeps what it has for
// Y. (From another device the message carries only the first's address, and stays there.)
func TestSendToANameHereGoesToItsLastHolder(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procP})
	b.nameHeld(first, nameX)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, conn2, _ := b.sessionPID("dddddd", "claude-throwaway", procQ)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })

	r := b.do(proto.Request{Op: proto.OpSend, To: nameX, Subject: "for X"})
	if r.Error != "" || r.Messages[0].To != second {
		t.Fatalf("send to %s: %+v, want it sent to %s", nameX, r, second)
	}

	// X, resumed in a third process, is told at once; the first, back as Y, has nothing.
	third, _, _ := b.sessionPID("eeeeee", "claude-throwaway-2", procR)
	if told := b.hook("claude-x", procR); len(told) != 1 || told[0] != "for X" {
		t.Fatalf("the session that took %s back was told %v", nameX, told)
	}
	b.nameIs(third, nameX)
	b.sessionPID("cccccc", "claude-y", procP)
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(got) != 0 {
		t.Fatalf("the first session, which moved on from %s, got %+v", nameX, got)
	}
}

// #37 when a session takes back, in its grace, the name another session took meanwhile. A drops,
// B takes A's key and name under another ID, B drops, and A's server comes back: A holds the name
// again, so once B stops, a message for B is A's.
func TestTakeoverInTheGraceHoldsTheNameAgain(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 10*time.Second, 0)
	dropped := func(what string) {
		t.Helper()
		eventually(t, what, func() bool {
			return b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x"}).Self == ""
		})
	}

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	conn1.Close()
	dropped("the first to drop")

	second, conn2, _ := b.sessionPID("dddddd", "claude-x", procQ)
	b.nameIs(second, nameX)
	conn2.Close()
	dropped("the second to drop")

	b.helloPID("cccccc", "claude-x", procP)
	b.nameIs(first, nameX)
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	if n := busLines(b, first, "stopped"); n != 0 {
		t.Fatalf("the first stopped before its server came back; the test needs it back in its grace")
	}

	b.do(proto.Request{Op: proto.OpSend, To: second, Subject: "for the second"})
	b.noticed("claude-x", procP, "for the second")
}

// movedOn sets b up for #46. X's first session stops, so its kept inbox waits for X; a second
// session, opened on a throwaway conversation, takes X with a /resume and then follows a /clear
// to Y, so it holds X last but is called Y. The second is left running.
func movedOn(t *testing.T, b *device) (first, second, nameX, nameY string, conn2 net.Conn) {
	t.Helper()
	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX = b.named(first)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, conn2, _ = b.sessionPID("dddddd", "claude-throwaway", procQ)
	nameT := b.named(second)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procQ})
	nameY = b.nameHeld(second, nameX, nameT)
	return first, second, nameX, nameY, conn2
}

// resumedAs is a new process, opened on a throwaway conversation, that resumes conversation claude:
// it returns what its hook is told as the session moves there.
func (d *device) resumedAs(id, claude string, pid int) []string {
	d.t.Helper()
	d.sessionPID(id, "claude-throwaway-"+id, pid)
	return d.hook(claude, pid)
}

// #37 and #46 with two moves, which leave two retired IDs. X's first session stopped; a second took
// X, moved on to Y and stopped; a third took Y. A send to X lands in the same place from this device
// as from another, at most one hop from the address the registry has. That is the first's kept
// inbox: the second, the last to hold X, is called Y now, and the third holds Y. The next session to
// take X takes both messages.
func TestSendToANameIsForwardedOnceFromAnyDevice(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")
	first, second, nameX, nameY, conn2 := movedOn(t, b)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	third, _, _ := b.sessionPID("eeeeee", "claude-throwaway-2", procR)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procR})
	b.nameIs(third, nameY)
	eventually(t, "presence", func() bool {
		return !a.listsSession(first) && !a.listsSession(second) && a.nameOf(third) == nameY
	})

	there := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "from another device"})
	here := b.do(proto.Request{Op: proto.OpSend, To: nameX, Subject: "from this device"})
	if there.Error != "" || here.Error != "" || here.Messages[0].To != there.Messages[0].To {
		t.Fatalf("sent from another device: %+v; from this one: %+v", there, here)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "from another device") && logHas(b, "from this device") })
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "eeeeee"}).Messages; len(got) != 0 {
		t.Fatalf("the session that holds %s got %+v", nameY, got)
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); strings.Join(sorted(told), ",") != "from another device,from this device" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// #46 through adoption. The last to hold X has moved to Y and stopped, and the session that holds Y
// now restarts its server in its grace: taking Y again, it takes what was kept for Y, and nothing
// sent for X may be among it. That waits for the next session to take X.
func TestTakingANameAgainTakesNothingForAnother(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 5*time.Second, 0)
	first, second, nameX, nameY, conn2 := movedOn(t, b)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	third, conn3, _ := b.sessionPID("eeeeee", "claude-throwaway-2", procR)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procR})
	b.nameIs(third, nameY)

	b.do(proto.Request{Op: proto.OpSend, To: first, Subject: "for X"})
	eventually(t, "the message to be queued", func() bool { return logHas(b, "for X") })
	conn3.Close()
	eventually(t, "the third to drop", func() bool {
		return b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y"}).Self == ""
	})
	b.helloPID("eeeeee", "claude-y", procR)
	if told := b.hook("claude-y", procR); len(told) != 0 {
		t.Fatalf("the session called %s, back in its grace, was told %v", nameY, told)
	}
	if n := busLines(b, third, "stopped"); n != 0 {
		t.Fatalf("the third stopped before its server came back; the test needs it back in its grace")
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); len(told) != 1 || told[0] != "for X" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// #46 with the holder running. The last to hold X has moved to Y and is still running: a send to X,
// from this device or another, is not its. It waits in X's first session's kept inbox, for the next
// session to take X.
func TestSendToANameSkipsARunningHolderThatMovedOn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")
	first, second, nameX, nameY, _ := movedOn(t, b)
	eventually(t, "presence", func() bool { return !a.listsSession(first) && a.nameOf(second) == nameY })

	here := b.do(proto.Request{Op: proto.OpSend, To: nameX, Subject: "from this device"})
	there := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "from another device"})
	if here.Error != "" || there.Error != "" || here.Messages[0].To != first || there.Messages[0].To != first {
		t.Fatalf("sent from this device: %+v; from another: %+v; want both sent to %s", here, there, first)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "from another device") && logHas(b, "from this device") })
	if told := b.hook("claude-y", procQ); len(told) != 0 {
		t.Fatalf("the session called %s was told %v", nameY, told)
	}
	if told := b.resumedAs("eeeeee", "claude-x", procR); strings.Join(sorted(told), ",") != "from another device,from this device" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// #46 with the holder stopped. The last to hold X has moved to Y and stopped, and nobody holds Y.
// A send to X here waits for X, not with the holder, whose kept inbox goes to the next session to
// take Y.
func TestSendToANameSkipsAStoppedHolderThatMovedOn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	_, second, nameX, nameY, conn2 := movedOn(t, b)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })

	if r := b.do(proto.Request{Op: proto.OpSend, To: nameX, Subject: "for X"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "for X") })
	if told := b.resumedAs("eeeeee", "claude-y", procR); len(told) != 0 {
		t.Fatalf("the next session to take %s was told %v", nameY, told)
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); len(told) != 1 || told[0] != "for X" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// The real hook: a payload on stdin and CLAUDE_PID in its environment drive a switch end to end, and
// the notice for the new conversation comes out on stdout.
func TestHookRunDrivesASwitch(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	sender, _ := a.session("aaaaaa", "claude-sender")
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)
	eventually(t, "presence", func() bool { return a.nameOf(bob) == nameA && b.seesSession(sender) })

	run := func(claude string) string {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"session_id": claude, "hook_event_name": "UserPromptSubmit",
			"cwd": filepath.Dir(b.gitDir)})
		t.Setenv("CLAUDE_PID", strconv.Itoa(procP))
		var out bytes.Buffer
		hook.Run(bytes.NewReader(payload), &out)
		return out.String()
	}

	if out := run("claude-b"); out != "" {
		t.Fatalf("the hook printed %q with nothing to tell", out)
	}
	nameB := b.nameHeld(bob, nameA)
	a.nameIs(bob, nameB)

	a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameB, Subject: "for the new conversation"})
	var out string
	eventually(t, "the hook to print the notice", func() bool {
		out = run("claude-b")
		return out != ""
	})
	if !strings.Contains(out, "for the new conversation") {
		t.Fatalf("the hook printed %q", out)
	}
}

// A subagent started before a /clear still reports the conversation it started in (#231). Its
// hooks are the session's, for what it commits, but the session doesn't follow them back: it keeps
// the new conversation's name, and the do-not-disturb that conversation set.
func TestASubagentsOldClaudeIDMovesNothing(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	bob, _, _ := b.sessionPID("bbbbbb", "claude-a", procP)
	nameA := b.named(bob)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-b", ClientPID: procP})
	nameB := b.nameHeld(bob, nameA)
	b.tagSelf("bbbbbb", []string{"do-not-disturb"}, nil)

	for _, op := range []string{proto.OpNotices, proto.OpToolStart, proto.OpToolEnd, proto.OpTurnEnd} {
		b.do(proto.Request{Op: op, ClaudeSession: "claude-a", ClientPID: procP, Subagent: true, ToolUseID: "t1"})
	}
	if got := b.nameOf(bob); got != nameB {
		t.Fatalf("after a subagent's hooks, the session is called %q, want %q", got, nameB)
	}
	var dnd bool
	for _, dv := range b.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == bob {
				for _, tg := range s.Tags {
					dnd = dnd || tg.Name == "do-not-disturb"
				}
			}
		}
	}
	if !dnd {
		t.Errorf("a subagent's hook lifted the session's do-not-disturb")
	}
	if logHas(b, nameB+" -> "+nameA) {
		t.Errorf("the session followed a subagent's hook back to the conversation it left")
	}
}

// #45: one ID can hold several names over time, and the registry has a name at the ID that first
// took it. Session one took X, followed a /clear to Y and stopped; session two resumed the
// conversation that was X, took X and stopped. A send to X from another device goes to session one's
// address, but the message says it was sent to X: it waits for the next session to take X, not for
// the next to take Y, which is what session one was called last. A send from this device, which
// knows who holds X, already did.
func TestSendToANameItsAddressLeftGoesByTheName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procP})
	nameY := b.nameHeld(first, nameX)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })

	second, conn2, _ := b.sessionPID("dddddd", "claude-throwaway", procQ)
	b.named(second)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	eventually(t, "presence to empty", func() bool { return !a.listsSession(first) && !a.listsSession(second) })

	there := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "from another device"})
	here := b.do(proto.Request{Op: proto.OpSend, To: nameX, Subject: "from this device"})
	// This device knows who holds X and sends there; the other has only the registry's address.
	if there.Error != "" || here.Error != "" || there.Messages[0].To != first {
		t.Fatalf("sent from another device: %+v; from this one: %+v; want the first sent to %s", there, here, first)
	}
	eventually(t, "the messages to be queued", func() bool { return logHas(b, "from another device") && logHas(b, "from this device") })

	if told := b.resumedAs("eeeeee", "claude-y", procR); len(told) != 0 {
		t.Fatalf("the next session to take %s was told %v", nameY, told)
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); strings.Join(sorted(told), ",") != "from another device,from this device" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// #45 the other way round (Opus's review): the message to X comes while nobody holds X, and waits in
// the kept inbox of the session that left it. The next session to take X takes it from there, not
// the next to take Y.
func TestASendToANameItsAddressLeftWaitsForTheName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	first, conn1, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procP})
	nameY := b.nameHeld(first, nameX)
	conn1.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, first, "stopped") == 1 })
	eventually(t, "presence to empty", func() bool { return !a.listsSession(first) })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "for X"}); r.Error != "" || r.Messages[0].To != first {
		t.Fatalf("send: %+v; want it sent to %s", r, first)
	}
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameY, Subject: "for Y"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the messages to be queued", func() bool { return logHas(b, "for X") && logHas(b, "for Y") })

	if told := b.resumedAs("eeeeee", "claude-y", procR); len(told) != 1 || told[0] != "for Y" {
		t.Fatalf("the next session to take %s was told %v", nameY, told)
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); len(told) != 1 || told[0] != "for X" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}

// #45 with the session at the address running (Opus's review): it is called Y now, and a message to
// X, which another session took and left, is not its. It waits for the next session to take X.
func TestARunningSessionGetsNothingForANameAnotherTook(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, 300*time.Millisecond, 0)
	a.session("aaaaaa", "claude-sender")

	first, _, _ := b.sessionPID("cccccc", "claude-x", procP)
	nameX := b.named(first)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-y", ClientPID: procP})
	nameY := b.nameHeld(first, nameX)

	second, conn2, _ := b.sessionPID("dddddd", "claude-throwaway", procQ)
	b.named(second)
	b.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-x", ClientPID: procQ})
	b.nameIs(second, nameX)
	conn2.Close()
	eventually(t, "the second to stop", func() bool { return busLines(b, second, "stopped") == 1 })
	eventually(t, "presence", func() bool { return !a.listsSession(second) && a.nameOf(first) == nameY })

	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameX, Subject: "for X"}); r.Error != "" || r.Messages[0].To != first {
		t.Fatalf("send: %+v; want it sent to %s", r, first)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "for X") })
	if told := b.hook("claude-y", procP); len(told) != 0 {
		t.Fatalf("the session called %s was told %v", nameY, told)
	}
	if told := b.resumedAs("ffffff", "claude-x", procS); len(told) != 1 || told[0] != "for X" {
		t.Fatalf("the next session to take %s was told %v", nameX, told)
	}
}
