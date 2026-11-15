package hook_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// The hook finds its session's MCP server among the records of this user's (package here). The
// tests' are in a directory of their own, empty unless a test publishes one, so a hook here never
// finds a real session's, and none takes the test's process for Claude Code's.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hook-sessions")
	if err != nil {
		panic(err)
	}
	os.Setenv(brand.SessionsDirEnv, dir)
	os.Unsetenv("CLAUDE_PID")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// publish puts a server's record where the hook looks, for the length of the test.
func publish(t *testing.T, s here.Session) {
	t.Helper()
	dir, err := here.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := here.Publish(dir, s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { here.Withdraw(dir, s.ServerPID) })
}

// ops are the requests of one kind among those a fake daemon got.
func ops(requests []proto.Request, op string) []proto.Request {
	var out []proto.Request
	for _, r := range requests {
		if r.Op == op {
			out = append(out, r)
		}
	}
	return out
}

// A session that joined a clone it doesn't work in (`mur join -C`) has its hook speak to that
// clone's daemon, wherever the hook runs: in another repo, whose own daemon hears nothing, or in no
// repo at all (#422). A call is bracketed with the repository it ran in, which the daemon doesn't
// watch.
func TestTheHookFindsTheCloneItsSessionJoined(t *testing.T) {
	joined, works := newRepo(t), newRepo(t)
	theirs := fakeDaemon(t, filepath.Join(joined, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpTurnEnd {
			b, _ := json.Marshal(proto.Response{OK: true, Read: []proto.MessageView{{From: "dev-0001/aaaaaa", Subject: "s", Body: "b"}}})
			return string(b)
		}
		return `{"ok":true,"unread":2}`
	})
	own := fakeDaemon(t, filepath.Join(works, ".git"), okToAll)
	publish(t, here.Session{ServerPID: os.Getpid(), ClientPID: 4242, GitDir: filepath.Join(joined, ".git"), On: true})
	t.Setenv("CLAUDE_PID", "4242")

	for _, cwd := range []string{works, t.TempDir()} {
		if out, _ := run("PostToolUse", cwd); !strings.Contains(out, "2 messages unread") {
			t.Errorf("in %s the hook printed %q, want the count", cwd, out)
		}
	}
	if out, _ := run("Stop", works); !strings.Contains(out, `"decision":"block"`) {
		t.Errorf("Stop printed %q, want the turn kept going", out)
	}
	if got := own(); len(got) != 0 {
		t.Errorf("the daemon of the repo the session works in was asked %+v", got)
	}
	got := theirs()
	if n := len(ops(got, proto.OpNotices)); n != 2 {
		t.Errorf("the joined clone's daemon got %d notices requests, want 2: %+v", n, got)
	}
	ends := ops(got, proto.OpToolEnd)
	if len(ends) != 2 {
		t.Fatalf("the joined clone's daemon was told of %d calls ending, want 2: %+v", len(ends), got)
	}
	want, _ := os.Stat(filepath.Join(works, ".git"))
	if at, err := os.Stat(ends[0].GitDir); err != nil || !os.SameFile(at, want) {
		t.Errorf("the call in %s was bracketed with the clone %q", works, ends[0].GitDir)
	}
	if ends[1].GitDir != "" {
		t.Errorf("the call outside any repo was bracketed with the clone %q", ends[1].GitDir)
	}
}

// Claude Code runs the hook in every project. In a session whose server is in no repository it
// asks no daemon, not even the one of a joined clone it happens to run in: the session isn't in
// Murmur there. One with no record, as before servers published them, goes by where it runs.
func TestTheHookDoesNothingForASessionInNoClone(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	publish(t, here.Session{ServerPID: os.Getpid(), ClientPID: 4242})
	t.Setenv("CLAUDE_PID", "4242")
	for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop"} {
		if out, _ := run(event, repo); out != "" {
			t.Errorf("%s printed %q", event, out)
		}
	}
	if got := requests(); len(got) != 0 {
		t.Errorf("the daemon was asked %+v", got)
	}
	t.Setenv("CLAUDE_PID", "4243") // another process, with no server
	run("PostToolUse", repo)
	if got := ops(requests(), proto.OpNotices); len(got) != 1 {
		t.Errorf("without a record the hook sent %d notices requests, want 1", len(got))
	}
}

