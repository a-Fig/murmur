package join

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/msgsock"
)

// hermesDeliverHarness loads the plugin as Hermes does, with a stand-in for gateway.session_context
// that gives the session key of the session at work, and runs its hooks as Hermes fires them (#1184):
// a turn's start, a terminal call and another tool's call, the turn's end, an interrupted turn's end,
// a subagent's turn. It prints what the plugin gave back and what it injected, one fact a line. Then
// it prints the socket the plugin opened for the session and waits for the test to write to it, and
// closes the session as Hermes does.
const hermesDeliverHarness = `
import importlib.util, json, sys, time, types
gw = types.ModuleType("gateway"); sc = types.ModuleType("gateway.session_context")
KEY = [""]
sc.bound_session_key = lambda: KEY[0]
sc.get_session_env = lambda name, default="": default
gw.session_context = sc
sys.modules["gateway"] = gw; sys.modules["gateway.session_context"] = sc
spec = importlib.util.spec_from_file_location("murmur_plugin", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
injected = []
class Ctx:
    def __init__(self, cwd):
        self.tools, self.hooks, self.cwd = {}, {}, cwd
    def register_tool(self, name, toolset, schema, handler, **kw):
        self.tools[name] = handler
    def register_hook(self, name, cb):
        self.hooks[name] = cb
    def session_cwd(self):
        return self.cwd
    def inject_message(self, content, role="user", session_key=None, origin=None):
        injected.append((content, session_key))
        return "REFUSE" not in content
ctx = Ctx(sys.argv[2])
m.register(ctx)
h = ctx.hooks
def say(*a):
    print(*a, flush=True)
say("hooks:", ",".join(sorted(k for k in h if k not in ("subagent_start", "subagent_stop"))))
KEY[0] = "agent:main:local:dm:local-1"
say("start:", json.dumps(h["pre_llm_call"](session_id="local-1", user_message="hello there", platform="cli")))
say("pre:", h["pre_tool_call"](tool_name="terminal", args={"command": "git commit"}, session_id="local-1", tool_call_id="c1"))
say("terminal:", json.dumps(h["transform_tool_result"](tool_name="terminal", args={}, result="OUT", session_id="local-1", tool_call_id="c1", status="ok")))
say("read:", json.dumps(h["transform_tool_result"](tool_name="read_file", args={}, result="FILE", session_id="local-1", tool_call_id="c2", status="ok")))
h["on_session_end"](session_id="local-1", completed=True, interrupted=False, failed=False)
say("end injected:", json.dumps(injected))
del injected[:]
h["on_session_end"](session_id="local-1", completed=False, interrupted=True, failed=False)
say("interrupted injected:", json.dumps(injected))
h["subagent_start"](parent_session_id="local-1", child_session_id="kid-1")
KEY[0] = "agent:main:local:dm:kid-1"
say("child start:", json.dumps(h["pre_llm_call"](session_id="kid-1", user_message="sub task", platform="cli")))
KEY[0] = ""
addr, token = m._socket_of("local-1")
say("socket:", json.dumps([addr, token]))
sys.stdin.readline()
for _ in range(100):
    if injected:
        break
    time.sleep(0.05)
say("socket injected:", json.dumps(injected))
del injected[:]
h["on_session_finalize"](session_id="local-1", platform="cli")
say("finalized:", json.dumps(m._socket_of("local-1")))
sys.stdin.readline()
# A socket Hermes refuses a turn through closes only itself, not one the session opened after it.
old = m._open_socket(ctx, "local-2")
m._close_socket("local-2")
new = m._open_socket(ctx, "local-2")
m._keys["local-2"] = "agent:main:local:dm:local-2"
old._inject("REFUSE this")
say("stale refused:", json.dumps([m._socket_of("local-2")[0] == new.addr, old.closed, new.closed]))
m._close_socket("local-2")
# Where Hermes doesn't let the plugin start turns, it opens no socket for the session.
ctx2 = Ctx(sys.argv[2])
ctx2._gateway_injection_allowed = lambda: False
env = m._env(ctx2, "local-3", sys.argv[2])
say("no grant:", json.dumps(["MURMUR_HERMES_SOCKET" in env, m._socket_of("local-3")[0]]))
`

