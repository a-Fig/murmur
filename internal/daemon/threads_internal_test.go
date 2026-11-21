package daemon

import (
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
	"github.com/a-Fig/murmur/internal/web"
)

// The wake setting's "within N minutes" (#25) and settled T3 threads (#189), as the daemon decides
// them from what T3's database said.

func threadsDaemon(t *testing.T) *Daemon {
	d := idleDaemon(nil)
	d.dir = t.TempDir() // state.json, where a test saves
	d.device = "dev"
	d.log = log.New(io.Discard, "", 0)
	d.pushedAt = map[string]time.Time{}
	d.threads = map[threadKey]t3db.State{}
	d.threadRead = map[threadKey]time.Time{}
	d.threadWarned = map[threadKey]bool{}
	d.peers = map[string]*peer{}
	d.handedTo, d.handing = map[threadKey][]handed{}, map[handing]bool{}
	d.kick, d.pushKick = make(chan struct{}, 1), make(chan struct{}, 1)
	return d
}

func TestAntigravityT3HookLookupRetriesAndLeavesANewerRouteAlone(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	d.bgCtx = ctx
	t.Cleanup(func() { cancel(); d.bg.Wait() })

	route := proto.Route{T3CLI: "/t3/bin/t3", T3Home: "/t3"}
	s := &session{id: "aaaaaa", agent: "antigravity in T3", clientPID: 100, route: route}
	d.sessions[s.id] = s
	var calls atomic.Int32
	retrying, release := make(chan struct{}), make(chan struct{})
	d.opt.ThreadOf = func(ctx context.Context, cli, home, native string) (string, error) {
		if cli != route.T3CLI || home != route.T3Home || native != "agy-conversation" {
			t.Errorf("looked up %q in %q via %q", native, home, cli)
		}
		switch calls.Add(1) {
		case 1:
			return "", t3db.ErrNotFound
		case 2:
			close(retrying)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "old-thread", nil
	}
	hook := proto.Request{Agent: "antigravity", ClaudeSession: "agy-conversation", Ancestors: []int{100}}
	d.mu.Lock()
	d.agentHookSessionsLocked(hook)
	d.mu.Unlock()
	select {
	case <-retrying:
	case <-ctx.Done():
		t.Fatal("the lookup did not retry after a missing row")
	}
	if !d.mu.TryLock() {
		t.Fatal("the lookup held the daemon mutex while reading T3's database")
	}
	d.agentHookSessionsLocked(hook)
	d.agentHookSessionsLocked(hook)
	s.route.T3Thread = "new-thread"
	d.mu.Unlock()
	close(release)
	d.bg.Wait()
	if got := calls.Load(); got != 2 {
		t.Errorf("lookups = %d, want one attempt and its retry", got)
	}
	if got := s.route.T3Thread; got != "new-thread" {
		t.Errorf("the hook's lookup replaced the newer route with %q", got)
	}
}

// inT3 registers session id as running in T3 thread "thread-<id>", last at work ago.
func inT3(d *Daemon, id string, ago time.Duration) {
	at := time.Now().Add(-ago)
	d.sessions[id] = &session{id: id, name: "name-" + id, started: at, lastActive: at,
		route: proto.Route{T3CLI: "/t3", T3Thread: "thread-" + id}}
}

func wakeWithin(minutes int) pageSettings {
	return pageSettings{Wake: web.Wake{When: web.WakeWithin, Minutes: minutes}}
}

func TestWithinGoesByT3sRunsWhereKnown(t *testing.T) {
	t.Parallel()
	ago := func(d time.Duration) time.Time { return time.Now().Add(-d) }
	for _, tc := range []struct {
		name  string
		state *t3db.State // nil: nothing read yet
		ran   time.Duration
		wake  pageSettings
		want  bool
	}{
		{"idle 4 minutes, within the default 5", &t3db.State{LastRun: ago(4 * time.Minute)}, 0, wakeWithin(0), true},
		{"idle 6 minutes, past the default 5", &t3db.State{LastRun: ago(6 * time.Minute)}, 0, wakeWithin(0), false},
		{"idle 5 minutes, within 10", &t3db.State{LastRun: ago(5 * time.Minute)}, 0, wakeWithin(10), true},
		{"busy, however long since the last run", &t3db.State{Busy: true, LastRun: ago(time.Hour)}, 0, wakeWithin(0), true},
		// The session's own times say nothing once T3 has spoken: its MCP server may have started
		// lately for a thread long idle.
		{"T3 says idle though the session started lately", &t3db.State{LastRun: ago(time.Hour)}, 0, wakeWithin(0), false},
		{"not read yet: the session's own times", nil, time.Minute, wakeWithin(0), true},
		{"not read yet, and the session long idle", nil, time.Hour, wakeWithin(0), false},
		{"always, idle an hour", &t3db.State{LastRun: ago(time.Hour)}, 0, pageSettings{Wake: web.Wake{When: web.WakeAlways}}, true},
		{"never, idle a minute", &t3db.State{LastRun: ago(time.Minute)}, 0, pageSettings{Wake: web.Wake{When: web.WakeNever}}, false},
		{"an unset choice is within", &t3db.State{LastRun: ago(6 * time.Minute)}, 0, pageSettings{}, false},
		{"settled, under always", &t3db.State{Settled: true, LastRun: ago(time.Second)}, 0, pageSettings{Wake: web.Wake{When: web.WakeAlways}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := threadsDaemon(t)
			inT3(d, "aaaaaa", tc.ran)
			if tc.state != nil {
				d.threads[threadKey{"", "thread-aaaaaa"}] = *tc.state
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if got := d.mayWakeLocked("aaaaaa", tc.wake); got != tc.want {
				t.Errorf("mayWakeLocked = %v, want %v", got, tc.want)
			}
		})
	}
}

// A session's own setting overrides the device's, by name.
func TestWithinTakesTheSessionsOwnSetting(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{LastRun: time.Now().Add(-20 * time.Minute)}
	wake := wakeWithin(0)
	wake.SessionWake = map[string]web.Wake{"name-aaaaaa": {When: web.WakeWithin, Minutes: 30}}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.mayWakeLocked("aaaaaa", wake) {
		t.Errorf("idle 20 minutes, with its own 30: not woken")
	}
	wake.SessionWake = nil
	if d.mayWakeLocked("aaaaaa", wake) {
		t.Errorf("idle 20 minutes, with the device's 5: woken")
	}
}

// A message just handed over as a turn counts as work: a second one right behind it goes in too,
// before T3's database shows the run the first one started.
func TestATurnHandedOverCountsAsWork(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{LastRun: time.Now().Add(-2*time.Minute - 59*time.Second)}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pushedAt["aaaaaa"] = time.Now()
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{LastRun: time.Now().Add(-10 * time.Minute)}
	if !d.mayWakeLocked("aaaaaa", wakeWithin(0)) {
		t.Errorf("just handed a message: not woken")
	}
}

// A stopped session in T3 whose thread hasn't been read goes by when it stopped.
func TestWithinForAStoppedThreadNotReadYet(t *testing.T) {
	t.Parallel()
	d := idleDaemon(map[string]time.Duration{"aaaaaa": time.Minute, "bbbbbb": 10 * time.Minute})
	d.pushedAt = map[string]time.Time{}
	d.threads = map[threadKey]t3db.State{}
	d.threadRead = map[threadKey]time.Time{}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.mayWakeLocked("aaaaaa", wakeWithin(0)) {
		t.Errorf("stopped a minute ago: not woken")
	}
	if d.mayWakeLocked("bbbbbb", wakeWithin(0)) {
		t.Errorf("stopped 10 minutes ago: woken")
	}
	if !d.holdsRouteLocked(wakeWithin(0)) {
		t.Errorf("a thread it can still wake doesn't keep the daemon up")
	}
	delete(d.st.Stopped, "aaaaaa")
	if d.holdsRouteLocked(wakeWithin(0)) {
		t.Errorf("a thread idle past the setting keeps the daemon up")
	}
}

// refreshThreads reads each thread once, keeps what it read last while a read fails, and forgets
// a thread no session is in any more. Settled sessions leave presence, are listed as offline, and
// get nothing.
func TestSettledThreadsGoOffline(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	inT3(d, "bbbbbb", 0)
	d.sessions["cccccc"] = &session{id: "cccccc", name: "name-cccccc", started: time.Now()} // not in T3
	d.st.Inbox = map[string][]*inMsg{}
	reads := map[string]int{}
	fail := false
	d.opt.ThreadState = func(_ context.Context, cli, home, thread string) (t3db.State, error) {
		reads[thread]++
		if fail {
			return t3db.State{}, errors.New("locked")
		}
		return t3db.State{Settled: thread == "thread-aaaaaa", LastRun: time.Now()}, nil
	}
	d.refreshThreads(context.Background())
	if reads["thread-aaaaaa"] != 1 || reads["thread-bbbbbb"] != 1 || len(reads) != 2 {
		t.Fatalf("reads: %v", reads)
	}
	check := func(when string) {
		t.Helper()
		d.mu.Lock()
		defer d.mu.Unlock()
		if !d.offMeshLocked("aaaaaa") || d.offMeshLocked("bbbbbb") || d.offMeshLocked("cccccc") {
			t.Errorf("%s: settled a, b, c = %v %v %v", when, d.offMeshLocked("aaaaaa"), d.offMeshLocked("bbbbbb"), d.offMeshLocked("cccccc"))
		}
		if got := d.offlineIDsLocked(); len(got) != 1 || got[0] != "aaaaaa" {
			t.Errorf("%s: offline %q", when, got)
		}
		if !d.offlineLocked("dev", "aaaaaa") || d.offlineLocked("dev", "bbbbbb") {
			t.Errorf("%s: offlineLocked", when)
		}
		for _, s := range d.whoLocked("cccccc", tags.Filter{})[0].Sessions {
			if s.Name == "name-aaaaaa" {
				t.Errorf("%s: who lists the settled session", when)
			}
		}
		if d.mayWakeLocked("aaaaaa", pageSettings{Wake: web.Wake{When: web.WakeAlways}}) {
			t.Errorf("%s: a settled thread may be woken", when)
		}
		// A message for everyone reaches the others, not the settled one.
		d.st.Inbox = map[string][]*inMsg{}
		d.queueLocked(bus.Event{ID: "m", Type: bus.Message, From: "other/xxxxxx", To: "*", At: time.Now()}, modeNormal)
		if len(d.st.Inbox["aaaaaa"]) != 0 || len(d.st.Inbox["bbbbbb"]) != 1 {
			t.Errorf("%s: inboxes after a broadcast: %v", when, d.st.Inbox)
		}
	}
	check("after the read")
	fail = true
	d.refreshThreads(context.Background())
	check("after a read that failed")

	// Unsettled, it is back.
	fail = false
	d.opt.ThreadState = func(context.Context, string, string, string) (t3db.State, error) {
		return t3db.State{LastRun: time.Now()}, nil
	}
	d.refreshThreads(context.Background())
	d.mu.Lock()
	if d.offMeshLocked("aaaaaa") || len(d.offlineIDsLocked()) != 0 {
		t.Errorf("unsettled, still offline")
	}
	d.mu.Unlock()

	// A thread no session is in any more is forgotten.
	d.mu.Lock()
	delete(d.sessions, "aaaaaa")
	d.mu.Unlock()
	d.refreshThreads(context.Background())
	if _, ok := d.threads[threadKey{"", "thread-aaaaaa"}]; ok {
		t.Errorf("the thread of a session gone is still held")
	}
}

// Another device's presence says which of its sessions are offline; only while it is current.
func TestOfflineOnAnotherDevice(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.opt.StaleAfter = time.Hour
	d.peers["peer"] = &peer{pres: bus.Presence{Device: "peer", Offline: []string{"zzzzzz"}}, lastChange: time.Now()}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.offlineLocked("peer", "zzzzzz") || d.offlineLocked("peer", "yyyyyy") || d.offlineLocked("nobody", "zzzzzz") {
		t.Errorf("offlineLocked on a peer")
	}
}

// A name's earlier ID, which another device may still send to, is offline with the settled
// session that holds the name now.
func TestOfflineListsForwardedIDs(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{Settled: true}
	d.st.SessionNames["oldold"] = "name-aaaaaa"
	d.st.SessionNames["aaaaaa"] = "name-aaaaaa"
	d.st.NameHolders = map[string]string{"name-aaaaaa": "aaaaaa"}
	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.offlineIDsLocked(); len(got) != 2 || got[0] != "aaaaaa" || got[1] != "oldold" {
		t.Errorf("offline: %q", got)
	}
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{}
	if got := d.offlineIDsLocked(); len(got) != 0 {
		t.Errorf("unsettled, offline: %q", got)
	}
}

// What a restarted daemon holds for sessions reconnecting isn't given to a settled one.
func TestCatchUpSkipsASettledSession(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.started = time.Now()
	d.caughtUp = map[string]bool{}
	d.st.Inbox = map[string][]*inMsg{}
	inT3(d, "aaaaaa", 0)
	inT3(d, "bbbbbb", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{Settled: true}
	d.pending = []bus.Event{{ID: "m", Type: bus.Message, From: "other/xxxxxx", To: "*", At: time.Now()}}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.catchUpLocked("aaaaaa")
	d.catchUpLocked("bbbbbb")
	if len(d.st.Inbox["aaaaaa"]) != 0 || len(d.st.Inbox["bbbbbb"]) != 1 {
		t.Errorf("inboxes after catch-up: %v", d.st.Inbox)
	}
}

// A session at work again lets the handoffs look at what was held back while it was idle.
func TestActivityLetsHeldMessagesGo(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.sessions["aaaaaa"] = &session{id: "aaaaaa", started: time.Now().Add(-time.Hour)}
	d.st.Inbox = map[string][]*inMsg{}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touchLocked(d.sessions["aaaaaa"], time.Now())
	select {
	case <-d.pushKick:
		t.Errorf("kicked with nothing unread")
	default:
	}
	d.sessions["aaaaaa"].lastActive = time.Now().Add(-time.Hour)
	d.st.Inbox["aaaaaa"] = []*inMsg{{Event: bus.Event{ID: "m"}, Arrived: time.Now()}}
	if d.mayWakeLocked("aaaaaa", wakeWithin(0)) {
		t.Fatalf("idle an hour: woken")
	}
	d.touchLocked(d.sessions["aaaaaa"], time.Now())
	select {
	case <-d.pushKick:
	default:
		t.Errorf("not kicked")
	}
	if !d.mayWakeLocked("aaaaaa", wakeWithin(0)) {
		t.Errorf("at work again: not woken")
	}
}

// A job picked before its thread settled is not handed over: the check is made again at the
// handoff, and the message stays in the inbox, neither failed nor marked as handed over.
func TestHandoffChecksAgain(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "aaaaaa", 0)
	d.threads[threadKey{"", "thread-aaaaaa"}] = t3db.State{Settled: true}
	m := &inMsg{Event: bus.Event{ID: "m"}, Arrived: time.Now(), Pushing: true}
	d.st.Inbox = map[string][]*inMsg{"aaaaaa": {m}}
	j := &pushJob{id: "aaaaaa", addr: "dev/aaaaaa", msgs: []*inMsg{m}, via: viaT3,
		route: proto.Route{T3CLI: "/no/such/t3", T3Thread: "thread-aaaaaa"}}
	err := d.sendTurn(context.Background(), j)
	if !errors.Is(err, errNotNow) {
		t.Fatalf("sendTurn = %v, want errNotNow", err)
	}
	d.finishPush(j, err)
	if m.Pushing || m.PushFailed || m.Attempted || len(d.st.Inbox["aaaaaa"]) != 1 {
		t.Errorf("the message after: %+v, inbox %d", m, len(d.st.Inbox["aaaaaa"]))
	}
}

// Two sessions of one T3 thread with a copy of the same message (#99): while one copy is on its
// way in, the other waits, and the hook leaves it alone though its session is at work. Once the
// handoff is over, the record of it is gone whatever the outcome: a copy handed over takes the
// other out (#470's record, for #70), and one that failed or was refused lets the other go in.
func TestASecondCopyWaitsForTheHandoff(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
	}{{"handed over", nil}, {"failed", errors.New("t3 failed")}, {"not now", errNotNow}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := threadsDaemon(t)
			d.handing, d.handedTo = map[handing]bool{}, map[threadKey][]handed{}
			d.introduced, d.pushWarned, d.pushing = map[string]map[string]bool{}, map[string]bool{}, map[string]bool{}
			for _, id := range []string{"aaaaaa", "bbbbbb"} {
				inT3(d, id, 0)
				d.sessions[id].route.T3Thread = "thread-one"
			}
			ev := bus.Event{V: bus.Version, ID: "ev1", Type: bus.Message, From: "peer/cccccc", To: "*", Body: "b", At: time.Now()}
			mb := &inMsg{Event: ev, Arrived: time.Now()}
			d.st.Inbox = map[string][]*inMsg{"aaaaaa": {{Event: ev, Arrived: time.Now()}}, "bbbbbb": {mb}}
			wake := wakeWithin(0)

			d.mu.Lock()
			ja := d.nextPushLocked("aaaaaa", wake)
			jb := d.nextPushLocked("bbbbbb", wake)
			// bbbbbb's turn starts, and its hook runs, before aaaaaa's handoff returns (#611).
			k, b := threadKey{"", "thread-one"}, d.sessions["bbbbbb"]
			b.hooked, b.busyAt, d.threads[k] = true, time.Now(), t3db.State{Busy: true}
			queued := d.queuedForPushLocked("bbbbbb", mb, wake)
			b.hooked, b.busyAt = false, time.Time{}
			delete(d.threads, k)
			d.mu.Unlock()
			if ja == nil || jb != nil {
				t.Fatalf("jobs taken: aaaaaa %v, bbbbbb %v; want only aaaaaa's", ja != nil, jb != nil)
			}
			if !queued {
				t.Errorf("the hook may tell bbbbbb of the copy on its way in through aaaaaa")
			}

			d.finishPush(ja, tc.err)
			d.mu.Lock()
			defer d.mu.Unlock()
			if len(d.handing) != 0 {
				t.Errorf("still on its way in after the handoff: %v", d.handing)
			}
			next, _, _ := d.candidateLocked("bbbbbb", wake)
			if tc.err == nil {
				if len(d.st.Inbox["bbbbbb"]) != 0 || next != nil {
					t.Errorf("bbbbbb's copy kept after aaaaaa's was handed over")
				}
				return
			}
			if next != mb || mb.PushFailed {
				t.Errorf("bbbbbb's copy not offered after aaaaaa's %v: offered %v, failed %v", tc.err, next != nil, mb.PushFailed)
			}
		})
	}
}

