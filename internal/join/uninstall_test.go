package join

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/jsonedit"
	"github.com/a-Fig/murmur/internal/skills"
)

// uninstallSandbox is newSandbox with the other agents and a record of clones of its own, and a
// home of its own for anything that asks the process for one: nothing reaches the real user's.
func uninstallSandbox(t *testing.T) (*sandbox, string) {
	t.Helper()
	sb := newSandbox(t, true)
	withAgents(sb)
	for _, k := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA", "CODEX_HOME", "GROK_HOME", "PI_CODING_AGENT_DIR"} {
		t.Setenv(k, filepath.Join(sb.root, "home"))
	}
	record := filepath.Join(sb.root, "clones")
	t.Setenv(clones.DirEnv, record)
	return sb, record
}

// Everything join writes per user comes out again, in every agent, and the user's own entries stay:
// another server, another allow rule, another hook, whose trust moves with it to its new place.
func TestUninstallRemovesWhatJoinWrote(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	withOpenCode(t, sb, "opencode v2.2.0")
	ctx := context.Background()
	for _, d := range []string{sb.ClaudeDir, sb.CodexHome, sb.OpenCodeDir, sb.MuseDir, sb.PiDir, sb.GrokHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, sb.ClaudeJSON, "{\n  \"mcpServers\": {\"other\": {\"type\": \"stdio\", \"command\": \"o\", \"args\": []}}\n}\n")
	write(t, filepath.Join(sb.ClaudeDir, "settings.json"), "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n")
	write(t, filepath.Join(sb.CodexHome, "config.toml"), "model = \"o3\"\n\n[mcp_servers.other]\ncommand = \"o\"\n")
	write(t, filepath.Join(sb.GrokHome, "config.toml"), "[permission]\nallow = [\"Bash(ls)\"]\n")
	write(t, filepath.Join(sb.MuseDir, "settings.json"), "{\n  \"schema_version\": 1,\n  \"mcpServers\": {\"other\": {\"command\": \"o\"}}\n}\n")
	write(t, filepath.Join(sb.OpenCodeDir, "opencode.json"), "{\n  // mine\n  \"model\": \"x/y\",\n}\n")
	for _, a := range agents {
		h := sb.defaultHome(a)
		if _, _, err := a.register(sb.Env, ctx, h); err != nil {
			t.Fatalf("%s: %v", a.label, err)
		}
		if dir := skillsDir(a, h); dir != "" {
			if _, _, err := installSkills(dir, a.label); err != nil {
				t.Fatal(err)
			}
		}
	}

	// A hook of the user's after Murmur's in Codex, trusted at its place.
	hooksPath := filepath.Join(sb.CodexHome, "hooks.json")
	text := read(t, hooksPath)
	text, _, err := jsonedit.AppendIn(text, []string{"hooks", "Stop"}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "lint"}}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, hooksPath, text)
	// The sandbox's Codex home is named (CODEX_HOME), so Codex keys its trust by the home's
	// canonical path, as join wrote Murmur's: /private/var on macOS, long names on Windows (#332).
	h := sb.defaultHome(agents[agentIndex("codex")])
	key := codexKeyPath(hooksPath, h.Named)
	cfg := filepath.Join(sb.CodexHome, "config.toml")
	write(t, cfg, read(t, cfg)+"\n[hooks.state."+tomlKey(key+":stop:1:0")+"]\ntrusted_hash = \"sha256:mine\"\n")

	r, err := Uninstall(ctx, sb.Env, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if len(r.Problems) > 0 || len(r.Left) == 0 {
		t.Errorf("report:\n%s", r)
	}

	claude := read(t, sb.ClaudeJSON)
	if strings.Contains(claude, `"`+brand.MCPServer+`"`) || !strings.Contains(claude, `"other"`) {
		t.Errorf(".claude.json:\n%s", claude)
	}
	if s := read(t, filepath.Join(sb.ClaudeDir, "settings.json")); strings.Contains(s, allowRule) || !strings.Contains(s, "Bash(ls)") {
		t.Errorf("settings.json:\n%s", s)
	}
	codex := read(t, cfg)
	if strings.Contains(codex, "mcp_servers."+brand.MCPServer) || strings.Contains(codex, ":user_prompt_submit:") || strings.Contains(codex, ":post_tool_use:") ||
		!strings.Contains(codex, "[mcp_servers.other]") || !strings.Contains(codex, `model = "o3"`) {
		t.Errorf("config.toml:\n%s", codex)
	}
	if !strings.Contains(codex, tomlKey(key+":stop:0:0")+"]\ntrusted_hash = \"sha256:mine\"") || strings.Contains(codex, ":stop:1:0") {
		t.Errorf("the user's hook's trust didn't move to its new place:\n%s", codex)
	}
	if h := read(t, hooksPath); strings.Contains(h, hookName) || !strings.Contains(h, `"lint"`) || strings.Contains(h, "PostToolUse") {
		t.Errorf("hooks.json:\n%s", h)
	}
	grok := read(t, filepath.Join(sb.GrokHome, "config.toml"))
	if strings.Contains(grok, brand.MCPServer) || !strings.Contains(grok, `allow = ["Bash(ls)"]`) {
		t.Errorf("Grok's config.toml:\n%s", grok)
	}
	if muse := read(t, filepath.Join(sb.MuseDir, "settings.json")); strings.Contains(muse, brand.MCPServer) || !strings.Contains(muse, `"other"`) {
		t.Errorf("Muse's settings.json:\n%s", muse)
	}
	oc := read(t, filepath.Join(sb.OpenCodeDir, "opencode.json"))
	if strings.Contains(oc, brand.MCPServer) || strings.Contains(oc, "permissions") || !strings.Contains(oc, "// mine") {
		t.Errorf("opencode.json:\n%s", oc)
	}
	for _, gone := range []string{
		piExtensionPath(sb.PiDir),
		filepath.Join(sb.GrokHome, "hooks", grokHookFile),
		filepath.Join(sb.ClaudeDir, "skills", skills.All()[0].Name),
		filepath.Join(sb.CodexHome, "skills", skills.All()[0].Name),
	} {
		if exists(gone) {
			t.Errorf("%s is still there", gone)
		}
	}

	// Again, nothing is left to do, and nothing changes.
	before := read(t, cfg)
	r, err = Uninstall(ctx, sb.Env, false)
	if err != nil || len(r.Did) != 1 || !strings.HasPrefix(r.Did[0], "Nothing to remove") || read(t, cfg) != before {
		t.Errorf("second: %v\n%s", err, r)
	}
}