// Another agent's hook finds its session's record among those of the processes above it, by the
// session's own ID. A record without it isn't taken: it may be a sibling's in a process that runs
// several sessions, or that of an agent that started this one. The hook then goes by where it
// runs (Sol's reviews of #426).
func TestAnotherAgentsHookFindsItsSessionsClone(t *testing.T) {
	joined, works := newRepo(t), newRepo(t)
	theirs := fakeDaemon(t, filepath.Join(joined, ".git"), okToAll)
	own := fakeDaemon(t, filepath.Join(works, ".git"), okToAll)
	payload, _ := json.Marshal(map[string]string{"session_id": "thread-1", "hook_event_name": "PostToolUse", "cwd": works, "tool_name": "Bash"})
	asked := func(requests func() []proto.Request) int { return len(ops(requests(), proto.OpNotices)) }
	// The hook's parent stands in for the agent's process. A record's server has to be a process
	// that is running: this test's, or its parent's for a second one.
	agent, there := os.Getppid(), filepath.Join(joined, ".git")
	steps := []struct {
		what    string
		records []here.Session
		joined  bool // the hook speaks to the clone the record names, not the repo it runs in
	}{
		{"no record", nil, false},
		{"the session's record", []here.Session{{ServerPID: os.Getpid(), ClientPID: agent, Agent: "codex", Native: "thread-1", GitDir: there, On: true}}, true},
		{"a record of a session whose ID isn't known", []here.Session{{ServerPID: os.Getpid(), ClientPID: agent, Agent: "codex", GitDir: there, On: true}}, false},
		{"a sibling's record", []here.Session{{ServerPID: os.Getpid(), ClientPID: agent, Agent: "codex in T3", Native: "thread-2", GitDir: there, On: true}}, false},
		{"the record of the Claude Code that started the agent", []here.Session{{ServerPID: os.Getpid(), ClientPID: agent, Agent: "claude-code", ClaudeSession: "thread-1", GitDir: there, On: true}}, false},
		{"that agent's record, in no repository", []here.Session{{ServerPID: os.Getpid(), ClientPID: agent, Agent: "claude-code"}}, false},
		{"the session's record beside a sibling's", []here.Session{
			{ServerPID: os.Getpid(), ClientPID: agent, Agent: "codex in T3", Native: "thread-1", GitDir: there, On: true},
			{ServerPID: os.Getppid(), ClientPID: agent, Agent: "codex in T3", Native: "thread-2", GitDir: filepath.Join(works, ".git"), On: true}}, true},
	}
	dir, _ := here.DefaultDir()
	for _, step := range steps {
		for _, r := range step.records {
			publish(t, r)
		}
		wasTheirs, wasOwn := asked(theirs), asked(own)
		hook.RunFor(hook.Codex, bytes.NewReader(payload), &bytes.Buffer{})
		gotTheirs, gotOwn := asked(theirs)-wasTheirs, asked(own)-wasOwn
		if wantTheirs := map[bool]int{true: 1}[step.joined]; gotTheirs != wantTheirs || gotOwn != 1-wantTheirs {
			t.Errorf("%s: the clone a record names was asked %d times, the repo the hook runs in %d", step.what, gotTheirs, gotOwn)
		}
		for _, r := range step.records {
			here.Withdraw(dir, r.ServerPID)
		}
	}
}

