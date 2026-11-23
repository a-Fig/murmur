package title

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/t3db"
)

const lineageSchema = `CREATE TABLE threads (id TEXT PRIMARY KEY, source TEXT NOT NULL, originator TEXT)`

// The sources below are as Codex 0.166 and 0.167 wrote them on Tyler's laptop (2026-11-12). The
// program that asked for a thread (originator) changes nothing.
func TestCodexLineageReadsWhoStartedAThread(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), lineageSchema,
		`INSERT INTO threads VALUES ('terminal', 'vscode', 'codex-tui')`,
		`INSERT INTO threads VALUES ('exec', 'exec', 'codex_exec')`,
		`INSERT INTO threads VALUES ('old', 'vscode', NULL)`,
		`INSERT INTO threads VALUES ('tool', 'vscode', 'some-tool')`,
		`INSERT INTO threads VALUES ('review', '{"subagent":"review"}', 'some-tool')`,
		`INSERT INTO threads VALUES ('guardian', '{"subagent":{"other":"guardian"}}', 'Codex Desktop')`,
		`INSERT INTO threads VALUES ('spawned', '{"subagent":{"thread_spawn":{"parent_thread_id":"th-parent","depth":1,"agent_path":"/root/x","agent_nickname":"Raman","agent_role":null}}}', 'Codex Desktop')`,
		`INSERT INTO threads VALUES ('odd', '{"subagent":null}', 'Codex Desktop')`)
	ctx := context.Background()
	for id, want := range map[string]Lineage{
		"terminal": {},
		"exec":     {},
		"old":      {},
		"tool":     {},
		"review":   {Subagent: true},
		"guardian": {Subagent: true},
		"spawned":  {Subagent: true, Parent: "th-parent"},
		"odd":      {},
	} {
		got, err := CodexLineage(ctx, dir, id)
		if err != nil || got != want {
			t.Errorf("CodexLineage(%s) = %+v, %v; want %+v", id, got, err, want)
		}
	}
	if _, err := CodexLineage(ctx, dir, "nobody"); !errors.Is(err, ErrNoThread) {
		t.Errorf("a thread Codex doesn't have: %v, want ErrNoThread", err)
	}
	if _, err := CodexLineage(ctx, t.TempDir(), "terminal"); err == nil || errors.Is(err, ErrNoThread) {
		t.Errorf("a directory with no state: %v, want an error that isn't ErrNoThread", err)
	}
}

