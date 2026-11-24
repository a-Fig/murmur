package join

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// Hermes Agent (#975, #1183; Nous Research's hermes-agent at 8da3100, read 2026-11-23, and run from
// source on Windows with a stand-in model) runs every session, from its classic CLI, TUI, Desktop or
// ACP, in one gateway process per profile, and that process runs the profile's MCP servers: one
// `mur mcp` would serve every Hermes session in every repo, and no MCP call says which session made
// it. Its plugins run in the gateway too, but a plugin's tool handler is given its session's ID and
// can read the session's working directory. So join writes a plugin of Murmur's own, which registers
// Murmur's seven tools with Hermes, and no MCP entry: each call runs `mur tool` with the session's ID
// and folder (internal/mcpserver, tool.go), which answers as the MCP tool does.
//
//   - Where: plugins/murmur/ (plugin.yaml and __init__.py) in Hermes' home, $HERMES_HOME, else
//     %LOCALAPPDATA%\hermes on Windows and ~/.hermes elsewhere (hermes_constants.py), and in each of
//     its profiles, profiles/<name>/, each a home of its own with its own config.yaml and plugins
//     (user-guide/profiles.md). A folder counts as a profile only with one of the files Hermes
//     names (hermesProfileFiles).
//   - On: a plugin there loads only once its name is in plugins.enabled in config.yaml
//     (user-guide/features/plugins.md, "Plugins are opt-in"), so join adds it there, editing the
//     file as text (hermesyaml.go). A plugin the user put in plugins.disabled stays off: join says
//     so and leaves it.
//   - When: the gateway discovers plugins once, as it starts, for every session it runs
//     ("Project-local plugins"), so sessions get the tools after `hermes gateway restart`.
//   - The tools are named murmur_who and so on: who and send alone could meet a tool of Hermes' own
//     or another plugin's, and Hermes refuses a second tool of one name.
//   - They are there in every session, in any folder, and a call outside a joined clone answers that
//     Murmur is off there, as the MCP server's passive tools do. They can't be left out where the
//     session's folder isn't joined, as the MCP server leaves them out: Hermes keeps one list of
//     tools for the whole process (get_tool_definitions in model_tools.py), checked about every 30
//     seconds, so one session's folder would decide every session's tools (seen 2026-11-23). By
//     default Hermes shows a plugin's tools only as a line each, in its tool_search tool, and loads
//     a tool's schema when the model asks (user-guide/features/tool-search.md), so they cost little.
//   - A subagent (delegate_task) acts as the session that started it, which the plugin learns from
//     Hermes' subagent_start hook: its calls go as its parent's, as other agents' subagents do
//     (docs/design/delegates.md), so it never shows as a session of its own.
//   - Skills go in skills/ of each home, which Hermes scans for SKILL.md files.
//
// The plugin brings a session its messages as Claude Code's hook does (#1184), running `mur hook
// --agent hermes` (internal/mcpserver, HermesHook) from Hermes' plugin hooks, in a clone the user
// joined only, by the session's folder (the same few stats as Pi's extension):
//   - pre_llm_call, once a turn before the model's first request, is UserPromptSubmit: its notice
//     goes into the turn's user message as context.
//   - transform_tool_result, after each call and before the result reaches the model, is
//     PostToolUse: the notice goes after the result. That is the one way into a running turn a
//     plugin has (agent.steer, which Hermes has for its own /steer, isn't one), and the place
//     OpenCode's model refused text in a file it had read (docs/design/clients.md); here it is after
//     the tool's own output, under Murmur's [Murmur] line. pre_tool_call and it bracket a terminal
//     call, so the daemon credits the session's commits.
//   - on_session_end, at each turn's end, is Stop, which keeps nothing going: putting what is
//     unread in with ctx.inject_message could lose it, as Hermes may refuse it once the daemon has
//     marked it read. It stays in the inbox, and the daemon hands it over through the socket once
//     the session is idle, where a refusal loses nothing.
//   - A socket the plugin opens for the session, a unix socket or on Windows a named pipe (Python
//     has no unix sockets there), takes Claude Code's messaging-socket lines and puts each message
//     in with inject_message: a turn of its own in an idle session. inject_message works for a
//     session a client has open, and from no hook's context only with the session's key, which the
//     plugin keeps from the hooks (seen 2026-11-23: a oneshot session's key was refused once it had
//     closed; an ACP session's still took a turn after its client went). The plugin closes the socket
//     at on_session_finalize and on_session_delete, or when Hermes refuses a turn, and the daemon
//     holds the session for as long as the socket answers and it hasn't been idle for 8 hours
//     (internal/daemon, socketHeld).
//   - inject_message needs plugins.entries.murmur.allow_gateway_injection in config.yaml, which
//     join sets and uninstall takes out (hermesyaml.go). Without it the plugin opens no socket, and
//     the session gets notices only.
//
// Hermes runs plugins in-process with the user's credentials, as it does every plugin; Murmur's runs
// `mur` and nothing else, and the grant lets it start a turn in a session of the user's own.

