package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/proto"
)

// How often a daemon polls follows the conversation (#93): Poll for FastFor after a message goes
// out, or one is handed over or received for a session here, or a session starts; slower toward
// PollCap while a session sits open and nothing is said; QuietPoll with no session. Presence
// heartbeats and status changes don't count. A failed poll keeps its back-off.

const (
	testPoll = 100 * time.Millisecond
	testCap  = 800 * time.Millisecond
)

func rateOptions() Options {
	opt := Options{Poll: 3 * time.Second, PollCap: 15 * time.Second, FastFor: 3 * time.Minute, QuietPoll: 30 * time.Second}
	return opt
}

func rateDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{opt: rateOptions(), dir: t.TempDir(), sessions: map[string]*session{}, device: "here-0001",
		log: log.New(io.Discard, "", 0), st: state{Inbox: map[string][]*inMsg{}, Stopped: map[string]time.Time{}}}
	d.sessions["bbbbbb"] = &session{id: "bbbbbb"}
	return d
}

// With a session open and nothing said, the wait doubles from Poll to PollCap and stays there.
func TestPollEveryBacksOffToTheCap(t *testing.T) {
	t.Parallel()
	d := rateDaemon(t)
	calm := d.opt.Poll
	var got []time.Duration
	for range 5 {
		r := d.pollEvery(calm)
		got = append(got, r.every)
		calm = r.calm
	}
	want := []time.Duration{6 * time.Second, 12 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("the waits are %v, want %v", got, want)
		}
	}
	// A cap under Poll never makes the poll faster than Poll.
	d.opt.PollCap = time.Second
	if r := d.pollEvery(d.opt.Poll); r.every != d.opt.Poll {
		t.Errorf("with PollCap under Poll the wait is %v, want Poll", r.every)
	}
}

// What moves a conversation sets the poll to Poll for FastFor, and the wait doubles again after.
func TestPollEveryIsFastAroundAConversation(t *testing.T) {
	t.Parallel()
	d := rateDaemon(t)
	d.mu.Lock()
	d.noteConversationLocked()
	d.mu.Unlock()
	if r := d.pollEvery(15 * time.Second); r.every != 3*time.Second || r.calm != 3*time.Second {
		t.Errorf("just after a conversation moved: %+v, want 3s and 3s to go on from", r)
	}
	d.mu.Lock()
	d.convAt = time.Now().Add(-d.opt.FastFor - time.Second)
	d.mu.Unlock()
	if r := d.pollEvery(3 * time.Second); r.every != 6*time.Second {
		t.Errorf("after FastFor: %v, want 6s", r.every)
	}
}

// Quiet mode is as it was: QuietPoll, whatever was said lately.
func TestPollEveryQuietIsUnchanged(t *testing.T) {
	t.Parallel()
	d := rateDaemon(t)
	delete(d.sessions, "bbbbbb")
	d.mu.Lock()
	d.noteConversationLocked()
	d.mu.Unlock()
	if r := d.pollEvery(3 * time.Second); r.every != 30*time.Second || !r.quiet || r.calm != 3*time.Second {
		t.Errorf("with no session, just after a message: %+v, want 30s, quiet", r)
	}
}

// The moves that count: a session starting, and a message queued in the inbox of a session
// here, or handed over to it as a turn. Not one for a session on another device, or for the CLI.
func TestWhatMovesAConversation(t *testing.T) {
	t.Parallel()
	msg := func(to string) bus.Event {
		return bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: to,
			Subject: "hi", At: time.Now()}
	}
	moved := func(d *Daemon) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.convAt.IsZero()
	}
	d := rateDaemon(t)
	d.queueLocked(msg("elsewhere-0001/bbbbbb"), modeNormal)
	d.queueLocked(msg("here-0001/"+proto.CLISession), modeNormal)
	if moved(d) {
		t.Errorf("a message that is for no session here moved the conversation")
	}
	d.queueLocked(msg("here-0001/bbbbbb"), modeNormal)
	if !moved(d) {
		t.Errorf("a message received for a session here did not move the conversation")
	}

	d = rateDaemon(t)
	d.mu.Lock()
	d.newSessionLocked(&proto.Hello{ID: "cccccc"}, "claude-code", "", "cccccc")
	d.mu.Unlock()
	if !moved(d) {
		t.Errorf("a session starting did not move the conversation")
	}
}

