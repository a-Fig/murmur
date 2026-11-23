package join

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/t3"
)

// agentHome is one configuration of one agent, where join registers Murmur's server: the agent's
// default one, or another account's that T3 Code runs (t3homes.go).
type agentHome struct {
	Dir   string // the agent's configuration directory
	JSON  string // for Claude Code, the file it keeps user-scope MCP servers in
	Label string // what the report calls it: "Claude Code", or with T3's name for the instance
	Bin   string // the agent's command as T3 runs it, for an agent that isn't on PATH
	// Named says a variable names Dir (CODEX_HOME) rather than Dir being the agent's default, which
	// Codex spells differently in its hooks' trust keys (codexKeyPath).
	Named bool
	// T3Own says T3 made Dir for one instance and manages what is in it (Antigravity's).
	T3Own bool
	// SkillsOnly is the shared Antigravity skills home when the standalone home is elsewhere.
	SkillsOnly bool
}

// agent is one of the coding agents join registers the server with.
type agent struct {
	driver string // T3 Code's name for it
	label  string
	// t3Var is the variable in a T3 instance's environment that moves its configuration, and sub
	// the directory under it that is the agent's (XDG_CONFIG_HOME/opencode); config.homePath, for
	// the drivers that have it, is the same variable's value.
	t3Var, sub string
	register   func(e Env, ctx context.Context, h agentHome) (did []string, fresh bool, err error)
	// removal says how to take the server out of h by hand, "" where there is nothing to take out.
	removal func(e Env, h agentHome) string
	// unregister takes out of hs, homes in one directory, what register put there, the skills
	// aside, for `mur uninstall` (uninstall.go).
	unregister func(e Env, ctx context.Context, hs []agentHome) (did, notes []string, err error)
}

// agents are the agents join knows, in the order it reports them.
var agents = []agent{
	{driver: "claudeAgent", label: "Claude Code", t3Var: "CLAUDE_CONFIG_DIR", register: Env.registerClaudeAt, removal: Env.claudeRemoval,
		unregister: Env.unregisterClaudeAt},
	{driver: "codex", label: "Codex", t3Var: "CODEX_HOME",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerCodexAt(h) },
		removal:    codexRemoval,
		unregister: Env.unregisterCodexAt},
	{driver: "opencode", label: "OpenCode", t3Var: "XDG_CONFIG_HOME", sub: "opencode",
		register:   Env.registerOpenCodeAt,
		removal:    openCodeRemoval,
		unregister: Env.unregisterOpenCodeAt},
	{driver: "muse", label: "Muse Code", t3Var: "XDG_CONFIG_HOME", sub: "muse",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerMuseAt(h) },
		removal:    museRemoval,
		unregister: Env.unregisterMuse},
	{driver: "pi", label: "Pi", t3Var: "PI_CODING_AGENT_DIR",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerPiAt(h) },
		removal:    piRemoval,
		unregister: Env.unregisterPiAt},
	{driver: "grok", label: "Grok", t3Var: "GROK_HOME",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerGrokAt(h) },
		removal:    grokRemoval,
		unregister: Env.unregisterGrokAt},
	{driver: "antigravity", label: "Antigravity",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerAntigravityAt(h) },
		removal:    antigravityRemoval,
		unregister: Env.unregisterAntigravityAt},
	{driver: "kiro", label: "Kiro", t3Var: "KIRO_HOME",
		register:   func(e Env, _ context.Context, h agentHome) ([]string, bool, error) { return e.registerKiroAt(h) },
		removal:    kiroRemoval,
		unregister: Env.unregisterKiroAt},
}

// defaultHome is an agent's configuration as this process's environment says, "" Dir where the
// Env doesn't say (a test's that leaves the agent out).
func (e Env) defaultHome(a agent) agentHome {
	h := agentHome{Label: a.label}
	switch a.driver {
	case "claudeAgent":
		h.Dir, h.JSON = e.ClaudeDir, e.ClaudeJSON
	case "codex":
		h.Dir, h.Named = e.CodexHome, !e.CodexHomeDefault
	case "opencode":
		h.Dir = e.OpenCodeDir
	case "muse":
		h.Dir = e.MuseDir
	case "pi":
		h.Dir = e.PiDir
	case "grok":
		h.Dir = e.GrokHome
	case "antigravity":
		h.Dir = e.GeminiHome
	case "kiro":
		h.Dir = e.KiroHome
	}
	return h
}