// hermesProfileFiles are the files that make a folder under profiles/ a profile to Hermes: one is
// enough (user-guide/profiles.md).
var hermesProfileFiles = []string{"config.yaml", ".env", "SOUL.md", "profile.yaml", "auth.json", "state.db"}

// hermesHomeDir is Hermes' home as Hermes finds it: HERMES_HOME, else the platform's default, each
// default with HERMES_DATA_DIR_SUFFIX.
func hermesHomeDir(getenv func(string) string, home string) string {
	return hermesHomeFor(getenv, home, runtime.GOOS == "windows")
}

func hermesHomeFor(getenv func(string) string, home string, windows bool) string {
	if dir := strings.TrimSpace(getenv("HERMES_HOME")); dir != "" {
		return expandHome(dir, home)
	}
	suffix := getenv("HERMES_DATA_DIR_SUFFIX")
	if windows {
		base := strings.TrimSpace(getenv("LOCALAPPDATA"))
		if base == "" {
			if home == "" {
				return ""
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "hermes"+suffix)
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".hermes"+suffix)
}

// isHermesHome says dir has one of the files that make a folder a Hermes home or profile.
func isHermesHome(dir string) bool {
	return slices.ContainsFunc(hermesProfileFiles, func(f string) bool { return exists(filepath.Join(dir, f)) })
}

// hermesProfiles are the homes of the profiles of Hermes home dir, each labeled. A dir that is a
// profile itself (HERMES_HOME set to one) has its root's profiles, and the root, listed, where the
// root is a Hermes home too: a folder named profiles somewhere else isn't Hermes'.
func hermesProfiles(dir string) []agentHome {
	if dir == "" {
		return nil
	}
	root := dir
	if up := filepath.Dir(filepath.Dir(dir)); filepath.Base(filepath.Dir(dir)) == "profiles" && isHermesHome(up) {
		root = up
	}
	var out []agentHome
	if !samePath(root, dir) && isDir(root) {
		out = append(out, agentHome{Dir: root, Label: "Hermes"})
	}
	entries, _ := os.ReadDir(filepath.Join(root, "profiles"))
	for _, en := range entries {
		p := filepath.Join(root, "profiles", en.Name())
		if !en.IsDir() || strings.HasPrefix(en.Name(), ".") || samePath(p, dir) || !isHermesHome(p) { // Hermes passes over dot folders too
			continue
		}
		out = append(out, agentHome{Dir: p, Label: "Hermes (profile " + en.Name() + ")"})
	}
	return out
}

// hermesPluginDir is Murmur's plugin folder in Hermes home dir.
func hermesPluginDir(dir string) string { return filepath.Join(dir, "plugins", brand.MCPServer) }

// hermesMarker starts both of the plugin's files.
const hermesMarker = "# Written by `" + brand.Command + " join`"

// hermesPluginFiles are the plugin's files for the mur at exe, by name.
func hermesPluginFiles(exe string) map[string]string {
	cmd, _ := json.Marshal(exe)
	r := strings.NewReplacer("{{product}}", brand.Product, "{{command}}", brand.Command, "{{server}}", brand.MCPServer,
		"{{exe}}", string(cmd), "{{state}}", brand.StateDir, "{{consent}}", consentFile,
		// internal/mcpserver reads these (tool.go).
		"{{sessionEnv}}", "MURMUR_HERMES_SESSION", "{{socketEnv}}", "MURMUR_HERMES_SOCKET", "{{tokenEnv}}", "MURMUR_HERMES_TOKEN")
	return map[string]string{"plugin.yaml": r.Replace(hermesManifest), "__init__.py": r.Replace(hermesPlugin)}
}

const hermesManifest = `# Written by ` + "`{{command}} join`" + ` ({{product}}, https://github.com/a-Fig/murmur), with __init__.py beside it.
name: {{server}}
version: "1"
description: "{{product}}'s tools, to message the coding agents on your teammates' machines in this repo"
`

// hermesPlugin is the plugin, with its names left for package brand to fill in. It is a file of its
// own so it reads, and is tested, as the Python it is; .gitattributes keeps it LF on every clone.
//
//go:embed hermesplugin.py
var hermesPlugin string

// hasHermes says Hermes is set up in home h: its folder is there. A hermes on PATH whose home
// isn't there yet hasn't been run, and its first run makes the home.
func (e Env) hasHermes(h agentHome) bool { return h.Dir != "" && isDir(h.Dir) }

// writeHermesPlugin writes the plugin's files into dir, and says whether it changed either. A file
// there that join didn't write is left alone, and is a problem.
func writeHermesPlugin(dir, exe string) (bool, error) {
	files := hermesPluginFiles(exe)
	names := []string{"plugin.yaml", "__init__.py"}
	for _, n := range names {
		path := filepath.Join(dir, n)
		b, err := os.ReadFile(path)
		switch {
		case err == nil && !bytes.HasPrefix(b, []byte(hermesMarker)):
			return false, fmt.Errorf("%s is there and isn't the one join writes, so the plugin was left alone", path)
		case err != nil && !errors.Is(err, os.ErrNotExist):
			return false, err
		}
	}
	changed := false
	for _, n := range names {
		path := filepath.Join(dir, n)
		if b, err := os.ReadFile(path); err == nil && strings.ReplaceAll(string(b), "\r\n", "\n") == files[n] {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return changed, err
		}
		if err := writeAtomic(path, []byte(files[n]), 0o644); err != nil {
			return changed, fmt.Errorf("%s: %w", path, err)
		}
		changed = true
	}
	return changed, nil
}

// registerHermesAt writes Murmur's plugin into Hermes home h and turns it on in its config.yaml.
func (e Env) registerHermesAt(h agentHome) (did []string, fresh bool, err error) {
	if !e.hasHermes(h) {
		return nil, false, nil
	}
	dir := hermesPluginDir(h.Dir)
	wrote, err := writeHermesPlugin(dir, e.Exe)
	if err != nil {
		return nil, false, err
	}
	if wrote {
		did = append(did, h.Label+": wrote "+brand.Product+"'s plugin, which gives every session its tools ("+dir+")")
	}
	cfg := filepath.Join(h.Dir, "config.yaml")
	text, err := readConfig(cfg)
	if err != nil {
		return did, wrote, err
	}
	off, err := yamlListHas(text, "disabled", brand.MCPServer)
	if err != nil {
		return did, wrote, fmt.Errorf("%s: %w, so %s wasn't added to plugins.enabled there: add it, or run `hermes plugins enable %s`", cfg, err, brand.MCPServer, brand.MCPServer)
	}
	if off {
		return did, wrote, fmt.Errorf("%s has %s in plugins.disabled, which keeps it off: take it out of there to have its tools", cfg, brand.MCPServer)
	}
	out, changed, err := addYAMLPlugin(text, brand.MCPServer)
	if err != nil {
		return did, wrote, fmt.Errorf("%s: %w, so %s wasn't added to plugins.enabled there: add it, or run `hermes plugins enable %s`", cfg, err, brand.MCPServer, brand.MCPServer)
	}
	// The plugin hands a session a message as a turn through Hermes' inject_message, which a plugin
	// may use only with this grant (#1184). Without it the session still gets notices.
	granted, grantChanged, grantErr := addYAMLGrant(out, brand.MCPServer)
	if grantErr == nil {
		out = granted
	}
	if changed || grantChanged {
		if err := writeAtomic(cfg, []byte(out), 0o600); err != nil {
			return did, wrote, fmt.Errorf("%s: %w", cfg, err)
		}
	}
	if changed {
		did = append(did, h.Label+": turned the plugin on, in plugins.enabled ("+cfg+")")
	}
	if grantChanged {
		did = append(did, h.Label+": let it start a turn in a session when a message comes (plugins.entries."+brand.MCPServer+"."+grantKey+" in "+cfg+")")
	}
	if grantErr != nil {
		return did, wrote || changed, fmt.Errorf("%s: %w, so %s can't start a turn in a Hermes session there: set plugins.entries.%s.%s to true for it to",
			cfg, grantErr, brand.Product, brand.MCPServer, grantKey)
	}
	return did, wrote || changed || grantChanged, nil
}

// RefreshHermes brings Murmur's Hermes plugin in each Hermes home up to this binary's, as the
// plugin lists the tools when Hermes' gateway starts: an update replaces the binary and doesn't run
// join again. It rewrites only a plugin join wrote that runs this very binary, and says whether it
// rewrote any. The gateway has read the old one by then, and loads the new one at its next start.
func RefreshHermes() (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	home, _ := os.UserHomeDir()
	dir := hermesHomeDir(os.Getenv, home)
	did := false
	var errs []error
	for _, h := range append([]agentHome{{Dir: dir}}, hermesProfiles(dir)...) {
		ok, err := refreshHermesAt(h.Dir, exe)
		did = did || ok
		errs = append(errs, err)
	}
	return did, errors.Join(errs...)
}

// hermesExeLine is the plugin's line that names the mur it runs.
var hermesExeLine = regexp.MustCompile(`(?m)^MUR = ("(?:[^"\\]|\\.)*")\s*$`)

func refreshHermesAt(dir, exe string) (bool, error) {
	if dir == "" {
		return false, nil
	}
	b, err := os.ReadFile(filepath.Join(hermesPluginDir(dir), "__init__.py"))
	if err != nil || !bytes.HasPrefix(b, []byte(hermesMarker)) {
		return false, nil
	}
	m := hermesExeLine.FindSubmatch(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")))
	var named string
	if m == nil || json.Unmarshal(m[1], &named) != nil || !sameFile(named, exe) {
		return false, nil
	}
	return writeHermesPlugin(hermesPluginDir(dir), named)
}

// unregisterHermesAt takes Murmur's plugin out of Hermes home hs[0], and its name out of
// plugins.enabled there. A plugin the user kept, as its first line invites, stays whole and on.
func (e Env) unregisterHermesAt(_ context.Context, hs []agentHome) (did, notes []string, err error) {
	h := hs[0]
	dir := hermesPluginDir(h.Dir)
	if hermesKept(dir) {
		return nil, []string{h.Label + ": left " + dir + " alone: it isn't the plugin " + brand.Product + " writes"}, nil
	}
	ours := func(b []byte) bool { return bytes.HasPrefix(b, []byte(hermesMarker)) }
	var problems []error
	gone := 0
	for _, n := range []string{"__init__.py", "plugin.yaml"} {
		removed, note, rerr := removeOwned(filepath.Join(dir, n), ours)
		if note != "" {
			notes = append(notes, h.Label+": "+note)
		}
		if rerr != nil {
			problems = append(problems, rerr)
		}
		if removed {
			gone++
		}
	}
	if gone > 0 {
		os.RemoveAll(filepath.Join(dir, "__pycache__")) // Python's, made as Hermes loaded the plugin
		os.Remove(dir)                                  // stays if anything else is in it
		did = append(did, h.Label+": removed "+brand.Product+"'s plugin ("+dir+")")
	}
	cfg := filepath.Join(h.Dir, "config.yaml")
	if text, rerr := readConfig(cfg); rerr != nil {
		problems = append(problems, rerr)
	} else if text != "" {
		out, changed, uerr := removeYAMLPlugin(text, brand.MCPServer)
		if uerr != nil {
			problems = append(problems, fmt.Errorf("%s: %w: take %s out of plugins.enabled by hand", cfg, uerr, brand.MCPServer))
		} else {
			ungrantedOut, ungranted, gerr := removeYAMLGrant(out, brand.MCPServer)
			if gerr != nil {
				problems = append(problems, fmt.Errorf("%s: %w: take plugins.entries.%s.%s out by hand", cfg, gerr, brand.MCPServer, grantKey))
			} else {
				out = ungrantedOut
			}
			var what []string
			if changed {
				what = append(what, "plugins.enabled")
			}
			if ungranted {
				what = append(what, "plugins.entries")
			}
			if len(what) > 0 {
				if werr := writeAtomic(cfg, []byte(out), 0o600); werr != nil {
					problems = append(problems, werr)
				} else {
					did = append(did, h.Label+": took the plugin out of "+strings.Join(what, " and ")+" ("+cfg+")")
				}
			}
		}
	}
	return did, notes, errors.Join(problems...)
}

// hermesOwned says the file at path is one join wrote.
func hermesOwned(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && bytes.HasPrefix(b, []byte(hermesMarker))
}

// hermesKept says the plugin in dir is there but no longer join's: the user changed it.
func hermesKept(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "__init__.py"))
	return err == nil && !bytes.HasPrefix(b, []byte(hermesMarker))
}

