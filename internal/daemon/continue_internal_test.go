package daemon

import (
	"fmt"
	"io"
	"log"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// A Stop's continuation that no other hook shows went on stops counting as at work after
// ContinueHold, outside T3: its answer may have been lost, and the agent stopped. A hook of the
// session's in that time confirms it.
func TestALostContinuationLapses(t *testing.T) {
	t.Parallel()
	d := &Daemon{opt: Options{ContinueHold: time.Minute}, dir: t.TempDir(), log: log.New(io.Discard, "", 0), device: "here-0001", started: time.Now(),
		sessions: map[string]*session{}, threadRead: map[threadKey]time.Time{},
		st: state{Inbox: map[string][]*inMsg{}}}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.newSessionLocked(&proto.Hello{ID: "bbbbbb", ClaudeSession: "claude-bob"}, "claude-code", "", "bbbbbb")
	s.hooked, s.stopSeen, s.stopContinues = true, true, true
	now := time.Now()
	s.busyAt, s.turnStart, s.continuedAt = now, now, now
	if !d.hookBusyLocked(s.id) {
		t.Fatal("not at work just after a continuation")
	}
	s.turnStart, s.continuedAt = now.Add(-time.Hour), now.Add(-d.opt.ContinueHold)
	if d.hookBusyLocked(s.id) {
		t.Fatal("still at work after continueHold with no hook since")
	}
	d.touchHookLocked(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"})
	if !d.hookBusyLocked(s.id) || !s.continuedAt.IsZero() || s.continuations != 0 {
		t.Errorf("after a tool call's hook: busy %v, continuedAt %v, continuations %d",
			d.hookBusyLocked(s.id), s.continuedAt, s.continuations)
	}
	if s.turnStart.Before(now) {
		t.Error("a hook after a lapsed continuation doesn't start a new turn")
	}
}

// A lapse ends the turn only of a session outside T3, by its route: a T3 session whose thread
// hasn't been read (the reads failing) keeps its turn, which the database ends once a read works.
func TestALapseLeavesATurnInT3ToTheDatabase(t *testing.T) {
	t.Parallel()
	d := &Daemon{opt: Options{ContinueHold: time.Minute}, dir: t.TempDir(), log: log.New(io.Discard, "", 0), device: "here-0001",
		started: time.Now(), sessions: map[string]*session{}, threadRead: map[threadKey]time.Time{},
		st: state{Inbox: map[string][]*inMsg{}}, pushKick: make(chan struct{}, 1)}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.newSessionLocked(&proto.Hello{ID: "bbbbbb", ClaudeSession: "claude-bob"}, "claude-code", "", "bbbbbb")
	s.route = proto.Route{T3CLI: "t3", T3Thread: "thread-bob"}
	s.hooked, s.stopSeen, s.stopContinues = true, true, true
	now := time.Now()
	s.busyAt, s.turnStart, s.continuedAt = now, now, now.Add(-d.opt.ContinueHold)
	d.endLapsedTurnsLocked(pageSettings{})
	if s.busyAt.IsZero() || !s.continuedAt.IsZero() {
		t.Errorf("after the lapse in T3: busyAt %v, continuedAt %v; want the turn kept, the mark gone", s.busyAt, s.continuedAt)
	}
	if !d.hookBusyLocked(s.id) {
		t.Error("the T3 session isn't at work after the lapse")
	}
}

// stopRig is a device with one session, bob: Claude Code in T3 thread thread-bob, whose hook speaks
// for it as claude-bob. Its hook's requests go straight to notices and turnEnd, and a message is
// queued as a sync queues one (queueInboxLocked): no remote, no git, no t3. That is all the Stop
// hook's decisions read, so tests of what a Stop gives, and whether a turn's end leaves the
// session idle, need no more; TestTheStopHookKeepsTheTurnGoing and its neighbours in
// busy_continue_test.go keep the whole path, from one device's send to the other's hook and T3.
func stopRig(t *testing.T) *Daemon {
	t.Helper()
	d := threadsDaemon(t)
	d.started = time.Now().Add(-time.Hour) // past the catch-up window
	d.st.Inbox = map[string][]*inMsg{}
	d.opt.ContinueHold = 2 * time.Minute
	d.introduced, d.pushing, d.pushWarned = map[string]map[string]bool{}, map[string]bool{}, map[string]bool{}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.newSessionLocked(&proto.Hello{ID: "bbbbbb", ClaudeSession: "claude-bob"}, "claude-code", "", "bbbbbb")
	s.route = proto.Route{T3CLI: "/t3", T3Thread: "thread-bob"}
	return d
}

// arrive queues a message from another device's session for bob, with subject subject.
func arrive(d *Daemon, subject string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queueInboxLocked("bbbbbb", bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message,
		From: "there-0002/aaaaaa", To: bus.Addr(d.device, "bbbbbb"), Subject: subject, Body: "its body"})
}

// The hook's requests: a prompt or a tool call (notices), and the Stop at a turn's end, which can
// keep the turn going.
func hookNotices(t *testing.T, d *Daemon) proto.Response {
	t.Helper()
	r := d.notices(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true})
	if r.Error != "" {
		t.Fatalf("notices: %s", r.Error)
	}
	return r
}

