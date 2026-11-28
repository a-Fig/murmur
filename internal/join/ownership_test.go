package join

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// #1199: Cursor's and Kiro's joins put Murmur's command and args on a server entry the user already
// had and keep every other field of it, as Codex's does; and an allow rule that was there before
// join isn't join's to take out.

// serverEntry reads mcpServers.murmur from the JSON at path.
func serverEntry(t *testing.T, path string) map[string]any {
	t.Helper()
	var doc struct {
		McpServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, read(t, path))
	}
	return doc.McpServers[brand.MCPServer]
}

func envOf(entry map[string]any) map[string]any {
	env, _ := entry["env"].(map[string]any)
	return env
}

const userServer = `{"mcpServers": {"murmur": {"command": "/old/mur", "args": ["mcp", "--agent", "elsewhere"], "env": {"MUR_DEBUG": "1"}}, "docs": {"url": "https://example.com/mcp"}}}`

func TestCursorJoinKeepsTheUsersFieldsOnItsServer(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	path := filepath.Join(sb.CursorDir, "mcp.json")
	write(t, path, userServer)

	if _, fresh, err := sb.registerCursorAt(h); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	entry := serverEntry(t, path)
	if entry["command"] != sb.Exe || envOf(entry)["MUR_DEBUG"] != "1" {
		t.Errorf("after join, the entry is %v, want join's command and the user's env", entry)
	}
	if _, fresh, err := sb.registerCursorAt(h); err != nil || fresh {
		t.Errorf("second join: fresh=%v err=%v", fresh, err)
	}

	// Uninstall takes the entry out whole, as Codex's does, and says its env went with it.
	_, notes, err := sb.unregisterCursorAt(context.Background(), []agentHome{h})
	if err != nil {
		t.Fatal(err)
	}
	if entry = serverEntry(t, path); entry != nil {
		t.Errorf("after uninstall, the entry is %v, want none", entry)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "env") {
		t.Errorf("uninstall doesn't say the env went: %q", notes)
	}
	if !strings.Contains(read(t, path), `"docs"`) {
		t.Errorf("the user's other server is gone:\n%s", read(t, path))
	}
}

func TestCursorUninstallKeepsAnAllowRuleJoinDidntAdd(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	write(t, sb.CursorConfig, `{"version": 1, "permissions": {"allow": ["Mcp(murmur:*)"]}}`)

	did, _, err := sb.registerCursorAt(h)
	if err != nil || strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Fatalf("join: did=%q err=%v", did, err)
	}
	if rem := cursorRemoval(sb.Env, h); strings.Contains(rem, "Mcp(") {
		t.Errorf("removal advice names a rule join didn't add: %q", rem)
	}
	did, notes, err := sb.unregisterCursorAt(context.Background(), []agentHome{h})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, sb.CursorConfig), "Mcp(murmur:*)") || strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Errorf("uninstall removed the user's rule: did=%q\n%s", did, read(t, sb.CursorConfig))
	}
	if !strings.Contains(strings.Join(notes, "\n"), sb.CursorConfig) {
		t.Errorf("uninstall doesn't say it kept the rule: %q", notes)
	}
}

// A rule join added goes on uninstall, and so does the record of it.
func TestCursorUninstallRemovesTheRuleJoinAdded(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	write(t, sb.CursorConfig, `{"version": 1, "permissions": {"allow": ["Shell(ls)"]}}`)
	if _, _, err := sb.registerCursorAt(h); err != nil {
		t.Fatal(err)
	}
	if rem := cursorRemoval(sb.Env, h); !strings.Contains(rem, "Mcp(murmur:*)") {
		t.Errorf("removal advice omits the rule join added: %q", rem)
	}
	if _, _, err := sb.unregisterCursorAt(context.Background(), []agentHome{h}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, sb.CursorConfig); strings.Contains(got, "Mcp(") || !strings.Contains(got, "Shell(ls)") {
		t.Errorf("cli-config.json after uninstall:\n%s", got)
	}
	if _, err := os.Stat(ruleRecordPath(sb.CursorConfig)); !os.IsNotExist(err) {
		t.Errorf("the record of the rule is still there (%v)", err)
	}
}

func TestKiroJoinKeepsTheUsersFieldsOnItsServer(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	path := filepath.Join(sb.KiroHome, "settings", "mcp.json")
	write(t, path, userServer)

	if _, _, err := sb.registerKiroAt(h); err != nil {
		t.Fatal(err)
	}
	entry := serverEntry(t, path)
	if entry["command"] != sb.Exe || envOf(entry)["MUR_DEBUG"] != "1" {
		t.Errorf("after join, the entry is %v, want join's command and the user's env", entry)
	}
	_, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil {
		t.Fatal(err)
	}
	if entry = serverEntry(t, path); entry != nil {
		t.Errorf("after uninstall, the entry is %v, want none", entry)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "env") {
		t.Errorf("uninstall doesn't say the env went: %q", notes)
	}
}

