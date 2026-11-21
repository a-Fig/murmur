package daemon

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3db"
)

// Internal tests of the hookNative -> t3db.ThreadOf fallback (#886): PR #885 name-push / pool
// invariants, bounded retries without log spam, lock-free SQLite reads with disconnect / Native
// arrival races, two live sessions sharing a thread, and ThreadState-before-push ordering.

func hookFallbackDaemon(t *testing.T) (*Daemon, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	d := threadsDaemon(t)
	d.log = log.New(&buf, "", 0)
	d.nameKick = make(chan struct{}, 1)
	d.threadKick = make(chan struct{}, 1)
	d.pushWarned = map[string]bool{}
	d.st.T3Routes = map[string]t3Route{}
	d.st.NameKeys = map[string]string{}
	d.st.NameHolders = map[string]string{}
	d.st.SessionNames = map[string]string{}
	d.st.NamesSeen = map[string]time.Time{}
	d.st.Used = map[string]string{}
	d.st.Stopped = map[string]time.Time{}
	d.st.Inbox = map[string][]*inMsg{}
	d.st.Commits = map[string][]commitRec{}
	d.opt.ThreadState = func(context.Context, string, string, string) (t3db.State, error) {
		return t3db.State{LastRun: time.Now()}, nil
	}
	return d, &buf
}

