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
	hookPath := filepath.Join(sb.KiroHome, "hooks", kiroHookFile)
	write(t, mcpPath, "{\n  // user server\n  \"mcpServers\": {\n    \"other\": {\"command\": \"o\", \"args\": []}\n  }\n}\n")

	did, fresh, err := sb.registerKiroAt(h)
	if err != nil || !fresh || len(did) != 3 {
		t.Fatalf("first register: did=%q fresh=%v err=%v", did, fresh, err)
	}
	mcpText := read(t, mcpPath)
	for _, want := range []string{"// user server", `"other"`, `"` + brand.MCPServer + `"`, `"--agent"`, `"kiro"`} {
		if !strings.Contains(mcpText, want) {
			t.Errorf("mcp.json lacks %s:\n%s", want, mcpText)
		}
	}
	hookText := read(t, hookPath)
	var hookDoc kiroHookDoc
	if err := json.Unmarshal([]byte(hookText), &hookDoc); err != nil {
		t.Fatalf("hooks/murmur.json isn't valid JSON (%v):\n%s", err, hookText)
	}
	if hookDoc.Version != "v1" || len(hookDoc.Hooks) != 2 ||
		hookDoc.Hooks[0].Trigger != "UserPromptSubmit" || hookDoc.Hooks[1].Trigger != "Stop" ||
		hookDoc.Hooks[0].Action.Type != "command" || !strings.HasSuffix(hookDoc.Hooks[0].Action.Command, " hook kiro") {
		t.Errorf("unexpected hooks/murmur.json: %+v", hookDoc)
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
	if err != nil || fresh || len(did) != 0 || read(t, mcpPath) != mcpText || read(t, permPath) != permText || read(t, hookPath) != hookText {
		t.Errorf("second register: did=%q fresh=%v err=%v", did, fresh, err)
	}

	// Repointing a moved binary updates both mcp.json and hooks/murmur.json without marking fresh.
	sb.Exe = filepath.Join(sb.root, "bin2", exeName("mur"))
	did, fresh, err = sb.registerKiroAt(h)
	if err != nil || fresh || len(did) != 2 || !strings.Contains(read(t, mcpPath), "bin2") || !strings.Contains(read(t, hookPath), "bin2") {
		t.Errorf("repoint register: did=%q fresh=%v err=%v\n%s\n%s", did, fresh, err, read(t, mcpPath), read(t, hookPath))
	}

	// Manual removal describes mcp.json, hooks/murmur.json, and permissions.json.
	if rem := kiroRemoval(sb.Env, h); !strings.Contains(rem, mcpPath) || !strings.Contains(rem, "delete "+hookPath) || !strings.Contains(rem, `remove "murmur/*" from the MCP allow rule in `+permPath) {
		t.Errorf("kiroRemoval = %q", rem)
	}

	// Uninstall removes all three while keeping the user's other server and valid "rules": [].
	did, _, err = sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 3 {
		t.Fatalf("unregister: did=%q err=%v", did, err)
	}
	if exists(hookPath) {
		t.Errorf("hooks/murmur.json still exists after uninstall")
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

// A hook written by a renamed Murmur binary (e.g. mur-dev.exe) is self-identifying via
// kiroHookDescription, so it is cleanly replaced when standard mur or another renamed binary
// joins later — even across partial MCP or hook write failures — and removed on uninstall even
// without settings/mcp.json.
func TestKiroRenamedBinaryHookReplacedAndRemoved(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	hookPath := filepath.Join(sb.KiroHome, "hooks", kiroHookFile)
	mcpPath := filepath.Join(sb.KiroHome, "settings", "mcp.json")

	devExe := filepath.Join(sb.root, "dev", exeName("mur-dev"))
	previewExe := filepath.Join(sb.root, "preview", exeName("mur-preview"))
	stdExe := sb.Exe
	sb.Exe = devExe
	if did, _, err := sb.registerKiroAt(h); err != nil || len(did) != 3 {
		t.Fatalf("dev register: did=%q err=%v", did, err)
	}
	if !strings.Contains(read(t, hookPath), "mur-dev") || !strings.Contains(read(t, hookPath), kiroHookDescription(kiroHookCommand(devExe))) {
		t.Fatalf("expected mur-dev and description marker in hook file:\n%s", read(t, hookPath))
	}

	// Standard mur sees the dev hook in manual removal and replaces it on join.
	sb.Exe = stdExe
	if rem := kiroRemoval(sb.Env, h); !strings.Contains(rem, "delete "+hookPath) {
		t.Errorf("kiroRemoval for renamed binary = %q", rem)
	}
	if did, _, err := sb.registerKiroAt(h); err != nil || len(did) != 2 || strings.Contains(read(t, hookPath), "mur-dev") {
		t.Fatalf("std register over dev hook: did=%q err=%v\n%s", did, err, read(t, hookPath))
	}

	// Reinstall with mur-dev, then uninstall with standard mur even after mcp.json is gone or
	// points elsewhere: self-identifying hook must still be removed cleanly.
	sb.Exe = devExe
	if _, _, err := sb.registerKiroAt(h); err != nil {
		t.Fatalf("re-register dev: %v", err)
	}
	if err := os.Remove(mcpPath); err != nil {
		t.Fatal(err)
	}
	sb.Exe = stdExe
	if did, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h}); err != nil || len(did) != 2 || len(notes) != 0 || exists(hookPath) {
		t.Fatalf("std uninstall of dev hook without mcp.json: did=%q notes=%q err=%v exists=%v", did, notes, err, exists(hookPath))
	}

	// Switching between two renamed binaries (mur-dev -> mur-preview) when either mcp.json or
	// hooks/murmur.json fails never strands the hook from later standard mur join or uninstall.
	sb.Exe = devExe
	if _, _, err := sb.registerKiroAt(h); err != nil {
		t.Fatalf("re-register dev 2: %v", err)
	}
	// Failure 1: mcp.json is malformed during mur-preview join -> hook still points to mur-dev
	// and standard mur uninstall still removes it.
	write(t, mcpPath, "{malformed")
	sb.Exe = previewExe
	if _, _, err := sb.registerKiroAt(h); err == nil {
		t.Fatal("expected preview register failure on malformed mcp.json")
	}
	sb.Exe = stdExe
	if rem := kiroRemoval(sb.Env, h); !strings.Contains(rem, "delete "+hookPath) {
		t.Errorf("kiroRemoval after failed mcp update = %q", rem)
	}
	// Restore valid mcp.json, then block hooks/murmur.json during mur-preview join (Failure 2):
	// mcp.json updates to mur-preview while hook stays mur-dev; once unblocked, standard mur
	// still recognizes and replaces the mur-dev hook.
	if err := os.Remove(mcpPath); err != nil {
		t.Fatal(err)
	}
	origHook := read(t, hookPath)
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	mustMakeDir(t, hookPath)
	sb.Exe = previewExe
	if _, _, err := sb.registerKiroAt(h); err == nil {
		t.Fatal("expected preview register failure while hookPath is blocked")
	}
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}
	write(t, hookPath, origHook)
	sb.Exe = stdExe
	if did, _, err := sb.registerKiroAt(h); err != nil || len(did) != 2 || strings.Contains(read(t, hookPath), "mur-dev") {
		t.Fatalf("std register after partial preview failure: did=%q err=%v\n%s", did, err, read(t, hookPath))
	}

	// writeOwnedFile refuses to overwrite a symlink or a foreign regular file, and createOwnedFile
	// refuses to overwrite an existing symlink or file.
	linkTarget := filepath.Join(sb.root, "target.json")
	write(t, linkTarget, "keep")
	linkPath := filepath.Join(sb.root, "link.json")
	linkOrSkip(t, linkTarget, linkPath)
	if err := writeOwnedFile(linkPath, []byte("new"), 0o644, isKiroHookFile); err == nil {
		t.Fatal("writeOwnedFile overwrote or replaced a symlink")
	}
	if err := createOwnedFile(linkPath, []byte("new"), 0o644); err == nil {
		t.Fatal("createOwnedFile overwrote or replaced a symlink")
	}
	if !isLink(linkPath) || read(t, linkTarget) != "keep" {
		t.Errorf("owned writers disturbed symlink or target: isLink=%v target=%q", isLink(linkPath), read(t, linkTarget))
	}
	foreignPath := filepath.Join(sb.root, "foreign.json")
	write(t, foreignPath, `{"version":"v1","hooks":[]}`)
	if err := createOwnedFile(foreignPath, []byte("new"), 0o644); err == nil || read(t, foreignPath) != `{"version":"v1","hooks":[]}` {
		t.Errorf("createOwnedFile overwrote existing file: err=%v body=%q", err, read(t, foreignPath))
	}
	if err := writeOwnedFile(foreignPath, []byte("new"), 0o644, isKiroHookFile); err == nil || read(t, foreignPath) != `{"version":"v1","hooks":[]}` {
		t.Errorf("writeOwnedFile overwrote foreign file: err=%v body=%q", err, read(t, foreignPath))
	}
}