// t3Home is the configuration a T3 instance of agent a runs in.
func (e Env) t3Home(a agent, in t3Instance) (agentHome, bool) {
	h := agentHome{Label: a.label + " (" + in.Name + " in T3 Code)", Bin: in.binary()}
	if a.driver == "antigravity" {
		// A home of T3's own for each instance, which nothing in its settings moves.
		h.Dir, h.T3Own = antigravityHomeOf(e.T3Settings, in.ID), true
		return h, h.Dir != ""
	}
	if a.driver == "kiro" {
		// Kiro's T3 provider has no config.homePath; it uses KIRO_HOME from its environment
		// (subject to Kiro V3's resolveUserConfigDir validation), else ~/.kiro.
		if dir := kiroHomeDir(func(k string) string { return expandHome(in.Env[k], e.Home) }, e.Home); dir != "" {
			h.Dir = dir
			return h, true
		}
		h.Dir = e.defaultHome(a).Dir
		return h, h.Dir != ""
	}
	dir := in.homeOf(a.t3Var, e.Home)
	switch {
	case dir != "" && a.sub != "":
		h.Dir = filepath.Join(dir, a.sub)
	case dir != "":
		h.Dir, h.Named = dir, true
	case a.driver == "claudeAgent" || a.driver == "codex":
		// T3 runs these with no variable of their own in the user's default, whatever the agent
		// running join has: this one may be another account's.
		if e.Home == "" {
			return h, false
		}
		if a.driver == "codex" {
			h.Dir = filepath.Join(e.Home, ".codex")
		} else {
			h.Dir, h.JSON = filepath.Join(e.Home, ".claude"), filepath.Join(e.Home, ".claude.json")
		}
		return h, true
	case e.Home != "":
		// The default as T3 would see it, not as this process does: the agent running join may be
		// another instance, with its own XDG_CONFIG_HOME.
		h.Dir = filepath.Join(append([]string{e.Home}, userDefault[a.driver]...)...)
	default:
		h.Dir = e.defaultHome(a).Dir
	}
	if a.driver == "claudeAgent" {
		h.JSON = filepath.Join(h.Dir, ".claude.json")
	}
	return h, h.Dir != ""
}

// userDefault is each other agent's configuration directory under the user's home when nothing
// moves it.
var userDefault = map[string][]string{
	"opencode": {".config", "opencode"},
	"muse":     {".config", "muse"},
	"pi":       {".pi", "agent"},
	"grok":     {".grok"},
	"kiro":     {".kiro"},
}

// homes lists, for each agent, every configuration join registers in: the default first, then the
// ones T3's instances run in, each once. notes says what was passed over, and why.
func (e Env) homes() (byAgent [][]agentHome, notes []string) { return e.homesWith(false) }

// homesWith is homes, with the configurations of T3's instances that are turned off too when off
// is set: an uninstall takes Murmur out of one that join registered in before it was turned off.
func (e Env) homesWith(off bool) (byAgent [][]agentHome, notes []string) {
	byAgent = make([][]agentHome, len(agents))
	for i, a := range agents {
		byAgent[i] = []agentHome{e.defaultHome(a)}
	}
	// A scratch home's join reads T3's settings only from the temporary folder (scratch.go).
	skipT3 := e.T3Settings != "" && e.scratchHome() != "" && !inTemp(e.T3Settings, false)
	settings := e.T3Settings
	if skipT3 {
		settings = ""
	}
	instances, err := readT3Instances(settings)
	if err != nil {
		notes = append(notes, "Couldn't read T3 Code's settings ("+e.T3Settings+"). Some provider instances may be missing. "+err.Error())
	}
	managedGemini := false
	for _, in := range instances {
		i := agentIndex(in.Driver)
		if i < 0 {
			continue
		}
		a := agents[i]
		h, ok := e.t3Home(a, in)
		if !ok {
			continue
		}
		sameHome := samePath
		if h.T3Own {
			sameHome = sameEntry
		}
		if h.T3Own && sameHome(h.Dir, e.GeminiHome) {
			managedGemini = true
		}
		if !in.On && !off {
			// Said only of a configuration that nothing else registers in: one that is the caller's
			// active home (e.g. a per-project T3 override) still gets registered, with T3Own kept.
			if sameHome(h.Dir, byAgent[i][0].Dir) && samePath(h.JSON, byAgent[i][0].JSON) {
				if h.T3Own {
					byAgent[i][0] = h
				}
				continue
			}
			notes = append(notes, a.label+" as T3 Code's \""+in.Name+"\" is turned off in T3, so its configuration ("+h.Dir+") was left alone; `"+brand.Command+" join` again after turning it on registers it")
			continue
		}
		dup := false
		for j, o := range byAgent[i] {
			if sameHome(o.Dir, h.Dir) && samePath(o.JSON, h.JSON) {
				dup = dup || sameCodexKey(a, o, h)
				if h.T3Own {
					byAgent[i][j] = h
				} else if o.Bin == "" {
					byAgent[i][j].Bin = h.Bin // the default home, with the command T3 runs it by
				}
			}
		}
		if !dup {
			byAgent[i] = append(byAgent[i], h)
		}
	}
	i := agentIndex("antigravity")
	homes := byAgent[i]
	if !managedGemini && isT3AntigravityHome(e.GeminiHome) {
		managedGemini = true
		homes[0].T3Own = true
		homes[0].Label += " (T3 Code)"
	}
	if e.Home != "" {
		shared := filepath.Join(e.Home, ".gemini")
		alreadyListed := slices.ContainsFunc(homes, func(h agentHome) bool { return sameEntry(h.Dir, shared) })
		usesShared := managedGemini || slices.ContainsFunc(homes, func(h agentHome) bool { return h.T3Own && (off || isDir(h.Dir)) })
		if usesShared && !alreadyListed && !isT3AntigravityHome(shared) {
			// When GEMINI_HOME is T3's, use ~/.gemini for standalone registration too.
			homes = append([]agentHome{{Dir: shared, Label: agents[i].label, SkillsOnly: !managedGemini}}, homes...)
		}
	}
	byAgent[i] = homes
	cmd := "join"
	if off {
		cmd = "uninstall"
	}
	if note := e.keepToScratch(byAgent, settings != "", skipT3, cmd); note != "" {
		notes = append(notes, note)
	}
	return byAgent, notes
}

