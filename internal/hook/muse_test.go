package hook_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proto"
)

// museRun runs Muse's hook with a payload of the shape Muse 1.4.14 gives (2026-11-16), and returns
// what it printed.
func museRun(t *testing.T, fields map[string]any) string {
	t.Helper()
	payload, _ := json.Marshal(fields)
	var out bytes.Buffer
	hook.RunFor(hook.Muse, bytes.NewReader(payload), &out)
	return out.String()
}

// Muse's hook (#461) takes Claude Code's payload and answers: its requests carry the agent's name,
// the session's ID and the processes above the hook, by which the daemon pairs it with the session
// its MCP server registered by MUSE_SESSION_ID; the prompt goes along, so a message handed over as
// the turn is read once it starts (#611); and Stop asks to keep the turn going. Notices come back as
// additionalContext, which Muse gives the model.
func TestMuseHook(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("MUSE_SESSION_ID", "01a1e597-b944")
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		if r.Op == proto.OpNotices {
			return `{"ok":true,"self":"fake-0001/aaaaaa","unread":1}`
		}
		return `{"ok":true}`
	})
	base := map[string]any{"session_id": "01a1e597-b944", "turn_id": "t1", "cwd": repo, "transcript_path": nil,
		"model": "muse-spark-1.3", "permission_mode": "bypassPermissions"}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	museRun(t, with("hook_event_name", "UserPromptSubmit", "prompt", "Read probe.txt"))
	out := museRun(t, with("hook_event_name", "PostToolUse", "tool_name", "read_file",
		"tool_input", map[string]any{"path": "probe.txt"}, "tool_use_id", "call_1", "tool_response", "1|alpha"))
	museRun(t, with("hook_event_name", "Stop", "stop_hook_active", false, "last_assistant_message", "done"))

	var got struct {
		HookSpecificOutput struct{ HookEventName, AdditionalContext string } `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.HookSpecificOutput.HookEventName != "PostToolUse" ||
		got.HookSpecificOutput.AdditionalContext == "" {
		t.Errorf("PostToolUse printed %q (%v)", out, err)
	}
	var notices, ends []proto.Request
	for _, r := range requests() {
		if r.Op == proto.OpPing {
			continue
		}
		if r.Agent != hook.Muse || r.ClaudeSession != "01a1e597-b944" || r.ClientPID != 0 || len(r.Ancestors) == 0 || r.Transcript != "" {
			t.Errorf("%s: %+v", r.Op, r)
		}
		switch r.Op {
		case proto.OpNotices:
			notices = append(notices, r)
		case proto.OpTurnEnd:
			ends = append(ends, r)
		case proto.OpToolEnd, proto.OpToolStart:
			t.Errorf("Muse's hook bracketed a call: %+v", r)
		}
	}
	if len(notices) != 2 || notices[0].Prompt != "Read probe.txt" || notices[1].Prompt != "" {
		t.Errorf("notices requests: %+v", notices)
	}
	if len(ends) != 1 || !ends[0].Continues {
		t.Errorf("turn ends: %+v", ends)
	}
}

// Muse runs side sessions of its own in a turn (its reminder model), whose hooks carry their own
// session_id while MUSE_SESSION_ID is still the session's. The hook asks nothing for them: a notice
// would go to the side session, and its tool calls after the turn's Stop would mark the session at
// work again. Without the variable, the payload's ID is taken as it is.
func TestMuseSideSessionsAreSkipped(t *testing.T) {
	repo := newRepo(t)
	requests := fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	side := map[string]any{"hook_event_name": "PostToolUse", "session_id": "01a1e597-c358", "cwd": repo,
		"tool_name": "submit_reminder_decision"}
	t.Setenv("MUSE_SESSION_ID", "01a1e597-b944")
	museRun(t, side)
	if r := requests(); len(r) != 0 {
		t.Errorf("a side session's hook asked %+v", r)
	}
	os.Unsetenv("MUSE_SESSION_ID")
	museRun(t, side)
	if len(ops(requests(), proto.OpNotices)) != 1 {
		t.Errorf("without MUSE_SESSION_ID: %+v", requests())
	}
}

// Muse gives a hook no APPDATA on Windows, by which the hook finds the records of the sessions' MCP
// servers: it takes Windows' own folder for it, and leaves one that is set.
func TestMuseHookFillsTheUsersFolders(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows only")
	}
	repo := newRepo(t)
	fakeDaemon(t, filepath.Join(repo, ".git"), okToAll)
	t.Setenv("APPDATA", "")
	t.Setenv("LOCALAPPDATA", `C:\mine`)
	os.Unsetenv("APPDATA")
	museRun(t, map[string]any{"hook_event_name": "PostToolUse", "session_id": "s", "cwd": repo})
	if os.Getenv("APPDATA") == "" || os.Getenv("LOCALAPPDATA") != `C:\mine` {
		t.Errorf("APPDATA %q, LOCALAPPDATA %q", os.Getenv("APPDATA"), os.Getenv("LOCALAPPDATA"))
	}
}