func TestKiroUninstallKeepsAnAllowRuleJoinDidntAdd(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	perms := filepath.Join(sb.KiroHome, "settings", "permissions.json")
	write(t, perms, `{"rules": [{"capability": "mcp", "match": ["murmur/*"], "effect": "allow"}]}`)

	did, _, err := sb.registerKiroAt(h)
	if err != nil || strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Fatalf("join: did=%q err=%v", did, err)
	}
	if rem := kiroRemoval(sb.Env, h); strings.Contains(rem, perms) {
		t.Errorf("removal advice names a rule join didn't add: %q", rem)
	}
	did, notes, err := sb.unregisterKiroAt(context.Background(), []agentHome{h})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, perms), "murmur/*") || strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Errorf("uninstall removed the user's rule: did=%q\n%s", did, read(t, perms))
	}
	if !strings.Contains(strings.Join(notes, "\n"), perms) {
		t.Errorf("uninstall doesn't say it kept the rule: %q", notes)
	}
}

func TestKiroUninstallRemovesTheRuleJoinAdded(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	mustMakeDir(t, sb.KiroHome)
	h := sb.defaultHome(agents[agentIndex("kiro")])
	perms := filepath.Join(sb.KiroHome, "settings", "permissions.json")
	if _, _, err := sb.registerKiroAt(h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, perms), "murmur/*") {
		t.Fatalf("join added no rule:\n%s", read(t, perms))
	}
	if rem := kiroRemoval(sb.Env, h); !strings.Contains(rem, perms) {
		t.Errorf("removal advice omits the rule join added: %q", rem)
	}
	if _, _, err := sb.unregisterKiroAt(context.Background(), []agentHome{h}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, perms), "murmur/*") {
		t.Errorf("uninstall left the rule join added:\n%s", read(t, perms))
	}
	if _, err := os.Stat(ruleRecordPath(perms)); !os.IsNotExist(err) {
		t.Errorf("the record of the rule is still there (%v)", err)
	}
}

// A file in a scratch Cursor home that is a link to one outside the temporary folder is written
// through by join and uninstall, so the home is left alone as if its folder were outside (#995).
func TestCursorScratchFileLinkedOutIsOutside(t *testing.T) {
	for _, file := range []string{"mcp.json", "hooks.json", "murmur-rules.json", "skills"} {
		t.Run(file, func(t *testing.T) {
			sb := scratchSandbox(t)
			sb.CursorDir = filepath.Join(sb.Home, ".cursor")
			sb.CursorConfig = filepath.Join(sb.CursorDir, "cli-config.json")
			real := filepath.Join(sb.root, "real")
			mustMakeDir(t, real)
			mustMakeDir(t, sb.CursorDir)
			byAgent, _ := sb.homes()
			if got := dirsOf(byAgent[agentIndex("cursor")]); len(got) != 1 || got[0] != sb.CursorDir {
				t.Fatalf("with no link, cursor homes = %q, want the scratch one", got)
			}
			switch file {
			case "murmur-rules.json":
				linkOrSkip(t, filepath.Join(real, file), ruleRecordPath(sb.CursorConfig))
			case "skills":
				mustMakeDir(t, filepath.Join(sb.CursorDir, "skills", "murmur"))
				linkOrSkip(t, filepath.Join(real, "SKILL.md"), filepath.Join(sb.CursorDir, "skills", "murmur", "SKILL.md"))
			default:
				linkOrSkip(t, filepath.Join(real, file), filepath.Join(sb.CursorDir, file))
			}
			for _, uninstall := range []bool{false, true} {
				byAgent, notes := sb.homesWith(uninstall)
				if got := dirsOf(byAgent[agentIndex("cursor")]); len(got) != 1 || got[0] != "" {
					t.Errorf("uninstall=%v: cursor homes = %q, want filtered out", uninstall, got)
				}
				if note := strings.Join(notes, "\n"); !strings.Contains(note, "Cursor in "+sb.CursorDir) {
					t.Errorf("uninstall=%v: note lacks the Cursor entry:\n%s", uninstall, note)
				}
			}
		})
	}
}

// Kiro's guard names the record join keeps beside permissions.json too.
func TestKiroScratchRecordLinkedOutIsOutside(t *testing.T) {
	sb := scratchSandbox(t)
	real := filepath.Join(sb.root, "real")
	mustMakeDir(t, real)
	mustMakeDir(t, filepath.Join(sb.KiroHome, "settings"))
	linkOrSkip(t, filepath.Join(real, "murmur-rules.json"), ruleRecordPath(filepath.Join(sb.KiroHome, "settings", "permissions.json")))
	byAgent, _ := sb.homes()
	if got := dirsOf(byAgent[agentIndex("kiro")]); len(got) != 1 || got[0] != "" {
		t.Errorf("kiro homes = %q, want filtered out", got)
	}
}

