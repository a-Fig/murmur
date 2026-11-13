package daemon_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"

	_ "modernc.org/sqlite"
)

// Tests of the conversation's title as the default status line (#66). Each client's state is a
// fixture in a temporary directory; the tests' TestMain points the home directory at an empty one,
// so none of them can read the real ~/.t3, ~/.codex or ~/.claude.

// openFixtureDB makes a SQLite database at path for the fixture to write, as the client holds its
// own: in WAL mode, as T3's database is, so the daemon reading it doesn't lock the test's writes out, and
// waiting on a lock a moment rather than failing (CI saw "database is locked" without both).
func openFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// t3Fixture makes a T3 data directory whose database holds threads (ID → title), and returns the
// directory and the database, for changing it.
func t3Fixture(t *testing.T, threads map[string]string) (home string, db *sql.DB) {
	t.Helper()
	home = t.TempDir()
	db = openFixtureDB(t, t3.StateDB(home))
	if _, err := db.Exec(`CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatal(err)
	}
	for id, title := range threads {
		if _, err := db.Exec(`INSERT INTO orchestration_v2_projection_threads VALUES (?, ?)`, id, title); err != nil {
			t.Fatal(err)
		}
	}
	return home, db
}

// t3Route is the route of a session that is T3 thread, in the data directory home.
func t3Route(home, thread string) proto.Route {
	return proto.Route{T3CLI: filepath.Join(home, "bin", "t3"), T3Home: home, T3Thread: thread}
}

// doingIn is the status line that who, asked by session as, shows for the session at addr, and
// whether who shows the session at all.
func (d *device) doingIn(as, addr string) (string, bool) {
	d.t.Helper()
	for _, dv := range d.do(proto.Request{Op: proto.OpWho, Session: as}).Devices {
		for _, s := range dv.Sessions {
			if s.Addr == addr {
				return s.Doing, true
			}
		}
	}
	return "", false
}

func (d *device) waitDoing(as, addr, want string) {
	d.t.Helper()
	eventually(d.t, "who to show "+want, func() bool {
		got, ok := d.doingIn(as, addr)
		return ok && got == want
	})
}

// A session in T3 shows its thread's title where it set no status, to itself and, in presence, to a
// teammate's device; a status it sets wins, and clearing it brings the title back; a rename in T3
// shows within a few seconds.
func TestT3ThreadTitleIsTheDefaultStatus(t *testing.T) {
	t.Parallel()
	home, db := t3Fixture(t, map[string]string{"thread-a": "Fix the login redirect"})
	remote := newRemote(t)
	a, b := newDevice(t, remote), newDevice(t, remote)
	alice, _ := a.sessionRoute("aaaaaa", "claude-alice", t3Route(home, "thread-a"))
	eventually(t, "presence", func() bool { return b.seesDevices(2) })

	a.waitDoing("aaaaaa", alice, "Fix the login redirect")
	b.waitDoing(proto.CLISession, alice, "Fix the login redirect")

	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: "moving auth middleware"})
	a.waitDoing("aaaaaa", alice, "moving auth middleware")
	if _, err := db.Exec(`UPDATE orchestration_v2_projection_threads SET title = 'Renamed while a status is set'`); err != nil {
		t.Fatal(err)
	}
	b.waitDoing(proto.CLISession, alice, "moving auth middleware")
	holds(t, "the status over the title", time.Second, func() bool {
		got, _ := a.doingIn("aaaaaa", alice)
		return got == "moving auth middleware"
	})

	a.do(proto.Request{Op: proto.OpStatus, Session: "aaaaaa", Doing: ""})
	a.waitDoing("aaaaaa", alice, "Renamed while a status is set")
	b.waitDoing(proto.CLISession, alice, "Renamed while a status is set")
}

// A title is cut to a status line's length, and one that holds a secret is not shown.
func TestTitleIsCutAndScrubbed(t *testing.T) {
	t.Parallel()
	home, _ := t3Fixture(t, map[string]string{
		"thread-long":   strings.Repeat("long title ", 30),
		"thread-secret": "debug with sk-ant-" + strings.Repeat("a1b2c3", 8),
		"thread-ok":     "Plain title",
	})
	a := newDevice(t, newRemote(t))
	long, _ := a.sessionRoute("aaaaaa", "", t3Route(home, "thread-long"))
	secret, _ := a.sessionRoute("bbbbbb", "", t3Route(home, "thread-secret"))
	ok, _ := a.sessionRoute("cccccc", "", t3Route(home, "thread-ok"))
	a.waitDoing("cccccc", ok, "Plain title")
	eventually(t, "the long title", func() bool {
		got, _ := a.doingIn("cccccc", long)
		return got != ""
	})
	if got, _ := a.doingIn("cccccc", long); len([]rune(got)) > 120 {
		t.Errorf("the long title is shown as %d characters", len([]rune(got)))
	}
	if got, _ := a.doingIn("cccccc", secret); got != "" {
		t.Errorf("a title with a secret in it is shown: %q", got)
	}
}

// What is wrong with the client's state costs the title, not the session: a title already shown
// stays through a database that can't be read as before, and a session whose thread has no row, or
// whose thread isn't known, shows none.
func TestUnreadableTitleSourceKeepsTheLastTitle(t *testing.T) {
	t.Parallel()
	home, db := t3Fixture(t, map[string]string{"thread-a": "Kept title"})
	a := newDevice(t, newRemote(t))
	alice, _ := a.sessionRoute("aaaaaa", "", t3Route(home, "thread-a"))
	nobody, _ := a.sessionRoute("bbbbbb", "", t3Route(home, "thread-none"))
	nothread, _ := a.sessionRoute("cccccc", "", proto.Route{T3CLI: filepath.Join(home, "bin", "t3")})
	a.waitDoing("aaaaaa", alice, "Kept title")

	// T3 moves the table, as a newer schema might.
	if _, err := db.Exec(`ALTER TABLE orchestration_v2_projection_threads RENAME TO threads_v3`); err != nil {
		t.Fatal(err)
	}
	holds(t, "the last title", time.Second, func() bool {
		got, _ := a.doingIn("aaaaaa", alice)
		return got == "Kept title"
	})
	for _, addr := range []string{nobody, nothread} {
		if got, ok := a.doingIn("aaaaaa", addr); !ok || got != "" {
			t.Errorf("%s shows %q", addr, got)
		}
	}
}

// A Codex session outside T3 shows the thread's explicit name, from its state database or its
// session index, and not the opening message that Codex keeps in the same row.
func TestCodexNameOutsideT3(t *testing.T) {
	// Not parallel: it writes into the CODEX_HOME that TestMain sets for the whole package.
	dir := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { os.RemoveAll(dir) })
	db := openFixtureDB(t, filepath.Join(dir, "state_5.sqlite"))
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT, title TEXT, preview TEXT)`); err != nil {
		t.Fatal(err)
	}
	opening := "introduce yourself to the other session"
	for _, r := range [][]any{{"cx-unnamed", nil, opening, opening}, {"cx-named", "Add benchmark tab", opening, opening},
		{"cx-indexed", nil, opening, opening}} {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, ?)`, r...); err != nil {
			t.Fatal(err)
		}
	}
	index := `{"id":"cx-indexed","thread_name":"Old name","updated_at":"2026-10-30T00:00:00Z"}` + "\n" +
		`{"id":"cx-indexed","thread_name":"Name from the index","updated_at":"2026-10-30T01:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "session_index.jsonl"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	a := newDevice(t, newRemote(t))
	unnamed, _ := a.sessionRoute("aaaaaa", "", proto.Route{Native: "cx-unnamed"})
	named, _ := a.sessionRoute("bbbbbb", "", proto.Route{Native: "cx-named"})
	indexed, _ := a.sessionRoute("cccccc", "", proto.Route{Native: "cx-indexed"})
	a.waitDoing("aaaaaa", named, "Add benchmark tab")
	a.waitDoing("aaaaaa", indexed, "Name from the index")
	if got, _ := a.doingIn("aaaaaa", unnamed); got != "" {
		t.Errorf("a thread with no name shows %q, its opening message", got)
	}
}

// A daemon started by a session with its own Codex home has that CODEX_HOME in its environment, as
// the server it inherits from does once join forwards the variable. A session in the default home,
// whose server names ~/.codex, is read there, not from the home of whoever started the daemon: a
// T3 thread in a provider home, then plain `codex` in a terminal (#88's review). A server from
// before #88 names no home, and its session is read from the daemon's CODEX_HOME, as on main then
// (Sol's review of #301).
func TestCodexNameIsReadFromTheHomeItsServerNames(t *testing.T) {
	thread := func(dir, id, name string) {
		db := openFixtureDB(t, filepath.Join(dir, "state_5.sqlite"))
		if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?)`, id, name); err != nil {
			t.Fatal(err)
		}
	}
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(h, ".codex") // the test's empty home, from TestMain
	t.Cleanup(func() { os.RemoveAll(def) })
	provider := t.TempDir()
	thread(provider, "cx-t3", "In the provider's home")
	thread(def, "cx-terminal", "In the default home")
	db := openFixtureDB(t, filepath.Join(provider, "state_5.sqlite"))
	if _, err := db.Exec(`INSERT INTO threads VALUES ('cx-old', 'In the daemon''s home')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", provider) // the first session's server started this daemon

	a := newDevice(t, newRemote(t))
	inT3, _ := a.sessionRoute("aaaaaa", "", proto.Route{Native: "cx-t3", CodexHome: provider})
	terminal, _ := a.sessionRoute("bbbbbb", "", proto.Route{Native: "cx-terminal", CodexHome: def})
	old, _ := a.sessionRoute("cccccc", "", proto.Route{Native: "cx-old"})
	a.waitDoing("aaaaaa", inT3, "In the provider's home")
	a.waitDoing("aaaaaa", terminal, "In the default home")
	a.waitDoing("aaaaaa", old, "In the daemon's home")
}

// One daemon serves every Codex session on the device, and they may not share a Codex home: each
// is read from the home its own MCP server named.
func TestCodexNameIsReadFromTheSessionsOwnHome(t *testing.T) {
	t.Parallel()
	homeOf := func(id, name string) string {
		dir := t.TempDir()
		db := openFixtureDB(t, filepath.Join(dir, "state_5.sqlite"))
		if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?)`, id, name); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	one, two := homeOf("cx-1", "Named in home one"), homeOf("cx-2", "Named in home two")
	a := newDevice(t, newRemote(t))
	first, _ := a.sessionRoute("aaaaaa", "", proto.Route{Native: "cx-1", CodexHome: one})
	second, _ := a.sessionRoute("bbbbbb", "", proto.Route{Native: "cx-2", CodexHome: two})
	a.waitDoing("aaaaaa", first, "Named in home one")
	a.waitDoing("aaaaaa", second, "Named in home two")
}

// A session that moves to another conversation doesn't keep the last one's title if the new one's
// can't be read: that title would be shown, and sent to teammates, as the new conversation's.
func TestTitleDoesNotOutliveItsConversation(t *testing.T) {
	t.Parallel()
	home, _ := t3Fixture(t, map[string]string{"thread-a": "First conversation"})
	a := newDevice(t, newRemote(t))
	alice, _ := a.sessionRoute("aaaaaa", "", t3Route(home, "thread-a"))
	a.waitDoing("aaaaaa", alice, "First conversation")

	elsewhere := t3Route(t.TempDir(), "thread-b") // a T3 data directory with no database to read
	if resp := a.do(proto.Request{Op: proto.OpRoute, Session: "aaaaaa", Route: &elsewhere}); resp.Error != "" {
		t.Fatal(resp.Error)
	}
	a.waitDoing("aaaaaa", alice, "")
	holds(t, "no title", 500*time.Millisecond, func() bool {
		got, _ := a.doingIn("aaaaaa", alice)
		return got == ""
	})
}

// A Claude Code session outside T3 shows the title in its transcript, which the hook names: the
// name the user gave it over the one Claude Code made up, and none rather than the first prompt.
// A new transcript, as after /clear, is a new conversation.
func TestClaudeTitleFromTheTranscriptTheHookNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	prompt := `{"type":"user","message":{"role":"user","content":"please rewrite everything"}}`
	auto := `{"type":"ai-title","aiTitle":"Rewrite the parser","sessionId":"s"}`
	first := write("one.jsonl", prompt, auto)
	second := write("two.jsonl", prompt)

	a := newDevice(t, newRemote(t))
	alice, _ := a.session("aaaaaa", "claude-alice")
	notice := func(path string) {
		a.do(proto.Request{Op: proto.OpNotices, ClaudeSession: "claude-alice", Transcript: path})
	}
	notice(first)
	a.waitDoing("aaaaaa", alice, "Rewrite the parser")

	write("one.jsonl", prompt, auto, `{"type":"custom-title","customTitle":"Parser, the sequel","sessionId":"s"}`)
	a.waitDoing("aaaaaa", alice, "Parser, the sequel")

	notice(second) // /clear: a conversation with a prompt and no title
	a.waitDoing("aaaaaa", alice, "")
	notice(filepath.Join(dir, "not-a-transcript.txt")) // not a path to read: ignored
	holds(t, "no title", 500*time.Millisecond, func() bool {
		got, _ := a.doingIn("aaaaaa", alice)
		return got == ""
	})
}

// A title is read every TitleEvery and no oftener: with a long one, none has been read yet.
func TestTitlesAreReadNoMoreOftenThanTitleEvery(t *testing.T) {
	t.Parallel()
	home, _ := t3Fixture(t, map[string]string{"thread-a": "Slow title"})
	dir := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(dir), "clone", "-q", "--no-local", newRemote(t), dir)
	d := &device{t: t, gitDir: filepath.Join(dir, ".git"), stale: testStale, poll: 100 * time.Millisecond,
		titleEvery: time.Hour}
	d.start()
	alice, _ := d.sessionRoute("aaaaaa", "", t3Route(home, "thread-a"))
	holds(t, "no title yet", 1500*time.Millisecond, func() bool {
		got, _ := d.doingIn("aaaaaa", alice)
		return got == ""
	})
}
