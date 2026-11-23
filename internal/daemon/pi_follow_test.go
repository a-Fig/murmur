package daemon_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// #1145: Pi moves a session to another file on /new, /resume and /fork, in the same process, and
// Murmur's extension starts the MCP server again with the new file and the one it left (From).
// The session follows, as a Claude Code one does on /clear: it keeps its address and inbox, its
// name follows the new file, and a message to the old name reaches it.

// piHello registers a Pi server of process pid for file, which left from ("" for none), at the ID
// the server makes from the file. It returns the reply's address, and the error, unchecked.
func (d *device) piHello(file, from string, pid int) (string, net.Conn, error) {
	d.t.Helper()
	conn, resp, err := d.c.Hello(context.Background(), proto.Hello{ID: proto.NativeID(file), Agent: "pi",
		ClientPID: pid, From: from, Route: proto.Route{Native: file}})
	if err != nil {
		return "", nil, err
	}
	d.t.Cleanup(func() { conn.Close() })
	return resp.Self, conn, nil
}

// piStart is piHello, failing the test on an error.
func (d *device) piStart(file, from string, pid int) (string, net.Conn) {
	d.t.Helper()
	addr, conn, err := d.piHello(file, from, pid)
	if err != nil {
		d.t.Fatalf("hello for %s: %v", file, err)
	}
	return addr, conn
}

// piAgain is piHello for a server Pi starts again: it tries again while the daemon hasn't yet seen
// the old server's connection close, as the server does.
func (d *device) piAgain(file, from string, pid int) (string, net.Conn) {
	d.t.Helper()
	var addr string
	var conn net.Conn
	eventually(d.t, "the server to register again", func() bool {
		a, c, err := d.piHello(file, from, pid)
		addr, conn = a, c
		return err == nil
	})
	return addr, conn
}

// idOf is the session ID in addr.
func idOf(addr string) string {
	_, id := bus.SplitAddr(addr)
	return id
}

// dropped waits for d to have the session at addr in its grace.
func (d *device) dropped(addr string) {
	d.t.Helper()
	eventually(d.t, "the session in its grace", func() bool {
		l := d.do(proto.Request{Op: proto.OpLookup, Name: addr}).Lookup
		return l != nil && l.Dropped
	})
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

const (
	piFileA = "/s/2026-11-23_a.jsonl"
	piFileB = "/s/2026-11-23_b.jsonl"
	piFileC = "/s/2026-11-20_c.jsonl"
)

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
			remote := newRemote(t)
			a, b := newDevice(t, remote), newDevice(t, remote)
			a.session("aaaaaa", "claude-sender")
			old, conn := b.piStart(piFileA, "", procP)
			nameA := b.named(old)
			a.nameIs(old, nameA)

			// Pi closes the old server as the new one starts: while the daemon still has it live,
			// the new one waits.
			if _, _, err := b.piHello(c.file, piFileA, procP); err == nil || !strings.Contains(err.Error(), "in use") {
				t.Fatalf("a hello while the old server is live: %v", err)
			}
			conn.Close()
			if got, _ := b.piAgain(c.file, piFileA, procP); got != old {
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
			if got := inboxOf(b, idOf(old)); len(got) != 1 || got[0] != "to the old name" {
				t.Fatalf("the session's inbox: %v", got)
			}
		})
	}
}

// After a move, a /reload starts the server again on the new file with no From, and so does Pi
// started again on it: either is the session that moved there, at its address, with what it had
// unread. Before, the file's own ID was a new session's, and the unread message stayed behind.
func TestAPiSessionKeepsItsAddressAcrossAReloadAfterAMove(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	b.session("aaaaaa", "")
	old, conn := b.piStart(piFileA, "", procP)
	conn.Close()
	_, conn = b.piAgain(piFileB, piFileA, procP) // /new
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: old, Subject: "before the reload"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "before the reload") })

	conn.Close()
	if got, conn := b.piAgain(piFileB, "", procP); got != old { // /reload
		t.Fatalf("after /reload the session is at %s, not %s", got, old)
	} else {
		conn.Close()
	}
	if got, _ := b.piAgain(piFileB, "", procQ); got != old { // Pi started again on the file
		t.Fatalf("Pi started again on the file is at %s, not %s", got, old)
	}
	if got := inboxOf(b, idOf(old)); len(got) != 1 || got[0] != "before the reload" {
		t.Fatalf("the session's inbox: %v", got)
	}
}

