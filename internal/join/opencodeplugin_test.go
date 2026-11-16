package join

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// openCodePluginFor is the plugin join writes for an OpenCode whose --version prints version, for
// the mur at exe: "" where it writes none.
func openCodePluginFor(t *testing.T, version, exe string) string {
	t.Helper()
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, version)
	sb.Exe = exe
	if _, _, err := sb.registerOpenCodeAt(context.Background(), openCodeHome(sb)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(openCodePluginPath(sb.OpenCodeDir))
	if err != nil {
		return ""
	}
	return string(b)
}

// Each OpenCode gets Murmur's plugin in its own version's shape beside its server: OpenCode 2's
// (#431), and OpenCode 1's (#446), which replaces one join wrote for an OpenCode 2 that was here
// before. Where the version can't be learned, a plugin join wrote stays as it is, and none is
// added. A plugin there that join didn't write is the user's, and stays.
func TestRegisterOpenCodeWritesThePluginForItsVersion(t *testing.T) {
	for _, version := range []string{"opencode v2.2.0", "1.19.5"} {
		sb := newSandbox(t, false)
		withAgents(sb)
		withOpenCode(t, sb, version)
		h := openCodeHome(sb)
		path := openCodePluginPath(sb.OpenCodeDir)
		v1 := version == "1.19.5"
		if v1 {
			write(t, path, openCodePlugin(sb.Exe)) // left by an OpenCode 2 that was here before
		}
		did, fresh, err := sb.registerOpenCodeAt(context.Background(), h)
		if err != nil || !fresh {
			t.Fatalf("%s: did=%q fresh=%v err=%v", version, did, fresh, err)
		}
		if !exists(path) {
			t.Fatalf("%s: no plugin written", version)
		}
		got := read(t, path)
		if !strings.HasPrefix(got, openCodeMarker) || !strings.Contains(strings.Join(did, "\n"), path) {
			t.Errorf("%s: the plugin, or the report of it: %q\n%s", version, did, got)
		}
		if v1 {
			// OpenCode 1's shape: a default export with server(), and no setup(), which 1.19.5 would
			// run as well.
			if got == openCodePlugin(sb.Exe) || !strings.Contains(got, "async server(input)") || strings.Contains(got, "setup(") ||
				!strings.Contains(got, "MURMUR_OPENCODE_SESSION") || !strings.Contains(got, `const mur = "`) {
				t.Errorf("%s: not OpenCode 1's plugin:\n%s", version, got)
			}
		} else if got != openCodePlugin(sb.Exe) {
			t.Errorf("%s: not OpenCode 2's plugin:\n%s", version, got)
		}
		if strings.Contains(got, "{{") {
			t.Errorf("%s: a name was left unfilled:\n%s", version, got)
		}
		if _, fresh, err := sb.registerOpenCodeAt(context.Background(), h); err != nil || fresh {
			t.Errorf("%s: a second join: fresh=%v err=%v", version, fresh, err)
		}
		if !strings.Contains(openCodeRemoval(sb.Env, h), path) {
			t.Errorf("%s: the removal doesn't name the plugin: %s", version, openCodeRemoval(sb.Env, h))
		}

		mine := "export default { id: \"mine\", setup() {} };\n"
		write(t, path, mine)
		if _, _, err := sb.registerOpenCodeAt(context.Background(), h); err == nil || read(t, path) != mine {
			t.Errorf("%s: a plugin of the user's own: err=%v\n%s", version, err, read(t, path))
		}
	}

	// A version join can't learn: each version's plugin is an error to the other.
	sb := newSandbox(t, false)
	withAgents(sb)
	withOpenCode(t, sb, "")
	h := openCodeHome(sb)
	path := openCodePluginPath(sb.OpenCodeDir)
	if _, _, err := sb.registerOpenCodeAt(context.Background(), h); err != nil || exists(path) {
		t.Fatalf("unknown version: err=%v, plugin written=%v", err, exists(path))
	}
	write(t, path, openCodePlugin(sb.Exe))
	if did, _, err := sb.registerOpenCodeAt(context.Background(), h); err != nil || read(t, path) != openCodePlugin(sb.Exe) ||
		strings.Contains(strings.Join(did, "\n"), path) {
		t.Errorf("unknown version, with a plugin there: did=%q err=%v", did, err)
	}
}

