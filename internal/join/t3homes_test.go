package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/skills"
)

// t3Settings is a T3 settings.json like the one on Tyler's laptop (2026-11-01), as far as join
// reads it, with a secret in it that join must not take.
func t3Settings(home string) string {
	q := func(p string) string { return filepath.ToSlash(p) }
	return `{
  "favorites": [{"provider": "opencode"}],
  "providerInstances": {
    "claude_personal": {"driver": "claudeAgent", "displayName": "personal claude", "enabled": true,
      "config": {"binaryPath": "claude", "homePath": "` + q(filepath.Join(home, ".claude_personal")) + `"}},
    "claudeAgent": {"driver": "claudeAgent", "displayName": "work claude", "enabled": true,
      "config": {"binaryPath": "claude", "homePath": "", "launchArgs": "", "autoCompactWindow": ""}},
    "codex_tilde": {"driver": "codex", "config": {"homePath": "~/.codex-work", "shadowHomePath": "~/.codex-t3/p"}},
    "pi": {"driver": "pi", "displayName": "Pi", "enabled": false,
      "environment": [{"name": "PI_CODING_AGENT_DIR", "value": "` + q(filepath.Join(home, "pi-t3")) + `"}]},
    "muse": {"driver": "muse", "displayName": "Muse Code", "enabled": true,
      "environment": [{"name": "META_API_KEY", "value": "secret-value", "sensitive": true}, {"name": "XDG_CONFIG_HOME", "value": "` + q(filepath.Join(home, "xdg")) + `"}],
      "config": {"binaryPath": "` + q(filepath.Join(home, "muse", "muse.cmd")) + `"}},
    "opencode": {"driver": "opencode", "config": {"binaryPath": "x", "serverPassword": 7}},
    "grok": {"driver": "grok", "displayName": "Grok", "config": {"enabled": true}},
    "broken": "not an object",
    "antigravity": {"driver": "antigravity", "enabled": true, "config": {"authMethod": "oauth-personal"}}
  }
}`
}

func TestReadT3InstancesFollowsT3sRules(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".t3", "userdata", "settings.json")
	write(t, path, t3Settings(home))
	got, err := readT3Instances(path)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]t3Instance{}
	for _, in := range got {
		byID[in.ID] = in
	}
	if _, ok := byID["broken"]; ok {
		t.Error("read an instance that isn't an object")
	}
	for id, on := range map[string]bool{"claude_personal": true, "claudeAgent": true, "codex_tilde": true, "pi": false, "muse": true,
		"opencode": false, "grok": true, "antigravity": true} {
		if byID[id].On != on {
			t.Errorf("%s: on = %v, want %v", id, byID[id].On, on)
		}
	}
	if h := byID["claude_personal"].homeOf("CLAUDE_CONFIG_DIR", home); h != filepath.Join(home, ".claude_personal") {
		t.Errorf("claude_personal's home is %q", h)
	}
	if h := byID["claudeAgent"].homeOf("CLAUDE_CONFIG_DIR", home); h != "" {
		t.Errorf("claudeAgent's home is %q, want the default", h)
	}
	if h := byID["codex_tilde"].homeOf("CODEX_HOME", home); h != filepath.Join(home, ".codex-work") {
		t.Errorf("codex_tilde's home is %q", h)
	}
	if v, ok := byID["muse"].Env["META_API_KEY"]; ok {
		t.Errorf("took a sensitive value: %q", v)
	}
	if byID["muse"].Env["XDG_CONFIG_HOME"] != filepath.ToSlash(filepath.Join(home, "xdg")) {
		t.Errorf("muse's environment: %v", byID["muse"].Env)
	}
	if byID["muse"].binary() != "" {
		t.Error("binary() named a file that isn't there")
	}
	write(t, filepath.Join(home, "muse", "muse.cmd"), "@echo off\n")
	if byID["muse"].binary() == "" {
		t.Error("binary() missed the file")
	}
}

func TestReadT3InstancesWithoutT3(t *testing.T) {
	if got, err := readT3Instances(filepath.Join(t.TempDir(), "nope.json")); err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
	if got, err := readT3Instances(""); err != nil || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
	path := filepath.Join(t.TempDir(), "settings.json")
	write(t, path, `{"providerInstances": {"x": {"driver": "pi", "environment": [{"name": "K", "value": "secret-value"}]}`)
	if _, err := readT3Instances(path); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Errorf("err = %v", err)
	}
}

