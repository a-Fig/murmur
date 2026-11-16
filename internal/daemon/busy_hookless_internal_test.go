package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// A session in T3 with no hook (Muse, OpenCode 1) is at work while T3's database shows its thread's
// run going, and just after a handoff until a read that began after it (#699). Nothing holds it
// where the thread has never been read.
func TestAHooklessSessionIsAtWorkWhileItsRunGoes(t *testing.T) {
	t.Parallel()
	busy, idle := &t3db.State{Busy: true}, &t3db.State{}
	left := &t3db.State{Busy: true, Left: true}
	for _, tc := range []struct {
		name   string
		state  *t3db.State   // nil: never read
		read   time.Duration // ago, when the last read began
		handed time.Duration // ago; 0: not handed over
		want   bool
	}{
		{"a run going", busy, 2 * time.Second, 0, true},
		{"no run", idle, 2 * time.Second, 0, false},
		{"only runs left behind", left, 2 * time.Second, 0, false},
		{"handed over, not read since", idle, 5 * time.Second, 2 * time.Second, true},
		{"handed over, a read since finds no run", idle, time.Second, 2 * time.Second, false},
		{"handed over, a read since finds the run", busy, time.Second, 2 * time.Second, true},
		{"handed over past handedHold, not read since", idle, handedHold + 2*time.Second, handedHold + time.Second, false},
		{"handed over, thread never read", nil, 0, 2 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := threadsDaemon(t)
			inT3(d, "aaaaaa", time.Hour)
			now := time.Now()
			s := d.sessions["aaaaaa"]
			k := threadKey{"", "thread-aaaaaa"}
			if tc.state != nil {
				d.threads[k], d.threadRead[k] = *tc.state, now.Add(-tc.read)
			}
			if tc.handed > 0 {
				s.handedAt = now.Add(-tc.handed)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if got := d.hookBusyLocked("aaaaaa"); got != tc.want {
				t.Errorf("hookBusyLocked = %v, want %v", got, tc.want)
			}
			if m, _, _ := d.candidateLocked("aaaaaa", wakeWithin(0)); tc.want && m != nil {
				t.Errorf("a message to hand over while at work")
			}
		})
	}
}

// The run of a hookless session is dated from when T3 took its prompt. When a read finds it over,
// what came during it is held, to go in as a turn however long the run took; what came before it
// and has aged past pushMaxAge is not, nor is a receipt, which asked to tell, not to wake.
func TestAHooklessRunsEndHoldsWhatCameDuringIt(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", time.Hour)
	s := d.sessions["aaaaaa"]
	now := time.Now()
	took := now.Add(-40 * time.Minute)
	k := threadKey{"", "thread-aaaaaa"}
	d.threads[k], d.threadRead[k] = t3db.State{Busy: true, LastRun: took}, now.Add(-39*time.Minute)
	during := &inMsg{Event: bus.Event{ID: "during", Type: bus.Message, From: "dev2/bbbbbb"}, Arrived: now.Add(-30 * time.Minute)}
	before := &inMsg{Event: bus.Event{ID: "before", Type: bus.Message, From: "dev2/bbbbbb"}, Arrived: now.Add(-50 * time.Minute)}
	receipt := &inMsg{Event: bus.Event{ID: "receipt", Type: bus.Message, From: "dev2/" + bus.ReceiptFrom}, Arrived: now.Add(-20 * time.Minute)}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": {before, during, receipt}}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.noteHooklessLocked(wakeWithin(0))
	if !s.runSeenAt.Equal(took) {
		t.Fatalf("runSeenAt = %v, want when T3 took the prompt, %v", s.runSeenAt, took)
	}
	if pushable(during) {
		t.Fatal("a message 30 minutes old is pushable before the run ends")
	}

	d.threads[k], d.threadRead[k] = t3db.State{LastRun: now.Add(-time.Second)}, now
	d.noteHooklessLocked(wakeWithin(0))
	if !during.Held || before.Held || receipt.Held {
		t.Errorf("held: during %v, before %v, receipt %v; want only during", during.Held, before.Held, receipt.Held)
	}
	if !s.runSeenAt.IsZero() {
		t.Errorf("runSeenAt = %v after the run ended, want zero", s.runSeenAt)
	}
	if m, via, _ := d.candidateLocked("aaaaaa", wakeWithin(0)); m != during || via != viaT3 {
		t.Errorf("candidate after the run = %v by %q, want the held message by T3", m, via)
	}
}