// fakeDaemon serves the daemon's protocol for the clone at gitDir: answer gives the response line
// to each request, or "" to hold the connection open without one. It returns the requests so far.
func fakeDaemon(t *testing.T, gitDir string, answer func(proto.Request) string) func() []proto.Request {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); ln.Close() })
	var mu sync.Mutex
	var got []proto.Request
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				line, err := r.ReadBytes('\n')
				if err != nil {
					return
				}
				var req proto.Request
				json.Unmarshal(line, &req)
				if req.Op == proto.OpPing && req.Nonce != "" { // the daemon's proof (#371)
					b, _ := json.Marshal(proto.Response{OK: true, Protocol: proto.Protocol, Proof: proto.Proof("tok", c.LocalAddr().String(), req.Nonce)})
					c.Write(append(b, '\n'))
					if line, err = r.ReadBytes('\n'); err != nil {
						return
					}
					req = proto.Request{}
					json.Unmarshal(line, &req)
				}
				mu.Lock()
				got = append(got, req)
				mu.Unlock()
				if resp := answer(req); resp != "" {
					c.Write([]byte(resp + "\n"))
				} else {
					<-done
				}
			}()
		}
	}()
	dir := client.StateDir(gitDir)
	os.MkdirAll(dir, 0o700)
	// It holds the clone's lock, as a daemon does: a client takes a daemon.json beside a free lock
	// for a dead daemon's.
	lock, err := proc.Lock(filepath.Join(dir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	b, _ := json.Marshal(proto.Info{Addr: ln.Addr().String(), Token: "tok", PID: os.Getpid(), Device: "fake-0001"})
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return func() []proto.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]proto.Request(nil), got...)
	}
}

func okToAll(proto.Request) string { return `{"ok":true}` }

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	return repo
}

func run(event, cwd string) (string, time.Duration) {
	payload, _ := json.Marshal(map[string]string{"session_id": "claude-x", "hook_event_name": event,
		"cwd": cwd, "tool_name": "Bash", "tool_use_id": "toolu_1"})
	var out bytes.Buffer
	start := time.Now()
	hook.Run(bytes.NewReader(payload), &out)
	return out.String(), time.Since(start)
}

// Before a tool call the hook runs no git process of its own: it finds the clone from its files,
// as one process would add a tenth of a second or more to every call on Windows (#21). It finds
// the same clone from a linked worktree and from a subdirectory.
func TestPreToolUseRunsNoGit(t *testing.T) {
	repo := newRepo(t)
	linked := filepath.Join(t.TempDir(), "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "side", linked)
	sub := filepath.Join(linked, "a", "b")
	os.MkdirAll(sub, 0o755)
	gitDir := filepath.Join(repo, ".git")
	requests := fakeDaemon(t, gitDir, okToAll)

	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	for _, cwd := range []string{repo, linked, sub} {
		if out, _ := run("PreToolUse", cwd); out != "" {
			t.Errorf("PreToolUse printed %q", out)
		}
	}
	b, _ := os.ReadFile(trace)
	if n := strings.Count(string(b), `"event":"start"`); n != 0 {
		t.Errorf("three PreToolUse hooks ran %d git processes, want none", n)
	}
	want, err := os.Stat(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	var starts int
	for _, r := range requests() {
		if r.Op == proto.OpToolStart {
			starts++
			// The same directory, not the same string: on macOS a linked worktree's .git file
			// names the clone under /private/var, where the temp dir's own path is /var, a link to
			// it. The daemon compares the two the same way (sameClone).
			if got, err := os.Stat(r.GitDir); err != nil || !os.SameFile(got, want) {
				t.Errorf("the hook in %s found the clone at %q, want %q", r.Cwd, r.GitDir, gitDir)
			}
		}
	}
	if starts != 3 {
		t.Errorf("the daemon was told of %d calls, want 3", starts)
	}
}

// A daemon that takes the hook's connection and never answers holds a tool call up for no more
// than BracketTimeout, and the hook prints nothing. (A daemon that isn't running at all is
// TestPreToolUseWithTheDaemonDownIsSilent, in the daemon's tests.)
func TestPreToolUseGivesUpOnAHungDaemon(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpPing {
			return `{"ok":true}`
		}
		return "" // hung
	})
	out, took := run("PreToolUse", repo)
	if out != "" {
		t.Errorf("PreToolUse printed %q", out)
	}
	if limit := hook.BracketTimeout + 2*time.Second; took > limit {
		t.Errorf("PreToolUse waited %v for a hung daemon, more than %v", took, limit)
	}
	var told bool
	for _, r := range requests() {
		told = told || r.Op == proto.OpToolStart
	}
	if !told {
		t.Error("the hook didn't tell the daemon about the call")
	}
}