// lookup knows a session another device lists as offline.
func TestLookupKnowsAnOfflinePeerSession(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	d.peers["peer"] = &peer{pres: bus.Presence{Device: "peer", Offline: []string{"zzzzzz"}}, lastChange: time.Now()}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.sessionKnownLocked("peer", "zzzzzz") {
		t.Errorf("an offline session on a peer is unknown")
	}
	if !d.detailLocked("peer/zzzzzz", "", 0).Offline {
		t.Errorf("lookup doesn't say it is settled")
	}
}

// A delegate (#188) is off the mesh, goes through its parent, and its commits are credited there.
func TestDelegateGoesThroughItsParent(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "parent", 0)
	inT3(d, "child1", 0)
	inT3(d, "child2", 0)
	d.st.Inbox = map[string][]*inMsg{
		"child1": {{Event: bus.Event{ID: "early", To: "*"}, Arrived: time.Now()}},
		"parent": {{Event: bus.Event{ID: "early", To: "*"}, Arrived: time.Now()}},
	}
	d.st.Commits = map[string][]commitRec{}
	d.opt.ThreadState = func(_ context.Context, _, _, thread string) (t3db.State, error) {
		switch thread {
		case "thread-child1":
			return t3db.State{Parent: "thread-parent"}, nil
		case "thread-child2":
			return t3db.State{Parent: "thread-not-here"}, nil
		}
		return t3db.State{}, nil
	}
	d.refreshThreads(context.Background())

	if why := d.childRefusal("child1"); !strings.Contains(why, "name-parent") {
		t.Errorf("the delegate's own send: %q", why)
	}
	if why := d.childRefusal("child2"); !strings.Contains(why, "the thread that started it") {
		t.Errorf("a delegate whose parent isn't here: %q", why)
	}
	if why := d.childRefusal("parent"); why != "" {
		t.Errorf("the parent may not send: %q", why)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.offMeshLocked("child1") || d.offMeshLocked("parent") {
		t.Errorf("off the mesh: child %v, parent %v", d.offMeshLocked("child1"), d.offMeshLocked("parent"))
	}
	if got := d.offlineIDsLocked(); len(got) != 2 || got[0] != "child1" || got[1] != "child2" {
		t.Errorf("offline: %q", got)
	}
	if why := d.offlineWhyLocked("dev", "child1", "name-child1"); !strings.Contains(why, "Message name-parent instead") {
		t.Errorf("a send to the delegate: %q", why)
	}
	if len(d.st.Inbox["child1"]) != 0 || len(d.st.Inbox["parent"]) != 1 {
		t.Errorf("inboxes once the delegate was found: %v", d.st.Inbox)
	}
	for _, s := range d.whoLocked("parent", tags.Filter{})[0].Sessions {
		if strings.HasPrefix(s.Name, "name-child") {
			t.Errorf("who lists %s", s.Name)
		}
	}
	got := d.creditedLocked([]*session{d.sessions["child1"], d.sessions["parent"], d.sessions["child2"]})
	if len(got) != 2 || got[0] != "parent" || got[1] != "child2" {
		t.Errorf("credited: %q", got)
	}
	if det := d.detailLocked("dev/child1", "", 0); !det.Offline || det.DelegateOf != "name-parent" {
		t.Errorf("lookup: offline %v, delegate of %q", det.Offline, det.DelegateOf)
	}
}

