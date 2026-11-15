package join

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/a-Fig/murmur/internal/join/jointest"
	"github.com/a-Fig/murmur/internal/setup"
)

var (
	binDir   string
	binOnce  sync.Once
	binError error
)

func TestMain(m *testing.M) {
	jointest.RunIfFakeClaude()
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// fakeClaude is the path of a stand-in claude command: a copy of the test binary, which answers as
// claude when run under that name.
func fakeClaude(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		binDir, binError = os.MkdirTemp("", "mur-join-bin")
		if binError == nil {
			jointest.CopySelf(t, filepath.Join(binDir, exeName("claude")))
		}
	})
	if binError != nil {
		t.Fatal(binError)
	}
	return filepath.Join(binDir, exeName("claude"))
}

type sandbox struct {
	Env
	root string
	log  string // what the stand-in claude was asked to do
}

// newSandbox is a user's machine in a temporary directory: its own Claude Code and Codex homes, and
// the stand-in claude on its PATH. Nothing here can reach the real user's settings.
func newSandbox(t *testing.T, withClaude bool) *sandbox {
	t.Helper()
	root := t.TempDir()
	sb := &sandbox{root: root, log: filepath.Join(root, "claude.log")}
	sb.Env = Env{
		Exe:        filepath.Join(root, "bin", exeName("mur")),
		ClaudeDir:  filepath.Join(root, "claude"),
		ClaudeJSON: filepath.Join(root, "claude", ".claude.json"),
		CodexHome:  filepath.Join(root, "codex"),
		Getenv:     func(string) string { return "" },
		LookPath: func(name string) (string, error) {
			if name == "claude" && withClaude {
				return fakeClaude(t), nil
			}
			return "", exec.ErrNotFound
		},
	}
	t.Setenv("CLAUDE_CONFIG_DIR", sb.ClaudeDir)
	t.Setenv(jointest.LogEnv, sb.log)
	return sb
}

func (sb *sandbox) claudeRuns(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(sb.log)
	return strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(b)), "\n", "\x00"))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// Claude Code gets the server once, for the user, and its tools are allowed. Joining again runs
// nothing and writes nothing; what else is in the user's files stays where it was.
func TestRegisterClaudeAddsTheServerOnce(t *testing.T) {
	sb := newSandbox(t, true)
	write(t, sb.ClaudeJSON, "{\n  \"numStartups\": 7,\n  \"mcpServers\": {\"other\": {\"type\": \"stdio\", \"command\": \"o\", \"args\": []}},\n  \"theme\": \"dark\"\n}\n")
	write(t, filepath.Join(sb.ClaudeDir, "settings.json"), "{\n    \"model\": \"x\",\n    \"permissions\": {\"allow\": [\"Bash(ls)\"], \"deny\": []},\n    \"theme\": \"dark\"\n}\n")

	did, fresh, err := sb.registerClaude(context.Background())
	if err != nil || !fresh || len(did) != 3 { // the server, the allow rule, the hook
		t.Fatalf("first: did=%q fresh=%v err=%v", did, fresh, err)
	}
	cfg := read(t, sb.ClaudeJSON)
	for _, want := range []string{`"numStartups": 7`, `"other"`, `"theme": "dark"`, `"murmur"`, filepath.ToSlash(sb.Exe), `"mcp"`} {
		if !strings.Contains(strings.ReplaceAll(cfg, `\\`, "/"), want) {
			t.Errorf(".claude.json lacks %s:\n%s", want, cfg)
		}
	}
	if strings.Index(cfg, `"numStartups"`) > strings.Index(cfg, `"mcpServers"`) || strings.Index(cfg, `"other"`) > strings.Index(cfg, `"murmur"`) {
		t.Errorf("the user's keys moved:\n%s", cfg)
	}
	settings := read(t, filepath.Join(sb.ClaudeDir, "settings.json"))
	// What the user had stays as it was, in its order and its indentation, with the hook after it.
	if want := "{\n    \"model\": \"x\",\n    \"permissions\": {\n        \"allow\": [\n            \"Bash(ls)\",\n            \"mcp__murmur\"\n        ],\n        \"deny\": []\n    },\n    \"theme\": \"dark\",\n    \"hooks\": {\n"; !strings.HasPrefix(settings, want) {
		t.Errorf("settings.json:\n%s\nwant it to start:\n%s", settings, want)
	}
	if events := hookEvents(t, filepath.Join(sb.ClaudeDir, "settings.json")); !reflect.DeepEqual(events, wantHookEvents()) {
		t.Errorf("the hook is on %v, want %v", events, wantHookEvents())
	}

	runs := len(sb.claudeRuns(t))
	before, beforeSettings := cfg, settings
	did, fresh, err = sb.registerClaude(context.Background())
	if err != nil || fresh || len(did) != 0 {
		t.Fatalf("second: did=%q fresh=%v err=%v", did, fresh, err)
	}
	if read(t, sb.ClaudeJSON) != before || read(t, filepath.Join(sb.ClaudeDir, "settings.json")) != beforeSettings || len(sb.claudeRuns(t)) != runs {
		t.Error("joining again changed something or ran claude")
	}
}

