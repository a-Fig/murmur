package daemon_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of what survives, and what must not, when a daemon stops and starts again.

func (d *device) restart() {
	d.stop()
	d.start()
}

func (d *device) seesDevices(n int) bool {
	return len(d.do(proto.Request{Op: proto.OpWho}).Devices) == n
}

// seesSession says whether d's who lists the session at addr on a device that is fresh, which is
// what a send needs to count it as known. A device stays listed when it goes stale, and the tests'
// load can hold its heartbeats back past their stale limit (testStale): a send then says its
// sessions aren't in any device's presence (#387).
func (d *device) seesSession(addr string) bool {
	_, listed, fresh := d.whoShows(addr)
	return listed && fresh
}

// listsSession says whether d's who lists the session at addr at all, its device stale or not. It
// is for waiting until a session is gone, which its device going stale doesn't stand in for, and
// for holds, which a heartbeat held back shouldn't break.
func (d *device) listsSession(addr string) bool {
	_, listed, _ := d.whoShows(addr)
	return listed
}

// whoShows is the session at addr in d's who: its name, whether it is listed, and whether its
// device is fresh. Fresh is more than not stale: d saw the device's presence change within half
// its stale limit, so a send right after a wait that ends on it has that half in hand, where one
// that ended on a device about to go stale would have nothing.
func (d *device) whoShows(addr string) (name string, listed, fresh bool) {
	for _, dv := range d.do(proto.Request{Op: proto.OpWho}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == addr {
				return s.Name, true, !dv.Stale && time.Since(dv.LastSeen) < d.stale/2
			}
		}
	}
	return "", false, false
}

func (d *device) helloAgain(id string, reconnect bool) {
	d.t.Helper()
	conn, _, err := d.c.Hello(context.Background(), proto.Hello{ID: id, Agent: "test", Reconnect: reconnect})
	if err != nil {
		d.t.Fatalf("hello %s: %v", id, err)
	}
	d.t.Cleanup(func() { conn.Close() })
}

// holds stays true for a while: for checking that something doesn't come back.
func holds(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if !cond() {
			t.Fatalf("%s stopped holding", what)
		}
	}
}

func TestRestartDoesNotReplayToNewSessions(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	eventually(t, "presence", func() bool { return a.seesDevices(2) && b.seesDevices(2) })
	// Both hold the team key before b stops. A device still waiting for its grant reads what it
	// missed late, once granted, and that can reach a session started within the minute after it was
	// sent (docs/design/encryption.md): another case than a restart's.
	eventually(t, "both to hold the team key", func() bool {
		ids := teamIDs(t, remote)
		return len(ids) == 1 && ringOf(t, a)[ids[0]] != nil && ringOf(t, b)[ids[0]] != nil
	})

	b.stop()
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "sent while b was down"})
	b.start()
	b.session("cccccc", "") // a new session, registered as soon as the daemon is up
	eventually(t, "b back", func() bool { return b.seesDevices(2) })
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "sent after"})

	for _, sess := range []string{"cccccc", proto.CLISession} {
		var got []string
		eventually(t, "the new message for "+sess, func() bool {
			for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages {
				got = append(got, m.Subject)
			}
			return len(got) > 0
		})
		if len(got) != 1 || got[0] != "sent after" {
			t.Fatalf("%s got %q", sess, got)
		}
	}
}

func TestInboxSurvivesCleanRestart(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "unread across a restart"})
	eventually(t, "the message to be queued", func() bool {
		return len(b.do(proto.Request{Op: proto.OpNotices, Session: "bbbbbb"}).Messages) == 1
	})

	b.restart()
	b.helloAgain("bbbbbb", true)
	got := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
	if len(got) != 1 || got[0].Subject != "unread across a restart" {
		t.Fatalf("inbox after restart: %+v", got)
	}
}

// A daemon that stops deletes its presence, so others drop it at their next sync rather than
// when it goes stale.
func TestGoodbyeDropsDeviceAtOnce(t *testing.T) {
	// Not parallel: it times how soon b is dropped, which other tests' load would stretch.
	remote := newRemote(t)
	// A real stale limit: the goodbye must drop the device long before it would go stale.
	a, b := newDevice(t, remote, time.Minute), newDevice(t, remote, time.Minute)
	eventually(t, "a sees b", func() bool { return a.seesDevices(2) })
	b.stop()
	start := time.Now()
	eventually(t, "b gone from a", func() bool { return a.seesDevices(1) })
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s", took)
	}
}

func (d *device) offline(remote string, off bool) {
	url := remote
	if off {
		url = filepath.Join(filepath.Dir(remote), "nowhere.git")
	}
	git(d.t, filepath.Dir(d.gitDir), "config", "remote.origin.url", url)
}

