package join

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

func TestKiroHomeDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	custom := filepath.Join(root, "custom-kiro")
	mustMakeDir(t, home)

	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		{"unset", "", filepath.Join(home, ".kiro")},
		{"whitespace", "   ", filepath.Join(home, ".kiro")},
		{"relative ignored", "relative/kiro", filepath.Join(home, ".kiro")},
		{"root ignored", filepath.VolumeName(root) + string(filepath.Separator), filepath.Join(home, ".kiro")},
		{"equals home ignored", home, filepath.Join(home, ".kiro")},
		{"contains home ignored", root, filepath.Join(home, ".kiro")},
		{"valid absolute override", custom, custom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kiroHomeDir(func(k string) string {
				if k == "KIRO_HOME" {
					return tc.env
				}
				return ""
			}, home)
			if !samePath(got, tc.want) {
				t.Errorf("kiroHomeDir(%q) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

func TestHasKiro(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(home, ".kiro")
	h := agentHome{Dir: dir, Label: "Kiro"}
	e := Env{Home: home, LookPath: func(string) (string, error) { return "", exec.ErrNotFound }}

	if e.hasKiro(h) {
		t.Fatal("reported Kiro installed with no dir, binary or PATH entry")
	}
	if !(Env{Home: home, LookPath: func(name string) (string, error) {
		if name == "kiro-cli" {
			return "/usr/local/bin/kiro-cli", nil
		}
		return "", exec.ErrNotFound
	}}).hasKiro(h) {
		t.Error("missed kiro-cli on PATH")
	}
	if !e.hasKiro(agentHome{Dir: dir, Bin: "/opt/kiro-cli", Label: "Kiro"}) {
		t.Error("missed T3 binaryPath")
	}

	winBin := filepath.Join(home, "AppData", "Local", "Kiro-Cli", "kiro-cli.exe")
	write(t, winBin, "")
	if !e.hasKiro(h) {
		t.Error("missed default Windows per-user kiro-cli.exe")
	}
	if err := os.Remove(winBin); err != nil {
		t.Fatal(err)
	}
	mustMakeDir(t, dir)
	if !e.hasKiro(h) {
		t.Error("missed existing ~/.kiro directory")
	}
}

func TestRegisterAndUnregisterKiro(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])

	mcpPath := filepath.Join(sb.KiroHome, "settings", "mcp.json")
	permPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")
	write(t, mcpPath, "{\n  // user server\n  \"mcpServers\": {\n    \"other\": {\"command\": \"o\", \"args\": []}\n  }\n}\n")

	did, fresh, err := sb.registerKiroAt(h)
	if err != nil || !fresh || len(did) != 2 {
		t.Fatalf("first register: did=%q fresh=%v err=%v", did, fresh, err)
	}
	mcpText := read(t, mcpPath)
	for _, want := range []string{"// user server", `"other"`, `"` + brand.MCPServer + `"`, `"--agent"`, `"kiro"`} {
		if !strings.Contains(mcpText, want) {
			t.Errorf("mcp.json lacks %s:\n%s", want, mcpText)
		}
	}
	permText := read(t, permPath)
	if !json.Valid([]byte(permText)) {
		t.Fatalf("permissions.json isn't valid plain JSON:\n%s", permText)
	}
	for _, want := range []string{`"rules"`, `"capability": "mcp"`, `"` + kiroAllowPattern + `"`, `"effect": "allow"`} {
		if !strings.Contains(permText, want) {
			t.Errorf("permissions.json lacks %s:\n%s", want, permText)
		}
	}

	// Idempotent rerun changes nothing.
	did, fresh, err = sb.registerKiroAt(h)
	if err != nil || fresh || len(did) != 0 || read(t, mcpPath) != mcpText || read(t, permPath) != permText {
		t.Errorf("second register: did=%q fresh=%v err=%v", did, fresh, err)
	}

	// Repointing a moved binary updates command without marking fresh.
	sb.Exe = filepath.Join(sb.root, "bin2", exeName("mur"))
	did, fresh, err = sb.registerKiroAt(h)
	if err != nil || fresh || len(did) != 1 || !strings.Contains(read(t, mcpPath), "bin2") {
		t.Errorf("repoint register: did=%q fresh=%v err=%v\n%s", did, fresh, err, read(t, mcpPath))
	}

	// Manual removal describes both files and preserves merged allow rules.
	if rem := kiroRemoval(sb.Env, h); !strings.Contains(rem, mcpPath) || !strings.Contains(rem, `remove "murmur/*" from the MCP allow rule in `+permPath) {
		t.Errorf("kiroRemoval = %q", rem)
	}

	// Uninstall removes both while keeping the user's other server and valid "rules": [].
	did, _, err = sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 2 {
		t.Fatalf("unregister: did=%q err=%v", did, err)
	}
	if got := read(t, mcpPath); strings.Contains(got, `"`+brand.MCPServer+`"`) || !strings.Contains(got, `"other"`) || !strings.Contains(got, "// user server") {
		t.Errorf("mcp.json after uninstall:\n%s", got)
	}
	if got := read(t, permPath); strings.Contains(got, kiroAllowPattern) || !strings.Contains(got, `"rules"`) || !json.Valid([]byte(got)) {
		t.Errorf("permissions.json after uninstall:\n%s", got)
	}
	if rem := kiroRemoval(sb.Env, h); rem != "" {
		t.Errorf("kiroRemoval after uninstall = %q", rem)
	}
}

// Because Kiro's addUserRule merges single-match rules of the same capability and effect into one
// rule's match array, uninstall removes only murmur/* and preserves the user's other patterns.
func TestUnregisterKiroPreservesMergedAllowPatterns(t *testing.T) {
	h := agentHome{Dir: t.TempDir(), Label: "Kiro"}
	permPath := filepath.Join(h.Dir, "settings", "permissions.json")
	write(t, permPath, "{\n  \"rules\": [\n    {\"capability\": \"mcp\", \"match\": [\"murmur/*\", \"github/*\"], \"effect\": \"allow\"},\n    {\"capability\": \"shell\", \"match\": [\"git status\"], \"effect\": \"allow\"}\n  ]\n}\n")

	did, _, err := (Env{}).unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 1 {
		t.Fatalf("unregister: did=%q err=%v", did, err)
	}
	got := read(t, permPath)
	if strings.Contains(got, kiroAllowPattern) || !strings.Contains(got, `"github/*"`) || !strings.Contains(got, `"git status"`) || !json.Valid([]byte(got)) {
		t.Errorf("permissions.json:\n%s", got)
	}
}

// Kiro reads permissions.yaml instead of permissions.json when permissions.yaml exists. Join
// still registers the MCP server, doesn't touch permissions.json, and always returns an
// approvalNote (never guessing from unparsed YAML comments or deny rules). Uninstall and manual
// removal likewise leave a shadowed permissions.json alone.
func TestRegisterKiroWithPermissionsYAML(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	yamlPath := filepath.Join(sb.KiroHome, "settings", "permissions.yaml")
	jsonPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")

	write(t, yamlPath, "rules:\n  # murmur/* is denied\n  - capability: mcp\n    match:\n      - murmur/*\n    effect: deny\n")
	did, fresh, err := sb.registerKiroAt(h)
	var note approvalNote
	if !errors.As(err, &note) || !fresh || len(did) != 1 {
		t.Fatalf("with yaml: did=%q fresh=%v err=%v", did, fresh, err)
	}
	if exists(jsonPath) {
		t.Error("created permissions.json alongside permissions.yaml")
	}

	// Even if a fallback permissions.json exists with murmur/*, uninstall and removal leave it alone.
	shadow := `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "allow"}]}` + "\n"
	write(t, jsonPath, shadow)
	if rem := kiroRemoval(sb.Env, h); strings.Contains(rem, jsonPath) {
		t.Errorf("kiroRemoval named shadowed permissions.json: %q", rem)
	}
	did, _, err = sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 1 || read(t, jsonPath) != shadow {
		t.Fatalf("unregister with yaml: did=%q err=%v perm=%s", did, err, read(t, jsonPath))
	}
}

// In Kiro's Cedar policy engine, deny and ask take precedence over allow. Join reports an
// approvalNote instead of claiming tools won't prompt when a matching deny/ask rule exists.
func TestRegisterKiroWithBlockingJSONRule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		wantNote string
	}{
		{"deny all mcp", `{"rules": [{"capability": "mcp", "match": ["*"], "effect": "deny"}]}`, `"deny"`},
		{"ask murmur", `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "ask"}]}`, `"ask"`},
		{"deny all unconditional", `{"rules": [{"capability": "all", "effect": "deny"}]}`, `"deny"`},
		{"ask excluding murmur allows adding rule", `{"rules": [{"capability": "mcp", "match": ["*"], "exclude": ["murmur/*"], "effect": "ask"}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])
			permPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")
			write(t, permPath, tc.body+"\n")

			did, _, err := sb.registerKiroAt(h)
			if tc.wantNote != "" {
				var note approvalNote
				if !errors.As(err, &note) || !strings.Contains(string(note), tc.wantNote) || len(did) != 1 {
					t.Fatalf("did=%q err=%v, want note containing %s", did, err, tc.wantNote)
				}
				if read(t, permPath) != tc.body+"\n" {
					t.Errorf("permissions.json changed despite blocking rule:\n%s", read(t, permPath))
				}
			} else {
				if err != nil || len(did) != 2 || !strings.Contains(read(t, permPath), `"effect": "allow"`) {
					t.Fatalf("excluded ask rule: did=%q err=%v\n%s", did, err, read(t, permPath))
				}
			}
		})
	}
}

func TestRegisterKiroHandlesMalformedPermissionsJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"comment", "{\n  // not plain json\n  \"rules\": []\n}\n"},
		{"rules not a list", `{"rules": {}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])
			permPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")
			write(t, permPath, tc.body)

			did, fresh, err := sb.registerKiroAt(h)
			var note approvalNote
			if !errors.As(err, &note) || !fresh || len(did) != 1 {
				t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
			}
			if read(t, permPath) != tc.body {
				t.Errorf("permissions.json was modified:\n%s", read(t, permPath))
			}
		})
	}
}

