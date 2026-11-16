package join

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
)

// openCode1Harness loads OpenCode 1's plugin the way OpenCode 1 does, a default export's
// server(input), with a stand-in for OpenCode's SDK client, and a stand-in mur that writes down what
// it was given and answers by the event. It runs the moments the plugin acts on, first in a clone
// nobody joined, then joined, then sends a turn on the socket as the daemon would, and prints what
// it saw, one fact a line.
const openCode1Harness = `
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import { pathToFileURL } from "node:url";
const [plugin, repo, log] = process.argv.slice(2);
const { default: p } = await import(pathToFileURL(plugin).href);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const prompts = [];
const client = {
  session: {
    get: async ({ path: { id } }) => ({ data: id === "ses_old_child" ? { id, parentID: "ses_a" } : { id } }),
    promptAsync: async ({ path: { id }, body }) => {
      const part = body.parts[0];
      prompts.push([id, part.text, Boolean(part.synthetic), body.agent ?? "", body.model?.modelID ?? ""]);
      return {};
    },
  },
};
console.log("id:", p.id, "setup:", typeof p.setup);
const h = await p.server({ client, directory: repo, worktree: repo, project: {}, serverUrl: new URL("http://localhost:4096") });
const cfg = { mcp: { murmur: { type: "local", command: ["mur"], environment: { KEEP: "1" } }, other: { type: "local" } } };
await h.config(cfg);
const env = cfg.mcp.murmur.environment;
const id = env.MURMUR_OPENCODE_SESSION;
console.log("env:", env.KEEP === "1" && /^[0-9a-f]{16}$/.test(id) && Boolean(env.MURMUR_OPENCODE_MESSAGING_SOCKET && env.MURMUR_OPENCODE_MESSAGING_TOKEN));
console.log("other server:", JSON.stringify(cfg.mcp.other));
const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : []);
const ev = (type, properties) => h.event({ event: { type, properties } });
const after = (sessionID) => h["tool.execute.after"]({ tool: "read", sessionID, callID: "c", args: {} }, { title: "", output: "x", metadata: {} });
const send = (token, text) =>
  new Promise((resolve) => {
    const c = net.connect(env.MURMUR_OPENCODE_MESSAGING_SOCKET, () => {
      c.end(JSON.stringify({ type: "auth", token }) + "\n" + JSON.stringify({ type: "user", message: { role: "user", content: text } }) + "\n");
    });
    c.on("close", () => resolve(true));
    c.on("error", () => resolve(false));
  });

await after("ses_a");
await ev("session.status", { sessionID: "ses_a", status: { type: "busy" } });
await ev("session.idle", { sessionID: "ses_a" });
console.log("hook runs before the join:", calls().length);
console.log("socket before the join:", await send(env.MURMUR_OPENCODE_MESSAGING_TOKEN, "too soon"));

fs.mkdirSync(path.join(repo, ".git", "murmur"), { recursive: true });
fs.writeFileSync(path.join(repo, ".git", "murmur", "joined.json"), "{}");
await ev("message.updated", { info: { role: "user", sessionID: "ses_a", agent: "plan", model: { providerID: "p", modelID: "m" } } });
await ev("session.created", { info: { id: "ses_child", parentID: "ses_a" } });
await ev("session.status", { sessionID: "ses_a", status: { type: "busy" } });
await ev("session.status", { sessionID: "ses_a", status: { type: "busy" } });
await after("ses_a");
await after("ses_child");
await after("ses_old_child");
await ev("session.status", { sessionID: "ses_child", status: { type: "busy" } });
await ev("session.idle", { sessionID: "ses_child" });
await ev("session.idle", { sessionID: "ses_a" });
await ev("session.status", { sessionID: "ses_a", status: { type: "busy" } });
await ev("session.error", { sessionID: "ses_a", error: { name: "MessageAbortedError" } });
await ev("session.idle", { sessionID: "ses_a" });
console.log("prompts:", JSON.stringify(prompts.splice(0)));
console.log("calls:", JSON.stringify(calls().map((c) => [c.hook_event_name, Boolean(c.interrupted)])));
console.log("all as the process's session:", calls().every((c) => c.session_id === id && c.cwd === repo && !c.child));

console.log("a wrong token:", await send("nope", "not this"), JSON.stringify(prompts.splice(0)));
await send(env.MURMUR_OPENCODE_MESSAGING_TOKEN, "a turn");
await sleep(200);
console.log("turns:", JSON.stringify(prompts.splice(0)));
await h.dispose();
await after("ses_a");
console.log("calls after dispose:", calls().length);
console.log("socket after dispose:", await send(env.MURMUR_OPENCODE_MESSAGING_TOKEN, "too late"), JSON.stringify(prompts));
// A server stopped by a signal exits without disposing of its plugins.
const h2 = await p.server({ client, directory: repo, worktree: repo, project: {}, serverUrl: new URL("http://localhost:4096") });
const cfg2 = { mcp: { murmur: { type: "local", command: ["mur"] } } };
await h2.config(cfg2);
await h2["tool.execute.after"]({ tool: "read", sessionID: "ses_z", callID: "c", args: {} }, { title: "", output: "x", metadata: {} });
console.log("second socket:", cfg2.mcp.murmur.environment.MURMUR_OPENCODE_MESSAGING_SOCKET);
process.exit(0);
`

// openCode1FakeMur stands in for mur, as openCodeFakeMur does: a notice after a tool call, and
// something to go on with at a turn's end that wasn't interrupted.
const openCode1FakeMur = `
const fs = require("node:fs");
let payload = "";
process.stdin.on("data", (b) => (payload += b));
process.stdin.on("end", () => {
  fs.appendFileSync(process.env.MUR_FAKE_LOG, payload + "\n");
  const p = JSON.parse(payload);
  if (p.interrupted) return;
  if (p.hook_event_name === "Stop") process.stdout.write(JSON.stringify({ decision: "block", reason: "read this" }));
  else if (p.hook_event_name === "PostToolUse")
    process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: "a notice" } }));
});
`

