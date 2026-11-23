package join

import (
	"context"
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

// Cursor (#918), its command line `agent` (also `cursor-agent`) and its editor. Read in the current
// CLI, 2026.11.22-e9042b2 (its package unpacked into a scratch folder, and run there in a scratch
// home), in @cursor/sdk 1.3.1, which T3 Code runs Cursor's threads with, and in cursor.com/docs
// (2026-11-22). No Cursor account was at hand, so no turn has run; what needs one is marked.
//
//   - MCP servers are mcpServers.<name> in ~/.cursor/mcp.json, for every project, and
//     .cursor/mcp.json in a project; the CLI and the editor read the same files, and T3 Code's
//     Cursor reads the user's too. A server is command, args and env. JSONC is read. Only a
//     project's file waits for approval (`agent mcp enable`, --approve-mcps); the user's loads as
//     it is: `agent mcp list` showed Murmur's ready, and `agent mcp list-tools` its tools, in a
//     scratch home.
//   - Cursor starts a server with only a few variables (PATH, TEMP and the home's: APPDATA,
//     LOCALAPPDATA and USERPROFILE on Windows, HOME and USER elsewhere) and no ID for the chat, which
//     it gives hooks and shell commands instead: the hook names it to the server (internal/hook's
//     cursor.go).
//   - Tool calls ask first unless a rule allows them. The rule for every tool of a server is
//     Mcp(<server>:*), in permissions.allow of cli-config.json, in CURSOR_CONFIG_DIR, else
//     $XDG_CONFIG_HOME/cursor, else ~/.cursor. Cursor rewrites that file itself, and it reads it
//     as strict JSON: a comment there made it throw the file away and write its defaults, the
//     user's settings gone. A file with only permissions in it is kept, and filled in. In T3 Code
//     Cursor runs with T3's approval policy instead, and the rule isn't read (not tried).
//   - Hooks are hooks.<event> in ~/.cursor/hooks.json, each a list of {command, timeout}, beside
//     "version": 1. On Windows Cursor runs a command through PowerShell, and elsewhere through a
//     shell, so the command line is Codex's (agentHookCommand). The events are in cursor.go in
//     internal/hook.
//   - Skills are read from ~/.cursor/skills, and from ~/.claude/skills, ~/.codex/skills and
//     ~/.agents/skills while "Include Third-Party Plugins, Skills, and Other Configs" is on (the
//     default). AGENTS.md, which carries the repo's Murmur section, is read too.

var cursorArgs = []string{"mcp", "--agent", "cursor"}

// cursorAllowRule lets Cursor call every tool of Murmur's server without asking.
var cursorAllowRule = "Mcp(" + brand.MCPServer + ":*)"

// cursorHookEvents are the events join puts the hook on: a new chat's start and each prompt (the
// session is at work, and the prompt marks a message handed over as the turn read, #611), each
// tool call (the notices), and a turn's end (#202).
var cursorHookEvents = []string{"sessionStart", "beforeSubmitPrompt", "postToolUse", "stop"}

// cursorHookTimeout is the hook's timeout in seconds. The hook bounds itself at a few seconds.
const cursorHookTimeout = 30

// cursorConfigDir is the folder of Cursor's cli-config.json, by the variables the CLI reads.
func cursorConfigDir(getenv func(string) string, home string) string {
	if dir := getenv("CURSOR_CONFIG_DIR"); strings.TrimSpace(dir) != "" {
		return dir
	}
	if xdg := getenv("XDG_CONFIG_HOME"); strings.TrimSpace(xdg) != "" {
		return filepath.Join(xdg, "cursor")
	}
	return filepath.Join(home, ".cursor")
}

// cursorConfigFile is the cli-config.json join writes the allow rule in, for home h: h.JSON, which
// for Cursor is that file.
func cursorConfigFile(h agentHome) string { return h.JSON }

// hasCursor says Cursor is on this machine: its folder, or its command line on PATH.
func (e Env) hasCursor(h agentHome) bool {
	if isDir(h.Dir) {
		return true
	}
	for _, name := range []string{"cursor-agent", "cursor"} {
		if _, err := e.look(name); err == nil {
			return true
		}
	}
	return false
}

// registerCursorAt puts Murmur's server in mcp.json, its hook in hooks.json and its allow rule in
// cli-config.json, for Cursor home h. A part join can't write is a problem after the others are
// written, so the server still goes in.
func (e Env) registerCursorAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" || !e.hasCursor(h) {
		return nil, false, nil
	}
	var errs []error
	path := filepath.Join(h.Dir, "mcp.json")
	if changed, err := e.setCursorServer(path); err != nil {
		errs = append(errs, err)
	} else if changed {
		fresh = true
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	path = filepath.Join(h.Dir, "hooks.json")
	if changed, err := setCursorHooks(path, agentHookCommand(e.Exe, "cursor")); err != nil {
		errs = append(errs, fmt.Errorf("%w, so %s's hook wasn't added", err, brand.Product))
	} else if changed {
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project ("+path+")")
	}
	if path = cursorConfigFile(h); path != "" {
		if changed, err := allowCursorTools(path); err != nil {
			errs = append(errs, fmt.Errorf("%w, so the allow rule wasn't added, and each of %s's tools asks before it runs", err, brand.Product))
		} else if changed {
			did = append(did, h.Label+": allowed "+brand.Product+"'s tools, so they don't ask each time ("+path+")")
		}
	}
	return did, fresh, errors.Join(errs...)
}

