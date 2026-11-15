package mcpserver

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/a-Fig/murmur/internal/proto"
)

// Tests of how a Codex or OpenCode session's server names its client's program, for the daemon to
// hand messages over by its command line (#135).

// startedBy makes the server's parent process pid, running exe, for the length of the test.
func startedBy(t *testing.T, pid int, exe string) {
	t.Helper()
	old := parentProgram
	parentProgram = func() (int, string) { return pid, exe }
	t.Cleanup(func() { parentProgram = old })
}

// abs is an absolute path on this system for the program name.
func abs(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// openCodeState makes an OpenCode state directory whose shared server is registered as content.
func openCodeState(t *testing.T, content string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if content == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "opencode", "service.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A Codex session's server names the Codex that started it, outside T3 only: inside, T3 is the way in.
func TestCodexRouteNamesTheCodexThatStartedTheServer(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	t.Setenv("T3CODE_HOME", "")
	codex := abs(t, "codex.exe")
	startedBy(t, 4242, codex)
	if r := routeFromEnv("codex"); r != (proto.Route{CodexCLI: codex}) {
		t.Errorf("route %+v, want only the Codex program", r)
	}
	t.Setenv("T3CODE_CLI_PATH", "/home/u/.t3/bin/t3")
	if r := routeFromEnv("codex"); r.CodexCLI != "" {
		t.Errorf("inside T3 the route names Codex's program: %+v", r)
	}
}

// A program replaced while it runs reads on Linux with " (deleted)" after its path, which names
// no file: the route has the path itself, where the new program is.
func TestRouteNamesTheProgramThatReplacedARunningOne(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	codex := abs(t, "codex")
	startedBy(t, 4242, codex+" (deleted)")
	if r := routeFromEnv("codex"); r.CodexCLI != codex {
		t.Errorf("the route names %q, want %q", r.CodexCLI, codex)
	}
}

// A server started by anything but the client, or whose parent can't be told, names no program.
func TestRouteNamesNoProgramThatIsntTheClient(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	for _, exe := range []string{"", abs(t, "node.exe"), abs(t, "bash"), "codex", abs(t, "my-codex-wrapper"),
		abs(t, "codex-command-runner.exe"), abs(t, "codex-code-mode-host")} {
		startedBy(t, 4242, exe)
		if r := routeFromEnv("codex"); r.CodexCLI != "" {
			t.Errorf("started by %q, the route names %q", exe, r.CodexCLI)
		}
	}
	// Another agent's server is not Codex's, whatever started it.
	startedBy(t, 4242, abs(t, "codex"))
	for _, agent := range []string{"", "claude-code", "pi", "grok", "muse", "opencode"} {
		if r := routeFromEnv(agent); r.CodexCLI != "" {
			t.Errorf("agent %q: the route names Codex's program", agent)
		}
	}
}

// An OpenCode session's server names its OpenCode only when that is the shared background server
// OpenCode 2 registers, which is the one `opencode api` asks.
func TestOpenCodeRouteNeedsTheSharedServer(t *testing.T) {
	t.Setenv("T3CODE_CLI_PATH", "")
	opencode := abs(t, "opencode")
	startedBy(t, 4242, opencode)

	openCodeState(t, `{"id":"x","version":"2.2.0","url":"http://127.0.0.1:28894","pid":4242,"password":"p"}`)
	if r := routeFromEnv("opencode"); r != (proto.Route{OpenCodeCLI: opencode}) {
		t.Errorf("route %+v, want only the OpenCode program", r)
	}
	t.Setenv("T3CODE_CLI_PATH", "/home/u/.t3/bin/t3")
	if r := routeFromEnv("opencode"); r.OpenCodeCLI != "" {
		t.Errorf("inside T3 the route names OpenCode's program: %+v", r)
	}
	t.Setenv("T3CODE_CLI_PATH", "")

	for name, content := range map[string]string{
		"no registration (OpenCode 1, or a private server)": "",
		"another process is the shared server":              `{"pid":` + strconv.Itoa(4243) + `}`,
		"a registration that can't be read":                 `{"pid":`,
		"a registration without a process":                  `{"url":"http://127.0.0.1:1"}`,
	} {
		openCodeState(t, content)
		if r := routeFromEnv("opencode"); r.OpenCodeCLI != "" {
			t.Errorf("%s: the route names %q", name, r.OpenCodeCLI)
		}
	}

	openCodeState(t, `{"pid":4242}`)
	startedBy(t, 4242, abs(t, "node"))
	if r := routeFromEnv("opencode"); r.OpenCodeCLI != "" {
		t.Errorf("started by node, the route names %q", r.OpenCodeCLI)
	}
}