// A reader that can't reach the remote (asleep, offline) has no grounds to call anyone else
// gone: it judges staleness only up to its own last successful sync.
func TestOfflineReaderCallsNobodyStale(t *testing.T) {
	// Not parallel: b must stay fresh on a for seconds, and other tests' load can hold one of its
	// heartbeats back for the stale limit (testStale) that makes it stale.
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bdev := b.self()
	eventually(t, "b on a", func() bool { return a.fresh(bdev) })
	a.offline(remote, true)
	holds(t, "b fresh on a while a is offline", 2*a.stale, func() bool { return a.fresh(bdev) })
	a.offline(remote, false)
	holds(t, "b fresh on a once a is back", time.Second, func() bool { return a.fresh(bdev) })
}

// A daemon that starts after another device went quiet, longer ago than the stale limit, shows it
// stale from the first time it lists it, not alive for one more stale period (#66).
func TestNewDaemonShowsADeadDeviceStaleAtOnce(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	bdev := b.self()
	eventually(t, "b's presence on the remote", func() bool {
		return strings.Contains(git(t, remote, "for-each-ref", "refs/murmur/presence/"+bdev), bdev)
	})
	b.offline(remote, true) // no more heartbeats, and no goodbye
	time.Sleep(2 * b.stale)

	c := newDeviceWith(t, remote, func(d *device) { d.noKey = true }) // b is gone, so nobody grants it
	var stale bool
	eventually(t, "c lists b", func() bool {
		for _, dv := range c.do(proto.Request{Op: proto.OpWho}).Devices {
			if dv.ID == bdev {
				stale = dv.Stale
				return true
			}
		}
		return false
	})
	if !stale {
		t.Errorf("c's first listing of b, quiet for %s, shows it alive", 2*b.stale)
	}
}

// After a restart, what arrived while the daemon was down reaches sessions that were running
// before and reconnect, and nobody else.
func TestRestartCatchesUpReconnectingSessions(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	// b holds the team key before it goes, as any device that has been up a few seconds does, so
	// what it reads on its return isn't held back for a grant (docs/design/encryption.md).
	eventually(t, "b holds the team key", func() bool {
		ids := teamIDs(t, remote)
		return len(ids) == 1 && ringOf(t, b)[ids[0]] != nil
	})

	b.stop()
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "direct, during the restart"})
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "broadcast, during the restart"})
	b.start()
	b.helloAgain("bbbbbb", true)
	b.session("cccccc", "")

	var got []string
	eventually(t, "bob catches up", func() bool {
		for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
			got = append(got, m.Subject)
		}
		return len(got) >= 2
	})
	if len(got) != 2 {
		t.Fatalf("bob got %q", got)
	}
	for _, sess := range []string{"cccccc", proto.CLISession} {
		if ms := b.do(proto.Request{Op: proto.OpInbox, Session: sess}).Messages; len(ms) != 0 {
			t.Fatalf("%s got %+v", sess, ms)
		}
	}
}

func TestStaleLockIsCleared(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	work := filepath.Dir(a.gitDir)
	time.Sleep(time.Second) // past the first sync, which records tips without reading them

	// Another device's log appears on the remote, while a's copy of that ref has a lock left
	// behind by a killed git.
	ref := "refs/murmur/mesh/zzz-0000"
	lock := filepath.Join(a.gitDir, filepath.FromSlash(ref)+".lock")
	os.MkdirAll(filepath.Dir(lock), 0o755)
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(lock, old, old)

	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "zzz-0000/s", At: time.Now().UTC(),
		To: "*", Subject: "through the lock"}
	msg, _ := bus.Encode(e, e.Line())
	empty := git(t, work, "hash-object", "-t", "tree", "--stdin")
	sha := gitIn(t, work, msg, "commit-tree", empty)
	git(t, work, "push", "-q", remote, sha+":"+ref)

	eventually(t, "the event through the cleared lock", func() bool {
		for _, ev := range a.do(proto.Request{Op: proto.OpLog}).Events {
			if strings.Contains(ev.Line, "through the lock") {
				return true
			}
		}
		return false
	})
}

// A message that lands after a restarted daemon's first sync, but before a session that was
// running reconnects, still reaches it.
func TestMessageBeforeReconnectIsCaughtUp(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	b.restart() // returns once the new daemon is listening
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "sent while bob reconnects"})
	eventually(t, "b reads it", func() bool {
		for _, ev := range b.do(proto.Request{Op: proto.OpLog}).Events {
			if strings.Contains(ev.Line, "sent while bob reconnects") {
				return true
			}
		}
		return false
	})
	b.helloAgain("bbbbbb", true)
	if got := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages; len(got) != 1 {
		t.Fatalf("bob got %d messages, want the one sent while he reconnected", len(got))
	}
}

