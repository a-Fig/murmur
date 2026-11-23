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

// Antigravity's hook (#650) is named its event by join's command line. PreInvocation fetches the
// notices with the turn's prompt and gives them back as a system message; Stop ends the turn and
// keeps it going with "continue" only at a turn's ordinary end and without active background work.
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
	out := agy("PreInvocation", nil) // protojson may omit invocationNum: 0
	if err := json.Unmarshal([]byte(out), &steps); err != nil || len(steps.InjectSteps) != 1 {
		t.Fatalf("PreInvocation printed %q: %v", out, err)
	}
	msgs := []proto.MessageView{{ID: "m1", From: "other-0001/bbbbbb", Subject: "hi"}}
	notice := steps.InjectSteps[0].SystemMessage.SystemMessage
	if want := view.HookWithInbox(nil, msgs, 1, view.AntigravityInboxTool); notice != want {
		t.Errorf("PreInvocation gave %q, want %q", notice, want)
	}
	t.Logf("Hook notice before:\n%sAfter:\n%s", view.Hook(nil, msgs, 1), notice)

	// Later calls in the turn carry the same lastUserInput, and send no prompt.
	agy("PreInvocation", map[string]any{"invocationNum": 1, "cwd": repo, "workspacePaths": []string{t.TempDir()}})

	m := proto.MessageView{ID: "m2", From: "other-0001/bbbbbb", Subject: "auth?", Body: "Changing it?"}
	stops := []struct {
		fields    map[string]any
		continues bool
	}{
		{map[string]any{"terminationReason": "NO_TOOL_CALL", "fullyIdle": true}, true},
		{map[string]any{"terminationReason": "NO_TOOL_CALL"}, true},
		{map[string]any{"terminationReason": "NO_TOOL_CALL", "fullyIdle": false}, false},
		{map[string]any{"terminationReason": "USER_CANCELED", "fullyIdle": true}, false},
	}
	for _, c := range stops {
		out := agy("Stop", c.fields)
		if !c.continues {
			if out != "{}\n" {
				t.Errorf("Stop %v printed %q", c.fields, out)
			}
			continue
		}
		var stop struct{ Decision, Reason string }
		want := view.Hook([]proto.MessageView{m}, nil, 0)
		if err := json.Unmarshal([]byte(out), &stop); err != nil || stop.Decision != "continue" || stop.Reason != want {
			t.Errorf("Stop %v printed %q, want continue with %q", c.fields, out, want)
		}
	}

	notices := ops(requests(), proto.OpNotices)
	if len(notices) != 2 {
		t.Fatalf("notices: %+v", notices)
	}
	if r := notices[0]; r.Agent != hook.Antigravity || r.ClaudeSession != "conv-1" || r.Prompt != "fix the test" || r.Transcript != "" {
		t.Errorf("PreInvocation's request: %+v", r)
	}
	if r := notices[1]; r.Prompt != "" {
		t.Errorf("the second PreInvocation's request: %+v", r)
	}
	ends := ops(requests(), proto.OpTurnEnd)
	if len(ends) != len(stops) {
		t.Fatalf("turn ends: %+v", ends)
	}
	for i, r := range ends {
		if r.Continues != stops[i].continues || r.ClaudeSession != "conv-1" {
			t.Errorf("Stop %v sent %+v", stops[i].fields, r)
		}
	}
}

func TestAntigravityHookFindsMovedSession(t *testing.T) {
	joined, scratch := newRepo(t), t.TempDir()
	gitDir := filepath.Join(joined, ".git")
	requests := fakeDaemon(t, gitDir, func(r proto.Request) string {
		if r.Op == proto.OpTurnEnd {
			return `{"ok":true,"delivered":[{"from":"other-0001/bbbbbb","subject":"hi"}]}`
		}
		return `{"ok":true,"unread":1}`
	})
	payload, _ := json.Marshal(map[string]string{"conversationId": "conv-1", "cwd": scratch})
	for _, c := range []struct {
		name, agent, thread, recordThread string
		want                              bool
	}{
		{"T3", "antigravity in T3", "thread-1", "thread-1", true},
		{"standalone", "antigravity", "", "", true},
		{"standalone under T3", "antigravity", "parent-thread", "", true},
		{"unrelated thread", "antigravity in T3", "thread-1", "thread-2", false},
		{"Claude parent", "claude-code", "", "", false},
		{"Codex parent", "codex in T3", "thread-1", "thread-1", false},
		{"unknown agent", "", "", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(t3.ThreadEnv, c.thread)
			publish(t, here.Session{ServerPID: os.Getpid(), ClientPID: os.Getppid(), GitDir: gitDir, On: true,
				Agent: c.agent, T3Thread: c.recordThread})
			for _, call := range []struct{ event, op, output string }{
				{"PreInvocation", proto.OpNotices, "1 message unread"},
				{"Stop", proto.OpTurnEnd, `"decision":"continue"`},
			} {
				before := len(ops(requests(), call.op))
				var out bytes.Buffer
				hook.RunForEvent(hook.Antigravity, call.event, bytes.NewReader(payload), &out)
				if got := strings.Contains(out.String(), call.output); got != c.want {
					t.Errorf("%s printed %s", call.event, out.String())
				}
				if got := len(ops(requests(), call.op)) > before; got != c.want {
					t.Errorf("%s reached the joined daemon: %v, want %v", call.event, got, c.want)
				}
			}
		})
	}
}

