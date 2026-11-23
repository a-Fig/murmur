package join

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// piHarness loads the extension the way Pi does, with a stand-in for Pi's API, in a clone the user
// hasn't joined yet: it starts a session, joins (writes the consent), and then, without another
// event, connects to the socket the MCP server was told of and sends a turn, as the daemon would.
// It prints what it saw, one fact a line.
const piHarness = `
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
const [ext, repo] = process.argv.slice(2);
const { default: load } = await import(ext);
const handlers = {}, regs = [], sent = [];
load({
  registerMcpServer: (_name, cfg) => regs.push(cfg),
  on: (event, h) => (handlers[event] = h),
  sendMessage: (m, o) => sent.push({ m, o }),
});
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
await handlers.session_start({}, { cwd: repo, sessionManager: { getSessionFile: () => "/s/a.jsonl" } });
const env = regs.at(-1).env;
console.log("socket given:", Boolean(env.MURMUR_PI_MESSAGING_SOCKET && env.MURMUR_PI_MESSAGING_TOKEN));
console.log("listening before the join:", fs.existsSync(env.MURMUR_PI_MESSAGING_SOCKET));
fs.mkdirSync(path.join(repo, ".git", "murmur"), { recursive: true });
fs.writeFileSync(path.join(repo, ".git", "murmur", "joined.json"), "{}");
await sleep(2500);
console.log("listening after the join:", fs.existsSync(env.MURMUR_PI_MESSAGING_SOCKET));
await new Promise((resolve) => {
  const c = net.connect(env.MURMUR_PI_MESSAGING_SOCKET, () => {
    c.end(JSON.stringify({ type: "auth", token: env.MURMUR_PI_MESSAGING_TOKEN }) + "\n" +
      JSON.stringify({ type: "user", message: { role: "user", content: "a turn" } }) + "\n");
  });
  c.on("close", resolve);
  c.on("error", resolve);
});
await sleep(300);
console.log("turns:", JSON.stringify(sent.map((s) => [s.m.customType, s.m.content, s.o])));
// A connection made to this session's socket, which outlives the session (/new, /resume), starts no
// turn in the next one.
const old = net.connect(env.MURMUR_PI_MESSAGING_SOCKET);
let oldClosed = false;
old.on("error", () => {});
old.on("close", () => (oldClosed = true));
await new Promise((r) => old.on("connect", r));
old.write(JSON.stringify({ type: "auth", token: env.MURMUR_PI_MESSAGING_TOKEN }) + "\n");
await sleep(200);
await handlers.session_start({}, { cwd: repo, sessionManager: { getSessionFile: () => "/s/b.jsonl" } });
if (!old.destroyed) old.write(JSON.stringify({ type: "user", message: { role: "user", content: "for a" } }) + "\n");
await sleep(300);
console.log("turns after a switch:", sent.length);
console.log("the old connection closed:", oldClosed);
const envB = regs.at(-1).env;
handlers.session_shutdown();
console.log("socket left after shutdown:", fs.existsSync(env.MURMUR_PI_MESSAGING_SOCKET) || fs.existsSync(envB.MURMUR_PI_MESSAGING_SOCKET));
`