func TestRegisterKiroExistingBroadAllowRule(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	permPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")

	// A rule with exclude doesn't cover murmur/* unambiguously, so join adds ours.
	write(t, permPath, `{"rules": [{"capability": "mcp", "match": ["*"], "exclude": ["murmur/*"], "effect": "allow"}]}`+"\n")
	if _, _, err := sb.registerKiroAt(h); err != nil {
		t.Fatalf("with exclude: %v", err)
	}
	if strings.Count(read(t, permPath), kiroAllowPattern) != 2 {
		t.Errorf("expected murmur/* allow rule added beside excluded rule:\n%s", read(t, permPath))
	}

	// An unconditional MCP allow rule already covers murmur/*, so join doesn't add a duplicate.
	broad := `{"rules": [{"capability": "mcp", "match": ["*"], "effect": "allow"}]}` + "\n"
	write(t, permPath, broad)
	if did, _, err := sb.registerKiroAt(h); err != nil || len(did) != 0 || read(t, permPath) != broad {
		t.Errorf("broad rule: did=%q err=%v\n%s", did, err, read(t, permPath))
	}
}

func TestUnregisterKiroContinuesAfterMalformedFile(t *testing.T) {
	h := agentHome{Dir: t.TempDir(), Label: "Kiro"}
	mcpPath := filepath.Join(h.Dir, "settings", "mcp.json")
	permPath := filepath.Join(h.Dir, "settings", "permissions.json")
	write(t, mcpPath, "{")
	write(t, permPath, `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "allow"}]}`)

	did, _, err := (Env{}).unregisterKiroAt(context.Background(), []agentHome{h})
	if err == nil || !strings.Contains(err.Error(), mcpPath) || len(did) != 1 {
		t.Fatalf("unregister: did=%q err=%v", did, err)
	}
	if read(t, mcpPath) != "{" || strings.Contains(read(t, permPath), kiroAllowPattern) {
		t.Errorf("did not clean valid file independently: perm=%s", read(t, permPath))
	}
}