// Every Claude Code and Codex home T3 runs gets the server, the default ones included, each once;
// an instance that is off is named and left alone (Tyler, 2026-11-01: his personal Claude Code in T3
// had no Murmur tools).
func TestJoinRegistersInEveryHomeT3Runs(t *testing.T) {
	sb := newSandbox(t, true)
	// The default homes are under the sandbox's home directory, so that the stand-in claude, run
	// with CLAUDE_CONFIG_DIR unset for them, writes there and not in the real user's.
	t.Setenv("HOME", sb.root)
	t.Setenv("USERPROFILE", sb.root)
	sb.Home = sb.root
	sb.T3Settings = filepath.Join(sb.root, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, t3Settings(sb.root))
	write(t, filepath.Join(sb.root, ".codex-work", "config.toml"), "model = \"x\"\n")

	write(t, filepath.Join(sb.root, "muse", "muse.cmd"), "@echo off\n") // Muse is found by T3's binaryPath
	// T3's own home for its Antigravity instance, named by the instance ID's SHA-256, as on Tyler's
	// laptop (2026-11-15), and there once T3 has run it (#650).
	agyHome := filepath.Join(sb.root, ".t3", "userdata", "providers", "antigravity", "ac0a3dfd6dddb20962cecff6ee5fe65e19d3923be20e52c5ab52ff877f7e4c32")
	if err := os.MkdirAll(agyHome, 0o755); err != nil {
		t.Fatal(err)
	}

	byAgent, notes := sb.homes()
	count := func(driver string) (n int) {
		for _, h := range byAgent[agentIndex(driver)] {
			if h.Dir != "" {
				n++
			}
		}
		return n
	}
	if count("claudeAgent") != 3 || count("codex") != 2 || count("muse") != 1 || count("opencode") != 0 || count("grok") != 1 || count("antigravity") != 2 {
		t.Fatalf("homes %+v", byAgent)
	}
	if !strings.Contains(strings.Join(notes, "\n"), `Pi as T3 Code's "Pi" is turned off`) {
		t.Errorf("notes don't name the instance that is off: %q", notes)
	}
	for i, a := range agents {
		for _, h := range byAgent[i] {
			if _, _, err := a.register(sb.Env, context.Background(), h); err != nil {
				t.Fatalf("%s: %v", h.Label, err)
			}
		}
	}
	if got := read(t, filepath.Join(sb.root, "xdg", "muse", "settings.json")); !strings.Contains(got, `"mode": "optional"`) || !strings.Contains(got, `"schema_version": 1`) {
		t.Errorf("Muse's settings.json:\n%s", got)
	}
	for _, path := range []string{sb.ClaudeJSON, filepath.Join(sb.root, ".claude.json"), filepath.Join(sb.root, ".claude_personal", ".claude.json")} {
		if !strings.Contains(read(t, path), `"murmur"`) {
			t.Errorf("%s lacks the server", path)
		}
	}
	for _, dir := range []string{filepath.Join(sb.root, ".claude"), filepath.Join(sb.root, ".claude_personal")} {
		if !strings.Contains(read(t, filepath.Join(dir, "settings.json")), allowRule) {
			t.Errorf("%s doesn't allow the tools", dir)
		}
	}
	if got := read(t, filepath.Join(agyHome, "config", "mcp_config.json")); !strings.Contains(got, `"murmur"`) {
		t.Errorf("T3's Antigravity home's mcp_config.json:\n%s", got)
	}
	if exists(filepath.Join(agyHome, "config", "skills")) {
		t.Error("join made T3's Antigravity home's skills folder, which T3 links to the user's")
	}
	if got := read(t, filepath.Join(agyHome, "config", "hooks.json")); !strings.Contains(got, "--event Stop antigravity") {
		t.Errorf("T3's Antigravity home's hooks.json:\n%s", got)
	}
	if got := read(t, filepath.Join(sb.root, ".codex-work", "config.toml")); !strings.HasPrefix(got, "model = \"x\"\n") || !strings.Contains(got, "[mcp_servers.murmur]") {
		t.Errorf("the T3 Codex home's config.toml:\n%s", got)
	}

	// Leaving says how to undo each.
	advice := sb.removal()
	for _, want := range []string{"CLAUDE_CONFIG_DIR set to " + filepath.Join(sb.root, ".claude_personal"), "personal claude", filepath.Join(sb.root, ".codex-work", "config.toml"),
		filepath.Join(sb.root, "xdg", "muse", "settings.json")} {
		if !strings.Contains(advice, want) {
			t.Errorf("leave's advice lacks %q:\n%s", want, advice)
		}
	}
}

