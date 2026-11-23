package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/mcpserver"
)

// Every command in usage has help of its own, cut from its entry (#832).
func TestEveryCommandHasItsHelp(t *testing.T) {
	for _, name := range []string{"init", "join", "uninstall", "ui", "app", "nickname", "stop", "rotate",
		"who", "send", "inbox", "status", "mcp", "hook", "daemon"} {
		var out bytes.Buffer
		if err := runHelp([]string{name}, &out); err != nil || !strings.HasPrefix(out.String(), "  mur "+name) {
			t.Errorf("mur help %s: %v\n%s", name, err, out.String())
		}
	}
	// An entry is the command's lines only: send's ends before inbox's starts.
	if send := commandHelp("send"); !strings.Contains(send, "TEXT is then the body") || strings.Contains(send, "mur inbox") {
		t.Errorf("send's help:\n%s", send)
	}
	if hook := commandHelp("hook"); strings.Count(hook, "  mur hook") != 2 || strings.Contains(hook, "mur daemon") {
		t.Errorf("hook's help has both its entries and nothing else:\n%s", hook)
	}
	if err := runHelp([]string{"nope"}, &bytes.Buffer{}); err == nil {
		t.Error("help for no command worked")
	}
}

// Usage names every MCP tool, and says which are tools only: those with no command. A name that is
// a tool only says which tool to call, in help and as a command (#813).
func TestUsageNamesTheTools(t *testing.T) {
	var tools, only string
	for _, line := range strings.Split(usage, "\n") {
		if strings.Contains(line, "uses them instead:") {
			tools = line
		}
		if i := strings.Index(line, " are tools only"); i >= 0 {
			only = line[strings.LastIndex(line[:i], ". ")+1 : i] // the sentence that says so
		}
	}
	named := func(text, word string) bool { return regexp.MustCompile(`\b` + word + `\b`).MatchString(text) }
	for _, tool := range mcpserver.ToolNames() {
		if !named(tools, tool) {
			t.Errorf("usage's line of tools doesn't name %s: %q", tool, tools)
		}
		isCommand := commandHelp(tool) != ""
		if named(only, tool) == isCommand {
			t.Errorf("%s: a command %v, but usage's tools-only list %q says otherwise", tool, isCommand, only)
		}
		if hint := toolOnly(tool); isCommand != (hint == "") || !isCommand && !strings.Contains(hint, "mcp__murmur__"+tool) {
			t.Errorf("toolOnly(%s) = %q", tool, hint)
		}
	}
	if toolOnly("nope") != "" {
		t.Error("a name that is neither is called a tool")
	}
}

// The real mur: help for one command, and a tool's name as a command.
func TestHelpForOneCommand(t *testing.T) {
	b := newBox(t)
	out := b.mustRun(b.root, "help", "stop")
	if !strings.HasPrefix(out, "  mur stop") || strings.Contains(out, "mur init") {
		t.Errorf("mur help stop:\n%s", out)
	}
	out, code := b.run(b.root, "lookup")
	if code == 0 || !strings.Contains(out, "mcp__murmur__lookup") || strings.Contains(out, "Setup:") {
		t.Errorf("mur lookup: exit %d\n%s", code, out)
	}
}