// A session that resolves its T3 thread via hookNative while a name push (that also refills the
// pool) is in flight takes the pushed name on both keys and spends no second name from the pool
// (#885 / #886).
func TestHookNativeFallbackRekeysDuringInFlightNamePush(t *testing.T) {
	t.Parallel()
	d, _ := namedDaemon(t)
	d.opt.NamePool = 3
	d.opt.TitleEvery = 0
	d.opt.ThreadOf = func(_ context.Context, _, _, native string) (string, error) {
		if native == "conv-push-1" {
			return "thread-agy-push", nil
		}
		return "", t3db.ErrNotFound
	}
	d.opt.ThreadState = func(context.Context, string, string, string) (t3db.State, error) {
		return t3db.State{LastRun: time.Now()}, nil
	}

	d.mu.Lock()
	s := &session{
		id:        "xxxxxx",
		agent:     "antigravity in T3",
		key:       "session:xxxxxx",
		clientPID: 800,
		route:     proto.Route{T3CLI: "/opt/t3/bin/t3"},
	}
	d.sessions[s.id] = s
	d.mu.Unlock()

	w := d.nextNames()
	if len(w.wants) != 1 || w.reserve != 3 {
		t.Fatalf("nextNames: wants=%+v reserve=%d; want 1 want and 3 reserve", w.wants, w.reserve)
	}

	// Hook arrives while pushNames is in flight, and refreshThreads resolves the T3 thread.
	d.mu.Lock()
	d.hookSessionsLocked(proto.Request{Agent: "antigravity", ClaudeSession: "conv-push-1", Ancestors: []int{801, 800}})
	d.mu.Unlock()
	d.refreshThreads(context.Background())

	if err := d.pushNames(context.Background(), w); err != nil {
		t.Fatal(err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if s.name == "" {
		t.Fatal("session has no name after push landed")
	}
	if s.route.T3Thread != "thread-agy-push" || s.key != "t3:thread-agy-push" {
		t.Errorf("route=%+v key=%q; want thread-agy-push", s.route, s.key)
	}
	if got := d.st.NameKeys["session:xxxxxx"]; got != s.name {
		t.Errorf("session key has %q, want %q", got, s.name)
	}
	if got := d.st.NameKeys["t3:thread-agy-push"]; got != s.name {
		t.Errorf("t3 key has %q, want %q", got, s.name)
	}
	if len(d.st.NamePool) != 3 {
		t.Errorf("pool has %d names after landing, want 3 (no second name taken)", len(d.st.NamePool))
	}
}

// Retries are bounded at hookThreadMaxTries, ErrNotFound produces no log spam, real errors are
// logged once without leaking native IDs or T3 paths, and backoff respects TitleEvery.
func TestHookNativeFallbackBoundedRetriesAndSingleLog(t *testing.T) {
	t.Parallel()
	t.Run("ErrNotFound stops after max tries with no log", func(t *testing.T) {
		t.Parallel()
		d, buf := hookFallbackDaemon(t)
		calls := 0
		d.opt.ThreadOf = func(context.Context, string, string, string) (string, error) {
			calls++
			return "", t3db.ErrNotFound
		}
		d.sessions["aaaaaa"] = &session{
			id: "aaaaaa", agent: "antigravity in T3", key: "session:aaaaaa",
			route: proto.Route{T3CLI: "/t3", T3Home: "/secret/t3home"}, hookNative: "secret-native-1",
		}
		for range hookThreadMaxTries + 5 {
			d.refreshThreads(context.Background())
		}
		if calls != hookThreadMaxTries {
			t.Errorf("ThreadOf called %d times, want %d", calls, hookThreadMaxTries)
		}
		if buf.Len() != 0 {
			t.Errorf("unexpected log on ErrNotFound: %q", buf.String())
		}
	})

	t.Run("non-ErrNotFound error logs once and stops after max tries", func(t *testing.T) {
		t.Parallel()
		d, buf := hookFallbackDaemon(t)
		calls := 0
		d.opt.ThreadOf = func(context.Context, string, string, string) (string, error) {
			calls++
			return "", errors.New("database is locked")
		}
		d.sessions["aaaaaa"] = &session{
			id: "aaaaaa", agent: "antigravity in T3", key: "session:aaaaaa",
			route: proto.Route{T3CLI: "/t3", T3Home: "/secret/t3home"}, hookNative: "secret-native-2",
		}
		for range hookThreadMaxTries + 5 {
			d.refreshThreads(context.Background())
		}
		if calls != hookThreadMaxTries {
			t.Errorf("ThreadOf called %d times, want %d", calls, hookThreadMaxTries)
		}
		logged := buf.String()
		if n := strings.Count(logged, "can't find the T3 thread"); n != 1 {
			t.Errorf("logged %d times, want 1:\n%s", n, logged)
		}
		for _, secret := range []string{"secret-native-2", "/secret/t3home"} {
			if strings.Contains(logged, secret) {
				t.Errorf("log leaked %q:\n%s", secret, logged)
			}
		}
	})

	t.Run("backoff pauses between retries when TitleEvery is set", func(t *testing.T) {
		t.Parallel()
		d, _ := hookFallbackDaemon(t)
		d.opt.TitleEvery = time.Hour
		calls := 0
		d.opt.ThreadOf = func(context.Context, string, string, string) (string, error) {
			calls++
			return "", t3db.ErrNotFound
		}
		d.sessions["aaaaaa"] = &session{
			id: "aaaaaa", agent: "antigravity in T3", key: "session:aaaaaa",
			route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-backoff",
		}
		d.refreshThreads(context.Background())
		d.refreshThreads(context.Background())
		if calls != 1 {
			t.Errorf("ThreadOf called %d times within backoff window, want 1", calls)
		}
	})
}

// ThreadOf runs without holding d.mu, and a session that disconnects (deleted, dropped into grace,
// or replaced at the same ID) while ThreadOf is in flight is not mutated when ThreadOf returns.
func TestHookNativeFallbackDisconnectRaces(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(d *Daemon, s *session)
	}{
		{"deleted", func(d *Daemon, s *session) { delete(d.sessions, s.id) }},
		{"dropped into grace", func(d *Daemon, s *session) { s.dropped = time.Now() }},
		{"replaced by new session", func(d *Daemon, s *session) {
			d.sessions[s.id] = &session{id: s.id, agent: "antigravity in T3", key: "session:" + s.id,
				route: proto.Route{T3CLI: "/t3"}}
		}},
		{"grace takeover changed clientPID", func(d *Daemon, s *session) {
			s.clientPID = 999
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, _ := hookFallbackDaemon(t)
			orig := &session{
				id: "aaaaaa", agent: "antigravity in T3", key: "session:aaaaaa", name: "calm-heron",
				route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-race",
			}
			d.sessions["aaaaaa"] = orig
			entered := make(chan struct{})
			release := make(chan struct{})
			d.opt.ThreadOf = func(context.Context, string, string, string) (string, error) {
				close(entered)
				<-release
				return "thread-stale", nil
			}

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.refreshThreads(context.Background())
			}()

			<-entered
			// Acquiring d.mu here verifies ThreadOf is never called with d.mu held.
			d.mu.Lock()
			tc.mutate(d, orig)
			d.mu.Unlock()
			close(release)
			wg.Wait()

			d.mu.Lock()
			defer d.mu.Unlock()
			if orig.route.T3Thread != "" {
				t.Errorf("orig got T3Thread=%q after %s", orig.route.T3Thread, tc.name)
			}
			if cur := d.sessions["aaaaaa"]; cur != nil && cur.route.T3Thread != "" {
				t.Errorf("current session got stale T3Thread=%q after %s", cur.route.T3Thread, tc.name)
			}
			if _, ok := d.st.T3Routes["aaaaaa"]; ok {
				t.Errorf("T3Routes polluted after %s: %+v", tc.name, d.st.T3Routes)
			}
			if _, ok := d.st.NameKeys["t3:thread-stale"]; ok {
				t.Errorf("NameKeys polluted after %s: %+v", tc.name, d.st.NameKeys)
			}
		})
	}
}

