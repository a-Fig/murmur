package daemon_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// #1145: Pi moves a session to another file on /new, /resume and /fork, in the same process, and
// Murmur's extension starts the MCP server again with the new file and the one it left (From).
// The session follows, as a Claude Code one does on /clear: it keeps its address and inbox, its
// name follows the new file, and a message to the old name reaches it.

// piHello registers a Pi server of process pid for file, which left from ("" for none). It returns
// the reply's address, and the error, unchecked.
func (d *device) piHello(id, file, from string, pid int) (string, net.Conn, error) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "pi", ClientPID: pid, From: from,
		Route: proto.Route{Native: file}})
	if err != nil {
		return "", nil, err
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn, nil
}

// piMoved is piHello for the server Pi starts after a move: it tries again while the daemon hasn't
// yet seen the old server's connection close, as the server does.
func (d *device) piMoved(id, file, from string, pid int) string {
	d.t.Helper()
	var addr string
	eventually(d.t, "the moved session to register", func() bool {
		a, _, err := d.piHello(id, file, from, pid)
		addr = a
		return err == nil
	})
	return addr
}

// whoNames is every name a's who shows, with the address it shows it at.
func whoNames(a *device) map[string]string {
	got := map[string]string{}
	for _, dv := range a.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			got[s.Name] = s.Addr
		}
	}
	return got
}

// After /new or /fork the session is at its old address under a new name, a send to the old name
// from another device reaches it and says what it is called now, and who shows the old name
// nowhere. Before, the new file was a new session, and the old one, its server gone, kept what was
// sent to its name until its grace ran out.
func TestAPiSessionFollowsNewAndFork(t *testing.T) {
	for _, c := range []struct{ move, file string }{
		{"/new", "/s/2026-11-23_new.jsonl"},
		{"/fork", "/s/2026-11-23_fork.jsonl"},
	} {
		t.Run(c.move, func(t *testing.T) {
			t.Parallel()
			const fileA = "/s/2026-11-23_a.jsonl"
			remote := newRemote(t)
			a, b := newDevice(t, remote), newDevice(t, remote)
			a.session("aaaaaa", "claude-sender")
			old, conn, err := b.piHello("0a0a0a", fileA, "", procP)
			if err != nil {
				t.Fatal(err)
			}
			nameA := b.named(old)
			a.nameIs(old, nameA)

			// Pi closes the old server as the new one starts: while the daemon still has it live,
			// the new one waits.
			if _, _, err := b.piHello("0b0b0b", c.file, fileA, procP); err == nil || !strings.Contains(err.Error(), "in use") {
				t.Fatalf("a hello while the old server is live: %v", err)
			}
			conn.Close()
			if got := b.piMoved("0b0b0b", c.file, fileA, procP); got != old {
				t.Fatalf("after %s the session is at %s, not %s", c.move, got, old)
			}
			nameB := b.nameHeld(old, nameA)
			a.nameIs(old, nameB)
			if seen := whoNames(a); seen[nameA] != "" {
				t.Errorf("who shows %s at %s after %s", nameA, seen[nameA], c.move)
			}

			r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameA, Subject: "to the old name"})
			if r.Error != "" || !strings.Contains(r.Note, nameB) {
				t.Fatalf("send to the old name: %+v", r)
			}
			if got := inboxOf(b, "0a0a0a"); len(got) != 1 || got[0] != "to the old name" {
				t.Fatalf("the session's inbox: %v", got)
			}
		})
	}
}

// After /resume to a file that was a conversation of its own, the session takes that one's name,
// as a Claude Code session does on a /resume back, and still keeps its address: a message to
// either name reaches it.
func TestAPiSessionFollowsAResume(t *testing.T) {
	const earlier = "0c0c0c"
	t.Cleanup(daemon.SetGraceBegins(func(id string) <-chan struct{} {
		if id != earlier {
			return nil // the session that moves keeps its grace until it is taken over
		}
		over := make(chan struct{})
		close(over)
		return over
	}))
	const fileA, fileC = "/s/2026-11-23_a.jsonl", "/s/2026-11-20_c.jsonl"
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, time.Hour, time.Hour)
	a.session("aaaaaa", "claude-sender")

	// File C was a conversation of its own, in an earlier Pi, and has stopped.
	addrC, connC, err := b.piHello(earlier, fileC, "", procQ)
	if err != nil {
		t.Fatal(err)
	}
	nameC := b.named(addrC)
	connC.Close()
	eventually(t, "the earlier session to stop", func() bool { return busLines(b, addrC, "stopped") == 1 })

	old, conn, err := b.piHello("0a0a0a", fileA, "", procP)
	if err != nil {
		t.Fatal(err)
	}
	nameA := b.named(old)
	a.nameIs(old, nameA)
	conn.Close()

	if got := b.piMoved(earlier, fileC, fileA, procP); got != old {
		t.Fatalf("after /resume the session is at %s, not %s", got, old)
	}
	b.nameIs(old, nameC)
	a.nameIs(old, nameC)
	if seen := whoNames(a); seen[nameA] != "" {
		t.Errorf("who shows %s at %s after /resume", nameA, seen[nameA])
	}

	for _, to := range []string{nameA, nameC} {
		if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: to, Subject: "to " + to}); r.Error != "" {
			t.Fatalf("send to %s: %+v", to, r)
		}
	}
	if got := inboxWant(b, "0a0a0a", 2); len(got) != 2 {
		t.Fatalf("the session's inbox: %v", got)
	}
}

// Another Pi process on the file a session left is a conversation of its own: a hello naming that
// file as its From, from another process, isn't given the session's address.
func TestAPiSessionOfAnotherProcessDoesNotTakeTheFileItLeft(t *testing.T) {
	t.Parallel()
	const fileA = "/s/2026-11-23_a.jsonl"
	remote := newRemote(t)
	b := newDevice(t, remote)
	old, conn, err := b.piHello("0a0a0a", fileA, "", procP)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	eventually(t, "the session in its grace", func() bool {
		d := b.do(proto.Request{Op: proto.OpLookup, Name: old}).Lookup
		return d != nil && d.Dropped
	})
	if got, _, err := b.piHello("0d0d0d", "/s/2026-11-23_d.jsonl", fileA, procQ); err != nil || got == old {
		t.Fatalf("another process's hello: %s, %v", got, err)
	}
}
