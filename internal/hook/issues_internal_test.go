package hook

import (
	"encoding/json"
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
		{"Pi's bash, its result as content blocks",
			shell("bash", "gh issue create -t x -b y",
				[]any{map[string]any{"type": "text", "text": "https://github.com/a-Fig/murmur/issues/44\n"}}),
			[]proto.Issue{opened("a-Fig/murmur", 44)}},
		{"Pi's powershell",
			shell("powershell", "gh issue close 45",
				[]any{map[string]any{"type": "text", "text": "✓ Closed issue a-Fig/murmur#45 (x)\n"}}),
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
