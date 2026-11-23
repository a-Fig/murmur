package hook

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/a-Fig/murmur/internal/proto"
)

// The GitHub issues a tool call filed or closed (#510), so that the page's Recent view can name the
// session that did it: GitHub says only which login opened an issue, and the agents on one machine
// share their user's. The hook reads them from the call's input and output, which an agent's hook
// is given after the call: Claude Code's PostToolUse as tool_input and tool_response, Codex's under
// the same names (codex-rs hooks, rust-v0.169.0), Grok's as toolInput, with tool_response beside its
// own toolResult (Grok Build's docs/user-guide/10-hooks.md, read 2026-11-15). Murmur's Pi extension
// passes them under Claude Code's names for a shell or MCP call, its result shaped as an MCP tool's:
// Pi's content blocks, and isError (#864). OpenCode's plugin passes neither, so an issue its sessions file shows under the login.
//
// Only calls of a known shape are read: a shell command that runs `gh issue create` or `gh issue
// close`, and an MCP tool of GitHub's server that creates an issue or closes one. Any other call's
// output is never looked at, and of these only the issue's repository and number, and whether it
// was closed, go to the daemon.

// shellTools are the agents' names for the tool that runs a command: Claude Code's and Codex's Bash,
// Grok's run_terminal_command, and Pi's bash and powershell (Pi 0.99.0's types).
var shellTools = map[string]bool{"Bash": true, "run_terminal_command": true, "bash": true, "powershell": true}

// maxIssues bounds what one call can report.
const (
	maxIssues    = 10
	maxWalkNodes = 10000
	maxJSONDepth = 2
)