// Because Kiro's addUserRule merges single-match rules of the same capability and effect into one
// rule's match array, uninstall removes only murmur/* and preserves the user's other patterns.
func TestUnregisterKiroPreservesMergedAllowPatterns(t *testing.T) {
	h := agentHome{Dir: t.TempDir(), Label: "Kiro"}
	permPath := filepath.Join(h.Dir, "settings", "permissions.json")
	write(t, permPath, "{\n  \"rules\": [\n    {\"capability\": \"mcp\", \"match\": [\"murmur/*\", \"github/*\"], \"effect\": \"allow\"},\n    {\"capability\": \"shell\", \"match\": [\"git status\"], \"effect\": \"allow\"}\n  ]\n}\n")

	if err := recordRule(permPath, kiroAllowPattern); err != nil { // join added it
		t.Fatal(err)
	}

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
	if !errors.As(err, &note) || !fresh || len(did) != 2 {
		t.Fatalf("with yaml: did=%q fresh=%v err=%v", did, fresh, err)
	}
	if exists(jsonPath) {
		t.Error("created permissions.json alongside permissions.yaml")
	}

	// Even if a fallback permissions.json exists with murmur/*, uninstall leaves it alone with a note.
	shadow := `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "allow"}]}` + "\n"
	write(t, jsonPath, shadow)
	if rem := kiroRemoval(sb.Env, h); strings.Contains(rem, jsonPath) {
		t.Errorf("kiroRemoval named shadowed permissions.json: %q", rem)
	}
	did, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 2 || read(t, jsonPath) != shadow {
		t.Fatalf("unregister with yaml: did=%q err=%v perm=%s", did, err, read(t, jsonPath))
	}
	if got := strings.Join(notes, "\n"); !strings.Contains(got, jsonPath) || !strings.Contains(got, yamlPath) {
		t.Errorf("unregister with shadowed permissions.json lacked note: %q", notes)
	}
}