// setCursorServer writes Murmur's entry in the mcp.json at path, and says whether it changed.
func (e Env) setCursorServer(path string) (bool, error) {
	text, err := readConfig(path)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	entry := jsonedit.New()
	entry.Set("command", e.Exe)
	entry.Set("args", toAny(cursorArgs))
	out, changed, err := jsonedit.SetIn(text, []string{"mcpServers", brand.MCPServer}, entry)
	if err != nil {
		return false, fmt.Errorf("%s: %w, so the server wasn't added", path, err)
	}
	if !changed {
		return false, nil
	}
	return true, writeJSONConfig(path, out, 0o644)
}

// allowCursorTools adds the allow rule to the cli-config.json at path, and says whether it was
// new. A file Cursor would refuse (a comment, a trailing comma) is left alone: written back with
// the rule in it, it would still be refused, and Cursor would replace it with its defaults.
func allowCursorTools(path string) (bool, error) {
	text, err := readConfig(path)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	if !json.Valid([]byte(text)) {
		return false, fmt.Errorf("%s isn't plain JSON, which is all Cursor reads there", path)
	}
	raw, found, err := jsonedit.GetIn(text, []string{"permissions", "allow"})
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	if found {
		var allow []any
		if json.Unmarshal([]byte(raw), &allow) != nil {
			return false, fmt.Errorf("%s: \"permissions.allow\" isn't a list", path)
		}
		if slices.Contains(allow, any(cursorAllowRule)) {
			return false, nil
		}
	}
	out, _, err := jsonedit.AppendIn(text, []string{"permissions", "allow"}, cursorAllowRule)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, writeJSONConfig(path, out, 0o644)
}

// isCursorHook says a hooks.json entry is join's, by its command (`mur hook cursor`).
func isCursorHook(v any) bool {
	o, _ := v.(*jsonedit.Object)
	c, _ := o.Get("command")
	cmd, _ := c.(string)
	return strings.HasSuffix(strings.TrimSpace(cmd), " hook cursor") && isAgentHookCommand(cmd)
}

// setCursorHooks puts join's hook on each of cursorHookEvents in the hooks.json at path, running
// command, and says whether that changed the file. An entry of join's with another command (the
// binary moved) is replaced in its place, and the user's own hooks stay as they are.
func setCursorHooks(path, command string) (bool, error) {
	text, err := readConfig(path)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	changed := false
	if _, ok := doc.Get("version"); !ok {
		doc.Set("version", 1)
		changed = true
	}
	hooks := doc.Object("hooks")
	if hooks == nil {
		if _, taken := doc.Get("hooks"); taken {
			return false, fmt.Errorf(`%s: "hooks" isn't an object`, path)
		}
		hooks = jsonedit.New()
		doc.Set("hooks", hooks)
	}
	for _, ev := range cursorHookEvents {
		var list []any
		if v, ok := hooks.Get(ev); ok {
			if list, ok = v.([]any); !ok {
				return false, fmt.Errorf("%s: hooks.%s isn't a list", path, ev)
			}
		}
		entry := jsonedit.New()
		entry.Set("command", command)
		entry.Set("timeout", cursorHookTimeout)
		kept, placed := []any{}, false
		for _, v := range list {
			switch {
			case !isCursorHook(v):
				kept = append(kept, v)
			case placed:
				changed = true // a second entry of join's
			case sameCursorHook(v, command):
				kept, placed = append(kept, v), true
			default:
				kept, placed, changed = append(kept, entry), true, true
			}
		}
		if !placed {
			kept, changed = append(kept, entry), true
		}
		hooks.Set(ev, kept)
	}
	if !changed {
		return false, nil
	}
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, writeJSONConfig(path, string(b), 0o644)
}

