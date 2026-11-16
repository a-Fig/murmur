package t3db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/t3"
)

// The fixture is a state database like T3's, as far as the lookup goes: the table, its columns,
// and a payload that holds the provider's own session ID (seen on T3 0.1.x, 2026-10-29).

const schema = `CREATE TABLE orchestration_v2_projection_provider_threads (
	thread_id TEXT, provider TEXT, payload_json TEXT, updated_at TEXT)`

type row struct{ thread, provider, payload, updated string }

func payload(native, app string) string {
	return `{"appThreadId":"` + app + `","nativeThreadRef":{"provider":"x","nativeId":"` + native + `"},"status":"ready"}`
}

// fixture makes T3's data directory (with userdata/statev2.sqlite) holding rows, and returns it.
func fixture(t *testing.T, rows ...row) string {
	t.Helper()
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		var thread any = r.thread
		if r.thread == "" {
			thread = nil
		}
		if _, err := db.Exec(`INSERT INTO orchestration_v2_projection_provider_threads VALUES (?, ?, ?, ?)`,
			thread, r.provider, r.payload, r.updated); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// open opens (and makes) a database for writing, as T3 holds its own.
func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// As a URI, as the reader opens it: given as a plain name, a path with a ? in it is cut there.
	u := fileURL(path)
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// lookup asks about native in the data directory home. The user's own ~/.t3, which ThreadOf also
// tries, is swapped for an empty directory, so that nothing here reads the real T3's database.
func lookup(home, native string) (string, error) {
	return ThreadOf(context.Background(), "", home, native)
}

func TestMain(m *testing.M) {
	empty, err := os.MkdirTemp("", "t3db-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("USERPROFILE", empty) // os.UserHomeDir on Windows
	os.Setenv("HOME", empty)
	code := m.Run()
	os.RemoveAll(empty)
	os.Exit(code)
}

func TestThreadOfFindsTheThreadOfAClaudeSessionAndOfACodexThread(t *testing.T) {
	home := fixture(t,
		row{"thread-claude", "claudeAgent", payload("claude-session-1", "thread-claude"), "2026-10-29T10:00:00Z"},
		row{"thread-codex", "codex", payload("codex-thread-9", "thread-codex"), "2026-10-29T10:05:00Z"},
		row{"thread-other", "claudeAgent", payload("claude-session-2", "thread-other"), "2026-10-29T10:06:00Z"})
	for native, want := range map[string]string{
		"claude-session-1": "thread-claude", "codex-thread-9": "thread-codex", "claude-session-2": "thread-other"} {
		if got, err := lookup(home, native); err != nil || got != want {
			t.Errorf("ThreadOf(%s) = %q, %v; want %q", native, got, err, want)
		}
	}
	if _, err := lookup(home, "no-such-session"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown session: %v, want ErrNotFound", err)
	}
	if _, err := lookup(home, ""); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("no session ID: %v", err)
	}
}

// The data directory comes from T3CODE_HOME, else from where the command line is.
func TestThreadOfFindsTheDatabaseFromTheCLIsPath(t *testing.T) {
	home := fixture(t, row{"thread-1", "claudeAgent", payload("s1", "thread-1"), "2026-10-29T10:00:00Z"})
	cli := filepath.Join(home, "bin", "t3.cmd")
	if got, err := ThreadOf(context.Background(), cli, "", "s1"); err != nil || got != "thread-1" {
		t.Errorf("by the CLI's path: %q, %v", got, err)
	}
	if got, err := ThreadOf(context.Background(), filepath.Join(t.TempDir(), "bin", "t3"), home, "s1"); err != nil || got != "thread-1" {
		t.Errorf("by the home: %q, %v", got, err)
	}
}

// A session in two rows (resumed under another thread, an ID reused) is the one updated last; a
// row that left thread_id empty has the thread in its payload.
func TestThreadOfPicksTheNewestAndFallsBackToThePayload(t *testing.T) {
	home := fixture(t,
		row{"old-thread", "claudeAgent", payload("s1", "old-thread"), "2026-10-29T09:00:00Z"},
		row{"new-thread", "claudeAgent", payload("s1", "new-thread"), "2026-10-29T11:00:00Z"},
		row{"", "claudeAgent", payload("s2", "payload-thread"), "2026-10-29T09:00:00Z"})
	if got, err := lookup(home, "s1"); err != nil || got != "new-thread" {
		t.Errorf("s1: %q, %v", got, err)
	}
	if got, err := lookup(home, "s2"); err != nil || got != "payload-thread" {
		t.Errorf("s2: %q, %v", got, err)
	}
}

// A row T3 wrote that isn't JSON, or has no native ID, is passed over: it doesn't fail the lookup
// of everyone else's thread.
func TestThreadOfPassesOverRowsItCannotRead(t *testing.T) {
	home := fixture(t,
		row{"junk-1", "x", "not json at all", "2026-10-29T12:00:00Z"},
		row{"junk-2", "x", `{"nativeThreadRef":null}`, "2026-10-29T12:00:00Z"},
		row{"junk-3", "x", `{"nativeThreadRef":{"nativeId":null}}`, "2026-10-29T12:00:00Z"},
		row{"junk-4", "x", `[]`, "2026-10-29T12:00:00Z"},
		row{"real", "claudeAgent", payload("s1", "real"), "2026-10-29T10:00:00Z"})
	if got, err := lookup(home, "s1"); err != nil || got != "real" {
		t.Errorf("ThreadOf = %q, %v", got, err)
	}
}

// Whatever is wrong with T3's database is an error, not a panic or a wrong thread, and not
// ErrNotFound, which means T3 was read and has no such thread.
func TestThreadOfFailsSoftly(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"no data directory": func(t *testing.T) string { return filepath.Join(t.TempDir(), "nothing") },
		"no database": func(t *testing.T) string {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(t3.StateDB(home)), 0o755); err != nil {
				t.Fatal(err)
			}
			return home
		},
		"the database is a directory": func(t *testing.T) string {
			home := t.TempDir()
			if err := os.MkdirAll(t3.StateDB(home), 0o755); err != nil {
				t.Fatal(err)
			}
			return home
		},
		"not a database": func(t *testing.T) string {
			home := t.TempDir()
			os.MkdirAll(filepath.Dir(t3.StateDB(home)), 0o755)
			if err := os.WriteFile(t3.StateDB(home), []byte("this is not sqlite, and long enough to be read as a header, or not"), 0o600); err != nil {
				t.Fatal(err)
			}
			return home
		},
		"an empty database": func(t *testing.T) string {
			home := t.TempDir()
			db := open(t, t3.StateDB(home))
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE unrelated (x)`); err != nil {
				t.Fatal(err)
			}
			return home
		},
		"a table with other columns": func(t *testing.T) string {
			home := t.TempDir()
			db := open(t, t3.StateDB(home))
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE orchestration_v2_projection_provider_threads (id TEXT, data TEXT)`); err != nil {
				t.Fatal(err)
			}
			return home
		},
	}
	for name, make := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := lookup(make(t), "s1")
			if err == nil || errors.Is(err, ErrNotFound) || got != "" {
				t.Errorf("ThreadOf = %q, %v; want an error that is not ErrNotFound", got, err)
			}
		})
	}
}