// One home named twice, by T3 and by the environment, is registered in once.
func TestHomesAreDeduplicated(t *testing.T) {
	sb := newSandbox(t, false)
	sb.Home = sb.root
	sb.T3Settings = filepath.Join(sb.root, "settings.json")
	write(t, sb.T3Settings, `{"providerInstances": {"a": {"driver": "claudeAgent", "config": {"homePath": "`+filepath.ToSlash(sb.ClaudeDir)+`"}},
		"b": {"driver": "claudeAgent", "config": {"homePath": "`+filepath.ToSlash(strings.ToUpper(sb.ClaudeDir[:1])+sb.ClaudeDir[1:])+`/"}}}}`)
	if byAgent, _ := sb.homes(); len(byAgent[0]) != 1 {
		t.Errorf("claude homes %+v", byAgent[0])
	}
}

func TestAntigravityHomeDedupKeepsT3Ownership(t *testing.T) {
	sb := newSandbox(t, false)
	sb.T3Settings = filepath.Join(sb.root, "settings.json")
	sb.GeminiHome = antigravityHomeOf(sb.T3Settings, "work")
	bin := filepath.Join(sb.root, exeName("agy"))
	write(t, bin, "")
	write(t, sb.T3Settings, fmt.Sprintf(`{"providerInstances":{"work":{"driver":"antigravity","enabled":true,"config":{"binaryPath":%q}}}}`, bin))
	for _, off := range []bool{false, true} {
		homes, _ := sb.homesWith(off)
		got := homes[agentIndex("antigravity")]
		want := agentHome{Dir: sb.GeminiHome, Bin: bin, T3Own: true, Label: "Antigravity (work in T3 Code)"}
		if len(got) != 1 || got[0] != want {
			t.Errorf("off=%v: homes = %+v, want %+v", off, got, want)
		}
	}
}

