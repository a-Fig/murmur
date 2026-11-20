package daemon_test

import (
	"context"
	"crypto/rand"
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

// A conversation T3 had before /clear, resumed on the command line while the thread still had its
// name, has a name of its own there. Once the thread has stopped, the command line's session is the
// conversation's only holder: when T3 imports the conversation again it gives it up, with what it
// had unread, as any holder does (Sol's review of 8d031b55).
func TestAConversationResumedWhileT3HadItsNameGoesOnInT3WithItsMessages(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	t3, conn := b.claudeIn("tttttt", 800, "conv-a", "thread-1")
	name := b.named(t3)
	b.hook("conv-b", 800) // /clear
	b.nameIs(t3, name)
	cli, _ := b.claudeIn("cccccc", 900, "conv-a", "")
	if mine := b.named(cli); mine == name {
		t.Fatalf("the command line took %s", name)
	}
	other, _ := b.claudeIn("ssssss", 600, "conv-other", "")
	b.named(other)
	b.send("ssssss", b.nameOf(cli), "for the command line", "")
	conn.Close()
	eventually(t, t3+" to stop", func() bool { return busLines(b, t3, "stopped") == 1 })

	b.claudeIn("uuuuuu", 1000, "conv-a", "thread-2")
	// T3 hands what it took on to its thread as a turn, so neither inbox has it then.
	if left := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(left) != 0 {
		t.Errorf("the command line kept %d unread after the conversation went on in T3", len(left))
	}
}

// nameGate is a NameSource that blocks while it is shut, so that a test can hold the assigner's
// push in flight: the assigner picks names outside the daemon's lock. held is signalled when a
// pick waits at the gate.
type nameGate struct {
	mu   sync.Mutex
	cond *sync.Cond
	open bool
	held chan struct{}
}

func newNameGate() *nameGate {
	g := &nameGate{held: make(chan struct{}, 1)}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *nameGate) Read(p []byte) (int, error) {
	g.mu.Lock()
	for !g.open {
		select {
		case g.held <- struct{}{}:
		default:
		}
		g.cond.Wait()
	}
	g.mu.Unlock()
	return rand.Read(p)
}

func (g *nameGate) let() {
	g.mu.Lock()
	g.open = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *nameGate) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-g.held:
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
}

// A T3 session that hasn't found its thread waits on the assigner for its name, and stops before the
// name lands. The name is still T3's: the next session, which finds the thread, leaves the thread's
// next session the address the name is at, as #642 has it (Sol's review of 8d031b55).
func TestANameThatLandsAfterItsT3SessionStoppedIsStillT3s(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newNameGate()
	b := newDeviceWith(t, remote, func(d *device) {
		d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g
	})
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code in T3", ClaudeSession: "conv-4", ClientPID: 800})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	g.waitHeld(t)
	conn.Close()
	eventually(t, resp.Self+" to stop", func() bool { return busLines(b, resp.Self, "stopped") == 1 })
	g.let()
	eventually(t, "the name to land", func() bool { return logHas(b, " -> "+resp.Self) })

	conn, resp, err = b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code in T3", ClaudeSession: "conv-4", ClientPID: 900})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	name := b.named(resp.Self)
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "xxxxxx", Route: &proto.Route{T3Thread: "thread-4", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	conn.Close()
	eventually(t, resp.Self+" to stop again", func() bool { return busLines(b, resp.Self, "stopped") == 2 })

	next, _ := b.claudeIn("tttttt", 1000, "conv-4", "thread-4")
	if !strings.HasSuffix(next, "/xxxxxx") {
		t.Errorf("the thread's next session registered as %s, not at the address %s is at", next, name)
	}
	b.nameIs(next, name)
}

// With no name set aside, a T3 session that hasn't found its thread waits on the assigner, and finds
// the thread while the name is being pushed. The command line, resuming the conversation later,
// should have the name T3 had (#874).
func TestAThreadFoundWhileItsNameIsPushedKeepsOneName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newPushGate(false)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code in T3", ClaudeSession: "conv-3", ClientPID: 800})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	select {
	case <-g.held: // the push for its name is out
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "xxxxxx", Route: &proto.Route{T3Thread: "thread-3", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	g.set(true)
	name := b.named(resp.Self)
	conn.Close()
	eventually(t, "T3 to stop", func() bool { return busLines(b, resp.Self, "stopped") == 1 })

	conn, resp, err = b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code", ClaudeSession: "conv-3", ClientPID: 900})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if got := b.named(resp.Self); got != name {
		t.Errorf("the command line is %s, T3 was %s", got, name)
	}
}

