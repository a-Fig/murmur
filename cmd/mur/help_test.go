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

// Usage lists every MCP tool with its line, marks the ones that are commands too, and says so of
// nothing else (#1103). A name that is a tool only says so as a command (#813), and its help
// gives the tool's full description.
func TestUsageListsTheTools(t *testing.T) {
	tools := mcpserver.Tools()
	if len(tools) < 7 {
		t.Fatalf("only %d tools: %v", len(tools), tools)
	}
	for _, tool := range tools {
		line := ""
		for _, l := range strings.Split(usage, "\n") {
			if strings.HasPrefix(l, "  "+tool.Name+" ") && strings.Contains(l, tool.Summary) {
				line = l
			}
		}
		if line == "" {
			t.Errorf("usage has no line for tool %s: %s", tool.Name, usage)
			continue
		}
		isCommand := commandHelp(tool.Name) != ""
		if marked := strings.Contains(line, "also: "+brand.Command+" "+tool.Name); marked != isCommand {
			t.Errorf("%s: a command %v, but its line says %q", tool.Name, isCommand, line)
		}
		desc, err := mcpserver.ToolDescription(tool.Name)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runHelp([]string{tool.Name}, &out); err != nil {
			t.Errorf("mur help %s: %v", tool.Name, err)
		}
		if got := strings.Join(strings.Fields(out.String()), " "); !strings.Contains(got, strings.Join(strings.Fields(desc), " ")) {
			t.Errorf("mur help %s doesn't give the description %q:\n%s", tool.Name, desc, out.String())
		}
		if hint := toolOnly(tool.Name); isCommand != (hint == "") || !isCommand && !strings.HasPrefix(hint, tool.Name+" is one of") {
			t.Errorf("toolOnly(%s) = %q", tool.Name, hint)
		}
	}
	if !strings.Contains(usage, "mcp__"+brand.MCPServer+"__who") {
		t.Errorf("usage doesn't give Claude Code's name for a tool:\n%s", usage)
	}
	if strings.Contains(usage, "{") {
		t.Errorf("usage has an unfilled placeholder:\n%s", usage)
	}
	// A tool that is a command too gets both entries, the command's first.
	var out bytes.Buffer
	if err := runHelp([]string{"who"}, &out); err != nil || !strings.HasPrefix(out.String(), "  mur who") || !strings.Contains(out.String(), "\n  who  ") {
		t.Errorf("mur help who: %v\n%s", err, out.String())
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
