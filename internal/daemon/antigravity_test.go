package daemon_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
	"github.com/a-Fig/murmur/internal/t3db"
	"github.com/a-Fig/murmur/internal/tags"
)

// End-to-end tests of Antigravity sessions running inside T3 Code (#886):
//   - primary path (T3CODE_THREAD_ID at hello, ByThread=true): idle turns, T3 title, derived
//     model: tag, settled offline, delegate isolation, interrupted-turn closing, and stable
//     identity across restart;
//   - fallback path (older T3 without T3CODE_THREAD_ID): hookNative -> t3db.ThreadOf resolution
//     against a real SQLite T3 state database and against transient ErrNotFound.

// agyT3Fixture creates a T3 data directory whose statev2.sqlite has the three projection tables
// Murmur reads (threads, runs, provider_threads).
func agyT3Fixture(t *testing.T) (home string, db *sql.DB) {
	t.Helper()
	home = t.TempDir()
	db = openFixtureDB(t, t3.StateDB(home))
	for _, stmt := range []string{
		`CREATE TABLE orchestration_v2_projection_threads (
			thread_id TEXT PRIMARY KEY,
			title TEXT,
			payload_json TEXT,
			archived_at TEXT,
			deleted_at TEXT
		)`,
		`CREATE TABLE orchestration_v2_projection_runs (
			run_id TEXT PRIMARY KEY,
			thread_id TEXT,
			ordinal INTEGER,
			status TEXT,
			requested_at TEXT,
			completed_at TEXT
		)`,
		`CREATE TABLE orchestration_v2_projection_provider_threads (
			id TEXT PRIMARY KEY,
			thread_id TEXT,
			payload_json TEXT,
			updated_at TEXT
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return home, db
}

func insertT3Thread(t *testing.T, db *sql.DB, threadID, title, model, parent, settled string) {
	t.Helper()
	payload := map[string]any{}
	if model != "" {
		payload["modelSelection"] = map[string]any{"model": model}
	}
	if parent != "" {
		payload["lineage"] = map[string]any{"parentThreadId": parent, "relationshipToParent": "subagent"}
	}
	if settled != "" {
		payload["settledOverride"] = settled
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO orchestration_v2_projection_threads (thread_id, title, payload_json) VALUES (?, ?, ?)`,
		threadID, title, string(b)); err != nil {
		t.Fatal(err)
	}
}

func insertT3ProviderThread(t *testing.T, db *sql.DB, id, threadID, nativeID, updatedAt string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"appThreadId":     threadID,
		"nativeThreadRef": map[string]any{"nativeId": nativeID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO orchestration_v2_projection_provider_threads (id, thread_id, payload_json, updated_at) VALUES (?, ?, ?, ?)`,
		id, threadID, string(payload), updatedAt); err != nil {
		t.Fatal(err)
	}
}

func insertT3Run(t *testing.T, db *sql.DB, runID, threadID string, ordinal int, status, requestedAt, completedAt string) {
	t.Helper()
	var comp any
	if completedAt != "" {
		comp = completedAt
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO orchestration_v2_projection_runs (run_id, thread_id, ordinal, status, requested_at, completed_at) VALUES (?, ?, ?, ?, ?, ?)`,
		runID, threadID, ordinal, status, requestedAt, comp); err != nil {
		t.Fatal(err)
	}
}

func (d *device) agyHello(id string, pid int, r proto.Route, byThread bool) (string, net.Conn) {
	d.t.Helper()
	var conn net.Conn
	var resp proto.Response
	eventually(d.t, "antigravity hello "+id, func() bool {
		c, res, err := d.c.Hello(context.Background(), proto.Hello{
			ID: id, Agent: "antigravity in T3", ClientPID: pid, ByThread: byThread, Route: r,
		})
		if err != nil {
			return false
		}
		d.t.Cleanup(func() { c.Close() })
		conn, resp = c, res
		return true
	})
	return resp.Self, conn
}

func (d *device) hasTag(addr, want string) bool {
	entry, ok := d.whoEntry(addr)
	return ok && tags.Has(entry.Tags, want)
}