// A device back from invisible looks at the remote at once: its colour goes back into its presence
// only after a sync, so a poll slowed to the cap left its teammates seeing an automatic colour for
// that long (#1093). Going invisible, or to do not disturb from online, wakes nothing.
func TestBackFromInvisibleThePollLooksAtOnce(t *testing.T) {
	t.Parallel()
	woken := func(d *Daemon) bool {
		select {
		case <-d.pollWake:
			return true
		default:
			return false
		}
	}
	moved := func(d *Daemon) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.convAt.IsZero()
	}
	d := rateDaemon(t)
	d.pollWake = make(chan struct{}, 1) // as a running daemon's is
	d.setMode("online", "dnd", time.Time{})
	d.setMode("dnd", "invisible", time.Now())
	if woken(d) || moved(d) {
		t.Errorf("going to do not disturb and then invisible woke the poll")
	}
	d.setMode("invisible", "online", time.Now())
	if !moved(d) || !woken(d) {
		t.Errorf("coming back online from invisible didn't wake the poll")
	}
	// At the slowest rate the loop runs at with a session open, that wake ends its wait.
	slow := pollRate{every: d.opt.PollCap, calm: d.opt.PollCap}
	if !d.wakeEndsWait(d.opt.PollCap, slow, false) {
		t.Errorf("the wake wouldn't end a wait at the cap")
	}

	d = rateDaemon(t)
	d.pollWake = make(chan struct{}, 1)
	d.setMode("online", "invisible", time.Now())
	d.setMode("invisible", "dnd", time.Now())
	if !moved(d) || !woken(d) {
		t.Errorf("coming back from invisible to do not disturb didn't wake the poll")
	}
}

// A message handed over to a session as a turn moves it; one whose handoff failed does not.
func TestHandoffMovesTheConversation(t *testing.T) {
	t.Parallel()
	d, _, _ := rateRig(t, rateOptions(), false)
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: d.device + "/bbbbbb", Subject: "hi"}
	job := func() *pushJob {
		m := &inMsg{Event: e}
		d.mu.Lock()
		d.st.Inbox["bbbbbb"] = []*inMsg{m}
		d.mu.Unlock()
		return &pushJob{id: "bbbbbb", addr: d.device + "/bbbbbb", msgs: []*inMsg{m}, via: "t3"}
	}
	d.finishPush(job(), io.ErrUnexpectedEOF)
	d.mu.Lock()
	failed := !d.convAt.IsZero()
	d.mu.Unlock()
	if failed {
		t.Errorf("a failed handoff moved the conversation")
	}
	d.finishPush(job(), nil)
	d.mu.Lock()
	moved := !d.convAt.IsZero()
	d.mu.Unlock()
	if !moved {
		t.Errorf("a queued message did not move the conversation")
	}
}

// rateRig is a daemon in a clone of a bare remote, with a session "bbbbbb" registered without a
// burst of its own, and a repo for a peer device on the same remote. With run, it has been through
// its first sync and its poll loop is going.
func rateRig(t *testing.T, opt Options, run bool) (*Daemon, *bus.Repo, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	runGit(t, root, "init", "-q", "--bare", remote)
	clone, peerClone := filepath.Join(root, "clone"), filepath.Join(root, "peer")
	runGit(t, root, "clone", "-q", "--no-local", remote, clone)
	runGit(t, root, "clone", "-q", "--no-local", remote, peerClone)
	opt.GitDir = filepath.Join(clone, ".git")
	opt.Log = log.New(io.Discard, "", 0)
	opt.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	d, err := open(ctx, opt, t.TempDir())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	d.bgCtx = ctx
	t.Cleanup(func() { cancel(); d.bg.Wait() })
	// A team key to encrypt for: no team loop runs here to make the first one.
	d.mu.Lock()
	withEpoch(d, true)
	d.mu.Unlock()
	peer := &bus.Repo{Git: gitx.Git{Dir: filepath.Join(peerClone, ".git")}, Name: "peer-0001",
		Email: "peer-0001@" + brand.Name + ".invalid", Remote: "origin"}
	if run {
		if err := d.pollOnce(ctx); err != nil {
			t.Fatal(err)
		}
		d.mu.Lock()
		d.sessions["bbbbbb"] = &session{id: "bbbbbb", started: time.Now()}
		d.mu.Unlock()
		d.bg.Add(1)
		go func() {
			defer d.bg.Done()
			d.pollLoop(ctx)
		}()
	} else {
		d.mu.Lock()
		d.sessions["bbbbbb"] = &session{id: "bbbbbb", started: time.Now()}
		d.mu.Unlock()
	}
	return d, peer, clone
}

// pollsPerSecond counts the remote listings the daemon makes in a window, as a rate per second.
func pollsPerSecond(d *Daemon, window time.Duration) float64 {
	before := d.polls.Load()
	time.Sleep(window)
	return float64(d.polls.Load()-before) / window.Seconds()
}