// From Sol's review of #188: a message on its way in when the delegate is found, a session that
// finds a thread already known to be a child, and commits made before the thread was read.
func TestDelegateLeftoversAreCleared(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "parent", 0)
	inT3(d, "child1", 0)
	d.st.Commits = map[string][]commitRec{
		"child1": {{SHA: "c1", Branch: "feature", At: time.Now().Add(-time.Minute)}},
		"parent": {{SHA: "p1", Branch: "main", At: time.Now().Add(-2 * time.Minute)}},
	}
	onItsWay := &inMsg{Event: bus.Event{ID: "pushing"}, Arrived: time.Now(), Pushing: true}
	d.st.Inbox = map[string][]*inMsg{"child1": {onItsWay, {Event: bus.Event{ID: "waiting"}, Arrived: time.Now()}}}
	d.opt.ThreadState = func(_ context.Context, _, _, thread string) (t3db.State, error) {
		if thread == "thread-child1" || thread == "thread-child2" {
			return t3db.State{Parent: "thread-parent"}, nil
		}
		return t3db.State{}, nil
	}
	d.refreshThreads(context.Background())

	d.mu.Lock()
	if box := d.st.Inbox["child1"]; len(box) != 1 || box[0] != onItsWay {
		t.Errorf("inbox with a message on its way in: %d messages", len(box))
	}
	if len(d.st.Commits["child1"]) != 0 || len(d.st.Commits["parent"]) != 2 || d.st.Commits["parent"][1].SHA != "c1" {
		t.Errorf("commits: %+v", d.st.Commits)
	}
	if got := d.committedLocked("parent"); len(got) != 2 || got[0] != "feature" {
		t.Errorf("the parent's branches: %q", got)
	}
	d.mu.Unlock()

	// The handoff ends refused, at the handoff: the message goes.
	j := &pushJob{id: "child1", msgs: []*inMsg{onItsWay}}
	d.finishPush(j, errNotNow)
	if len(d.st.Inbox["child1"]) != 0 {
		t.Errorf("after the refused handoff: %d messages", len(d.st.Inbox["child1"]))
	}

	// A new MCP server in the delegate's thread, which got a message before it found the thread.
	d.mu.Lock()
	d.sessions["child2"] = &session{id: "child2", started: time.Now()}
	d.st.Inbox["child2"] = []*inMsg{{Event: bus.Event{ID: "early"}, Arrived: time.Now()}}
	d.sessions["child2"].route = proto.Route{T3CLI: "/t3", T3Thread: "thread-child1"}
	d.mu.Unlock()
	resp := d.handle(context.Background(), proto.Request{Op: proto.OpInbox, Session: "child2"})
	if !resp.OK || len(resp.Messages) != 0 || len(d.st.Inbox["child2"]) != 0 {
		t.Errorf("the delegate's inbox: %+v, left %d", resp, len(d.st.Inbox["child2"]))
	}
}