// An Antigravity thread in T3 with T3CODE_THREAD_ID gets the full T3 integration against a real
// statev2.sqlite: title, derived model: tag, idle turns via fake T3, interrupted-turn closing,
// stable identity across restart, settled offline, and delegate isolation (#886).
func TestAntigravityInT3FullLifecycle(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	home, db := agyT3Fixture(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertT3Thread(t, db, "thread-agy", "Fix Antigravity T3", "gemini-4-neon", "", "")
	insertT3Run(t, db, "run-1", "thread-agy", 1, "completed", now, now)
	insertT3Thread(t, db, "thread-agy-child", "Delegate helper", "gemini-4-neon", "thread-agy", "")
	insertT3Run(t, db, "run-c1", "thread-agy-child", 1, "completed", now, now)

	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 300 * time.Millisecond; d.keep = time.Hour })
	alice, _ := a.session("aaaaaa", "")
	route := proto.Route{T3CLI: exe, T3Home: home, T3Thread: "thread-agy"}
	agy, agyConn := b.agyHello("112233", 700, route, true)
	agyName := b.named(agy)

	eventually(t, "both devices see agy with title and derived model tag", func() bool {
		doingA, okA := a.doingIn("aaaaaa", agy)
		doingB, okB := b.doingIn("112233", agy)
		return b.seesSession(alice) && okA && okB &&
			doingA == "Fix Antigravity T3" && doingB == "Fix Antigravity T3" &&
			a.hasTag(agy, "model:gemini-4-neon") && b.hasTag(agy, "model:gemini-4-neon")
	})
	a.nameIs(agy, agyName)

	// Idle turn delivery via fake T3.
	a.send("aaaaaa", agyName, "wake antigravity", "please look")
	eventually(t, "idle turn handed over via fake T3", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-agy -" || c.Home != home || !strings.Contains(c.Stdin, "wake antigravity") {
		t.Fatalf("fake T3 call: %+v", c)
	}

	// Interrupted turn closed from T3's DB: PreInvocation marks busy while run-2 is running, a
	// message waits without queuing behind the run, then run-2 completes without a Stop hook.
	reqAt := time.Now().UTC().Format(time.RFC3339Nano)
	insertT3Run(t, db, "run-2", "thread-agy", 2, "running", reqAt, "")
	b.do(proto.Request{Op: proto.OpNotices, Agent: "antigravity", ClaudeSession: "conv-agy", Ancestors: []int{701, 700}})
	a.send("aaaaaa", agyName, "during interrupted turn", "held until interrupted")
	eventually(t, "message reached b while busy", func() bool { return logHas(b, "during interrupted turn") })
	holds(t, "no handoff while Antigravity turn is running", 600*time.Millisecond, func() bool { return len(calls()) == 1 })
	doneAt := time.Now().UTC().Format(time.RFC3339Nano)
	insertT3Run(t, db, "run-2", "thread-agy", 2, "completed", reqAt, doneAt)
	eventually(t, "interrupted turn closed and message handed over", func() bool { return len(calls()) == 2 })
	if !strings.Contains(calls()[1].Stdin, "during interrupted turn") {
		t.Errorf("second turn stdin: %q", calls()[1].Stdin)
	}

	// Delegate isolation: a child Antigravity thread leaves who on both devices and cannot send.
	childRoute := proto.Route{T3CLI: exe, T3Home: home, T3Thread: "thread-agy-child"}
	child, _ := b.agyHello("445566", 710, childRoute, true)
	eventually(t, "delegate off the mesh", func() bool { return !a.listsSession(child) && !b.listsSession(child) })
	if resp := b.trySend("445566", alice, "from delegate"); resp.OK || !strings.Contains(resp.Error, agyName) {
		t.Errorf("delegate send: ok=%v err=%q (want refusal naming %s)", resp.OK, resp.Error, agyName)
	}
	if resp := b.do(proto.Request{Op: proto.OpStatus, Session: "445566", Doing: "sneaking"}); resp.OK || !strings.Contains(resp.Error, agyName) {
		t.Errorf("delegate status: ok=%v err=%q", resp.OK, resp.Error)
	}

	// Settled thread goes offline on both devices and refuses messages; unsettling brings it back.
	insertT3Thread(t, db, "thread-agy", "Fix Antigravity T3", "gemini-4-neon", "", "settled")
	eventually(t, "settled agy leaves who", func() bool { return !a.listsSession(agy) && !b.listsSession(agy) })
	if resp := a.trySend("aaaaaa", agyName, "to settled"); resp.OK || !strings.Contains(resp.Error, "settled") {
		t.Errorf("send to settled: ok=%v err=%q", resp.OK, resp.Error)
	}
	insertT3Thread(t, db, "thread-agy", "Fix Antigravity T3", "gemini-4-neon", "", "")
	eventually(t, "unsettled agy returns", func() bool { return a.seesSession(agy) && b.listsSession(agy) })

	// Stable identity across MCP server restart: same address and same name.
	agyConn.Close()
	eventually(t, "agy stops after grace", func() bool { return busLines(b, agy, "stopped") == 1 })
	restarted, _ := b.agyHello("112233", 720, route, true)
	if restarted != agy {
		t.Errorf("restarted at %s, want %s", restarted, agy)
	}
	b.nameIs(restarted, agyName)
}