func TestAntigravityToolHooks(t *testing.T) {
	repo, other := newRepo(t), newRepo(t)
	gitDir := filepath.Join(repo, ".git")
	requests := fakeDaemon(t, gitDir, okToAll)
	sub := filepath.Join(other, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(repo, sub)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		event, name, id, err, cwd, op, useID string
		step                                 *int
	}{
		{"PreToolUse", "run_command", "toolu_01", "", "", proto.OpToolStart, "toolu_01", new(3)},
		{"PostToolUse", "run_command", "toolu_01", "", "", proto.OpToolEnd, "toolu_01", new(4)},
		{"PreToolUse", "run_command", "", "", sub, proto.OpToolStart, "step-0", new(0)},
		{"PostToolUse", "run_command", "", "exit status 1", sub, proto.OpToolEnd, "step-0", new(0)},
		{"PreToolUse", "run_command", "relative", "", relative, proto.OpToolStart, "relative", nil},
		{"PreToolUse", "view_file", "reader", "", "", "", "", nil},
		{"PostToolUse", "view_file", "reader", "", "", "", "", nil},
		{"PreToolUse", "run_command", "", "", "", "", "", nil},
		{"PostToolUse", "", "", "", "", "", "", new(5)},
	} {
		payload := map[string]any{"conversationId": "conv-1", "cwd": repo, "error": c.err}
		if c.step != nil {
			payload["stepIdx"] = *c.step
		}
		if c.name != "" {
			payload["toolCall"] = map[string]any{"id": c.id, "name": c.name, "args": map[string]string{"Cwd": c.cwd}}
		}
		b, _ := json.Marshal(payload)
		before := len(requests())
		var out bytes.Buffer
		hook.RunForEvent(hook.Antigravity, c.event, bytes.NewReader(b), &out)
		wantOut := "{}\n"
		if c.event == "PreToolUse" {
			wantOut = "{\"decision\":\"allow\"}\n"
		}
		if out.String() != wantOut {
			t.Errorf("%s %s printed %q, want %q", c.event, c.name, out.String(), wantOut)
		}
		var got []proto.Request
		for _, r := range requests()[before:] {
			if r.Op != proto.OpPing {
				got = append(got, r)
			}
		}
		if c.op == "" {
			if len(got) != 0 {
				t.Errorf("%s %s sent %+v", c.event, c.name, got)
			}
			continue
		}
		wantCwd, wantGitDir := repo, gitDir
		if c.cwd != "" {
			wantCwd, wantGitDir = sub, filepath.Join(other, ".git")
		}
		if len(got) != 1 || got[0].Op != c.op || got[0].ToolUseID != c.useID ||
			got[0].Agent != hook.Antigravity || got[0].ClaudeSession != "conv-1" || got[0].Cwd != wantCwd {
			t.Fatalf("%s %s sent %+v", c.event, c.useID, got)
		}
		want, err := os.Stat(wantGitDir)
		if at, atErr := os.Stat(got[0].GitDir); err != nil || atErr != nil || !os.SameFile(at, want) {
			t.Errorf("%s %s sent GitDir %q, want %q", c.event, c.useID, got[0].GitDir, wantGitDir)
		}
	}

	var out bytes.Buffer
	hook.RunForEvent(hook.Antigravity, "PreToolUse", strings.NewReader("{"), &out)
	if out.String() != "{\"decision\":\"allow\"}\n" {
		t.Errorf("malformed PreToolUse printed %q", out.String())
	}

	// Non-object args on another tool and nil stdin/stdout never panic and preserve Antigravity's
	// JSON contract (#1142).
	out.Reset()
	hook.RunForEvent(hook.Antigravity, "PreToolUse",
		strings.NewReader(`{"conversationId":"conv-1","cwd":"`+filepath.ToSlash(repo)+`","toolCall":{"id":"t1","name":"view_file","args":"not-an-object"}}`), &out)
	if out.String() != "{\"decision\":\"allow\"}\n" {
		t.Errorf("non-object args PreToolUse printed %q", out.String())
	}
	out.Reset()
	hook.RunForEvent(hook.Antigravity, "PreToolUse", nil, &out)
	if out.String() != "{\"decision\":\"allow\"}\n" {
		t.Errorf("nil stdin PreToolUse printed %q", out.String())
	}
	hook.RunForEvent(hook.Antigravity, "PreToolUse", nil, nil)
	hook.RunForEvent("", "PostToolUse", nil, nil)
}