// OpenCode 1's shape comes out too, and its "permission" keeps what the user had in it.
func TestUninstallOpenCodeV1(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	path := filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	write(t, path, "{\n  \"mcp\": {\"murmur\": {\"type\": \"local\", \"command\": [\"mur\", \"mcp\"], \"enabled\": true}, \"other\": {\"type\": \"local\"}},\n  \"permission\": {\"*\": \"ask\", \"murmur_*\": \"allow\"}\n}\n")
	did, _, err := sb.unregisterOpenCodeAt(context.Background(), []agentHome{openCodeHome(sb)})
	if err != nil || len(did) != 2 {
		t.Fatalf("did=%q err=%v", did, err)
	}
	got := read(t, path)
	if strings.Contains(got, "murmur") || !strings.Contains(got, `"other"`) || !strings.Contains(got, `"*": "ask"`) {
		t.Errorf("opencode.jsonc:\n%s", got)
	}
}

// A clone still joined stops the uninstall, which names it and changes nothing; --leave leaves it
// first, then removes the rest, the record of clones included.
func TestUninstallLeavesJoinedClonesOnlyWhenAsked(t *testing.T) {
	sb, record := uninstallSandbox(t)
	ctx := context.Background()
	write(t, filepath.Join(sb.CodexHome, "config.toml"), "[mcp_servers.murmur]\ncommand = \"mur\"\nargs = [\"mcp\"]\n")
	p, _ := clone(t)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	if err := clones.Add(record, clones.Clone{GitDir: p.GitDir, Name: "team-repo"}); err != nil {
		t.Fatal(err)
	}

	_, err := Uninstall(ctx, sb.Env, false)
	if err == nil || !strings.Contains(err.Error(), "team-repo") || !strings.Contains(err.Error(), "--leave") {
		t.Fatalf("err = %v", err)
	}
	if !p.Joined() || !strings.Contains(read(t, filepath.Join(sb.CodexHome, "config.toml")), "mcp_servers.murmur") {
		t.Fatal("a refused uninstall changed something")
	}

	r, err := Uninstall(ctx, sb.Env, true)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if p.Joined() {
		t.Error("the clone is still joined")
	}
	if strings.Contains(read(t, filepath.Join(sb.CodexHome, "config.toml")), "murmur") {
		t.Error("Codex still has the server")
	}
	if exists(record) {
		t.Error("the record of clones is still there")
	}
	if s := r.String(); !strings.Contains(s, "team-repo: ") || !strings.Contains(s, "Left for you:") {
		t.Errorf("report:\n%s", s)
	}
}

