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
	"github.com/a-Fig/murmur/internal/view"
)

func runKiro(payload string) string {
	var out bytes.Buffer
	hook.RunFor(hook.Kiro, strings.NewReader(payload), &out)
	return out.String()
}

// Kiro's UserPromptSubmit injects raw stdout (not Claude Code's hookSpecificOutput JSON) into the
// turn context, passes the prompt so a handed-over turn is marked read (#611), and names Kiro's
// mcp_murmur_inbox tool. Stop returns {"decision":"block","reason":"..."} when unread messages
// wait, and nothing when idle. PostToolUse skips OpNotices so Kiro 3.4.0's discarded stdout never
// loses whole-message notices (#910).
func TestKiroHookEvents(t *testing.T) {
	repo := newRepo(t)
	msgs := []proto.MessageView{{From: "dev-0001/aaaaaa", FromName: "quiet-otter", Subject: "heads up", Body: "check api.go"}}
	got := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		switch r.Op {
		case proto.OpNotices:
			b, _ := json.Marshal(proto.Response{OK: true, Messages: msgs, Unread: 1})
			return string(b)
		case proto.OpTurnEnd:
			b, _ := json.Marshal(proto.Response{OK: true, Read: msgs})
			return string(b)
		}
		return `{"ok":true}`
	})

	// UserPromptSubmit: plain text stdout with mcp_murmur_inbox, not JSON.
	promptPayload, _ := json.Marshal(map[string]any{
		"session_id":      "sess_kiro_1",
		"hook_event_name": "UserPromptSubmit",
		"cwd":             repo,
		"prompt":          "Message from quiet-otter via Murmur",
	})
	out := runKiro(string(promptPayload))
	want := view.HookWithInbox(nil, msgs, 1, view.KiroInboxTool)
	if out != want {
		t.Errorf("UserPromptSubmit output = %q, want plain text %q", out, want)
	}
	if strings.Contains(out, "hookSpecificOutput") {
		t.Errorf("UserPromptSubmit wrapped output in Claude Code JSON: %q", out)
	}
	if !strings.Contains(out, view.KiroInboxTool) {
		t.Errorf("UserPromptSubmit didn't name %s: %q", view.KiroInboxTool, out)
	}

	// Stop: JSON block decision with full message text.
	stopPayload, _ := json.Marshal(map[string]any{
		"session_id":      "sess_kiro_1",
		"hook_event_name": "Stop",
		"cwd":             repo,
	})
	stopOut := runKiro(string(stopPayload))
	var dec struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stopOut), &dec); err != nil {
		t.Fatalf("Stop output isn't JSON (%v): %q", err, stopOut)
	}
	if dec.Decision != "block" || !strings.Contains(dec.Reason, "check api.go") {
		t.Errorf("Stop decision = %+v", dec)
	}

	// PostToolUse: must not call OpNotices or write stdout (Kiro 3.4.0 discards it).
	postPayload, _ := json.Marshal(map[string]any{
		"session_id":      "sess_kiro_1",
		"hook_event_name": "PostToolUse",
		"cwd":             repo,
		"tool_name":       "execute_bash",
	})
	if postOut := runKiro(string(postPayload)); postOut != "" {
		t.Errorf("PostToolUse printed %q, want empty", postOut)
	}

	reqs := got()
	notices := ops(reqs, proto.OpNotices)
	if len(notices) != 1 || notices[0].Agent != hook.Kiro || notices[0].ClaudeSession != "sess_kiro_1" || notices[0].Prompt != "Message from quiet-otter via Murmur" {
		t.Errorf("OpNotices requests = %+v", notices)
	}
	ends := ops(reqs, proto.OpTurnEnd)
	if len(ends) != 1 || ends[0].Agent != hook.Kiro || !ends[0].Continues {
		t.Errorf("OpTurnEnd requests = %+v", ends)
	}
}

// Kiro's hook finds a standalone Kiro session's moved clone (`mur join -C`) through its single
// here.Session record.
func TestKiroHookMovedClone(t *testing.T) {
	joined := newRepo(t)
	outside := t.TempDir()
	got := fakeDaemon(t, filepath.Join(joined, ".git"), func(r proto.Request) string {
		return `{"ok":true}`
	})
	publish(t, here.Session{ServerPID: os.Getpid(), ClientPID: os.Getppid(), GitDir: filepath.Join(joined, ".git"), Agent: "kiro", On: true})

	payload, _ := json.Marshal(map[string]any{
		"session_id":      "sess_moved_clone",
		"hook_event_name": "Stop",
		"cwd":             outside,
	})
	if out := runKiro(string(payload)); out != "" {
		t.Errorf("idle Stop printed %q", out)
	}
	ends := ops(got(), proto.OpTurnEnd)
	if len(ends) != 1 || ends[0].ClaudeSession != "sess_moved_clone" {
		t.Errorf("OpTurnEnd = %+v, want sess_moved_clone in joined clone", ends)
	}
}

// Kiro hooks require session_id in the stdin payload and must not fall back to KIRO_SESSION_ID
// from the environment, which a nested Kiro session can inherit from an outer Kiro session.
func TestKiroHookIgnoresEnvSessionIDWithoutPayload(t *testing.T) {
	repo := newRepo(t)
	got := fakeDaemon(t, filepath.Join(repo, ".git"), func(r proto.Request) string {
		return `{"ok":true}`
	})
	t.Setenv("KIRO_SESSION_ID", "inherited-outer-session")
	payload, _ := json.Marshal(map[string]any{
		"hook_event_name": "Stop",
		"cwd":             repo,
	})
	if out := runKiro(string(payload)); out != "" {
		t.Errorf("missing session_id printed %q, want empty", out)
	}
	if reqs := got(); len(reqs) != 0 {
		t.Errorf("missing session_id contacted daemon with %+v", reqs)
	}
}
