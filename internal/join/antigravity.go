package join

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
// T3's settings moves it. Join registers each initialized instance and the standalone install.
const (
	antigravityCLI = "antigravity-cli"
	antigravityACP = "antigravity-acp"
	// Uninstall removes Murmur's allow rule, including one added by hand.
	antigravityAllowRule = "mcp(" + brand.MCPServer + "/*)"
)

var antigravityAllowPath = []string{"permissions", "allow"}

// approvalNote is a tool approval rule join couldn't add. It is a note, not a problem: the server
// and the hook still run, and Murmur's tools ask before each call.
type approvalNote string

func (n approvalNote) Error() string { return string(n) }

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

// isT3AntigravityHome recognizes a managed GEMINI_HOME without T3's settings, by its path:
// userdata/providers/antigravity/<64 hex>, as written or through a link. Its contents say nothing:
// a plain ~/.gemini holds antigravity-acp/ too when Antigravity runs over ACP outside T3.
func isT3AntigravityHome(dir string) bool {
	if dir == "" {
		return false
	}
	if hasAntigravityInstanceLayout(dir) {
		return true
	}
	// On Windows EvalSymlinks can leave a junction as written, but Readlink reads its target.
	if target, err := os.Readlink(dir); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(dir), target)
		}
		dir = target
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	return hasAntigravityInstanceLayout(dir)
}

func hasAntigravityInstanceLayout(dir string) bool {
	decoded, err := hex.DecodeString(filepath.Base(dir))
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	provider := filepath.Dir(dir)
	return samePath(filepath.Base(provider), "antigravity") &&
		samePath(filepath.Base(filepath.Dir(provider)), "providers") &&
		samePath(filepath.Base(filepath.Dir(filepath.Dir(provider))), "userdata")
}

// registerAntigravityAt installs Murmur's server, hook and tool approval in h. Fresh means the
// server was just added. T3 makes its instance's home when it first runs it.
func (e Env) registerAntigravityAt(h agentHome) (did []string, fresh bool, err error) {
	if !e.hasAntigravity(h) {
		return nil, false, nil
	}
	path := filepath.Join(h.Dir, "config", "mcp_config.json")
	entry := jsonedit.New()
	entry.Set("command", e.Exe)
	entry.Set("args", toAny(antigravityArgs))
	changed, fresh, err := setJSONIn(path, []string{"mcpServers", brand.MCPServer}, entry)
	if err != nil {
		return nil, false, err
	}
	if changed {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	hooks := filepath.Join(h.Dir, "config", "hooks.json")
	hooked, _, err := setJSONIn(hooks, []string{antigravityHookName}, antigravityHook(e.Exe))
	if err != nil {
		return did, fresh, err
	}
	if hooked {
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project ("+hooks+")")
	}
	settingsDir := antigravityACP
	if !h.T3Own {
		settingsDir = antigravityCLI
		// Antigravity 1.5.13 (2026-11-20) stores approvals in the CLI or ACP app data.
		// The shared antigravity/ folder alone doesn't establish a standalone CLI install.
		if _, err := e.look("agy"); err != nil && !isDir(filepath.Join(h.Dir, settingsDir)) {
			return did, fresh, nil
		}
	}
	settings := filepath.Join(h.Dir, settingsDir, "settings.json")
	allowed, err := allowAntigravityTools(settings)
	couldnt := fmt.Sprintf("couldn't add %q to permissions.allow in %s", antigravityAllowRule, settings)
	if errors.Is(err, os.ErrPermission) {
		return did, fresh, approvalNote(couldnt + ", so " + brand.Product + "'s tools will ask before each call. Add it by hand, outside Antigravity.")
	}
	if err != nil {
		return did, fresh, approvalNote(couldnt + ": " + err.Error() + ".")
	}
	if allowed {
		did = append(did, h.Label+": allowed "+brand.Product+"'s tools ("+settings+")")
	}
	return did, fresh, err
}

// hasAntigravity says Antigravity is installed for home h.
func (e Env) hasAntigravity(h agentHome) bool {
	if h.Dir == "" || h.SkillsOnly {
		return false
	}
	if h.T3Own {
		return isDir(h.Dir)
	}
	if h.Bin != "" {
		return true
	}
	if _, err := e.look("agy"); err == nil {
		return true
	}
	return isDir(filepath.Join(h.Dir, "antigravity")) || isDir(filepath.Join(h.Dir, antigravityCLI))
}

// setJSONIn sets path in the JSON file at path to v, making the file, and its directory, if they
// aren't there. Added reports whether the key was new.
func setJSONIn(file string, path []string, v any) (changed, added bool, err error) {
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, false, err
	}
	text := string(b)
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	out, changed, err := jsonedit.SetIn(text, path, v)
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", file, err)
	}
	if !changed {
		return false, false, nil
	}
	if _, found, err := jsonedit.GetIn(text, path); err != nil {
		return false, false, fmt.Errorf("%s: %w", file, err)
	} else {
		added = !found
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return false, false, err
	}
	if err := writeAtomic(file, []byte(out), 0o600); err != nil {
		return false, false, fmt.Errorf("%s: %w", file, err)
	}
	return true, added, nil
}

