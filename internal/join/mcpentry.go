package join

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Cursor's and Kiro's mcp.json hold Murmur's server as mcpServers.murmur, which a user may have
// written first, with fields of their own (env, say). Join sets command and args on it and keeps
// every other field, as Codex's join does with a TOML table (#1199); uninstall takes out the same
// two, and the entry with them if nothing else is left.

// mergeMCPServer returns text with Murmur's server running exe with args. added is true where the
// entry is new. An entry that is there keeps every field but command and args.
func mergeMCPServer(text, exe string, args []string) (out string, changed, added bool, err error) {
	path := []string{"mcpServers", brand.MCPServer}
	raw, found, err := jsonedit.GetIn(text, path)
	if err != nil {
		return "", false, false, err
	}
	if !found {
		entry := jsonedit.New()
		entry.Set("command", exe)
		entry.Set("args", toAny(args))
		out, changed, err = jsonedit.SetIn(text, path, entry)
		return out, changed, true, err
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) != nil || obj == nil {
		return "", false, false, fmt.Errorf(`"mcpServers.%s" isn't an object`, brand.MCPServer)
	}
	out = text
	for _, kv := range []struct {
		key string
		val any
	}{{"command", exe}, {"args", toAny(args)}} {
		var did bool
		if out, did, err = jsonedit.SetIn(out, append(slices.Clone(path), kv.key), kv.val); err != nil {
			return "", false, false, err
		}
		changed = changed || did
	}
	return out, changed, false, nil
}

// setMCPServerFile writes Murmur's server into the file at path, making the file and its directory
// where they aren't there (mergeMCPServer). added reports whether the entry was new.
func setMCPServerFile(file, exe string, args []string) (changed, added bool, err error) {
	text, err := readConfig(file)
	if err != nil {
		return false, false, err
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	out, changed, added, err := mergeMCPServer(text, exe, args)
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", file, err)
	}
	if !changed {
		return false, false, nil
	}
	if err := writeJSONConfig(file, out, 0o600); err != nil {
		return false, false, err
	}
	return true, added, nil
}

// removeMCPServer returns text without join's part of Murmur's server: the whole entry if it holds
// only command and args, else those two, with the names of the user's other fields in kept. An entry
// that isn't an object goes whole. changed is false where the entry isn't there.
func removeMCPServer(text string) (out string, changed bool, kept []string, err error) {
	path := []string{"mcpServers", brand.MCPServer}
	raw, found, err := jsonedit.GetIn(text, path)
	if err != nil || !found {
		return text, false, nil, err
	}
	if entry, perr := jsonedit.Parse([]byte(raw)); perr == nil {
		for _, k := range entry.Keys() {
			if k != "command" && k != "args" {
				kept = append(kept, k)
			}
		}
	}
	if len(kept) == 0 {
		out, changed, err = jsonSteps(text, deleteStep(path...), emptyStep("mcpServers"))
		return out, changed, nil, err
	}
	out, changed, err = jsonSteps(text, deleteStep(append(slices.Clone(path), "command")...), deleteStep(append(slices.Clone(path), "args")...))
	return out, changed, kept, err
}

// keptServerNote says uninstall left the user's fields on Murmur's entry in path.
func keptServerNote(label, path string, kept []string) string {
	return label + ": left " + strings.Join(kept, ", ") + " on the " + brand.MCPServer + " MCP server in " + path +
		", which are yours; delete the entry if you don't need them"
}
