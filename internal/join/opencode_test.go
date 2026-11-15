package join

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/jsonedit"
)

// withOpenCode puts a stand-in opencode on the sandbox's PATH, which says version when asked; ""
// says nothing a version can be read from (it fails).
func withOpenCode(t *testing.T, sb *sandbox, version string) {
	t.Helper()
	bin := filepath.Join(sb.root, "bin", exeName("opencode"))
	look := sb.LookPath
	sb.LookPath = func(name string) (string, error) {
		if name == "opencode" {
			return bin, nil
		}
		return look(name)
	}
	sb.Run = func(_ context.Context, name string, args ...string) (string, error) {
		if name != bin || len(args) != 1 || args[0] != "--version" {
			t.Errorf("ran %s %q", name, args)
		}
		if version == "" {
			return "", errors.New("exit status 1")
		}
		return version + "\n", nil
	}
}

func openCodeHome(sb *sandbox) agentHome { return sb.defaultHome(agents[agentIndex("opencode")]) }

// value is the value at path in a config, as plain JSON, "" where it isn't there.
func value(t *testing.T, text string, path ...string) string {
	t.Helper()
	raw, found, err := jsonedit.GetIn(text, path)
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(path, "."), err, text)
	}
	if !found {
		return ""
	}
	return strings.Join(strings.Fields(raw), "")
}

// OpenCode 1 gets mcp.murmur with "enabled", and its tools allowed in "permission", last; no
// "permissions", which OpenCode 1 refuses the whole file over, and no mcp.servers (#388).
func TestRegisterOpenCodeV1(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "1.19.5")
	path := filepath.Join(sb.OpenCodeDir, "opencode.json")
	before := "{\n  // mine\n  \"model\": \"x/y\",\n  \"permission\": {\n    \"murmur_*\": \"deny\",\n    \"*\": \"ask\"\n  }\n}\n"
	write(t, path, before)
	did, fresh, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb))
	if err != nil || !fresh || len(did) != 2 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	got := read(t, path)
	cmd := `["` + strings.ReplaceAll(sb.Exe, `\`, `\\`) + `","mcp","--agent","opencode"]`
	if v := value(t, got, "mcp", "murmur"); v != `{"type":"local","command":`+cmd+`,"enabled":true}` {
		t.Errorf("mcp.murmur: %s", v)
	}
	if v := value(t, got, "permission"); v != `{"*":"ask","murmur_*":"allow"}` {
		t.Errorf("permission: %s", v)
	}
	if !strings.HasPrefix(got, "{\n  // mine\n  \"model\": \"x/y\",") || value(t, got, "permissions") != "" || value(t, got, "mcp", "servers") != "" || strings.Contains(got, "codemode") {
		t.Errorf("opencode.json:\n%s", got)
	}
	if _, fresh, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil || fresh || read(t, path) != got {
		t.Errorf("second: fresh=%v err=%v", fresh, err)
	}
}

// A version join can't learn, from an opencode that fails, prints no version, or isn't run at all,
// gets OpenCode 1's shape, which both versions load.
func TestRegisterOpenCodeUnknownVersionIsV1(t *testing.T) {
	for name, set := range map[string]func(*sandbox){
		"fails":      func(sb *sandbox) { withOpenCode(t, sb, "") },
		"no version": func(sb *sandbox) { withOpenCode(t, sb, "opencode dev") },
		"not run": func(sb *sandbox) {
			withOpenCode(t, sb, "opencode v2.2.0")
			sb.Run = nil
		},
		"not on PATH": func(sb *sandbox) {},
	} {
		t.Run(name, func(t *testing.T) {
			sb := newSandbox(t, false)
			withAgents(sb)
			set(sb)
			if err := os.MkdirAll(sb.OpenCodeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
				t.Fatal(err)
			}
			got := read(t, filepath.Join(sb.OpenCodeDir, "opencode.json"))
			if value(t, got, "mcp", "murmur") == "" || value(t, got, "permission", "murmur_*") != `"allow"` || value(t, got, "permissions") != "" {
				t.Errorf("opencode.json:\n%s", got)
			}
		})
	}
}

// opencode2 is OpenCode 2's: alone on PATH, or as the command T3 runs when it says no version.
func TestRegisterOpenCode2Command(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	sb.LookPath = func(name string) (string, error) {
		if name == "opencode2" {
			return filepath.Join(sb.root, "bin", exeName("opencode2")), nil
		}
		return "", exec.ErrNotFound
	}
	if s := sb.openCodeShapeFor(context.Background(), openCodeHome(sb)); !s.v2() {
		t.Error("opencode2 alone on PATH: not OpenCode 2")
	}
	h := openCodeHome(sb)
	h.Bin = filepath.Join(sb.root, "t3", exeName("opencode2"))
	sb.Run = func(context.Context, string, ...string) (string, error) { return "", errors.New("no") }
	if s := sb.openCodeShapeFor(context.Background(), h); !s.v2() {
		t.Error("T3's opencode2: not OpenCode 2")
	}
	// T3's command is the one asked, not PATH's.
	h.Bin = filepath.Join(sb.root, "t3", exeName("opencode"))
	sb.Run = func(_ context.Context, name string, _ ...string) (string, error) {
		if name != h.Bin {
			t.Errorf("asked %s", name)
		}
		return "1.19.5", nil
	}
	if s := sb.openCodeShapeFor(context.Background(), h); s.v2() {
		t.Error("T3's opencode 1.19.5: OpenCode 2")
	}
}

