package join

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withAgents gives the sandbox the other agents' directories, under its root, with none of them
// there yet.
func withAgents(sb *sandbox) {
	sb.OpenCodeDir = filepath.Join(sb.root, "config", "opencode")
	sb.MuseDir = filepath.Join(sb.root, "config", "muse")
	sb.PiDir = filepath.Join(sb.root, "pi", "agent")
	sb.GrokHome = filepath.Join(sb.root, "grok")
}

// OpenCode 2's JSONC config gets the server and the allow rule, its comments and layout kept; once.
func TestRegisterOpenCodeKeepsTheConfig(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "opencode v2.2.0")
	path := filepath.Join(sb.OpenCodeDir, "opencode.json")
	before := "{\n    // mine\n    \"$schema\":  \"https://opencode.ai/config.json\",\n    \"model\":  \"x/y\",\n}\n"
	write(t, path, before)
	h := sb.defaultHome(agents[agentIndex("opencode")])
	did, fresh, err := sb.registerOpenCodeAt(context.Background(), h)
	if err != nil || !fresh || len(did) != 3 { // the server, the rule, and the plugin (#431)
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	got := read(t, path)
	for _, want := range []string{"// mine", `"$schema":  "https://opencode.ai/config.json",`, `"type": "local"`, `"codemode": false`, `"--agent"`, `"opencode"`, `"action": "murmur_*"`} {
		if !strings.Contains(got, want) {
			t.Errorf("opencode.json lacks %s:\n%s", want, got)
		}
	}
	if _, fresh, err := sb.registerOpenCodeAt(context.Background(), h); err != nil || fresh || read(t, path) != got {
		t.Errorf("second: fresh=%v err=%v", fresh, err)
	}
	if r := openCodeRemoval(sb.Env, h); !strings.Contains(r, path) {
		t.Errorf("removal: %q", r)
	}
}

// A machine without OpenCode, or Muse, Pi or Grok, gets none of their files.
func TestOtherAgentsSkipAMachineWithoutThem(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	for _, d := range []string{"opencode", "muse", "pi", "grok"} {
		a := agents[agentIndex(d)]
		did, fresh, err := a.register(sb.Env, context.Background(), sb.defaultHome(a))
		if err != nil || fresh || len(did) != 0 {
			t.Errorf("%s: did=%q fresh=%v err=%v", d, did, fresh, err)
		}
	}
	for _, dir := range []string{sb.OpenCodeDir, sb.MuseDir, sb.PiDir, sb.GrokHome} {
		if exists(dir) {
			t.Errorf("created %s", dir)
		}
	}
}

func TestRegisterOpenCodeRefusesWhatItCannotEdit(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	path := filepath.Join(sb.OpenCodeDir, "opencode.jsonc")
	bad := "{\"mcp\": [], // a list\n}"
	write(t, path, bad)
	if _, _, err := sb.registerOpenCodeAt(context.Background(), sb.defaultHome(agents[agentIndex("opencode")])); err == nil {
		t.Error("no error")
	}
	if read(t, path) != bad {
		t.Error("changed a file it couldn't edit")
	}
}

func TestRegisterMuse(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	path := filepath.Join(sb.MuseDir, "settings.json")
	write(t, path, "{\n  \"schema_version\": 1,\n  \"tui\": {\n    \"foreign_context_notice_shown\": true\n  }\n}\n")
	h := sb.defaultHome(agents[agentIndex("muse")])
	if _, fresh, err := sb.registerMuseAt(h); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	got := read(t, path)
	for _, want := range []string{`"foreign_context_notice_shown": true`, `"mcpServers": {`, `"mode": "optional"`, `"T3CODE_CLI_PATH": "${T3CODE_CLI_PATH:-}"`, `"muse"`} {
		if !strings.Contains(got, want) {
			t.Errorf("settings.json lacks %s:\n%s", want, got)
		}
	}
	if _, fresh, _ := sb.registerMuseAt(h); fresh {
		t.Error("registered twice")
	}

	// The older key, beside which a new one would lose every server, is left for the user.
	legacy := `{"schema_version": 1, "mcp_servers": {"x": {"command": "x"}}}`
	write(t, path, legacy)
	if _, _, err := sb.registerMuseAt(h); err == nil || read(t, path) != legacy {
		t.Errorf("a file with mcp_servers: err %v\n%s", err, read(t, path))
	}
}

