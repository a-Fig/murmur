package hook

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// The issues a call filed or closed are read from what each agent's hook is given after it (#510):
// gh's own output for a shell command, and the result's address for GitHub's MCP tools. A call of
// any other kind, or one that didn't file or close anything, reports none.
func TestIssuesACallFiledOrClosed(t *testing.T) {
	raw := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	shell := func(tool, command string, response any) input {
		return input{ToolName: tool, ToolInput: raw(map[string]any{"command": command}), ToolResponse: raw(response)}
	}
	mcp := func(tool string, args map[string]any, response any) input {
		return input{ToolName: tool, ToolInput: raw(args), ToolResponse: raw(response)}
	}
	// A result of GitHub's MCP server, as its text content.
	result := func(v any) any {
		return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw(v))}}}
	}
	opened := func(repo string, n int) proto.Issue { return proto.Issue{Repo: repo, Number: n} }
	closed := func(repo string, n int) proto.Issue { return proto.Issue{Repo: repo, Number: n, Closed: true} }
	cases := []struct {
		name string
		in   input
		want []proto.Issue
	}{
		{"Claude Code's gh issue create",
			shell("Bash", `gh issue create --title "Recent: name the filer" --body "$(cat body.md)" --label bug`,
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/37\n", "stderr": "", "interrupted": false}),
			[]proto.Issue{opened("a-Fig/murmur", 37)}},
		{"Codex's response is the command's output alone",
			shell("Bash", "gh issue new -t x -b y", "https://github.com/a-Fig/murmur/issues/38\n"),
			[]proto.Issue{opened("a-Fig/murmur", 38)}},
		{"Codex's command as words",
			input{ToolName: "Bash", ToolInput: raw(map[string]any{"command": []string{"gh", "issue", "create", "-t", "x"}}),
				ToolResponse: raw("https://github.com/a-Fig/murmur/issues/39\n")},
			[]proto.Issue{opened("a-Fig/murmur", 39)}},
		{"Grok's terminal",
			shell("run_terminal_command", "gh issue create -t x -b y",
				map[string]any{"type": "Bash", "exit_code": 0, "output_for_prompt": "https://github.com/a-Fig/murmur/issues/40"}),
			[]proto.Issue{opened("a-Fig/murmur", 40)}},
		{"Pi's bash, its result shaped as an MCP tool's",
			shell("bash", "gh issue create -t x -b y",
				map[string]any{"content": []any{map[string]any{"type": "text", "text": "https://github.com/a-Fig/murmur/issues/44\n"}}, "isError": false}),
			[]proto.Issue{opened("a-Fig/murmur", 44)}},
		{"Pi's powershell",
			shell("powershell", "gh issue close 45",
				map[string]any{"content": []any{map[string]any{"type": "text", "text": "✓ Closed issue a-Fig/murmur#45 (x)\n"}}, "isError": false}),
			[]proto.Issue{closed("a-Fig/murmur", 45)}},
		{"gh issue close",
			shell("Bash", "gh issue close 41 --comment 'Fixed by #42'",
				map[string]any{"stdout": "", "stderr": "✓ Closed issue a-Fig/murmur#41 (Recent: name the filer)\n"}),
			[]proto.Issue{closed("a-Fig/murmur", 41)}},
		{"an issue closed already",
			shell("Bash", "gh issue close 41", map[string]any{"stderr": "! Issue a-Fig/murmur#41 (x) is already closed\n"}), nil},
		{"a link in other output isn't an issue filed",
			shell("Bash", "gh issue view 37 && gh issue create -t x",
				map[string]any{"stdout": "see https://github.com/a-Fig/murmur/issues/12 for more\nhttps://github.com/a-Fig/murmur/issues/43\n"}),
			[]proto.Issue{opened("a-Fig/murmur", 43)}},
		{"a shell's argv",
			input{ToolName: "Bash", ToolInput: raw(map[string]any{"command": []string{"bash", "-lc", "cd murmur && gh issue close 41"}}),
				ToolResponse: raw("✓ Closed issue a-Fig/murmur#41 (x)\n")},
			[]proto.Issue{closed("a-Fig/murmur", 41)}},
		{"gh where a command starts",
			shell("Bash", "cd murmur && GH_REPO=a-Fig/murmur gh issue create -t x | tee out; url=\"$(gh issue create -t y)\"\nfor n in 7; do gh issue close $n; done",
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/51\nhttps://github.com/a-Fig/murmur/issues/52\n", "stderr": "✓ Closed issue a-Fig/murmur#7 (x)\n"}),
			[]proto.Issue{opened("a-Fig/murmur", 51), opened("a-Fig/murmur", 52), closed("a-Fig/murmur", 7)}},
		{"gh after if, elif and until",
			shell("Bash", "if gh issue close 8; then :; elif gh issue close 9; then :; fi; until gh issue create -t x; do sleep 5; done",
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/53\n", "stderr": "✓ Closed issue a-Fig/murmur#8 (x)\n"}),
			[]proto.Issue{opened("a-Fig/murmur", 53), closed("a-Fig/murmur", 8)}},
		{"an apostrophe in a comment",
			shell("Bash", "# don't forget the label\ngh issue create -t x --label bug",
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/54\n"}),
			[]proto.Issue{opened("a-Fig/murmur", 54)}},
		{"a comment that names the command",
			shell("Bash", "gh issue view 12 # read it; gh issue close 12 later",
				map[string]any{"stdout": "title: x\n", "stderr": "✓ Closed issue a-Fig/murmur#12 (x)\n"}), nil},
		{"a grep for the command, whose context shows a close",
			shell("Bash", `rg -n -C2 "gh issue close" internal/hook`,
				map[string]any{"stdout": "internal/hook/issues_internal_test.go-56-\t\t\t\tmap[string]any{\"stdout\": \"\", \"stderr\": \"✓ Closed issue a-Fig/murmur#41 (Recent: name the filer)\\n\"}),\n"}), nil},
		{"a diff that adds a close line",
			shell("Bash", `git log -p -S'gh issue close'`,
				map[string]any{"stdout": "+\t\t\t\tmap[string]any{\"stderr\": \"✓ Closed issue a-Fig/murmur#41 (x)\\n\"}),\n"}), nil},
		{"an echo of the command, then issues listed",
			shell("Bash", `echo "next: gh issue create"; gh issue list --json url --jq '.[].url'`,
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/5\nhttps://github.com/a-Fig/murmur/issues/6\n"}), nil},
		{"a create, then another issue viewed",
			shell("Bash", `gh issue create -t x -b y && gh issue view 12 --json url -q .url`,
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/60\nhttps://github.com/a-Fig/murmur/issues/12\n"}), nil},
		{"a body that names the command",
			shell("Bash", "gh issue create -t x --body \"$(cat <<'EOF'\nthen; gh issue close 9\nEOF\n)\"",
				map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/61\n"}),
			[]proto.Issue{opened("a-Fig/murmur", 61)}},
		{"a close line inside other output",
			shell("Bash", "gh issue close 41 && cat notes.txt",
				map[string]any{"stdout": "we said Closed issue a-Fig/murmur#40 (x) before\n", "stderr": "✓ Closed issue a-Fig/murmur#41 (x)\n"}),
			[]proto.Issue{closed("a-Fig/murmur", 41)}},
		{"a command that runs no gh issue create",
			shell("Bash", "gh issue list", map[string]any{"stdout": "https://github.com/a-Fig/murmur/issues/37\n"}), nil},
		{"a failed create",
			shell("Bash", "gh issue create -t x", map[string]any{"stderr": "could not create issue: HTTP 410\n"}), nil},
		{"a tool that runs no command",
			input{ToolName: "Read", ToolResponse: raw("https://github.com/a-Fig/murmur/issues/37\n")}, nil},
		{"GitHub's MCP create_issue, the whole issue",
			mcp("mcp__github__create_issue", map[string]any{"owner": "a-Fig", "repo": "murmur", "title": "x", "body": "see https://github.com/a-Fig/murmur/issues/12"},
				result(map[string]any{"number": 44, "html_url": "https://github.com/a-Fig/murmur/issues/44", "body": "see https://github.com/a-Fig/murmur/issues/12",
					"user": map[string]any{"html_url": "https://github.com/tyler"}})),
			[]proto.Issue{opened("a-Fig/murmur", 44)}},
		{"issue_write create, its short answer",
			mcp("mcp__github__issue_write", map[string]any{"method": "create", "owner": "a-Fig", "repo": "murmur", "title": "x"},
				result(map[string]any{"id": "3601", "url": "https://github.com/a-Fig/murmur/issues/45"})),
			[]proto.Issue{opened("a-Fig/murmur", 45)}},
		{"Grok's name for an MCP tool",
			mcp("github__create_issue", map[string]any{"title": "x"}, result(map[string]any{"html_url": "https://github.com/a-Fig/murmur/issues/46"})),
			[]proto.Issue{opened("a-Fig/murmur", 46)}},
		{"issue_write closing",
			mcp("mcp__github__issue_write", map[string]any{"method": "update", "issue_number": 47, "state": "closed", "state_reason": "completed"},
				result(map[string]any{"url": "https://github.com/a-Fig/murmur/issues/47"})),
			[]proto.Issue{closed("a-Fig/murmur", 47)}},
		{"update_issue closing",
			mcp("mcp__github__update_issue", map[string]any{"issue_number": "48", "state": "closed"},
				result(map[string]any{"html_url": "https://github.com/a-Fig/murmur/issues/48", "state": "closed"})),
			[]proto.Issue{closed("a-Fig/murmur", 48)}},
		{"an update that doesn't close",
			mcp("mcp__github__issue_write", map[string]any{"method": "update", "issue_number": 47, "title": "y"},
				result(map[string]any{"url": "https://github.com/a-Fig/murmur/issues/47"})), nil},
		{"an MCP error",
			mcp("mcp__github__create_issue", map[string]any{"title": "x"},
				map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "https://github.com/a-Fig/murmur/issues/49"}}}), nil},
		{"Pi's failed close, whose error's details give the issue's url",
			mcp("mcp__github__issue_write", map[string]any{"method": "update", "issue_number": 47, "state": "closed"},
				map[string]any{"isError": true, "content": []any{map[string]any{"type": "text",
					"text": string(raw(map[string]any{"message": "Resource not accessible by integration", "url": "https://github.com/a-Fig/murmur/issues/47"}))}}}), nil},
		{"another tracker's create_issue",
			mcp("mcp__linear__create_issue", map[string]any{"title": "x"}, result(map[string]any{"url": "https://linear.app/team/issue/ENG-12"})), nil},
		{"a pull request isn't an issue",
			mcp("mcp__github__create_pull_request", map[string]any{"title": "x"}, result(map[string]any{"html_url": "https://github.com/a-Fig/murmur/issues/50"})), nil},
	}
	for _, c := range cases {
		if got := issuesOf(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestRealPayloadShapesRegression pins real payload shapes from Claude Code, Codex, Grok, Pi,
// and Antigravity (taken from existing tests and docs/design/clients.md) so hardening never
// changes what issues or events real harnesses extract (#1142).
func TestRealPayloadShapesRegression(t *testing.T) {
	opened := func(repo string, n int) proto.Issue { return proto.Issue{Repo: repo, Number: n} }
	closed := func(repo string, n int) proto.Issue { return proto.Issue{Repo: repo, Number: n, Closed: true} }

	issuePayloads := []struct {
		name    string
		agent   string
		payload string
		want    []proto.Issue
	}{
		{
			name:  "Claude Code Bash gh issue create with CRLF",
			agent: "",
			payload: `{"session_id":"claude-1","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"Bash","tool_use_id":"toolu_1",` +
				`"tool_input":{"command":"gh issue create --title \"Bug\" --body \"details\""},` +
				`"tool_response":{"stdout":"Creating issue in a-Fig/murmur\r\n\r\nhttps://github.com/a-Fig/murmur/issues/1142\r\n","stderr":"","interrupted":false}}`,
			want: []proto.Issue{opened("a-Fig/murmur", 1142)},
		},
		{
			name:  "Claude Code Bash gh issue close without checkmark",
			agent: "",
			payload: `{"session_id":"claude-1","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"Bash","tool_use_id":"toolu_2",` +
				`"tool_input":{"command":"gh issue close 1142 --reason completed"},` +
				`"tool_response":{"stdout":"","stderr":"Closed issue a-Fig/murmur#1142 (Harden hook)\r\n","interrupted":false}}`,
			want: []proto.Issue{closed("a-Fig/murmur", 1142)},
		},
		{
			name:  "Claude Code GitHub MCP issue_write create",
			agent: "",
			payload: `{"session_id":"claude-1","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"mcp__github__issue_write","tool_use_id":"toolu_3",` +
				`"tool_input":{"method":"create","owner":"a-Fig","repo":"murmur","title":"t"},` +
				`"tool_response":{"content":[{"type":"text","text":"{\"id\":1,\"number\":501,\"html_url\":\"https://github.com/a-Fig/murmur/issues/501\"}"}],"isError":false}}`,
			want: []proto.Issue{opened("a-Fig/murmur", 501)},
		},
		{
			name:  "Codex Bash argv gh issue create",
			agent: Codex,
			payload: `{"session_id":"codex-1","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"Bash","tool_use_id":"call_1",` +
				`"tool_input":{"command":["gh","issue","create","-t","x","-b","y"]},` +
				`"tool_response":"https://github.com/a-Fig/murmur/issues/502\n"}`,
			want: []proto.Issue{opened("a-Fig/murmur", 502)},
		},
		{
			name:  "Codex Bash shell argv gh issue close",
			agent: Codex,
			payload: `{"session_id":"codex-1","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"Bash","tool_use_id":"call_2",` +
				`"tool_input":{"command":["bash","-lc","gh issue close 502"]},` +
				`"tool_response":"✓ Closed issue a-Fig/murmur#502 (x)\n"}`,
			want: []proto.Issue{closed("a-Fig/murmur", 502)},
		},
		{
			name:  "Grok run_terminal_command with toolResult and tool_response",
			agent: Grok,
			payload: `{"sessionId":"grok-1","hook_event_name":"PostToolUse","cwd":"/repo","toolName":"run_terminal_command",` +
				`"toolInput":{"command":"gh issue close 503"},` +
				`"toolResult":{"type":"Bash"},"tool_response":{"type":"Bash","exit_code":0,"output_for_prompt":"✓ Closed issue a-Fig/murmur#503 (x)\n"}}`,
			want: []proto.Issue{closed("a-Fig/murmur", 503)},
		},
		{
			name:  "Grok MCP github__create_issue",
			agent: Grok,
			payload: `{"sessionId":"grok-1","hook_event_name":"PostToolUse","cwd":"/repo","toolName":"github__create_issue",` +
				`"toolInput":{"title":"x"},` +
				`"tool_response":{"content":[{"type":"text","text":"{\"html_url\":\"https://github.com/a-Fig/murmur/issues/504\"}"}]}}`,
			want: []proto.Issue{opened("a-Fig/murmur", 504)},
		},
		{
			name:  "Pi powershell gh issue create",
			agent: Pi,
			payload: `{"session_id":"/s/p.jsonl","hook_event_name":"PostToolUse","cwd":"/repo","tool_name":"powershell",` +
				`"tool_input":{"command":"gh issue create -t x -b y"},` +
				`"tool_response":{"content":[{"type":"text","text":"https://github.com/a-Fig/murmur/issues/505\r\n"}],"isError":false}}`,
			want: []proto.Issue{opened("a-Fig/murmur", 505)},
		},
	}

	for _, tc := range issuePayloads {
		var in input
		if err := json.Unmarshal([]byte(tc.payload), &in); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if tc.agent == Grok {
			normalizeGrok(&in)
		}
		if got := issuesOf(in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: issuesOf = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	// Real Antigravity payload shapes (PreInvocation, Stop, PreToolUse, PostToolUse).
	agyCases := []struct {
		name      string
		event     string
		payload   string
		wantOK    bool
		wantEvent string
		wantID    string
		wantTool  string
		wantUseID string
	}{
		{
			name:      "Antigravity PreInvocation first call",
			event:     "PreInvocation",
			payload:   `{"conversationId":"conv-1","workspacePaths":["/repo"],"lastUserInput":"fix bug"}`,
			wantOK:    true,
			wantEvent: "UserPromptSubmit",
			wantID:    "conv-1",
		},
		{
			name:      "Antigravity Stop ordinary end",
			event:     "Stop",
			payload:   `{"conversationId":"conv-1","cwd":"/repo","terminationReason":"NO_TOOL_CALL","fullyIdle":true}`,
			wantOK:    true,
			wantEvent: stop,
			wantID:    "conv-1",
		},
		{
			name:      "Antigravity PreToolUse run_command",
			event:     preTool,
			payload:   `{"conversationId":"conv-1","cwd":"/repo","stepIdx":3,"toolCall":{"id":"toolu_9","name":"run_command","args":{"Cwd":"/repo"}}}`,
			wantOK:    true,
			wantEvent: preTool,
			wantID:    "conv-1",
			wantTool:  antigravityCommand,
			wantUseID: "toolu_9",
		},
		{
			name:      "Antigravity PostToolUse failure with stepIdx fallback",
			event:     postTool,
			payload:   `{"conversationId":"conv-1","cwd":"/repo","stepIdx":4,"error":"exit status 1","toolCall":{"id":"","name":"run_command","args":{"Cwd":""}}}`,
			wantOK:    true,
			wantEvent: postFailure,
			wantID:    "conv-1",
			wantTool:  antigravityCommand,
			wantUseID: "step-4",
		},
	}
	for _, tc := range agyCases {
		var in input
		if err := json.Unmarshal([]byte(tc.payload), &in); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		ok := fromAntigravity(&in, tc.event)
		if ok != tc.wantOK || in.Event != tc.wantEvent || in.SessionID != tc.wantID ||
			in.ToolName != tc.wantTool || in.ToolUseID != tc.wantUseID {
			t.Errorf("%s: ok=%v event=%q id=%q tool=%q useID=%q",
				tc.name, ok, in.Event, in.SessionID, in.ToolName, in.ToolUseID)
		}
	}
}

func TestHookPayloadAndIssueHardeningGuards(t *testing.T) {
	t.Run("normalizeGrok separates toolName, toolInput, and toolResult fallbacks", func(t *testing.T) {
		var mixed input
		if err := json.Unmarshal([]byte(`{"sessionId":"g1","toolName":"run_terminal_command","tool_input":{"command":"gh issue close 7"},"toolResult":{"output":"✓ Closed issue a-Fig/murmur#7 (x)\n"}}`), &mixed); err != nil {
			t.Fatal(err)
		}
		normalizeGrok(&mixed)
		want := []proto.Issue{{Repo: "a-Fig/murmur", Number: 7, Closed: true}}
		if got := issuesOf(mixed); !reflect.DeepEqual(got, want) {
			t.Errorf("mixed Grok fields: %+v, want %+v", got, want)
		}

		var snakeNameCamelInput input
		if err := json.Unmarshal([]byte(`{"session_id":"g2","tool_name":"run_terminal_command","toolInput":{"command":"gh issue create"},"tool_response":null,"toolResult":"https://github.com/a-Fig/murmur/issues/8\n"}`), &snakeNameCamelInput); err != nil {
			t.Fatal(err)
		}
		normalizeGrok(&snakeNameCamelInput)
		want2 := []proto.Issue{{Repo: "a-Fig/murmur", Number: 8}}
		if got := issuesOf(snakeNameCamelInput); !reflect.DeepEqual(got, want2) {
			t.Errorf("snake tool_name + camel toolInput + null tool_response: %+v, want %+v", got, want2)
		}
	})

	t.Run("resultIssue is deterministic and bounds JSON-string re-decode depth and queue size", func(t *testing.T) {
		sibling := map[string]any{
			"z": map[string]any{"url": "https://github.com/a-Fig/murmur/issues/99"},
			"a": map[string]any{"url": "https://github.com/a-Fig/murmur/issues/11"},
		}
		for range 20 {
			m := resultIssue(sibling)
			if m == nil || m[3] != "11" {
				t.Fatalf("resultIssue sibling = %v, want deterministic match 11", m)
			}
		}

		// Depth 2 (double-wrapped JSON string) is decoded; depth 3 is capped.
		inner, _ := json.Marshal(map[string]any{"url": "https://github.com/a-Fig/murmur/issues/22"})
		d1, _ := json.Marshal(map[string]any{"text": string(inner)})
		d2, _ := json.Marshal(map[string]any{"text": string(d1)})
		if m := resultIssue(map[string]any{"content": string(d1)}); m == nil || m[3] != "22" {
			t.Errorf("depth 2 JSON string = %v, want 22", m)
		}
		if m := resultIssue(map[string]any{"content": string(d2)}); m != nil {
			t.Errorf("depth 3 JSON string = %v, want nil (capped)", m)
		}

		// Large flat slice beyond maxWalkNodes stays bounded and visits early items first.
		big := make([]any, maxWalkNodes+5000)
		for i := range big {
			big[i] = "x"
		}
		big[0] = "first"
		big[len(big)-1] = "last"
		var visited int
		var first string
		var sawLast bool
		walk(big, func(s string) {
			if visited == 0 {
				first = s
			}
			if s == "last" {
				sawLast = true
			}
			visited++
		})
		if first != "first" || sawLast || visited != maxWalkNodes-1 {
			t.Errorf("walk(big): first=%q sawLast=%v visited=%d, want first=%q sawLast=false visited=%d",
				first, sawLast, visited, "first", maxWalkNodes-1)
		}

		bigRes := make([]any, maxWalkNodes+5000)
		bigRes[len(bigRes)-1] = map[string]any{"url": "https://github.com/a-Fig/murmur/issues/88"}
		if m := resultIssue(bigRes); m != nil {
			t.Errorf("resultIssue past maxWalkNodes = %v, want nil", m)
		}
	})

	t.Run("mcpIssues rejects non-integer, overflowing, or non-positive issue_number", func(t *testing.T) {
		resp := json.RawMessage(`{"url":"https://github.com/a-Fig/murmur/issues/47"}`)
		for _, num := range []string{`47.5`, `99999999999999999999999`, `-47`, `0`, `true`, `{"n":47}`} {
			in := json.RawMessage(`{"method":"update","state":"closed","issue_number":` + num + `}`)
			if got := mcpIssues("issue_write", in, resp); got != nil {
				t.Errorf("issue_number=%s: got %+v, want nil", num, got)
			}
		}
	})

	t.Run("shellIssues rejects non-string argv and shell metachars in direct gh argv", func(t *testing.T) {
		resp := json.RawMessage(`"https://github.com/a-Fig/murmur/issues/77\n"`)
		if got := shellIssues(json.RawMessage(`{"command":["gh",123,"issue","create"]}`), resp); got != nil {
			t.Errorf("non-string argv = %+v, want nil", got)
		}
		if got := shellIssues(json.RawMessage(`{"command":["gh","issue","create; gh issue close 77"]}`), resp); got != nil {
			t.Errorf("direct gh argv with metachars = %+v, want nil", got)
		}
	})

	t.Run("fromAntigravity tolerates non-object args and skips empty workspacePaths", func(t *testing.T) {
		var strArgs input
		if err := json.Unmarshal([]byte(`{"conversationId":"conv-2","workspacePaths":["","/repo"],"toolCall":{"id":"t1","name":"view_file","args":"raw string"}}`), &strArgs); err != nil {
			t.Fatalf("unmarshal non-object args: %v", err)
		}
		if !fromAntigravity(&strArgs, preTool) || strArgs.Cwd != "/repo" || strArgs.ToolName != "view_file" {
			t.Errorf("fromAntigravity(string args) = %+v", strArgs)
		}

		var relCwd input
		if err := json.Unmarshal([]byte(`{"conversationId":"conv-3","workspacePaths":["","/repo"],"toolCall":{"id":"t2","name":"run_command","args":{"Cwd":"sub"}}}`), &relCwd); err != nil {
			t.Fatalf("unmarshal relative Cwd with workspacePaths: %v", err)
		}
		if !fromAntigravity(&relCwd, preTool) || relCwd.Cwd != "/repo" || relCwd.toolCallCwd != filepath.Join("/repo", "sub") {
			t.Errorf("fromAntigravity(workspacePaths + relative Cwd): cwd=%q toolCallCwd=%q", relCwd.Cwd, relCwd.toolCallCwd)
		}
	})
}
