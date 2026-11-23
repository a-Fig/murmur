package hook_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proto"
)

// cursorRun runs Cursor's hook with a payload of the shape cursor-agent 2026.11.22-e9042b2 gives
// (read in its source), and returns what it printed.
func cursorRun(t *testing.T, repo, event string, kv ...any) string {
	t.Helper()
	m := map[string]any{"conversation_id": "c0ffee-chat", "session_id": "c0ffee-chat", "generation_id": "g1", "model": "m",
		"hook_event_name": event, "cursor_version": "2026.11.22-e9042b2", "workspace_roots": []string{repo},
		"user_email": nil, "transcript_path": nil}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	payload, _ := json.Marshal(m)
	var out bytes.Buffer
	hook.RunFor(hook.Cursor, bytes.NewReader(payload), &out)
	return out.String()
}

// Cursor's hook (#922) asks as the session's other hooks do, by Cursor's conversation ID and the
// processes above it: at a chat's start and each prompt (with the prompt), and after each tool
// call, with the notices coming back as additional_context; at a turn's ordinary end it keeps the
// chat going with what wasn't read, as followup_message. An aborted turn keeps nothing going.
// Every answer is a JSON object.
func TestCursorHook(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		switch r.Op {
		case proto.OpNotices:
			b, _ := json.Marshal(proto.Response{OK: true, Unread: 1,
				Messages: []proto.MessageView{{From: "dev-0001/aaaaaa", FromName: "quiet-otter", Subject: "the hook"}}})
			return string(b)
		case proto.OpTurnEnd:
			b, _ := json.Marshal(proto.Response{OK: true, Read: []proto.MessageView{{From: "dev-0001/aaaaaa", Subject: "s", Body: "b"}}})
			return string(b)
		}
		return `{"ok":true}`
	})
	answer := func(out string) map[string]string {
		var a map[string]string
		if err := json.Unmarshal([]byte(out), &a); err != nil {
			t.Fatalf("not a JSON object: %q", out)
		}
		return a
	}
	if a := answer(cursorRun(t, repo, "sessionStart", "is_background_agent", false, "composer_mode", "agent")); a["additional_context"] == "" {
		t.Errorf("sessionStart: %q", a)
	}
	cursorRun(t, repo, "beforeSubmitPrompt", "prompt", "Read probe.txt", "attachments", []any{})
	a := answer(cursorRun(t, repo, "postToolUse", "tool_name", "Shell", "tool_input", map[string]any{"command": "ls"},
		"tool_output", "a", "tool_use_id", "tool_1", "cwd", repo, "duration", 12))
	if a["additional_context"] == "" || a["followup_message"] != "" {
		t.Errorf("postToolUse: %q", a)
	}
	if a := answer(cursorRun(t, repo, "stop", "status", "completed", "loop_count", 0)); a["followup_message"] == "" {
		t.Errorf("stop: %q", a)
	}
	if a := answer(cursorRun(t, repo, "stop", "status", "aborted", "loop_count", 0)); len(a) != 0 {
		t.Errorf("an aborted turn's stop: %q", a)
	}
	if a := answer(cursorRun(t, repo, "preToolUse", "tool_name", "Shell")); len(a) != 0 {
		t.Errorf("preToolUse: %q", a)
	}

	var notices, ends []proto.Request
	for _, r := range requests() {
		if r.Op == proto.OpPing {
			continue
		}
		if r.Agent != hook.Cursor || r.ClaudeSession != "c0ffee-chat" || r.ClientPID != 0 || len(r.Ancestors) == 0 || r.Subagent {
			t.Errorf("%s: %+v", r.Op, r)
		}
		switch r.Op {
		case proto.OpNotices:
			notices = append(notices, r)
		case proto.OpTurnEnd:
			ends = append(ends, r)
		case proto.OpToolStart, proto.OpToolEnd:
			t.Errorf("Cursor's hook bracketed a call: %+v", r)
		}
	}
	if len(notices) != 3 || notices[1].Prompt != "Read probe.txt" || notices[0].Prompt != "" || notices[2].Prompt != "" {
		t.Errorf("notices requests: %+v", notices)
	}
	if len(ends) != 2 || !ends[0].Continues || ends[1].Continues {
		t.Errorf("turn ends: %+v", ends)
	}
}

// A call made inside a subagent says so (parent_tool_call_id), and its notice goes to the subagent,
// as a Claude Code subagent's does.
func TestCursorSubagentCall(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	cursorRun(t, repo, "postToolUse", "tool_name", "Read", "tool_use_id", "t2", "cwd", repo, "parent_tool_call_id", "task-1")
	if n := ops(requests(), proto.OpNotices); len(n) != 1 || !n[0].Subagent {
		t.Errorf("%+v", n)
	}
}

// Cursor runs Claude Code's hooks from ~/.claude/settings.json too, under its own event names.
// Claude Code's hook, so run, asks nothing, whatever the event is called: Cursor's own hook speaks
// for the session.
func TestClaudeCodesHookStepsAsideForCursor(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	for _, event := range []string{"postToolUse", "PostToolUse", "stop", "Stop"} {
		payload, _ := json.Marshal(map[string]any{"session_id": "c0ffee-chat", "conversation_id": "c0ffee-chat",
			"hook_event_name": event, "cwd": repo, "cursor_version": "2026.11.22-e9042b2", "tool_name": "Shell", "tool_use_id": "t"})
		var out bytes.Buffer
		hook.Run(bytes.NewReader(payload), &out)
		if out.Len() != 0 {
			t.Errorf("%s printed %q", event, out.String())
		}
	}
	if r := requests(); len(r) != 0 {
		t.Errorf("Claude Code's hook asked %+v", r)
	}
}

// Cursor gives its MCP servers no ID for the chat, so its hook names the chat to the server the
// Cursor process above it started (#921): once, and only where that process has one server of
// Cursor's, with no chat yet. A subagent's call names nothing.
func TestCursorHookNamesTheChatToItsServer(t *testing.T) {
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	dir := os.Getenv("MURMUR_SESSIONS_DIR")
	t.Cleanup(func() { os.RemoveAll(dir); os.MkdirAll(dir, 0o700) })
	rec := here.Session{ServerPID: os.Getpid(), ClientPID: os.Getppid(), ClientStarted: "t0", Agent: "cursor"}
	if err := here.Publish(dir, rec); err != nil {
		t.Fatal(err)
	}
	cursorRun(t, repo, "postToolUse", "tool_name", "Read", "tool_use_id", "t2", "cwd", repo, "parent_tool_call_id", "task-1")
	if got := here.NamedFor(dir, rec); got != "" {
		t.Errorf("a subagent's call named %q", got)
	}
	cursorRun(t, repo, "sessionStart")
	if got := here.NamedFor(dir, rec); got != "c0ffee-chat" {
		t.Errorf("named %q", got)
	}

	// Two servers of Cursor's under one process (T3 Code's): no name for either.
	os.Remove(filepath.Join(dir, "named-"+strconv.Itoa(os.Getppid())+".json"))
	other := rec
	other.ServerPID = os.Getppid()
	here.Publish(dir, other)
	cursorRun(t, repo, "sessionStart")
	if got := here.NamedFor(dir, rec); got != "" {
		t.Errorf("with two servers, named %q", got)
	}
}