func TestKiroT3InstanceAndScratchIsolation(t *testing.T) {
	sb := scratchSandbox(t)
	real := filepath.Join(sb.root, "real")
	sb.T3Settings = filepath.Join(sb.Home, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, `{
  "providerInstances": {
    "kiro_off": {"driver": "kiro", "displayName": "Kiro Off"},
    "kiro_bad_env": {"driver": "kiro", "displayName": "Kiro Bad Env", "enabled": true,
      "config": {"homePath": "`+filepath.ToSlash(filepath.Join(real, "ignored-homepath"))+`"},
      "environment": [{"name": "KIRO_HOME", "value": "~"}]},
    "kiro_on": {"driver": "kiro", "displayName": "Kiro On", "enabled": true,
      "environment": [{"name": "KIRO_HOME", "value": "`+filepath.ToSlash(filepath.Join(real, "kiro-ext"))+`"}]}
  }
}`)
	byAgent, notes := sb.homes()
	note := strings.Join(notes, "\n")
	if !strings.Contains(note, `Kiro as T3 Code's "Kiro Off" is turned off`) {
		t.Errorf("kiro should default to off in T3:\n%s", note)
	}
	if !strings.Contains(note, "Kiro (Kiro On in T3 Code)") || !strings.Contains(note, sb.T3Settings) {
		t.Errorf("scratch isolation should leave external T3 Kiro home alone:\n%s", note)
	}
	// kiro_bad_env has config.homePath (which T3's Kiro driver ignores) and KIRO_HOME=~ (which
	// Kiro V3 rejects because it equals home), so it must resolve to <Home>/.kiro.
	kiroHomes := byAgent[agentIndex("kiro")]
	foundDefault := false
	for _, h := range kiroHomes {
		if h.Dir != "" && strings.HasPrefix(h.Dir, real) {
			t.Errorf("external or ignored Kiro home was not filtered: %+v", h)
		}
		if samePath(h.Dir, filepath.Join(sb.Home, ".kiro")) {
			foundDefault = true
		}
	}
	if !foundDefault {
		t.Errorf("kiro_bad_env did not fall back to ~/.kiro: %+v", kiroHomes)
	}
}

