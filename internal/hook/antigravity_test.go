package hook_test

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// Antigravity's hook (#650) is named its event by join's command line. PreInvocation fetches the
// notices with the turn's prompt and gives them back as a system message; Stop ends the turn and
// keeps it going with "continue" only at a turn's ordinary end. Every answer is a JSON object, {}
// where there is nothing to say, as Antigravity wants one.
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
	out := agy("PreInvocation", map[string]any{"invocationNum": 1})
	if err := json.Unmarshal([]byte(out), &steps); err != nil || len(steps.InjectSteps) != 1 {
		t.Fatalf("PreInvocation printed %q: %v", out, err)
	}
	notice := view.Hook(nil, []proto.MessageView{{ID: "m1", From: "other-0001/bbbbbb", Subject: "hi"}}, 1)
	if got := steps.InjectSteps[0].SystemMessage.SystemMessage; got != notice {
		t.Errorf("PreInvocation gave %q, want %q", got, notice)
	}

	// Later calls in the turn carry the same lastUserInput, and send no prompt.
	agy("PreInvocation", map[string]any{"invocationNum": 2})

	var stop struct{ Decision, Reason string }
	out = agy("Stop", map[string]any{"terminationReason": "NO_TOOL_CALL", "fullyIdle": true})
	if err := json.Unmarshal([]byte(out), &stop); err != nil {
		t.Fatalf("Stop printed %q: %v", out, err)
	}
	m := proto.MessageView{ID: "m2", From: "other-0001/bbbbbb", Subject: "auth?", Body: "Changing it?"}
	if want := view.Hook([]proto.MessageView{m}, nil, 0); stop.Decision != "continue" || stop.Reason != want {
		t.Errorf("Stop printed %+v, want continue with %q", stop, want)
	}
	// A turn the user cancelled ends, and keeps nothing going.
	if out := agy("Stop", map[string]any{"terminationReason": "USER_CANCELED"}); out != "{}\n" {
		t.Errorf("a cancelled Stop printed %q", out)
	}
	// An event join doesn't install it on asks nothing, and still answers.
	if out := agy("PostToolUse", nil); out != "{}\n" {
		t.Errorf("PostToolUse printed %q", out)
	}

	var ops []proto.Request
	for _, r := range requests() {
		if r.Op != proto.OpPing {
			ops = append(ops, r)
		}
	}
	if len(ops) != 4 {
		t.Fatalf("requests: %+v", ops)
	}
	if r := ops[0]; r.Op != proto.OpNotices || r.Agent != hook.Antigravity || r.ClaudeSession != "conv-1" || r.Prompt != "fix the test" || r.Transcript != "" {
		t.Errorf("PreInvocation's request: %+v", r)
	}
	if r := ops[1]; r.Op != proto.OpNotices || r.Prompt != "" {
		t.Errorf("the second PreInvocation's request: %+v", r)
	}
	ops = ops[1:]
	if r := ops[1]; r.Op != proto.OpTurnEnd || !r.Continues || r.ClaudeSession != "conv-1" {
		t.Errorf("Stop's request: %+v", r)
	}
	if r := ops[2]; r.Op != proto.OpTurnEnd || r.Continues {
		t.Errorf("the cancelled Stop's request: %+v", r)
	}
}
