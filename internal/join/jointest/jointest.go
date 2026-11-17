// Package jointest is what the tests of join share: a stand-in for the claude command, so that no
// test runs the real one against the real user's settings, and a way to make a copy of the running
// test binary under another name (mur, claude), since the tests exercise mur by running it.
package jointest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/jsonedit"
)

// LogEnv names the file the stand-in appends the arguments of each run to, one line per run.
const LogEnv = "MURMUR_FAKE_CLAUDE_LOG"

// HangEnv, set to anything, has the stand-in hang until it is killed, as a claude stuck on its
// first run might.
const HangEnv = "MURMUR_FAKE_CLAUDE_HANG"

// RunIfFakeClaude does what the claude command does and exits, when this process was started under
// that name. Tests call it first thing in TestMain.
//
// It knows two commands, the ones join runs, and does them as Claude Code 2.1.312 does for user
// scope (2026-10-29): `mcp add --scope user NAME -- COMMAND ARGS...` adds a stdio server to
// .claude.json, in $CLAUDE_CONFIG_DIR if that is set and the home directory if not, and fails if the
// name is taken; `mcp remove --scope user NAME` removes it.
func RunIfFakeClaude() {
	if strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe") != "claude" {
		return
	}
	if log := os.Getenv(LogEnv); log != "" {
		if f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
			f.Close()
		}
	}
	if os.Getenv(HangEnv) != "" {
		time.Sleep(time.Hour)
	}
	if err := fakeClaude(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func fakeClaude(args []string) error {
	if len(args) < 5 || args[0] != "mcp" || args[2] != "--scope" || args[3] != "user" {
		return fmt.Errorf("the stand-in claude doesn't know %q", args)
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		var err error
		if dir, err = os.UserHomeDir(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, ".claude.json")
	cfg, raw, err := jsonedit.ReadFile(path)
	if err != nil {
		return err
	}
	servers := cfg.Object("mcpServers")
	if servers == nil {
		servers = jsonedit.New()
		cfg.Set("mcpServers", servers)
	}
	name := args[4]
	_, have := servers.Get(name)
	switch args[1] {
	case "add":
		if have {
			return fmt.Errorf("MCP server %s already exists in user config", name)
		}
		if len(args) < 8 || args[5] != "--" {
			return fmt.Errorf("usage: mcp add --scope user NAME -- COMMAND [ARGS...]")
		}
		entry := jsonedit.New()
		entry.Set("type", "stdio")
		entry.Set("command", args[6])
		rest := []any{}
		for _, a := range args[7:] {
			rest = append(rest, a)
		}
		entry.Set("args", rest)
		entry.Set("env", jsonedit.New())
		servers.Set(name, entry)
	case "remove":
		if !have {
			return fmt.Errorf("No user-scoped MCP server found with name: %s", name)
		}
		servers.Delete(name)
	default:
		return fmt.Errorf("the stand-in claude doesn't know %q", args)
	}
	out, err := jsonedit.Format(cfg, raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// CopySelf copies the running test binary to dst. A copy named mur behaves as mur when the test's
// TestMain hands over to main, and one named claude as the stand-in.
func CopySelf(t testing.TB, dst string) {
	t.Helper()
	if err := copySelf(dst); err != nil {
		t.Fatal(err)
	}
}

func copySelf(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := os.Open(self)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
