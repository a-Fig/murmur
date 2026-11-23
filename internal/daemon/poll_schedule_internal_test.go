package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
)

// These pin down when the poll loop looks at the remote and how a sync reads what it fetched, as
// docs/design/transport.md ("Reading, and how fast") describes them, without a remote, a git or a
// timer: each step is an event applied to the daemon's state, then the decision pollLoop makes
// after a poll (pollNext) or on a wake (wakeEndsWait). Time is controlled through the fields the
// code reads, convAt and lastPage, set relative to now. TestPollFollowsTheConversation and the
// other rateRig tests still run the real loop against a real remote.

// The defaults are the numbers the design doc gives.
func TestPollDefaultsAreTheDocsNumbers(t *testing.T) {
	t.Parallel()
	var o Options
	o.defaults()
	for _, c := range []struct {
		name      string
		got, want time.Duration
	}{
		{"Poll", o.Poll, 3 * time.Second},
		{"PollCap", o.PollCap, 15 * time.Second},
		{"FastFor", o.FastFor, 3 * time.Minute},
		{"QuietPoll", o.QuietPoll, 30 * time.Second},
		{"FetchLimit", o.FetchLimit, 15 * time.Second},
		{"ReadFirstLimit", o.ReadFirstLimit, 15 * time.Second},
		{"FirstSyncLimit", o.FirstSyncLimit, 10 * time.Second},
		{"maxPollBackoff", maxPollBackoff, time.Minute},
	} {
		if c.got != c.want {
			t.Errorf("%s is %v, want %v", c.name, c.got, c.want)
		}
	}
}

// scheduleDaemon is rateDaemon with every default, so PageOpen is set too.
func scheduleDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := rateDaemon(t)
	d.opt.defaults()
	return d
}

// The events a schedule test applies before a poll. Each holds mu as the code that does it would.
type scheduleEvent func(d *Daemon)

func nothing(*Daemon) {}

func conversation(d *Daemon) {
	d.mu.Lock()
	d.noteConversationLocked()
	d.mu.Unlock()
}

// conversationOver dates the last conversation move to just past FastFor.
func conversationOver(d *Daemon) {
	d.mu.Lock()
	d.convAt = time.Now().Add(-d.opt.FastFor - time.Second)
	d.mu.Unlock()
}

func noSession(d *Daemon) {
	d.mu.Lock()
	clear(d.sessions)
	d.mu.Unlock()
}

// sessionStarts adds a session without moving the conversation; newSessionLocked moves it too
// (TestWhatMovesAConversation), which is sessionStarts then conversation.
func sessionStarts(d *Daemon) {
	d.mu.Lock()
	d.sessions["cccccc"] = &session{id: "cccccc"}
	d.mu.Unlock()
}

func pageOpens(d *Daemon) {
	d.mu.Lock()
	d.lastPage = time.Now()
	d.mu.Unlock()
}

func pageGone(d *Daemon) {
	d.mu.Lock()
	d.lastPage = time.Now().Add(-d.opt.PageOpen - time.Second)
	d.mu.Unlock()
}

// One poll: what happened before it, whether it failed, and what the loop then chose: the rate
// the state calls for, and the wait until the next poll.
type scheduleStep struct {
	before      scheduleEvent
	failed      bool
	every, wait time.Duration
	quiet       bool
}