// A server registered for a binary that has since moved is replaced, not left pointing at nothing
// and not added twice.
func TestRegisterClaudeReplacesAStaleServer(t *testing.T) {
	sb := newSandbox(t, true)
	write(t, sb.ClaudeJSON, `{"mcpServers": {"murmur": {"type": "stdio", "command": "/old/mur", "args": ["mcp"], "env": {}}}}`)
	if _, fresh, err := sb.registerClaude(context.Background()); err != nil || !fresh {
		t.Fatalf("fresh=%v err=%v", fresh, err)
	}
	cfg := strings.ReplaceAll(read(t, sb.ClaudeJSON), `\\`, "/")
	if strings.Contains(cfg, "/old/mur") || !strings.Contains(cfg, filepath.ToSlash(sb.Exe)) || strings.Count(cfg, `"murmur"`) != 1 {
		t.Errorf(".claude.json:\n%s", cfg)
	}
}

// A machine with Claude Code's settings but no claude command on PATH still gets the entry.
func TestRegisterClaudeWithoutTheCommand(t *testing.T) {
	sb := newSandbox(t, false)
	write(t, sb.ClaudeJSON, "{\n  \"keep\": true\n}\n")
	did, fresh, err := sb.registerClaude(context.Background())
	if err != nil || !fresh || len(did) != 3 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	cfg := read(t, sb.ClaudeJSON)
	if !strings.Contains(cfg, `"keep": true`) || !strings.Contains(cfg, `"murmur"`) || !strings.Contains(cfg, `"type": "stdio"`) {
		t.Errorf(".claude.json:\n%s", cfg)
	}
	if _, fresh, _ := sb.registerClaude(context.Background()); fresh {
		t.Error("registered it twice")
	}
}

// A machine that has never had Claude Code isn't given a ~/.claude.json.
func TestRegisterClaudeSkipsAMachineWithoutIt(t *testing.T) {
	sb := newSandbox(t, false)
	did, fresh, err := sb.registerClaude(context.Background())
	if err != nil || fresh || len(did) != 0 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	if exists(sb.ClaudeJSON) || exists(filepath.Join(sb.ClaudeDir, "settings.json")) {
		t.Error("created Claude Code files on a machine without it")
	}
}

// A settings.json join can't safely edit is reported, and left alone.
func TestRegisterClaudeRefusesWhatItCannotEdit(t *testing.T) {
	sb := newSandbox(t, true)
	bad := `{"permissions": {"allow": "Bash"}}`
	write(t, filepath.Join(sb.ClaudeDir, "settings.json"), bad)
	if _, _, err := sb.registerClaude(context.Background()); err == nil {
		t.Error("no error")
	}
	if read(t, filepath.Join(sb.ClaudeDir, "settings.json")) != bad {
		t.Error("changed a file it couldn't edit")
	}
}

