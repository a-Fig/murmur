package here

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// A command finds its session's server by the agent's process: its nearest ancestor that is some
// server's parent, and CLAUDE_PID only when the ancestors couldn't be read.
func TestCallerIsTheNearestAgent(t *testing.T) {
	a := Session{ServerPID: 10, ClientPID: 100}
	b := Session{ServerPID: 20, ClientPID: 200}
	all := []Session{a, b}
	if got := Caller(all, []int{7, 200, 100}, 0, ""); !slices.Equal(got, []Session{b}) {
		t.Errorf("by ancestors: %+v, want the nearest, %+v", got, b)
	}
	// An agent that Claude Code (100) started inherits CLAUDE_PID: its own session (200), the
	// nearer ancestor, is the one its commands move.
	if got := Caller(all, []int{7, 200, 100}, 100, ""); !slices.Equal(got, []Session{b}) {
		t.Errorf("nested under Claude Code: %+v, want the nearer %+v", got, b)
	}
	if got := Caller(all, nil, 100, ""); !slices.Equal(got, []Session{a}) {
		t.Errorf("CLAUDE_PID without ancestors: %+v, want %+v", got, a)
	}
	if got := Caller(all, []int{7, 8}, 0, ""); got != nil {
		t.Errorf("no agent among the ancestors: %+v", got)
	}
}

// One process running several sessions gives them all back, unless Claude Code's session ID picks one.
func TestCallerOfASharedProcess(t *testing.T) {
	a := Session{ServerPID: 10, ClientPID: 100, ClaudeSession: "aaa"}
	b := Session{ServerPID: 11, ClientPID: 100, ClaudeSession: "bbb"}
	if got := Caller([]Session{a, b}, []int{100}, 0, ""); len(got) != 2 {
		t.Errorf("can't tell them apart, yet got %+v", got)
	}
	if got := Caller([]Session{a, b}, []int{100}, 0, "bbb"); !slices.Equal(got, []Session{b}) {
		t.Errorf("by session ID: %+v, want %+v", got, b)
	}
}

// A move reaches the server of that agent process, and only with the same start time and Claude
// session: a new process that took a dead one's PID has others.
func TestAskedMatchesTheSession(t *testing.T) {
	dir := t.TempDir()
	s := Session{ServerPID: 10, ClientPID: 100, ClientStarted: "t1", ClaudeSession: "aaa"}
	at := time.Now()
	if err := Ask(dir, s, "/work/repo", at); err != nil {
		t.Fatal(err)
	}
	m, ok := Asked(dir, Session{ServerPID: 11, ClientPID: 100, ClientStarted: "t1", ClaudeSession: "aaa"})
	if !ok || m.Dir != "/work/repo" || !m.At.Equal(at) {
		t.Errorf("Asked by a restarted server of the same session = %+v, %v", m, ok)
	}
	for name, other := range map[string]Session{
		"another Claude session":                  {ClientPID: 100, ClientStarted: "t1", ClaudeSession: "zzz"},
		"another process":                         {ClientPID: 101, ClientStarted: "t1", ClaudeSession: "aaa"},
		"a later process with the PID, no Claude": {ClientPID: 100, ClientStarted: "t2"},
	} {
		if _, ok := Asked(dir, other); ok {
			t.Errorf("a move was taken by %s", name)
		}
	}
}

// A file still being written is neither a session nor removed: two records of one server would
// look like two sessions, and removing it would lose the write.
func TestSessionsSkipsFilesBeingWritten(t *testing.T) {
	dir := t.TempDir()
	live := Session{ServerPID: os.Getpid(), ClientPID: os.Getppid()}
	if err := Publish(dir, live); err != nil {
		t.Fatal(err)
	}
	tmp := sessionPath(dir, live.ServerPID) + ".99.tmp"
	if err := os.WriteFile(tmp, []byte(`{"serverPid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := Sessions(dir)
	if !slices.Equal(got, []Session{live}) {
		t.Errorf("Sessions = %+v, want only %+v", got, live)
	}
	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("the file being written was removed: %v", err)
	}
}

func TestStartedTellsProcessesApart(t *testing.T) {
	if Started(os.Getpid()) == "" || Started(os.Getpid()) != Started(os.Getpid()) {
		t.Errorf("Started(self) = %q, not stable", Started(os.Getpid()))
	}
	if Started(exitedPID(t)) != "" {
		t.Error("an exited process has a start time")
	}
}

// Records of servers that exited, and moves for agents that exited, are cleared away.
func TestSessionsDropsTheDead(t *testing.T) {
	dir := t.TempDir()
	dead := exitedPID(t)
	live := Session{ServerPID: os.Getpid(), ClientPID: os.Getppid()}
	for _, s := range []Session{live, {ServerPID: dead, ClientPID: os.Getppid()}} {
		if err := Publish(dir, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := Ask(dir, Session{ClientPID: dead}, "/x", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := Sessions(dir)
	if err != nil || !slices.Equal(got, []Session{live}) {
		t.Fatalf("Sessions = %+v, %v; want only %+v", got, err, live)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 {
		t.Errorf("files left: %q, want only the live record", left)
	}
	Withdraw(dir, live.ServerPID)
	if got, _ := Sessions(dir); len(got) != 0 {
		t.Errorf("after Withdraw: %+v", got)
	}
}

func TestAncestorsStartWithTheParent(t *testing.T) {
	a := Ancestors()
	if len(a) == 0 || a[0] != os.Getppid() {
		t.Errorf("Ancestors = %v, want the parent %d first", a, os.Getppid())
	}
}

func exitedPID(t *testing.T) int {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A record that can't be read is left for its server, which is still writing it.
func TestSessionsLeavesAnUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	path := sessionPath(dir, 12345)
	if err := os.WriteFile(path, []byte(`{"serverPid":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := Sessions(dir); len(got) != 0 {
		t.Errorf("Sessions = %+v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("an unreadable record was removed: %v", err)
	}
}

// The hook asks for the servers of its agent's process on every tool call: those still running that
// the nearest of the processes it names started, and it removes no record on the way.
func TestOfIsTheNearestProcessesLiveServers(t *testing.T) {
	dir := t.TempDir()
	me, parent := os.Getpid(), os.Getppid()
	mine := Session{ServerPID: me, ClientPID: 100, GitDir: "/a/.git", On: true}
	sibling := Session{ServerPID: parent, ClientPID: 100, GitDir: "/b/.git"}
	above := Session{ServerPID: me + 1<<22, ClientPID: 200, GitDir: "/c/.git"} // no such process
	for i, s := range []Session{mine, sibling, above} {
		// Written under names of their own: two of them have one server PID.
		if err := writeJSON(filepath.Join(dir, "server-"+string(rune('a'+i))+".json"), s); err != nil {
			t.Fatal(err)
		}
	}
	if got := Of(dir, []int{7, 100, 200}); !slices.Equal(got, []Session{mine, sibling}) {
		t.Errorf("of the nearest process: %+v", got)
	}
	if got := Of(dir, []int{200}); got != nil {
		t.Errorf("a server that isn't running was returned: %+v", got)
	}
	// A dead server's record is passed over for the next process up.
	if got := Of(dir, []int{200, 100}); len(got) != 2 {
		t.Errorf("past a process whose server is gone: %+v", got)
	}
	if got := Of(dir, nil); got != nil {
		t.Errorf("with no process named: %+v", got)
	}
	if got := Of(filepath.Join(dir, "none"), []int{100}); got != nil {
		t.Errorf("with no directory: %+v", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Errorf("Of removed a record: %d left", len(entries))
	}
}
