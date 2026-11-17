package daemon_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// claudeIn registers a Claude Code session of process pid on conversation claude, in T3 thread
// thread if it isn't "".
func (d *device) claudeIn(id string, pid int, claude, thread string) (string, net.Conn) {
	d.t.Helper()
	h := proto.Hello{ID: id, Agent: "claude-code", ClaudeSession: claude, ClientPID: pid}
	if thread != "" {
		// A T3 session's server makes its ID from the thread, as it does since #642.
		h.Agent, h.Route, h.ByThread = "claude-code in T3", proto.Route{T3Thread: thread, T3CLI: "/opt/t3/bin/t3"}, true
	}
	conn, resp, err := d.c.Hello(context.Background(), h)
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn
}

// A Claude Code conversation keeps its name when it moves between the command line and T3 (#643),
// in either direction, though its name key is its Claude session's in one and its T3 thread's in
// the other: T3 imports a command-line conversation and resumes its Claude session, and `claude
// --resume` goes on with one T3 had. With the other still open, the one that registered last takes
// the name, as a Codex thread's does (#568).
func TestAClaudeConversationKeepsItsNameBetweenTheCommandLineAndT3(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	stopped := func(addr string) {
		t.Helper()
		eventually(t, addr+" to stop", func() bool { return busLines(b, addr, "stopped") == 1 })
	}

	// From the command line to T3, the command line's session stopped.
	cli, conn := b.claudeIn("aaaaaa", 700, "conv-1", "")
	name := b.named(cli)
	conn.Close()
	stopped(cli)
	t3, conn := b.claudeIn("bbbbbb", 800, "conv-1", "thread-1")
	b.nameIs(t3, name)

	// Back to the command line, T3's session stopped.
	conn.Close()
	stopped(t3)
	back, conn := b.claudeIn("cccccc", 900, "conv-1", "")
	b.nameIs(back, name)

	// With the command line's session still open, T3's goes on with the conversation and takes the
	// name; the command line's gets one of its own.
	t3again, _ := b.claudeIn("dddddd", 1000, "conv-1", "thread-1")
	b.nameIs(t3again, name)
	eventually(t, "the command line's session to have a name of its own", func() bool {
		other := b.nameOf(back)
		return other != "" && other != name
	})
	conn.Close()

	// A conversation in another thread has another name.
	other, _ := b.claudeIn("eeeeee", 1100, "conv-2", "thread-2")
	if got := b.named(other); got == name {
		t.Errorf("a conversation in another thread took %s", name)
	}
}

// A conversation begun in T3 keeps its name on the command line too, and back in T3; and a T3
// session that took the command line's name doesn't take its session's address with it, which
// the command line goes on using, so the two never ask for one ID (Opus's review of #643).
func TestAClaudeConversationBegunInT3KeepsItsName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	stopped := func(addr string) {
		t.Helper()
		eventually(t, addr+" to stop", func() bool { return busLines(b, addr, "stopped") == 1 })
	}
	t3, conn := b.claudeIn("tttttt", 800, "conv-9", "thread-9")
	name := b.named(t3)
	conn.Close()
	stopped(t3)
	cli, conn := b.claudeIn("aaaaaa", 900, "conv-9", "")
	b.nameIs(cli, name)

	// The other way: a name the command line gave, which the registry has at its session's
	// address. T3's session keeps its own ID, and the command line, back while T3's is open,
	// registers at its own.
	other, oconn := b.claudeIn("cccccc", 700, "conv-8", "")
	first := b.named(other)
	oconn.Close()
	stopped(other)
	in, _ := b.claudeIn("uuuuuu", 1000, "conv-8", "thread-8")
	if !strings.HasSuffix(in, "/uuuuuu") {
		t.Errorf("T3's session registered as %s, at the command line's address", in)
	}
	b.nameIs(in, first)
	again, _ := b.claudeIn("cccccc", 1100, "conv-8", "")
	if !strings.HasSuffix(again, "/cccccc") {
		t.Errorf("the command line's session came back as %s", again)
	}
	b.nameIs(again, first)
	conn.Close()
}

// A session that has given its conversation up keeps the name it got then, when yet another
// program goes on with the conversation (Opus's review of #643).
func TestAClaudeSessionGivesItsConversationUpOnce(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	a, _ := b.claudeIn("aaaaaa", 700, "conv-1", "")
	name := b.named(a)
	t3, _ := b.claudeIn("bbbbbb", 800, "conv-1", "thread-1")
	b.nameIs(t3, name)
	var mine string
	eventually(t, "the command line's session to have a name of its own", func() bool {
		mine = b.nameOf(a)
		return mine != "" && mine != name
	})
	c, _ := b.claudeIn("cccccc", 900, "conv-1", "")
	b.named(c)
	b.nameIs(a, mine)
}