// Uninstall deletes the plugin join wrote, and the plugins folder if that empties it; a plugin of
// the user's own at the same path stays, with a note.
func TestUninstallOpenCodeRemovesThePlugin(t *testing.T) {
	sb, _ := uninstallSandbox(t)
	h := openCodeHome(sb)
	path := openCodePluginPath(sb.OpenCodeDir)
	write(t, path, openCodePlugin(sb.Exe))
	did, notes, err := sb.unregisterOpenCodeAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 1 || len(notes) != 0 || exists(path) || exists(filepath.Dir(path)) {
		t.Fatalf("did=%q notes=%q err=%v, plugin there=%v", did, notes, err, exists(path))
	}

	other := filepath.Join(filepath.Dir(path), "theirs.js")
	write(t, other, "export default {};\n")
	mine := "export default { id: \"mine\", setup() {} };\n"
	write(t, path, mine)
	did, notes, err = sb.unregisterOpenCodeAt(context.Background(), []agentHome{h})
	if err != nil || len(did) != 0 || len(notes) != 1 || read(t, path) != mine || !exists(other) {
		t.Errorf("a plugin of the user's own: did=%q notes=%q err=%v", did, notes, err)
	}
}

// A plugin from an older mur is rewritten as its MCP server starts, if join wrote it and it runs
// this binary, with the plugin for the same version of OpenCode.
func TestRefreshOpenCode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "opencode")
	exe := filepath.Join(t.TempDir(), exeName("mur"))
	other := filepath.Join(t.TempDir(), exeName("mur"))
	for _, f := range []string{exe, other} {
		write(t, f, "binary")
	}
	path := openCodePluginPath(dir)
	if did, err := refreshOpenCodeAt(dir, exe, 0); did || err != nil {
		t.Fatalf("with no plugin: did=%v err=%v", did, err)
	}
	old := strings.Replace(openCodePlugin(exe), `"steer"`, `"queue"`, 1)
	if old == openCodePlugin(exe) {
		t.Fatal("the stand-in for an older plugin is the plugin")
	}
	write(t, path, old)
	if did, err := refreshOpenCodeAt(dir, exe, 0); !did || err != nil || read(t, path) != openCodePlugin(exe) {
		t.Fatalf("an old one: did=%v err=%v", did, err)
	}
	if did, _ := refreshOpenCodeAt(dir, exe, 0); did {
		t.Error("rewrote its own")
	}

	// OpenCode 1's is rewritten with OpenCode 1's.
	v1 := openCodePluginFor(t, "1.19.5", exe)
	if v1 == "" {
		t.Fatal("join wrote no plugin for OpenCode 1")
	}
	write(t, path, strings.Replace(v1, `"busy"`, `"retry"`, 1))
	if did, err := refreshOpenCodeAt(dir, exe, 0); !did || err != nil || read(t, path) != v1 {
		t.Fatalf("an old OpenCode 1 one: did=%v err=%v\n%s", did, err, read(t, path))
	}
	if did, _ := refreshOpenCodeAt(dir, exe, 0); did {
		t.Error("rewrote OpenCode 1's own")
	}
	// A line of its own says which it is, whatever the words around it, and that line never changes:
	// files written by older binaries carry it.
	const line = "\n// For OpenCode 1.\n"
	head, rest, ok := strings.Cut(v1, line)
	_, body, ok2 := strings.Cut(rest, "\nimport ")
	if !ok || !ok2 {
		t.Fatalf("OpenCode 1's plugin has no line %q before its imports", line)
	}
	write(t, path, head+line+"// An older plugin's words.\nimport "+body)
	if did, err := refreshOpenCodeAt(dir, exe, 0); !did || err != nil || read(t, path) != v1 {
		t.Fatalf("a reworded OpenCode 1 one: did=%v err=%v\n%s", did, err, read(t, path))
	}

	// The OpenCode that started the server says which it loads: an upgrade from 1 to 2 moves the
	// file to 2's, and back.
	if did, err := refreshOpenCodeAt(dir, exe, 2); !did || err != nil || read(t, path) != openCodePlugin(exe) {
		t.Fatalf("OpenCode 1's under OpenCode 2: did=%v err=%v\n%s", did, err, read(t, path))
	}
	if did, err := refreshOpenCodeAt(dir, exe, 1); !did || err != nil || read(t, path) != v1 {
		t.Fatalf("OpenCode 2's under OpenCode 1: did=%v err=%v\n%s", did, err, read(t, path))
	}

	for name, text := range map[string]string{
		"another mur's":     strings.Replace(openCodePlugin(other), `"steer"`, `"queue"`, 1),
		"someone else's":    "export default { id: \"x\", setup() {} };\n",
		"with no mur in it": openCodeMarker + " (Murmur).\nexport default {};\n",
	} {
		write(t, path, text)
		if did, err := refreshOpenCodeAt(dir, exe, 0); did || err != nil || read(t, path) != text {
			t.Errorf("%s: did=%v err=%v", name, did, err)
		}
	}
}