// A daemon from before #21 can still be running when the hook is upgraded: it refuses the ends of
// calls as an unknown op. The notices after a tool call still arrive.
func TestNoticesFromADaemonThatDoesntBracket(t *testing.T) {
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		switch r.Op {
		case proto.OpPing:
			return `{"ok":true}`
		case proto.OpNotices:
			return `{"ok":true,"self":"fake-0001/aaaaaa","messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"the old daemon's notice"}]}`
		}
		return `{"ok":false,"error":"unknown op ` + r.Op + `"}`
	})
	if out, _ := run("PreToolUse", repo); out != "" {
		t.Errorf("PreToolUse printed %q", out)
	}
	out, _ := run("PostToolUse", repo)
	if !strings.Contains(out, "the old daemon's notice") {
		t.Errorf("PostToolUse printed %q, want the notice", out)
	}
}

// The transcript's path, which holds the session's title, goes to the daemon with the notices
// request (#66): the daemon cannot find it from the session ID alone, as the hook's payload names it.
func TestNoticesCarryTheTranscriptPath(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	payload, _ := json.Marshal(map[string]string{"session_id": "claude-x", "hook_event_name": "UserPromptSubmit",
		"cwd": repo, "transcript_path": "/home/u/.claude/projects/p/claude-x.jsonl"})
	hook.Run(bytes.NewReader(payload), &bytes.Buffer{})
	for _, r := range requests() {
		if r.Op == proto.OpNotices {
			if r.Transcript != "/home/u/.claude/projects/p/claude-x.jsonl" {
				t.Errorf("the notices request carries transcript %q", r.Transcript)
			}
			if !r.Whole {
				t.Errorf("the notices request doesn't say the hook prints whole messages (#229)")
			}
			return
		}
	}
	t.Errorf("the hook sent no notices request: %+v", requests())
}

// The end of a turn (Stop) tells the daemon the session is idle, and prints nothing (#202).
func TestStopTellsTheDaemonTheTurnEnded(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"not for Stop"}],"unread":1}`
		}
		return `{"ok":true}`
	})
	if out, _ := run("Stop", repo); out != "" {
		t.Errorf("Stop printed %q", out)
	}
	var ended bool
	for _, r := range requests() {
		switch r.Op {
		case proto.OpTurnEnd:
			ended = r.ClaudeSession == "claude-x" && r.Continues
		case proto.OpNotices:
			t.Error("Stop asked for notices")
		}
	}
	if !ended {
		t.Errorf("the daemon wasn't told the turn of claude-x ended, by a hook that can keep it going: %+v", requests())
	}
}

// A subagent's tool call (agent_id in the payload) asks for notices as a subagent's, so the daemon
// knows the session is at work but tells it nothing there (#188, #202). A command it runs is
// bracketed as a subagent's too, so it doesn't count as the session's own hook.
func TestASubagentsCallIsMarked(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		payload, _ := json.Marshal(map[string]string{"session_id": "claude-x", "hook_event_name": event,
			"cwd": repo, "tool_name": "Bash", "tool_use_id": "toolu_2", "agent_id": "agent-1"})
		hook.Run(bytes.NewReader(payload), &bytes.Buffer{})
	}
	seen := map[string]bool{}
	for _, r := range requests() {
		seen[r.Op] = true
		if r.Op != proto.OpPing && !r.Subagent { // the client's ping, finding the daemon, speaks for nobody
			t.Errorf("a subagent's %s request isn't marked as one", r.Op)
		}
	}
	for _, op := range []string{proto.OpNotices, proto.OpToolStart, proto.OpToolEnd} {
		if !seen[op] {
			t.Errorf("the hook sent no %s request: %+v", op, requests())
		}
	}
}

// A count with no new messages, which the daemon sends when the number changed, is shown alone.
func TestTheUnreadCountAlone(t *testing.T) {
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":2}`
		}
		return `{"ok":true}`
	})
	out, _ := run("PostToolUse", repo)
	if !strings.Contains(out, "2 messages unread") {
		t.Errorf("PostToolUse printed %q, want the count", out)
	}
}

