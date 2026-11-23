package clientcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
)

// The tests run the test binary again as `codex` or `opencode`: it records what it was given, then
// does what MURMUR_TEST_FAKE_CLIENT_MODE says.
const (
	fakeEnv     = "MURMUR_TEST_FAKE_CLIENT"
	fakeModeEnv = "MURMUR_TEST_FAKE_CLIENT_MODE"
	// The grandchild modes start one more copy that holds standard error open after the stand-in
	// exits, as the OpenCode server `opencode api` starts would. It holds the lock file until it
	// exits, and exits once the alive file is gone.
	fakeLockEnv  = "MURMUR_TEST_FAKE_CLIENT_LOCK"
	fakeAliveEnv = "MURMUR_TEST_FAKE_CLIENT_ALIVE"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeModeEnv) == "grandchild" {
		// Its parent probes the same lock while it waits, so a probe can hold it for a moment.
		held, err := proc.Lock(os.Getenv(fakeLockEnv))
		for end := time.Now().Add(5 * time.Second); errors.Is(err, proc.ErrLocked) && time.Now().Before(end); {
			time.Sleep(5 * time.Millisecond)
			held, err = proc.Lock(os.Getenv(fakeLockEnv))
		}
		if err != nil {
			os.Exit(7)
		}
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
			if _, err := os.Stat(os.Getenv(fakeAliveEnv)); err != nil {
				break
			}
		}
		held.Close()
		os.Exit(0)
	}
	if path := os.Getenv(fakeEnv); path != "" {
		line, _ := json.Marshal(call{Args: os.Args[1:], Home: os.Getenv("CODEX_HOME"), State: os.Getenv("XDG_STATE_HOME")})
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		f.Write(append(line, '\n'))
		f.Close()
		switch os.Getenv(fakeModeEnv) {
		case "fail":
			os.Stdout.WriteString("{\"_tag\":\"SessionNotFoundError\"}\n")
			os.Stderr.WriteString("\n  HTTP 404 Not Found  \nsecond line\n")
			os.Exit(1)
		case "echo":
			os.Stderr.WriteString("Error: couldn't queue: " + os.Args[len(os.Args)-1] + "\n")
			os.Exit(1)
		case "hang":
			time.Sleep(time.Minute)
		case "noisy":
			os.Stderr.WriteString("\n  HTTP 500 first line  \n")
			chunk := strings.Repeat("x", 64<<10)
			for range 32 { // 2 MiB
				os.Stderr.WriteString(chunk)
			}
			os.Exit(1)
		case "grandchild-exit0", "grandchild-fail":
			startGrandchild()
			if os.Getenv(fakeModeEnv) == "grandchild-fail" {
				os.Stderr.WriteString("Error: thread is locked\n")
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startGrandchild starts the grandchild with this stand-in's standard error, and returns once it
// holds its lock.
func startGrandchild() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(4)
	}
	c := exec.Command(exe)
	c.Env = withEnv(os.Environ(), fakeModeEnv, "grandchild")
	c.Stderr = os.Stderr
	hideWindow(c) // its parent has no console, so Windows would open one for it
	if err := c.Start(); err != nil {
		os.Exit(4)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		held, err := proc.Lock(os.Getenv(fakeLockEnv))
		if errors.Is(err, proc.ErrLocked) {
			return // the grandchild has it
		} else if err != nil {
			os.Exit(6)
		}
		held.Close()
	}
	os.Exit(5)
}

type call struct {
	Args  []string `json:"args"`
	Home  string   `json:"home"`
	State string   `json:"state"`
}

// fake sets the stand-in up and returns its path and a reader of its calls.
func fake(t *testing.T, mode string) (exe string, calls func() []call) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "calls.jsonl")
	t.Setenv(fakeEnv, path)
	t.Setenv(fakeModeEnv, mode)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	return exe, func() []call {
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		var out []call
		sc := bufio.NewScanner(f)
		sc.Buffer(nil, 4<<20)
		for sc.Scan() {
			var c call
			if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		return out
	}
}

// texts are messages a command line has to carry whole: quotes, what a shell would expand,
// newlines, non-ASCII, a leading dash, and one as long as a message's body may be.
var texts = []string{
	"plain",
	"quotes \" and ' and `ticks` and $VARS and %PATH% and ^ & | < > \\ and a last backslash \\",
	"two\r\nlines\nand a blank\n\nand — über 日本語 🙂",
	"--thread other",
	strings.Repeat("a line of a long message.\n", 800),
}

// Codex is run as `codex queue --thread <thread> --message=<text>`, the text reaching it as it was.
func TestCodexQueueGivesTheThreadAndTheText(t *testing.T) {
	exe, calls := fake(t, "")
	for _, text := range texts {
		if err := CodexQueue(context.Background(), exe, "", "0199aaaa-thread", text); err != nil {
			t.Fatalf("CodexQueue(%.30q): %v", text, err)
		}
	}
	got := calls()
	if len(got) != len(texts) {
		t.Fatalf("%d calls, want %d", len(got), len(texts))
	}
	for i, c := range got {
		want := []string{"queue", "--thread", "0199aaaa-thread", "--message=" + texts[i]}
		if strings.Join(c.Args, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("text %d: run as %.200q", i, c.Args)
		}
		if c.Home != "" {
			t.Errorf("CODEX_HOME is %q with no home given", c.Home)
		}
	}
}

func TestCodexQueuePassesTheCodexHome(t *testing.T) {
	exe, calls := fake(t, "")
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "another session's home"))
	home := filepath.Join(t.TempDir(), "codex home")
	if err := CodexQueue(context.Background(), exe, home, "thread-1", "x"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || got[0].Home != home {
		t.Errorf("calls %+v, want one with CODEX_HOME %q", got, home)
	}
}