// The wait after each poll, as pollLoop computes it, for a run of polls from a daemon that has a
// session "bbbbbb" open, has said nothing, and has just started (wait and calm at Poll).
func TestPollScheduleFollowsEvents(t *testing.T) {
	t.Parallel()
	const s = time.Second
	ok := func(before scheduleEvent, every, wait time.Duration) scheduleStep {
		return scheduleStep{before: before, every: every, wait: wait}
	}
	fail := func(before scheduleEvent, every, wait time.Duration) scheduleStep {
		return scheduleStep{before: before, failed: true, every: every, wait: wait}
	}
	quiet := func(st scheduleStep) scheduleStep { st.quiet = true; return st }
	for _, c := range []struct {
		name  string
		opt   func(o *Options)
		steps []scheduleStep
	}{
		{name: "a session open and nothing said doubles the wait up to PollCap", steps: []scheduleStep{
			ok(nothing, 6*s, 6*s), ok(nothing, 12*s, 12*s), ok(nothing, 15*s, 15*s), ok(nothing, 15*s, 15*s),
		}},
		{name: "a conversation holds the poll at Poll for FastFor, and each move restarts it", steps: []scheduleStep{
			ok(nothing, 6*s, 6*s), ok(nothing, 12*s, 12*s),
			ok(conversation, 3*s, 3*s), ok(nothing, 3*s, 3*s),
			ok(conversationOver, 6*s, 6*s), ok(nothing, 12*s, 12*s),
			ok(conversation, 3*s, 3*s),
		}},
		{name: "a failing poll doubles its wait to a minute, and one that works is back at Poll", steps: []scheduleStep{
			ok(conversation, 3*s, 3*s),
			fail(nothing, 3*s, 6*s), fail(nothing, 3*s, 12*s), fail(nothing, 3*s, 24*s), fail(nothing, 3*s, 48*s),
			fail(nothing, 3*s, time.Minute), fail(nothing, 3*s, time.Minute),
			ok(nothing, 3*s, 3*s),
		}},
		// The slow phase goes on doubling under a failure, so the first poll that works after one
		// waits PollCap, the rate the slow phase reached meanwhile, not the 6 s it failed at.
		{name: "a failure in the slow phase backs off from the slow rate", steps: []scheduleStep{
			ok(nothing, 6*s, 6*s),
			fail(nothing, 12*s, 12*s), fail(nothing, 15*s, 24*s), fail(nothing, 15*s, 48*s), fail(nothing, 15*s, time.Minute),
			ok(nothing, 15*s, 15*s),
		}},
		{name: "a conversation during a failure leaves the wait to the backoff", steps: []scheduleStep{
			ok(nothing, 6*s, 6*s), fail(nothing, 12*s, 12*s),
			fail(conversation, 3*s, 24*s), fail(nothing, 3*s, 48*s),
			ok(nothing, 3*s, 3*s),
		}},
		{name: "with no session and no page the poll is QuietPoll, a conversation or not", steps: []scheduleStep{
			quiet(ok(noSession, 30*s, 30*s)), quiet(ok(nothing, 30*s, 30*s)),
			quiet(ok(conversation, 30*s, 30*s)),
		}},
		{name: "a quiet poll that fails backs off to a minute", steps: []scheduleStep{
			quiet(ok(noSession, 30*s, 30*s)),
			quiet(fail(nothing, 30*s, time.Minute)), quiet(fail(nothing, 30*s, time.Minute)),
			quiet(ok(nothing, 30*s, 30*s)),
		}},
		// Quiet carries Poll as calm, so a session that comes starts the slow phase over.
		{name: "a session that comes ends quiet, and the slow phase starts over", steps: []scheduleStep{
			ok(nothing, 6*s, 6*s), ok(nothing, 12*s, 12*s),
			quiet(ok(noSession, 30*s, 30*s)),
			ok(sessionStarts, 6*s, 6*s), ok(nothing, 12*s, 12*s),
			quiet(ok(noSession, 30*s, 30*s)),
			ok(func(d *Daemon) { sessionStarts(d); conversation(d) }, 3*s, 3*s),
		}},
		{name: "a page open counts as a session, until it has not called for PageOpen", steps: []scheduleStep{
			quiet(ok(noSession, 30*s, 30*s)),
			ok(pageOpens, 6*s, 6*s), ok(nothing, 12*s, 12*s),
			quiet(ok(pageGone, 30*s, 30*s)),
		}},
		{name: "a PollCap under Poll never makes the poll faster than Poll",
			opt: func(o *Options) { o.PollCap = time.Second },
			steps: []scheduleStep{
				ok(nothing, 3*s, 3*s), ok(nothing, 3*s, 3*s),
			}},
		{name: "a QuietPoll under Poll never makes the poll faster than Poll",
			opt: func(o *Options) { o.Poll, o.QuietPoll = 45*s, 30*s },
			steps: []scheduleStep{
				quiet(ok(noSession, 45*s, 45*s)),
				quiet(fail(nothing, 45*s, time.Minute)), // the backoff's minute, over a base under it
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := scheduleDaemon(t)
			if c.opt != nil {
				c.opt(&d.opt)
			}
			wait, calm := d.opt.Poll, d.opt.Poll
			for i, st := range c.steps {
				st.before(d)
				rate, next := d.pollNext(wait, calm, st.failed)
				calm, wait = rate.calm, next
				if rate.every != st.every || next != st.wait || rate.quiet != st.quiet {
					t.Fatalf("poll %d (failed %v): rate %v, wait %v, quiet %v; want rate %v, wait %v, quiet %v",
						i+1, st.failed, rate.every, next, rate.quiet, st.every, st.wait, st.quiet)
				}
			}
		})
	}
}

