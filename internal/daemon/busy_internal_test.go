package daemon

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/web"
)

// What the hook's having a session at work does to its messages (#202), checked on stopRig: the
// hook's requests go through handle, a message is queued as a sync queues one, and the handoff
// loop's part is taken a step at a time (look, turn). None of it reads the bus, and none of these
// tests' sessions has a thread T3's database knows, as none of the two-device tests they replace
// (#1010) had. busy_test.go keeps the path whole: TestABusySessionIsToldAndGetsTheTurnWhenIdle from
// one device's send through the other's hook to t3, TestATurnSeenBeforeItsHandoffReturnsHoldsNothing
// with a t3 slow to answer, and the tests of what T3's database ends and holds.

// look is the handoff loop's decision for bob, once: the look pushLoop takes of bob's inbox
// (candidateLocked), and, if that finds a message to hand over, the job its pusher takes
// (nextPushLocked), noted as sendTurn notes one before it runs t3 (sendingLocked). nil if it finds
// none. The job is on its way, as while `t3 thread send` runs, until finishPush.
//
// It is not the loop: pushLoop first ends turns that lapsed and waits out a save that failed or an
// update, and sendTurn asks again, before it sends, whether the user still consents, the device is
// visible, and the session may be woken and isn't at work. Nothing in these tests comes between
// the look and the send for those to answer differently.
func look(d *Daemon) *pushJob {
	wake := d.wakeSnapshot()
	d.mu.Lock()
	defer d.mu.Unlock()
	if m, _, _ := d.candidateLocked("bbbbbb", wake); m == nil {
		return nil
	}
	d.pushing["bbbbbb"] = true
	j := d.nextPushLocked("bbbbbb", wake)
	if j != nil && j.via == viaT3 {
		d.sendingLocked(j)
	}
	return j
}

// turn is look, with what it found finished as the pusher finishes a handoff that worked
// (finishPush), t3 not having been run: the turn's text, "" if there was nothing to hand over.
func turn(d *Daemon) string {
	j := look(d)
	if j == nil {
		return ""
	}
	d.finishPush(j, nil)
	return j.text
}

// turnEnded is the hook at the end of a turn, one that can't keep the turn going.
func turnEnded(t *testing.T, d *Daemon) {
	t.Helper()
	if r := d.handle(context.Background(), proto.Request{Op: proto.OpTurnEnd, ClaudeSession: "claude-bob"}); r.Error != "" {
		t.Fatalf("turnend: %s", r.Error)
	}
}

// finishedATurn is the hook through one turn, a prompt and its end, so the daemon knows the
// session's Stop hook runs.
func finishedATurn(t *testing.T, d *Daemon) {
	t.Helper()
	hookNotices(t, d)
	turnEnded(t, d)
}

// subagentNotices is a subagent's tool call in bob's conversation.
func subagentNotices(t *testing.T, d *Daemon) proto.Response {
	t.Helper()
	r := d.handle(context.Background(), proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Subagent: true})
	if r.Error != "" {
		t.Fatalf("a subagent's notices: %s", r.Error)
	}
	return r
}

// A message that comes while the session is idle wakes it as before, and one that comes during a
// turn with no tool call after it, so nothing was told, goes in when the turn ends.
//
// Kept from the two-device test it replaces: an idle session's message is its next turn, one that
// comes while the hook has it at work is no turn, and the turn's end makes it one. A fake is
// enough, as the session being at work is what the hook last said, which the daemon holds. The two
// devices started about 270 git: 49 s on a Windows runner (main's run 45190341796).
func TestAnIdleSessionIsWokenAndAnUntoldMessageWaitsForTheTurnEnd(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	finishedATurn(t, d)
	arrive(d, "while idle")
	if got := turn(d); !strings.Contains(got, "while idle") {
		t.Fatalf("the idle session's turn: %q", got)
	}

	hookNotices(t, d) // the turn it was woken for
	arrive(d, "late in the turn")
	if j := look(d); j != nil {
		t.Fatalf("a turn while at work: %q", j.text)
	}
	turnEnded(t, d)
	if got := turn(d); !strings.Contains(got, "late in the turn") {
		t.Errorf("the turn after: %q", got)
	}
}