// Where a symbolic link can't be made (Windows without the privilege), a junction at each name
// leads outside all the same, and the home is left alone.
func TestCursorScratchJunctionAtAFileNameIsOutside(t *testing.T) {
	if _, err := exec.LookPath("cmd"); err != nil {
		t.Skip("junctions are Windows'")
	}
	for _, file := range []string{"mcp.json", "hooks.json", "skills"} {
		t.Run(file, func(t *testing.T) {
			sb := scratchSandbox(t)
			sb.CursorDir = filepath.Join(sb.Home, ".cursor")
			sb.CursorConfig = filepath.Join(sb.CursorDir, "cli-config.json")
			target := filepath.Join(sb.root, "real", file)
			mustMakeDir(t, target)
			mustMakeDir(t, sb.CursorDir)
			if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(sb.CursorDir, file), target).CombinedOutput(); err != nil {
				t.Fatalf("mklink /J: %v\n%s", err, out)
			}
			byAgent, _ := sb.homes()
			if got := dirsOf(byAgent[agentIndex("cursor")]); len(got) != 1 || got[0] != "" {
				t.Errorf("cursor homes = %q, want filtered out", got)
			}
		})
	}
}

// A record that doesn't parse says nothing: uninstall still finishes, and a rule in the file stays.
func TestACorruptRuleRecordDoesntStopUninstall(t *testing.T) {
	sb := newSandbox(t, false)
	h := withCursor(t, sb)
	write(t, ruleRecordPath(sb.CursorConfig), "{not json")
	if _, _, err := sb.unregisterCursorAt(context.Background(), []agentHome{h}); err != nil {
		t.Errorf("uninstall with a corrupt record: %v", err)
	}
	write(t, sb.CursorConfig, `{"version": 1, "permissions": {"allow": ["Mcp(murmur:*)"]}}`)
	_, notes, err := sb.unregisterCursorAt(context.Background(), []agentHome{h})
	if err != nil || !strings.Contains(read(t, sb.CursorConfig), "Mcp(murmur:*)") || len(notes) == 0 {
		t.Errorf("err=%v notes=%q\n%s", err, notes, read(t, sb.CursorConfig))
	}
}

// recordJoinAdded notes in the record beside path that join added rule to it, as a join of this
// version does, for a test that writes the file by hand.
func recordJoinAdded(t *testing.T, path, rule string) {
	t.Helper()
	if err := recordRule(path, rule); err != nil {
		t.Fatal(err)
	}
}

// #1211: Claude Code's and Antigravity's joins record the rule they add, as Cursor's and Kiro's do,
// and uninstall takes out only a recorded one.

func TestClaudeUninstallKeepsAnAllowRuleJoinDidntAdd(t *testing.T) {
	sb := newSandbox(t, false)
	h := agentHome{Dir: filepath.Join(sb.root, "claude"), JSON: filepath.Join(sb.root, "claude.json"), Label: "Claude Code"}
	settings := filepath.Join(h.Dir, "settings.json")
	write(t, settings, `{"permissions": {"allow": ["mcp__murmur"]}}`)

	if allowed, err := allowClaudeTools(h.Dir); err != nil || allowed {
		t.Fatalf("join: allowed=%v err=%v", allowed, err)
	}
	if _, err := os.Stat(ruleRecordPath(settings)); !os.IsNotExist(err) {
		t.Errorf("join recorded a rule it didn't add (%v)", err)
	}
	did, notes, err := sb.unregisterClaudeAt(context.Background(), []agentHome{h})
	if err != nil || strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Fatalf("uninstall: did=%q err=%v", did, err)
	}
	if !strings.Contains(read(t, settings), "mcp__murmur") || !strings.Contains(strings.Join(notes, "\n"), settings) {
		t.Errorf("uninstall dropped the user's rule or didn't say it kept it: notes=%q\n%s", notes, read(t, settings))
	}
}