// In Kiro's Cedar policy engine, deny and ask take precedence over allow. Join skips adding a dead
// allow rule when all Murmur tools are blocked; when only some tools are blocked (e.g. murmur/send),
// join still adds the allow rule for the unaffected tools and reports the overriding rule via an
// approvalNote.
func TestRegisterKiroWithBlockingJSONRule(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantNotes []string
		wantAllow bool
	}{
		{"deny all mcp", `{"rules": [{"capability": "mcp", "match": ["*"], "effect": "deny"}]}`, []string{`"deny"`}, false},
		{"ask murmur", `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "ask"}]}`, []string{`"ask"`}, false},
		{"deny single tool send", `{"rules": [{"capability": "mcp", "match": ["murmur/send"], "effect": "deny"}]}`, []string{`"deny" rule covering murmur/send`}, true},
		{"ask single tool mute", `{"rules": [{"capability": "mcp", "match": ["murmur/mute"], "effect": "ask"}]}`, []string{`"ask" rule covering murmur/mute`}, true},
		{"mixed deny send and ask mute", `{"rules": [{"capability": "mcp", "match": ["murmur/send"], "effect": "deny"}, {"capability": "mcp", "match": ["murmur/mute"], "effect": "ask"}]}`, []string{`"deny" rule covering murmur/send`, `"ask" rule covering murmur/mute`}, true},
		{"deny all unconditional", `{"rules": [{"capability": "all", "effect": "deny"}]}`, []string{`"deny"`}, false},
		{"ask excluding murmur allows adding rule", `{"rules": [{"capability": "mcp", "match": ["*"], "exclude": ["murmur/*"], "effect": "ask"}]}`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])
			permPath := filepath.Join(sb.KiroHome, "settings", "permissions.json")
			write(t, permPath, tc.body+"\n")

			did, _, err := sb.registerKiroAt(h)
			if len(tc.wantNotes) > 0 {
				var note approvalNote
				if !errors.As(err, &note) {
					t.Fatalf("did=%q err=%v, want approvalNote", did, err)
				}
				for _, want := range tc.wantNotes {
					if !strings.Contains(string(note), want) {
						t.Errorf("note = %q, want substring %q", string(note), want)
					}
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			gotPerm := read(t, permPath)
			if tc.wantAllow {
				if len(did) != 3 || !strings.Contains(gotPerm, `"effect": "allow"`) {
					t.Fatalf("expected allow rule added: did=%q\n%s", did, gotPerm)
				}
				// Rerun does not duplicate the allow rule.
				if did2, _, _ := sb.registerKiroAt(h); len(did2) != 0 || read(t, permPath) != gotPerm {
					t.Errorf("rerun modified permissions.json: did=%q\n%s", did2, read(t, permPath))
				}
			} else {
				if len(did) != 2 || gotPerm != tc.body+"\n" {
					t.Errorf("expected permissions.json unchanged: did=%q\n%s", did, gotPerm)
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
			if !errors.As(err, &note) || !fresh || len(did) != 2 {
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
	if err := recordRule(permPath, kiroAllowPattern); err != nil { // join added it
		t.Fatal(err)
	}

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
    "kiro_padded": {"driver": "kiro", "displayName": "Kiro Padded", "enabled": true,
      "environment": [{"name": "KIRO_HOME", "value": "  `+filepath.ToSlash(filepath.Join(sb.Home, "kiro-padded"))+`  "}]},
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
	// kiro_padded has whitespace around an absolute path inside the scratch home and must trim it.
	kiroHomes := byAgent[agentIndex("kiro")]
	foundDefault, foundPadded := false, false
	for _, h := range kiroHomes {
		if h.Dir != "" && strings.HasPrefix(h.Dir, real) {
			t.Errorf("external or ignored Kiro home was not filtered: %+v", h)
		}
		if samePath(h.Dir, filepath.Join(sb.Home, ".kiro")) {
			foundDefault = true
		}
		if samePath(h.Dir, filepath.Join(sb.Home, "kiro-padded")) {
			foundPadded = true
		}
	}
	if !foundDefault || !foundPadded {
		t.Errorf("foundDefault=%v foundPadded=%v, homes=%+v", foundDefault, foundPadded, kiroHomes)
	}
}

// A link at settings/, settings/mcp.json, or settings/permissions.json inside a scratch Kiro home
// pointing outside the temporary folder (including via a linked directory followed by `..`) must
// be caught by keepToScratch (#995).
func TestKiroScratchNestedLinkOutIsOutside(t *testing.T) {
	for _, kind := range []string{"settings-dir", "settings-junction", "mcp-file", "perm-file", "mcp-dotdot-via-link"} {
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
			case "mcp-dotdot-via-link":
				settings := filepath.Join(sb.KiroHome, "settings")
				mustMakeDir(t, settings)
				sub := filepath.Join(real, "sub")
				mustMakeDir(t, sub)
				linkOrSkip(t, sub, filepath.Join(settings, "alias"))
				sep := string(filepath.Separator)
				linkOrSkip(t, "alias"+sep+".."+sep+"mcp.json", filepath.Join(settings, "mcp.json"))
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

// Drive-rooted paths on Windows (e.g. `\profiles\codex\config.toml`, where filepath.IsAbs is
// false) must keep their leading separator through resolved/resolveLinks.
func TestResolveLinksPreservesDriveRootedPath(t *testing.T) {
	sep := string(filepath.Separator)
	p := sep + "profiles" + sep + "codex" + sep + "config.toml"
	if got := resolveLinks(p); !strings.HasPrefix(got, sep) {
		t.Errorf("resolveLinks(%q) = %q, lost leading separator", p, got)
	}
}

// A foreign or customized hooks/murmur.json is left alone on both join (as a real Problem, not
// downgraded to an approvalNote even when permissions.yaml is present) and uninstall (with a note),
// and is omitted from manual removal instructions.
func mustKiroHooks(t *testing.T, cmd string) string {
	t.Helper()
	b, err := kiroHooks(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestKiroForeignHookFileLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "unrelated hook",
			body: `{"version": "v1", "hooks": [{"name": "mine", "trigger": "Stop", "action": {"type": "command", "command": "echo hi"}}]}` + "\n",
		},
		{
			name: "custom single murmur-* hook on another trigger",
			body: `{"version": "v1", "hooks": [{"name": "murmur-audit", "trigger": "PreToolUse", "action": {"type": "command", "command": "my-wrapper hook kiro"}}]}` + "\n",
		},
		{
			name: "two hooks with unexpected trigger",
			body: `{"version": "v1", "hooks": [{"name": "murmur-prompt", "trigger": "PreToolUse", "action": {"type": "command", "command": "mur hook kiro"}, "timeout": 30}, {"name": "murmur-stop", "trigger": "Stop", "action": {"type": "command", "command": "mur hook kiro"}, "timeout": 30}]}` + "\n",
		},
		{
			name: "customized timeout zero",
			body: strings.Replace(mustKiroHooks(t, "mur hook kiro"), `"timeout": 30`, `"timeout": 0`, 1),
		},
		{
			name: "customized timeout omitted",
			body: strings.Replace(mustKiroHooks(t, "mur hook kiro"), ",\n      \"timeout\": 30", "", 1),
		},
		{
			name: "customized command keeping original murmur description digest",
			body: strings.ReplaceAll(mustKiroHooks(t, `'/usr/local/bin/mur' hook kiro`), `'/usr/local/bin/mur' hook kiro`, `'/usr/local/bin/my-wrapper' hook kiro`),
		},
		{
			name: "wrapper binary without murmur description",
			body: `{"version": "v1", "hooks": [{"name": "murmur-prompt", "trigger": "UserPromptSubmit", "action": {"type": "command", "command": "my-wrapper hook kiro"}, "timeout": 30}, {"name": "murmur-stop", "trigger": "Stop", "action": {"type": "command", "command": "my-wrapper hook kiro"}, "timeout": 30}]}` + "\n",
		},
		{
			name: "env wrapper prefix around mur",
			body: mustKiroHooks(t, "env FOO=1 /usr/bin/mur hook kiro"),
		},
		{
			name: "extra flag before hook kiro",
			body: mustKiroHooks(t, `"C:\bin\mur.exe" --debug hook kiro`),
		},
		{
			name: "mismatched commands across hooks",
			body: strings.Replace(mustKiroHooks(t, `'/a/mur' hook kiro`), `'/a/mur'`, `'/b/mur'`, 1),
		},
		{
			name: "extra user field",
			body: strings.Replace(mustKiroHooks(t, "mur hook kiro"), `"trigger": "UserPromptSubmit",`, `"trigger": "UserPromptSubmit", "enabled": false,`, 1),
		},
		{
			name: "duplicate case-folded timeout key",
			body: strings.Replace(mustKiroHooks(t, "mur hook kiro"), `"timeout": 30`, "\"timeout\": 60,\n      \"Timeout\": 30", 1),
		},
		{
			name: "trailing json token",
			body: mustKiroHooks(t, "mur hook kiro") + "{}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])
			hookPath := filepath.Join(sb.KiroHome, "hooks", kiroHookFile)
			yamlPath := filepath.Join(sb.KiroHome, "settings", "permissions.yaml")
			write(t, hookPath, tc.body)
			write(t, yamlPath, "rules: []\n")

			_, _, err := sb.registerKiroAt(h)
			var note approvalNote
			if err == nil || errors.As(err, &note) || !strings.Contains(err.Error(), hookPath) {
				t.Fatalf("expected non-approvalNote error for foreign hook, got %v", err)
			}
			if read(t, hookPath) != tc.body {
				t.Error("overwrote foreign hooks/murmur.json")
			}
			if rem := kiroRemoval(sb.Env, h); strings.Contains(rem, hookPath) {
				t.Errorf("kiroRemoval named foreign hook file: %q", rem)
			}
			_, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h})
			if err != nil || read(t, hookPath) != tc.body || !strings.Contains(strings.Join(notes, "\n"), hookPath) {
				t.Errorf("unregister with foreign hook: err=%v notes=%q body=%q", err, notes, read(t, hookPath))
			}
		})
	}
}

// Join, uninstall, and manual removal all leave a symlinked hooks/murmur.json alone (both live
// symlinks and dangling symlinks), matching removeOwned's policy for user-owned links.
func TestKiroSymlinkHookLeftAlone(t *testing.T) {
	for _, dangling := range []bool{false, true} {
		name := "live"
		if dangling {
			name = "dangling"
		}
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			mustMakeDir(t, sb.KiroHome)
			h := sb.defaultHome(agents[agentIndex("kiro")])

			target := filepath.Join(sb.root, "user-owned-murmur.json")
			var origTarget string
			if !dangling {
				want, err := kiroHooks(kiroHookCommand(filepath.Join(sb.root, "old", exeName("mur"))))
				if err != nil {
					t.Fatal(err)
				}
				origTarget = string(want)
				write(t, target, origTarget)
			}
			hookPath := filepath.Join(sb.KiroHome, "hooks", kiroHookFile)
			mustMakeDir(t, filepath.Dir(hookPath))
			linkOrSkip(t, target, hookPath)

			// Join must refuse to write through the symlink (and must not create a missing target).
			if _, _, err := sb.registerKiroAt(h); err == nil || !strings.Contains(err.Error(), hookPath) {
				t.Fatalf("register through %s symlink: err=%v, want error naming %s", name, err, hookPath)
			}
			if dangling && exists(target) {
				t.Error("join created target through dangling symlink")
			}
			if !dangling && read(t, target) != origTarget {
				t.Error("join overwrote target through live symlink")
			}

			// Manual removal omits the link; uninstall preserves it with a note.
			if rem := kiroRemoval(sb.Env, h); strings.Contains(rem, hookPath) {
				t.Errorf("kiroRemoval named %s symlinked hook file: %q", name, rem)
			}
			did, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h})
			if err != nil {
				t.Fatalf("unregister %s symlink hook: did=%q notes=%q err=%v", name, did, notes, err)
			}
			if _, lerr := os.Lstat(hookPath); lerr != nil {
				t.Errorf("removed %s symlinked hook file: %v", name, lerr)
			}
			if !strings.Contains(strings.Join(notes, "\n"), hookPath) {
				t.Errorf("expected note leaving %s symlinked hook alone, got %q", name, notes)
			}
		})
	}
}