// Hooks that aren't the handed turn's prompt, come while the send runs, keep the hold (#677): a
// tool call and a Stop may be of an earlier turn that T3's database had ended, and say nothing of
// the turn the send makes, which T3 may have queued behind them. The next message waits. A prompt
// that is the handed turn's, and its Stop, are that turn, come and gone: the next message goes.
//
// Kept from the two-device test it replaces: a tool call and a Stop while the handoff is on its
// way leave the session held once the handoff returns. Added here: so does a prompt that isn't the
// handed turn's, and the handed turn's own prompt doesn't, which with a real t3 slow to answer is
// TestATurnSeenBeforeItsHandoffReturnsHoldsNothing's. A fake is enough, as the hold is made from
// when the handoff returned and what the hook said meanwhile; the job is taken and finished as
// the pusher does, with the hooks between, where the old test had a t3 that slept 3 s and then
// watched 6 s for no second call. The two devices took 59 s on a Windows runner (main's run
// 45190341796).
func TestHooksThatArentTheHandedTurnKeepTheHold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		prompt func(handed string) string // what the hook's prompt says while the send runs
		held   bool
	}{
		{"a tool call and a Stop", func(string) string { return "" }, true},
		{"another prompt and its Stop", func(string) string { return "fix the build first" }, true},
		{"the handed turn's prompt and its Stop", func(handed string) string { return handed }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := stopRig(t)
			finishedATurn(t, d)
			arrive(d, "first")
			j := look(d)
			if j == nil || j.via != viaT3 {
				t.Fatalf("the first: %+v, want a turn by T3", j)
			}
			r := d.handle(context.Background(), proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob", Whole: true, Prompt: tc.prompt(j.text)})
			if r.Error != "" {
				t.Fatalf("notices: %s", r.Error)
			}
			turnEnded(t, d)
			// The send returns after that Stop, by a clock that can tell the two apart: Windows's
			// moves in steps of up to 16 ms, and the hold is for a handoff later than the last
			// turn's end.
			time.Sleep(20 * time.Millisecond)
			d.finishPush(j, nil)
			arrive(d, "second")
			switch next := look(d); {
			case tc.held && next != nil:
				t.Fatalf("the second wasn't held behind the first's turn: %q", next.text)
			case !tc.held && (next == nil || !strings.Contains(next.text, "second")):
				t.Fatalf("the second was held after the first's turn had come and gone: %+v", next)
			}
		})
	}
}