// A link at settings/, settings/mcp.json, or settings/permissions.json inside a scratch Kiro home
// pointing outside the temporary folder must be caught by keepToScratch (#995).
func TestKiroScratchNestedLinkOutIsOutside(t *testing.T) {
	for _, kind := range []string{"settings-dir", "settings-junction", "mcp-file", "perm-file"} {
		t.Run(kind, func(t *testing.T) {
			sb := scratchSandbox(t)
			real := filepath.Join(sb.root, "real")
			mustMakeDir(t, real)
			mustMakeDir(t, sb.KiroHome)
			switch kind {
			case "settings-dir":
				linkOrSkip(t, filepath.Join(real, "settings"), filepath.Join(sb.KiroHome, "settings"))
			case "settings-junction":
				if _, err := exec.LookPath("cmd"); err != nil {
					t.Skip("junctions are Windows'")
				}
				target := filepath.Join(real, "settings")
				mustMakeDir(t, target)
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(sb.KiroHome, "settings"), target).CombinedOutput(); err != nil {
					t.Fatalf("mklink /J: %v\n%s", err, out)
				}
			case "mcp-file":
				mustMakeDir(t, filepath.Join(sb.KiroHome, "settings"))
				linkOrSkip(t, filepath.Join(real, "mcp.json"), filepath.Join(sb.KiroHome, "settings", "mcp.json"))
			case "perm-file":
				mustMakeDir(t, filepath.Join(sb.KiroHome, "settings"))
				linkOrSkip(t, filepath.Join(real, "permissions.json"), filepath.Join(sb.KiroHome, "settings", "permissions.json"))
			}
			byAgent, notes := sb.homes()
			if got := dirsOf(byAgent[agentIndex("kiro")]); len(got) != 1 || got[0] != "" {
				t.Errorf("%s: kiro homes = %q, want filtered out", kind, got)
			}
			if note := strings.Join(notes, "\n"); !strings.Contains(note, "Kiro in "+sb.KiroHome) {
				t.Errorf("%s: note lacks Kiro entry:\n%s", kind, note)
			}
		})
	}
}