// sameCursorHook says an entry of join's runs command, with join's timeout.
func sameCursorHook(v any, command string) bool {
	o, _ := v.(*jsonedit.Object)
	c, _ := o.Get("command")
	t, _ := o.Get("timeout")
	n, ok := t.(json.Number)
	return c == command && ok && n.String() == fmt.Sprint(cursorHookTimeout)
}

// removeCursorHooks returns hooks.json's text without join's entries; an event left with none goes
// too. changed is false where there were none. "hooks" stays, even empty: Cursor refuses a
// hooks.json without it ("missing 'hooks' property").
func removeCursorHooks(text string) (out string, changed bool, err error) {
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return "", false, err
	}
	hooks := doc.Object("hooks")
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		list, ok := v.([]any)
		if !ok {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(list), isCursorHook)
		switch {
		case len(kept) == len(list):
			continue
		case len(kept) == 0:
			hooks.Delete(ev)
		default:
			hooks.Set(ev, kept)
		}
		changed = true
	}
	if !changed {
		return text, false, nil
	}
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// unregisterCursorAt takes out of Cursor home hs[0] what join put there: the server, the hook and
// the allow rule.
func (e Env) unregisterCursorAt(_ context.Context, hs []agentHome) (did, notes []string, err error) {
	h := hs[0]
	var errs []error
	path := filepath.Join(h.Dir, "mcp.json")
	if text, rerr := readConfig(path); rerr != nil {
		errs = append(errs, rerr)
	} else if text != "" {
		out, changed, err := jsonSteps(text, deleteStep("mcpServers", brand.MCPServer), emptyStep("mcpServers"))
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		case changed:
			if err := writeJSONConfig(path, out, 0o644); err != nil {
				errs = append(errs, err)
			} else {
				did = append(did, h.Label+": removed the "+brand.MCPServer+" MCP server ("+path+")")
			}
		}
	}
	path = filepath.Join(h.Dir, "hooks.json")
	if text, rerr := readConfig(path); rerr != nil {
		errs = append(errs, rerr)
	} else if text != "" {
		out, changed, err := removeCursorHooks(text)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		case changed:
			if err := writeJSONConfig(path, out, 0o644); err != nil {
				errs = append(errs, err)
			} else {
				did = append(did, h.Label+": removed "+brand.Product+"'s hook ("+path+")")
			}
		}
	}
	if path = cursorConfigFile(h); path != "" {
		if text, rerr := readConfig(path); rerr != nil {
			errs = append(errs, rerr)
		} else if text != "" {
			out, changed, err := jsonedit.RemoveIn(text, []string{"permissions", "allow"}, cursorAllowRule)
			switch {
			case err != nil:
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
			case changed:
				if err := writeJSONConfig(path, out, 0o644); err != nil {
					errs = append(errs, err)
				} else {
					did = append(did, h.Label+": removed the rule that allowed "+brand.Product+"'s tools ("+path+")")
				}
			}
		}
	}
	return did, nil, errors.Join(errs...)
}

// cursorRemoval says how to take out by hand what join put in Cursor home h.
func cursorRemoval(_ Env, h agentHome) string {
	var steps []string
	path := filepath.Join(h.Dir, "mcp.json")
	if b, err := os.ReadFile(path); err == nil {
		if _, found, _ := jsonedit.GetIn(string(b), []string{"mcpServers", brand.MCPServer}); found {
			steps = append(steps, "delete \""+brand.MCPServer+"\" from mcpServers in "+path)
		}
	}
	path = filepath.Join(h.Dir, "hooks.json")
	if b, err := os.ReadFile(path); err == nil {
		if _, hooked, _ := removeCursorHooks(string(b)); hooked {
			steps = append(steps, "delete the hooks that run `"+brand.Command+" hook cursor` from "+path)
		}
	}
	if path = cursorConfigFile(h); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			if _, allowed, _ := jsonedit.RemoveIn(string(b), []string{"permissions", "allow"}, cursorAllowRule); allowed {
				steps = append(steps, "delete \""+cursorAllowRule+"\" from permissions.allow in "+path)
			}
		}
	}
	return strings.Join(steps, "; ")
}