func TestRegisterCodexKeepsTheConfig(t *testing.T) {
	sb := newSandbox(t, false)
	path := filepath.Join(sb.CodexHome, "config.toml")
	before := "# mine\nmodel = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"o\"\n"
	write(t, path, before)
	did, fresh, err := sb.registerCodex()
	if err != nil || !fresh || len(did) != 2 { // the server, and the hook (#225)
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	got := read(t, path)
	if !strings.HasPrefix(got, before) || !strings.Contains(got, "[mcp_servers.murmur]") || !strings.Contains(got, "[hooks.state.") {
		t.Errorf("config.toml:\n%s", got)
	}
	if did, fresh, err := sb.registerCodex(); err != nil || fresh || len(did) != 0 {
		t.Errorf("second: did=%q fresh=%v err=%v", did, fresh, err)
	}
}

// A config file that is a symbolic link, as a dotfiles repository makes, is written through: the link
// stays, and the file it points to gets the server (Codex review, 2026-10-29).
func TestRegisterCodexWritesThroughASymlink(t *testing.T) {
	sb := newSandbox(t, false)
	target := filepath.Join(sb.root, "dotfiles", "codex.toml")
	write(t, target, "model = \"gpt\"\n")
	link := filepath.Join(sb.CodexHome, "config.toml")
	if err := os.MkdirAll(sb.CodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if _, _, err := sb.registerCodex(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link was replaced by a file (%v)", err)
	}
	if got := read(t, target); !strings.HasPrefix(got, "model = ") || !strings.Contains(got, "[mcp_servers.murmur]") {
		t.Errorf("the file the link points to:\n%s", got)
	}
}

func TestRegisterCodexSkipsAMachineWithoutIt(t *testing.T) {
	sb := newSandbox(t, false)
	if did, fresh, err := sb.registerCodex(); err != nil || fresh || len(did) != 0 {
		t.Fatalf("did=%q fresh=%v err=%v", did, fresh, err)
	}
	if exists(sb.CodexHome) {
		t.Error("created Codex's home on a machine without it")
	}
}

// clone makes a repository with a worktree and the marker in both.
func clone(t *testing.T) (Place, string) {
	t.Helper()
	dir := t.TempDir()
	main := filepath.Join(dir, "main")
	git(t, dir, "init", "-q", "--initial-branch=main", main)
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	other := filepath.Join(dir, "other")
	git(t, main, "worktree", "add", "-q", "-b", "other", other)
	if _, err := setup.Init(context.Background(), main); err != nil {
		t.Fatal(err)
	}
	p, err := Locate(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	return p, other
}

// The notice hook goes into the Claude Code home's settings, once, beside what the user has there.
func TestTheHookGoesInTheClaudeHomeOnce(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "settings.json")
	exe := filepath.Join(t.TempDir(), "a b", "bin", "mur") // a path with a space, in this OS's own form
	command, err := json.Marshal(setup.HookCommand(exe))
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n")

	if changed, err := installClaudeHook(home, exe); err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	got := read(t, path)
	if !strings.Contains(got, string(command)) || !strings.Contains(got, "a b") || !strings.Contains(got, "Bash(ls)") {
		t.Errorf("settings.json:\n%s", got)
	}
	if events := hookEvents(t, path); !reflect.DeepEqual(events, wantHookEvents()) {
		t.Errorf("the hook is on %v, want %v", events, wantHookEvents())
	}
	if !hasClaudeHook(path) {
		t.Error("the hook isn't seen where it was written")
	}
	if again, err := installClaudeHook(home, exe); err != nil || again {
		t.Errorf("a second install changed the file: %v (err %v)", again, err)
	}

	// The binary moved: the hook names the new path and there is still one of it.
	if _, err := installClaudeHook(home, `/new/mur`); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if strings.Contains(got, "a b") || strings.Count(got, `/new/mur`) != len(setup.HookEvents) {
		t.Errorf("after the binary moved:\n%s", got)
	}
}

// The hook a join before #422 put in every working tree of the clone comes out of each. That leaves
// the user's other settings and deletes a file that held only the hook.
func TestAnOlderJoinsHooksComeOutOfEveryWorktree(t *testing.T) {
	p, other := clone(t)
	ctx := context.Background()
	write(t, filepath.Join(other, ".claude", "settings.local.json"), "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n")
	oldHooks(t, p, filepath.Join(t.TempDir(), "bin", "mur"))

	removed, _, err := removeHooks(ctx, p, nil)
	if err != nil || len(removed) != 2 {
		t.Fatalf("removed %q, err %v", removed, err)
	}
	if exists(filepath.Join(p.Top, ".claude", "settings.local.json")) || exists(filepath.Join(p.Top, ".claude")) {
		t.Error("a file that held only the hook is still there")
	}
	left := read(t, filepath.Join(other, ".claude", "settings.local.json"))
	if strings.Contains(left, "hook") || !strings.Contains(left, "Bash(ls)") {
		t.Errorf("what is left in the worktree with its own settings:\n%s", left)
	}
	if again, _, err := removeHooks(ctx, p, nil); err != nil || len(again) != 0 {
		t.Errorf("a second removal changed %q (err %v)", again, err)
	}
}

// A working tree's hook file that is the user's own link to Claude Code's settings holds the
// per-user hook, not an older join's: taking the older hooks out passes it over (Sol's review of
// #426).
func TestTheNewHookStaysWhereAWorktreesFileLinksToTheHome(t *testing.T) {
	p, _ := clone(t)
	home := t.TempDir()
	settings := filepath.Join(home, "settings.json")
	if _, err := installClaudeHook(home, filepath.Join(t.TempDir(), "bin", "mur")); err != nil {
		t.Fatal(err)
	}
	linkOrSkip(t, settings, filepath.Join(p.Top, ".claude", "settings.local.json"))

	removed, notes, err := removeHooks(context.Background(), p, []string{settings})
	if err != nil || len(removed) != 0 || len(notes) != 0 {
		t.Errorf("removed %q, notes %q, err %v", removed, notes, err)
	}
	if !hasClaudeHook(settings) {
		t.Errorf("the per-user hook went with the older ones:\n%s", read(t, settings))
	}
}

// Whether Murmur may run is the repo's invitation and the user's consent together, read from files.
func TestActiveNeedsTheMarkerAndTheConsent(t *testing.T) {
	p, _ := clone(t)
	if !p.Marked() || p.Joined() || p.Active() {
		t.Fatalf("marked=%v joined=%v active=%v before joining", p.Marked(), p.Joined(), p.Active())
	}
	if news, err := p.RecordConsent(); err != nil || !news {
		t.Fatalf("consent: %v, %v", news, err)
	}
	if news, _ := p.RecordConsent(); news {
		t.Error("recording consent twice was news")
	}
	if !p.Joined() || !p.Active() || !Joined(p.GitDir) {
		t.Error("not active after joining")
	}

	// A working tree without the marker (an older branch) is not one Murmur runs in, joined or not.
	os.Remove(filepath.Join(p.Top, "AGENTS.md"))
	os.Remove(filepath.Join(p.Top, "CLAUDE.md"))
	if p.Marked() || p.Active() || !p.Joined() {
		t.Errorf("without the marker: marked=%v joined=%v active=%v", p.Marked(), p.Joined(), p.Active())
	}
	if had, err := p.ForgetConsent(); err != nil || !had {
		t.Fatalf("forget: %v, %v", had, err)
	}
	if had, _ := p.ForgetConsent(); had {
		t.Error("forgetting twice was news")
	}

	// No clone, no consent: nothing may be read from a path relative to wherever the process is.
	if (Place{}).Joined() || (Place{}).Active() || Joined("") {
		t.Error("an empty place is joined")
	}
}

// Leaving a clone that never joined has nothing to undo and no daemon to look for, and says so.
func TestLeaveInACloneThatNeverJoined(t *testing.T) {
	sb := newSandbox(t, false)
	p, _ := clone(t)
	r, err := Leave(context.Background(), sb.Env, p.Top)
	if err != nil || len(r.Problems) != 0 {
		t.Fatalf("err %v, problems %q", err, r.Problems)
	}
	if len(r.Did) != 1 || !strings.Contains(r.Did[0], "Nothing to undo") {
		t.Errorf("did %q", r.Did)
	}
}
