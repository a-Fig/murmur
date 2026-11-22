package mcpserver

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/here"
)

// mine is this test process's server's record in dir, if it has published one.
func mine(t *testing.T, dir string) (here.Session, bool) {
	t.Helper()
	all, err := here.Sessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if s.ServerPID == os.Getpid() {
			return s, true
		}
	}
	return here.Session{}, false
}

func joinedRepo(t *testing.T, f *fakeDaemon) (top, gitDir string) {
	t.Helper()
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	return p.Top, p.GitDir
}

// A session whose agent started outside any repo has no tools, but publishes a record, and when
// `mur join` asks it to move into a repo the user joined, it turns on there (#200).
func TestASessionStartedOutsideMovesIntoTheRepo(t *testing.T) {
	hdir := t.TempDir()
	f := &fakeDaemon{}
	top, gitDir := joinedRepo(t, f)
	cs, changed := openWith(t, Options{Agent: "claude-code", Version: "test", Dir: t.TempDir(), Poll: 25 * time.Millisecond, HereDir: hdir})

	var rec here.Session
	waitFor(t, "the server's record", func() (ok bool) { rec, ok = mine(t, hdir); return })
	if rec.ClientPID != os.Getppid() || rec.On || rec.GitDir != "" {
		t.Fatalf("record outside any repo: %+v", rec)
	}
	settle()
	if names := toolNamesOf(t, cs); len(names) != 0 || f.helloCount() != 0 {
		t.Fatalf("on outside any repo: tools %q, hellos %d", names, f.helloCount())
	}

	if err := here.Ask(hdir, rec, top, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tools to appear", func() bool { return offersTools(toolNamesOf(t, cs)) })
	waitFor(t, "the client to be told the tool list changed", func() bool { return changed.Load() > 0 })
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	if h := f.lastHello(); h.Cwd != top {
		t.Errorf("registered with cwd %q, want the repo it moved to, %q", h.Cwd, top)
	}
	waitFor(t, "the record to say it is on there", func() bool {
		r, _ := mine(t, hdir)
		return r.On && r.GitDir == gitDir
	})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil || res.IsError {
		t.Fatalf("who: %v %+v", err, res)
	}
}

func TestAntigravityPublishesT3ThreadBeforeAndAfterMove(t *testing.T) {
	t.Setenv(threadEnv, "thread-1")
	t.Setenv("ANTIGRAVITY_CONVERSATION_ID", "")
	hdir := t.TempDir()
	top, gitDir := joinedRepo(t, &fakeDaemon{})
	openWith(t, Options{Agent: "antigravity", Dir: t.TempDir(), Poll: 25 * time.Millisecond, HereDir: hdir})
	var rec here.Session
	waitFor(t, "the passive server's record", func() (ok bool) { rec, ok = mine(t, hdir); return })
	if rec.On || rec.T3Thread != "thread-1" || rec.Native != "" {
		t.Fatalf("passive record: %+v", rec)
	}
	if err := here.Ask(hdir, rec, top, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the moved server's record", func() bool {
		rec, _ = mine(t, hdir)
		return rec.On && rec.GitDir == gitDir
	})
	if rec.T3Thread != "thread-1" || rec.Native != "" {
		t.Errorf("moved record: %+v", rec)
	}
}

func TestHerePublishesKnownT3ThreadWithoutInheritingParentThread(t *testing.T) {
	t.Setenv(threadEnv, "parent-thread")
	old := agentAbove
	agentAbove = func() bool { return true }
	t.Cleanup(func() { agentAbove = old })
	s := &server{opt: Options{Agent: "antigravity", HereDir: t.TempDir()}}
	s.initHere()
	s.publish()
	if r, ok := mine(t, s.hereDir); !ok || r.T3Thread != "" {
		t.Fatalf("record for nested client: %+v", r)
	}
	s.link = &link{}
	s.link.hello.T3Thread = "known-thread"
	s.publish()
	if r, ok := mine(t, s.hereDir); !ok || r.T3Thread != "known-thread" {
		t.Errorf("record once thread is known: %+v", r)
	}
}

