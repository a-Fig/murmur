package title

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/t3db"
)

// The readers read fixtures in temporary directories: Codex's state as it was on codex-cli 0.164.0
// and a Claude Code transcript as of 2.1.312 (2026-10-30). Nothing here reads a real home.

func TestMain(m *testing.M) {
	empty, err := os.MkdirTemp("", "title-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("USERPROFILE", empty) // os.UserHomeDir on Windows
	os.Setenv("HOME", empty)
	os.Unsetenv("CODEX_HOME")
	code := m.Run()
	os.RemoveAll(empty)
	os.Exit(code)
}

func writeDB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

const codexSchema = `CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT, title TEXT, preview TEXT, rollout_path TEXT)`

func TestCodexReadsTheExplicitNameAndNotTheOpeningMessage(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), codexSchema,
		`INSERT INTO threads VALUES ('named', 'Add benchmark tab', 'Work on your own branch, open a PR', 'Work on your own branch', 'x')`,
		`INSERT INTO threads VALUES ('unnamed', NULL, 'introduce yourself to the other session', 'introduce yourself', 'x')`)
	ctx := context.Background()
	if got, err := Codex(ctx, dir, "named"); err != nil || got != "Add benchmark tab" {
		t.Errorf("named thread: %q, %v", got, err)
	}
	if got, err := Codex(ctx, dir, "unnamed"); err != nil || got != "" {
		t.Errorf("a thread with no name gave %q, %v; its title and preview are its opening message", got, err)
	}
	if got, err := Codex(ctx, dir, "no-such-thread"); err != nil || got != "" {
		t.Errorf("an unknown thread: %q, %v", got, err)
	}
}

func TestCodexFallsBackToTheSessionIndexAndTakesTheLastName(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), codexSchema, `INSERT INTO threads VALUES ('t1', NULL, 'opening', 'opening', 'x')`)
	index := `{"id":"t1","thread_name":"First name","updated_at":"2026-10-30T00:00:00Z"}` + "\n" +
		`not json at all` + "\n" +
		`{"id":"t2","thread_name":"Another thread's","updated_at":"2026-10-30T00:30:00Z"}` + "\n" +
		`{"id":"t1","thread_name":"Second name","updated_at":"2026-10-30T01:00:00Z"}` // no final newline
	if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := Codex(context.Background(), dir, "t1"); err != nil || got != "Second name" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCodexTakesTheNewestStateDatabase(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), codexSchema, `INSERT INTO threads VALUES ('t1', 'from five', '', '', 'x')`)
	writeDB(t, filepath.Join(dir, "state_12.sqlite"), codexSchema, `INSERT INTO threads VALUES ('t1', 'from twelve', '', '', 'x')`)
	if got, err := Codex(context.Background(), dir, "t1"); err != nil || got != "from twelve" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestCodexFailsSoftly(t *testing.T) {
	ctx := context.Background()
	t.Run("nothing there", func(t *testing.T) {
		if got, err := Codex(ctx, t.TempDir(), "t1"); err == nil || got != "" {
			t.Errorf("an empty directory: %q, %v", got, err)
		}
		if _, err := Codex(ctx, filepath.Join(t.TempDir(), "nope"), "t1"); err == nil {
			t.Errorf("a directory that isn't there gave no error")
		}
		if _, err := Codex(ctx, "", "t1"); err == nil {
			t.Errorf("no directory gave no error")
		}
	})
	t.Run("another schema", func(t *testing.T) {
		dir := t.TempDir()
		// No name column, as in an older Codex; and a table by another name, as in a newer.
		writeDB(t, filepath.Join(dir, "state_5.sqlite"), `CREATE TABLE threads (id TEXT, title TEXT)`, `INSERT INTO threads VALUES ('t1', 'opening')`)
		if got, err := Codex(ctx, dir, "t1"); err == nil || got != "" {
			t.Errorf("no name column: %q, %v", got, err)
		}
		other := t.TempDir()
		writeDB(t, filepath.Join(other, "state_5.sqlite"), `CREATE TABLE conversations (id TEXT, name TEXT)`)
		if got, err := Codex(ctx, other, "t1"); err == nil || got != "" {
			t.Errorf("no threads table: %q, %v", got, err)
		}
		// The index still answers for a state it can't read.
		if err := os.WriteFile(filepath.Join(other, "session_index.jsonl"), []byte(`{"id":"t1","thread_name":"Indexed"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := Codex(ctx, other, "t1"); err != nil || got != "Indexed" {
			t.Errorf("the index with an unreadable database: %q, %v", got, err)
		}
		// Renamed id column errors cleanly rather than misreading.
		renamedID := t.TempDir()
		writeDB(t, filepath.Join(renamedID, "state_5.sqlite"), `CREATE TABLE threads (thread_id TEXT, name TEXT)`, `INSERT INTO threads VALUES ('t1', 'named')`)
		if got, err := Codex(ctx, renamedID, "t1"); err == nil || got != "" {
			t.Errorf("renamed id column: %q, %v", got, err)
		}
		// Extra columns added by a newer Codex release change nothing.
		extra := t.TempDir()
		writeDB(t, filepath.Join(extra, "state_5.sqlite"), `CREATE TABLE threads (id TEXT PRIMARY KEY, extra_mid TEXT, name TEXT, extra_end INTEGER)`, `INSERT INTO threads VALUES ('t1', 'x', 'Still works', 42)`)
		if got, err := Codex(ctx, extra, "t1"); err != nil || got != "Still works" {
			t.Errorf("extra columns: %q, %v", got, err)
		}
	})
	t.Run("not a database", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("this is not SQLite, it is a long enough line to look like a file"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := Codex(ctx, dir, "t1"); err == nil || got != "" {
			t.Errorf("%q, %v", got, err)
		}
	})
	t.Run("locked", func(t *testing.T) {
		defer func(b, tot time.Duration) { t3db.Busy, Total = b, tot }(t3db.Busy, Total)
		t3db.Busy, Total = 100*time.Millisecond, time.Second
		dir := t.TempDir()
		path := filepath.Join(dir, "state_5.sqlite")
		writeDB(t, path, codexSchema, `INSERT INTO threads VALUES ('t1', 'named', '', '', 'x')`)
		db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		// An exclusive lock, as a writer in the middle of a commit holds.
		if _, err := db.Exec(`BEGIN EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		got, err := Codex(context.Background(), dir, "t1")
		if err == nil || got != "" {
			t.Errorf("a locked database gave %q, %v", got, err)
		}
		if time.Since(start) > 3*time.Second {
			t.Errorf("waited %s for the lock", time.Since(start))
		}
	})
}

func TestCodexWritesNothing(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), codexSchema, `INSERT INTO threads VALUES ('t1', 'named', '', '', 'x')`)
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Codex(context.Background(), dir, "t1"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Errorf("the directory had %d entries and has %d", len(before), len(after))
	}
}

