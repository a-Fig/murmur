package daemon_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of one address per T3 thread (#642). T3 runs a thread on a provider instance (a Claude
// account or config home), and each instance has its own Claude session for the thread, so a
// switch starts a new MCP server with another Claude session ID. The server takes its ID from the
// thread (T3CODE_THREAD_ID) and says so in its hello (ByThread): the switch is then the same
// session, at the same address, and other devices never see an address they can't name.

// threadHello registers a session of T3 thread thread, from a server of process pid in Claude
// session claude, retrying while the daemon hasn't yet seen the last connection for the ID close.
func threadHello(d *device, id, claude string, pid int, t3cli, thread string, byThread bool) (net.Conn, proto.Response) {
	d.t.Helper()
	var conn net.Conn
	var resp proto.Response
	eventually(d.t, "hello "+id, func() bool {
		c, r, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "test", ClaudeSession: claude,
			ClientPID: pid, ByThread: byThread, Route: proto.Route{T3CLI: t3cli, T3Thread: thread}})
		if err != nil {
			return false
		}
		d.t.Cleanup(func() { c.Close() })
		conn, resp = c, r
		return true
	})
	return conn, resp
}

// The manager's case (#642): a thread on instance P, switched to W, where it sends, then back to
// P. Every server registers at the thread's address (the server makes it, see package mcpserver),
// so W's message is the thread's on the other device too, after W's server has gone, and its
// address is still found.
func TestAProviderSwitchKeepsTheThreadsAddress(t *testing.T) {
	remote := newRemote(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	exe, calls := useFakeT3(t)
	sender, _ := a.session("cccccc", "claude-sender")

	cp, resp := threadHello(b, "aaaaaa", "claude-p", procP, exe, "thread-t", true)
	addr := resp.Self
	n := b.named(addr)
	eventually(t, "each device to see the other", func() bool { return a.nameOf(addr) == n && b.seesSession(sender) })

	// T3 lets the instance's session go, and the other instance's starts.
	cp.Close()
	cw, resp := threadHello(b, "aaaaaa", "claude-w", procQ, exe, "thread-t", true)
	if resp.Self != addr {
		t.Fatalf("the switch registered at %s, not the thread's %s", resp.Self, addr)
	}
	if got := b.named(addr); got != n {
		t.Fatalf("the switch renamed the thread %s -> %s", n, got)
	}
	b.send("aaaaaa", sender, "from W", "x")

	// And back. The thread is idle long enough for the session to stop.
	cw.Close()
	eventually(t, "the session to stop", func() bool { return busLines(b, addr, "stopped") == 1 })
	eventually(t, "the other device to see it go", func() bool { return !a.seesSession(addr) })
	for _, m := range a.do(proto.Request{Op: proto.OpLookup, Name: sender}).Lookup.Messages {
		if m.Subject == "from W" && m.FromName != n {
			t.Errorf("the other device shows the message from W as from %q (%s), not %s", m.FromName, m.From, n)
		}
	}
	if r := a.do(proto.Request{Op: proto.OpLookup, Name: addr}); r.Lookup == nil || r.Lookup.Name != n {
		t.Errorf("lookup of the thread's address on the other device: %q %+v", r.Error, r.Lookup)
	}
	before := len(calls())
	a.send("cccccc", n, "to the thread", "x")
	eventually(t, "the message handed to the thread", func() bool { return len(calls()) > before })

	threadHello(b, "aaaaaa", "claude-p", procS, exe, "thread-t", true)
	if got := b.named(addr); got != n {
		t.Fatalf("switching back renamed the thread %s -> %s", n, got)
	}
}

// A thread named before servers took their ID from it has its name in the registry at the address
// of the session that first had it. A server that registers by the thread is given that address, so
// the thread keeps the one every device has its name at. A server that doesn't say it takes the
// address it is given (one from before #642, still running after an update) keeps its own.
func TestAThreadNamedBeforeKeepsItsRegisteredAddress(t *testing.T) {
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	exe, _ := useFakeT3(t)

	old, resp := threadHello(b, "0a1b2c", "claude-p", procP, exe, "thread-t", false)
	addr := resp.Self
	n := b.named(addr)
	old.Close()
	eventually(t, "the session to stop", func() bool { return busLines(b, addr, "stopped") == 1 })

	_, resp = threadHello(b, "7e7e7e", "claude-w", procQ, exe, "thread-t", true)
	if resp.Self != addr || resp.Name != n {
		t.Fatalf("the thread registered at %s as %q, want %s as %s", resp.Self, resp.Name, addr, n)
	}

	_, resp = threadHello(b, "0d0d0d", "claude-x", procS, exe, "thread-u", false)
	if !strings.HasSuffix(resp.Self, "/0d0d0d") {
		t.Fatalf("a hello that doesn't register by its thread was moved to %s", resp.Self)
	}
}

// A process inside a thread that isn't the thread's own (claude run from the thread's shell, which
// inherits T3CODE_THREAD_ID) asks for the thread's address while the thread's session has it: it is
// refused, as any hello for an ID in use is, and its server goes on as a session of its own.
func TestTheThreadsAddressWhileItsSessionIsLiveIsInUse(t *testing.T) {
	remote := newRemote(t)
	b := newDevice(t, remote)
	exe, _ := useFakeT3(t)
	_, resp := threadHello(b, "aaaaaa", "claude-p", procP, exe, "thread-t", true)
	b.named(resp.Self)
	_, _, err := b.c.Hello(context.Background(), proto.Hello{ID: "bbbbbb", Agent: "test", ClaudeSession: "claude-nested",
		ClientPID: procQ, ByThread: true, Route: proto.Route{T3CLI: exe, T3Thread: "thread-t"}})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a second server for a live thread: %v", err)
	}
}