// OpenCode 1's plugin, run under Node with a stand-in for OpenCode 1 (#446, #205): it gives
// Murmur's server, and no other, an ID for the process and its socket, and every hook request
// carries that ID; it runs no hook and takes no turn until its clone is joined; a turn's start is
// told once; after a tool call it puts the hook's notice in the session as a synthetic prompt, with
// the agent and model of the session's last prompt; a turn's end that the hook keeps going is put
// in the same way, and one that came after an error is told as interrupted and says nothing; a
// session another session started, seen made or asked about, runs no hook; a turn on the socket
// goes to the session last at work, shown, and one with a wrong token goes nowhere; and once
// OpenCode disposes of it, nothing more runs and the socket is gone, as it is when the process
// exits without disposing of it.
func TestOpenCode1PluginRunsTheHook(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	if node, err = filepath.Abs(node); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "hook"), openCode1FakeMur)
	text := openCodePluginFor(t, "1.19.5", node)
	if text == "" {
		t.Fatal("join wrote no plugin for OpenCode 1")
	}
	plugin := filepath.Join(dir, "murmur.mjs")
	harness := filepath.Join(dir, "harness.mjs")
	log := filepath.Join(dir, "calls.jsonl")
	write(t, plugin, text)
	write(t, harness, openCode1Harness)
	cmd := exec.Command(node, harness, plugin, repo, log)
	cmd.Env = append(withoutT3(os.Environ()), "MUR_FAKE_LOG="+log)
	if runtime.GOOS != "windows" {
		cmd.Env = append(cmd.Env, "TMPDIR=/tmp")
	}
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		"id: murmur setup: undefined",
		"env: true",
		`other server: {"type":"local"}`,
		"hook runs before the join: 0",
		"socket before the join: false",
		`prompts: [["ses_a","a notice",true,"plan","m"],["ses_a","read this",true,"plan","m"]]`,
		`calls: [["UserPromptSubmit",false],["PostToolUse",false],["Stop",false],["UserPromptSubmit",false],["Stop",true]]`,
		"all as the process's session: true",
		"a wrong token: true []",
		`turns: [["ses_a","a turn",false,"plan","m"]]`,
		"calls after dispose: 5",
		"socket after dispose: false []",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the harness didn't say %q:\n%s", want, got)
		}
	}
	_, second, ok := strings.Cut(got, "second socket: ")
	if second = strings.TrimSpace(strings.SplitN(second, "\n", 2)[0]); !ok || second == "" {
		t.Fatalf("no second socket:\n%s", got)
	}
	if _, err := os.Stat(second); runtime.GOOS != "windows" && !os.IsNotExist(err) {
		t.Errorf("the socket's file outlived its process: %s (%v)", second, err)
	}
}

// withoutT3 is env without T3CODE_CLI_PATH, which a test run inside T3 Code has.
func withoutT3(env []string) []string {
	var out []string
	for _, kv := range env {
		if !strings.HasPrefix(strings.ToUpper(kv), "T3CODE_CLI_PATH=") {
			out = append(out, kv)
		}
	}
	return out
}

// openCode1T3Harness loads OpenCode 1's plugin in a joined clone, as OpenCode 1 under T3 Code
// would, and prints the hooks it gave and whether it ran mur or touched the config.
const openCode1T3Harness = `
import fs from "node:fs";
import { pathToFileURL } from "node:url";
const [plugin, repo, log] = process.argv.slice(2);
const { default: p } = await import(pathToFileURL(plugin).href);
const h = await p.server({ client: {}, directory: repo, worktree: repo, project: {}, serverUrl: new URL("http://localhost:4096") });
const cfg = { mcp: { murmur: { type: "local", command: ["mur"] } } };
await h.config?.(cfg);
await h["tool.execute.after"]?.({ tool: "read", sessionID: "ses_a", callID: "c", args: {} }, { title: "", output: "x", metadata: {} });
await h.event?.({ event: { type: "session.idle", properties: { sessionID: "ses_a" } } });
console.log("hooks:", JSON.stringify(Object.keys(h)));
console.log("config:", JSON.stringify(cfg));
console.log("ran mur:", fs.existsSync(log));
process.exit(0);
`

// Under T3 Code OpenCode 1's plugin does nothing (#446): no ID or socket for Murmur's server, no
// hook, so an OpenCode 1 thread is as it was before the plugin, and T3 hands it its messages.
func TestOpenCode1PluginIsOffInT3(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	if node, err = filepath.Abs(node); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git", brand.StateDir), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, ".git", brand.StateDir, consentFile), "{}")
	write(t, filepath.Join(repo, "hook"), openCode1FakeMur)
	text := openCodePluginFor(t, "1.19.5", node)
	if text == "" {
		t.Fatal("join wrote no plugin for OpenCode 1")
	}
	plugin := filepath.Join(dir, "murmur.mjs")
	harness := filepath.Join(dir, "harness.mjs")
	log := filepath.Join(dir, "calls.jsonl")
	write(t, plugin, text)
	write(t, harness, openCode1T3Harness)
	cmd := exec.Command(node, harness, plugin, repo, log)
	cmd.Env = append(withoutT3(os.Environ()), "MUR_FAKE_LOG="+log, "T3CODE_CLI_PATH="+filepath.Join(dir, "t3"))
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		"hooks: []",
		`config: {"mcp":{"murmur":{"type":"local","command":["mur"]}}}`,
		"ran mur: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the harness didn't say %q:\n%s", want, got)
		}
	}
}