// A T3 session that finds its thread while its name is being pushed, and stops before the push
// lands, still keeps one name and its registered address: the thread's next ByThread session takes
// that address and name even after a provider switch, and `claude --resume` gets the same name
// (#874).
func TestAThreadFoundThenStoppedWhileItsNameIsPushedKeepsNameAndAddress(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newPushGate(false)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code in T3", ClaudeSession: "conv-s1", ClientPID: 800})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	select {
	case <-g.held:
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "xxxxxx", Route: &proto.Route{T3Thread: "thread-s1", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	conn.Close()
	eventually(t, "T3 to stop", func() bool { return busLines(b, resp.Self, "stopped") == 1 })
	g.set(true)
	eventually(t, "the name to land", func() bool { return logHas(b, " -> "+resp.Self) })

	next, nconn := b.claudeIn("tttttt", 900, "conv-s2", "thread-s1")
	if !strings.HasSuffix(next, "/xxxxxx") {
		t.Errorf("the thread's next session registered as %s, not at %s", next, resp.Self)
	}
	name := b.named(next)
	nconn.Close()
	eventually(t, "the second T3 session to stop", func() bool { return busLines(b, next, "stopped") == 2 })

	cli, _ := b.claudeIn("xxxxxx", 1000, "conv-s1", "")
	if got := b.named(cli); got != name {
		t.Errorf("the command line is %s, T3 was %s", got, name)
	}
}

// A command-line conversation that T3 imports while the command line's name is still being pushed
// keeps that one name, and leaves the command line its address: the thread's next ByThread session
// keeps an address of its own, because the name was given to the command line (#874).
func TestACLIImportWhileItsNameIsPushedKeepsOneNameAndCLIAddress(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newPushGate(false)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "aaaaaa", Agent: "claude-code", ClaudeSession: "conv-i1", ClientPID: 700})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	select {
	case <-g.held:
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
	conn.Close()
	eventually(t, "the command line to stop", func() bool { return busLines(b, resp.Self, "stopped") == 1 })

	t3, tconn := b.claudeIn("tttttt", 800, "conv-i1", "thread-i1")
	g.set(true)
	name := b.named(t3)
	tconn.Close()
	eventually(t, "T3 to stop", func() bool { return busLines(b, t3, "stopped") == 1 })

	next, _ := b.claudeIn("tttttt", 900, "conv-i1", "thread-i1")
	if !strings.HasSuffix(next, "/tttttt") {
		t.Errorf("T3's next session registered as %s, at the command line's address", next)
	}
	b.nameIs(next, name)
	cli, _ := b.claudeIn("aaaaaa", 1000, "conv-i1", "")
	if !strings.HasSuffix(cli, "/aaaaaa") {
		t.Errorf("the command line came back as %s", cli)
	}
	b.nameIs(cli, name)
}

// If a T3 session finds its thread and /clears while its name is being pushed, and the old
// conversation is resumed on the command line before the push lands, the two live sessions must
// not end up sharing a name (#874).
func TestAThreadClearedWhileItsNameIsPushedDoesNotShareALiveName(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	g := newPushGate(false)
	t.Cleanup(func() { g.set(true) })
	b := newDeviceWith(t, remote, func(d *device) { d.grace, d.keep, d.names = 300*time.Millisecond, time.Hour, g })
	conn, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "xxxxxx", Agent: "claude-code in T3", ClaudeSession: "conv-c1", ClientPID: 800})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	select {
	case <-g.held:
	case <-time.After(time.Minute):
		t.Fatal("no names push reached the gate")
	}
	if r := b.do(proto.Request{Op: proto.OpRoute, Session: "xxxxxx", Route: &proto.Route{T3Thread: "thread-c1", T3CLI: "/opt/t3/bin/t3"}}); r.Error != "" {
		t.Fatalf("route: %s", r.Error)
	}
	b.hook("conv-c2", 800) // /clear in T3
	cli, _ := b.claudeIn("cccccc", 900, "conv-c1", "")
	g.set(true)

	t3Name := b.named(resp.Self)
	cliName := b.named(cli)
	if cliName == t3Name {
		t.Errorf("two live sessions share the name %s (%s and %s)", t3Name, resp.Self, cli)
	}
}