// A config join wrote for OpenCode 2 is made OpenCode 1's when that is what is installed: Murmur's
// server and rule move, what held only them goes, and the user's entries beside them stay.
func TestRegisterOpenCodeMovesV2ToV1(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "1.19.5")
	path := filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	write(t, path, `{
  // mine
  "model": "x/y",
  "mcp": {
    "servers": {
      "murmur": {"type": "local", "command": ["/old/mur", "mcp"], "codemode": false}
    },
    "other": {"type": "local", "command": ["o"]}
  },
  "permissions": [
    {"action": "murmur_*", "resource": "*", "effect": "allow"}
  ]
}
`)
	did, fresh, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb))
	if err != nil || !fresh || len(did) != 3 || !strings.Contains(did[0], "moved") || !strings.Contains(did[0], "OpenCode 1") {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	got := read(t, path)
	if value(t, got, "mcp", "servers") != "" || value(t, got, "permissions") != "" || strings.Contains(got, "/old/mur") {
		t.Errorf("V2 entries left:\n%s", got)
	}
	if value(t, got, "mcp", "other") != `{"type":"local","command":["o"]}` || !strings.Contains(got, "// mine") || value(t, got, "model") != `"x/y"` {
		t.Errorf("the user's entries:\n%s", got)
	}
	if value(t, got, "mcp", "murmur", "enabled") != "true" || value(t, got, "permission") != `{"murmur_*":"allow"}` {
		t.Errorf("V1 entries:\n%s", got)
	}

	// The user's own V2 servers and rules aren't join's to take out: only Murmur's go.
	write(t, path, `{"mcp": {"servers": {"murmur": {"type": "local"}, "theirs": {"type": "local"}}}, "permissions": [{"action": "*", "resource": "*", "effect": "ask"}, {"action": "murmur_*", "resource": "*", "effect": "allow"}]}`)
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if value(t, got, "mcp", "servers") != `{"theirs":{"type":"local"}}` || value(t, got, "permissions") != `[{"action":"*","resource":"*","effect":"ask"}]` {
		t.Errorf("the user's V2 entries:\n%s", got)
	}
}

// And the other way: a config written for OpenCode 1 is made OpenCode 2's.
func TestRegisterOpenCodeMovesV1ToV2(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "opencode v2.2.0")
	path := filepath.Join(sb.OpenCodeDir, "opencode.json")
	write(t, path, `{
  "mcp": {
    "murmur": {"type": "local", "command": ["/old/mur", "mcp"], "enabled": true},
    "other": {"type": "local", "command": ["o"]}
  },
  "permission": {"*": "ask", "murmur_*": "allow"}
}
`)
	did, fresh, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb))
	if err != nil || !fresh || len(did) != 4 || !strings.Contains(did[0], "OpenCode 2") { // the last is the plugin (#431)
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	got := read(t, path)
	if value(t, got, "mcp", "murmur") != "" || value(t, got, "permission") != `{"*":"ask"}` || strings.Contains(got, "/old/mur") {
		t.Errorf("V1 entries left:\n%s", got)
	}
	if value(t, got, "mcp", "other") != `{"type":"local","command":["o"]}` {
		t.Errorf("the user's server:\n%s", got)
	}
	if value(t, got, "mcp", "servers", "murmur", "codemode") != "false" || value(t, got, "permissions") != `[{"action":"murmur_*","resource":"*","effect":"allow"}]` {
		t.Errorf("V2 entries:\n%s", got)
	}
	// A "permission" that held only Murmur's key goes.
	write(t, path, `{"mcp": {"murmur": {"type": "local"}}, "permission": {"murmur_*": "allow"}}`)
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); value(t, got, "permission") != "" || value(t, got, "mcp", "murmur") != "" {
		t.Errorf("opencode.json:\n%s", got)
	}
}