// sameCodexKey says two homes in one directory have Codex name their hook alike. Where one is
// named by CODEX_HOME and the other is the default, through a link (T3 naming ~/.codex, which is
// one), Codex spells each differently, and the hook is trusted under both: the second home is kept
// for that, and registering in it again changes nothing else.
func sameCodexKey(a agent, o, h agentHome) bool {
	if a.driver != "codex" || o.Named == h.Named {
		return true
	}
	p, q := filepath.Join(o.Dir, "hooks.json"), filepath.Join(h.Dir, "hooks.json")
	return codexKeyPath(p, o.Named) == codexKeyPath(q, h.Named)
}

func agentIndex(driver string) int {
	for i, a := range agents {
		if a.driver == driver {
			return i
		}
	}
	return -1
}

// t3SettingsPath is T3's settings.json, in the first of T3's data directories that has one.
func t3SettingsPath(cli, home string) string {
	for _, h := range t3.Homes(cli, home) {
		p := filepath.Join(h, "userdata", "settings.json")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	return ""
}

// samePath compares paths as the file system does: without case on Windows and macOS's default.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// removal says how to take the server out of every agent configuration join registers in, for
// the advice of a leave.
func (e Env) removal() string {
	byAgent, _ := e.homes()
	var steps []string
	for i, a := range agents {
		for _, h := range byAgent[i] {
			if h.Dir == "" {
				continue
			}
			if step := a.removal(e, h); step != "" {
				steps = append(steps, step+" (for "+h.Label+")")
			}
			if step := skillsRemoval(a, h); step != "" {
				steps = append(steps, step+" (the skills, for "+h.Label+")")
			}
		}
	}
	if len(steps) == 0 {
		return "nothing, as no agent here has it"
	}
	return strings.Join(steps, "; ")
}

// codexRemoval is the removal for Codex: the server, and the hook (codexhook.go).
func codexRemoval(e Env, h agentHome) string {
	steps := []string{tableRemoval(e, h)}
	path := filepath.Join(h.Dir, "hooks.json")
	if hasMurmurCodexHook(path) {
		steps = append(steps, "delete the hooks named "+hookName+" (statusMessage) from "+path)
	}
	return joinSteps(steps)
}

// grokRemoval is the removal for Grok: the server, and the hook's file (grokhook.go).
func grokRemoval(e Env, h agentHome) string {
	steps := []string{tableRemoval(e, h)}
	if path := filepath.Join(h.Dir, "hooks", grokHookFile); fileExists(path) {
		steps = append(steps, "delete "+path)
	}
	return joinSteps(steps)
}

// joinSteps joins the steps that aren't "".
func joinSteps(steps []string) string {
	var out []string
	for _, s := range steps {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", and ")
}

// tableRemoval is the removal of the server for an agent with a config.toml: Codex, Grok.
func tableRemoval(_ Env, h agentHome) string {
	path := filepath.Join(h.Dir, "config.toml")
	if b, err := os.ReadFile(path); err != nil || !strings.Contains(string(b), "[mcp_servers."+brand.MCPServer+"]") {
		return ""
	}
	return "delete the [mcp_servers." + brand.MCPServer + "] table from " + path
}