func TestJoinAndUninstallFromT3Antigravity(t *testing.T) {
	for _, c := range []struct {
		name                         string
		bin, onPath, cliHome, custom bool
	}{
		{name: "bundled"},
		{name: "configured binary", bin: true, cliHome: true},
		{name: "agy on PATH", onPath: true},
		{name: "custom GEMINI_HOME", onPath: true, custom: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sb, _ := uninstallSandbox(t)
			sb.Home = filepath.Join(sb.root, "home")
			sb.T3Settings = filepath.Join(sb.root, ".t3", "userdata", "settings.json")
			sb.GeminiHome = antigravityHomeOf(sb.T3Settings, "active")
			sb.Getenv = envWith(map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_THREAD_ID": "thread-1"}).Getenv
			bin := ""
			if c.bin {
				bin = filepath.Join(sb.root, exeName("agy"))
				write(t, bin, "")
			}
			sb.LookPath = func(name string) (string, error) {
				if name == "agy" && c.onPath {
					return filepath.Join(sb.root, exeName("agy")), nil
				}
				return "", exec.ErrNotFound
			}
			settings := `{"providerInstances":{
				"active":{"driver":"antigravity","enabled":%t,"config":{"binaryPath":%q}},
				"later":{"driver":"antigravity","enabled":true,"config":{"binaryPath":%q}},
				"unused":{"driver":"antigravity","enabled":false}
			}}`
			write(t, sb.T3Settings, fmt.Sprintf(settings, true, bin, bin))
			inst := sb.GeminiHome
			shared := filepath.Join(sb.Home, ".gemini")
			standalone, activeIndex := shared, 1
			if c.custom {
				standalone = filepath.Join(sb.root, "custom-gemini")
				sb.GeminiHome, activeIndex = standalone, 2
			}
			later := antigravityHomeOf(sb.T3Settings, "later")
			unused := antigravityHomeOf(sb.T3Settings, "unused")
			untouched := filepath.Join(unused, "config", "mcp_config.json")
			const userConfig = `{"mcpServers":{"other":{}}}`
			write(t, untouched, userConfig)
			acp := filepath.Join(inst, "antigravity-acp", "settings.json")
			cli := filepath.Join(standalone, "antigravity-cli", "settings.json")
			mustMakeDir(t, inst)
			if c.cliHome {
				mustMakeDir(t, filepath.Dir(cli))
			}

			homes, _ := sb.homes()
			got := homes[agentIndex("antigravity")]
			if len(got) != activeIndex+2 || got[0].Dir != shared || got[0].T3Own || got[0].SkillsOnly != c.custom ||
				got[activeIndex].Dir != inst || !got[activeIndex].T3Own || got[activeIndex].Bin != bin {
				t.Fatalf("Antigravity homes: %+v", got)
			}

			p := startJoinTestDaemon(t)
			r, err := Join(t.Context(), sb.Env, p.Top)
			if err != nil || !strings.Contains(r.Tell[0], "To load its tools, open a new thread") {
				t.Fatalf("join: %v\n%s", err, r)
			}
			// Join just added Murmur to the standalone home too, whose open sessions need restarting.
			const othersRestart = "Antigravity loads MCP servers only when a session starts"
			if tell := strings.Join(r.Tell, "\n"); strings.Contains(tell, othersRestart) != (c.cliHome || c.onPath) {
				t.Errorf("advice about the standalone home:\n%s", tell)
			}
			if !strings.Contains(r.String(), "Not done:\n") || !strings.Contains(r.String(), "  - Antigravity (later in T3 Code): open a thread") || exists(later) {
				t.Errorf("join with an unused T3 instance:\n%s", r)
			}
			if !strings.Contains(strings.Join(r.Notes, "\n"), `T3 Code's "unused" is turned off`) || read(t, untouched) != userConfig || exists(filepath.Join(unused, "config", "hooks.json")) {
				t.Errorf("join with a disabled T3 instance:\n%s", r)
			}
			for _, name := range []string{"mcp_config.json", "hooks.json"} {
				if !strings.Contains(read(t, filepath.Join(inst, "config", name)), `"murmur"`) {
					t.Errorf("missing T3 registration in %s", name)
				}
			}
			if !strings.Contains(read(t, acp), `"mcp(murmur/*)"`) {
				t.Errorf("missing T3 tool approval in %s", acp)
			}
			if exists(filepath.Join(inst, "antigravity-cli")) || exists(filepath.Join(standalone, "antigravity-acp")) {
				t.Error("wrote approval to the wrong app data directory")
			}
			standaloneMCP := filepath.Join(standalone, "config", "mcp_config.json")
			if c.cliHome || c.onPath {
				if !strings.Contains(read(t, cli), `"mcp(murmur/*)"`) || !strings.Contains(read(t, standaloneMCP), `"murmur"`) {
					t.Error("missing registration or approval for standalone Antigravity")
				}
			} else if exists(cli) || exists(standaloneMCP) {
				t.Error("registered standalone Antigravity with only T3 installed")
			}
			if c.custom && (exists(filepath.Join(shared, "config", "mcp_config.json")) || exists(filepath.Join(shared, "antigravity-cli"))) {
				t.Error("registered in the shared skills home despite a custom GEMINI_HOME")
			}
			for _, s := range skills.All() {
				if read(t, filepath.Join(standalone, "config", "skills", s.Name, "SKILL.md")) != string(s.Text) {
					t.Errorf("missing skill %s in the user's home", s.Name)
				}
				if c.custom && read(t, filepath.Join(shared, "config", "skills", s.Name, "SKILL.md")) != string(s.Text) {
					t.Errorf("missing shared T3 skill %s", s.Name)
				}
			}
			if exists(filepath.Join(inst, "config", "skills")) {
				t.Error("created a skills folder in T3's instance home")
			}

			mustMakeDir(t, later)
			// This session's home was registered already; the instance just opened is fresh, and its
			// sessions are told to restart.
			if r, err := Join(t.Context(), sb.Env, p.Top); err != nil || !strings.Contains(r.Tell[0], "within a few seconds") ||
				!strings.Contains(strings.Join(r.Tell, "\n"), "was just added to Antigravity. "+othersRestart) {
				t.Fatalf("join after opening a different instance: %v\n%s", err, r)
			}
			write(t, sb.T3Settings, fmt.Sprintf(settings, false, bin, bin))
			if r, err := Uninstall(t.Context(), sb.Env, true); err != nil {
				t.Fatalf("uninstall: %v\n%s", err, r)
			}
			for _, path := range []string{acp, cli, standaloneMCP, filepath.Join(inst, "config", "mcp_config.json"), filepath.Join(inst, "config", "hooks.json")} {
				if b, err := os.ReadFile(path); err == nil && strings.Contains(string(b), "murmur") || err != nil && !os.IsNotExist(err) {
					t.Errorf("after uninstall, %s = %q, err = %v", path, b, err)
				}
			}
			for _, s := range skills.All() {
				if exists(filepath.Join(standalone, "config", "skills", s.Name, "SKILL.md")) || exists(filepath.Join(shared, "config", "skills", s.Name, "SKILL.md")) {
					t.Errorf("uninstall left the user's %s skill", s.Name)
				}
			}
			if exists(filepath.Join(inst, "config", "skills")) {
				t.Error("uninstall changed T3's skills directory")
			}
		})
	}
}