func TestEditTOMLTables(t *testing.T) {
	server := []string{"mcp_servers", "murmur"}
	drop := func(p []string) ([]string, bool) { return nil, hasPrefix(p, server) }
	for _, c := range []struct{ name, in, want string }{
		{"last", "a = 1\n\n[mcp_servers.murmur]\ncommand = \"mur\"\n", "a = 1\n"},
		{"middle", "a = 1\n\n[mcp_servers.murmur]\ncommand = \"mur\"\n\n[x]\nb = 2\n", "a = 1\n\n[x]\nb = 2\n"},
		{"first", "[mcp_servers.murmur]\ncommand = \"mur\"\n\n[x]\nb = 2\n", "[x]\nb = 2\n"},
		{"only", "[mcp_servers.murmur]\ncommand = \"mur\"\n", ""},
		{"subtable", "a = 1\n\n[mcp_servers.murmur]\ncommand = \"mur\"\n\n[mcp_servers.murmur.env]\nX = \"1\"\n\n[x]\n", "a = 1\n\n[x]\n"},
		{"crlf", "a = 1\r\n\r\n[mcp_servers.murmur]\r\ncommand = \"mur\"\r\n\r\n[x]\r\n", "a = 1\r\n\r\n[x]\r\n"},
		{"in a string", "a = \"\"\"\n[mcp_servers.murmur]\n\"\"\"\n", "a = \"\"\"\n[mcp_servers.murmur]\n\"\"\"\n"},
		{"none", "a = 1\n", "a = 1\n"},
	} {
		got, _, err := editTOMLTables(c.in, "config.toml", [][]string{server}, drop)
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, in := range []string{
		"mcp_servers.murmur.command = \"mur\"\n",
		"[mcp_servers]\nmurmur = { command = \"mur\" }\n",
		"mcp_servers = { murmur = { command = \"mur\" } }\n",
	} {
		if _, _, err := editTOMLTables(in, "config.toml", [][]string{server}, drop); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestShiftTrust(t *testing.T) {
	p := []string{`C:\h\.codex\hooks.json:`}
	removed := map[string][]int{"stop": {1}, "post_tool_use": {0, 2}}
	for _, c := range []struct {
		key, want string
		drop      bool
	}{
		{p[0] + "stop:0:0", p[0] + "stop:0:0", false},
		{p[0] + "stop:1:0", "", true},
		{p[0] + "stop:2:0", p[0] + "stop:1:0", false},
		{p[0] + "post_tool_use:1:0", p[0] + "post_tool_use:0:0", false},
		{p[0] + "post_tool_use:3:1", p[0] + "post_tool_use:1:1", false},
		{p[0] + "user_prompt_submit:4:0", p[0] + "user_prompt_submit:4:0", false},
		{`D:\other\hooks.json:stop:2:0`, `D:\other\hooks.json:stop:2:0`, false},
	} {
		got, drop := shiftTrust(c.key, p, removed)
		if got != c.want && !drop || drop != c.drop {
			t.Errorf("%s: %q %v, want %q %v", c.key, got, drop, c.want, c.drop)
		}
	}
}

// A T3 instance turned off still has what join wrote while it was on, and uninstall takes it out
// (Sol's review of #409): homes passes it over, for join.
func TestUninstallReachesAnInstanceTurnedOff(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	sb.Home = sb.root
	t.Setenv("HOME", sb.root)
	t.Setenv("USERPROFILE", sb.root)
	sb.T3Settings = filepath.Join(sb.root, ".t3", "userdata", "settings.json")
	write(t, sb.T3Settings, t3Settings(sb.root))
	ext := piExtensionPath(filepath.Join(sb.root, "pi-t3")) // the "pi" instance, which is off
	write(t, ext, piExtension(sb.Exe))
	if r, err := Uninstall(context.Background(), sb.Env, false); err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if exists(ext) {
		t.Error("the extension in the turned-off instance's home is still there")
	}
}

// Codex's two files change together or not at all: a config.toml uninstall won't edit leaves
// hooks.json as it was, so that a rerun still finds Murmur's groups (Sol's review of #409).
func TestUninstallCodexChangesNothingWhenTheTrustCannotBeEdited(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	if err := os.MkdirAll(sb.CodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sb.registerCodex(); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(sb.CodexHome, "config.toml")
	write(t, cfg, "[hooks]\nstate = {}\n\n"+read(t, cfg))
	hooks := read(t, filepath.Join(sb.CodexHome, "hooks.json"))
	if _, _, err := sb.unregisterCodexAt(context.Background(), []agentHome{sb.defaultHome(agents[agentIndex("codex")])}); err == nil {
		t.Fatal("no error")
	}
	if read(t, filepath.Join(sb.CodexHome, "hooks.json")) != hooks {
		t.Error("hooks.json changed")
	}
}

// Homes that are one directory on disk, through a link, are one group, with every spelling kept.
func TestSameDirFollowsLinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "codex")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	linkOrSkip(t, real, link)
	got := sameDir([]agentHome{{Dir: real}, {Dir: link, Named: true}, {Dir: filepath.Join(root, "other")}})
	if len(got) != 2 || len(got[0]) != 2 {
		t.Errorf("groups: %v", got)
	}
}

// A clone whose cleanup didn't finish stays on the record, so a rerun finds it.
func TestRemoveRecordKeepsWhatIsUnfinished(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "clones")
	a, b := clones.Clone{GitDir: filepath.Join(dir, "a", ".git")}, clones.Clone{GitDir: filepath.Join(dir, "b", ".git")}
	for _, c := range []clones.Clone{a, b} {
		if err := clones.Add(dir, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := removeRecord(dir, map[string]bool{a.ID(): true, b.ID(): false}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, a.ID()+".json")) || !exists(filepath.Join(dir, b.ID()+".json")) {
		t.Error("the wrong clone's file went")
	}
	if _, err := removeRecord(dir, map[string]bool{b.ID(): true}); err != nil || exists(dir) {
		t.Errorf("err=%v; the folder is still there: %v", err, exists(dir))
	}
}

// A clone that left without finishing (its hook stayed) and is still on the record is left again,
// which takes the hook out, and only then comes off the record (Sol's second review of #409).
func TestUninstallFinishesALeaveThatDidNot(t *testing.T) {
	sb, record := uninstallSandbox(t)
	p, other := clone(t)
	oldHooks(t, p, sb.Exe)
	if err := clones.Add(record, clones.Clone{GitDir: p.GitDir}); err != nil {
		t.Fatal(err)
	}
	if r, err := Uninstall(context.Background(), sb.Env, false); err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if exists(filepath.Join(other, filepath.FromSlash(hookFile))) || exists(filepath.Join(p.Top, filepath.FromSlash(hookFile))) {
		t.Error("the hook is still there")
	}
	if exists(record) {
		t.Error("the record is still there")
	}
}

// A clone whose folder isn't there now (a drive not mounted) keeps its record, and says so: it may
// come back with its consent and hooks in it (Sol's second review of #409).
func TestUninstallKeepsTheRecordOfAMissingClone(t *testing.T) {
	sb, record := uninstallSandbox(t)
	gone := clones.Clone{GitDir: filepath.Join(sb.root, "unmounted", ".git")}
	if err := clones.Add(record, gone); err != nil {
		t.Fatal(err)
	}
	r, err := Uninstall(context.Background(), sb.Env, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if !exists(filepath.Join(record, gone.ID()+".json")) || !strings.Contains(r.String(), "Kept 1 clone") {
		t.Errorf("record dropped, or not said:\n%s", r)
	}
}

// Two Codex homes whose hooks.json is one file (links to a dotfiles repository's) each get their
// own trust moved, though the first edit takes Murmur's groups out of the file both read (Sol's
// second review of #409).
func TestUninstallCodexHomesSharingAHooksFile(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	if err := os.MkdirAll(sb.CodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sb.registerCodex(); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(sb.CodexHome, "hooks.json")
	text, _, err := jsonedit.AppendIn(read(t, shared), []string{"hooks", "Stop"}, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "lint"}}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, shared, text)
	second := filepath.Join(sb.root, "codex-work")
	// A hard link, which needs no privilege on Windows, is one file to os.SameFile as a symbolic one is.
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(shared, filepath.Join(second, "hooks.json")); err != nil {
		t.Fatal(err)
	}
	key := codexKeyPath(filepath.Join(second, "hooks.json"), true)
	write(t, filepath.Join(second, "config.toml"), "[hooks.state."+tomlKey(key+":stop:0:0")+"]\ntrusted_hash = \"sha256:m\"\n\n[hooks.state."+tomlKey(key+":stop:1:0")+"]\ntrusted_hash = \"sha256:mine\"\n")
	groups := [][]agentHome{{sb.defaultHome(agents[agentIndex("codex")])}, {{Dir: second, Label: "Codex (work)", Named: true}}}
	if _, _, err := sb.unregisterCodex(context.Background(), groups); err != nil {
		t.Fatal(err)
	}
	got := read(t, filepath.Join(second, "config.toml"))
	if got != "[hooks.state."+tomlKey(key+":stop:0:0")+"]\ntrusted_hash = \"sha256:mine\"\n" {
		t.Errorf("the second home's config.toml:\n%s", got)
	}
}

// Murmur's folder in the user's config directory goes too (#430): the old check-ins' user ID, the
// records of MCP servers that exited, and the record of clones. A server still running keeps its
// record and the folder, and the report says so.
func TestUninstallRemovesTheUserFolder(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	// The default folders, which the sandbox's home holds, in place of the ones tests name.
	t.Setenv(clones.DirEnv, "")
	t.Setenv(brand.SessionsDirEnv, "")
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(config, sb.root) {
		t.Fatalf("the config directory %s isn't the sandbox's", config)
	}
	dir := filepath.Join(config, brand.Name)
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(filepath.Join(dir, "clones"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "id"), "0123456789abcdef0123456789abcdef\n")
	if err := here.Publish(sessions, here.Session{ServerPID: exitedPID(t)}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A server still running: this test's process stands in for it.
	if err := here.Publish(sessions, here.Session{ServerPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	r, err := Uninstall(ctx, sb.Env, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if _, err := os.Stat(filepath.Join(dir, "id")); !os.IsNotExist(err) {
		t.Errorf("the old user ID is still there: %v", err)
	}
	if left, _ := here.Sessions(sessions); len(left) != 1 || left[0].ServerPID != os.Getpid() {
		t.Errorf("sessions' records left = %+v, want only the running server's", left)
	}
	if _, err := os.Stat(filepath.Join(dir, "clones")); !os.IsNotExist(err) {
		t.Errorf("the record of clones is still there: %v", err)
	}
	want := brand.Product + "'s folder in your config directory (" + dir + "), which still holds sessions (the records of 1 agent session whose " + brand.Product + " server still runs"
	if s := r.String(); !strings.Contains(s, want) || !strings.Contains(s, "Removed the user ID") || strings.Contains(s, "Nothing to remove") {
		t.Errorf("the report doesn't say what went and what stayed:\n%s", s)
	}

	// Once that server has exited, uninstall again takes the rest.
	here.Withdraw(sessions, os.Getpid())
	r, err = Uninstall(ctx, sb.Env, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("the folder is still there (%v), holding %v\n%s", err, entries, r)
	}
	s := r.String()
	if !strings.Contains(s, "Removed "+brand.Product+"'s folder in your config directory ("+dir+")") || strings.Contains(s, "your config directory ("+dir+"), which") {
		t.Errorf("report:\n%s", s)
	}
}

// A folder that isn't the default one, as tests and MURMUR_CLONES_DIR name, is never removed
// whole: only the record of clones in it is.
func TestUninstallLeavesANamedFolder(t *testing.T) {
	sb, record := uninstallSandbox(t)
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config, brand.Name)
	write(t, filepath.Join(dir, "id"), "0123456789abcdef0123456789abcdef\n")
	if err := os.MkdirAll(record, 0o700); err != nil {
		t.Fatal(err)
	}
	if r, err := Uninstall(context.Background(), sb.Env, false); err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if _, err := os.Stat(filepath.Join(dir, "id")); err != nil {
		t.Errorf("the default folder was touched while another was named: %v", err)
	}
}

func exitedPID(t *testing.T) int {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// A config folder that is a link isn't removed: that would take the link away and leave what it
// points to, a running server's record and all (from the review).
func TestUninstallLeavesALinkedFolder(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	t.Setenv(clones.DirEnv, "")
	t.Setenv(brand.SessionsDirEnv, "")
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(sb.root, "elsewhere")
	write(t, filepath.Join(real, "id"), "0123456789abcdef0123456789abcdef\n")
	if err := os.MkdirAll(config, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(config, brand.Name)
	if err := os.Symlink(real, dir); err != nil {
		t.Skipf("can't make a link here: %v", err)
	}
	r, err := Uninstall(context.Background(), sb.Env, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link is gone or changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "id")); err != nil {
		t.Errorf("what the link points to was touched: %v", err)
	}
	if s := r.String(); !strings.Contains(s, "which is a link or a file rather than a folder") {
		t.Errorf("report:\n%s", s)
	}

	// The same for its sessions folder, which holds a running server's record (from the second review).
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(sb.root, "sessions-elsewhere")
	if err := here.Publish(sessions, here.Session{ServerPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sessions, filepath.Join(dir, "sessions")); err != nil {
		t.Fatal(err)
	}
	if r, err = Uninstall(context.Background(), sb.Env, false); err != nil {
		t.Fatalf("%v\n%s", err, r)
	}
	if fi, err := os.Lstat(filepath.Join(dir, "sessions")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the sessions link is gone or changed: %v", err)
	}
	if left, _ := here.Sessions(sessions); len(left) != 1 {
		t.Errorf("the running server's record = %+v", left)
	}
	if s := r.String(); !strings.Contains(s, "whose sessions ("+filepath.Join(dir, "sessions")+") is a link or a file") || strings.Contains(s, "Removed "+brand.Product+"'s folder") {
		t.Errorf("report:\n%s", s)
	}
}
