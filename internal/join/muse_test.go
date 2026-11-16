package join

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// museHooks is what Muse's settings.json says runs on each event, as "matcher command" lines.
func museHooks(t *testing.T, text string) map[string][]string {
	t.Helper()
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("settings.json isn't strict JSON (%v):\n%s", err, text)
	}
	out := map[string][]string{}
	for ev, groups := range doc.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				out[ev] = append(out[ev], g.Matcher+" "+h.Type+" "+h.Command)
			}
		}
	}
	return out
}

// Join puts Murmur's hook in Muse's settings.json beside its server, on a prompt, after every tool
// call and at Stop (#461), keeping the user's own hooks and settings; a second join changes
// nothing, and one after the binary moved replaces the hook in its place rather than adding one.
// Uninstall takes out only what join wrote.
func TestMuseHook(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	path := filepath.Join(sb.MuseDir, "settings.json")
	write(t, path, `{
  "schema_version": 1,
  "hooks": {
    "PostToolUse": [{"matcher": "write_file", "hooks": [{"type": "command", "command": "lint"}]}]
  }
}
`)
	h := sb.defaultHome(agents[agentIndex("muse")])
	did, fresh, err := sb.registerMuseAt(h)
	if err != nil || !fresh || len(did) != 2 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	command := agentHookCommand(sb.Exe, "muse")
	got := read(t, path)
	want := map[string][]string{
		"UserPromptSubmit": {" command " + command},
		"PostToolUse":      {"write_file command lint", "* command " + command},
		"Stop":             {" command " + command},
	}
	if hooks := museHooks(t, got); !equalHooks(hooks, want) {
		t.Errorf("hooks: %q\n%s", hooks, got)
	}
	if !strings.Contains(got, `"schema_version": 1`) || !strings.Contains(got, `"`+brand.MCPServer+`": {`) {
		t.Errorf("settings.json:\n%s", got)
	}
	if did, _, err := sb.registerMuseAt(h); err != nil || len(did) != 0 || read(t, path) != got {
		t.Errorf("second join: did=%q err=%v\n%s", did, err, read(t, path))
	}
	if r := museRemoval(sb.Env, h); !strings.Contains(r, "mcpServers") || !strings.Contains(r, "hook muse") {
		t.Errorf("removal: %q", r)
	}

	// The binary moved: the hook names the new path, once, in the same place.
	sb.Exe = filepath.Join(sb.root, "elsewhere", exeName("mur"))
	if did, _, err := sb.registerMuseAt(h); err != nil || len(did) != 2 {
		t.Fatalf("after a move: did=%q err=%v", did, err)
	}
	moved := agentHookCommand(sb.Exe, "muse")
	want["UserPromptSubmit"] = []string{" command " + moved}
	want["PostToolUse"] = []string{"write_file command lint", "* command " + moved}
	want["Stop"] = []string{" command " + moved}
	if hooks := museHooks(t, read(t, path)); !equalHooks(hooks, want) {
		t.Errorf("after a move: %q", hooks)
	}

	did, _, err = sb.unregisterMuse(context.Background(), []agentHome{h})
	if err != nil || len(did) != 2 {
		t.Fatalf("uninstall: did=%q err=%v", did, err)
	}
	left := read(t, path)
	if hooks := museHooks(t, left); !equalHooks(hooks, map[string][]string{"PostToolUse": {"write_file command lint"}}) {
		t.Errorf("after uninstall: %q\n%s", hooks, left)
	}
	if strings.Contains(left, brand.MCPServer) || strings.Contains(left, "UserPromptSubmit") || !strings.Contains(left, `"schema_version": 1`) {
		t.Errorf("after uninstall:\n%s", left)
	}
}

// A settings.json whose hooks join can't read keeps them, and gets the server all the same: the hook
// is a reported problem.
func TestMuseHooksJoinCantRead(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	path := filepath.Join(sb.MuseDir, "settings.json")
	write(t, path, `{"schema_version": 1, "hooks": {"Stop": "mine"}}`)
	did, _, err := sb.registerMuseAt(sb.defaultHome(agents[agentIndex("muse")]))
	if err == nil || !strings.Contains(err.Error(), "hook wasn't added") || len(did) != 1 {
		t.Errorf("did=%q err=%v", did, err)
	}
	if got := read(t, path); !strings.Contains(got, `"Stop": "mine"`) || !strings.Contains(got, brand.MCPServer) {
		t.Errorf("settings.json:\n%s", got)
	}
}

// Muse runs a hook through cmd on Windows and can't run a quoted path, so a path the hook would
// need quotes for is refused; elsewhere it is single-quoted for sh.
func TestMuseHookCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		if c, err := museHookCommand("/opt/my tools/mur"); err != nil || c != "'/opt/my tools/mur' hook muse" {
			t.Errorf("%q, %v", c, err)
		}
		return
	}
	if c, err := museHookCommand(`C:\bin\mur.exe`); err != nil || c != `C:\bin\mur.exe hook muse` {
		t.Errorf("%q, %v", c, err)
	}
	if c, err := museHookCommand(`C:\no such dir\a&b\mur.exe`); err == nil {
		t.Errorf("a path cmd can't run unquoted: %q", c)
	}
}

func equalHooks(a, b map[string][]string) bool { return maps.EqualFunc(a, b, slices.Equal[[]string]) }