// hermesRemoval says how to take out by hand what join put in Hermes home h.
func hermesRemoval(_ Env, h agentHome) string {
	var steps []string
	dir := hermesPluginDir(h.Dir)
	if hermesKept(dir) {
		return ""
	}
	if hermesOwned(filepath.Join(dir, "__init__.py")) {
		steps = append(steps, "delete "+dir)
	}
	cfg := filepath.Join(h.Dir, "config.yaml")
	if text, err := readConfig(cfg); err == nil {
		if on, _ := yamlListHas(text, "enabled", brand.MCPServer); on {
			steps = append(steps, "take "+brand.MCPServer+" out of plugins.enabled in "+cfg)
		}
		if _, changed, err := removeYAMLGrant(text, brand.MCPServer); err == nil && changed {
			steps = append(steps, "take plugins.entries."+brand.MCPServer+"."+grantKey+" out of "+cfg)
		}
	}
	return strings.Join(steps, ", and ")
}

// looksLikeHermes says the agent running join is Hermes, which gives every command a session runs
// HERMES_SESSION_ID (tools/environments/local.py, seen 2026-11-23). An agent of another kind started
// from Hermes' terminal inherits it, so theirs win.
func (e Env) looksLikeHermes() bool {
	return e.getenv("HERMES_SESSION_ID") != "" && e.getenv("CLAUDECODE") != "1" && !e.looksLikeCodex() && !e.looksLikePi() &&
		!e.looksLikeKiro() && !e.looksLikeAntigravity() && !e.looksLikeCursor()
}