// A message the daemon gives whole (#204) is added to the agent's context as its turn would read,
// before the count of what is left unread.
func TestReadMessagesAreShownWhole(t *testing.T) {
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1,"delivered":[{"id":"m1","from":"other-0001/bbbbbb",` +
				`"fromName":"brave-heron","subject":"auth middleware?","body":"Are you changing it?"}]}`
		}
		return `{"ok":true}`
	})
	out, _ := run("PostToolUse", repo)
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("PostToolUse printed %q: %v", out, err)
	}
	m := proto.MessageView{ID: "m1", From: "other-0001/bbbbbb", FromName: "brave-heron", Subject: "auth middleware?",
		Body: "Are you changing it?"}
	if want := view.Hook([]proto.MessageView{m}, nil, 1); got.HookSpecificOutput.AdditionalContext != want {
		t.Errorf("the hook added %q, want %q", got.HookSpecificOutput.AdditionalContext, want)
	}
}

// The messages the daemon gives at the end of a turn keep it going: the Stop hook blocks the stop,
// with each message as its turn would read as the reason, which Claude Code gives the agent (#202).
func TestStopKeepsTheTurnGoingWithItsMessages(t *testing.T) {
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpTurnEnd {
			return `{"ok":true,"delivered":[{"id":"m1","from":"other-0001/bbbbbb",` +
				`"fromName":"brave-heron","subject":"auth middleware?","body":"Are you changing it?"}]}`
		}
		return `{"ok":true}`
	})
	out, _ := run("Stop", repo)
	var got struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("Stop printed %q: %v", out, err)
	}
	m := proto.MessageView{ID: "m1", From: "other-0001/bbbbbb", FromName: "brave-heron", Subject: "auth middleware?",
		Body: "Are you changing it?"}
	if want := view.Hook([]proto.MessageView{m}, nil, 0); got.Decision != "block" || got.Reason != want {
		t.Errorf("Stop printed %+v, want block with %q", got, want)
	}
}

// Pi's extension runs the hook with the session's file for its ID (#205): the requests name the
// session by it, never by a Claude Code ID or process, even where Pi inherited Claude Code's
// CLAUDE_PID, and bracket no call. What the hook prints is Claude Code's form, which the extension
// reads.
func TestPiHookNamesItsSessionByItsFile(t *testing.T) {
	t.Setenv("CLAUDE_PID", "4242")
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1,"messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"hi"}]}`
		}
		return `{"ok":true}`
	})
	const file = "/home/u/.pi/agent/sessions/a.jsonl"
	pi := func(event string) string {
		payload, _ := json.Marshal(map[string]string{"session_id": file, "hook_event_name": event, "cwd": repo,
			"tool_name": "bash", "tool_use_id": "call_1", "transcript_path": file})
		var out bytes.Buffer
		hook.RunFor("pi", bytes.NewReader(payload), &out)
		return out.String()
	}
	out := pi("PostToolUse")
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !strings.Contains(got.HookSpecificOutput.AdditionalContext, `"hi"`) {
		t.Fatalf("PostToolUse printed %q: %v", out, err)
	}
	pi("Stop")
	var reqs []proto.Request
	for _, r := range requests() {
		if r.Op != proto.OpPing {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 2 || reqs[0].Op != proto.OpNotices || reqs[1].Op != proto.OpTurnEnd {
		t.Fatalf("requests: %+v", reqs)
	}
	for _, r := range reqs {
		if r.Native != file || r.ClaudeSession != "" || r.ClientPID != 0 || r.Transcript != "" {
			t.Errorf("%s named its session %q, Claude %q, pid %d, transcript %q", r.Op, r.Native, r.ClaudeSession, r.ClientPID, r.Transcript)
		}
	}
	if !reqs[1].Continues {
		t.Error("Pi's Stop doesn't say it can keep the work going")
	}
}

// Pi's extension tells the hook when the work ended without a chance to go on (Escape, an error):
// the Stop says it can't keep the work going, and prints nothing, even if a daemon gives something
// back. Only Pi's hook reads that: Claude Code fires no Stop then.
func TestAnInterruptedPiStopKeepsNothingGoing(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpTurnEnd {
			return `{"ok":true,"delivered":[{"id":"m1","from":"other-0001/bbbbbb","subject":"lost if printed"}]}`
		}
		return `{"ok":true}`
	})
	stopFor := func(agent string) (string, proto.Request) {
		payload, _ := json.Marshal(map[string]any{"session_id": "/s/a.jsonl", "hook_event_name": "Stop", "cwd": repo, "interrupted": true})
		var out bytes.Buffer
		hook.RunFor(agent, bytes.NewReader(payload), &out)
		var last proto.Request
		for _, r := range requests() {
			if r.Op == proto.OpTurnEnd {
				last = r
			}
		}
		return out.String(), last
	}
	if out, r := stopFor("pi"); out != "" || r.Continues || r.Native != "/s/a.jsonl" {
		t.Errorf("Pi's interrupted Stop printed %q, request %+v", out, r)
	}
	if out, r := stopFor(""); out == "" || !r.Continues {
		t.Errorf("Claude Code's Stop printed %q, request %+v: interrupted is Pi's alone", out, r)
	}
}