// When a daemon's first sync fails, sessions register before anything is settled. What was then
// sent after they started still reaches them; the backlog doesn't.
func TestFirstSyncFailedStillQueues(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a := newDevice(t, remote)
	pushEvent(t, remote, "refs/murmur/mesh/old-0001", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: "old-0001/x", At: time.Now().Add(-10 * time.Minute).UTC(), To: "*", Subject: "backlog"})
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", remote, dir)
	// Pushes go out, so presence is seen, but listing and fetching fail.
	git(t, dir, "config", "remote.origin.pushurl", remote)
	git(t, dir, "config", "remote.origin.url", filepath.Join(filepath.Dir(remote), "nowhere.git"))
	b := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond}
	b.start()
	bob, _ := b.session("bbbbbb", "")
	// a sees b's device, not bob: b, never having read the team ref, encrypts its presence for
	// itself alone (docs/design/encryption.md).
	eventually(t, "a sees b", func() bool { return a.seesDevices(2) })

	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "broadcast"})
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "direct"})
	git(t, dir, "config", "remote.origin.url", remote)

	var got []string
	eventually(t, "bob gets both", func() bool {
		for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
			got = append(got, m.Subject)
		}
		return len(got) >= 2
	})
	sort.Strings(got)
	if strings.Join(got, ",") != "broadcast,direct" {
		t.Fatalf("bob got %q, want the broadcast and the direct message", got)
	}
}

// After a restart, a person's change to the tags of a session whose name was pending and that kept
// nothing here, an agent's add of it to a group, and the group's message, all read before the
// session reconnects, reach it once it does (#524): the daemon knew nothing of it when it read them.
func TestTagsReadBeforeANamelessSessionReconnectsReachIt(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newDevice(t, remote)
	const far = "refs/murmur/mesh/far-0001"
	read := func(what string) func() bool {
		return func() bool {
			for _, ev := range b.do(proto.Request{Op: proto.OpLog}).Events {
				if strings.Contains(ev.Line, what) {
					return true
				}
			}
			return false
		}
	}
	// b has read far-0001's log before it stops, so what is written there meanwhile is news to it.
	pushEvent(t, remote, far, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "far-0001/xxxxxx",
		At: time.Now().UTC(), To: "*", Subject: "before"})
	eventually(t, "b reads far-0001's log", read("before"))
	b.offline(remote, true) // so bob's name can't land
	bob, _ := b.session("bbbbbb", "")
	b.stop()

	now := time.Now().UTC()
	pushEvent(t, remote, far, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Tags, From: "far-0001/cli",
		At: now, To: bob, Add: []string{"release"}})
	pushEvent(t, remote, far, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.GroupAdd, From: "far-0001/xxxxxx",
		At: now, To: bob, Add: []string{"group:ui"}})
	pushEvent(t, remote, far, bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "far-0001/xxxxxx",
		At: now, To: "group:ui", Subject: "to the group"})
	b.offline(remote, false)
	started := time.Now()
	b.start()
	eventually(t, "b reads them", read("to the group"))
	b.helloAgain("bbbbbb", true)
	// What is read is held only for CatchUpWindow from the start, so a runner too slow to read the
	// log and say hello within it fails here without a bug: the line says how long they took.
	took := time.Since(started).Round(time.Millisecond)

	if !b.inGroup("bbbbbb", bob, "group:ui") || !b.inGroup("bbbbbb", bob, "release") {
		t.Errorf("back, bob has %v; want release and group:ui (the first read after the restart, and the hello, took %s; the hold lasts %s)",
			b.do(proto.Request{Op: proto.OpTags, Session: "bbbbbb"}).Tags, took, daemon.CatchUpWindow)
	}
	got := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
	if len(got) != 1 || got[0].Subject != "to the group" {
		t.Errorf("back, bob's inbox: %+v; want the group's message (the first read after the restart, and the hello, took %s; the hold lasts %s)",
			got, took, daemon.CatchUpWindow)
	}
}