// allowAntigravityTools adds Murmur's rule to the settings at path. Its own errors don't name the
// file, which the note they go in names already; an error from the file system may still.
func allowAntigravityTools(path string) (bool, error) {
	b, err := jsonedit.ReadText(resolveLinks(path))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	text := string(b)
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	raw, found, err := jsonedit.GetIn(text, antigravityAllowPath)
	if err != nil {
		return false, err
	}
	if found {
		var rules []any
		if err := json.Unmarshal([]byte(raw), &rules); err != nil || rules == nil {
			return false, errors.New("permissions.allow isn't a list")
		}
		if slices.Contains(rules, any(antigravityAllowRule)) {
			return false, nil
		}
	}
	out, changed, err := jsonedit.AppendIn(text, antigravityAllowPath, antigravityAllowRule)
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if err := parses(out); err != nil {
		return false, err
	}
	return true, writeAtomic(path, []byte(out), 0o600)
}

// antigravityHook is join's entry in hooks.json, running exe's hook for two events, the fewest that
// do the work. PreInvocation, before each call of the model, is a turn's start and the moment after
// each tool call: it brings the notices, which Antigravity takes back from it as a step to add, where
// PostToolUse's answer is ignored. Stop ends the turn, and keeps it going with what wasn't read
// (#202). Neither takes a matcher.
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

// antigravityRemoval tells how to remove the server, hook and tool approval by hand.
func antigravityRemoval(_ Env, h agentHome) string {
	if h.SkillsOnly {
		return ""
	}
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
	for _, dir := range []string{antigravityCLI, antigravityACP} {
		path := filepath.Join(h.Dir, dir, "settings.json")
		if text, err := readConfig(path); err == nil && text != "" {
			if _, found, _ := jsonedit.RemoveIn(text, antigravityAllowPath, antigravityAllowRule); found {
				steps = append(steps, "delete \""+antigravityAllowRule+"\" from permissions.allow in "+path)
			}
		}
	}
	return strings.Join(steps, "; ")
}

// unregisterAntigravityAt takes Murmur's server, hook and tool approval out of an Antigravity home.
func (e Env) unregisterAntigravityAt(_ context.Context, hs []agentHome) (did, notes []string, err error) {
	h := hs[0]
	if h.SkillsOnly {
		return nil, nil, nil
	}
	var problems []error
	for _, f := range []struct {
		file  string
		steps []func(string) (string, bool, error)
		what  string
	}{
		{filepath.Join("config", "mcp_config.json"), []func(string) (string, bool, error){deleteStep("mcpServers", brand.MCPServer), emptyStep("mcpServers")}, "the " + brand.MCPServer + " MCP server"},
		{filepath.Join("config", "hooks.json"), []func(string) (string, bool, error){deleteStep(antigravityHookName)}, brand.Product + "'s hook"},
		{filepath.Join(antigravityCLI, "settings.json"), []func(string) (string, bool, error){removeAntigravityAllow}, "the rule that allowed " + brand.Product + "'s tools"},
		{filepath.Join(antigravityACP, "settings.json"), []func(string) (string, bool, error){removeAntigravityAllow}, "the rule that allowed " + brand.Product + "'s tools"},
	} {
		path := filepath.Join(h.Dir, f.file)
		text, err := readConfig(path)
		if err != nil || text == "" {
			if err != nil {
				problems = append(problems, err)
			}
			continue
		}
		out, changed, err := jsonSteps(text, f.steps...)
		if err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if !changed {
			continue
		}
		if err := writeJSONConfig(path, out, 0o600); err != nil {
			problems = append(problems, err)
			continue
		}
		did = append(did, h.Label+": removed "+f.what+" ("+path+")")
	}
	return did, notes, errors.Join(problems...)
}

func removeAntigravityAllow(text string) (string, bool, error) {
	out, removed, err := jsonedit.RemoveIn(text, antigravityAllowPath, antigravityAllowRule)
	if err != nil || !removed {
		return out, removed, err
	}
	out, _, err = jsonSteps(out, emptyStep(antigravityAllowPath...), emptyStep("permissions"))
	return out, true, err
}