// Murmur's OpenCode plugin runs the hook with OpenCode's session ID (#431), as Pi's extension does
// with the session's file: the requests name the session by it. For a session another session
// started, each request says so and names the parent (#455), the one sent as the session is made
// included; no other agent's hook can say that. An interrupted Stop keeps nothing going.
func TestOpenCodeHookNamesItsSessionAndItsParent(t *testing.T) {
	t.Setenv("CLAUDE_PID", "4242")
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		switch r.Op {
		case proto.OpNotices:
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1,"messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"hi"}]}`
		case proto.OpTurnEnd:
			return `{"ok":true,"delivered":[{"id":"m2","from":"other-0001/bbbbbb","subject":"at the end"}]}`
		}
		return `{"ok":true}`
	})
	run := func(agent string, payload map[string]any) string {
		payload["cwd"] = repo
		b, _ := json.Marshal(payload)
		var out bytes.Buffer
		hook.RunFor(agent, bytes.NewReader(b), &out)
		return out.String()
	}
	if out := run("opencode", map[string]any{"session_id": "ses_a", "hook_event_name": "PostToolUse"}); !strings.Contains(out, `\"hi\"`) {
		t.Errorf("PostToolUse printed %q", out)
	}
	if out := run("opencode", map[string]any{"session_id": "ses_a", "hook_event_name": "Stop"}); !strings.Contains(out, `"decision":"block"`) {
		t.Errorf("Stop printed %q", out)
	}
	if out := run("opencode", map[string]any{"session_id": "ses_a", "hook_event_name": "Stop", "interrupted": true}); out != "" {
		t.Errorf("an interrupted Stop printed %q", out)
	}
	run("opencode", map[string]any{"session_id": "ses_kid", "hook_event_name": "SessionStart", "child": true, "parent_native": "ses_a"})
	run("pi", map[string]any{"session_id": "/s/a.jsonl", "hook_event_name": "PostToolUse", "child": true, "parent_native": "ses_a"})
	var reqs []proto.Request
	for _, r := range requests() {
		if r.Op != proto.OpPing {
			reqs = append(reqs, r)
		}
	}
	if len(reqs) != 5 {
		t.Fatalf("requests: %+v", reqs)
	}
	for i, r := range reqs[:4] {
		want := "ses_a"
		if i == 3 {
			want = "ses_kid"
		}
		if r.Native != want || r.ClaudeSession != "" || r.ClientPID != 0 || r.Agent != "" || len(r.Ancestors) != 0 {
			t.Errorf("%s named its session %q, Claude %q, pid %d, agent %q", r.Op, r.Native, r.ClaudeSession, r.ClientPID, r.Agent)
		}
	}
	if !reqs[1].Continues || reqs[2].Continues {
		t.Errorf("Continues is %v on the Stop and %v on the interrupted one; want true, false", reqs[1].Continues, reqs[2].Continues)
	}
	if reqs[0].Child || reqs[0].ParentNative != "" {
		t.Errorf("a session a person started was named a child: %+v", reqs[0])
	}
	if !reqs[3].Child || reqs[3].ParentNative != "ses_a" || reqs[3].Op != proto.OpNotices {
		t.Errorf("the child's request: %+v", reqs[3])
	}
	if reqs[4].Child || reqs[4].ParentNative != "" {
		t.Errorf("Pi's hook named a child: %+v", reqs[4])
	}
}

