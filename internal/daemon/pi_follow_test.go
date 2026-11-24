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
		ClientPID: pid, From: from, TakesSelf: true, Route: proto.Route{Native: file}})
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

// A /resume to a file whose own session's Pi has gone, its session in its grace, carries that
// conversation on in the session that moved: once the grace runs out, what it had unread is the
// moved session's, which holds the file's name now, as after a Claude Code /resume.
func TestAPiResumeTakesWhatTheFilesSessionHadUnread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 3*time.Second, time.Hour)
	b.session("aaaaaa", "")
	addrC, connC := b.piStart(piFileC, "", procQ)
	b.named(addrC)
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

// A /resume to a file another Pi has open leaves that Pi's session as it was, live or in its grace
// while its server restarts: its unread messages stay its own, and its server, started again,
// registers at its own address. Before, the file was recorded as the moved session's, so that
// server was refused as in use, and in its grace its unread messages went to the moved session.
func TestAPiResumeOfAFileAnotherPiHasLeavesThatPiAlone(t *testing.T) {
	for _, grace := range []bool{false, true} {
		what := "live"
		if grace {
			what = "in its grace"
		}
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			remote := newRemote(t)
			b := newTimedDevice(t, remote, time.Hour, time.Hour)
			b.session("aaaaaa", "")
			addrQ, connQ := b.piStart(piFileC, "", procQ)
			if r := b.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: addrQ, Subject: "for q"}); r.Error != "" {
				t.Fatalf("send: %+v", r)
			}
			eventually(t, "the message to be queued", func() bool { return logHas(b, "for q") })
			if grace {
				connQ.Close()
				b.dropped(addrQ)
			}

			old, conn := b.piStart(piFileA, "", procP)
			conn.Close()
			if got, _ := b.piAgain(piFileC, piFileA, procP); got != old { // P's /resume of C
				t.Fatalf("after /resume the session is at %s, not %s", got, old)
			}
			if !grace {
				connQ.Close()
			}
			if got, _ := b.piAgain(piFileC, "", procQ); got != addrQ { // Q's server starts again
				t.Fatalf("Q's server, started again, is at %s, not %s", got, addrQ)
			}
			if got := inboxOf(b, idOf(addrQ)); len(got) != 1 || got[0] != "for q" {
				t.Fatalf("Q's inbox: %v", got)
			}
		})
	}
}