// T3 writing the database at that moment holds a lock readers wait for: the lookup waits only
// the moment it is allowed, then gives up, and the database is the same afterwards.
func TestThreadOfGivesUpOnALockedDatabase(t *testing.T) {
	home := fixture(t, row{"thread-1", "claudeAgent", payload("s1", "thread-1"), "2026-10-29T10:00:00Z"})
	defer func(b, tot time.Duration) { Busy, Total = b, tot }(Busy, Total)
	Busy, Total = 150*time.Millisecond, 2*time.Second

	w := open(t, t3.StateDB(home))
	defer w.Close()
	if _, err := w.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := lookup(home, "s1")
	if err == nil || errors.Is(err, ErrNotFound) || got != "" {
		t.Fatalf("ThreadOf = %q, %v; want an error", got, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("took %s to give up", took)
	}
	if _, err := w.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if got, err := lookup(home, "s1"); err != nil || got != "thread-1" {
		t.Errorf("once the lock is released: %q, %v", got, err)
	}
}

// T3 runs its database in write-ahead log mode, where the newest rows may be only in the log; a
// reader that ignored the log (immutable mode does) would miss the thread just started. Nor may
// a read change the files.
func TestThreadOfSeesWhatIsOnlyInTheLogAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	w := open(t, t3.StateDB(home))
	defer w.Close()
	for _, q := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, schema,
		`INSERT INTO orchestration_v2_projection_provider_threads VALUES ('thread-new', 'claudeAgent', '` + payload("s-new", "thread-new") + `', '2026-10-29T10:00:00Z')`} {
		if _, err := w.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	wal := t3.StateDB(home) + "-wal"
	if fi, err := os.Stat(wal); err != nil || fi.Size() == 0 {
		t.Fatalf("the row should be in the log only: %v", err)
	}
	before := sums(t, home)
	for range 3 {
		if got, err := lookup(home, "s-new"); err != nil || got != "thread-new" {
			t.Fatalf("ThreadOf = %q, %v", got, err)
		}
	}
	if after := sums(t, home); after != before {
		t.Errorf("the lookup changed the files:\nbefore %s\nafter  %s", before, after)
	}
}