// An agent the hook doesn't know gets nothing, and the daemon isn't asked.
func TestHookForAnUnknownAgentDoesNothing(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	payload, _ := json.Marshal(map[string]string{"session_id": "x", "hook_event_name": "PostToolUse", "cwd": repo})
	var out bytes.Buffer
	hook.RunFor("muse", bytes.NewReader(payload), &out)
	if out.Len() != 0 || len(requests()) != 0 {
		t.Errorf("printed %q, asked %+v", out.String(), requests())
	}
}

// Codex's and Grok's hooks (#225) carry their agent's name and the processes above the hook, by which the
// daemon finds the session before it knows the conversation's ID. They bracket no tool call (join
// installs them on no PreToolUse) and send no transcript, which is Claude Code's. Grok spells the
// session ID sessionId.
func TestAnotherAgentsHook(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	codex, _ := json.Marshal(map[string]string{"session_id": "thread-1", "hook_event_name": "PostToolUse",
		"cwd": repo, "tool_name": "Bash", "tool_use_id": "call_1", "transcript_path": "/x/rollout.jsonl"})
	hook.RunFor(hook.Codex, bytes.NewReader(codex), &bytes.Buffer{})
	grok, _ := json.Marshal(map[string]string{"sessionId": "grok-1", "hook_event_name": "Stop", "cwd": repo})
	hook.RunFor(hook.Grok, bytes.NewReader(grok), &bytes.Buffer{})
	seen := map[string]proto.Request{}
	for _, r := range requests() {
		if r.Op == proto.OpPing {
			continue
		}
		seen[r.Op] = r
		if len(r.Ancestors) == 0 || r.Ancestors[0] != os.Getppid() || r.ClientPID != 0 {
			t.Errorf("%s: ancestors %v, client PID %d", r.Op, r.Ancestors, r.ClientPID)
		}
	}
	if r, ok := seen[proto.OpNotices]; !ok || r.Agent != hook.Codex || r.ClaudeSession != "thread-1" || r.Transcript != "" {
		t.Errorf("Codex's notices request: %+v", r)
	}
	if r, ok := seen[proto.OpTurnEnd]; !ok || r.Agent != hook.Grok || r.ClaudeSession != "grok-1" || !r.Continues {
		t.Errorf("Grok's turn end: %+v", r)
	}
	if _, ok := seen[proto.OpToolEnd]; ok {
		t.Error("Codex's hook bracketed a call")
	}
}

// Grok fires a Stop when the session closes too, whose answer it ignores: that one doesn't ask to
// keep the turn going, so what the daemon holds is handed over as a turn instead. Claude Code's and
// Codex's hooks don't read Grok's spelling of the session ID.
func TestGroksStopAtTheSessionsClose(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	closing, _ := json.Marshal(map[string]string{"sessionId": "grok-1", "hook_event_name": "Stop", "reason": "shutdown", "cwd": repo})
	hook.RunFor(hook.Grok, bytes.NewReader(closing), &bytes.Buffer{})
	camel, _ := json.Marshal(map[string]string{"sessionId": "grok-1", "hook_event_name": "Stop", "cwd": repo})
	hook.RunFor(hook.Codex, bytes.NewReader(camel), &bytes.Buffer{})
	var ends []proto.Request
	for _, r := range requests() {
		if r.Op == proto.OpTurnEnd {
			ends = append(ends, r)
		}
	}
	if len(ends) != 1 || ends[0].Continues || ends[0].ClaudeSession != "grok-1" {
		t.Errorf("turn ends: %+v", ends)
	}
}