// OpenCode is run as `opencode api session.prompt --param sessionID=<session> -d <body>`, the body
// JSON that holds the text as it was and asks for it to wait behind a running turn.
func TestOpenCodePromptGivesTheSessionAndTheText(t *testing.T) {
	exe, calls := fake(t, "")
	for _, text := range texts {
		if err := OpenCodePrompt(context.Background(), exe, "", "ses_abc123", text); err != nil {
			t.Fatalf("OpenCodePrompt(%.30q): %v", text, err)
		}
	}
	got := calls()
	if len(got) != len(texts) {
		t.Fatalf("%d calls, want %d", len(got), len(texts))
	}
	for i, c := range got {
		if len(c.Args) != 6 || strings.Join(c.Args[:5], " ") != "api session.prompt --param sessionID=ses_abc123 -d" {
			t.Fatalf("text %d: run as %.200q", i, c.Args)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(c.Args[5]), &body); err != nil {
			t.Fatalf("text %d: the body isn't JSON: %v", i, err)
		}
		if len(body) != 2 || body["text"] != texts[i] || body["delivery"] != "queue" {
			t.Errorf("text %d: the body is %.200q", i, c.Args[5])
		}
		if c.State != "" {
			t.Errorf("XDG_STATE_HOME is %q with no state directory given", c.State)
		}
	}
}

// The command looks for the shared server in the state directory it is given, whatever this
// process's own XDG_STATE_HOME is (#486).
func TestOpenCodePromptPassesTheStateDirectory(t *testing.T) {
	exe, calls := fake(t, "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "another session's state"))
	state := filepath.Join(t.TempDir(), "opencode state")
	if err := OpenCodePrompt(context.Background(), exe, state, "ses_abc123", "x"); err != nil {
		t.Fatal(err)
	}
	if got := calls(); len(got) != 1 || got[0].State != state {
		t.Errorf("calls %+v, want one with XDG_STATE_HOME %q", got, state)
	}
}

// Without a program, a thread or a session nothing is run, nor for an ID that would read as a flag.
func TestNothingIsRunWithoutSomewhereToSend(t *testing.T) {
	exe, calls := fake(t, "")
	ctx := context.Background()
	for name, err := range map[string]error{
		"no codex":           CodexQueue(ctx, "", "", "thread", "x"),
		"no thread":          CodexQueue(ctx, exe, "", "", "x"),
		"a flag for thread":  CodexQueue(ctx, exe, "", "--resume", "x"),
		"no opencode":        OpenCodePrompt(ctx, "", "", "ses_1", "x"),
		"no session":         OpenCodePrompt(ctx, exe, "", "", "x"),
		"not a session's ID": OpenCodePrompt(ctx, exe, "", "x --server http://elsewhere", "x"),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(calls()); n != 0 {
		t.Errorf("the command was run %d times", n)
	}
}

// A command that fails gives what it printed on standard error, in a line, and not the server's
// answer on standard output; it isn't ErrNotStarted, as the client may have the message.
func TestFailureSaysWhatTheCommandPrinted(t *testing.T) {
	exe, _ := fake(t, "fail")
	err := OpenCodePrompt(context.Background(), exe, "", "ses_abc", "the secret text")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404 Not Found") {
		t.Fatalf("err = %v, want the line the command printed", err)
	}
	if errors.Is(err, ErrNotStarted) {
		t.Errorf("a command that ran and failed reads as not started: %v", err)
	}
	for _, not := range []string{"second line", "SessionNotFoundError", "the secret text"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("the error has %q: %v", not, err)
		}
	}
}

// An error line that quotes the message is left out: errors are logged, and a message's text isn't.
func TestFailureNeverHoldsTheText(t *testing.T) {
	exe, _ := fake(t, "echo")
	err := CodexQueue(context.Background(), exe, "", "thread-1", "the secret text\nof two lines")
	if err == nil {
		t.Fatal("no error from a command that failed")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("the error holds the message: %v", err)
	}
}

// A program that isn't there never started: the one failure that is safe to try again.
func TestMissingProgramIsNotStarted(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-codex")
	if err := CodexQueue(context.Background(), missing, "", "thread-1", "x"); !errors.Is(err, ErrNotStarted) {
		t.Errorf("err = %v, want ErrNotStarted", err)
	}
	if err := OpenCodePrompt(context.Background(), missing, "", "ses_1", "x"); !errors.Is(err, ErrNotStarted) {
		t.Errorf("err = %v, want ErrNotStarted", err)
	}
}

// A cancelled context ends the command, and the error says so.
func TestCancelEndsTheCommand(t *testing.T) {
	exe, _ := fake(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := CodexQueue(ctx, exe, "", "thread-1", "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("it took %s to give up", took)
	}
}

// Windows starts no command line over 32,767 characters, so a text that long isn't tried there:
// the error is ErrTooLong, and not ErrNotStarted, which would have it tried again.
func TestTextTooLongForWindowsIsRefused(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows has the limit")
	}
	exe, calls := fake(t, "")
	long := strings.Repeat("0123456789", 3300)
	for name, err := range map[string]error{
		"codex":    CodexQueue(context.Background(), exe, "", "thread-1", long),
		"opencode": OpenCodePrompt(context.Background(), exe, "", "ses_1", long),
	} {
		if !errors.Is(err, ErrTooLong) || errors.Is(err, ErrNotStarted) {
			t.Errorf("%s: err = %v, want ErrTooLong", name, err)
		}
	}
	if n := len(calls()); n != 0 {
		t.Errorf("the command was run %d times", n)
	}
}