// A message the agent read with inbox during its turn is not given to it again when the turn ends.
//
// Kept from the two-device test it replaces: told of by the hook, read with inbox, and no turn at
// the turn's end. A fake is enough, as the inbox is the daemon's own and reading it is a request
// to this daemon. The two devices took 37 s on a Windows runner (main's run 45190341796).
func TestAMessageReadDuringTheTurnIsNotATurnAfter(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	finishedATurn(t, d)
	hookNotices(t, d)
	arrive(d, "read me now")
	if got := subjects(hookNotices(t, d).Messages); len(got) != 1 {
		t.Fatalf("the hook told of %v", got)
	}
	r := d.handle(context.Background(), proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	if got := subjects(r.Messages); r.Error != "" || len(got) != 1 {
		t.Fatalf("inbox: %v (%s)", got, r.Error)
	}
	turnEnded(t, d)
	if j := look(d); j != nil {
		t.Fatalf("a turn for a message already read: %q", j.text)
	}
}

// A subagent's tool call during the session's turn tells it nothing: the notice would go to the
// subagent (#188).
//
// Kept from the two-device test it replaces: a subagent's hook is told of nothing and no count,
// the message is no turn meanwhile, and the session's own next tool call is told of it. A fake is
// enough, as whose hook it is comes in the request. TestBusySessionGetsTheWholeMessageThroughTheHook
// keeps a subagent's hook being given nothing with a message that crossed the bus. The two devices
// took 38 s on a Windows runner (main's run 45190341796).
func TestASubagentsCallTellsNothing(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	finishedATurn(t, d)
	hookNotices(t, d) // the turn the subagent works in
	subagentNotices(t, d)
	arrive(d, "for the parent")
	if r := subagentNotices(t, d); len(r.Messages) != 0 || len(r.Read) != 0 || r.Unread != 0 {
		t.Fatalf("the subagent was told %v, given %v, %d unread", subjects(r.Messages), subjects(r.Read), r.Unread)
	}
	if j := look(d); j != nil {
		t.Fatalf("a turn during the session's turn: %q", j.text)
	}
	if got := subjects(hookNotices(t, d).Messages); len(got) != 1 {
		t.Errorf("the session's own next tool call was told %v", got)
	}
}

// A subagent working in the background after the session's turn ended doesn't hold the session's
// messages: its tool calls don't mark the session at work.
//
// Kept from the two-device test it replaces: after a turn's end, a subagent's hook, and the
// message that comes then is a turn. A fake is enough, for the reason above. The two devices took
// 19 s on a Windows runner (main's run 45190341796).
func TestABackgroundSubagentDoesntHoldTheSession(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	finishedATurn(t, d)
	subagentNotices(t, d)
	arrive(d, "while the subagent works")
	if got := turn(d); !strings.Contains(got, "while the subagent works") {
		t.Fatalf("the turn: %q", got)
	}
}

// A clone joined before #202 has no Stop hook until join runs again. Until the daemon has seen a
// turn end, a busy session whose thread T3's database doesn't show gets its message as a turn, as
// before: held, it would never be seen to finish its turn.
//
// Kept from the two-device test it replaces: a prompt's hook and no Stop yet, and the message that
// comes is a turn. A fake is enough, as whether a Stop has been seen is the daemon's own. The two
// devices took 26 s on a Windows runner (main's run 45190341796).
func TestBeforeTheFirstStopABusySessionGetsTheTurn(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	hookNotices(t, d)
	arrive(d, "no stop hook")
	if got := turn(d); !strings.Contains(got, "no stop hook") {
		t.Fatalf("the turn: %q", got)
	}
}

// bobsName is the name bob has in the tests that set the page's settings, which are kept by name.
const bobsName = "brave-heron"

// named gives bob his name, as it lands for a session.
func named(d *Daemon) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setNameLocked(d.sessions["bbbbbb"], bobsName)
}

// saveSettings saves the page's settings as its PUT /api/settings does, past the HTTP handler:
// checked, written to the file, and the handoff loop kicked.
func saveSettings(t *testing.T, d *Daemon, set web.Settings) {
	t.Helper()
	if _, err := (pageSource{d}).SetSettings(context.Background(), set); err != nil {
		t.Fatalf("saving the settings: %v", err)
	}
}

// A message whose handoff to T3 failed may be in T3's queue all the same: with whole messages set,
// it still gets only its subject, as after any failed handoff, not a second telling in full.
//
// Kept from the two-device test it replaces: with bob set to whole messages, a message whose
// handoff failed is told of by the hook, subject only. A fake is enough, as what the hook gives is
// decided from the message's marks and the settings: the handoff is taken and marked as the
// pusher does (attempt, finishPush), with an error where t3 exited 1.
// TestFailedHandoffFallsBackToTheNotice keeps a real t3 failing, and
// TestBusySessionGetsTheWholeMessageThroughTheHook the setting giving a message whole. The two
// devices took 41 s on a Windows runner (main's run 45190341796).
func TestAFailedHandoffIsNotGivenWhole(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	named(d)
	saveSettings(t, d, web.Settings{Wake: web.Wake{When: web.WakeAlways}, SessionBusy: map[string]string{bobsName: web.BusyWhole}})

	arrive(d, "handed over")
	j := look(d)
	if j == nil || j.via != viaT3 {
		t.Fatalf("the handoff: %+v, want a turn by T3", j)
	}
	d.finishPush(j, d.attempt(j.msgs, func() error { return errors.New("exit status 1") }))
	r := hookNotices(t, d)
	if len(r.Read) != 0 || len(r.Messages) != 1 || r.Messages[0].Subject != "handed over" || r.Messages[0].Body != "" {
		t.Errorf("after a failed handoff the hook was given %+v; want the subject only", r)
	}
}

