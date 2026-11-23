package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/mcpserver"
)

// runHelp is `mur help [COMMAND|TOOL]`: the whole usage, or one command's entry in it (#832), or
// one MCP tool's, with its full description (#1103). The command's entry is cut from usage itself,
// so the two can't say different things; a name that is both a command and a tool gets both.
func runHelp(args []string, out io.Writer) error {
	switch len(args) {
	case 0:
		fmt.Fprint(out, usage)
		return nil
	case 1:
		entry := commandHelp(args[0])
		tool, err := toolHelp(args[0])
		if err != nil {
			return err
		}
		if entry == "" && tool == "" {
			return fmt.Errorf("no command or tool %q; %s help lists them", args[0], brand.Command)
		}
		if entry != "" && tool != "" {
			entry += "\n"
		}
		fmt.Fprint(out, entry+tool)
		return nil
	}
	return fmt.Errorf("usage: %s help [COMMAND|TOOL]", brand.Command)
}

// commandHelp is name's entries in usage, with the lines that carry each one on, or "".
func commandHelp(name string) string { return entryIn(usage, name) }

// entryIn is name's entries in text, a usage. An entry starts a line with two spaces and the
// command's name; the lines after it that start with more spaces carry it on.
func entryIn(text, name string) string {
	prefix := "  " + brand.Command + " " + name
	var b strings.Builder
	in := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case line == prefix || strings.HasPrefix(line, prefix+" "):
			in = true
		case in && strings.HasPrefix(line, "   "):
		default:
			in = false
		}
		if in {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// withTools puts the MCP tools where text, a usage, has {tools}: one line each, from the server's
// own list, marked when the tool is a command too.
func withTools(text string) string {
	tools := mcpserver.Tools()
	var name, summary int
	for _, t := range tools {
		name, summary = max(name, len(t.Name)), max(summary, len(t.Summary))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "For agents with %s's tools, used instead of those commands (Claude Code: mcp__%s__who and so on).\n",
		brand.Product, brand.MCPServer)
	fmt.Fprintf(&b, "\"also\" marks a tool with a command too. %s help TOOL gives a tool's full description:\n", brand.Command)
	for _, t := range tools {
		line := fmt.Sprintf("  %-*s  %s", name, t.Name, t.Summary)
		if entryIn(text, t.Name) != "" {
			line = fmt.Sprintf("%-*s  also: %s %s", name+2+2+summary, line, brand.Command, t.Name)
		}
		b.WriteString(line + "\n")
	}
	return strings.Replace(text, "{tools}\n", b.String(), 1)
}

// toolHelp is the entry for name if it is an MCP tool: its line in the list and its full
// description, wrapped under it; "" if it is no tool.
func toolHelp(name string) (string, error) {
	for _, t := range mcpserver.Tools() {
		if t.Name != name {
			continue
		}
		desc, err := mcpserver.ToolDescription(name)
		if err != nil {
			return "", err
		}
		b := "  " + t.Name + "  " + t.Summary + "\n"
		row := "   "
		for _, w := range strings.Fields(desc) {
			if len(row)+1+len(w) > 100 {
				b += row + "\n"
				row = "   "
			}
			row += " " + w
		}
		return b + row + "\n", nil
	}
	return "", nil
}

func isTool(name string) bool {
	for _, t := range mcpserver.Tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// toolOnly says, for a name that is one of the MCP tools and has no command, where to read about
// it; otherwise it is "". An agent that tried `mur lookup` was told only that there is no such
// command (#813).
func toolOnly(name string) string {
	if commandHelp(name) != "" || !isTool(name) {
		return ""
	}
	return fmt.Sprintf("%s is one of %s's MCP tools, not a command. %s help %s describes it.",
		name, brand.Product, brand.Command, name)
}