// Codex's state_N.sqlite is Codex's own and may change in any update (#1013). Schema drift,
// missing tables/columns, corrupt files, and write locks must fail cleanly (and never as
// ErrNoThread, never panicking, never hanging past Total), while extra columns and odd source
// shapes degrade safely without misclassifying user threads as subagents.
func TestCodexLineageSchemaDriftLocksAndOddSources(t *testing.T) {
	ctx := context.Background()

	t.Run("newest state database wins", func(t *testing.T) {
		dir := t.TempDir()
		writeDB(t, filepath.Join(dir, "state_5.sqlite"), lineageSchema, `INSERT INTO threads VALUES ('t1', 'vscode', 'tui')`)
		writeDB(t, filepath.Join(dir, "state_12.sqlite"), lineageSchema, `INSERT INTO threads VALUES ('t1', '{"subagent":"review"}', 'tui')`)
		got, err := CodexLineage(ctx, dir, "t1")
		if err != nil || !got.Subagent {
			t.Errorf("got %+v, %v; want Subagent=true from state_12", got, err)
		}
	})

	t.Run("schema drift errors cleanly and never returns ErrNoThread", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ddl  string
		}{
			{"missing threads table", `CREATE TABLE conversations (id TEXT PRIMARY KEY, source TEXT)`},
			{"missing source column", `CREATE TABLE threads (id TEXT PRIMARY KEY, originator TEXT)`},
			{"renamed source column", `CREATE TABLE threads (id TEXT PRIMARY KEY, source_v2 TEXT)`},
			{"renamed id column", `CREATE TABLE threads (thread_id TEXT PRIMARY KEY, source TEXT)`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				writeDB(t, filepath.Join(dir, "state_5.sqlite"), tc.ddl)
				if got, err := CodexLineage(ctx, dir, "t1"); err == nil || errors.Is(err, ErrNoThread) || got != (Lineage{}) {
					t.Errorf("%s: got (%+v, %v), want non-ErrNoThread error", tc.name, got, err)
				}
			})
		}
	})

	t.Run("extra columns and nullable source succeed", func(t *testing.T) {
		dir := t.TempDir()
		writeDB(t, filepath.Join(dir, "state_5.sqlite"),
			`CREATE TABLE threads (id TEXT PRIMARY KEY, extra_mid TEXT, source TEXT, extra_end INTEGER)`,
			`INSERT INTO threads VALUES ('null-src', 'x', NULL, 1)`,
			`INSERT INTO threads VALUES ('spawned', 'x', '{"subagent":{"thread_spawn":{"parent_thread_id":"p1"}}}', 2)`)
		if got, err := CodexLineage(ctx, dir, "null-src"); err != nil || got != (Lineage{}) {
			t.Errorf("NULL source: got (%+v, %v)", got, err)
		}
		if got, err := CodexLineage(ctx, dir, "spawned"); err != nil || got != (Lineage{Subagent: true, Parent: "p1"}) {
			t.Errorf("extra columns spawned: got (%+v, %v)", got, err)
		}
	})

	t.Run("corrupt file and empty arguments fail cleanly", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("not a sqlite database at all"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := CodexLineage(ctx, dir, "t1"); err == nil || errors.Is(err, ErrNoThread) || got != (Lineage{}) {
			t.Errorf("corrupt file: got (%+v, %v)", got, err)
		}
		if _, err := CodexLineage(ctx, "", "t1"); err == nil {
			t.Error("empty dir gave no error")
		}
		if _, err := CodexLineage(ctx, dir, ""); err == nil {
			t.Error("empty id gave no error")
		}
	})

	t.Run("exclusive lock respects busy timeout", func(t *testing.T) {
		defer func(b, tot time.Duration) { t3db.Busy, Total = b, tot }(t3db.Busy, Total)
		t3db.Busy, Total = 100*time.Millisecond, time.Second
		dir := t.TempDir()
		path := filepath.Join(dir, "state_5.sqlite")
		writeDB(t, path, lineageSchema, `INSERT INTO threads VALUES ('t1', 'vscode', 'tui')`)
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`BEGIN EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got, err := CodexLineage(ctx, dir, "t1")
		if err == nil || errors.Is(err, ErrNoThread) || got != (Lineage{}) {
			t.Errorf("locked db: got (%+v, %v), want non-ErrNoThread error", got, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("waited %s for the lock", time.Since(start))
		}
	})

	t.Run("writes nothing to Codex directory", func(t *testing.T) {
		dir := t.TempDir()
		writeDB(t, filepath.Join(dir, "state_5.sqlite"), lineageSchema, `INSERT INTO threads VALUES ('t1', 'vscode', 'tui')`)
		before, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CodexLineage(ctx, dir, "t1"); err != nil {
			t.Fatal(err)
		}
		after, _ := os.ReadDir(dir)
		if len(after) != len(before) {
			t.Errorf("directory had %d entries, now %d", len(before), len(after))
		}
	})

	t.Run("odd source JSON shapes", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			source string
			want   Lineage
		}{
			{"subagent false", `{"subagent":false}`, Lineage{}},
			{"subagent true", `{"subagent":true}`, Lineage{}},
			{"subagent number", `{"subagent":0}`, Lineage{}},
			{"subagent array", `{"subagent":[]}`, Lineage{}},
			{"subagent empty string", `{"subagent":""}`, Lineage{}},
			{"subagent spaced null", `{"subagent":  null }`, Lineage{}},
			{"subagent compact variant", `{"subagent":"compact"}`, Lineage{Subagent: true}},
			{"non-string parent_thread_id", `{"subagent":{"thread_spawn":{"parent_thread_id":123}}}`, Lineage{Subagent: true}},
			{"non-object thread_spawn", `{"subagent":{"thread_spawn":"unexpected"}}`, Lineage{Subagent: true}},
			{"empty object", `{}`, Lineage{}},
			{"json array", `[1,2]`, Lineage{}},
			{"malformed json", `{"subagent":`, Lineage{}},
			{"empty string", ``, Lineage{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				sub, parent := subagentSource(tc.source)
				if got := (Lineage{Subagent: sub, Parent: parent}); got != tc.want {
					t.Errorf("subagentSource(%q) = %+v, want %+v", tc.source, got, tc.want)
				}
			})
		}
	})
}

// Against a real Codex state, in MURMUR_REAL_CODEX_DIR, when MURMUR_REAL_CODEX_THREADS names
// threads to read, as "id=subagent,id=person": the kind each must be read as.
func TestCodexLineageOfRealThreads(t *testing.T) {
	spec := os.Getenv("MURMUR_REAL_CODEX_THREADS")
	if spec == "" {
		t.Skip("MURMUR_REAL_CODEX_THREADS isn't set")
	}
	for _, pair := range strings.Split(spec, ",") {
		id, kind, _ := strings.Cut(pair, "=")
		got, err := CodexLineage(context.Background(), os.Getenv("MURMUR_REAL_CODEX_DIR"), id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		t.Logf("%s: %+v", id, got)
		if got.Subagent != (kind == "subagent") {
			t.Errorf("%s was read as %+v, want %s", id, got, kind)
		}
	}
}
