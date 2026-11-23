package t3db

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/t3"
)

// T3's database is its own, and an update may change it (#1016). These tests change it the ways
// an update might, one table or column at a time, and say what the readers do then: a read that
// can't be made is an error, never a wrong answer, a panic or a wait past Total; and a change that
// leaves what a reader reads alone, such as a new column, changes nothing.

// col is a column of a fixture table and the value its one row holds, as SQL.
type col struct{ name, value string }

// driftTables are the three tables the readers read, with the columns T3 0.1.x has that they read
// (and title and provider, which some don't), each holding one row: thread t1, at work on run r1,
// in a Claude session s1.
var driftTables = map[string][]col{
	"orchestration_v2_projection_threads": {
		{"thread_id", "'t1'"}, {"title", "'A title'"}, {"archived_at", "NULL"}, {"deleted_at", "NULL"},
		{"payload_json", `'{"modelSelection":{"model":"claude-opus-5-5"}}'`},
	},
	"orchestration_v2_projection_runs": {
		{"run_id", "'r1'"}, {"thread_id", "'t1'"}, {"status", "'running'"},
		{"requested_at", "'2026-11-07T22:30:00.000Z'"}, {"completed_at", "NULL"}, {"ordinal", "1"},
	},
	"orchestration_v2_projection_provider_threads": {
		{"thread_id", "'t1'"}, {"provider", "'claudeAgent'"}, {"payload_json", `'` + payload("s1", "t1") + `'`},
		{"updated_at", "'2026-11-07T22:00:00Z'"},
	},
}

// drift is one change an update to T3 might make to table: drop the table, or drop, rename or add
// a column.
type drift struct {
	table  string
	gone   bool   // the table isn't there
	drop   string // a column that isn't there
	rename string // a column under another name
	extra  bool   // a column the readers don't know, in the middle
}

const (
	threadsTable  = "orchestration_v2_projection_threads"
	runsTable     = "orchestration_v2_projection_runs"
	providerTable = "orchestration_v2_projection_provider_threads"
)

// drifted makes T3's data directory with the three tables, the one dr names changed as it says.
func drifted(t *testing.T, dr drift) string {
	t.Helper()
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	defer db.Close()
	for table, cols := range driftTables {
		if table == dr.table && dr.gone {
			continue
		}
		var names, values []string
		for i, c := range cols {
			if table == dr.table && dr.extra && i == len(cols)/2 {
				names, values = append(names, "added_in_a_later_t3"), append(values, "'whatever'")
			}
			switch {
			case table == dr.table && c.name == dr.drop:
				continue
			case table == dr.table && c.name == dr.rename:
				names, values = append(names, c.name+"_v2"), append(values, c.value)
			default:
				names, values = append(names, c.name), append(values, c.value)
			}
		}
		for _, q := range []string{
			`CREATE TABLE ` + table + ` (` + strings.Join(names, ", ") + `)`,
			`INSERT INTO ` + table + ` VALUES (` + strings.Join(values, ", ") + `)`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
	}
	return home
}

// driftCases are, for each table in tables, the table gone, each of its columns in reads dropped
// and renamed, and a column added; and each column in unread dropped, which the reader doesn't
// notice. ok says the read must still give the answer.
type driftCase struct {
	name string
	dr   drift
	ok   bool
}

func driftCases(tables map[string][]string, unread map[string][]string) []driftCase {
	var cases []driftCase
	for table, reads := range tables {
		short := strings.TrimPrefix(table, "orchestration_v2_projection_")
		cases = append(cases,
			driftCase{short + " gone", drift{table: table, gone: true}, false},
			driftCase{short + " with a new column", drift{table: table, extra: true}, true})
		for _, c := range reads {
			cases = append(cases,
				driftCase{short + " without " + c, drift{table: table, drop: c}, false},
				driftCase{short + " with " + c + " renamed", drift{table: table, rename: c}, false})
		}
		for _, c := range unread[table] {
			cases = append(cases, driftCase{short + " without " + c + ", which isn't read", drift{table: table, drop: c}, true})
		}
	}
	return cases
}

// timed runs read and fails the test if it took longer than the reader may.
func timed(t *testing.T, read func()) {
	t.Helper()
	start := time.Now()
	read()
	if took := time.Since(start); took > Total+time.Second {
		t.Errorf("took %s, past Total (%s)", took, Total)
	}
}

func TestStateOfUnderSchemaDrift(t *testing.T) {
	want := State{Busy: true, LastRun: time.Date(2026, 11, 7, 22, 30, 0, 0, time.UTC), Model: "claude-opus-5-5"}
	cases := driftCases(map[string][]string{
		threadsTable: {"thread_id", "archived_at", "deleted_at", "payload_json"},
		runsTable:    {"thread_id", "status", "requested_at", "completed_at", "ordinal"},
	}, map[string][]string{threadsTable: {"title"}, runsTable: {"run_id"}})
	// The provider table is ThreadOf's: StateOf doesn't miss it.
	cases = append(cases, driftCase{"provider_threads gone", drift{table: providerTable, gone: true}, true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := drifted(t, tc.dr)
			var got State
			var err error
			timed(t, func() { got, err = StateOf(context.Background(), "", home, "t1") })
			switch {
			case tc.ok && (err != nil || got != want):
				t.Errorf("StateOf = %+v, %v; want %+v", got, err, want)
			case !tc.ok && (err == nil || errors.Is(err, ErrNotFound) || got != State{}):
				t.Errorf("StateOf = %+v, %v; want an error that is not ErrNotFound, and no State", got, err)
			}
		})
	}
}

