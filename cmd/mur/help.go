package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/mcpserver"
)

// runHelp is `mur help [COMMAND]`: the whole usage, or one command's entry in it (#832). The entry
// is cut from usage itself, so the two can't say different things. A name that is one of the MCP
// tools and no command says which tool to call.
func runHelp(args []string, out io.Writer) error {
	switch len(args) {
	case 0:
		fmt.Fprint(out, usage)
		return nil
	case 1:
		if entry := commandHelp(args[0]); entry != "" {
			fmt.Fprint(out, entry)
			return nil
		}
		if tool := toolOnly(args[0]); tool != "" {
			fmt.Fprintln(out, tool)
			return nil
		}
		return fmt.Errorf("no command %q; %s help lists them", args[0], brand.Command)
	}
	return fmt.Errorf("usage: %s help [COMMAND]", brand.Command)
}

// commandHelp is name's entries in usage, with the lines that carry each one on, or "". An entry
// starts a line with two spaces and the command's name; the lines after it that start with more
// spaces carry it on.
func commandHelp(name string) string {
	prefix := "  " + brand.Command + " " + name
	var b strings.Builder
	in := false
	for _, line := range strings.Split(usage, "\n") {
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

// toolOnly says, for a name that is one of the MCP tools and has no command, which tool to call;
// otherwise it is "". An agent that tried `mur lookup` was told only that there is no such command
// (#813).
func toolOnly(name string) string {
	if commandHelp(name) != "" || !slices.Contains(mcpserver.ToolNames(), name) {
		return ""
	}
	return fmt.Sprintf("%s is one of %s's MCP tools, not a command. %s help lists the commands.",
		name, brand.Product, brand.Command)
}