// cmdQuotePath quotes Windows paths for cmd.exe so spaces, metacharacters, and %VAR% stay literal
// without expanding environment variables.
func TestCmdQuotePathEscapesPercent(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\with space\mur.exe`, `"C:\with space\mur.exe"`},
		{`C:\tools\%USERNAME%\mur.exe`, `C:\tools\^%"USERNAME"^%"\mur.exe"`},
		{`C:\my tools & (1)\%USERNAME%\mur.exe`, `C:\my" tools & (1)\"^%"USERNAME"^%"\mur.exe"`},
	} {
		if got := cmdQuotePath(tc.in); got != tc.want {
			t.Errorf("cmdQuotePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if !isKiroHookCommand(tc.want + " hook kiro") {
			t.Errorf("isKiroHookCommand rejected %q", tc.want+" hook kiro")
		}
	}

	// Execute a real command through both Kiro's shell (Node's cmd /d /s /c "..." on Windows, sh
	// elsewhere) and Antigravity's shell (cmd /d /c on Windows) when a folder literally contains
	// %USERNAME% with spaces and `&`, verifying %USERNAME% is not expanded.
	dir := filepath.Join(t.TempDir(), "my tools & (1)", "%USERNAME%")
	mustMakeDir(t, dir)
	script, body := filepath.Join(dir, "mur"), "#!/bin/sh\nprintf 'ok:%s\\n' \"$*\"\n"
	if os.PathSeparator == '\\' {
		script, body = filepath.Join(dir, "mur.cmd"), "@echo ok:%*\r\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmdLine := kiroHookCommand(script)
	for _, run := range []struct {
		name string
		fn   func(string) *exec.Cmd
	}{
		{"kiro-shell", kiroShellCmd},
		{"antigravity-shell", antigravityShellCmd},
	} {
		out, err := run.fn(cmdLine).Output()
		if err != nil || strings.TrimSpace(string(out)) != "ok:hook kiro" {
			t.Fatalf("%s kiroHookCommand(%q) -> %q printed %q, err=%v", run.name, script, cmdLine, out, err)
		}
	}
	if os.PathSeparator == '\\' {
		// Also test the quoted path directly (bypassing shortPath when 8.3 names exist on Windows),
		// with both a .cmd script and a copied native .exe under Kiro's cmd /d /s /c "..." and
		// Antigravity's cmd /d /c (including PreToolUse's JSON fallback).
		quoted := cmdQuotePath(script) + " hook kiro"
		for _, run := range []struct {
			name string
			fn   func(string) *exec.Cmd
		}{
			{"kiro-shell", kiroShellCmd},
			{"antigravity-shell", antigravityShellCmd},
		} {
			out, err := run.fn(quoted).Output()
			if err != nil || strings.TrimSpace(string(out)) != "ok:hook kiro" {
				t.Fatalf("%s cmdQuotePath(%q) -> %q printed %q, err=%v", run.name, script, quoted, out, err)
			}
		}
		withFallback := cmdQuotePath(script) + ` hook --event PreToolUse antigravity || echo {"decision":"allow"}`
		out, err := antigravityShellCmd(withFallback).Output()
		if err != nil || strings.TrimSpace(string(out)) != "ok:hook --event PreToolUse antigravity" {
			t.Fatalf("fallback %q printed %q, err=%v", withFallback, out, err)
		}
		missingFallback := cmdQuotePath(script+".missing") + ` hook --event PreToolUse antigravity || echo {"decision":"allow"}`
		out, err = antigravityShellCmd(missingFallback).Output()
		if err != nil || strings.TrimSpace(string(out)) != `{"decision":"allow"}` {
			t.Fatalf("missing fallback %q printed %q, err=%v", missingFallback, out, err)
		}

		// Native .exe under Kiro's Node launch path (cmd /d /s /c "...") in both a %USERNAME%
		// directory and a space/& directory without %.
		whereBin, err := exec.LookPath("where.exe")
		if err != nil {
			t.Fatalf("where.exe not found: %v", err)
		}
		exeBytes, err := os.ReadFile(whereBin)
		if err != nil {
			t.Fatal(err)
		}
		for _, sub := range []string{dir, filepath.Join(t.TempDir(), "my tools & (1)")} {
			mustMakeDir(t, sub)
			exeCopy := filepath.Join(sub, "mur.exe")
			if err := os.WriteFile(exeCopy, exeBytes, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, cmd := range []string{
				cmdHookCommand(exeCopy, " cmd.exe", "", "", false),
				cmdQuotePath(exeCopy) + " cmd.exe",
			} {
				out, err := kiroShellCmd(cmd).Output()
				if err != nil || !strings.Contains(strings.ToLower(string(out)), "cmd.exe") {
					t.Fatalf("kiroShellCmd native exe (%q) printed %q, err=%v", cmd, out, err)
				}
			}
		}
	}
}

// A nested link at hooks/ or skills/ inside a scratch Kiro home pointing outside temp is caught
// by keepToScratch (#995).
func TestKiroScratchNestedHookAndSkillLinks(t *testing.T) {
	for _, kind := range []string{"hooks-junction", "skills-junction", "hooks-file", "skills-file"} {
		t.Run(kind, func(t *testing.T) {
			sb := scratchSandbox(t)
			real := filepath.Join(sb.root, "real")
			mustMakeDir(t, real)
			mustMakeDir(t, sb.KiroHome)
			switch kind {
			case "hooks-junction":
				if _, err := exec.LookPath("cmd"); err != nil {
					t.Skip("junctions are Windows'")
				}
				target := filepath.Join(real, "hooks")
				mustMakeDir(t, target)
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(sb.KiroHome, "hooks"), target).CombinedOutput(); err != nil {
					t.Fatalf("mklink /J: %v\n%s", err, out)
				}
			case "skills-junction":
				if _, err := exec.LookPath("cmd"); err != nil {
					t.Skip("junctions are Windows'")
				}
				target := filepath.Join(real, "skills")
				mustMakeDir(t, target)
				if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(sb.KiroHome, "skills"), target).CombinedOutput(); err != nil {
					t.Fatalf("mklink /J: %v\n%s", err, out)
				}
			case "hooks-file":
				mustMakeDir(t, filepath.Join(sb.KiroHome, "hooks"))
				linkOrSkip(t, filepath.Join(real, kiroHookFile), filepath.Join(sb.KiroHome, "hooks", kiroHookFile))
			case "skills-file":
				mustMakeDir(t, filepath.Join(sb.KiroHome, "skills", "murmur"))
				linkOrSkip(t, filepath.Join(real, "SKILL.md"), filepath.Join(sb.KiroHome, "skills", "murmur", "SKILL.md"))
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