// A handoff refused at the handoff, in an inbox another session took over while it was queued: the
// kick from the takeover found the message on its way, so the new owner needs another (Sol's review
// of #196). Neither a notice nor the next message would otherwise come for it soon.
func TestRefusedHandoffLetsTheNewOwnerGo(t *testing.T) {
	t.Parallel()
	d := threadsDaemon(t)
	inT3(d, "newnew", 0)
	m := &inMsg{Event: bus.Event{ID: "taken-over"}, Arrived: time.Now(), Pushing: true}
	d.st.Inbox = map[string][]*inMsg{"newnew": {m}}
	always := pageSettings{Wake: web.Wake{When: web.WakeAlways}}
	if got, _, _ := d.candidateLocked("newnew", always); got != nil {
		t.Fatal("a message on its way was picked again")
	}
	d.finishPush(&pushJob{id: "oldold", addr: "dev/oldold", msgs: []*inMsg{m}, via: viaT3}, errNotNow)
	if m.Pushing || m.PushFailed {
		t.Errorf("the message after the refusal: pushing=%v failed=%v", m.Pushing, m.PushFailed)
	}
	select {
	case <-d.pushKick:
	default:
		t.Error("no kick after the refusal")
	}
	if got, _, _ := d.candidateLocked("newnew", always); got != m {
		t.Errorf("the new owner's candidate: %v", got)
	}
}