// A session is in one repo at a time: moving to another takes it out of the first, and its calls go
// to the new repo's daemon.
func TestAMoveLeavesTheRepoItWasIn(t *testing.T) {
	hdir := t.TempDir()
	fa, fb := &fakeDaemon{}, &fakeDaemon{}
	topA, _ := joinedRepo(t, fa)
	topB, gitB := joinedRepo(t, fb)
	cs, _ := openWith(t, Options{Agent: "claude-code", Version: "test", Dir: topA, Poll: 25 * time.Millisecond, HereDir: hdir})
	waitFor(t, "the session to register in A", func() bool { return fa.helloCount() == 1 })

	rec, _ := mine(t, hdir)
	if err := here.Ask(hdir, rec, topB, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to register in B", func() bool { return fb.helloCount() == 1 })
	waitFor(t, "the record to say it is in B", func() bool { r, _ := mine(t, hdir); return r.On && r.GitDir == gitB })

	before := fa.requestCount()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "who"})
	if err != nil || res.IsError {
		t.Fatalf("who: %v %+v", err, res)
	}
	if fa.requestCount() != before || fb.requestCount() == 0 {
		t.Errorf("who went to A %d times and B %d times after the move", fa.requestCount()-before, fb.requestCount())
	}
	settle()
	if fa.helloCount() != 1 {
		t.Errorf("registered in A again after moving: %d hellos", fa.helloCount())
	}
}

// OpenCode's one server serves every session in its directory, so it can't be moved for one of
// them. It still publishes a record, marked fixed: without one, join would look further up the
// process tree and move the Claude Code session that started OpenCode. A move left for it anyway
// is ignored.
func TestASharedServerPublishesAFixedRecordAndStays(t *testing.T) {
	hdir := t.TempDir()
	f := &fakeDaemon{}
	top, _ := joinedRepo(t, f)
	openWith(t, Options{Agent: "opencode", Version: "test", Dir: t.TempDir(), Poll: 25 * time.Millisecond, HereDir: hdir})
	var rec here.Session
	waitFor(t, "the server's record", func() (ok bool) { rec, ok = mine(t, hdir); return })
	if !rec.Fixed {
		t.Fatalf("a shared server's record isn't fixed: %+v", rec)
	}
	if err := here.Ask(hdir, rec, top, time.Now()); err != nil {
		t.Fatal(err)
	}
	settle()
	if r, _ := mine(t, hdir); r.GitDir != "" || r.On || f.helloCount() != 0 {
		t.Errorf("a shared server moved: record %+v, hellos %d", r, f.helloCount())
	}
}

// A move into a repo the user hasn't joined does nothing there: consent is per clone, and moving
// doesn't stand in for it.
func TestAMoveIntoARepoNotJoinedStaysOff(t *testing.T) {
	hdir := t.TempDir()
	f := &fakeDaemon{}
	p := repo(t, true, f)
	cs, _ := openWith(t, Options{Agent: "claude-code", Version: "test", Dir: t.TempDir(), Poll: 25 * time.Millisecond, HereDir: hdir})
	var rec here.Session
	waitFor(t, "the server's record", func() (ok bool) { rec, ok = mine(t, hdir); return })
	if err := here.Ask(hdir, rec, p.Top, time.Now()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the record to say where it is", func() bool { r, _ := mine(t, hdir); return r.GitDir == p.GitDir })
	settle()
	if names := toolNamesOf(t, cs); len(names) != 0 || f.helloCount() != 0 || spawned(p) {
		t.Errorf("acted in a repo the user hasn't joined: tools %q, hellos %d, spawned %v", names, f.helloCount(), spawned(p))
	}
	// Joining afterwards turns it on, as for a session that started there.
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
}

// A record removed from under a running server comes back.
func TestARemovedRecordIsWrittenAgain(t *testing.T) {
	hdir := t.TempDir()
	openWith(t, Options{Agent: "claude-code", Version: "test", Dir: t.TempDir(), Poll: 25 * time.Millisecond, HereDir: hdir})
	waitFor(t, "the server's record", func() bool { _, ok := mine(t, hdir); return ok })
	here.Withdraw(hdir, os.Getpid())
	waitFor(t, "the record to be written again", func() bool { _, ok := mine(t, hdir); return ok })
}
