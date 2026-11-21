package hook_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
	"github.com/a-Fig/murmur/internal/view"
)

// Antigravity's hook (#650, #892, #894) is named its event by join's command line. PreInvocation
// fetches the notices with the turn's prompt on invocation 0 (0-indexed in Antigravity 1.5.13,
// 2026-11-20) and gives them back as a system message; Stop ends the turn and keeps it going with
// "continue" only at a turn's ordinary, fully-idle end. Every answer is a JSON object:
// {"decision":"allow"} on PreToolUse and {} elsewhere when it has nothing to say.
func TestAntigravitysHook(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		switch r.Op {
		case proto.OpNotices:
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1,"messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"hi"}]}`
		case proto.OpTurnEnd:
			if r.Continues {
				return `{"ok":true,"delivered":[{"id":"m2","from":"other-0001/bbbbbb","subject":"auth?","body":"Changing it?"}]}`
			}
		}
		return `{"ok":true}`
	})
	agy := func(event string, extra map[string]any) string {
		payload := map[string]any{"conversationId": "conv-1", "workspacePaths": []string{repo},
			"transcriptPath": "/x/transcript.jsonl", "modelName": "auto", "lastUserInput": "fix the test"}
		for k, v := range extra {
			payload[k] = v
		}
		b, _ := json.Marshal(payload)
		var out bytes.Buffer
		hook.RunForEvent(hook.Antigravity, event, bytes.NewReader(b), &out)
		return out.String()
	}

	var steps struct {
		InjectSteps []struct {
			SystemMessage struct {
				SystemMessage string `json:"systemMessage"`
			} `json:"systemMessage"`
		} `json:"injectSteps"`
	}
	// invocationNum is 0-indexed: 0 is the turn's first call and carries lastUserInput as Prompt.
	out := agy("PreInvocation", map[string]any{"invocationNum": 0})
	if err := json.Unmarshal([]byte(out), &steps); err != nil || len(steps.InjectSteps) != 1 {
		t.Fatalf("PreInvocation printed %q: %v", out, err)
	}
	notice := view.HookFor(hook.Antigravity, nil, []proto.MessageView{{ID: "m1", From: "other-0001/bbbbbb", Subject: "hi"}}, 1)
	if got := steps.InjectSteps[0].SystemMessage.SystemMessage; got != notice || strings.Contains(got, "mcp__murmur__inbox") {
		t.Errorf("PreInvocation gave %q, want %q", got, notice)
	}

	// Invocation 1 (after the first tool call) carries the same lastUserInput, and sends no prompt.
	agy("PreInvocation", map[string]any{"invocationNum": 1})

	var stop struct{ Decision, Reason string }
	out = agy("Stop", map[string]any{"terminationReason": "NO_TOOL_CALL", "fullyIdle": true})
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
		t.Fatalf("Stop printed %q: %v", out, err)
	}
	m := proto.MessageView{ID: "m2", From: "other-0001/bbbbbb", Subject: "auth?", Body: "Changing it?"}
	if want := view.Hook([]proto.MessageView{m}, nil, 0); stop.Decision != "continue" || stop.Reason != want {
		t.Errorf("Stop printed %+v, want continue with %q", stop, want)
	}
	// Backwards-compatible: absent fullyIdle still continues on NO_TOOL_CALL.
	stop = struct{ Decision, Reason string }{}
	out = agy("Stop", map[string]any{"terminationReason": "NO_TOOL_CALL"})
	if err := json.Unmarshal([]byte(out), &stop); err != nil || stop.Decision != "continue" {
		t.Errorf("Stop without fullyIdle printed %q: %v", out, err)
	}
	// Background tasks still running (fullyIdle: false) ends without forcing continuation.
	if out := agy("Stop", map[string]any{"terminationReason": "NO_TOOL_CALL", "fullyIdle": false}); out != "{}\n" {
		t.Errorf("non-idle Stop printed %q", out)
	}
	// A turn the user cancelled ends, and keeps nothing going.
	if out := agy("Stop", map[string]any{"terminationReason": "USER_CANCELED"}); out != "{}\n" {
		t.Errorf("a cancelled Stop printed %q", out)
	}
	// An event join doesn't install it on asks nothing, and still answers {}.
	if out := agy("SessionStart", nil); out != "{}\n" {
		t.Errorf("SessionStart printed %q", out)
	}

	var ops []proto.Request
	for _, r := range requests() {
		if r.Op != proto.OpPing {
			ops = append(ops, r)
		}
	}
	if len(ops) != 6 {
		t.Fatalf("requests: %+v", ops)
	}
	if r := ops[0]; r.Op != proto.OpNotices || r.Agent != hook.Antigravity || r.ClaudeSession != "conv-1" || r.Prompt != "fix the test" || r.Transcript != "" {
		t.Errorf("PreInvocation 0's request: %+v", r)
	}
	if r := ops[1]; r.Op != proto.OpNotices || r.Prompt != "" {
		t.Errorf("PreInvocation 1's request: %+v", r)
	}
	if r := ops[2]; r.Op != proto.OpTurnEnd || !r.Continues || r.ClaudeSession != "conv-1" {
		t.Errorf("fullyIdle=true Stop's request: %+v", r)
	}
	if r := ops[3]; r.Op != proto.OpTurnEnd || !r.Continues {
		t.Errorf("absent-fullyIdle Stop's request: %+v", r)
	}
	if r := ops[4]; r.Op != proto.OpTurnEnd || r.Continues {
		t.Errorf("fullyIdle=false Stop's request: %+v", r)
	}
	if r := ops[5]; r.Op != proto.OpTurnEnd || r.Continues {
		t.Errorf("the cancelled Stop's request: %+v", r)
	}
}