// sums fingerprints the database file and its log (not the shared-memory index, which every reader
// of a log-mode database keeps).
func sums(t *testing.T, home string) string {
	t.Helper()
	var out string
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(t3.StateDB(home) + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%x ", sha256.Sum256(b))
	}
	return out
}

// What a path could be taken for in a URI (a ?, a #, a space) is escaped, on every platform: the
// path with a ? in it can only be made on some of them, but its URI can be checked on all.
func TestDSNEscapesWhatAPathCouldBeTakenFor(t *testing.T) {
	got := dsn("/t3/what?is this#x/userdata/statev2.sqlite")
	const path = "file:///t3/what%3Fis%20this%23x/userdata/statev2.sqlite?"
	if !strings.HasPrefix(got, path) {
		t.Errorf("dsn = %q, want it to start with %q", got, path)
	}
	for _, want := range []string{"mode=ro", "query_only%281%29", "busy_timeout%28"} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn = %q, want %q in it", got, want)
		}
	}
}

// A path with a space, a percent sign or a question mark in it opens as itself, not as a URI.
func TestThreadOfOpensOddPaths(t *testing.T) {
	dirs := []string{"t3 home", "100%25 sure", "a#b&c=d", "ünï"}
	if runtime.GOOS != "windows" {
		dirs = append(dirs, "what?is this") // a name Windows doesn't allow
	}
	for _, dir := range dirs {
		root := filepath.Join(t.TempDir(), dir)
		db := open(t, t3.StateDB(root))
		for _, q := range []string{schema, `INSERT INTO orchestration_v2_projection_provider_threads VALUES ('thread-1', 'x', '` + payload("s1", "thread-1") + `', '1')`} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", dir, err)
			}
		}
		db.Close()
		if got, err := lookup(root, "s1"); err != nil || got != "thread-1" {
			t.Errorf("%q: ThreadOf = %q, %v", dir, got, err)
		}
	}
}

const threadsSchema = `CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, title TEXT, payload_json TEXT)`

func titleFixture(t *testing.T, stmts ...string) string {
	t.Helper()
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	defer db.Close()
	for _, s := range append([]string{threadsSchema}, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func TestTitleOfReadsTheThreadsTitle(t *testing.T) {
	home := titleFixture(t,
		`INSERT INTO orchestration_v2_projection_threads VALUES ('thread-a', 'Offer Help to Murmur Session', '{"title":"Offer Help to Murmur Session"}')`,
		`INSERT INTO orchestration_v2_projection_threads VALUES ('thread-untitled', NULL, '{}')`)
	ctx := context.Background()
	if got, err := TitleOf(ctx, "", home, "thread-a"); err != nil || got != "Offer Help to Murmur Session" {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := TitleOf(ctx, "", home, "thread-untitled"); err != nil || got != "" {
		t.Errorf("a thread with no title: %q, %v", got, err)
	}
	if got, err := TitleOf(ctx, "", home, "thread-none"); !errors.Is(err, ErrNotFound) || got != "" {
		t.Errorf("a thread with no row: %q, %v", got, err)
	}
	if _, err := TitleOf(ctx, "", home, ""); err == nil {
		t.Errorf("no thread gave no error")
	}
}

func TestTitleOfFailsSoftly(t *testing.T) {
	cases := map[string]string{
		"no data directory": filepath.Join(t.TempDir(), "nothing"),
		"another schema": func() string {
			home := t.TempDir()
			db := open(t, t3.StateDB(home))
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE orchestration_v2_projection_threads (id TEXT, name TEXT)`); err != nil {
				t.Fatal(err)
			}
			return home
		}(),
		"no such table": func() string {
			home := t.TempDir()
			db := open(t, t3.StateDB(home))
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE unrelated (x)`); err != nil {
				t.Fatal(err)
			}
			return home
		}(),
	}
	for name, home := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := TitleOf(context.Background(), "", home, "thread-a")
			if err == nil || errors.Is(err, ErrNotFound) || got != "" {
				t.Errorf("TitleOf = %q, %v; want an error that is not ErrNotFound", got, err)
			}
		})
	}
}

func TestTitleOfGivesUpOnALockedDatabase(t *testing.T) {
	defer func(b, tot time.Duration) { Busy, Total = b, tot }(Busy, Total)
	Busy, Total = 150*time.Millisecond, 2*time.Second
	home := titleFixture(t, `INSERT INTO orchestration_v2_projection_threads VALUES ('thread-a', 'Titled', '{}')`)
	db := open(t, t3.StateDB(home))
	defer db.Close()
	if _, err := db.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := TitleOf(context.Background(), "", home, "thread-a")
	if err == nil || got != "" || time.Since(start) > 3*time.Second {
		t.Errorf("TitleOf = %q, %v after %s", got, err, time.Since(start))
	}
}