// A Pi session that was running when its user joined turns on by itself (#205, the re-review's
// R1): the extension gives the MCP server its socket's address from the start, opens it within
// a second or two of the consent appearing, with no prompt or tool call in between, and takes a
// turn on it. Before the consent the socket isn't open. It needs a Node that runs TypeScript
// (22.6 or later), as Pi's own does; without one the test is skipped.
func TestPiExtensionTurnsOnWhenItsCloneIsJoined(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the harness uses a unix socket")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	out, err := exec.Command(node, "--version").Output()
	m := regexp.MustCompile(`^v(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if err != nil || m == nil {
		t.Skip("node's version unknown")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 22 || major == 22 && minor < 6 {
		t.Skipf("node %s.%s can't run TypeScript", m[1], m[2])
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(dir, "murmur.mts")
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(ext, []byte(piExtension("/bin/false")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(piHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext, repo)
	cmd.Env = append(os.Environ(), "TMPDIR=/tmp")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		"socket given: true",
		"listening before the join: false",
		"listening after the join: true",
		`turns: [["murmur","a turn",{"triggerTurn":true,"deliverAs":"followUp"}]]`,
		"turns after a switch: 1",
		"the old connection closed: true",
		"socket left after shutdown: false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the harness didn't say %q:\n%s", want, got)
		}
	}
}

// piLoadHarness loads the extension with a stand-in for Pi's API and prints how many servers it
// registered and how many of Pi's events it listens for.
const piLoadHarness = `
import { pathToFileURL } from "node:url";
const { default: load } = await import(pathToFileURL(process.argv[2]).href);
let regs = 0, events = 0;
load({ registerMcpServer: () => regs++, on: () => events++, sendMessage: () => {} });
console.log("registered " + regs + ", listening for " + events);
`

// A subagent that the pi-subagents package runs as a pi of its own, marked in its environment, is
// kept off Murmur (#457): there the extension registers no server and listens for nothing, so the
// child is no session of its own. Any other pi gets the server and the events.
func TestPiExtensionStaysOutOfASubagent(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	out, err := exec.Command(node, "--version").Output()
	m := regexp.MustCompile(`^v(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if err != nil || m == nil {
		t.Skip("node's version unknown")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 22 || major == 22 && minor < 6 {
		t.Skipf("node %s.%s can't run TypeScript", m[1], m[2])
	}
	dir := t.TempDir()
	ext := filepath.Join(dir, "murmur.mts")
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(ext, []byte(piExtension("/bin/false")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(piLoadHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(child string) string {
		cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext)
		cmd.Env = append(os.Environ(), "PI_SUBAGENT_CHILD="+child)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("the harness: %v\n%s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if got := run("1"); got != "registered 0, listening for 0" {
		t.Errorf("in a subagent: %s", got)
	}
	if got := run(""); !strings.HasPrefix(got, "registered 1, listening for ") || strings.HasSuffix(got, " 0") {
		t.Errorf("in a pi of the user's: %s", got)
	}
}

// piCallHarness loads the extension in a joined clone, with node standing in for mur: the
// extension runs `<node> hook --agent pi` in the clone, which runs the clone's file named hook,
// and that file keeps each payload it is given. The harness then reports tool calls as Pi
// would and prints what the hook was given of each.
const piCallHarness = `
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
const [ext, repo] = process.argv.slice(2);
fs.mkdirSync(path.join(repo, ".git", "murmur"), { recursive: true });
fs.writeFileSync(path.join(repo, ".git", "murmur", "joined.json"), "{}");
fs.writeFileSync(path.join(repo, "hook"),
  "let s = ''; process.stdin.on('data', (b) => (s += b)); process.stdin.on('end', () => require('fs').appendFileSync('calls.jsonl', s + String.fromCharCode(10)));");
const { default: load } = await import(pathToFileURL(ext).href);
const handlers = {};
load({ registerMcpServer: () => {}, on: (event, h) => (handlers[event] = h), sendMessage: () => {} });
await handlers.session_start({}, { cwd: repo, sessionManager: { getSessionFile: () => "/s/a.jsonl" } });
const out = [{ type: "text", text: "https://github.com/a-Fig/murmur/issues/7\n" }];
await handlers.tool_call({ toolName: "bash", toolCallId: "call_7", input: { command: "gh issue create -t x" } }, { cwd: repo });
await handlers.tool_call({ toolName: "read", toolCallId: "call_8", input: { path: "a.go" } }, { cwd: repo });
await handlers.tool_call({ toolName: "mcp__github__issue_write", toolCallId: "call_9", input: {} }, { cwd: repo });
await handlers.tool_result({ toolName: "bash", toolCallId: "call_7", input: { command: "gh issue create -t x" }, content: out }, { cwd: repo });
await handlers.tool_result({ toolName: "read", toolCallId: "call_8", input: { path: "a.go" }, content: out }, { cwd: repo });
const denied = [{ type: "text", text: JSON.stringify({ message: "Not Found", url: "https://github.com/a-Fig/murmur/issues/7" }) }];
await handlers.tool_result({ toolName: "mcp__github__issue_write", input: { method: "update", issue_number: 7, state: "closed" }, content: denied, isError: true }, { cwd: repo });
await handlers.tool_result({ toolName: "bash", input: { command: "cat big" }, content: [{ type: "text", text: "x".repeat(600 * 1024) }] }, { cwd: repo });
await handlers.tool_result({ toolName: "bash", toolCallId: "call_10", input: { command: "cat big" }, content: [{ type: "text", text: "x".repeat(600 * 1024) }] }, { cwd: repo });
await handlers.session_shutdown?.();
for (const line of fs.readFileSync(path.join(repo, "calls.jsonl"), "utf8").split("\n").filter(Boolean)) {
  const p = JSON.parse(line);
  console.log(p.hook_event_name, JSON.stringify({ name: p.tool_name, input: p.tool_input, response: p.tool_response, id: p.tool_use_id }));
}
`

// After a shell or MCP call, the extension gives the hook the call's name, input and output, as
// Claude Code's hook is given them, so an issue a Pi session files is credited to it (#510, #864).
// The output carries Pi's isError, so a failed close isn't credited (TestIssuesACallFiledOrClosed).
// Not for other tools, whose output the hook never reads, nor for a call too big for the hook,
// which reads at most 1 MiB; their hook still runs, for the notice. A shell call is also announced
// before it runs, and carries Pi's ID for the call at both ends, so the daemon can credit the
// commits it makes (#868); no other call is. A shell call whose output is too big still carries
// its ID, or its start would never be closed.
func TestPiExtensionGivesTheHookAShellCall(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	out, err := exec.Command(node, "--version").Output()
	m := regexp.MustCompile(`^v(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if err != nil || m == nil {
		t.Skip("node's version unknown")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 22 || major == 22 && minor < 6 {
		t.Skipf("node %s.%s can't run TypeScript", m[1], m[2])
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(dir, "murmur.mts")
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(ext, []byte(piExtension(node)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harness, []byte(piCallHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "--experimental-strip-types", "--no-warnings", harness, ext, repo)
	cmd.Env = append(os.Environ(), "PI_SUBAGENT_CHILD=")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n")
	want := []string{
		`PreToolUse {"name":"bash","id":"call_7"}`,
		`PostToolUse {"name":"bash","input":{"command":"gh issue create -t x"},"response":{"content":[{"type":"text","text":"https://github.com/a-Fig/murmur/issues/7\n"}],"isError":false},"id":"call_7"}`,
		`PostToolUse {}`,
		`PostToolUse {"name":"mcp__github__issue_write","input":{"method":"update","issue_number":7,"state":"closed"},"response":{"content":[{"type":"text","text":"{\"message\":\"Not Found\",\"url\":\"https://github.com/a-Fig/murmur/issues/7\"}"}],"isError":true}}`,
		`PostToolUse {}`,
		`PostToolUse {"name":"bash","id":"call_10"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the hook was given:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
