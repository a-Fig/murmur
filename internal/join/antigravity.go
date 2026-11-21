package join

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Antigravity is Google's agent (#650). Its home is $GEMINI_HOME, else ~/.gemini, and it reads its
// MCP servers from config/mcp_config.json there ({"mcpServers": {<name>: {command, args}}}) and its
// hooks from config/hooks.json ({<hook name>: {<event>: [handlers]}}), each for every project. All
// of this is from the documentation built into Antigravity's harness (1.4.5, read 2026-11-15).
//
// T3 Code runs it with a home of its own for each provider instance, which it says isolates
// credentials, hooks and MCP servers: <T3's userdata>/providers/antigravity/<sha256 of the
// instance's ID, in hex> (T3's resolveAntigravityInstanceDirectories, seen on T3 0.1.x). Nothing in
// T3's settings moves it, so join registers there for each instance, as well as in the default.
//
// Antigravity gives the commands it runs ANTIGRAVITY_CONVERSATION_ID, the ID T3 keeps for the
// thread's session (seen in a T3 thread's shell, 2026-11-15; not yet seen in an MCP server's), which
// is how Murmur's server finds its thread.

// antigravityArgs are the server's arguments in Antigravity's config.
var antigravityArgs = []string{"mcp", "--agent", "antigravity"}

// antigravityHookName is join's own entry in hooks.json, whose top-level keys name hooks.
var antigravityHookName = brand.MCPServer

// antigravityHomeOf is the home T3 runs Antigravity instance id in, given T3's settings.json, which
// is in the same userdata directory: "" where join doesn't know where T3 keeps its data.
func antigravityHomeOf(t3Settings, id string) string {
	if t3Settings == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(filepath.Dir(t3Settings), "providers", "antigravity", hex.EncodeToString(sum[:]))
}

// registerAntigravityAt puts Murmur's server and hook in Antigravity's config in h. Antigravity isn't
// touched where it isn't there: T3 makes an instance's home when it first runs it, and ~/.gemini
// alone is Gemini CLI's too, so the default home counts only with the agy command or a folder only
// Antigravity makes there.
func (e Env) registerAntigravityAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" || !e.hasAntigravity(h) {
		return nil, false, nil
	}
	path := filepath.Join(h.Dir, "config", "mcp_config.json")
	entry := jsonedit.New()
	entry.Set("command", e.Exe)
	entry.Set("args", toAny(antigravityArgs))
	changed, err := setJSONIn(path, []string{"mcpServers", brand.MCPServer}, entry)
	if err != nil {
		return nil, false, err
	}
	if changed {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	hooks := filepath.Join(h.Dir, "config", "hooks.json")
	hooked, err := setJSONIn(hooks, []string{antigravityHookName}, antigravityHook(e.Exe))
	if err != nil {
		return did, changed, err
	}
	if hooked {
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project ("+hooks+")")
	}
	return did, changed, nil
}

// hasAntigravity says Antigravity is installed for home h.
func (e Env) hasAntigravity(h agentHome) bool {
	if h.Bin != "" || h.T3Own && isDir(h.Dir) {
		return true
	}
	if _, err := e.look("agy"); err == nil {
		return true
	}
	return !h.T3Own && (isDir(filepath.Join(h.Dir, "antigravity")) || isDir(filepath.Join(h.Dir, "antigravity-cli")))
}

// setJSONIn sets path in the JSON file at path to v, making the file, and its directory, if they
// aren't there. changed is false where it was v already.
func setJSONIn(file string, path []string, v any) (changed bool, err error) {
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	text := string(b)
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	out, changed, err := jsonedit.SetIn(text, path, v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", file, err)
	}
	if !changed {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return false, err
	}
	if err := writeAtomic(file, []byte(out), 0o600); err != nil {
		return false, fmt.Errorf("%s: %w", file, err)
	}
	return true, nil
}

// antigravityHook is join's entry in hooks.json. PreInvocation brings notices before each model
// call, and Stop ends the turn or keeps it going with what wasn't read (#202). PreToolUse and
// PostToolUse bracket shell commands for commit credit (#894), so only those two take a matcher.
func antigravityHook(exe string) *jsonedit.Object {
	handler := func(event string) []any {
		h := jsonedit.New()
		h.Set("type", "command")
		h.Set("command", antigravityHookCommand(exe, event))
		h.Set("timeout", codexHookTimeout)
		return []any{h}
	}
	o := jsonedit.New()
	o.Set("PreInvocation", handler("PreInvocation"))
	o.Set("Stop", handler("Stop"))
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		group := jsonedit.New()
		group.Set("matcher", "run_command")
		group.Set("hooks", handler(event))
		o.Set(event, []any{group})
	}
	return o
}

// antigravityHookCommand is the command line of the hook for event. Antigravity runs it with cmd /c
// on Windows and sh -c elsewhere, with the event, which its payload doesn't name, as an argument.
func antigravityHookCommand(exe, event string) string {
	args := " hook --event " + event + " antigravity"
	if runtime.GOOS == "windows" {
		if bareSafe(exe) {
			return exe + args
		}
		if short := shortPath(exe); bareSafe(short) {
			return short + args
		}
		return `"` + exe + `"` + args
	}
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "'" + args
}

// antigravityRemoval is the removal for Antigravity: the server, and join's hook.
func antigravityRemoval(_ Env, h agentHome) string {
	var steps []string
	for _, f := range []struct {
		file string
		path []string
		what string
	}{
		{"mcp_config.json", []string{"mcpServers", brand.MCPServer}, "delete \"" + brand.MCPServer + "\" from mcpServers in "},
		{"hooks.json", []string{antigravityHookName}, "delete \"" + antigravityHookName + "\" from "},
	} {
		path := filepath.Join(h.Dir, "config", f.file)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if _, found, _ := jsonedit.GetIn(string(b), f.path); found {
			steps = append(steps, f.what+path)
		}
	}
	return strings.Join(steps, "; ")
}

// unregisterAntigravityAt takes Murmur's server and hook out of an Antigravity home.
func (e Env) unregisterAntigravityAt(_ context.Context, hs []agentHome) (did, notes []string, err error) {
	h := hs[0]
	for _, f := range []struct {
		file  string
		steps []func(string) (string, bool, error)
		what  string
	}{
		{"mcp_config.json", []func(string) (string, bool, error){deleteStep("mcpServers", brand.MCPServer), emptyStep("mcpServers")}, "the " + brand.MCPServer + " MCP server"},
		{"hooks.json", []func(string) (string, bool, error){deleteStep(antigravityHookName)}, brand.Product + "'s hook"},
	} {
		path := filepath.Join(h.Dir, "config", f.file)
		text, err := readConfig(path)
		if err != nil || text == "" {
			if err != nil {
				return did, notes, err
			}
			continue
		}
		out, changed, err := jsonSteps(text, f.steps...)
		if err != nil {
			return did, notes, fmt.Errorf("%s: %w", path, err)
		}
		if !changed {
			continue
		}
		if err := writeJSONConfig(path, out, 0o600); err != nil {
			return did, notes, err
		}
		did = append(did, h.Label+": removed "+f.what+" ("+path+")")
	}
	return did, notes, nil
}