func TestClaudeUninstallRemovesTheRuleJoinAdded(t *testing.T) {
	sb := newSandbox(t, false)
	h := agentHome{Dir: filepath.Join(sb.root, "claude"), JSON: filepath.Join(sb.root, "claude.json"), Label: "Claude Code"}
	settings := filepath.Join(h.Dir, "settings.json")
	write(t, settings, `{"permissions": {"allow": ["Bash(ls)"]}}`)
	if allowed, err := allowClaudeTools(h.Dir); err != nil || !allowed {
		t.Fatalf("join: allowed=%v err=%v", allowed, err)
	}
	if !joinAddedRule(settings, allowRule) {
		t.Fatal("join didn't record the rule it added")
	}
	did, notes, err := sb.unregisterClaudeAt(context.Background(), []agentHome{h})
	if err != nil || len(notes) != 0 || !strings.Contains(strings.Join(did, "\n"), "allowed") {
		t.Fatalf("uninstall: did=%q notes=%q err=%v", did, notes, err)
	}
	if got := read(t, settings); strings.Contains(got, "mcp__murmur") || !strings.Contains(got, "Bash(ls)") {
		t.Errorf("settings.json after uninstall:\n%s", got)
	}
	if _, err := os.Stat(ruleRecordPath(settings)); !os.IsNotExist(err) {
		t.Errorf("the record of the rule is still there (%v)", err)
	}
}

// A settings.json deleted after join leaves no rule to take out, so its record goes too, as
// takeOutAddedRuleMode does for the other agents.
func TestClaudeUninstallDropsTheRecordOfADeletedSettingsFile(t *testing.T) {
	sb := newSandbox(t, false)
	h := agentHome{Dir: filepath.Join(sb.root, "claude"), JSON: filepath.Join(sb.root, "claude.json"), Label: "Claude Code"}
	settings := filepath.Join(h.Dir, "settings.json")
	if allowed, err := allowClaudeTools(h.Dir); err != nil || !allowed {
		t.Fatalf("join: allowed=%v err=%v", allowed, err)
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sb.unregisterClaudeAt(context.Background(), []agentHome{h}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(ruleRecordPath(settings)); !os.IsNotExist(err) {
		t.Errorf("the record of the rule is still there (%v)", err)
	}
}

func TestAntigravityUninstallKeepsAnAllowRuleJoinDidntAdd(t *testing.T) {
	sb := newSandbox(t, false)
	h := agentHome{Dir: filepath.Join(sb.root, "gemini"), Label: "Antigravity", T3Own: true}
	cli := filepath.Join(h.Dir, "antigravity-cli", "settings.json")
	acp := filepath.Join(h.Dir, "antigravity-acp", "settings.json")
	write(t, cli, `{"permissions": {"allow": ["mcp(murmur/*)"]}}`)

	if allowed, err := allowAntigravityTools(cli); err != nil || allowed {
		t.Fatalf("join: allowed=%v err=%v", allowed, err)
	}
	if rem := antigravityRemoval(sb.Env, h); strings.Contains(rem, "mcp(murmur") {
		t.Errorf("removal advice names a rule join didn't add: %q", rem)
	}
	// The ACP file's rule is recorded, the CLI file's isn't: only the ACP one goes.
	write(t, acp, `{"permissions": {"allow": ["mcp(murmur/*)"]}}`)
	recordJoinAdded(t, acp, antigravityAllowRule)
	did, notes, err := sb.unregisterAntigravityAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 1 || !strings.Contains(did[0], acp) {
		t.Fatalf("uninstall: did=%q err=%v", did, err)
	}
	if !strings.Contains(read(t, cli), "mcp(murmur/*)") || !strings.Contains(strings.Join(notes, "\n"), cli) {
		t.Errorf("uninstall dropped the user's rule or didn't say it kept it: notes=%q\n%s", notes, read(t, cli))
	}
	if strings.Contains(read(t, acp), "murmur") {
		t.Errorf("the recorded rule stayed:\n%s", read(t, acp))
	}
}

func TestAntigravityUninstallRemovesTheRuleJoinAdded(t *testing.T) {
	sb := newSandbox(t, false)
	h := agentHome{Dir: filepath.Join(sb.root, "gemini"), Label: "Antigravity", T3Own: true}
	cli := filepath.Join(h.Dir, "antigravity-cli", "settings.json")
	write(t, cli, `{"model": "mine"}`)
	if allowed, err := allowAntigravityTools(cli); err != nil || !allowed {
		t.Fatalf("join: allowed=%v err=%v", allowed, err)
	}
	if rem := antigravityRemoval(sb.Env, h); !strings.Contains(rem, "mcp(murmur/*)") {
		t.Errorf("removal advice omits the rule join added: %q", rem)
	}
	if _, notes, err := sb.unregisterAntigravityAt(context.Background(), []agentHome{h}); err != nil || len(notes) != 0 {
		t.Fatalf("uninstall: notes=%q err=%v", notes, err)
	}
	if got := read(t, cli); strings.Contains(got, "murmur") || !strings.Contains(got, `"model": "mine"`) {
		t.Errorf("settings.json after uninstall:\n%s", got)
	}
	if _, err := os.Stat(ruleRecordPath(cli)); !os.IsNotExist(err) {
		t.Errorf("the record of the rule is still there (%v)", err)
	}
}