// The default home is ~/.codex whatever this process's CODEX_HOME says: in the daemon, that is the
// home of the session that started it.
func TestDefaultCodexDirIgnoresCodexHome(t *testing.T) {
	h, _ := os.UserHomeDir()
	for _, env := range []string{"/somewhere/codex", ""} {
		t.Setenv("CODEX_HOME", env)
		if got := DefaultCodexDir(); got != filepath.Join(h, ".codex") {
			t.Errorf("CODEX_HOME=%q: got %q", env, got)
		}
	}
}

func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

func transcript(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClaudeTitleIsTheLastCustomTitleElseTheLastAITitle(t *testing.T) {
	prompt := `{"type":"user","message":{"role":"user","content":"the first prompt, which is not a title"}}`
	cases := []struct{ name, content, want string }{
		{"only a prompt", lines(prompt), ""},
		{"an AI title", lines(prompt, `{"type":"ai-title","aiTitle":"First idea","sessionId":"s"}`), "First idea"},
		{"the last AI title", lines(`{"type":"ai-title","aiTitle":"First idea","sessionId":"s"}`, prompt,
			`{"type":"ai-title","aiTitle":"Better idea","sessionId":"s"}`), "Better idea"},
		{"a name beats an AI title written after it", lines(`{"type":"custom-title","customTitle":"My name","sessionId":"s"}`,
			`{"type":"ai-title","aiTitle":"Machine's idea","sessionId":"s"}`), "My name"},
		{"the last name", lines(`{"type":"custom-title","customTitle":"Old name","sessionId":"s"}`, prompt,
			`{"type":"custom-title","customTitle":"New name","sessionId":"s"}`), "New name"},
		{"text that only looks like a title", lines(`{"type":"user","message":{"content":"{\"type\":\"ai-title\",\"aiTitle\":\"fake\"}"}}`), ""},
		{"a half-written last line", lines(`{"type":"ai-title","aiTitle":"Whole","sessionId":"s"}`) + `{"type":"ai-title","aiTitle":"Hal`, "Whole"},
		{"no trailing newline", `{"type":"ai-title","aiTitle":"Last line","sessionId":"s"}`, "Last line"},
		{"an empty file", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, err := Claude(transcript(t, c.content)); err != nil || got != c.want {
				t.Errorf("got %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// Claude Code keeps the titles near the end of a transcript that can be many megabytes; they are
// found there, and a title further back than the window is not looked for.
func TestClaudeTitleIsFoundInTheEndOfALargeTranscript(t *testing.T) {
	filler := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", 4000) + `"}}` + "\n"
	big := strings.Repeat(filler, 100) // 400 KB
	p := transcript(t, `{"type":"ai-title","aiTitle":"Too far back","sessionId":"s"}`+"\n"+big)
	if got, err := Claude(p); err != nil || got != "" {
		t.Errorf("a title 400 KB back: %q, %v", got, err)
	}
	p = transcript(t, `{"type":"ai-title","aiTitle":"Too far back","sessionId":"s"}`+"\n"+big+
		`{"type":"custom-title","customTitle":"Near the end","sessionId":"s"}`+"\n"+filler[:len(filler)/2])
	if got, err := Claude(p); err != nil || got != "Near the end" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestClaudeFailsSoftly(t *testing.T) {
	if _, err := Claude(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Errorf("a missing transcript gave no error")
	}
	if _, err := Claude(t.TempDir()); err == nil {
		t.Errorf("a directory gave no error")
	}
	if got, err := Claude(transcript(t, "\x00\x01 not json {{{\n\n[1,2]\n")); err != nil || got != "" {
		t.Errorf("garbage: %q, %v", got, err)
	}
}

// codexIndex streams oversized lines past maxIndexLine without buffering them whole, reads
// multi-buffer lines under the cap, and fails cleanly if session_index.jsonl is a directory.
func TestCodexIndexBoundedLineReading(t *testing.T) {
	t.Run("oversized line skipped and later valid line still read", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "session_index.jsonl")
		oversized := `{"id":"t1","thread_name":"` + strings.Repeat("x", maxIndexLine+1024) + `"}` + "\n"
		content := `{"id":"t1","thread_name":"Before"}` + "\n" +
			oversized +
			`{"id":"t1","thread_name":"After"}` + "\n"
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := codexIndex(p, "t1"); err != nil || got != "After" {
			t.Errorf("got %q, %v; want After", got, err)
		}
	})
	t.Run("oversized final line without newline keeps earlier valid name", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "session_index.jsonl")
		content := `{"id":"t1","thread_name":"Kept"}` + "\n" +
			`{"id":"t1","thread_name":"` + strings.Repeat("y", maxIndexLine+1024) + `"}`
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := codexIndex(p, "t1"); err != nil || got != "Kept" {
			t.Errorf("got %q, %v; want Kept", got, err)
		}
	})
	t.Run("multi-buffer valid line under cap is read", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "session_index.jsonl")
		// Longer than the 64 KiB bufio buffer, well under maxIndexLine.
		pad := strings.Repeat("a", 80<<10)
		line := `{"id":"t1","pad":"` + pad + `","thread_name":"Long valid"}` + "\n"
		if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
		if got, err := codexIndex(p, "t1"); err != nil || got != "Long valid" {
			t.Errorf("got %q, %v; want Long valid", got, err)
		}
	})
	t.Run("directory returns error", func(t *testing.T) {
		if _, err := codexIndex(t.TempDir(), "t1"); err == nil {
			t.Error("directory session_index.jsonl gave no error")
		}
	})
}

// A transcript truncated between Stat and ReadAt returns a short read with io.EOF; claudeTail
// must slice to n bytes so trailing zero padding doesn't break JSON parsing on the final line.
func TestClaudeShortReadAtDropsStaleTail(t *testing.T) {
	line := `{"type":"ai-title","aiTitle":"Truncated cleanly","sessionId":"s"}`
	r := strings.NewReader(line)
	// Observed size (4096) is larger than the actual remaining bytes (len(line)).
	if got, err := claudeTail(r, 4096); err != nil || got != "Truncated cleanly" {
		t.Errorf("short ReadAt: got %q, %v; want %q", got, err, "Truncated cleanly")
	}
	// Truncated below the tail window offset: ReadAt returns (0, io.EOF).
	if got, err := claudeTail(r, tailBytes+4096); err != nil || got != "" {
		t.Errorf("truncated below offset: got %q, %v; want empty", got, err)
	}
}

func TestCleanCutsToAStatusLineAndDropsSecrets(t *testing.T) {
	if got := Clean("  two\nlines\tand\x1b[31m escapes  "); got != "two lines and [31m escapes" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", 300)
	if got := Clean(long); len([]rune(got)) != Max {
		t.Errorf("a long title is %d runes, want %d", len([]rune(got)), Max)
	}
	if got := Clean("deploy with sk-ant-" + strings.Repeat("a1b2c3", 8)); got != "" {
		t.Errorf("a title with a key in it: %q", got)
	}
	// A token that runs past the cut is still a token: cut, it would be too short to be known.
	crossing := strings.Repeat("x", 110) + " ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789AB"
	if got := Clean(crossing); got != "" {
		t.Errorf("a title with a token across the cut: %q", got)
	}
}

// An index that can't be read doesn't say the thread has no name: the error lets the caller keep
// the name it had.
func TestCodexIndexThatCantBeReadIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeDB(t, filepath.Join(dir, "state_5.sqlite"), codexSchema, `INSERT INTO threads VALUES ('t1', NULL, 'opening', 'opening', 'x')`)
	if err := os.Mkdir(filepath.Join(dir, "session_index.jsonl"), 0o755); err != nil { // opens, but can't be read
		t.Fatal(err)
	}
	if got, err := Codex(context.Background(), dir, "t1"); err == nil || got != "" {
		t.Errorf("an unreadable index with an unnamed thread in the database: %q, %v", got, err)
	}
}