var (
	// gh issue create (or new) or close where a command starts, in a command whose quoted text and
	// comments are masked (unquoted): at its start, or after ;, &, |, (, or a newline, past
	// assignments like GH_REPO=x and words like if, do, then or env that run what follows.
	ghIssueCmd = regexp.MustCompile(`(?m)(?:^|[;&|(\n])[ \t]*(?:(?:[A-Za-z_][A-Za-z0-9_]*=\S*|if|elif|while|until|do|then|else|time|command|exec|env|!|\{)[ \t]+)*gh[ \t]+issue[ \t]+(create|new|close)\b`)
	// What gh prints (cli/cli's pkg/cmd/issue, read 2026-11-15): `create` the new issue's address
	// on a line of its own, and `close` "✓ Closed issue owner/name#37 (title)", on stderr whether
	// or not that is a terminal. An issue that was closed already is "! Issue ... is already closed".
	createdLine = regexp.MustCompile(`(?m)^[ \t]*https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/issues/([0-9]+)[ \t\r]*$`)
	closedLine  = regexp.MustCompile(`(?m)^[ \t]*(?:✓[ \t]*)?Closed issue ([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([0-9]+) \(`)
	issueURL    = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/issues/([0-9]+)$`)
)

// issuesOf is the issues the call in's payload describes filed or closed, if any.
func issuesOf(in input) []proto.Issue {
	if shellTools[in.ToolName] {
		return shellIssues(in.ToolInput, in.ToolResponse)
	}
	if i := strings.LastIndex(in.ToolName, "__"); i > 0 {
		return mcpIssues(in.ToolName[i+2:], in.ToolInput, in.ToolResponse)
	}
	return nil
}

// shellIssues reads what gh printed, for a command that ran it to file or close an issue. It
// counts the gh issue commands the command runs, and if the output holds more issues filed or closed
// than that, something else in the command printed them (a gh issue list, a cat of a log), so it
// reports none rather than credit the call with an issue it didn't file.
func shellIssues(input, response json.RawMessage) []proto.Issue {
	var args struct {
		Command any `json:"command"` // a string; a list of words where an agent passes argv
	}
	if json.Unmarshal(input, &args) != nil {
		return nil
	}
	creates, closes := 0, 0
	switch c := args.Command.(type) {
	case string:
		creates, closes = ghIssueCalls(c)
	case []any:
		words := make([]string, 0, len(c))
		for _, w := range c {
			s, ok := w.(string)
			if !ok {
				return nil
			}
			words = append(words, s)
		}
		// argv is one command: gh's own words, or a shell's, whose last word is its script.
		if len(words) >= 3 && words[0] == "gh" && words[1] == "issue" {
			switch words[2] {
			case "create", "new":
				creates = 1
			case "close":
				closes = 1
			}
		} else if len(words) > 0 {
			creates, closes = ghIssueCalls(words[len(words)-1])
		}
	}
	if creates == 0 && closes == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(response, &v) != nil {
		return nil
	}
	var filed, shut []proto.Issue
	add := func(list *[]proto.Issue, m []string, isClosed bool) {
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 || len(*list) > maxIssues {
			return
		}
		is := proto.Issue{Repo: m[1] + "/" + m[2], Number: n, Closed: isClosed}
		for _, o := range *list {
			if o == is {
				return
			}
		}
		*list = append(*list, is)
	}
	walk(v, func(s string) {
		if creates > 0 {
			for _, m := range createdLine.FindAllStringSubmatch(s, maxIssues+1) {
				add(&filed, m, false)
			}
		}
		if closes > 0 {
			for _, m := range closedLine.FindAllStringSubmatch(s, maxIssues+1) {
				add(&shut, m, true)
			}
		}
	})
	if len(filed) > creates || len(shut) > closes {
		return nil
	}
	out := append(filed, shut...)
	if len(out) > maxIssues {
		out = out[:maxIssues]
	}
	return out
}

// ghIssueCalls counts the gh issue create (or new) and gh issue close commands a shell command runs,
// where a command starts and outside quotes, so text that only names one (an echo, a grep pattern, a
// commit message) doesn't count. It doesn't parse a shell: a loop that runs one close for several
// issues counts once.
func ghIssueCalls(command string) (creates, closes int) {
	for _, m := range ghIssueCmd.FindAllStringSubmatch(unquoted(command), -1) {
		if m[1] == "close" {
			closes++
		} else {
			creates++
		}
	}
	return creates, closes
}

// unquoted is command with what is inside quotes, or escaped by a backslash, replaced by '_', except
// a $( ) or backtick substitution inside double quotes, which runs as a command does. A backtick is
// written as ( or ), so that what follows one starts a command. A comment (a # that starts a word,
// to the end of its line) is masked too, so that an apostrophe in it (# don't) opens no quote.
func unquoted(command string) string {
	const (
		plain = iota
		single
		double
		sub  // $( ) or ( ), which ) ends
		tick // `, which ` ends
	)
	stack := []int{plain}
	var b strings.Builder
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		r, mode := rs[i], stack[len(stack)-1]
		switch mode {
		case single:
			if r == '\'' {
				stack = stack[:len(stack)-1]
			}
			b.WriteByte('_')
			continue
		case double:
			switch {
			case r == '"':
				stack = stack[:len(stack)-1]
				b.WriteByte('_')
			case r == '\\' && i+1 < len(rs):
				i++
				b.WriteString("__")
			case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
				i++
				stack = append(stack, sub)
				b.WriteString("$(")
			case r == '`':
				stack = append(stack, tick)
				b.WriteByte('(')
			default:
				b.WriteByte('_')
			}
			continue
		}
		switch {
		case r == '#' && (i == 0 || strings.ContainsRune(" \t\n;&|()<>", rs[i-1])):
			for ; i+1 < len(rs) && rs[i+1] != '\n'; i++ {
				b.WriteByte('_')
			}
			b.WriteByte('_')
		case r == '\\' && i+1 < len(rs) && rs[i+1] == '\n':
			i++
			b.WriteByte(' ') // a line continued
		case r == '\\' && i+1 < len(rs):
			i++
			b.WriteString("__")
		case r == '\'':
			stack = append(stack, single)
			b.WriteByte('_')
		case r == '"':
			stack = append(stack, double)
			b.WriteByte('_')
		case r == '(':
			stack = append(stack, sub)
			b.WriteRune(r)
		case r == ')' && mode == sub:
			stack = stack[:len(stack)-1]
			b.WriteRune(r)
		case r == '`' && mode == tick:
			stack = stack[:len(stack)-1]
			b.WriteByte(')')
		case r == '`':
			stack = append(stack, tick)
			b.WriteByte('(')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mcpIssues reads the issue a GitHub MCP tool filed or closed, named tool without its server's
// prefix: create_issue and update_issue, and issue_write with a method, of github/github-mcp-server.
// The issue is the one whose address the result gives; a call whose result names none, or says it
// failed, reports nothing.
func mcpIssues(tool string, input, response json.RawMessage) []proto.Issue {
	var args struct {
		Method string `json:"method"`
		State  string `json:"state"`
		Number any    `json:"issue_number"`
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if dec.Decode(&args) != nil {
		return nil
	}
	closed := false
	switch {
	case tool == "create_issue", tool == "issue_write" && args.Method == "create":
	case tool == "update_issue", tool == "issue_write" && args.Method == "update":
		if args.State != "closed" {
			return nil
		}
		closed = true
	default:
		return nil
	}
	var v any
	if json.Unmarshal(response, &v) != nil {
		return nil
	}
	m := resultIssue(v)
	if m == nil {
		return nil
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return nil
	}
	if closed && args.Number != nil && fmtNumber(args.Number) != m[3] {
		return nil // the result names another issue than the one asked to be closed
	}
	return []proto.Issue{{Repo: m[1] + "/" + m[2], Number: n, Closed: closed}}
}

// resultIssue is the address of the issue an MCP tool's result describes, split by issueURL: the
// html_url or url of the shallowest object that has one. A result's text that is JSON is read as
// such. Nil if there is none, or the result is an error.
func resultIssue(v any) []string {
	type node struct {
		v     any
		depth int
	}
	queue := []node{{v: v}}
	enqueued := 1
	enqueue := func(x any, depth int) {
		if enqueued < maxWalkNodes {
			queue = append(queue, node{v: x, depth: depth})
			enqueued++
		}
	}
	for head := 0; head < len(queue); head++ {
		cur := queue[head]
		queue[head] = node{}
		switch t := cur.v.(type) {
		case map[string]any:
			if e, _ := t["isError"].(bool); e {
				return nil
			}
			for _, k := range []string{"html_url", "url"} {
				if s, ok := t[k].(string); ok {
					if m := issueURL.FindStringSubmatch(s); m != nil {
						return m
					}
				}
			}
			for _, k := range sortedKeys(t) {
				enqueue(t[k], cur.depth)
			}
		case []any:
			for _, y := range t {
				enqueue(y, cur.depth)
			}
		case string:
			if cur.depth < maxJSONDepth {
				if s := strings.TrimSpace(t); strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
					var y any
					if json.Unmarshal([]byte(s), &y) == nil {
						enqueue(y, cur.depth+1)
					}
				}
			}
		}
	}
	return nil
}

// walk calls f on every string in v, a decoded JSON value, up to maxWalkNodes total nodes.
func walk(v any, f func(string)) {
	stack := []any{v}
	enqueued := 1
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack[len(stack)-1] = nil
		stack = stack[:len(stack)-1]
		switch t := x.(type) {
		case string:
			f(t)
		case []any:
			n := min(len(t), maxWalkNodes-enqueued)
			for i := n - 1; i >= 0; i-- {
				stack = append(stack, t[i])
			}
			enqueued += n
		case map[string]any:
			keys := sortedKeys(t)
			n := min(len(keys), maxWalkNodes-enqueued)
			for i := n - 1; i >= 0; i-- {
				stack = append(stack, t[keys[i]])
			}
			enqueued += n
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// fmtNumber is a JSON positive integer or numeric string as the decimal digits it holds.
func fmtNumber(v any) string {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 64)
		if err != nil || i <= 0 {
			return ""
		}
		return strconv.FormatInt(i, 10)
	case float64:
		if n <= 0 || n != math.Trunc(n) || n >= 1<<53 {
			return ""
		}
		return strconv.FormatInt(int64(n), 10)
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err != nil || i <= 0 {
			return ""
		}
		return strconv.FormatInt(i, 10)
	}
	return ""
}