// What a hookless session in T3 has waiting goes in as one turn: four messages are one turn, not
// four in T3's queue (#699). A sender is described only in its first message of the turn. A hooked
// session still gets one message a turn, the rest by its hook.
func TestAHooklessSessionGetsWhatWaitedAsOneTurn(t *testing.T) {
	t.Parallel()
	for _, hooked := range []bool{false, true} {
		d := threadsDaemon(t)
		inT3(d, "aaaaaa", 0)
		d.sessions["aaaaaa"].hooked = hooked
		d.threads[threadKey{"", "thread-aaaaaa"}], d.threadRead[threadKey{"", "thread-aaaaaa"}] = t3db.State{LastRun: time.Now()}, time.Now()
		var box []*inMsg
		for i, from := range []string{"dev2/bbbbbb", "dev2/cccccc", "dev2/bbbbbb", "dev2/dddddd"} {
			box = append(box, &inMsg{Event: bus.Event{ID: string(rune('a' + i)), Type: bus.Message, From: from,
				Subject: "subject " + string(rune('a'+i)), Body: "body"}, Arrived: time.Now().Add(time.Duration(i) * time.Millisecond)})
		}
		d.st.Inbox = map[string][]*inMsg{"aaaaaa": box}
		d.mu.Lock()
		j := d.nextPushLocked("aaaaaa", wakeWithin(0))
		d.mu.Unlock()
		if j == nil {
			t.Fatalf("hooked %v: no job", hooked)
		}
		if hooked {
			if len(j.msgs) != 1 || strings.Count(j.text, "Message from") != 1 {
				t.Errorf("hooked: a job of %d messages, text %q; want one", len(j.msgs), j.text)
			}
			continue
		}
		if len(j.msgs) != 4 || strings.Count(j.text, "Message from") != 4 {
			t.Fatalf("hookless: a job of %d messages, text %q; want all four", len(j.msgs), j.text)
		}
		for i, m := range j.msgs {
			if m != box[i] || !m.Pushing {
				t.Errorf("message %d: %s, pushing %v; want %s, in the order they came", i, m.Event.ID, m.Pushing, box[i].Event.ID)
			}
		}
		if i, k := strings.Index(j.text, "subject a"), strings.Index(j.text, "subject d"); i < 0 || k < i {
			t.Errorf("the turn's messages are out of order: %q", j.text)
		}

		d.finishPush(j, nil)
		d.mu.Lock()
		if n := len(d.st.Inbox["aaaaaa"]); n != 0 {
			t.Errorf("%d messages left in the inbox after the turn was handed over", n)
		}
		if d.sessions["aaaaaa"].handedAt.IsZero() || !d.hookBusyLocked("aaaaaa") {
			t.Error("not at work just after the turn was handed over")
		}
		d.mu.Unlock()
	}
}

// A turn of several messages carries no more than maxBatch: the rest wait for the next.
func TestAHooklessTurnStopsAtMaxBatch(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}], d.threadRead[threadKey{"", "thread-aaaaaa"}] = t3db.State{LastRun: time.Now()}, time.Now()
	big := strings.Repeat("x", maxBatch/2)
	var box []*inMsg
	for i := range 3 {
		box = append(box, &inMsg{Event: bus.Event{ID: string(rune('a' + i)), Type: bus.Message, From: "dev2/bbbbbb", Subject: "s", Body: big},
			Arrived: time.Now()})
	}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": box}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.batchLocked("aaaaaa", box[0], viaT3, wakeWithin(0)); len(got) != 1 {
		t.Errorf("a turn of %d messages of half maxBatch each, want 1", len(got))
	}
}

