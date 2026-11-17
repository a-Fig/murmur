package daemon_test

import (
	"context"
	"net"
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
		h.Agent, h.Route = "claude-code in T3", proto.Route{T3Thread: thread, T3CLI: "/opt/t3/bin/t3"}
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

	// Another conversation in the same thread, after /clear, is still the thread's; one of another
	// thread is another name.
	other, _ := b.claudeIn("eeeeee", 1100, "conv-2", "thread-2")
	if got := b.named(other); got == name {
		t.Errorf("a conversation in another thread took %s", name)
	}
}