// A server from before #1145, which goes by the ID it asked for whatever the reply says, is given
// that ID, though the daemon has recorded another for its file: else the two would disagree on
// which session it is.
func TestAnOlderPiServerRegistersAtTheIDItAskedFor(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	old, conn := b.piStart(piFileA, "", procP)
	conn.Close()
	_, conn = b.piAgain(piFileB, piFileA, procP) // /new: piFileB is recorded as old's
	conn.Close()
	b.dropped(old)
	c, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: proto.NativeID(piFileB), Agent: "pi",
		ClientPID: procQ, Route: proto.Route{Native: piFileB}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if idOf(resp.Self) != proto.NativeID(piFileB) {
		t.Fatalf("an older server was registered at %s, not the %s it asked for", resp.Self, proto.NativeID(piFileB))
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

// A second server of the same Pi process on the same file, while the first is still live (one
// that hangs as Pi reloads), registers at the ID it took after its own was in use, as on main.
// Before, it was handed the live session's ID again, and refused as in use, for as long as the
// first stayed.
func TestASecondPiServerOnTheSameFileKeepsTheIDItTook(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	first, _ := b.piStart(piFileA, "", procP) // still live
	c, resp, err := b.c.Hello(context.Background(), proto.Hello{ID: "f0f0f0", Agent: "pi",
		ClientPID: procP, TakesSelf: true, Route: proto.Route{Native: piFileA}})
	if err != nil {
		t.Fatalf("the second server, at the ID it took: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	if resp.Self == first || idOf(resp.Self) != "f0f0f0" {
		t.Fatalf("the second server is at %s, not at f0f0f0 (the first is at %s)", resp.Self, first)
	}
}

// dropStateKeys removes the named top-level JSON fields from d's state.json while d is stopped,
// simulating a downgrade to a daemon that drops unknown fields or a full prune past 30 days.
func (d *device) dropStateKeys(keys ...string) {
	d.t.Helper()
	path := filepath.Join(d.gitDir, brand.StateDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		d.t.Fatalf("read state.json: %v", err)
	}
	var st map[string]json.RawMessage
	if err := json.Unmarshal(b, &st); err != nil {
		d.t.Fatalf("unmarshal state.json: %v", err)
	}
	for _, k := range keys {
		delete(st, k)
	}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		d.t.Fatalf("marshal state.json: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		d.t.Fatalf("write state.json: %v", err)
	}
}

// #1187: When PiFiles is lost — after a downgrade to a daemon before #1145 or a 30-day prune — a
// Pi opened on the file a moved session left makes that moved session's ID from the file again.
// It must get a fresh ID instead of taking over the moved session's grace or kept inbox.
func TestAPiOnALeftFileDoesNotTakeOverAfterPiFilesIsLost(t *testing.T) {
	for _, grace := range []bool{false, true} {
		what := "stopped with inbox kept"
		if grace {
			what = "in its grace"
		}
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			remote := newRemote(t)
			b := newTimedDevice(t, remote, time.Hour, time.Hour)
			b.session("aaaaaa", "")
			moved, conn := b.piStart(piFileA, "", procP)
			nameA := b.named(moved)
			conn.Close()
			_, conn = b.piAgain(piFileB, piFileA, procP) // /new
			nameB := b.nameHeld(moved, nameA)
			if r := b.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: moved, Subject: "for b"}); r.Error != "" {
				t.Fatalf("send: %+v", r)
			}
			eventually(t, "the message to be queued", func() bool { return logHas(b, "for b") })

			// Simulate a downgrade to a daemon before #1145 dropping piFiles from state.json.
			conn.Close()
			b.stop()
			b.dropStateKeys("piFiles")
			b.start()
			if grace {
				// The still-running Pi server on piFileB reconnects with its learned ID, then drops
				// into its grace while piFiles is still missing.
				c, _, err := b.c.Hello(context.Background(), proto.Hello{ID: idOf(moved), Agent: "pi",
					ClientPID: procP, Reconnect: true, TakesSelf: true, Route: proto.Route{Native: piFileB}})
				if err != nil {
					t.Fatalf("reconnect moved session: %v", err)
				}
				c.Close()
				b.dropped(moved)
			}

			other, connOther, err := b.piHello(piFileA, "", procQ)
			if err != nil || other == "" || other == moved {
				t.Fatalf("Pi on the left file after piFiles loss: %s, %v (moved was %s)", other, err, moved)
			}
			b.nameIs(other, nameA)
			if got := b.do(proto.Request{Op: proto.OpInbox, Session: idOf(other)}).Messages; len(got) != 0 {
				t.Fatalf("Pi on the left file stole %v", viewSubjects(got))
			}

			// The fresh ID is recorded in PiFiles so reloading or reopening piFileA keeps it.
			connOther.Close()
			b.dropped(other)
			if again, _ := b.piAgain(piFileA, "", procQ+1); again != other {
				t.Fatalf("reopening piFileA gave %s, not %s", again, other)
			}

			// Opening piFileB still gets conversation B's name and unread message.
			if grace {
				c, _, err := b.c.Hello(context.Background(), proto.Hello{ID: idOf(moved), Agent: "pi",
					ClientPID: procP, Reconnect: true, TakesSelf: true, Route: proto.Route{Native: piFileB}})
				if err != nil {
					t.Fatalf("reconnect piFileB: %v", err)
				}
				t.Cleanup(func() { c.Close() })
				if got := inboxOf(b, idOf(moved)); len(got) != 1 || got[0] != "for b" {
					t.Fatalf("piFileB's inbox after grace takeover: %v", got)
				}
			} else {
				addrB, _ := b.piStart(piFileB, "", procQ+2)
				b.nameIs(addrB, nameB)
				if got := inboxOf(b, idOf(addrB)); len(got) != 1 || got[0] != "for b" {
					t.Fatalf("piFileB's adopted inbox: %v", got)
				}
			}
		})
	}
}