// If the MCP server learns Native (e.g. Codex _meta.threadId) or T3Thread via OpRoute while a
// hookNative lookup is in flight, the authoritative route wins and the stale hook result is dropped.
func TestHookNativeFallbackNativeArrivesDuringLookup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		route      proto.Route
		wantThread string
	}{
		{"Native arrives", proto.Route{T3CLI: "/t3", Native: "codex-real"}, ""},
		{"T3Thread arrives", proto.Route{T3CLI: "/t3", Native: "codex-real", T3Thread: "thread-real"}, "thread-real"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, _ := hookFallbackDaemon(t)
			s := &session{
				id: "aaaaaa", agent: "antigravity in T3", key: "session:aaaaaa", name: "brisk-finch",
				route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-wrong",
			}
			d.sessions["aaaaaa"] = s
			entered := make(chan struct{})
			release := make(chan struct{})
			d.opt.ThreadOf = func(context.Context, string, string, string) (string, error) {
				close(entered)
				<-release
				return "thread-wrong", nil
			}

			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.refreshThreads(context.Background())
			}()

			<-entered
			d.mu.Lock()
			d.setRouteLocked(s, tc.route)
			d.mu.Unlock()
			close(release)
			wg.Wait()

			d.mu.Lock()
			defer d.mu.Unlock()
			if s.route.T3Thread != tc.wantThread {
				t.Errorf("T3Thread = %q, want %q", s.route.T3Thread, tc.wantThread)
			}
		})
	}
}

// Two live sessions resolving to the same T3 thread keep distinct keys and distinct names, and the
// first live session keeps its route.
func TestHookNativeFallbackTwoSessionsShareThread(t *testing.T) {
	t.Parallel()
	d, _ := hookFallbackDaemon(t)
	d.opt.ThreadOf = func(_ context.Context, _, _, native string) (string, error) {
		if native == "conv-second" {
			return "thread-shared", nil
		}
		return "", t3db.ErrNotFound
	}
	s1 := &session{
		id: "aaaaaa", agent: "antigravity in T3", key: "t3:thread-shared", name: "quiet-otter",
		clientPID: 700, route: proto.Route{T3CLI: "/t3", T3Thread: "thread-shared"},
	}
	s2 := &session{
		id: "bbbbbb", agent: "antigravity in T3", key: "session:bbbbbb", name: "bold-lark",
		clientPID: 700, route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-second",
	}
	d.sessions[s1.id] = s1
	d.sessions[s2.id] = s2
	d.st.NameKeys["t3:thread-shared"] = "quiet-otter"
	d.st.NameKeys["session:bbbbbb"] = "bold-lark"
	d.st.T3Routes[s1.id] = t3Route{CLI: "/t3", Thread: "thread-shared"}

	d.refreshThreads(context.Background())

	d.mu.Lock()
	defer d.mu.Unlock()
	if s1.name != "quiet-otter" || s1.key != "t3:thread-shared" || s1.route.T3Thread != "thread-shared" {
		t.Errorf("s1 altered: name=%q key=%q route=%+v", s1.name, s1.key, s1.route)
	}
	if s2.route.T3Thread != "thread-shared" || s2.key != "t3:thread-shared/bbbbbb" || s2.name != "bold-lark" {
		t.Errorf("s2: name=%q key=%q route=%+v; want bold-lark on unique key", s2.name, s2.key, s2.route)
	}
	if d.st.T3Routes[s1.id].Thread != "thread-shared" {
		t.Errorf("s1's live route was retired: %+v", d.st.T3Routes)
	}
}

