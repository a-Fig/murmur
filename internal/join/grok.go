package join

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// Grok is xAI's Grok Build CLI, `grok`, the one T3 Code's grok driver runs (over ACP). Its user
// config is config.toml in $GROK_HOME, else ~/.grok, and an MCP server there is a table of the shape
// Codex's has: [mcp_servers.<name>] with command and args. Grok passes its own environment on to the
// server, T3CODE_CLI_PATH included, and adds GROK_SESSION_ID, the session's ID. Its tools are
// allowed by a rule in [permission] allow: MCPTool(<server>__<tool>). All of this is from Grok's
// documentation and source (docs.x.ai/build, github.com/xai-org/grok-build), read 2026-11-01; Grok
// wasn't installed on any machine this was tried on, so none of it has been seen run.
//
// Grok also loads the servers of Claude Code's ~/.claude.json by default ([compat.claude] mcps),
// where join registers Murmur too, under the same name. Which of the two it then runs is not known.

var grokServer = tomlServer{file: "config.toml", args: []string{"mcp", "--agent", "grok"}}

// grokAllow is the permission rule that lets Grok run every tool of Murmur's server.
var grokAllow = "MCPTool(" + brand.MCPServer + "__*)"

// registerGrokAt puts Murmur's server in Grok's config.toml in h, and allows its tools. Grok isn't
// touched where neither its command nor the directory is there.
func (e Env) registerGrokAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" {
		return nil, false, nil
	}
	if _, lerr := e.look("grok"); lerr != nil && h.Bin == "" && !isDir(h.Dir) {
		return nil, false, nil
	}
	path := filepath.Join(h.Dir, "config.toml")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	out, changed, err := editTOMLServer(string(b), e.Exe, grokServer)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	out, allowed, err := addTOMLListItem(out, "permission", "allow", grokAllow, "config.toml")
	if err == nil && (changed || allowed) {
		err = checkTOMLEdit(string(b), out)
	}
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	if changed || allowed {
		if err := writeAtomic(path, []byte(out), 0o600); err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
	}
	if changed {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	if allowed {
		did = append(did, h.Label+": allowed "+brand.Product+"'s tools, so they don't ask each time ("+path+", [permission] allow)")
	}
	hookDid, err := e.installGrokHook(h)
	return append(did, hookDid...), changed, err
}

// addTOMLListItem returns text with item in the list key of [table], adding the key, or the table at
// the end, if it isn't there. changed is false, and out is text, when the list has it. A list over
// several lines, or one set with a dotted key or an inline table, is refused: join doesn't edit it.
func addTOMLListItem(text, table, key, item, file string) (out string, changed bool, err error) {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	open := "" // the multi-line string the line starts in
	var current []string
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		inString := open != ""
		if open = tomlLineEnd(line, open); inString {
			continue
		}
		if path, ok := tableHeader(line); ok {
			current = path
			switch {
			case start >= 0 && end == len(lines):
				end = i
			case start < 0 && len(path) == 1 && path[0] == table && !strings.HasPrefix(strings.TrimSpace(line), "[["):
				start = i
			}
			continue
		}
		if k, _, ok := keyValue(line); ok && len(current) == 0 && k[0] == table {
			return "", false, fmt.Errorf("%s sets [%s] in a form join doesn't edit (a dotted key or an inline table): add %q to its %s by hand", file, table, item, key)
		}
	}
	entry := key + " = [" + joinQuoted([]string{item}) + "]"
	if start < 0 {
		var b strings.Builder
		b.WriteString(text)
		if text != "" && !strings.HasSuffix(text, "\n") {
			b.WriteString(nl)
		}
		if strings.TrimSpace(text) != "" {
			b.WriteString(nl)
		}
		b.WriteString("[" + table + "]" + nl + entry + nl)
		return b.String(), true, nil
	}
	block := append([]string(nil), lines[start+1:end]...)
	for i, raw := range block {
		line := strings.TrimRight(raw, "\r")
		path, val, ok := keyValue(line)
		if !ok || len(path) != 1 || path[0] != key {
			continue
		}
		items, after, ok := stringListRest(val)
		if !singleLine(val) || !ok || (after != "" && after[0] != '#') {
			return "", false, fmt.Errorf("%s has %s in [%s] in a form join doesn't edit (several lines, or not a list of strings): add %q to it by hand", file, key, table, item)
		}
		if slices.Contains(items, item) {
			return text, false, nil
		}
		if after != "" {
			after = " " + after
		}
		block[i] = key + " = [" + joinQuoted(append(items, item)) + "]" + after + raw[len(line):]
		rebuilt := append(append(append([]string(nil), lines[:start+1]...), block...), lines[end:]...)
		return strings.Join(rebuilt, "\n"), true, nil
	}
	at := len(block)
	for at > 0 && strings.TrimSpace(block[at-1]) == "" {
		at--
	}
	if nl == "\r\n" {
		entry += "\r"
	}
	block = append(block[:at], append([]string{entry}, block[at:]...)...)
	rebuilt := append(append(append([]string(nil), lines[:start+1]...), block...), lines[end:]...)
	return strings.Join(rebuilt, "\n"), true, nil
}