// A hook from before Whole prints only the subjects in Messages (#229). With whole messages set, it
// still gets the subject, and the message stays in the inbox, not given whole to a hook that would
// print nothing of it. A hook that says it prints them is given the next one whole.
//
// Kept from the two-device test it replaces: with bob set to whole messages, a hook that doesn't
// say it prints them gets the subject and the message stays in the inbox. Added here: the same
// rig's next message goes whole to a hook that does say so, so the first wasn't kept back for some
// other reason. A fake is enough, as what the hook is given is decided from its request and the
// settings. The two devices took 37 s on a Windows runner (main's run 45190341796).
func TestAnOlderHookGetsTheSubjectOnly(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	named(d)
	d.mu.Lock()
	d.sessions["bbbbbb"].route = proto.Route{} // no way in but the hook, as the old test's session
	d.mu.Unlock()
	saveSettings(t, d, web.Settings{Wake: web.Wake{When: web.WakeAlways}, SessionBusy: map[string]string{bobsName: web.BusyWhole}})

	arrive(d, "for an older hook")
	r := d.handle(context.Background(), proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"})
	if r.Error != "" || len(r.Read) != 0 || len(r.Messages) != 1 || r.Messages[0].Subject != "for an older hook" || r.Messages[0].Body != "" {
		t.Errorf("a hook that doesn't print whole messages was given %+v; want the subject only", r)
	}
	arrive(d, "for a hook that prints them")
	if r := hookNotices(t, d); len(r.Read) != 1 || r.Read[0].Subject != "for a hook that prints them" || r.Read[0].Body != "its body" {
		t.Errorf("a hook that prints whole messages was given %+v; want the message whole", r)
	}
	r = d.handle(context.Background(), proto.Request{Op: proto.OpInbox, Session: "bbbbbb"})
	if got := subjects(r.Messages); r.Error != "" || !slices.Equal(got, []string{"for an older hook"}) {
		t.Errorf("the inbox has %v (%s); want the message told to an older hook, and not the one given whole", got, r.Error)
	}
}

// The page's wake setting "never" keeps messages as notices, for the device or for one session; a
// message that waited is handed over when the setting is lifted.
//
// Kept from the two-device test it replaces: under never no turn is made; a save that lets bob
// alone be woken kicks the handoff loop, whose look then hands over what waited; and with the
// device at always and bob at never, the next message is no turn and the hook tells of it. A fake
// is enough, as the setting is this device's own: it is saved as the page saves it (SetSettings),
// to the file, and read by the look as the loop reads it. TestAPersonWakesALongIdleThreadWhereAnAgentWouldnt
// keeps a setting put through the page's HTTP handler deciding a real handoff. The two devices
// took 39 s on a Windows runner (main's run 45190341796).
func TestWakeNeverKeepsTheNotice(t *testing.T) {
	t.Parallel()
	d := stopRig(t)
	named(d)
	never, always := web.Wake{When: web.WakeNever}, web.Wake{When: web.WakeAlways}
	kicked := func() bool {
		select {
		case <-d.pushKick:
			return true
		default:
			return false
		}
	}
	saveSettings(t, d, web.Settings{Wake: never})
	arrive(d, "held")
	if j := look(d); j != nil {
		t.Fatalf("a handoff under never: %q", j.text)
	}

	// One session allowed while the device says never.
	kicked()
	saveSettings(t, d, web.Settings{Wake: never, SessionWake: map[string]web.Wake{bobsName: always}})
	if !kicked() {
		t.Error("lifting the setting didn't kick the handoff loop, which is all that would look at what waited")
	}
	if got := turn(d); !strings.Contains(got, "held") {
		t.Fatalf("the turn for the session that may be woken: %q", got)
	}

	// The device allowed while this session says never.
	saveSettings(t, d, web.Settings{Wake: always, SessionWake: map[string]web.Wake{bobsName: never}})
	arrive(d, "held again")
	if j := look(d); j != nil {
		t.Fatalf("a handoff to a session that says never: %q", j.text)
	}
	r := d.handle(context.Background(), proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-bob"})
	if got := subjects(r.Messages); r.Error != "" || !slices.Equal(got, []string{"held again"}) {
		t.Errorf("notices: %q (%s)", got, r.Error)
	}
}