func TestThreadOfUnderSchemaDrift(t *testing.T) {
	cases := driftCases(map[string][]string{providerTable: {"thread_id", "payload_json", "updated_at"}},
		map[string][]string{providerTable: {"provider"}})
	cases = append(cases,
		driftCase{"threads gone", drift{table: threadsTable, gone: true}, true},
		driftCase{"runs gone", drift{table: runsTable, gone: true}, true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := drifted(t, tc.dr)
			var got string
			var err error
			timed(t, func() { got, err = lookup(home, "s1") })
			switch {
			case tc.ok && (err != nil || got != "t1"):
				t.Errorf("ThreadOf = %q, %v; want t1", got, err)
			case !tc.ok && (err == nil || errors.Is(err, ErrNotFound) || got != ""):
				t.Errorf("ThreadOf = %q, %v; want an error that is not ErrNotFound", got, err)
			}
		})
	}
}

func TestTitleOfUnderSchemaDrift(t *testing.T) {
	cases := driftCases(map[string][]string{threadsTable: {"thread_id", "title"}},
		map[string][]string{threadsTable: {"payload_json", "archived_at"}})
	cases = append(cases,
		driftCase{"runs gone", drift{table: runsTable, gone: true}, true},
		driftCase{"provider_threads gone", drift{table: providerTable, gone: true}, true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := drifted(t, tc.dr)
			var got string
			var err error
			timed(t, func() { got, err = TitleOf(context.Background(), "", home, "t1") })
			switch {
			case tc.ok && (err != nil || got != "A title"):
				t.Errorf("TitleOf = %q, %v; want %q", got, err, "A title")
			case !tc.ok && (err == nil || errors.Is(err, ErrNotFound) || got != ""):
				t.Errorf("TitleOf = %q, %v; want an error that is not ErrNotFound", got, err)
			}
		})
	}
}