// A wake (kickPoll) ends the loop's wait only when what came calls for a faster rate than the
// wait was chosen at, and not during a failure's backoff unless the loop was quiet.
func TestWakeEndsTheWaitOnlyForAFasterRate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		setup   scheduleEvent // the state the wait was chosen in
		calm    time.Duration // the slow phase then
		failing bool
		event   scheduleEvent // what came during the wait
		wake    bool
	}{
		{"at the cap, a conversation moves", nothing, 15 * time.Second, false, conversation, true},
		{"at the cap, a page opens", nothing, 15 * time.Second, false, pageOpens, false},
		{"at Poll already, a conversation moves", conversation, 3 * time.Second, false, conversation, false},
		{"in the slow phase, a wake with nothing new", nothing, 6 * time.Second, false, nothing, false},
		{"quiet, a session starts", noSession, 3 * time.Second, false, sessionStarts, true},
		{"quiet, a page opens", noSession, 3 * time.Second, false, pageOpens, true},
		{"quiet, a message with no session here", noSession, 3 * time.Second, false, conversation, false},
		{"failing at the cap, a conversation moves", nothing, 15 * time.Second, true, conversation, false},
		{"failing while quiet, a session starts", noSession, 3 * time.Second, true, sessionStarts, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d := scheduleDaemon(t)
			c.setup(d)
			rate := d.pollEvery(c.calm)
			c.event(d)
			if got := d.wakeEndsWait(rate.calm, rate, c.failing); got != c.wake {
				t.Errorf("waiting at %v (quiet %v, failing %v): the wake ends the wait %v, want %v",
					rate.every, rate.quiet, c.failing, got, c.wake)
			}
		})
	}
}

// A conversation's move wakes the loop, and moves that come before it wakes are one wake.
func TestAConversationWakesThePoll(t *testing.T) {
	t.Parallel()
	d := scheduleDaemon(t)
	d.pollWake = make(chan struct{}, 1)
	conversation(d)
	conversation(d)
	if len(d.pollWake) != 1 {
		t.Fatalf("%d wakes waiting after a conversation moved twice, want 1", len(d.pollWake))
	}
}

// A sync reads the names registry, then every presence, then the team, then the logs, which hold
// the messages, so a turn is written with what the same sync brought about its sender. Only the
// refs Murmur reads are there to order. pollOnce itself, which also holds every log back until the
// loop over readOrder is done, is only run against a real remote, since every read in it is a git
// (TestATurnKnowsTheSenderFetchedWithItsMessage).
func TestReadOrderPutsNamesPresenceAndTeamBeforeLogs(t *testing.T) {
	t.Parallel()
	listed := map[string]string{
		brand.MeshPrefix + "bbbb-0002":       "1",
		brand.MeshPrefix + "aaaa-0001":       "2",
		brand.PresencePrefix + "bbbb-0002":   "3",
		brand.PresencePrefix + "aaaa-0001":   "4",
		brand.TeamRef:                        "5",
		brand.NamesRef:                       "6",
		brand.RefRoot + "elsewhere/x":        "7", // not a ref Murmur reads
		brand.PresencePrefix + "aaaa-0001/x": "8",
	}
	got := readOrder(readable(listed))
	want := []string{
		brand.NamesRef,
		brand.PresencePrefix + "aaaa-0001", brand.PresencePrefix + "bbbb-0002",
		brand.TeamRef,
		brand.MeshPrefix + "aaaa-0001", brand.MeshPrefix + "bbbb-0002",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("a sync reads\n  %v\nwant\n  %v", got, want)
	}
}