// #1187: Even long after a moved session has stopped and PiFiles has been pruned, the registry
// still lists the moved conversation's name at the old address (Used). Opening Pi on the left file
// must take a fresh ID so messages to the moved conversation's name do not reach the left file
// with a "called something else now" note.
func TestAPiOnALeftFileDoesNotReceiveMovedNameAfterFullPrune(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	a.session("aaaaaa", "claude-sender")

	moved, conn := b.piStart(piFileA, "", procP)
	nameA := b.named(moved)
	a.nameIs(moved, nameA)
	conn.Close()
	_, conn = b.piAgain(piFileB, piFileA, procP) // /new
	nameB := b.nameHeld(moved, nameA)
	a.nameIs(moved, nameB)

	// Simulate 30 quiet days: moved is no longer stopped/kept and PiFiles has been pruned, while
	// NameKeys, Used, SessionNames and NameHolders remain.
	conn.Close()
	b.stop()
	b.dropStateKeys("piFiles", "stopped", "downed", "inbox", "registered")
	b.start()

	other, _, err := b.piHello(piFileA, "", procQ)
	if err != nil || other == "" || other == moved {
		t.Fatalf("Pi on the left file after full prune: %s, %v (moved was %s)", other, err, moved)
	}
	b.nameIs(other, nameA)
	a.nameIs(other, nameA)

	r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameB, Subject: "for moved b"})
	if r.Error != "" {
		t.Fatalf("send to %s: %+v", nameB, r)
	}
	if strings.Contains(r.Note, nameA) {
		t.Fatalf("send to %s misdirected to %s: %+v", nameB, nameA, r)
	}
	// Send a follow-up to nameA and wait for it; once it lands, the earlier send to nameB has been
	// processed too, and must not be in other's inbox.
	if r := a.do(proto.Request{Op: proto.OpSend, Session: "aaaaaa", To: nameA, Subject: "for a"}); r.Error != "" {
		t.Fatalf("send to %s: %+v", nameA, r)
	}
	if got := inboxOf(b, idOf(other)); len(got) != 1 || got[0] != "for a" {
		t.Fatalf("piFileA's inbox: %v, want only [for a]", got)
	}
}

// #1187: A Pi session that moved off its file and back (/new, then /resume) is still that file's
// conversation, though its name from the other file is at its address in Used too. After PiFiles
// is lost, Pi on the file keeps the session's ID rather than taking a fresh one (Opus's review of
// 08e9e082).
func TestAPiSessionBackOnItsFileKeepsItsIDAfterPiFilesIsLost(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, time.Hour, time.Hour)
	moved, conn := b.piStart(piFileA, "", procP)
	nameA := b.named(moved)
	conn.Close()
	_, conn = b.piAgain(piFileB, piFileA, procP) // /new
	b.nameHeld(moved, nameA)
	conn.Close()
	back, conn := b.piAgain(piFileA, piFileB, procP) // /resume of the first file
	if back != moved {
		t.Fatalf("/resume back gave %s, want %s", back, moved)
	}
	b.nameIs(back, nameA)

	conn.Close()
	b.stop()
	b.dropStateKeys("piFiles")
	b.start()
	again, _ := b.piAgain(piFileA, "", procQ)
	if again != moved {
		t.Fatalf("after piFiles loss, Pi on the file it came back to got %s, want %s", again, moved)
	}
	b.nameIs(again, nameA)
}
