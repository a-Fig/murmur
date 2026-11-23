package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/mcpserver"
)

// Every command in usage has help of its own, cut from its entry (#832).
func TestEveryCommandHasItsHelp(t *testing.T) {
	entry := regexp.MustCompile(`(?m)^  mur ([a-z]+)`)
	var names []string
	for _, m := range entry.FindAllStringSubmatch(usage, -1) {
		names = append(names, m[1])
	}
	if len(names) < 15 {
		t.Fatalf("found only %d commands in usage: %v", len(names), names)
	}
	for _, name := range names {
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

// Usage names every MCP tool, and says which have no command. A name that is a tool only says so,
// in help and as a command (#813).
func TestUsageNamesTheTools(t *testing.T) {
	// The paragraph about the tools, as one line, however it is wrapped.
	start := strings.Index(usage, "An agent with Murmur's tools")
	if start < 0 {
		t.Fatal("usage has no paragraph about the tools")
	}
	para, _, _ := strings.Cut(usage[start:], "\n\n")
	para = strings.Join(strings.Fields(para), " ")
	_, only, _ := strings.Cut(para, "Only ")
	only, _, _ = strings.Cut(only, " have no command")
	named := func(text, word string) bool { return regexp.MustCompile(`\b` + word + `\b`).MatchString(text) }
	for _, tool := range mcpserver.ToolNames() {
		if !named(para, tool) {
			t.Errorf("usage's paragraph about the tools doesn't name %s: %q", tool, para)
		}
		isCommand := commandHelp(tool) != ""
		if named(only, tool) == isCommand {
			t.Errorf("%s: a command %v, but usage's list of tools with no command, %q, says otherwise", tool, isCommand, only)
		}
		if hint := toolOnly(tool); isCommand != (hint == "") || !isCommand && !strings.HasPrefix(hint, tool+" is one of") {
			t.Errorf("toolOnly(%s) = %q", tool, hint)
		}
	}
	if !strings.Contains(para, "mcp__"+brand.MCPServer+"__who") {
		t.Errorf("usage doesn't give Claude Code's name for a tool: %q", para)
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
	if code == 0 || !strings.Contains(out, "lookup is one of") || strings.Contains(out, "Setup:") {
		t.Errorf("mur lookup: exit %d\n%s", code, out)
	}
}
