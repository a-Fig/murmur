package join

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hermesHarness loads the plugin as Hermes does, calling its register(ctx) with a stand-in for
// Hermes' context, and then calls a tool as Hermes would: as a session, as a subagent of it, and as
// a call with no session. It prints what it saw, one fact a line.
const hermesHarness = `
import importlib.util, sys
spec = importlib.util.spec_from_file_location("murmur_plugin", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
class Ctx:
    def __init__(self, cwd):
        self.tools, self.hooks, self.cwd = {}, {}, cwd
    def register_tool(self, name, toolset, schema, handler, **kw):
        self.tools[name] = (toolset, schema, handler)
    def register_hook(self, name, cb):
        self.hooks[name] = cb
    def session_cwd(self):
        return self.cwd
ctx = Ctx(sys.argv[2])
m.register(ctx)
print("tools:", ",".join(sorted(ctx.tools)))
toolset, schema, h = ctx.tools["murmur_who"]
print("schema:", toolset, schema["name"], schema["description"], schema["parameters"]["type"])
print("call:", h({"tags": ["ui"]}, session_id="local-1"))
ctx.hooks["subagent_start"](parent_session_id="local-1", child_session_id="child-9", child_role="leaf")
print("child:", h({}, session_id="child-9"))
ctx.hooks["subagent_stop"](child_session_id="child-9")
print("child after:", h({}, session_id="child-9"))
print("no session:", h({}))
ctx.cwd = ""
print("no folder:", h({}, session_id="local-1"))
`

// hermesStandIn is the mur the plugin runs: --list lists one tool, and a call prints what it was
// given; a call for the session "fail" fails.
const hermesStandIn = `
import json, os, sys
if sys.argv[1:] == ["tool", "--list"]:
    print(json.dumps([{"name": "who", "description": "Show the agents.", "inputSchema": {"type": "object"}}]))
    sys.exit(0)
print("session=%s folder=%s argv=%s args=%s" % (os.environ.get("MURMUR_HERMES_SESSION"),
      os.path.basename(os.getcwd()), " ".join(sys.argv[1:]), sys.stdin.read()))
`

// The plugin runs in Python: it registers the tools mur lists, under Murmur's names; a call runs
// mur with the session's ID, its folder and the arguments; a subagent's goes as its parent's; and a
// call with no session or folder is refused without running mur. Skipped where no Python is on
// PATH; CI's runners have one.
func TestHermesPluginRunsInPython(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		if py, err = exec.LookPath("python"); err != nil {
			t.Skip("no python on PATH")
		}
	}
	if out, err := exec.Command(py, "-c", "import sys; assert sys.version_info >= (3, 8)").CombinedOutput(); err != nil {
		t.Skipf("no usable python: %v %s", err, out)
	}
	dir := t.TempDir()
	standIn := filepath.Join(dir, "standin.py")
	write(t, standIn, hermesStandIn)
	exe := filepath.Join(dir, "mur")
	if runtime.GOOS == "windows" {
		exe += ".cmd"
		write(t, exe, "@\""+py+"\" \""+standIn+"\" %*\r\n")
	} else {
		write(t, exe, "#!/bin/sh\nexec '"+py+"' '"+standIn+"' \"$@\"\n")
		if err := os.Chmod(exe, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	plugin := filepath.Join(dir, "plugins", "murmur")
	if _, err := writeHermesPlugin(plugin, exe); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "the-repo")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "harness.py")
	write(t, harness, hermesHarness)
	out, err := exec.Command(py, "-I", harness, filepath.Join(plugin, "__init__.py"), folder).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := strings.ReplaceAll(string(out), "\r\n", "\n")
	for _, want := range []string{
		"tools: murmur_who\n",
		"schema: murmur murmur_who Show the agents. object\n",
		`call: session=local-1 folder=the-repo argv=tool --agent hermes who args={"tags": ["ui"]}` + "\n",
		"child: session=local-1 folder=the-repo",
		"child after: session=child-9 folder=the-repo",
		`no session: {"error": "Murmur can't tell which Hermes session this is`,
		`no folder: {"error": "Murmur can't tell which folder this session works in`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}