func stopHook(t *testing.T, d *Daemon) proto.Response {
	t.Helper()
	r := d.turnEnd(proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob", Continues: true})
	if r.Error != "" {
		t.Fatalf("turnend: %s", r.Error)
	}
	return r
}

func subjects(ms []proto.MessageView) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Subject)
	}
	return out
}

// Claude Code ends a turn after eight continuations in a row, whatever the hook says: the ninth
// Stop with no tool call between gives nothing, and its message is handed over as a turn instead. A tool call starts the count again.
//
// Kept from the two-device test it replaces (#1010): the count of Stops in a row, what each gives,
// and that the ninth's message is the next turn for T3. A fake is enough, as none of that reads the
// bus: how the messages reached bob's inbox, and the T3 call that takes the turn, are the same for
// any message, and TestTheStopHookKeepsTheTurnGoing and TestALapsedContinuationHandsOverWhatCameMeanwhile
// keep them end to end. The two devices sent nine messages across the remote for it: about 530 git
// and 17 s on a laptop, and 76 s on a Windows runner (#1088's baseline).
func TestContinuationsStopAtClaudeCodesCap(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	hookNotices(t, d)
	stopHook(t, d)
	hookNotices(t, d)
	for i := 0; i < stopBlockCap+1; i++ {
		subject := fmt.Sprintf("message %d", i+1)
		arrive(d, subject)
		got := subjects(stopHook(t, d).Read)
		switch {
		case i < stopBlockCap && (len(got) != 1 || got[0] != subject):
			t.Fatalf("Stop %d gave %v", i+1, got)
		case i == stopBlockCap && len(got) != 0:
			t.Fatalf("the ninth Stop in a row gave %v", got)
		}
	}
	// The handoff loop's next job is the ninth, as a turn by T3; it is handed over as the loop
	// would, but for running t3.
	wake := d.wakeSnapshot()
	d.mu.Lock()
	j := d.nextPushLocked("bbbbbb", wake)
	d.mu.Unlock()
	if j == nil || j.via != viaT3 || len(j.msgs) != 1 || j.msgs[0].Event.Subject != "message 9" {
		t.Fatalf("the next turn after the ninth Stop: %+v, want message 9 by T3", j)
	}
	d.finishPush(j, nil)

	hookNotices(t, d) // a new turn
	arrive(d, "after a tool call")
	if got := subjects(stopHook(t, d).Read); len(got) != 1 || got[0] != "after a tool call" {
		t.Fatalf("a Stop after the count started again gave %v", got)
	}
}

// A message the session was told of before its first Stop, when there was no known way in, is
// given at that Stop: the Stop is the way in.
//
// Kept from the two-device test it replaces (#1010): a session outside T3 with no socket, told of a
// message by the hook during its first turn, is given it whole at that turn's Stop. A fake is
// enough, as both are the hook's requests and what they read is the inbox: the message is queued
// as a sync queues one. The test this replaces started about 220 git: 12 s on a laptop and 51 s on
// a Windows runner (#1088's baseline).
func TestTheFirstStopGivesWhatWasToldBeforeIt(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	d.mu.Lock()
	d.sessions["bbbbbb"].route = proto.Route{} // outside T3, and no socket: no way in but the Stop
	d.mu.Unlock()

	hookNotices(t, d)
	arrive(d, "told in the first turn")
	if got := subjects(hookNotices(t, d).Messages); len(got) != 1 {
		t.Fatalf("the hook told of %v", got)
	}
	if got := subjects(stopHook(t, d).Read); len(got) != 1 || got[0] != "told in the first turn" {
		t.Fatalf("the first Stop gave %v", got)
	}
}