// Another Pi on the file a session left is a conversation of its own, whether the session that
// left is live or in its grace: it registers at once, at an address of its own, and is never handed
// what the session that left had. Before, the file made the ID the session that left still had:
// live, the hello was refused for about 7 s, and in its grace it was taken over, inbox and all.
func TestAnotherPiOnTheFileASessionLeftIsASessionOfItsOwn(t *testing.T) {
	for _, grace := range []bool{false, true} {
		what := "live"
		if grace {
			what = "in its grace"
		}
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			remote := newRemote(t)
			b := newDevice(t, remote)
			b.session("aaaaaa", "")
			moved, conn := b.piStart(piFileA, "", procP)
			conn.Close()
			_, conn = b.piAgain(piFileB, piFileA, procP) // /new
			if r := b.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: moved, Subject: "for b"}); r.Error != "" {
				t.Fatalf("send: %+v", r)
			}
			eventually(t, "the message to be queued", func() bool { return logHas(b, "for b") })
			if grace {
				conn.Close()
				b.dropped(moved)
			}

			other, _, err := b.piHello(piFileA, "", procQ)
			if err != nil || other == "" || other == moved {
				t.Fatalf("another Pi on the file left: %s, %v", other, err)
			}
			if got := b.do(proto.Request{Op: proto.OpInbox, Session: idOf(other)}).Messages; len(got) != 0 {
				t.Fatalf("the other Pi was handed %v", viewSubjects(got))
			}
			if !grace {
				if got := inboxOf(b, idOf(moved)); len(got) != 1 || got[0] != "for b" {
					t.Fatalf("the moved session's inbox: %v", got)
				}
			}
		})
	}
}

// After /resume to a file that was a conversation of its own, the session takes that one's name,
// as a Claude Code session does on a /resume back, and still keeps its address: a message to
// either name reaches it.
func TestAPiSessionFollowsAResume(t *testing.T) {
	earlier := proto.NativeID(piFileC)
	t.Cleanup(daemon.SetGraceBegins(func(id string) <-chan struct{} {
		if id != earlier {
			return nil // the session that moves keeps its grace until it is taken over
		}
		over := make(chan struct{})
		close(over)
		return over
	}))
	remote := newRemote(t)
	a := newDevice(t, remote)
	b := newTimedDevice(t, remote, time.Hour, time.Hour)
	a.session("aaaaaa", "claude-sender")

	// File C was a conversation of its own, in an earlier Pi, and has stopped.
	addrC, connC := b.piStart(piFileC, "", procQ)
	nameC := b.named(addrC)
	connC.Close()
	eventually(t, "the earlier session to stop", func() bool { return busLines(b, addrC, "stopped") == 1 })

	old, conn := b.piStart(piFileA, "", procP)
	nameA := b.named(old)
	a.nameIs(old, nameA)
	conn.Close()

	if got, _ := b.piAgain(piFileC, piFileA, procP); got != old {
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
	if got := inboxWant(b, idOf(old), 2); len(got) != 2 {
		t.Fatalf("the session's inbox: %v", got)
	}
}

// A /resume to a file whose own session is still in its grace, its Pi gone, carries that
// conversation on in the session that moved: what it had unread is the moved session's at once.
// Before, it stayed in the grace inbox until the grace ran out.
func TestAPiResumeTakesWhatTheFilesSessionHadUnread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, time.Hour, time.Hour)
	b.session("aaaaaa", "")
	addrC, connC := b.piStart(piFileC, "", procQ)
	if r := b.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: addrC, Subject: "for c"}); r.Error != "" {
		t.Fatalf("send: %+v", r)
	}
	eventually(t, "the message to be queued", func() bool { return logHas(b, "for c") })
	connC.Close()
	b.dropped(addrC)

	old, conn := b.piStart(piFileA, "", procP)
	conn.Close()
	if got, _ := b.piAgain(piFileC, piFileA, procP); got != old {
		t.Fatalf("after /resume the session is at %s, not %s", got, old)
	}
	if got := inboxOf(b, idOf(old)); len(got) != 1 || got[0] != "for c" {
		t.Fatalf("the session's inbox: %v", got)
	}
}

// A hello that names a file another Pi process's session left is no move of that session: the
// session in its grace stays where it is, and the hello is a session of its own.
func TestAPiOfAnotherProcessDoesNotFollowASessionItDidNotHave(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	old, conn := b.piStart(piFileA, "", procP)
	conn.Close()
	b.dropped(old)
	if got, _, err := b.piHello(piFileB, piFileA, procQ); err != nil || got == old {
		t.Fatalf("another process's hello: %s, %v", got, err)
	}
}
