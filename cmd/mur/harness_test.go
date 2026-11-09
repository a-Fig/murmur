package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/join/jointest"
	"github.com/a-Fig/murmur/internal/testgit"
)

// These tests run mur as a user would: a copy of the test binary named mur behaves as the mur
// command (TestMain hands it to main), and one named claude as a stand-in for Claude Code's
// command, so that nothing here runs the real claude or touches the real user's settings. A daemon
// that a command starts is the same copy run as `mur daemon`.
func TestMain(m *testing.M) {
	jointest.RunIfFakeClaude()
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe")
	if name == "mur" || (len(os.Args) > 1 && os.Args[1] == "daemon") {
		main()
		return
	}
	testgit.Isolate()
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(code)
}

var (
	binDir  string
	binOnce sync.Once
	binErr  error
)

func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// binaries makes the mur and claude copies, once for all the tests. MURMUR_UNDER_TEST names a built
// mur to use in place of this test binary's copy, which is how these tests are run against another
// version: the tests that describe new behaviour fail on the version before it.
func binaries(t *testing.T) (mur, claude string) {
	t.Helper()
	binOnce.Do(func() {
		if binDir, binErr = os.MkdirTemp("", "mur-bin"); binErr == nil {
			if other := os.Getenv("MURMUR_UNDER_TEST"); other != "" {
				binErr = copyFile(other, filepath.Join(binDir, exeName("mur")))
			} else {
				jointest.CopySelf(t, filepath.Join(binDir, exeName("mur")))
			}
			jointest.CopySelf(t, filepath.Join(binDir, exeName("claude")))
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return filepath.Join(binDir, exeName("mur")), filepath.Join(binDir, exeName("claude"))
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// cloneOf makes a bare remote with one commit, and a clone of it, under root.
func cloneOf(t *testing.T, root string) (remote, clone string) {
	t.Helper()
	remote, seed, clone := filepath.Join(root, "remote.git"), filepath.Join(root, "seed"), filepath.Join(root, "clone")
	git(t, root, "init", "-q", "--bare", "--initial-branch=main", remote)
	git(t, root, "init", "-q", "--initial-branch=main", seed)
	git(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, seed, "push", "-q", remote, "HEAD:refs/heads/main")
	git(t, root, "clone", "-q", remote, clone)
	return remote, clone
}

// stopDaemon stops the clone's daemon and waits until it has let go of the clone's files.
func stopDaemon(clone string) {
	runStop(context.Background(), []string{"-C", clone}, &bytes.Buffer{})
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(clone, ".git", "murmur", "daemon.json")); err != nil {
			return
		}
	}
}

// box is a user's machine: its own home, Claude Code and Codex directories, a stand-in claude on
// PATH, and a clone with a remote. Every path a command reads or writes is under root.
type box struct {
	t         *testing.T
	root      string
	mur       string // the mur command
	claude    string // the stand-in claude
	home      string
	claudeDir string // CLAUDE_CONFIG_DIR
	codexHome string
	log       string // what the stand-in claude was asked, one line a run
	remote    string
	clone     string
	extraEnv  []string
}

func newBox(t *testing.T) *box {
	t.Helper()
	mur, claude := binaries(t)
	root := t.TempDir()
	b := &box{t: t, root: root, mur: mur, claude: claude,
		home:      filepath.Join(root, "home"),
		claudeDir: filepath.Join(root, "home", "claude-config"),
		codexHome: filepath.Join(root, "home", "codex"),
		log:       filepath.Join(root, "claude.log"),
	}
	for _, d := range []string{b.home, b.codexHome} { // Codex is installed; Claude Code is the command on PATH
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.remote, b.clone = cloneOf(t, root)
	t.Cleanup(func() { stopDaemon(b.clone) })
	return b
}

// env is the environment a command runs in: the real one, except for everything that says whose
// machine this is and which agent is running the command.
func (b *box) env() []string {
	drop := map[string]bool{"HOME": true, "USERPROFILE": true, "CLAUDE_CONFIG_DIR": true, "CODEX_HOME": true, "PATH": true,
		"CLAUDECODE": true, "CLAUDE_CODE_SESSION_ID": true, "T3CODE_CLI_PATH": true, "CODEX_THREAD_ID": true,
		"CODEX_SANDBOX": true, "CODEX_CI": true, "XDG_CONFIG_HOME": true, jointest.LogEnv: true,
		// Where T3 Code and the other agents keep their settings: join reads and writes those too.
		"T3CODE_HOME": true, "PI_CODING_AGENT_DIR": true, "GROK_HOME": true,
		// Which session ran the command, and where sessions are found (internal/here).
		"CLAUDE_PID": true, brand.SessionsDirEnv: true}
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !drop[strings.ToUpper(k)] && !drop[k] {
			env = append(env, kv)
		}
	}
	return append(env,
		"HOME="+b.home, "USERPROFILE="+b.home,
		"CLAUDE_CONFIG_DIR="+b.claudeDir, "CODEX_HOME="+b.codexHome,
		jointest.LogEnv+"="+b.log, brand.SessionsDirEnv+"="+filepath.Join(b.root, "sessions"),
		"PATH="+filepath.Dir(b.mur)+string(os.PathListSeparator)+os.Getenv("PATH"),
		// init commits as the user, and this home has no git config to say who that is.
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	)
}

// run runs mur with the given arguments in dir and returns its output and exit status.
func (b *box) run(dir string, args ...string) (string, int) {
	b.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.mur, args...)
	cmd.Dir = dir
	cmd.Env = append(b.env(), b.extraEnv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return out.String(), ee.ExitCode()
	}
	if err != nil {
		b.t.Fatalf("mur %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return out.String(), 0
}

func (b *box) mustRun(dir string, args ...string) string {
	b.t.Helper()
	out, code := b.run(dir, args...)
	if code != 0 {
		b.t.Fatalf("mur %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// claudeJSON, claudeSettings and codexConfig are where the stand-in claude and join write.
func (b *box) claudeJSON() string     { return filepath.Join(b.claudeDir, ".claude.json") }
func (b *box) claudeSettings() string { return filepath.Join(b.claudeDir, "settings.json") }
func (b *box) codexConfig() string    { return filepath.Join(b.codexHome, "config.toml") }

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// snapshot is the content of every file under the given roots, by path, to compare before and after.
func snapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, root := range roots {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.Contains(filepath.ToSlash(path), "/.git/") {
				return nil // git's own bookkeeping changes on any command, and the daemon owns .git/murmur
			}
			b, _ := os.ReadFile(path)
			snap[path] = string(b)
			return nil
		})
	}
	return snap
}
