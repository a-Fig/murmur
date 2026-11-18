package daemon_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
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

// A session in T3 that registered before it knew its thread, and found it later, gave the thread
// its name itself: the thread's next session takes its address, as #642 has it, not one of its own
// (Opus's review of #643).
func TestAT3SessionThatFoundItsThreadLateKeepsItsAddress(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "ssssss", Agent: "claude-code in T3", ClaudeSession: "conv-s", ClientPID: 700})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	name := b.named(resp.Self)
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "ssssss", Route: &proto.Route{T3Thread: "thread-s", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	conn.Close()
	eventually(t, "the first to stop", func() bool { return busLines(b, resp.Self, "stopped") == 1 })
	next, _ := b.claudeIn("tttttt", 800, "conv-s", "thread-s")
	if !strings.HasSuffix(next, "/ssssss") {
		t.Errorf("the thread's next session registered as %s, not at the address its name is at", next)
	}
	b.nameIs(next, name)
}

// A command-line conversation that T3 imports, whose server registers before it knows its thread,
// registers at the command line's address, which is made from the Claude session as the command
// line's is. The name is still the command line's: the thread's next session keeps an address of
// its own, and the command line, resuming the conversation while T3 has it, registers at its own
// (Sol's review of #643).
func TestAT3SessionThatFoundItsThreadLateLeavesTheCommandLineItsAddress(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	stopped := func(addr string, n int) {
		t.Helper()
		eventually(t, addr+" to stop", func() bool { return busLines(b, addr, "stopped") == n })
	}
	cli, conn := b.claudeIn("aaaaaa", 700, "conv-l", "")
	name := b.named(cli)
	conn.Close()
	stopped(cli, 1)

	// T3 imports it; its server registers at the same ID, made from the Claude session, and finds
	// its thread after.
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "aaaaaa", Agent: "claude-code in T3", ClaudeSession: "conv-l", ClientPID: 800})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	b.nameIs(resp.Self, name)
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "aaaaaa", Route: &proto.Route{T3Thread: "thread-l", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	conn.Close()
	stopped(resp.Self, 2)

	next, _ := b.claudeIn("tttttt", 900, "conv-l", "thread-l")
	if !strings.HasSuffix(next, "/tttttt") {
		t.Errorf("the thread's next session registered as %s, at the command line's address", next)
	}
	b.nameIs(next, name)
	again, _ := b.claudeIn("aaaaaa", 1000, "conv-l", "")
	if !strings.HasSuffix(again, "/aaaaaa") {
		t.Errorf("the command line's session came back as %s", again)
	}
	b.nameIs(again, name)
}

// After /clear in T3 the thread's name is also the old conversation's, which its Claude session no
// longer matches: whichever of the thread and the old conversation, resumed on the command line,
// registers while the other runs has a name of its own (docs/design/session-names.md, Sol's review
// of #643).
func TestAfterClearInT3TheThreadAndTheOldConversationShareAName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	t3, conn := b.claudeIn("tttttt", 800, "conv-c1", "thread-c")
	name := b.named(t3)
	b.hook("conv-c2", 800) // /clear
	b.nameIs(t3, name)

	// The thread running, the old conversation resumed on the command line has a name of its own.
	cli, cconn := b.claudeIn("cccccc", 900, "conv-c1", "")
	if got := b.named(cli); got == name {
		t.Errorf("the old conversation took %s from the thread", name)
	}
	b.nameIs(t3, name)
	cconn.Close()
	eventually(t, cli+" to stop", func() bool { return busLines(b, cli, "stopped") == 1 })

	// The thread stopped, the old conversation has the name, and the thread back meanwhile has
	// another.
	conn.Close()
	eventually(t, t3+" to stop", func() bool { return busLines(b, t3, "stopped") == 1 })
	cli, _ = b.claudeIn("cccccc", 1000, "conv-c1", "")
	b.nameIs(cli, name)
	back, _ := b.claudeIn("uuuuuu", 1100, "conv-c2", "thread-c")
	if got := b.named(back); got == name {
		t.Errorf("the thread took %s back from the old conversation", name)
	}
}

// lateT3Import has T3 import conversation claude, whose name is name, into thread: its server, of
// process pid, registers at id before it knows the thread, as one made from the Claude session
// is, finds the thread, and stops, its stop the nth the daemon has logged for id.
func lateT3Import(t *testing.T, b *device, id string, pid int, claude, thread, name string, n int) {
	t.Helper()
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "claude-code in T3", ClaudeSession: claude, ClientPID: pid})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b.nameIs(resp.Self, name)
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: id, Route: &proto.Route{T3Thread: thread, T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	conn.Close()
	eventually(t, resp.Self+" to stop", func() bool { return busLines(b, resp.Self, "stopped") == n })
}

// A name the command line was given before #643, which T3 imports into a thread it finds late, is
// still the command line's: a daemon from before #643 kept no record of which program a name was
// given in, so the thread keeps an address of its own (Sol's review of 7e4ead79).
func TestANameFromBeforeTheUpgradeLeavesTheCommandLineItsAddress(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	cli, conn := b.claudeIn("aaaaaa", 700, "conv-u", "")
	name := b.named(cli)
	conn.Close()
	eventually(t, cli+" to stop", func() bool { return busLines(b, cli, "stopped") == 1 })

	// The state a daemon from before #643 would have left.
	b.stop()
	path := filepath.Join(b.gitDir, brand.StateDir, "state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]json.RawMessage
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"namedInT3", "namedFromCLI", "cliNames"} {
		delete(st, k)
	}
	if raw, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()

	lateT3Import(t, b, "aaaaaa", 800, "conv-u", "thread-u", name, 1) // the first on this daemon's log
	next, _ := b.claudeIn("tttttt", 900, "conv-u", "thread-u")
	if !strings.HasSuffix(next, "/tttttt") {
		t.Errorf("the thread's next session registered as %s, at the command line's address", next)
	}
	b.nameIs(next, name)
	again, _ := b.claudeIn("aaaaaa", 1000, "conv-u", "")
	if !strings.HasSuffix(again, "/aaaaaa") {
		t.Errorf("the command line's session came back as %s", again)
	}
	b.nameIs(again, name)
}

// Of two MCP servers of one command-line process on a conversation, the later is a session of its
// own, with its own name (#38). When T3 goes on with the conversation, the first gives it up, and
// the later keeps its name and what it has unread (Sol's review of 7e4ead79).
func TestTheLaterOfTwoServersKeepsItsOwnWhenT3GoesOn(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	first, _ := b.claudeIn("aaaaaa", 700, "conv-p", "")
	name := b.named(first)
	later, _ := b.claudeIn("bbbbbb", 700, "conv-p", "")
	mine := b.named(later)
	if mine == name {
		t.Fatalf("the later server took %s", name)
	}
	b.claudeIn("ssssss", 600, "conv-other", "")
	b.send("ssssss", mine, "for the later server", "")

	t3, _ := b.claudeIn("tttttt", 800, "conv-p", "thread-p")
	b.nameIs(t3, name)
	eventually(t, "the first server's session to have a name of its own", func() bool {
		other := b.nameOf(first)
		return other != "" && other != name && other != mine
	})
	b.nameIs(later, mine)
	if m := inboxMessage(t, b, "bbbbbb"); m.Subject != "for the later server" {
		t.Errorf("the later server has %q", m.Subject)
	}
}