func TestJoinAndUninstallRecognizeManagedGeminiHome(t *testing.T) {
	for _, mode := range []string{"junction", "missing settings", "junction with invalid settings"} {
		t.Run(mode, func(t *testing.T) {
			sb, _ := uninstallSandbox(t)
			sb.Home = filepath.Join(sb.root, "home")
			sb.T3Settings = filepath.Join(sb.root, "userdata", "settings.json")
			sb.Getenv = envWith(map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_THREAD_ID": "thread-1"}).Getenv
			sb.LookPath = func(name string) (string, error) {
				if name == "agy" {
					return filepath.Join(sb.root, exeName("agy")), nil
				}
				return "", exec.ErrNotFound
			}
			inst := antigravityHomeOf(sb.T3Settings, "active")
			switch mode {
			case "junction":
				write(t, sb.T3Settings, `{"providerInstances":{"active":{"driver":"antigravity","enabled":true}}}`)
			case "missing settings":
				// T3's settings aren't found, so only the home's path says it is T3's.
				sb.T3Settings = ""
			case "junction with invalid settings":
				write(t, sb.T3Settings, `{`)
			}
			mustMakeDir(t, inst)
			sb.GeminiHome = inst
			if mode != "missing settings" {
				sb.GeminiHome = filepath.Join(sb.root, "gemini-alias")
				linkAntigravityDir(t, inst, sb.GeminiHome)
			}
			p := startJoinTestDaemon(t)
			r, err := Join(t.Context(), sb.Env, p.Top)
			if err != nil || !strings.Contains(r.Tell[0], "was just added to Antigravity") {
				t.Fatalf("join: %v\n%s", err, r)
			}
			acp := filepath.Join(inst, "antigravity-acp", "settings.json")
			mcp := filepath.Join(inst, "config", "mcp_config.json")
			instanceSkills := filepath.Join(inst, "config", "skills")
			sharedSkill := filepath.Join(sb.Home, ".gemini", "config", "skills", "murmur", "SKILL.md")
			if exists(instanceSkills) || exists(filepath.Join(inst, "antigravity-cli")) || !exists(sharedSkill) ||
				!strings.Contains(read(t, acp), `"mcp(murmur/*)"`) || !strings.Contains(read(t, mcp), `"murmur"`) {
				t.Errorf("join used the wrong home:\n%s", r)
			}
			// An instance's skills link can point outside the shared home. Only remove shared skills.
			linkedSkills := filepath.Join(sb.root, "other-skills")
			linkedSkill := filepath.Join(linkedSkills, "murmur", "SKILL.md")
			write(t, linkedSkill, read(t, sharedSkill))
			linkAntigravityDir(t, linkedSkills, instanceSkills)
			if r, err := Uninstall(t.Context(), sb.Env, true); err != nil {
				t.Fatalf("uninstall: %v\n%s", err, r)
			}
			if !exists(linkedSkill) || !sameEntry(instanceSkills, linkedSkills) || exists(sharedSkill) ||
				strings.Contains(read(t, acp), `"mcp(murmur/*)"`) || strings.Contains(read(t, mcp), `"murmur"`) {
				t.Error("uninstall missed the managed home or followed its skills link")
			}
		})
	}
}

// A plain ~/.gemini is the user's own even when it holds only antigravity-acp/, as it does for
// anyone running Antigravity over ACP outside T3: its skills go there, and uninstall takes them out.
func TestPlainGeminiHomeWithACPIsNotT3s(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	sb.Home = filepath.Join(sb.root, "home")
	sb.GeminiHome = filepath.Join(sb.Home, ".gemini")
	sb.LookPath = func(name string) (string, error) {
		if name == "agy" {
			return filepath.Join(sb.root, exeName("agy")), nil
		}
		return "", exec.ErrNotFound
	}
	mustMakeDir(t, filepath.Join(sb.GeminiHome, "antigravity-acp"))
	if isT3AntigravityHome(sb.GeminiHome) {
		t.Fatal("a plain ~/.gemini with antigravity-acp/ was taken for T3's")
	}
	homes, _ := sb.homes()
	if got := homes[agentIndex("antigravity")]; len(got) != 1 || got[0].T3Own || got[0].Label != "Antigravity" || !samePath(got[0].Dir, sb.GeminiHome) {
		t.Fatalf("Antigravity homes: %+v", got)
	}
	p := startJoinTestDaemon(t)
	if r, err := Join(t.Context(), sb.Env, p.Top); err != nil {
		t.Fatalf("join: %v\n%s", err, r)
	}
	skill := filepath.Join(sb.GeminiHome, "config", "skills", "murmur", "SKILL.md")
	if !exists(skill) {
		t.Error("join installed no skills in ~/.gemini")
	}
	if r, err := Uninstall(t.Context(), sb.Env, true); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, r)
	}
	if exists(skill) {
		t.Error("uninstall left the skills in ~/.gemini")
	}
}