// Pi gets Murmur's extension, which names this binary, once; a file of the same name that isn't
// Murmur's is left alone.
func TestRegisterPi(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	if err := os.MkdirAll(sb.PiDir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := sb.defaultHome(agents[agentIndex("pi")])
	if _, fresh, err := sb.registerPiAt(h); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	path := piExtensionPath(sb.PiDir)
	got := read(t, path)
	for _, want := range []string{`pi.registerMcpServer("murmur"`, `MURMUR_PI_SESSION_FILE`, `"--agent", "pi"`, `exposure: "direct"`, `"session_start"`,
		// Messages (#205): the hook at Claude Code's moments, and the socket for a turn when idle.
		`MURMUR_PI_MESSAGING_SOCKET: sock`, `MURMUR_PI_MESSAGING_TOKEN: token`, `["hook", "--agent", "pi"]`,
		`hook("UserPromptSubmit"`, `hook("PostToolUse"`, `hook("Stop"`, `"tool_result"`, `"agent_before_settle"`,
		`deliverAs: "steer"`, `triggerTurn: true, deliverAs: "followUp"`, `continue: true`, `customType: "murmur"`,
		`"session_shutdown"`, `rec?.token !== key`,
		// Only in a clone the user joined (its consent file), and an interrupted end told as one.
		`"murmur", "joined.json"`, `if (!on || !file) return resolve(null)`,
		`"agent_settled"`, `event?.aborted`, `{ interrupted: true }`, `event === "Stop" ? undefined : ctx?.signal`,
		`Buffer.byteLength(p) <= 100`,
		// A session that was running when its user joined turns on then (the late join): the socket's
		// address is chosen as it starts and given to the MCP server, and the consent is looked for
		// again each second until it is there.
		`sock = sockPath(`, `watch = setInterval(turnOn, 1000)`, `if (on || !sock || !joined(cwd)) return`,
		`await listen(sock, token)`, `edits here are lost`} {
		if !strings.Contains(got, want) {
			t.Errorf("the extension lacks %s:\n%s", want, got)
		}
	}
	// It is also looked for at each prompt and tool call, before the hook would run.
	for _, event := range []string{`"before_agent_start"`, `"tool_result"`} {
		_, handler, _ := strings.Cut(got, `pi.on(`+event)
		handler, _, _ = strings.Cut(handler, "\n  });")
		look, hook := strings.Index(handler, "await turnOn();"), strings.Index(handler, "await hook(")
		if look < 0 || hook < 0 || look > hook {
			t.Errorf("%s doesn't look for the consent before it runs the hook:\n%s", event, handler)
		}
	}
	// The user's own words are never forged: nothing Murmur says goes in as the user's message.
	for _, bad := range []string{"sendUserMessage", "{{"} {
		if strings.Contains(got, bad) {
			t.Errorf("the extension has %s:\n%s", bad, got)
		}
	}
	// What the hook prints and what the socket takes are what package hook and package msgsock
	// write: the extension reads them by these names.
	for _, want := range []string{`hookSpecificOutput?.additionalContext`, `r?.decision !== "block"`, `r?.reason`,
		`rec?.type !== "auth"`, `rec?.type === "user"`, `rec?.message?.content`, `session_id: file, hook_event_name: event`} {
		if !strings.Contains(got, want) {
			t.Errorf("the extension lacks %s:\n%s", want, got)
		}
	}
	if !strings.Contains(strings.ReplaceAll(got, `\\`, `\`), sb.Exe) {
		t.Errorf("the extension doesn't run %s:\n%s", sb.Exe, got)
	}
	if _, fresh, _ := sb.registerPiAt(h); fresh {
		t.Error("wrote it twice")
	}
	write(t, path, "export default function () {}\n")
	if _, _, err := sb.registerPiAt(h); err == nil {
		t.Error("replaced someone else's file")
	}
}

// piBefore205 is the extension join wrote before #205, for the mur at exe.
func piBefore205(exe string) string {
	cmd, _ := json.Marshal(exe)
	return "// Written by `mur join` (Murmur, https://github.com/a-Fig/murmur). mur join writes it again when mur moves.\n" +
		"export default function (pi: any) {\n  const register = (file: string) =>\n" +
		"    pi.registerMcpServer(\"murmur\", {\n      command: " + string(cmd) + ",\n      args: [\"mcp\", \"--agent\", \"pi\"],\n" +
		"      exposure: \"direct\",\n      env: { MURMUR_PI_SESSION_FILE: file },\n    });\n  register(\"\");\n}\n"
}

// Pi's MCP server brings an extension join wrote for it up to its own, as an update doesn't run
// join again (#205), and leaves alone one that isn't join's, one that runs another mur, and one
// that is already its own.
func TestRefreshPi(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	exe := filepath.Join(t.TempDir(), exeName("mur"))
	other := filepath.Join(t.TempDir(), exeName("mur"))
	for _, f := range []string{exe, other} {
		write(t, f, "binary")
	}
	path := piExtensionPath(dir)
	if did, err := refreshPiAt(dir, exe); did || err != nil {
		t.Fatalf("with no extension: did=%v err=%v", did, err)
	}
	write(t, path, piBefore205(exe))
	if did, err := refreshPiAt(dir, exe); !did || err != nil || read(t, path) != piExtension(exe) {
		t.Fatalf("an old one: did=%v err=%v\n%s", did, err, read(t, path))
	}
	if did, _ := refreshPiAt(dir, exe); did {
		t.Error("rewrote its own")
	}
	for name, text := range map[string]string{
		"another mur's":     piBefore205(other),
		"someone else's":    "export default function (pi: any) {}\n",
		"with no mur in it": "// Written by `mur join` (Murmur).\nexport default function () {}\n",
	} {
		write(t, path, text)
		if did, err := refreshPiAt(dir, exe); did || err != nil || read(t, path) != text {
			t.Errorf("%s: did=%v err=%v", name, did, err)
		}
	}
}

func TestRegisterGrok(t *testing.T) {
	sb := newSandbox(t, false)
	withAgents(sb)
	path := filepath.Join(sb.GrokHome, "config.toml")
	before := "# mine\n[permission]\nallow = [\"Bash(ls)\"] # kept\n"
	write(t, path, before)
	h := sb.defaultHome(agents[agentIndex("grok")])
	did, fresh, err := sb.registerGrokAt(h)
	if err != nil || !fresh || len(did) != 3 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	hooks := read(t, filepath.Join(sb.GrokHome, "hooks", "murmur.json"))
	for _, want := range []string{`"PostToolUse"`, `"Stop"`, ` hook grok"`, `"timeout": 30`} {
		if !strings.Contains(hooks, want) {
			t.Errorf("the hook file lacks %s:\n%s", want, hooks)
		}
	}
	if strings.Contains(hooks, "matcher") || strings.Contains(hooks, "UserPromptSubmit") {
		t.Errorf("the hook file:\n%s", hooks)
	}
	got := read(t, path)
	want := "# mine\n[permission]\nallow = [\"Bash(ls)\", \"MCPTool(murmur__*)\"] # kept\n\n[mcp_servers.murmur]\ncommand = " + tomlString(sb.Exe) + "\nargs = [\"mcp\", \"--agent\", \"grok\"]\n"
	if got != want {
		t.Errorf("config.toml:\n%s\nwant:\n%s", got, want)
	}
	if did, fresh, err := sb.registerGrokAt(h); err != nil || fresh || len(did) != 0 {
		t.Errorf("second: did=%q fresh=%v err=%v", did, fresh, err)
	}
	if r := grokRemoval(sb.Env, h); !strings.Contains(r, path) || !strings.Contains(r, "murmur.json") {
		t.Errorf("removal: %q", r)
	}
}

func TestAddTOMLListItem(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"", "[permission]\nallow = [\"x\"]\n"},
		{"a = 1\n", "a = 1\n\n[permission]\nallow = [\"x\"]\n"},
		{"[permission]\nmode = \"y\"\n\n[other]\n", "[permission]\nmode = \"y\"\nallow = [\"x\"]\n\n[other]\n"},
		{"[permission]\r\nallow = []\r\n", "[permission]\r\nallow = [\"x\"]\r\n"},
	} {
		got, changed, err := addTOMLListItem(tc.src, "permission", "allow", "x", "config.toml")
		if err != nil || !changed || got != tc.want {
			t.Errorf("%q: got %q (%v, %v), want %q", tc.src, got, changed, err, tc.want)
		}
	}
	for _, bad := range []string{"permission.allow = [\"y\"]\n", "permission = {allow = []}\n", "[permission]\nallow = [\n  \"y\",\n]\n"} {
		if _, _, err := addTOMLListItem(bad, "permission", "allow", "x", "config.toml"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// What join prints after adding Murmur to the other agents says when their sessions get it.
func TestOthersAdvice(t *testing.T) {
	got := othersAdvice([]string{"OpenCode", "Muse Code"})
	if !strings.Contains(got, "OpenCode sessions get its tools within seconds") || !strings.Contains(got, "Muse Code loads MCP servers only when a session starts") {
		t.Errorf("advice: %s", got)
	}
}
