package join

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if count("claudeAgent") != 3 || count("codex") != 2 || count("muse") != 1 || count("opencode") != 0 || count("grok") != 1 || count("antigravity") != 1 {
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