// When hookNative resolves to a delegate thread, refreshThreads reads its ThreadState before
// applying the route so the delegate is immediately off the mesh and its early inbox is cleared in
// the same pass.
func TestHookNativeFallbackDelegateStateAppliedInSamePass(t *testing.T) {
	t.Parallel()
	d, _ := hookFallbackDaemon(t)
	d.opt.ThreadOf = func(_ context.Context, _, _, native string) (string, error) {
		if native == "conv-kid" {
			return "thread-kid", nil
		}
		return "", t3db.ErrNotFound
	}
	d.opt.ThreadState = func(_ context.Context, _, _, thread string) (t3db.State, error) {
		if thread == "thread-kid" {
			return t3db.State{Parent: "thread-boss", Busy: true}, nil
		}
		return t3db.State{}, nil
	}
	d.sessions["parent"] = &session{
		id: "parent", agent: "antigravity in T3", key: "t3:thread-boss", name: "calm-heron",
		route: proto.Route{T3CLI: "/t3", T3Thread: "thread-boss"},
	}
	d.sessions["kidkid"] = &session{
		id: "kidkid", agent: "antigravity in T3", key: "session:kidkid", name: "tiny-wren",
		route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-kid", hooked: true, busyAt: time.Now(),
	}
	d.st.Inbox["kidkid"] = []*inMsg{{Event: bus.Event{ID: "early", To: "*"}, Arrived: time.Now()}}

	d.refreshThreads(context.Background())

	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.offMeshLocked("kidkid") {
		t.Error("fallback delegate is still on the mesh after refreshThreads")
	}
	if len(d.st.Inbox["kidkid"]) != 0 {
		t.Errorf("fallback delegate kept %d inbox messages", len(d.st.Inbox["kidkid"]))
	}
	if parent, child := d.parentOfLocked("kidkid"); !child || parent != "parent" {
		t.Errorf("parentOfLocked = (%q, %v), want (parent, true)", parent, child)
	}
}

// Only Antigravity sessions use the hookNative -> ThreadOf fallback (preventing Codex's shared
// app-server PID from binding the wrong thread), and a failed ThreadState read holds the resolved
// route back until state is cached so a delegate or settled thread cannot be woken without state.
func TestHookNativeFallbackSkipsNonAntigravityAndHoldsFailedState(t *testing.T) {
	t.Parallel()
	d, _ := hookFallbackDaemon(t)
	var lookups []string
	d.opt.ThreadOf = func(_ context.Context, _, _, native string) (string, error) {
		lookups = append(lookups, native)
		return "thread-" + native, nil
	}
	failState := true
	d.opt.ThreadState = func(_ context.Context, _, _, thread string) (t3db.State, error) {
		if failState {
			return t3db.State{}, errors.New("sqlite busy")
		}
		return t3db.State{Parent: "thread-parent"}, nil
	}
	d.sessions["codex1"] = &session{
		id: "codex1", agent: "codex in T3", key: "session:codex1", name: "gray-fox",
		clientPID: 500, route: proto.Route{T3CLI: "/t3"}, hookNative: "codex-other-thread",
	}
	d.sessions["agydel"] = &session{
		id: "agydel", agent: "antigravity in T3", key: "session:agydel", name: "tiny-owl",
		clientPID: 600, route: proto.Route{T3CLI: "/t3"}, hookNative: "conv-del",
	}

	d.refreshThreads(context.Background())

	d.mu.Lock()
	if len(lookups) != 1 || lookups[0] != "conv-del" {
		t.Errorf("ThreadOf lookups = %v, want [conv-del] only (Codex must be skipped)", lookups)
	}
	if d.sessions["codex1"].route.T3Thread != "" {
		t.Errorf("codex1 got T3Thread=%q, want empty", d.sessions["codex1"].route.T3Thread)
	}
	if d.sessions["agydel"].route.T3Thread != "" {
		t.Errorf("agydel exposed T3Thread=%q before ThreadState succeeded", d.sessions["agydel"].route.T3Thread)
	}
	d.mu.Unlock()

	failState = false
	d.refreshThreads(context.Background())

	d.mu.Lock()
	defer d.mu.Unlock()
	if got := d.sessions["agydel"].route.T3Thread; got != "thread-conv-del" {
		t.Errorf("agydel T3Thread = %q after state succeeded, want thread-conv-del", got)
	}
	if !d.offMeshLocked("agydel") {
		t.Error("agydel delegate is still on the mesh after ThreadState succeeded")
	}
}