// A sync whose time ran out, or whose registry and presence ran past their own limit, is no fresh
// look: pollOnce returns cutShort's error before it sets lastPollOK, which staleness is judged by.
func TestACutShortSyncIsNoFreshLook(t *testing.T) {
	t.Parallel()
	live := context.Background()
	cancelled, cancel := context.WithCancel(live)
	cancel()
	expired, cancel2 := context.WithDeadline(live, time.Now().Add(-time.Second))
	defer cancel2()
	for _, c := range []struct {
		name     string
		ctx      context.Context
		firstErr error
		want     error // nil for a fresh look
		says     string
	}{
		{"every read done in time", live, nil, nil, ""},
		{"the sync cancelled", cancelled, nil, context.Canceled, ""},
		{"the sync out of time", expired, nil, context.DeadlineExceeded, ""},
		{"out of time, and the first reads too", expired, context.DeadlineExceeded, context.DeadlineExceeded, ""},
		{"the first reads past their limit", live, context.DeadlineExceeded, context.DeadlineExceeded,
			"reading the names registry and presence took over 2.5s"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := cutShort(c.ctx, c.firstErr, 2468*time.Millisecond)
			if c.want == nil {
				if err != nil {
					t.Fatalf("a full sync is cut short: %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("cutShort = %v, want %v", err, c.want)
			}
			if c.says != "" && !strings.Contains(err.Error(), c.says) {
				t.Errorf("cutShort says %q, want it to say %q", err, c.says)
			}
		})
	}
}

// What freshness is for: a peer is stale only by the last successful sync, so a daemon whose syncs
// fail, or that hasn't had one, calls nobody gone however long ago it last saw them change.
func TestStalenessGoesByTheLastSuccessfulSync(t *testing.T) {
	t.Parallel()
	d := scheduleDaemon(t)
	seen := time.Now().Add(-time.Hour)
	p := &peer{lastChange: seen}
	for _, c := range []struct {
		name   string
		lastOK time.Time
		stale  bool
	}{
		{"no successful sync yet", time.Time{}, false},
		{"the last success within StaleAfter of the change", seen.Add(d.opt.StaleAfter - time.Second), false},
		{"the last success past StaleAfter of the change", seen.Add(d.opt.StaleAfter + time.Second), true},
	} {
		d.lastPollOK = c.lastOK
		if got := d.staleLocked(p); got != c.stale {
			t.Errorf("%s: stale %v, want %v", c.name, got, c.stale)
		}
	}
}

// The first reads end with the sync: one with no time left gives them none, and one cancelled
// cancels them. TestReadFirstContextKeepsTheLogsTime has the limits of a sync with time left.
func TestReadFirstContextEndsWithTheSync(t *testing.T) {
	t.Parallel()
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	first, cancelFirst := readFirstContext(expired, 15*time.Second)
	defer cancelFirst()
	if !errors.Is(first.Err(), context.DeadlineExceeded) {
		t.Errorf("a sync out of time leaves the first reads %v, want them out of time", first.Err())
	}

	sync, cancelSync := context.WithCancel(context.Background())
	first2, cancelFirst2 := readFirstContext(sync, 15*time.Second)
	defer cancelFirst2()
	cancelSync()
	<-first2.Done() // closed by the parent's cancel, at once
	if !errors.Is(first2.Err(), context.Canceled) {
		t.Errorf("a cancelled sync leaves the first reads %v, want them cancelled", first2.Err())
	}
}
