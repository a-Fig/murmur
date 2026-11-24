package join

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Cursor's and Kiro's mcp.json hold Murmur's server as mcpServers.murmur, which a user may have
// written first, with fields of their own (env, say). Join sets command and args on it and keeps
// every other field, as Codex's join does with a TOML table (#1199). Uninstall takes the whole entry
// out, as Codex's does with its table: left with only the user's fields it would be a server with no
// command, which Cursor and Kiro may refuse the file over. It says which fields went with it.
//
// An entry that is a remote server (a url, or a type other than stdio) isn't Murmur's to set up: a
// command added to it would leave a mixed entry, and the allow rule for the name would approve that
// server's tools (#1213). Join refuses it before it edits the file or writes any approval, and
// uninstall leaves it.

// errRemoteServer is a `murmur` entry in mcp.json that is a remote server.
var errRemoteServer = errors.New(`"mcpServers.` + brand.MCPServer + `" is a remote server (it has a url, or a type other than stdio), not a local one ` +
	brand.Command + ` can set up, so it was left alone and no allow rule was added for it; rename it or take it out of the file, then run ` +
	"`" + brand.Command + " join` again")

// isRemoteServer says an mcp.json entry is a remote server.
func isRemoteServer(entry map[string]any) bool {
	if _, ok := entry["url"]; ok {
		return true
	}
	t, ok := entry["type"]
	return ok && t != "stdio"
}

// hasLocalServer says text has Murmur's entry, and it isn't a remote server.
func hasLocalServer(text string) bool {
	raw, found, err := jsonedit.GetIn(text, []string{"mcpServers", brand.MCPServer})
	if err != nil || !found {
		return false
	}
	var obj map[string]any
	return json.Unmarshal([]byte(raw), &obj) != nil || obj == nil || !isRemoteServer(obj)
}

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
	if isRemoteServer(obj) {
		return "", false, false, errRemoteServer
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
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return false, false, err
	}
	if err := writeJSONConfig(file, out, 0o600); err != nil {
		return false, false, err
	}
	return true, added, nil
}

// removeMCPServer returns text without Murmur's server, the whole entry, with the names of the
// user's own fields on it (all but command and args) in dropped. changed is false where the entry
// isn't there, and the error is errRemoteServer where it is a remote server, which isn't taken out.
func removeMCPServer(text string) (out string, changed bool, dropped []string, err error) {
	path := []string{"mcpServers", brand.MCPServer}
	raw, found, err := jsonedit.GetIn(text, path)
	if err != nil || !found {
		return text, false, nil, err
	}
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) == nil && obj != nil && isRemoteServer(obj) {
		return text, false, nil, errRemoteServer
	}
	if entry, perr := jsonedit.Parse([]byte(raw)); perr == nil {
		for _, k := range entry.Keys() {
			if k != "command" && k != "args" {
				dropped = append(dropped, k)
			}
		}
	}
	out, changed, err = jsonSteps(text, deleteStep(path...), emptyStep("mcpServers"))
	return out, changed, dropped, err
}

// droppedFieldsNote says the user's fields on Murmur's entry in path went with it.
func droppedFieldsNote(label, path string, dropped []string) string {
	return label + ": the " + brand.MCPServer + " MCP server in " + path + " had fields of yours (" + strings.Join(dropped, ", ") +
		"), which went with it"
}