// peerPresence writes the peer's presence ref, as its heartbeat or a change of status does.
func peerPresence(t *testing.T, peer *bus.Repo, doing string) {
	t.Helper()
	pres := bus.Presence{V: bus.Version, Device: "peer-0001", Host: "peer", At: time.Now().UTC(),
		Sessions: []bus.SessionInfo{{ID: "aaaaaa", Agent: "test", Doing: doing, Started: time.Now().UTC()}}}
	msg, err := bus.Encode(pres, "peer-0001: 1 sessions")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sha, err := peer.NewCommit(ctx, "", msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Push(ctx, sha, brand.PresencePrefix+"peer-0001", bus.Force, ""); err != nil {
		t.Fatal(err)
	}
}

// peerSays appends a message from the peer to its log on the remote.
func peerSays(t *testing.T, peer *bus.Repo, tip, to string) string {
	t.Helper()
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", At: time.Now().UTC(),
		To: to, Subject: "hello"}
	msg, err := bus.Encode(e, e.Line())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sha, err := peer.NewCommit(ctx, tip, msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Push(ctx, sha, brand.MeshPrefix+"peer-0001", bus.FastForward, ""); err != nil {
		t.Fatal(err)
	}
	return sha
}

func within(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// pollWait is the wait pollLoop chose after its last poll, as it tells the health snapshot.
func pollWait(d *Daemon) time.Duration {
	d.health.mu.Lock()
	defer d.health.mu.Unlock()
	return d.health.every
}

// shortestGap is the shortest time between the starts of two polls in a window: under the cap
// only if the loop's timer took a wait under it.
func shortestGap(d *Daemon, window time.Duration) time.Duration {
	shortest := window
	n, at := d.polls.Load(), time.Time{}
	for end := time.Now().Add(window); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if now := d.polls.Load(); now != n {
			if !at.IsZero() && now == n+1 {
				shortest = min(shortest, time.Since(at))
			}
			n, at = now, time.Now()
		}
	}
	return shortest
}

// The poll slows toward the cap while a session is open and nothing is said, ignores a peer's
// presence heartbeats and status changes, speeds up for a while after a message is received, slows
// again, and speeds up after this device sends one.
func TestPollFollowsTheConversation(t *testing.T) {
	// Not parallel: it counts polls over real time, which other tests' load would skew. The counts
	// are upper bounds, which load only lowers. That the poll speeds up is read from the wait the
	// loop chose, and from two polls coming closer together than the cap allows: a count of polls in
	// a second needed every poll to be quick, and under load one can take longer than Poll, which
	// failed the test with the loop doing what it should (#387). FastFor is long enough that the
	// rest of the poll that brought a message, which comes before the wait is chosen, doesn't use
	// it up.
	opt := Options{Poll: testPoll, PollCap: testCap, FastFor: 5 * time.Second, QuietPoll: time.Hour}
	d, peer, _ := rateRig(t, opt, true)
	atCap := func() bool { return pollWait(d) == testCap }
	fast := func(what string) {
		t.Helper()
		within(t, "the wait to go to Poll "+what, opt.FastFor, func() bool { return pollWait(d) == testPoll })
		// A timer left at the cap puts polls the cap apart, give or take a late tick; with Poll,
		// a poll comes as soon as the one before it ends, so only polls that all took most of the
		// cap could fail this.
		if gap := shortestGap(d, 3*time.Second); gap >= testCap*7/8 {
			t.Errorf("%s the polls came no closer than %v apart, with a wait of %v chosen", what, gap, testPoll)
		}
	}
	within(t, "the wait to double up to the cap", time.Minute, atCap) // 200, 400, 800 ms

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { // a teammate's heartbeats and a change of status every so often
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(150 * time.Millisecond):
				peerPresence(t, peer, fmt.Sprintf("step %d", i))
			}
		}
	}()
	before := d.polls.Load()
	window := 2400 * time.Millisecond
	for end := time.Now().Add(window); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if w := pollWait(d); w != testCap {
			t.Fatalf("with a peer's heartbeats and nothing said, the wait went to %v from the cap", w)
		}
	}
	slow := float64(d.polls.Load()-before) / window.Seconds()
	close(stop)
	<-done
	d.mu.Lock()
	seen := d.peers["peer-0001"] != nil
	d.mu.Unlock()
	if !seen {
		t.Fatalf("the daemon never saw the peer's presence, so the test says nothing about it")
	}
	if slow > 2.5 {
		t.Errorf("polled %.1f times a second with nothing said, want about 1 (a wait of up to %v)", slow, testCap)
	}

	// A message for the session: Poll for FastFor. Past FastFor the wait doubles from Poll, so it
	// is Poll again only if a message made it so.
	peerSays(t, peer, "", d.device+"/bbbbbb")
	within(t, "the message to arrive", time.Minute, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return len(d.st.Inbox["bbbbbb"]) == 1
	})
	fast("after a message arrived")

	// Slow again once FastFor has passed and the wait has doubled up.
	within(t, "the wait back at the cap once the conversation went quiet", time.Minute, atCap)
	if again := pollsPerSecond(d, window); again > 2.5 {
		t.Errorf("polled %.1f times a second once the conversation went quiet, want about 1", again)
	}

	// This device sends.
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: d.device + "/bbbbbb", At: time.Now().UTC(),
		To: "*", Subject: "reply"} // the peer writes in the clear, so it can't be sent to alone
	if err := d.appendEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	fast("after a send")
}