// Top-level cwd is kept when present (Antigravity 1.5.13), and workspacePaths[0] is only a
// fallback when cwd is empty (#892).
func TestAntigravityCwdPreservation(t *testing.T) {
	repoA, repoB := newRepo(t), newRepo(t)
	reqA := fakeDaemon(t, filepath.Join(repoA, ".git"), okToAll)
	reqB := fakeDaemon(t, filepath.Join(repoB, ".git"), okToAll)

	send := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		hook.RunForEvent(hook.Antigravity, "PreInvocation", bytes.NewReader(b), &bytes.Buffer{})
	}
	// cwd present: reaches repoA, not workspacePaths[0] (repoB).
	send(map[string]any{"conversationId": "conv-1", "cwd": repoA, "workspacePaths": []string{repoB}, "invocationNum": 0})
	if nA, nB := len(ops(reqA(), proto.OpNotices)), len(ops(reqB(), proto.OpNotices)); nA != 1 || nB != 0 {
		t.Errorf("with cwd set: repoA got %d, repoB got %d; want 1, 0", nA, nB)
	}
	// cwd absent: falls back to workspacePaths[0] (repoB).
	send(map[string]any{"conversationId": "conv-1", "workspacePaths": []string{repoB}, "invocationNum": 0})
	if nA, nB := len(ops(reqA(), proto.OpNotices)), len(ops(reqB(), proto.OpNotices)); nA != 1 || nB != 1 {
		t.Errorf("without cwd: repoA got %d, repoB got %d; want 1, 1", nA, nB)
	}
}