// With both files, the one read first (opencode.json) loses Murmur's entries of the other shape,
// "permissions" included, which would make OpenCode 1 refuse the merged config.
func TestRegisterOpenCodeV1WithBothFiles(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "1.19.5")
	jsonPath, jsoncPath := filepath.Join(sb.OpenCodeDir, "opencode.json"), filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	write(t, jsonPath, `{"model": "x/y", "mcp": {"servers": {"murmur": {"type": "local", "command": ["/old/mur"], "disabled": true}}}, "permissions": [{"action": "murmur_*", "resource": "*", "effect": "allow"}]}`)
	write(t, jsoncPath, "{\n  // mine\n}\n")
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, jsonPath); value(t, got, "mcp") != value(t, read(t, jsoncPath), "mcp") || value(t, got, "permissions") != "" || value(t, got, "model") != `"x/y"` {
		t.Errorf("opencode.json:\n%s", got)
	}
	if got := read(t, jsoncPath); !strings.Contains(got, "// mine") || value(t, got, "permission", "murmur_*") != `"allow"` {
		t.Errorf("opencode.jsonc:\n%s", got)
	}
}

// OpenCode 1's "permission" may be one word for every tool: "allow" needs nothing, and any other
// becomes the object that says the same, with Murmur's key after "*".
func TestRegisterOpenCodeV1PermissionWord(t *testing.T) {
	for word, want := range map[string]string{"allow": `"allow"`, "ask": `{"*":"ask","murmur_*":"allow"}`} {
		sb := newSandbox(t, false)
		withAgents(sb)
		withOpenCode(t, sb, "1.19.5")
		path := filepath.Join(sb.OpenCodeDir, "opencode.json")
		write(t, path, `{"permission": "`+word+`"}`)
		if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
			t.Fatal(err)
		}
		if got := value(t, read(t, path), "permission"); got != want {
			t.Errorf("%s: permission %s", word, got)
		}
	}
}

// A leave's advice names Murmur's entries in either shape.
func TestOpenCodeRemovalNamesBothShapes(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	jsonPath, jsoncPath := filepath.Join(sb.OpenCodeDir, "opencode.json"), filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	write(t, jsonPath, `{"mcp": {"servers": {"murmur": {}}}, "permissions": [{"action": "murmur_*", "resource": "*", "effect": "allow"}]}`)
	write(t, jsoncPath, `{"mcp": {"murmur": {}}, "permission": {"murmur_*": "allow"}}`)
	r := openCodeRemoval(sb.Env, openCodeHome(sb))
	for _, want := range []string{
		`delete "murmur" from mcp.servers, the murmur_* rule from permissions in ` + jsonPath,
		`delete "murmur" from mcp, "murmur_*" from permission in ` + jsoncPath,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("removal lacks %q:\n%s", want, r)
		}
	}
	write(t, jsonPath, `{"model": "x/y"}`)
	if err := os.Remove(jsoncPath); err != nil {
		t.Fatal(err)
	}
	if r := openCodeRemoval(sb.Env, openCodeHome(sb)); r != "" {
		t.Errorf("removal with nothing there: %q", r)
	}
}

// A config file that isn't a regular file isn't read or written.
func TestRegisterOpenCodeRefusesWhatIsNotAFile(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "1.19.5")
	path := filepath.Join(sb.OpenCodeDir, "opencode.json")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err == nil {
		t.Error("no error")
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Errorf("%s changed: %v", path, err)
	}
}

// The file read first loses an emptied "permission" after a // comment and stays readable (Sol's
// review of #396).
func TestRegisterOpenCodeV2KeepsTheOtherFileReadable(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "opencode v2.2.0")
	jsonPath := filepath.Join(sb.OpenCodeDir, "opencode.json")
	write(t, jsonPath, "{\"model\":\"x/y\", // keep\n \"permission\":{\"murmur_*\":\"allow\"}}")
	write(t, filepath.Join(sb.OpenCodeDir, "opencode.jsonc"), "{}")
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, jsonPath); got != "{\"model\":\"x/y\" // keep\n}" {
		t.Errorf("opencode.json: %q", got)
	}
}

// OpenCode 1 merges "permission" keeping a key where the file read first has it: a "murmur_*" in
// opencode.json would stay ahead of its "*", whatever opencode.jsonc says, so it goes from there
// (Sol's second review of #396).
func TestRegisterOpenCodeV1TakesTheRuleOutOfTheFileReadFirst(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "1.19.5")
	jsonPath, jsoncPath := filepath.Join(sb.OpenCodeDir, "opencode.json"), filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	write(t, jsonPath, `{"permission": {"murmur_*": "allow", "*": "deny"}}`)
	write(t, jsoncPath, "{}")
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	if got := value(t, read(t, jsonPath), "permission"); got != `{"*":"deny"}` {
		t.Errorf("opencode.json's permission: %s", got)
	}
	if got := value(t, read(t, jsoncPath), "permission"); got != `{"murmur_*":"allow"}` {
		t.Errorf("opencode.jsonc's permission: %s", got)
	}
	// A "permission" in it that held only Murmur's key goes too.
	write(t, jsonPath, `{"model": "x/y", "permission": {"murmur_*": "allow"}}`)
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, jsonPath); value(t, got, "permission") != "" || value(t, got, "model") != `"x/y"` {
		t.Errorf("opencode.json:\n%s", got)
	}
}