// With no session the poll stays at QuietPoll, a message sent from the daemon not changing that.
func TestPollQuietStaysQuiet(t *testing.T) {
	// Not parallel: it counts polls over real time, which other tests' load would skew.
	opt := Options{Poll: testPoll, PollCap: testCap, FastFor: time.Minute, QuietPoll: time.Second}
	d, _, _ := rateRig(t, opt, true)
	d.mu.Lock()
	delete(d.sessions, "bbbbbb")
	d.mu.Unlock()
	time.Sleep(1200 * time.Millisecond) // the poll that was waiting for Poll
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: d.device + "/" + proto.CLISession,
		At: time.Now().UTC(), To: "*", Subject: "from the CLI"}
	if err := d.appendEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if rate := pollsPerSecond(d, 2400*time.Millisecond); rate > 1.5 {
		t.Errorf("polled %.1f times a second with no session, want about 1 (QuietPoll is %v)", rate, opt.QuietPoll)
	}
}

// A poll that fails waits longer each time, even while the conversation is fresh.
func TestPollFailureStillBacksOff(t *testing.T) {
	// Not parallel: it counts polls over real time, which other tests' load would skew.
	opt := Options{Poll: testPoll, PollCap: testCap, FastFor: time.Minute, QuietPoll: time.Hour}
	d, _, clone := rateRig(t, opt, true)
	d.mu.Lock()
	d.noteConversationLocked()
	d.mu.Unlock()
	healthy := pollsPerSecond(d, time.Second)
	runGit(t, clone, "config", "remote.origin.url", filepath.Join(clone, "no-such-remote.git"))
	time.Sleep(500 * time.Millisecond)
	if failing := pollsPerSecond(d, 2*time.Second); failing > healthy/2 {
		t.Errorf("polled %.1f times a second while failing, against %.1f when it worked; want it much less", failing, healthy)
	}
}

// A conversation that moves while a failing poll waits doesn't end the wait: the remote is down or
// limiting, and the faster rate is for when it answers.
func TestConversationDuringAFailureKeepsTheBackoff(t *testing.T) {
	// Not parallel: it counts polls over real time, which other tests' load would skew.
	opt := Options{Poll: testPoll, PollCap: testCap, FastFor: time.Minute, QuietPoll: time.Hour}
	d, _, clone := rateRig(t, opt, true)
	d.mu.Lock()
	d.convAt = time.Now().Add(-2 * time.Minute) // FastFor is over
	d.mu.Unlock()
	time.Sleep(2 * time.Second) // up to the cap
	before := d.polls.Load()
	runGit(t, clone, "config", "remote.origin.url", filepath.Join(clone, "no-such-remote.git"))
	within(t, "three failed polls", 15*time.Second, func() bool { return d.polls.Load()-before >= 3 })
	time.Sleep(300 * time.Millisecond) // the third has failed, and the loop is waiting out the next
	polled := d.polls.Load()
	d.mu.Lock()
	d.noteConversationLocked()
	d.mu.Unlock()
	time.Sleep(time.Second)
	if n := d.polls.Load() - polled; n != 0 {
		t.Errorf("%d polls within a second of a conversation moving, during a failure's back-off", n)
	}
}

// Messages kept for a conversation that reach its live session when it takes the name move the
// conversation too, as one received does.
func TestAdoptingAKeptInboxMovesTheConversation(t *testing.T) {
	t.Parallel()
	d := rateDaemon(t)
	d.opt.StoppedInbox = time.Hour
	d.st.SessionNames = map[string]string{"aaaaaa": "quiet-otter"}
	d.st.Stopped["aaaaaa"] = time.Now().Add(-time.Minute)
	e := bus.Event{V: bus.Version, ID: bus.NewID(), Type: bus.Message, From: "peer-0001/aaaaaa", To: "here-0001/aaaaaa", Subject: "hi"}
	d.st.Inbox["aaaaaa"] = []*inMsg{{Event: e, Arrived: time.Now()}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.adoptLocked(d.sessions["bbbbbb"], "quiet-otter") {
		t.Fatal("the kept inbox was not taken")
	}
	if d.convAt.IsZero() {
		t.Errorf("taking a kept inbox did not move the conversation")
	}
}