// Under an older T3 that doesn't set T3CODE_THREAD_ID for Antigravity, the daemon resolves
// T3Thread from hookNative via t3db.ThreadOf on the first hook: preserving the session's name,
// enabling T3 title, derived model: tag, idle turns, delegate isolation, and name reuse across
// restarts (#886).
func TestAntigravityHookNativeFallbackEndToEnd(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	home, db := agyT3Fixture(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertT3Thread(t, db, "thread-fb", "Fallback thread title", "gemini-4-neon", "", "")
	insertT3Run(t, db, "run-fb1", "thread-fb", 1, "completed", now, now)
	insertT3ProviderThread(t, db, "pt-1", "thread-fb", "conv-fb-1", now)

	insertT3Thread(t, db, "thread-fb-kid", "Fallback delegate", "gemini-4-neon", "thread-fb", "")
	insertT3Run(t, db, "run-kid1", "thread-fb-kid", 1, "completed", now, now)
	insertT3ProviderThread(t, db, "pt-kid", "thread-fb-kid", "conv-fb-kid", now)

	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) { d.grace = 300 * time.Millisecond; d.keep = time.Hour })
	alice, _ := a.session("aaaaaa", "")
	// Registered without T3Thread or Native (older T3 without T3CODE_THREAD_ID).
	bareRoute := proto.Route{T3CLI: exe, T3Home: home}
	agy, agyConn := b.agyHello("bb1122", 800, bareRoute, false)
	origName := b.named(agy)
	eventually(t, "presence", func() bool { return a.seesSession(agy) && b.seesSession(alice) })
	a.nameIs(agy, origName)

	// Before any hook, a message cannot be handed over as a turn.
	a.send("aaaaaa", origName, "early before hook", "wait for fallback")
	eventually(t, "early message reached b", func() bool { return logHas(b, "early before hook") })
	holds(t, "no handoff before hook learns conversationId", 500*time.Millisecond, func() bool { return len(calls()) == 0 })

	// First hook (PreInvocation) reports conversationId -> delivers the early notice and triggers
	// fallback T3Thread resolution from statev2.sqlite.
	noticeResp := b.do(proto.Request{Op: proto.OpNotices, Agent: "antigravity", ClaudeSession: "conv-fb-1", Ancestors: []int{801, 800}})
	if len(noticeResp.Messages) != 1 || noticeResp.Messages[0].Subject != "early before hook" {
		t.Fatalf("first PreInvocation notices: %+v", noticeResp.Messages)
	}
	eventually(t, "fallback resolves title and model tag", func() bool {
		doing, ok := b.doingIn("bb1122", agy)
		return ok && doing == "Fallback thread title" && b.hasTag(agy, "model:gemini-4-neon")
	})
	// Name is preserved when moving from session:<id> to t3:thread-fb.
	b.nameIs(agy, origName)

	// Antigravity's Stop hook (Continues: true) hands over the unread early message whole to keep
	// the turn going, and a subsequent Stop leaves the thread idle.
	stopResp := b.do(proto.Request{Op: proto.OpTurnEnd, Agent: "antigravity", ClaudeSession: "conv-fb-1", Ancestors: []int{801, 800}, Continues: true})
	if len(stopResp.Read) != 1 || stopResp.Read[0].Subject != "early before hook" {
		t.Fatalf("first Stop Read: %+v", stopResp.Read)
	}
	b.do(proto.Request{Op: proto.OpTurnEnd, Agent: "antigravity", ClaudeSession: "conv-fb-1", Ancestors: []int{801, 800}, Continues: true})

	// Now idle with T3Thread resolved, a new message wakes the thread via fake T3.
	a.send("aaaaaa", origName, "after fallback", "wake via t3")
	eventually(t, "message handed over via fake T3 after fallback", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-fb -" || !strings.Contains(c.Stdin, "after fallback") {
		t.Fatalf("fake T3 call: %+v", c)
	}

	// A delegate without T3CODE_THREAD_ID is also taken off the mesh once its hook reports.
	kid, _ := b.agyHello("cc3344", 810, bareRoute, false)
	b.do(proto.Request{Op: proto.OpNotices, Agent: "antigravity", ClaudeSession: "conv-fb-kid", Ancestors: []int{811, 810}})
	eventually(t, "fallback delegate taken off the mesh", func() bool { return !b.listsSession(kid) && !a.listsSession(kid) })
	if resp := b.trySend("cc3344", alice, "from fallback delegate"); resp.OK || !strings.Contains(resp.Error, origName) {
		t.Errorf("fallback delegate send: ok=%v err=%q (want refusal naming %s)", resp.OK, resp.Error, origName)
	}

	// After the session stops, a new server in the same thread (different random ID, still no
	// T3CODE_THREAD_ID) rekeys back to origName once its hook resolves the same thread.
	agyConn.Close()
	eventually(t, "first agy stops", func() bool { return busLines(b, agy, "stopped") == 1 })
	later := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	insertT3ProviderThread(t, db, "pt-2", "thread-fb", "conv-fb-2", later)
	agy2, _ := b.agyHello("dd5566", 820, bareRoute, false)
	b.named(agy2) // wait for its initial name before rekeying
	b.do(proto.Request{Op: proto.OpNotices, Agent: "antigravity", ClaudeSession: "conv-fb-2", Ancestors: []int{821, 820}})
	b.nameIs(agy2, origName)
}

