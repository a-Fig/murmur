package join

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// withCursor gives the sandbox a Cursor folder, as Cursor's CLI makes on its first run.
func withCursor(t *testing.T, sb *sandbox) agentHome {
	t.Helper()
	sb.CursorDir = filepath.Join(sb.root, "cursor")
	sb.CursorConfig = filepath.Join(sb.CursorDir, "cli-config.json")
	if err := os.MkdirAll(sb.CursorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return sb.defaultHome(agents[agentIndex("cursor")])
}

// cursorHooks is what Cursor's hooks.json runs on each event.
func cursorHooks(t *testing.T, text string) map[string][]string {
	t.Helper()
	var doc struct {
		Version int                                   `json:"version"`
		Hooks   map[string][]struct{ Command string } `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil || doc.Version != 1 {
		t.Fatalf("hooks.json (version %d, %v):\n%s", doc.Version, err, text)
	}
	out := map[string][]string{}
	for ev, list := range doc.Hooks {
		for _, h := range list {
			out[ev] = append(out[ev], h.Command)
		}
	}
	return out
}

// Join puts Murmur's server in Cursor's mcp.json, its hook in hooks.json on a chat's start, each
// prompt, each tool call and each turn's end, and its allow rule in cli-config.json, keeping the
// user's own servers, hooks, settings and comments (#920, #922). A second join changes nothing; one
// after the binary moved points each at the new path, in place. Uninstall takes out only what join
// wrote.
func TestCursorJoin(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	mcpPath, hooksPath := filepath.Join(sb.CursorDir, "mcp.json"), filepath.Join(sb.CursorDir, "hooks.json")
	write(t, mcpPath, "{\n  // my servers\n  \"mcpServers\": {\n    \"docs\": {\"url\": \"https://example.com/mcp\"}\n  }\n}\n")
	write(t, hooksPath, `{"version": 1, "hooks": {"stop": [{"command": "say done"}]}}`)
	write(t, sb.CursorConfig, `{"version": 1, "permissions": {"allow": ["Shell(ls)"], "deny": []}, "approvalMode": "allowlist"}`)

	did, fresh, err := sb.registerCursorAt(h)
	if err != nil || !fresh || len(did) != 3 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	servers := read(t, mcpPath)
	for _, want := range []string{"// my servers", `"docs"`, `"` + brand.MCPServer + `": {`, `"--agent"`, `"cursor"`} {
		if !strings.Contains(servers, want) {
			t.Errorf("mcp.json lacks %s:\n%s", want, servers)
		}
	}
	command := agentHookCommand(sb.Exe, "cursor")
	want := map[string][]string{
		"sessionStart":       {command},
		"beforeSubmitPrompt": {command},
		"postToolUse":        {command},
		"stop":               {"say done", command},
	}
	if got := cursorHooks(t, read(t, hooksPath)); !equalHooks(got, want) {
		t.Errorf("hooks: %q", got)
	}
	config := read(t, sb.CursorConfig)
	var cfg struct {
		Permissions  struct{ Allow, Deny []string }
		ApprovalMode string
	}
	if err := json.Unmarshal([]byte(config), &cfg); err != nil || strings.Join(cfg.Permissions.Allow, " ") != "Shell(ls) Mcp(murmur:*)" ||
		cfg.ApprovalMode != "allowlist" {
		t.Errorf("cli-config.json (%v):\n%s", err, config)
	}

	if did, fresh, err := sb.registerCursorAt(h); err != nil || fresh || len(did) != 0 {
		t.Errorf("second join: did=%q fresh=%v err=%v", did, fresh, err)
	}
	if r := cursorRemoval(sb.Env, h); !strings.Contains(r, "mcpServers") || !strings.Contains(r, "hook cursor") || !strings.Contains(r, "Mcp(murmur:*)") {
		t.Errorf("removal: %q", r)
	}

	sb.Exe = filepath.Join(sb.root, "elsewhere", exeName("mur"))
	if did, _, err := sb.registerCursorAt(h); err != nil || len(did) != 2 {
		t.Fatalf("after a move: did=%q err=%v", did, err)
	}
	moved := agentHookCommand(sb.Exe, "cursor")
	want = map[string][]string{"sessionStart": {moved}, "beforeSubmitPrompt": {moved}, "postToolUse": {moved}, "stop": {"say done", moved}}
	if got := cursorHooks(t, read(t, hooksPath)); !equalHooks(got, want) {
		t.Errorf("after a move: %q", got)
	}

	did, _, err = sb.unregisterCursorAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 3 {
		t.Fatalf("uninstall: did=%q err=%v", did, err)
	}
	if left := read(t, mcpPath); strings.Contains(left, brand.MCPServer) || !strings.Contains(left, `"docs"`) || !strings.Contains(left, "// my servers") {
		t.Errorf("mcp.json after uninstall:\n%s", left)
	}
	if got := cursorHooks(t, read(t, hooksPath)); !equalHooks(got, map[string][]string{"stop": {"say done"}}) {
		t.Errorf("hooks after uninstall: %q", got)
	}
	if left := read(t, sb.CursorConfig); strings.Contains(left, "Mcp(") || !strings.Contains(left, "Shell(ls)") {
		t.Errorf("cli-config.json after uninstall:\n%s", left)
	}
}

// A fresh Cursor folder gets each file new, in the shape Cursor reads: hooks.json with its version,
// and a cli-config.json with only the rule, which Cursor fills in with its defaults and keeps
// (seen with cursor-agent 2026.11.22-e9042b2).
func TestCursorJoinMakesTheFiles(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	if _, fresh, err := sb.registerCursorAt(h); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	if got := read(t, sb.CursorConfig); !json.Valid([]byte(got)) || !strings.Contains(got, `"Mcp(murmur:*)"`) {
		t.Errorf("cli-config.json:\n%s", got)
	}
	if got := cursorHooks(t, read(t, filepath.Join(sb.CursorDir, "hooks.json"))); len(got) != len(cursorHookEvents) {
		t.Errorf("hooks: %q", got)
	}
}

// Cursor reads cli-config.json as strict JSON, and one with a comment it throws away, writing its
// defaults over the user's settings. Join leaves such a file alone, says why, and writes the
// server and the hook all the same.
func TestCursorConfigWithAComment(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	before := "{\n  // mine\n  \"approvalMode\": \"allowlist\"\n}\n"
	write(t, sb.CursorConfig, before)
	did, _, err := sb.registerCursorAt(h)
	if err == nil || !strings.Contains(err.Error(), "isn't plain JSON") || len(did) != 2 {
		t.Errorf("did=%q err=%v", did, err)
	}
	if got := read(t, sb.CursorConfig); got != before {
		t.Errorf("cli-config.json changed:\n%s", got)
	}
}

// A machine without Cursor's folder or its command line gets nothing of Cursor's.
func TestCursorNotInstalled(t *testing.T) {
	sb := newSandbox(t, false)
	sb.CursorDir = filepath.Join(sb.root, "cursor")
	sb.CursorConfig = filepath.Join(sb.CursorDir, "cli-config.json")
	h := sb.defaultHome(agents[agentIndex("cursor")])
	if did, fresh, err := sb.registerCursorAt(h); err != nil || fresh || len(did) != 0 {
		t.Errorf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	if _, err := os.Stat(sb.CursorDir); !os.IsNotExist(err) {
		t.Errorf("made %s", sb.CursorDir)
	}
}

// cli-config.json is where Cursor's CLI looks for it: CURSOR_CONFIG_DIR, else
// $XDG_CONFIG_HOME/cursor, else ~/.cursor.
func TestCursorConfigDir(t *testing.T) {
	home := filepath.Join("h", "me")
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	for _, c := range []struct {
		vars map[string]string
		want string
	}{
		{nil, filepath.Join(home, ".cursor")},
		{map[string]string{"XDG_CONFIG_HOME": "x"}, filepath.Join("x", "cursor")},
		{map[string]string{"XDG_CONFIG_HOME": "x", "CURSOR_CONFIG_DIR": "c"}, "c"},
		{map[string]string{"CURSOR_CONFIG_DIR": "  "}, filepath.Join(home, ".cursor")},
	} {
		if got := cursorConfigDir(env(c.vars), home); got != c.want {
			t.Errorf("%v: %q, want %q", c.vars, got, c.want)
		}
	}
}

// A hooks.json without its version gets one, as Cursor reads none without it; the user's hooks stay.
func TestCursorHooksWithoutAVersion(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	path := filepath.Join(sb.CursorDir, "hooks.json")
	write(t, path, `{"hooks": {"stop": [{"command": "say done"}]}}`)
	if _, _, err := sb.registerCursorAt(h); err != nil {
		t.Fatal(err)
	}
	if got := cursorHooks(t, read(t, path)); len(got["stop"]) != 2 || got["stop"][0] != "say done" {
		t.Errorf("hooks: %q", got)
	}
}

// An allow value that isn't a list (null, string, object) is left alone, with a reason.
func TestCursorAllowThatIsntAList(t *testing.T) {
	for _, allow := range []string{"null", `"Shell(ls)"`, "{}"} {
		sb := newSandbox(t, false)
		h := withCursor(t, sb)
		before := `{"permissions": {"allow": ` + allow + `}}`
		write(t, sb.CursorConfig, before)
		_, _, err := sb.registerCursorAt(h)
		got := read(t, sb.CursorConfig)
		if err == nil || !strings.Contains(err.Error(), "isn't a list") || got != before {
			t.Errorf("allow %s: err=%v\n%s", allow, err, got)
		}
	}
}