// TestRefreshOpenCodeAsksItsClient: the refresh as the MCP server starts asks the OpenCode that
// started it for its version, so OpenCode 1's plugin left from before an upgrade becomes OpenCode
// 2's, and one whose client isn't known keeps its own.
func TestRefreshOpenCodeAsksItsClient(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	dir := filepath.Join(config, "opencode")
	path := openCodePluginPath(dir)
	client := filepath.Join(t.TempDir(), exeName("opencode"))
	var asked []string
	e := Env{Run: func(_ context.Context, name string, args ...string) (string, error) {
		asked = append(asked, name+" "+strings.Join(args, " "))
		return "opencode v2.3.0\n", nil
	}}
	old := strings.Replace(openCode1Plugin(exe), `"busy"`, `"retry"`, 1)

	write(t, path, old)
	if did, err := e.refreshOpenCode(context.Background(), ""); !did || err != nil || read(t, path) != openCode1Plugin(exe) {
		t.Fatalf("with no client: did=%v err=%v\n%s", did, err, read(t, path))
	}
	if len(asked) != 0 {
		t.Errorf("with no client, ran %q", asked)
	}

	write(t, path, old)
	if did, err := e.refreshOpenCode(context.Background(), client); !did || err != nil || read(t, path) != openCodePlugin(exe) {
		t.Fatalf("under OpenCode 2.3.0: did=%v err=%v\n%s", did, err, read(t, path))
	}
	if want := client + " --version"; len(asked) != 1 || asked[0] != want {
		t.Errorf("ran %q, want %q", asked, want)
	}
}

// openCodeHarness loads the plugin the way OpenCode does, with a stand-in for OpenCode's API and a
// stand-in mur that writes down what it was given and answers by the event. It runs the moments
// the plugin acts on, first in a clone nobody joined, then joined, and prints what it saw.
const openCodeHarness = `
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";
const [plugin, repo, log] = process.argv.slice(2);
const { default: p } = await import(pathToFileURL(plugin).href);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const hooks = {}, said = [], queue = [];
let wake = null;
const emit = (type, data) => { queue.push({ type, data }); wake?.(); };
const ctx = {
  location: { directory: repo },
  tool: { hook: (name, cb) => (hooks[name] = cb) },
  session: {
    get: async ({ sessionID }) => (sessionID === "ses_old_child" ? { id: sessionID, parentID: "ses_parent" } : { id: sessionID }),
    synthetic: async (a) => { said.push(a); return {}; },
  },
  event: {
    subscribe: () => ({
      async *[Symbol.asyncIterator]() {
        for (;;) {
          while (queue.length) yield queue.shift();
          await new Promise((r) => (wake = r));
        }
      },
    }),
  },
};
const stop = p.setup(ctx);
console.log("id:", p.id);
const calls = () => (fs.existsSync(log) ? fs.readFileSync(log, "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : []);

await hooks["execute.after"]({ sessionID: "ses_a", tool: "read", status: "completed" });
emit("session.execution.succeeded", { sessionID: "ses_a" });
await sleep(300);
console.log("hook runs before the join:", calls().length);

fs.mkdirSync(path.join(repo, ".git", "murmur"), { recursive: true });
fs.writeFileSync(path.join(repo, ".git", "murmur", "joined.json"), "{}");
await hooks["execute.after"]({ sessionID: "ses_a", tool: "read", status: "completed" });
console.log("after a tool call:", JSON.stringify(said.splice(0)));
emit("session.execution.started", { sessionID: "ses_a" });
emit("session.execution.succeeded", { sessionID: "ses_a" });
emit("session.execution.interrupted", { sessionID: "ses_a", reason: "user" });
emit("session.created", { sessionID: "ses_child", parentID: "ses_a" });
emit("session.created", { sessionID: "ses_b" });
emit("session.execution.interrupted", { sessionID: "ses_b", reason: "shutdown" });
emit("mcp.status.changed", {});
for (let i = 0; i < 100 && calls().length < 5; i++) await sleep(100);
await sleep(300);
console.log("after the events:", JSON.stringify(said.splice(0)));
await hooks["execute.after"]({ sessionID: "ses_child", tool: "read", status: "completed" });
await hooks["execute.after"]({ sessionID: "ses_old_child", tool: "read", status: "completed" });
console.log("calls:", JSON.stringify(calls().map((c) => [c.hook_event_name, c.session_id, c.child, c.parent_native, Boolean(c.interrupted)])));
console.log("order of ses_a's:", calls().filter((c) => c.session_id === "ses_a").map((c) => c.hook_event_name + (c.interrupted ? "!" : "")).join(" "));
console.log("cwd:", calls().every((c) => c.cwd === repo));
stop();
await hooks["execute.after"]({ sessionID: "ses_a", tool: "read", status: "completed" });
console.log("calls after it stopped:", calls().length);
process.exit(0);
`