// The tables StateOf reads, with the columns it reads (T3 0.1.x, 2026-11-07).
const stateSchema = `CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, title TEXT,
	archived_at TEXT, deleted_at TEXT, payload_json TEXT);
CREATE TABLE orchestration_v2_projection_runs (run_id TEXT PRIMARY KEY, thread_id TEXT, status TEXT,
	requested_at TEXT, completed_at TEXT, ordinal INTEGER)`

func TestStateOfReadsSettledAndTheLatestRun(t *testing.T) {
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	for _, s := range []string{
		stateSchema,
		`INSERT INTO orchestration_v2_projection_threads VALUES
			('idle', 't', NULL, NULL, '{"settledOverride":null}'),
			('settled', 't', NULL, NULL, '{"settledOverride":"settled","settledAt":"2026-11-07T22:29:15.455Z"}'),
			('archived', 't', '2026-11-07T20:00:00.000Z', NULL, '{}'),
			('deleted', 't', NULL, '2026-11-07T20:00:00.000Z', '{}'),
			('busy', 't', NULL, NULL, '{"modelSelection":{"instanceId":"claude_personal","model":"claude-opus-5-5"}}'),
			('new', 't', NULL, NULL, 'not json'),
			('child', 't', NULL, NULL, '{"lineage":{"parentThreadId":"idle","relationshipToParent":"subagent","rootThreadId":"idle"}}'),
			('left', 't', NULL, NULL, '{}'),
			('cancelled', 't', NULL, NULL, '{}'),
			('tied', 't', NULL, NULL, '{}')`,
		`INSERT INTO orchestration_v2_projection_runs VALUES
			('r1', 'idle', 'completed', '2026-11-07T22:00:00.000Z', '2026-11-07T22:05:00.000Z', 1),
			('r2', 'idle', 'interrupted', '2026-11-07T22:28:36.290Z', '2026-11-07T22:29:10.318Z', 2),
			('r3', 'busy', 'completed', '2026-11-07T21:00:00.000Z', '2026-11-07T21:01:00.000Z', 3),
			('r4', 'busy', 'running', '2026-11-07T22:30:00.000Z', NULL, 4),
			('r5', 'left', 'queued', '2026-11-03T06:10:16.275Z', NULL, 5),
			('r6', 'left', 'completed', '2026-11-07T21:00:00.000Z', '2026-11-07T21:01:00.000Z', 6),
			('r7', 'cancelled', 'running', '2026-11-07T22:00:00.000Z', NULL, 1),
			('r8', 'cancelled', 'cancelled', '2026-11-07T22:10:00.000Z', '2026-11-07T22:11:00.000Z', 2),
			('r9', 'tied', 'completed', '2026-11-07T23:00:00.000Z', '2026-11-07T23:05:00.000Z', 1),
			('r10', 'tied', 'running', '2026-11-07T23:00:00.000Z', NULL, 2)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	want := map[string]State{
		"idle":     {LastRun: at("2026-11-07T22:29:10.318Z")},
		"settled":  {Settled: true},
		"archived": {Settled: true},
		"deleted":  {Settled: true},
		"busy":     {Busy: true, LastRun: at("2026-11-07T22:30:00.000Z"), Model: "claude-opus-5-5"},
		"new":      {},
		"child":    {Parent: "idle"},
		"left":     {Busy: true, Left: true, LastRun: at("2026-11-07T21:01:00.000Z")},
		// A follow-up taken out of the queue leaves the run going a turn (#672), and of two runs
		// asked for at once, the later by T3's ordinal is the newest.
		"cancelled": {Busy: true, LastRun: at("2026-11-07T22:11:00.000Z")},
		"tied":      {Busy: true, LastRun: at("2026-11-07T23:05:00.000Z")},
	}
	ctx := context.Background()
	for thread, w := range want {
		got, err := StateOf(ctx, "", home, thread)
		if err != nil || got != w {
			t.Errorf("%s: %+v, %v; want %+v", thread, got, err, w)
		}
	}
	if _, err := StateOf(ctx, "", home, "none"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a thread with no row: %v", err)
	}
	if _, err := StateOf(ctx, "", home, ""); err == nil {
		t.Errorf("no thread gave no error")
	}
}

func TestStateOfFailsOnATimeItCannotRead(t *testing.T) {
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	for _, s := range []string{
		stateSchema,
		`INSERT INTO orchestration_v2_projection_threads VALUES ('a', 't', NULL, NULL, '{}')`,
		`INSERT INTO orchestration_v2_projection_runs VALUES ('r', 'a', 'completed', 'yesterday', 'yesterday', 1)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if _, err := StateOf(context.Background(), "", home, "a"); err == nil {
		t.Errorf("a run time that isn't a time gave no error")
	}
}