// An instance turned off in T3's global settings but on for this project (87fb8153) is the home
// the session runs in: it keeps T3Own, gets the server, hook and rule, no skills folder of its own,
// and no note that it was turned off.
func TestJoinRegistersPerProjectT3AntigravityHome(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	sb.Home = filepath.Join(sb.root, "home")
	sb.T3Settings = filepath.Join(sb.root, ".t3", "userdata", "settings.json")
	sb.GeminiHome = antigravityHomeOf(sb.T3Settings, "active")
	sb.Getenv = envWith(map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_THREAD_ID": "thread-1"}).Getenv
	write(t, sb.T3Settings, `{"providerInstances":{"active":{"driver":"antigravity","enabled":false}}}`)
	inst := sb.GeminiHome
	mustMakeDir(t, inst)

	homes, _ := sb.homes()
	if !slices.ContainsFunc(homes[agentIndex("antigravity")], func(h agentHome) bool { return h.T3Own && samePath(h.Dir, inst) }) {
		t.Fatalf("Antigravity homes: %+v", homes[agentIndex("antigravity")])
	}
	p := startJoinTestDaemon(t)
	r, err := Join(t.Context(), sb.Env, p.Top)
	if err != nil {
		t.Fatalf("join: %v\n%s", err, r)
	}
	for _, name := range []string{"mcp_config.json", "hooks.json"} {
		if !strings.Contains(read(t, filepath.Join(inst, "config", name)), `"murmur"`) {
			t.Errorf("missing T3 registration in %s", name)
		}
	}
	if !strings.Contains(read(t, filepath.Join(inst, "antigravity-acp", "settings.json")), `"mcp(murmur/*)"`) {
		t.Error("missing T3 tool approval")
	}
	if exists(filepath.Join(inst, "config", "skills")) {
		t.Error("created a skills folder in T3's instance home")
	}
	if strings.Contains(r.String(), "turned off") {
		t.Errorf("said the instance this session runs in is turned off:\n%s", r)
	}
}

func linkAntigravityDir(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("making a junction: %v\n%s", err, out)
		}
		return
	}
	linkOrSkip(t, target, link)
}

func TestJoinReportsMalformedAntigravityApproval(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	path := filepath.Join(sb.GeminiHome, "antigravity-cli", "settings.json")
	p := startJoinTestDaemon(t)
	for _, c := range []struct{ settings, reason string }{
		{`{"permissions":[]}`, `"permissions" isn't an object`},
		{`{"permissions":{"allow":true}}`, "permissions.allow isn't a list"},
	} {
		write(t, path, c.settings)
		r, err := Join(t.Context(), sb.Env, p.Top)
		want := `Antigravity: couldn't add "mcp(murmur/*)" to permissions.allow in ` + path + ": " + c.reason + "."
		if err != nil || !p.Joined() || len(r.Notes) != 1 || r.Notes[0] != want {
			t.Fatalf("join with %s: %v\n%s", c.settings, err, r)
		}
		if read(t, path) != c.settings || !exists(filepath.Join(sb.GeminiHome, "config", "mcp_config.json")) ||
			!exists(filepath.Join(sb.GeminiHome, "config", "hooks.json")) {
			t.Errorf("join didn't keep settings and install the server and hook:\n%s", r)
		}
	}
}