// stateFixture makes T3's data directory with StateOf's tables (stateSchema) and the statements'
// rows.
func stateFixture(t *testing.T, stmts ...string) string {
	t.Helper()
	home := t.TempDir()
	db := open(t, t3.StateDB(home))
	defer db.Close()
	for _, s := range append([]string{stateSchema}, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return home
}

// A run status T3 may add, or write another way, is not 'queued' or 'running', so no run of it
// counts toward Busy: what this pins is today's behaviour (2026-11-23), and some of it may be
// wrong, which is for a person to decide, not this test.
//   - The match is exact: 'RUNNING' or 'Queued' is not busy. The statuses seen on T3 0.1.x are lowercase.
//   - A newest run of a status not known makes an older running run Left, so the daemon takes the
//     thread as not at work (runGoing). If T3 added a status for a run that is starting, say,
//     a thread at work would read as idle with a run left behind.
//   - A run with no status (NULL) isn't the newest, as a cancelled one isn't (NULL <> 'cancelled'
//     is not true), so an older running run behind it stays Busy and not Left. An empty status
//     is newest like any other, and makes it Left.
func TestStateOfWithRunStatusesItDoesNotKnow(t *testing.T) {
	const (
		older = "2026-11-07T22:00:00.000Z"
		newer = "2026-11-07T22:30:00.000Z"
	)
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	threads := []string{}
	runs := []string{}
	want := map[string]State{}
	// Alone: a single run, still open, of each status.
	for _, status := range []string{"paused", "failed", "", "RUNNING", "Queued", "running "} {
		id := "alone-" + status
		threads = append(threads, `('`+id+`', 't', NULL, NULL, '{}')`)
		runs = append(runs, `('r-`+id+`', '`+id+`', '`+status+`', '`+newer+`', NULL, 1)`)
		want[id] = State{LastRun: at(newer)}
	}
	threads = append(threads, `('alone-null', 't', NULL, NULL, '{}')`)
	runs = append(runs, `('r-alone-null', 'alone-null', NULL, '`+newer+`', NULL, 1)`)
	want["alone-null"] = State{LastRun: at(newer)}
	// Behind: an older run still running, and a newer one of the status.
	for _, status := range []string{"paused", "failed", "", "RUNNING"} {
		id := "behind-" + status
		threads = append(threads, `('`+id+`', 't', NULL, NULL, '{}')`)
		runs = append(runs,
			`('r1-`+id+`', '`+id+`', 'running', '`+older+`', NULL, 1)`,
			`('r2-`+id+`', '`+id+`', '`+status+`', '`+newer+`', NULL, 2)`)
		want[id] = State{Busy: true, Left: true, LastRun: at(newer)}
	}
	threads = append(threads, `('behind-null', 't', NULL, NULL, '{}')`)
	runs = append(runs,
		`('r1-behind-null', 'behind-null', 'running', '`+older+`', NULL, 1)`,
		`('r2-behind-null', 'behind-null', NULL, '`+newer+`', NULL, 2)`)
	want["behind-null"] = State{Busy: true, LastRun: at(newer)}

	home := stateFixture(t,
		`INSERT INTO orchestration_v2_projection_threads VALUES `+strings.Join(threads, ", "),
		`INSERT INTO orchestration_v2_projection_runs VALUES `+strings.Join(runs, ", "))
	for thread, w := range want {
		got, err := StateOf(context.Background(), "", home, thread)
		if err != nil || got != w {
			t.Errorf("%q: %+v, %v; want %+v", thread, got, err, w)
		}
	}
}

// A payload that is JSON, but not shaped as T3 0.1.x shapes it, fails no read: a path that isn't
// there is "". A path that is there, holding something other than text, is read as SQLite gives
// it: a number as its digits, an object as its JSON. This pins today's behaviour (2026-11-23).
func TestStateOfWithPayloadsShapedOtherwise(t *testing.T) {
	cases := []struct {
		payload string
		want    State
	}{
		{`{"lineage":"t0"}`, State{}},
		{`{"lineage":["t0"]}`, State{}},
		{`{"lineage":{"parentThreadId":42}}`, State{Parent: "42"}},
		{`{"lineage":{"parentThreadId":{"id":"t0"}}}`, State{Parent: `{"id":"t0"}`}},
		{`{"modelSelection":["claude-opus-5-5"]}`, State{}},
		{`{"modelSelection":"claude-opus-5-5"}`, State{}},
		{`{"modelSelection":{"model":{"id":"claude-opus-5-5"}}}`, State{Model: `{"id":"claude-opus-5-5"}`}},
		{`{"modelSelection":{"model":null}}`, State{}},
		// Only the text "settled" is settled.
		{`{"settledOverride":true}`, State{}},
		{`{"settledOverride":{"state":"settled"}}`, State{}},
		{`{"settledOverride":"Settled"}`, State{}},
		{`[]`, State{}},
		{`null`, State{}},
		{`"a string"`, State{}},
		{`{}`, State{}},
		{``, State{}},
	}
	var rows []string
	for i, tc := range cases {
		rows = append(rows, `('t`+string(rune('a'+i))+`', 't', NULL, NULL, '`+tc.payload+`')`)
	}
	home := stateFixture(t, `INSERT INTO orchestration_v2_projection_threads VALUES `+strings.Join(rows, ", "))
	for i, tc := range cases {
		got, err := StateOf(context.Background(), "", home, "t"+string(rune('a'+i)))
		if err != nil || got != tc.want {
			t.Errorf("payload %s: %+v, %v; want %+v", tc.payload, got, err, tc.want)
		}
	}
}

// T3 writing the database at that moment holds a lock: StateOf, which the daemon calls every few
// seconds and before every handoff, gives up within Total and says so, and reads it once the lock
// is gone.
func TestStateOfGivesUpOnALockedDatabase(t *testing.T) {
	defer func(b, tot time.Duration) { Busy, Total = b, tot }(Busy, Total)
	Busy, Total = 150*time.Millisecond, 2*time.Second
	home := stateFixture(t,
		`INSERT INTO orchestration_v2_projection_threads VALUES ('t1', 't', NULL, NULL, '{}')`,
		`INSERT INTO orchestration_v2_projection_runs VALUES ('r1', 't1', 'running', '2026-11-07T22:30:00.000Z', NULL, 1)`)
	w := open(t, t3.StateDB(home))
	defer w.Close()
	if _, err := w.Exec(`BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	got, err := StateOf(context.Background(), "", home, "t1")
	if err == nil || errors.Is(err, ErrNotFound) || got != (State{}) {
		t.Fatalf("StateOf = %+v, %v; want an error", got, err)
	}
	if took := time.Since(start); took > Total {
		t.Errorf("took %s to give up, past Total (%s)", took, Total)
	}
	if _, err := w.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if got, err := StateOf(context.Background(), "", home, "t1"); err != nil || !got.Busy {
		t.Errorf("once the lock is released: %+v, %v", got, err)
	}
}
