package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of #30: a stopped session's inbox is kept, and it is kept through a daemon that was gone
// longer than the catch-up limit (10 minutes), which reads the backlog in a skip.

// ageLastSync rewrites the stopped daemon's state.json so that it last synced ago ago, which is
// how long the next daemon will take it to have been down.
func ageLastSync(t *testing.T, d *device, ago time.Duration) {
	t.Helper()
	path := filepath.Join(d.gitDir, brand.StateDir, "state.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]json.RawMessage
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	st["lastSync"], _ = json.Marshal(time.Now().Add(-ago).UTC())
	if b, err = json.Marshal(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// keptFor is the subjects in session id's inbox in state.json, sorted. It reads the file rather
// than asking the daemon, as asking by registering the session would change what it is given. ok is
// false while the daemon is replacing the file, which on Windows fails a read made just then: an
// inbox read then isn't an empty one (#427).
func keptFor(t *testing.T, d *device, id string) (subjects []string, ok bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.gitDir, brand.StateDir, "state.json"))
	if err != nil {
		return nil, false
	}
	var st struct {
		Inbox map[string][]struct {
			Event struct {
				Subject string `json:"subject"`
			} `json:"event"`
		} `json:"inbox"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("state.json: %v", err)
	}
	var out []string
	for _, m := range st.Inbox[id] {
		out = append(out, m.Event.Subject)
	}
	sort.Strings(out)
	return out, true
}

// forgedMessage is a message from a session of the device whose log it is pushed onto.
func forgedMessage(from, to, subject string, at time.Time) bus.Event {
	return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: from + "/x", At: at.UTC(), To: to, Subject: subject}
}

// A session stops, its daemon is gone for longer than the catch-up limit, and a message is sent to
// the session meanwhile. The daemon that starts again keeps it in the session's inbox, and the
// session has it when it comes back: the common case of a laptop shut overnight. What was sent
// before the session stopped, to a session that isn't kept, or later than the inbox is kept, is
// still skipped.
func TestKeptInboxCatchesUpAfterALongOutage(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	bdev := b.self()
	bob, conn := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 })
	stoppedAt := time.Now()

	b.stop()
	ageLastSync(t, b, time.Hour)
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "direct, while the daemon was down"})
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "broadcast, while the daemon was down"})
	// A log b has never read, so its whole history is the backlog. The last of it is the one that
	// is kept; the others come before it in the log, so when it has arrived they have been judged.
	const old = "old-0001"
	for _, e := range []bus.Event{
		forgedMessage(old, bdev+"/bbbbbb", "before bob stopped", stoppedAt.Add(-10*time.Minute)),
		forgedMessage(old, bdev+"/cccccc", "for a session that isn't kept", stoppedAt.Add(time.Minute)),
		forgedMessage(old, bdev, "for the device, before bob stopped", stoppedAt.Add(-10*time.Minute)),
		forgedMessage(old, bdev+"/bbbbbb", "after the inbox would have run out", time.Now().Add(2*time.Hour)),
		forgedMessage(old, bdev+"/bbbbbb", "forged, while the daemon was down", stoppedAt.Add(time.Minute)),
	} {
		pushEvent(t, remote, "refs/murmur/mesh/"+old, e)
	}
	b.start()

	want := []string{"broadcast, while the daemon was down", "direct, while the daemon was down", "forged, while the daemon was down"}
	// The inbox that ended the wait is the one judged. A second read of the file could fall while
	// the daemon replaces it, and came back empty on Windows CI (#427).
	var kept []string
	eventually(t, "bob's inbox is kept", func() bool {
		kept, _ = keptFor(t, b, "bbbbbb")
		return len(kept) >= len(want)
	})
	if strings.Join(kept, "|") != strings.Join(want, "|") {
		t.Fatalf("bob's kept inbox: %q, want %q", kept, want)
	}
	var other []string
	eventually(t, "state.json to be read", func() (ok bool) {
		other, ok = keptFor(t, b, "cccccc")
		return ok
	})
	if len(other) != 0 {
		t.Errorf("a session that wasn't kept has %q", other)
	}

	// What was kept was read, so the page lists it. The history that was skipped is read back for the
	// page too (#138), though no inbox was given it.
	pg := b.page()
	for _, s := range append([]string{"before bob stopped", "for a session that isn't kept"}, want...) {
		eventually(t, "the page to list "+s, func() bool {
			_, ok := pg.messageWith(s)
			return ok
		})
	}

	if _, err := b.restartedMCP("bbbbbb", ""); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
		got = append(got, m.Subject)
	}
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("bob got %q when he came back, want %q", got, want)
	}
	// Another session, new here, still gets none of the history.
	b.session("cccccc", "")
	if ms := b.do(proto.Request{Op: proto.OpInbox, Session: "cccccc"}).Messages; len(ms) != 0 {
		t.Errorf("a new session got %+v", ms)
	}
}

// A first start has no stopped sessions and queues nothing from the history on the bus, for a
// session registering at once or later.
func TestFirstStartStillSkipsAllHistory(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "history"})
	pushEvent(t, remote, "refs/murmur/mesh/old-0001", forgedMessage("old-0001", "*", "more history", time.Now()))
	b := newDevice(t, remote)
	b.session("bbbbbb", "")
	holds(t, "nothing from history", time.Second, func() bool {
		return len(b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages) == 0 &&
			len(b.do(proto.Request{Op: proto.OpInbox, Session: proto.CLISession}).Messages) == 0
	})
}

// A message kept through a long outage for a stopped session of T3 reaches its thread as a turn,
// as one received live does, though the session has not come back and its MCP server is gone.
func TestKeptMessageWakesAStoppedT3Thread(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	a, b := newDevice(t, remote), newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	bob, conn := b.sessionRoute("bbbbbb", "", proto.Route{T3CLI: exe, T3Thread: "thread-bob"})
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	conn.Close()
	eventually(t, "bob stopped", func() bool { return busLines(b, bob, "stopped") == 1 })

	b.stop()
	ageLastSync(t, b, time.Hour)
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "sent overnight", Body: "wake up"})
	b.start()

	eventually(t, "the handoff into the thread", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-bob -" || !strings.Contains(c.Stdin, "wake up") {
		t.Errorf("t3 was run as %q with %q", c.Args, c.Stdin)
	}
}