func TestJoinReportsDeniedAntigravityApproval(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	sb.Home = filepath.Join(sb.root, "home")
	sb.T3Settings = filepath.Join(sb.root, "settings.json")
	sb.GeminiHome = antigravityHomeOf(sb.T3Settings, "active")
	sb.Getenv = envWith(map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_THREAD_ID": "thread-1"}).Getenv
	write(t, sb.T3Settings, `{"providerInstances":{"active":{"driver":"antigravity","enabled":true}}}`)
	path := filepath.Join(sb.GeminiHome, "antigravity-acp", "settings.json")
	const original = `{"model":"mine"}`
	write(t, path, original)
	protected, mode := filepath.Dir(path), os.FileMode(0o500)
	if runtime.GOOS == "windows" {
		protected, mode = path, 0o444
	}
	if err := os.Chmod(protected, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(protected, 0o755) })
	if err := writeAtomic(path, []byte(original), 0o600); err == nil {
		t.Skip("the test process can replace the protected settings file")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	p := startJoinTestDaemon(t)
	r, err := Join(t.Context(), sb.Env, p.Top)
	want := `Antigravity (active in T3 Code): couldn't add "mcp(murmur/*)" to permissions.allow in ` + path +
		", so Murmur's tools will ask before each call. Add it by hand, outside Antigravity."
	if err != nil || len(r.Notes) != 1 || r.Notes[0] != want {
		t.Fatalf("join: %v\n%s", err, r)
	}
	if read(t, path) != original || !exists(filepath.Join(sb.GeminiHome, "config", "mcp_config.json")) || !exists(filepath.Join(sb.GeminiHome, "config", "hooks.json")) {
		t.Errorf("join didn't keep settings and install the server and hook:\n%s", r)
	}
}

func mustMakeDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// Join normally starts its own executable as the daemon. Give it a daemon with a local git
// remote so it can exercise the real join path from the test binary.
func startJoinTestDaemon(t *testing.T) Place {
	t.Helper()
	p, _ := clone(t) // with a remote, as the daemon needs
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = daemon.Run(ctx, daemon.Options{GitDir: p.GitDir, NamePool: -1})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if runErr != nil {
			t.Errorf("daemon: %v", runErr)
		}
	})
	ready, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := client.Connect(ready, p.GitDir, false); err == nil {
			return p
		}
		select {
		case <-done:
			t.Fatalf("daemon exited before it was ready: %v", runErr)
		case <-ready.Done():
			t.Fatal("daemon wasn't ready within 15 seconds")
		case <-tick.C:
		}
	}
}

// ~/.codex as the default and as a T3 instance's CODEX_HOME is one home, unless it is a link: then
// Codex names its hook by the link for one and by where it leads for the other, and join keeps both
// so that the hook is trusted under each (Sol's review, 2026-11-09). The home directory is spelled
// canonically, as a real one is: a temporary directory isn't on macOS (/var is a link to
// /private/var) or on CI's Windows runner (RUNNER~1), and there the two spellings differ even with
// no link of the test's own.
func TestACodexHomeNamedTwoWays(t *testing.T) {
	for _, linked := range []bool{false, true} {
		sb := newSandbox(t, false)
		root, err := filepath.EvalSymlinks(sb.root)
		if err != nil {
			t.Fatal(err)
		}
		sb.Home = root
		sb.CodexHome, sb.CodexHomeDefault = filepath.Join(root, ".codex"), true
		if linked {
			real := filepath.Join(root, "dotfiles", "codex")
			if err := os.MkdirAll(real, 0o755); err != nil {
				t.Fatal(err)
			}
			linkOrSkip(t, real, sb.CodexHome)
		} else if err := os.MkdirAll(sb.CodexHome, 0o755); err != nil {
			t.Fatal(err)
		}
		sb.T3Settings = filepath.Join(sb.root, "settings.json")
		write(t, sb.T3Settings, `{"providerInstances": {"c": {"driver": "codex", "config": {"homePath": "`+filepath.ToSlash(sb.CodexHome)+`"}}}}`)
		byAgent, _ := sb.homes()
		want := 1
		if linked {
			want = 2
		}
		if got := byAgent[agentIndex("codex")]; len(got) != want {
			t.Errorf("linked %v: codex homes %+v", linked, got)
		}
	}
}