// When t3db.ThreadOf returns transient ErrNotFound while T3 hasn't flushed the provider-thread row
// yet, the daemon retries with backoff, logs no error spam, and hands over waiting messages once
// the row appears (#886).
func TestAntigravityHookNativeFallbackTransientNotFound(t *testing.T) {
	t.Parallel()
	remote := newRemote(t)
	exe, calls := useFakeT3(t)
	var out syncBuf
	var attempts atomic.Int32
	threads := &fakeThreads{st: map[string]t3db.State{"thread-delayed": {LastRun: time.Now()}}}
	a := newDevice(t, remote)
	b := newDeviceWith(t, remote, func(d *device) {
		d.logTo = &out
		d.titleEvery = 50 * time.Millisecond
		d.threadState = threads.read
		d.threadOf = func(_ context.Context, _, _, native string) (string, error) {
			if native != "conv-delayed" {
				return "", t3db.ErrNotFound
			}
			if attempts.Add(1) < 3 {
				return "", t3db.ErrNotFound
			}
			return "thread-delayed", nil
		}
	})
	alice, _ := a.session("aaaaaa", "")
	agy, _ := b.agyHello("ee7788", 900, proto.Route{T3CLI: exe}, false)
	agyName := b.named(agy)
	eventually(t, "presence", func() bool { return a.seesSession(agy) && b.seesSession(alice) })
	a.nameIs(agy, agyName)

	a.send("aaaaaa", agyName, "after transient not found", "hello")
	eventually(t, "message queued on b", func() bool { return logHas(b, "after transient not found") })
	// Stop hook reports conversationId while session is idle; ThreadOf misses twice then succeeds.
	b.do(proto.Request{Op: proto.OpTurnEnd, Agent: "antigravity", ClaudeSession: "conv-delayed", Ancestors: []int{901, 900}})
	eventually(t, "handoff after transient ErrNotFound resolves", func() bool { return len(calls()) == 1 })
	if c := calls()[0]; strings.Join(c.Args, " ") != "thread send thread-delayed -" {
		t.Fatalf("fake T3 call: %+v", c)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("ThreadOf called %d times, want 3", got)
	}
	if strings.Contains(out.String(), "can't find the T3 thread") {
		t.Errorf("ErrNotFound logged as warning:\n%s", out.String())
	}
}