// hermesDeliverStandIn is the mur the plugin runs: it records each hook's event, session, call and
// the socket it was given, one JSON line to the file its first argument names, and answers each
// event as `mur hook` would.
const hermesDeliverStandIn = `
import json, os, sys
args = sys.argv[2:]
if args == ["tool", "--list"]:
    print("[]"); sys.exit(0)
p = json.loads(sys.stdin.read() or "{}")
with open(sys.argv[1], "a", encoding="utf-8") as f:
    f.write(json.dumps({"argv": args, "event": p.get("hook_event_name"), "session": p.get("session_id"),
        "call": p.get("tool_use_id"), "tool": p.get("tool_name"), "prompt": p.get("prompt"), "cwd": os.path.basename(p.get("cwd") or ""),
        "interrupted": p.get("interrupted"), "socket": bool(os.environ.get("MURMUR_HERMES_SOCKET")),
        "token": bool(os.environ.get("MURMUR_HERMES_TOKEN"))}) + "\n")
ev = p.get("hook_event_name")
if ev in ("UserPromptSubmit", "PostToolUse"):
    print(json.dumps({"hookSpecificOutput": {"hookEventName": ev, "additionalContext": "NOTICE " + ev}}))
elif ev == "Stop" and not p.get("interrupted"):
    print(json.dumps({"decision": "block", "reason": "WHOLE AT THE END"}))
`