// Antigravity's PreToolUse and PostToolUse bracket shell commands for commit attribution (#894):
// PreToolUse always answers {"decision":"allow"}, PostToolUse answers {} without fetching notices,
// errors still close the bracket, stepIdx stands in when toolCall.id is absent, toolCall.args.Cwd
// overrides cwd, and non-shell tools are skipped as defense in depth.
func TestAntigravityToolBracketing(t *testing.T) {
	repo, other := newRepo(t), newRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	reqRepo := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1,"messages":[{"id":"m1","from":"other-0001/bbbbbb","subject":"do not lose me"}]}`
		}
		return `{"ok":true}`
	})
	reqOther := fakeDaemon(t, filepath.Join(other, ".git"), okToAll)

	runAgy := func(event string, payload map[string]any) string {
		b, _ := json.Marshal(payload)
		var out bytes.Buffer
		hook.RunForEvent(hook.Antigravity, event, bytes.NewReader(b), &out)
		return out.String()
	}

	// Normal run_command bracket with explicit toolCall.id and args.Cwd overriding workspacePaths[0].
	call := map[string]any{
		"conversationId": "conv-1",
		"cwd":            other,
		"workspacePaths": []string{other},
		"stepIdx":        5,
		"toolCall": map[string]any{
			"id":   "toolu_01",
			"name": "run_command",
			"args": map[string]any{"CommandLine": "git commit -m hi", "Cwd": repo},
		},
	}
	if out := runAgy("PreToolUse", call); out != "{\"decision\":\"allow\"}\n" {
		t.Fatalf("PreToolUse printed %q, want allow", out)
	}
	if out := runAgy("PostToolUse", call); out != "{}\n" {
		t.Fatalf("PostToolUse printed %q, want {}", out)
	}

	// Failing PostToolUse still closes the bracket and sends no notices.
	failCall := map[string]any{
		"conversationId": "conv-1",
		"cwd":            repo,
		"error":          "exit status 1",
		"toolCall": map[string]any{
			"id":   "toolu_02",
			"name": "run_command",
			"args": map[string]any{"CommandLine": "git commit && false"},
		},
	}
	if out := runAgy("PreToolUse", failCall); out != "{\"decision\":\"allow\"}\n" {
		t.Errorf("PreToolUse (failCall) printed %q", out)
	}
	if out := runAgy("PostToolUse", failCall); out != "{}\n" {
		t.Errorf("PostToolUse (error) printed %q", out)
	}

	// Missing toolCall.id falls back to stepIdx (including stepIdx == 0), and relative args.Cwd joins to cwd.
	stepCall := map[string]any{
		"conversationId": "conv-1",
		"cwd":            repo,
		"stepIdx":        0,
		"toolCall": map[string]any{
			"name": "run_command",
			"args": map[string]any{"CommandLine": "git status", "Cwd": "sub"},
		},
	}
	if out := runAgy("PreToolUse", stepCall); out != "{\"decision\":\"allow\"}\n" {
		t.Errorf("PreToolUse (stepIdx) printed %q", out)
	}
	if out := runAgy("PostToolUse", stepCall); out != "{}\n" {
		t.Errorf("PostToolUse (stepIdx) printed %q", out)
	}

	// Non-shell tool in cannotRun skips bracketing while keeping the stdout contracts.
	readCall := map[string]any{
		"conversationId": "conv-1",
		"cwd":            repo,
		"toolCall":       map[string]any{"id": "toolu_read", "name": "read_file", "args": map[string]any{"Path": "x"}},
	}
	if out := runAgy("PreToolUse", readCall); out != "{\"decision\":\"allow\"}\n" {
		t.Errorf("PreToolUse (read_file) printed %q", out)
	}
	if out := runAgy("PostToolUse", readCall); out != "{}\n" {
		t.Errorf("PostToolUse (read_file) printed %q", out)
	}

	// Early-exit PreToolUse paths (malformed JSON, missing conversationId, missing toolCall, no repo)
	// must still print {"decision":"allow"} so Antigravity never treats them as a block/error.
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"invalid json", "not-json"},
		{"no conversationId", `{"cwd":` + string(mustJSON(t, repo)) + `,"toolCall":{"id":"t","name":"run_command"}}`},
		{"no toolCall", `{"conversationId":"conv-1","cwd":` + string(mustJSON(t, repo)) + `}`},
		{"outside any repo", `{"conversationId":"conv-1","cwd":` + string(mustJSON(t, t.TempDir())) + `,"toolCall":{"id":"t","name":"run_command"}}`},
	} {
		var out bytes.Buffer
		hook.RunForEvent(hook.Antigravity, "PreToolUse", strings.NewReader(tc.raw), &out)
		if out.String() != "{\"decision\":\"allow\"}\n" {
			t.Errorf("PreToolUse (%s) printed %q, want allow", tc.name, out.String())
		}
	}

	if got := reqOther(); len(got) != 0 {
		t.Errorf("other repo's daemon was asked %+v despite args.Cwd pointing to repo", got)
	}
	got := reqRepo()
	if n := len(ops(got, proto.OpNotices)); n != 0 {
		t.Errorf("PreToolUse/PostToolUse sent %d OpNotices requests, want 0", n)
	}
	starts, ends := ops(got, proto.OpToolStart), ops(got, proto.OpToolEnd)
	if len(starts) != 3 || len(ends) != 3 {
		t.Fatalf("starts=%d ends=%d, want 3 each: %+v", len(starts), len(ends), got)
	}
	wantIDs := []string{"toolu_01", "toolu_02", "step-0"}
	for i, id := range wantIDs {
		if starts[i].ToolUseID != id || ends[i].ToolUseID != id {
			t.Errorf("call %d: start ID %q, end ID %q, want %q", i, starts[i].ToolUseID, ends[i].ToolUseID, id)
		}
	}
	if starts[2].Cwd != sub || ends[2].Cwd != sub {
		t.Errorf("relative args.Cwd resolved to %q / %q, want %q", starts[2].Cwd, ends[2].Cwd, sub)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An Antigravity session moved into a repo via `mur join -C` has Native == "" in its here.Session
// record because Antigravity doesn't pass ANTIGRAVITY_CONVERSATION_ID to MCP servers (#888).
// Inside T3, hooks match by T3Thread + agent; outside T3, standalone Antigravity matches a unique
// Antigravity record with empty Native and T3Thread. Cross-agent records, multi-session records,
// and records bound to another conversation or T3 thread are rejected.
func TestAntigravityMovedSessionLookup(t *testing.T) {
	joined, works := newRepo(t), newRepo(t)
	theirs := fakeDaemon(t, filepath.Join(joined, ".git"), okToAll)
	own := fakeDaemon(t, filepath.Join(works, ".git"), okToAll)
	parent, there, hereGit := os.Getppid(), filepath.Join(joined, ".git"), filepath.Join(works, ".git")
	dir, _ := here.DefaultDir()

	runPre := func(agent, cwd string) {
		var payload []byte
		if agent == hook.Antigravity {
			payload, _ = json.Marshal(map[string]any{"conversationId": "conv-1", "cwd": cwd, "invocationNum": 0})
			hook.RunForEvent(hook.Antigravity, "PreInvocation", bytes.NewReader(payload), &bytes.Buffer{})
		} else {
			payload, _ = json.Marshal(map[string]string{"session_id": "thread-1", "hook_event_name": "PostToolUse", "cwd": cwd, "tool_name": "Bash"})
			hook.RunFor(agent, bytes.NewReader(payload), &bytes.Buffer{})
		}
	}
	asked := func(fn func() []proto.Request) int { return len(ops(fn(), proto.OpNotices)) }

	cases := []struct {
		what       string
		agent      string
		t3Thread   string
		cwd        string
		records    []here.Session
		wantJoined int
		wantWorks  int
	}{
		{
			what:       "T3 Antigravity moved from scratch into joined repo",
			agent:      hook.Antigravity,
			t3Thread:   "thread-agy-1",
			cwd:        t.TempDir(),
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-agy-1", GitDir: there, On: true}},
			wantJoined: 1, wantWorks: 0,
		},
		{
			what:       "T3 Antigravity under headless t3 serve (agent 'antigravity')",
			agent:      hook.Antigravity,
			t3Thread:   "thread-agy-1",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity", T3Thread: "thread-agy-1", GitDir: there, On: true}},
			wantJoined: 1, wantWorks: 0,
		},
		{
			what:       "T3 Antigravity passive outside any repo asks no daemon even in joined cwd",
			agent:      hook.Antigravity,
			t3Thread:   "thread-agy-1",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-agy-1"}},
			wantJoined: 0, wantWorks: 0,
		},
		{
			what:     "T3 Antigravity picks its own thread beside a sibling thread",
			agent:    hook.Antigravity,
			t3Thread: "thread-agy-1",
			cwd:      works,
			records: []here.Session{
				{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-agy-1", GitDir: there, On: true},
				{ServerPID: os.Getppid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-agy-2", GitDir: hereGit, On: true},
			},
			wantJoined: 1, wantWorks: 0,
		},
		{
			what:       "T3 cross-agent: Antigravity hook ignores Claude Code record with same T3Thread",
			agent:      hook.Antigravity,
			t3Thread:   "thread-1",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "claude-code in T3", T3Thread: "thread-1", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "T3 cross-agent: Codex hook ignores Antigravity record with same T3Thread",
			agent:      hook.Codex,
			t3Thread:   "thread-1",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-1", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "T3 record bound to another Native conversation is rejected",
			agent:      hook.Antigravity,
			t3Thread:   "thread-1",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-1", Native: "conv-other", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "standalone Antigravity moved from scratch into joined repo",
			agent:      hook.Antigravity,
			t3Thread:   "",
			cwd:        t.TempDir(),
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity", GitDir: there, On: true}},
			wantJoined: 1, wantWorks: 0,
		},
		{
			what:       "standalone cross-agent: Antigravity hook ignores Claude Code record",
			agent:      hook.Antigravity,
			t3Thread:   "",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "claude-code", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "standalone cross-agent: Antigravity hook ignores Codex record",
			agent:      hook.Antigravity,
			t3Thread:   "",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "codex", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:     "standalone multi-session: two Antigravity records under one process are ambiguous",
			agent:    hook.Antigravity,
			t3Thread: "",
			cwd:      works,
			records: []here.Session{
				{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity", GitDir: there, On: true},
				{ServerPID: os.Getppid(), ClientPID: parent, Agent: "antigravity", GitDir: hereGit, On: true},
			},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "standalone Antigravity rejects record with a different known Native ID",
			agent:      hook.Antigravity,
			t3Thread:   "",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity", Native: "conv-other", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:       "standalone Antigravity (no T3Thread) rejects T3 thread's record",
			agent:      hook.Antigravity,
			t3Thread:   "",
			cwd:        works,
			records:    []here.Session{{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity in T3", T3Thread: "thread-1", GitDir: there, On: true}},
			wantJoined: 0, wantWorks: 1,
		},
		{
			what:     "exact Native match wins for Antigravity even beside a sibling",
			agent:    hook.Antigravity,
			t3Thread: "",
			cwd:      works,
			records: []here.Session{
				{ServerPID: os.Getpid(), ClientPID: parent, Agent: "antigravity", Native: "conv-1", GitDir: there, On: true},
				{ServerPID: os.Getppid(), ClientPID: parent, Agent: "antigravity", Native: "conv-2", GitDir: hereGit, On: true},
			},
			wantJoined: 1, wantWorks: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			t.Setenv(t3.ThreadEnv, tc.t3Thread)
			for _, r := range tc.records {
				publish(t, r)
			}
			defer func() {
				for _, r := range tc.records {
					here.Withdraw(dir, r.ServerPID)
				}
			}()
			wasJoined, wasWorks := asked(theirs), asked(own)
			runPre(tc.agent, tc.cwd)
			gotJoined, gotWorks := asked(theirs)-wasJoined, asked(own)-wasWorks
			if gotJoined != tc.wantJoined || gotWorks != tc.wantWorks {
				t.Errorf("joined asked %d, works asked %d; want %d, %d", gotJoined, gotWorks, tc.wantJoined, tc.wantWorks)
			}
		})
	}
}