// openCodeFakeMur stands in for mur. The plugin is given node for its mur, so `mur hook --agent
// opencode` runs a file called hook in the directory the plugin runs it in, the repo: this one. It
// appends the payload to $MUR_FAKE_LOG and answers as the hook would: a notice after ses_a's tool
// calls, and something to go on with at a turn's end that wasn't interrupted.
const openCodeFakeMur = `
const fs = require("node:fs");
let payload = "";
process.stdin.on("data", (b) => (payload += b));
process.stdin.on("end", () => {
  fs.appendFileSync(process.env.MUR_FAKE_LOG, payload + "\n");
  const p = JSON.parse(payload);
  if (p.interrupted) return;
  if (p.hook_event_name === "Stop") process.stdout.write(JSON.stringify({ decision: "block", reason: "read this" }));
  else if (p.session_id === "ses_a" && p.hook_event_name === "PostToolUse")
    process.stdout.write(JSON.stringify({ hookSpecificOutput: { hookEventName: "PostToolUse", additionalContext: "a notice" } }));
});
`

// The plugin, run under Node with a stand-in for OpenCode (#431, #455): it runs no hook until its
// clone is joined; after a tool call it steers the hook's notice into the session; a turn's end
// that the hook keeps going is said the same way, which starts another turn; an interrupted one
// is told as interrupted and says nothing, and a closing server's isn't told; one session's hooks
// reach mur in the order of their events; and a session another session started is named a
// child with its parent on every request, at once when it is made, and when it was made before the
// plugin loaded.
func TestOpenCodePluginRunsTheHook(t *testing.T) {
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
	write(t, filepath.Join(repo, "hook"), openCodeFakeMur)
	plugin := filepath.Join(dir, "murmur.mjs")
	harness := filepath.Join(dir, "harness.mjs")
	log := filepath.Join(dir, "calls.jsonl")
	write(t, plugin, openCodePlugin(node))
	write(t, harness, openCodeHarness)
	cmd := exec.Command(node, harness, plugin, repo, log)
	cmd.Env = append(os.Environ(), "MUR_FAKE_LOG="+log)
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the harness: %v\n%s", err, b)
	}
	got := string(b)
	for _, want := range []string{
		"id: murmur",
		"hook runs before the join: 0",
		`after a tool call: [{"sessionID":"ses_a","text":"a notice","delivery":"steer"}]`,
		`after the events: [{"sessionID":"ses_a","text":"read this","delivery":"steer"}]`,
		"order of ses_a's: PostToolUse UserPromptSubmit Stop Stop!",
		`["PostToolUse","ses_a",false,"",false]`,
		`["UserPromptSubmit","ses_a",false,"",false]`,
		`["Stop","ses_a",false,"",false]`,
		`["Stop","ses_a",false,"",true]`,
		`["SessionStart","ses_child",true,"ses_a",false]`,
		`["PostToolUse","ses_child",true,"ses_a",false]`,
		`["PostToolUse","ses_old_child",true,"ses_parent",false]`,
		"cwd: true",
		"calls after it stopped: 7",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the harness didn't say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `"ses_b"`) {
		t.Errorf("a session a person started was reported as it was made, or a closing server's interrupt as a turn's end:\n%s", got)
	}
}