// The plugin brings a Hermes session its messages as Claude Code's hook does (#1184): a notice as
// context when a turn starts, a notice after a tool's result, nothing put in at a turn's end (the
// daemon hands what is unread through the socket instead), nothing for a subagent, a terminal call
// bracketed for commit credit; and a socket for the session that takes Claude Code's messaging-socket
// lines, written by Murmur's own client, until the session closes, where Hermes lets the plugin start
// turns. Skipped where no Python is on PATH.
func TestHermesPluginDeliversInPython(t *testing.T) {
	py := hermesPython(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record.jsonl")
	exe := hermesStandInMur(t, py, dir, hermesDeliverStandIn, record)
	plugin := filepath.Join(dir, "plugins", "murmur")
	if _, err := writeHermesPlugin(plugin, exe); err != nil {
		t.Fatal(err)
	}
	// A clone the user joined: the plugin runs hooks and opens a socket only in one.
	folder := filepath.Join(dir, "the-repo")
	write(t, filepath.Join(folder, ".git", brand.StateDir, consentFile), "{}")
	harness := filepath.Join(dir, "harness.py")
	write(t, harness, hermesDeliverHarness)
	cmd := exec.Command(py, "-I", harness, filepath.Join(plugin, "__init__.py"), folder)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	got := map[string]string{}
	next := func(want string) string {
		t.Helper()
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("the harness stopped before %q:\n%s", want, stderr.String())
			}
			k, v, _ := strings.Cut(l, ": ")
			if k+":" != want {
				t.Fatalf("got %q, want %s ...\n%s", l, want, stderr.String())
			}
			got[k] = v
			return v
		case <-time.After(60 * time.Second):
			t.Fatalf("no %q:\n%s", want, stderr.String())
		}
		return ""
	}
	for _, k := range []string{"hooks:", "start:", "pre:", "terminal:", "read:", "end injected:", "interrupted injected:", "child start:"} {
		next(k)
	}
	for k, want := range map[string]string{
		"hooks":                "on_session_delete,on_session_end,on_session_finalize,pre_llm_call,pre_tool_call,transform_tool_result",
		"start":                `{"context": "NOTICE UserPromptSubmit"}`,
		"pre":                  "None",
		"end injected":         "[]",
		"interrupted injected": "[]",
		"child start":          "null",
	} {
		if got[k] != want {
			t.Errorf("%s: %s, want %s", k, got[k], want)
		}
	}
	for _, k := range []string{"terminal", "read"} {
		if !strings.Contains(got[k], "NOTICE PostToolUse") || !strings.HasPrefix(got[k], `"`+map[string]string{"terminal": "OUT", "read": "FILE"}[k]) {
			t.Errorf("%s: %s, want the result and the notice after it", k, got[k])
		}
	}

	addr, token := "", ""
	if v := next("socket:"); true {
		parts := strings.Split(strings.Trim(v, "[]"), ", ")
		if len(parts) != 2 {
			t.Fatalf("socket: %s", v)
		}
		addr, token = strings.Trim(parts[0], `"`), strings.Trim(parts[1], `"`)
		addr = strings.ReplaceAll(addr, `\\`, `\`)
	}
	if err := msgsock.Send(context.Background(), addr, token, "A TURN FROM A TEAMMATE"); err != nil {
		t.Fatalf("writing to the plugin's socket: %v", err)
	}
	stdin.Write([]byte("sent\n"))
	if v := next("socket injected:"); v != `[["A TURN FROM A TEAMMATE", "agent:main:local:dm:local-1"]]` {
		t.Errorf("the socket's turn went in as %s", v)
	}
	if v := next("finalized:"); v != `[null, null]` {
		t.Errorf("after the session closed its socket is %s", v)
	}
	if err := msgsock.Send(context.Background(), addr, token, "too late"); !errors.Is(err, msgsock.ErrNotConnected) {
		t.Errorf("writing to a closed session's socket: %v, want it not to connect", err)
	}
	stdin.Write([]byte("done\n"))
	if v := next("stale refused:"); v != "[true, true, false]" {
		t.Errorf("after a refusal through an old socket, [the session keeps its new socket, the old is closed, the new is closed]: %s", v)
	}
	if v := next("no grant:"); v != "[false, null]" {
		t.Errorf("without Hermes' grant, [a socket in the hook's env, the session's socket]: %s", v)
	}

	// What mur was asked: each hook as `mur hook --agent hermes`, with the session's socket; the
	// terminal call bracketed and the other not; the interrupted end said so; nothing for the child.
	b, _ := os.ReadFile(record)
	rec := strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n")) // Python's text mode on Windows
	want := []string{
		`{"argv": ["hook", "--agent", "hermes"], "event": "UserPromptSubmit", "session": "local-1", "call": null, "tool": null, "prompt": "hello there", "cwd": "the-repo", "interrupted": null, "socket": true, "token": true}`,
		`{"argv": ["hook", "--agent", "hermes"], "event": "PreToolUse", "session": "local-1", "call": "c1", "tool": "terminal", "prompt": null, "cwd": "the-repo", "interrupted": null, "socket": true, "token": true}`,
		`{"argv": ["hook", "--agent", "hermes"], "event": "PostToolUse", "session": "local-1", "call": "c1", "tool": "terminal", "prompt": null, "cwd": "the-repo", "interrupted": null, "socket": true, "token": true}`,
		`{"argv": ["hook", "--agent", "hermes"], "event": "PostToolUse", "session": "local-1", "call": null, "tool": "read_file", "prompt": null, "cwd": "the-repo", "interrupted": null, "socket": true, "token": true}`,
		`{"argv": ["hook", "--agent", "hermes"], "event": "Stop", "session": "local-1", "call": null, "tool": null, "prompt": null, "cwd": "the-repo", "interrupted": false, "socket": true, "token": true}`,
		`{"argv": ["hook", "--agent", "hermes"], "event": "Stop", "session": "local-1", "call": null, "tool": null, "prompt": null, "cwd": "the-repo", "interrupted": true, "socket": true, "token": true}`,
	}
	if rec != strings.Join(want, "\n") {
		t.Errorf("mur was asked:\n%s\nwant\n%s", rec, strings.Join(want, "\n"))
	}
}