// A session still connected when its daemon stops is a stopped session from then (#736): what is
// sent while the daemon is down is queued for it, and it gets that back however it returns, as a
// server that doesn't say it is reconnecting does here. Before, no stop was recorded, so nothing
// was kept for it, and the next daemon swept its inbox and its T3 route away.
func TestASessionTheDaemonsStopEndedKeepsItsInbox(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	eventually(t, "b holds the team key", func() bool {
		ids := teamIDs(t, remote)
		return len(ids) == 1 && ringOf(t, b)[ids[0]] != nil
	})
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "unread before the stop"})
	eventually(t, "the message to be queued", func() bool {
		return len(b.do(proto.Request{Op: proto.OpNotices, Session: "bbbbbb"}).Messages) == 1
	})

	b.stop()
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "direct, while it was down"})
	a.do(proto.Request{Op: proto.OpSend, To: "*", Subject: "broadcast, while it was down"})
	b.start()
	// The daemon reads what it missed with bob still away.
	eventually(t, "b to be back in a's sight", func() bool { return a.seesDevices(2) })
	b.helloAgain("bbbbbb", false)

	var got []string
	eventually(t, "bob gets what was kept for it", func() bool {
		got = got[:0]
		for _, m := range b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages {
			got = append(got, m.Subject)
		}
		return len(got) >= 3
	})
	if len(got) != 3 {
		t.Fatalf("bob got %q", got)
	}
}

// The state file lists a session while it is registered, and no longer once its stop is recorded,
// by its grace running out or by the daemon stopping (#762): what a killed daemon leaves listed
// is what was registered with it.
func TestTheStateFileListsRegisteredSessions(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	b := newTimedDevice(t, remote, 300*time.Millisecond, time.Hour)
	// ok is false while the file is being replaced, which says nothing either way.
	listed := func(id string) (registered, stopped, ok bool) {
		raw, err := os.ReadFile(filepath.Join(b.gitDir, brand.StateDir, "state.json"))
		var st struct {
			Registered map[string]bool      `json:"registered"`
			Stopped    map[string]time.Time `json:"stopped"`
		}
		if err != nil || json.Unmarshal(raw, &st) != nil {
			return false, false, false
		}
		_, stopped = st.Stopped[id]
		return st.Registered[id], stopped, true
	}
	is := func(id string, wantRegistered, wantStopped bool) func() bool {
		return func() bool {
			registered, stopped, ok := listed(id)
			return ok && registered == wantRegistered && stopped == wantStopped
		}
	}
	_, conn := b.session("bbbbbb", "")
	b.session("cccccc", "")
	// Listed by the time the session is told it registered: the only wait is for a read that
	// doesn't fall on the file being replaced.
	for _, id := range []string{"bbbbbb", "cccccc"} {
		var registered, ok bool
		eventually(t, "the state file to be read", func() bool { registered, _, ok = listed(id); return ok })
		if !registered {
			t.Fatalf("%s has registered and isn't listed in the state file", id)
		}
	}
	conn.Close()
	eventually(t, "the session whose grace ran out to leave the list, stopped", is("bbbbbb", false, true))
	b.stop()
	eventually(t, "the session the daemon's stop ended to leave the list, stopped", is("cccccc", false, true))
}

// A daemon that was killed leaves its state file as it last wrote it, with its sessions listed
// and no stop recorded. The next daemon counts them as stopped (#762): a message sent while no
// daemon ran is queued for the session before its server is back, as it is after a clean stop.
// Before, nothing was queued for it, and what it had went two minutes after the start.
func TestASessionOfAKilledDaemonGetsWhatWasSentMeanwhile(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	bob, _ := b.session("bbbbbb", "")
	eventually(t, "presence", func() bool { return a.seesSession(bob) })
	path := filepath.Join(b.gitDir, brand.StateDir, "state.json")
	type kept struct {
		Registered map[string]bool              `json:"registered"`
		Stopped    map[string]time.Time         `json:"stopped"`
		Inbox      map[string][]json.RawMessage `json:"inbox"`
	}
	read := func() (raw []byte, st kept, ok bool) {
		raw, err := os.ReadFile(path)
		return raw, st, err == nil && json.Unmarshal(raw, &st) == nil
	}
	// The file as a daemon killed now would leave it.
	var asKilled []byte
	eventually(t, "the state file with bob registered", func() bool {
		raw, st, ok := read()
		asKilled = raw
		return ok && st.Registered["bbbbbb"]
	})
	b.stop()
	if err := os.WriteFile(path, asKilled, 0o600); err != nil {
		t.Fatal(err)
	}
	a.do(proto.Request{Op: proto.OpSend, To: bob, Subject: "sent while no daemon ran"})
	b.start()
	eventually(t, "the message to be queued for a session not yet back", func() bool {
		_, st, ok := read()
		_, stopped := st.Stopped["bbbbbb"]
		return ok && stopped && len(st.Inbox["bbbbbb"]) == 1
	})
	b.helloAgain("bbbbbb", true)
	got := b.do(proto.Request{Op: proto.OpInbox, Session: "bbbbbb"}).Messages
	if len(got) != 1 || got[0].Subject != "sent while no daemon ran" {
		t.Fatalf("bob's inbox once back: %+v", got)
	}
}