// A session taken for one with no hook, whose hook then first runs mid-run (Claude Code or Codex
// before its first hook call), dates its turn from the run T3 had going: what came during it waited
// in the inbox, and is held for the turn's end however long ago it came.
func TestAFirstHookDatesTheTurnFromTheRunSeen(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", time.Hour)
	s := d.sessions["aaaaaa"]
	s.claudeSession = "claude-a"
	s.runSeenAt = time.Now().Add(-20 * time.Minute)
	seen := s.runSeenAt
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touchHookLocked(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-a"})
	if !s.hooked || !s.turnStart.Equal(seen) || !s.runSeenAt.IsZero() {
		t.Errorf("hooked %v, turnStart %v, runSeenAt %v; want hooked, the run's start %v, zero", s.hooked, s.turnStart, s.runSeenAt, seen)
	}
}

// A person's question wakes a session past its wake setting, and carries nothing else with it: a
// turn that also holds an agent's message is refused at the handoff if the session may no longer
// be woken for the agent's.
func TestAPersonsQuestionCarriesNoAgentsMessagePastTheWakeSetting(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", time.Hour)
	k := threadKey{"", "thread-aaaaaa"}
	d.threads[k], d.threadRead[k] = t3db.State{LastRun: time.Now().Add(-time.Hour)}, time.Now()
	ask := &inMsg{Event: bus.Event{ID: "ask", Type: bus.Message, From: "dev2/" + bus.PersonSession, To: "dev/aaaaaa"}, Arrived: time.Now(), Pushing: true}
	agent := &inMsg{Event: bus.Event{ID: "agent", Type: bus.Message, From: "dev2/bbbbbb", To: "dev/aaaaaa"}, Arrived: time.Now(), Pushing: true}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": {ask, agent}}
	j := &pushJob{id: "aaaaaa", addr: "dev/aaaaaa", msgs: []*inMsg{ask, agent}, via: viaT3,
		route: proto.Route{T3CLI: "/no/such/t3", T3Thread: "thread-aaaaaa"}}
	if err := d.sendTurn(context.Background(), j); !errors.Is(err, errNotNow) {
		t.Errorf("sendTurn = %v for a turn with an agent's message to a thread idle an hour, want errNotNow", err)
	}
}

// One prompt is one turn, and a turn of several messages marks each of them read (#611, #699).
func TestATurnOfSeveralMarksEachRead(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	k := threadKey{"", "thread-aaaaaa"}
	d.threads[k], d.threadRead[k] = t3db.State{LastRun: time.Now()}, time.Now()
	var box []*inMsg
	for _, id := range []string{"one", "two", "three"} {
		box = append(box, &inMsg{Event: bus.Event{ID: id, Type: bus.Message, From: "dev2/bbbbbb", To: "dev/aaaaaa",
			Subject: "subject " + id, Body: strings.Repeat(id+" ", 2000), Receipts: id != "one"}, Arrived: time.Now()})
	}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": box}
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.nextPushLocked("aaaaaa", wakeWithin(0))
	if len(j.msgs) != 3 {
		t.Fatalf("a turn of %d messages, want 3", len(j.msgs))
	}
	if !d.readOnPromptLocked("aaaaaa", j.text[:proto.MaxPrompt]) {
		t.Fatal("the turn's prompt, as the hook cuts it, marked nothing")
	}
	read := map[string]bool{}
	for _, m := range d.st.Marks {
		if m.State == bus.MarkRead {
			read[m.Msg] = true
		}
	}
	if !read["two"] || !read["three"] || read["one"] || len(d.st.AwaitingRead["aaaaaa"]) != 0 {
		t.Errorf("read %v, still waiting %+v; want two and three read, none waiting", read, d.st.AwaitingRead["aaaaaa"])
	}
}

// A turn of more messages than are kept waiting for their prompt (maxAwaiting) still marks those
// kept read: the entry holding the turn's text, trimmed, gives it to the first that stays.
func TestATurnOfMoreThanMaxAwaitingMarksTheKeptRead(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	k := threadKey{"", "thread-aaaaaa"}
	d.threads[k], d.threadRead[k] = t3db.State{LastRun: time.Now()}, time.Now()
	var box []*inMsg
	for i := range maxAwaiting + 1 {
		id := fmt.Sprintf("m%02d", i)
		box = append(box, &inMsg{Event: bus.Event{ID: id, Type: bus.Message, From: "dev2/bbbbbb", To: "dev/aaaaaa",
			Subject: "subject " + id, Receipts: true}, Arrived: time.Now()})
	}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": box}
	d.mu.Lock()
	defer d.mu.Unlock()
	j := d.nextPushLocked("aaaaaa", wakeWithin(0))
	if len(j.msgs) != maxAwaiting+1 || len(d.st.AwaitingRead["aaaaaa"]) != maxAwaiting {
		t.Fatalf("a turn of %d, %d waiting; want %d, %d", len(j.msgs), len(d.st.AwaitingRead["aaaaaa"]), maxAwaiting+1, maxAwaiting)
	}
	if !d.readOnPromptLocked("aaaaaa", j.text) {
		t.Fatal("the turn's prompt marked nothing")
	}
	if n := len(d.st.AwaitingRead["aaaaaa"]); n != 0 {
		t.Errorf("%d still waiting after the turn's prompt", n)
	}
}
