package t3

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/proc"
)

// The tests of Send run the test binary again as the `t3` command: it records what it was given,
// then does what MURMUR_TEST_FAKE_T3_MODE says.
const (
	fakeEnv                = "MURMUR_TEST_FAKE_T3"
	fakeModeEnv            = "MURMUR_TEST_FAKE_T3_MODE"
	fakeGrandchildLockEnv  = "MURMUR_TEST_FAKE_T3_GRANDCHILD_LOCK"
	fakeGrandchildReadyEnv = "MURMUR_TEST_FAKE_T3_GRANDCHILD_READY"
	fakeGrandchildStopEnv  = "MURMUR_TEST_FAKE_T3_GRANDCHILD_STOP"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeModeEnv) == "grandchild-sleep" {
		lockPath := os.Getenv(fakeGrandchildLockEnv)
		readyPath := os.Getenv(fakeGrandchildReadyEnv)
		stopPath := os.Getenv(fakeGrandchildStopEnv)
		if lockPath == "" || readyPath == "" || stopPath == "" {
			os.Exit(6)
		}
		// Hold lockPath until the OS reaps this process so tests can prove the grandchild is still
		// alive when Send returns and wait reliably for exit during cleanup.
		held, err := proc.Lock(lockPath)
		if err != nil {
			os.Exit(7)
		}
		_ = held
		tmp := readyPath + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(8)
		}
		if err := os.Rename(tmp, readyPath); err != nil {
			os.Exit(9)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(stopPath); err == nil {
				break
			}
			time.Sleep(15 * time.Millisecond)
		}
		os.Exit(0)
	}
	if path := os.Getenv(fakeEnv); path != "" {
		in, _ := io.ReadAll(os.Stdin)
		line, _ := json.Marshal(call{Args: os.Args[1:], Stdin: string(in), Home: os.Getenv("T3CODE_HOME")})
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(3)
		}
		f.Write(append(line, '\n'))
		f.Close()
		spawnGrandchild := func() {
			exe, err := os.Executable()
			if err != nil {
				os.Exit(4)
			}
			c := exec.Command(exe)
			c.Env = withEnv(os.Environ(), fakeModeEnv, "grandchild-sleep")
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			if err := c.Start(); err != nil {
				os.Exit(4)
			}
			readyPath := os.Getenv(fakeGrandchildReadyEnv)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(readyPath); err == nil {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			os.Exit(5)
		}
		switch os.Getenv(fakeModeEnv) {
		case "fail":
			os.Stderr.WriteString("\n  thread not found: nope  \nsecond line\n")
			os.Exit(1)
		case "failsilently":
			os.Exit(1)
		case "noisy-fail":
			os.Stderr.WriteString("\n  first real error  \n")
			chunk := strings.Repeat("x", 64<<10)
			for range 32 { // 2 MiB
				_, _ = os.Stdout.WriteString(chunk)
			}
			os.Exit(1)
		case "hang":
			time.Sleep(time.Minute)
		case "grandchild-hang":
			spawnGrandchild()
			time.Sleep(time.Minute)
		case "grandchild-exit0":
			spawnGrandchild()
			os.Exit(0)
		case "grandchild-fail":
			os.Stderr.WriteString("fatal: thread locked\n")
			spawnGrandchild()
			os.Exit(1)
		}
		os.Exit(0)
	}
	// A T3 thread's shell can have T3CODE_HOME set, and a Send given no home leaves the environment as
	// it is, so the stand-in would see the thread's own (#804). The tests start with none.
	os.Unsetenv("T3CODE_HOME")
	os.Exit(m.Run())
}

type call struct {
	Args  []string `json:"args"`
	Stdin string   `json:"stdin"`
	Home  string   `json:"home"`
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

// The thread goes as an argument and the text as standard input, whatever the text holds: quotes,
// newlines, percent signs, non-ASCII, and a length no command line takes on Windows.
func TestSendGivesTheTextOnStandardInput(t *testing.T) {
	exe, calls := fake(t, "")
	for _, text := range []string{
		"plain",
		"quotes \" and ' and `ticks` and $VARS and %PATH% and ^ & | < >",
		"two\r\nlines\nand a blank\n\nand — über 日本語 🙂",
		strings.Repeat("long line of text. ", 20000),
		"-",
		"",
	} {
		if err := Send(context.Background(), exe, "", "thread-1%2F", text); err != nil {
			t.Fatalf("Send(%.30q): %v", text, err)
		}
	}
	got := calls()
	if len(got) != 6 {
		t.Fatalf("%d calls, want 6", len(got))
	}
	for _, c := range got {
		if strings.Join(c.Args, " ") != "thread send thread-1%2F -" {
			t.Errorf("run as %q", c.Args)
		}
		if c.Home != "" {
			t.Errorf("T3CODE_HOME is %q with no home given", c.Home)
		}
	}
	if got[3].Stdin != strings.Repeat("long line of text. ", 20000) || got[2].Stdin != "two\r\nlines\nand a blank\n\nand — über 日本語 🙂" {
		t.Errorf("the text changed on its way through")
	}
}

func TestSendPassesTheDataDirectory(t *testing.T) {
	exe, calls := fake(t, "")
	home := filepath.Join(t.TempDir(), "t3 home")
	t.Setenv("T3CODE_HOME", filepath.Join(t.TempDir(), "inherited-old-home"))
	if err := Send(context.Background(), exe, home, "thread-1", "x"); err != nil {
		t.Fatal(err)
	}
	if c := calls(); len(c) != 1 || c[0].Home != home {
		t.Errorf("calls: %+v, want T3CODE_HOME=%s", c, home)
	}
}

func TestWithEnvDeduplicatesCaseInsensitively(t *testing.T) {
	got := withEnv([]string{"PATH=/bin", "t3code_home=/old1", "T3CODE_HOME=/old2", "T3CODE_HOME_EXTRA=keep"}, "T3CODE_HOME", "/new")
	want := "PATH=/bin|T3CODE_HOME_EXTRA=keep|T3CODE_HOME=/new"
	if strings.Join(got, "|") != want {
		t.Errorf("withEnv = %q, want %q", strings.Join(got, "|"), want)
	}
}

func TestSendRejectsFlagLikeThreadID(t *testing.T) {
	exe, calls := fake(t, "")
	for _, bad := range []string{"-h", "--help", "-"} {
		err := Send(context.Background(), exe, "", bad, "x")
		if err == nil || !errors.Is(err, ErrNotStarted) {
			t.Errorf("Send(%q) = %v, want error wrapping ErrNotStarted", bad, err)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Errorf("flag-like thread IDs ran %d commands, want 0", len(got))
	}
}

func TestSendCapsVerboseOutputAndKeepsFirstLine(t *testing.T) {
	exe, _ := fake(t, "noisy-fail")
	err := Send(context.Background(), exe, "", "thread-1", "SECRET payload")
	if err == nil || errors.Is(err, ErrNotStarted) {
		t.Fatalf("noisy-fail = %v, want non-ErrNotStarted failure", err)
	}
	if !strings.Contains(err.Error(), "first real error") || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error = %v, want first real error without secret", err)
	}
	cb := &cappedBuffer{max: 10}
	if n, err := cb.Write([]byte("1234567890abcdef")); err != nil || n != 16 {
		t.Errorf("cappedBuffer.Write = (%d, %v), want (16, nil)", n, err)
	}
	if cb.String() != "1234567890" {
		t.Errorf("cappedBuffer.String = %q, want 1234567890", cb.String())
	}
	// Splitting a multi-byte UTF-8 rune at max must not leave invalid UTF-8 in firstLine.
	utfCB := &cappedBuffer{max: 10}
	_, _ = utfCB.Write([]byte("     err")) // 8 bytes
	_, _ = utfCB.Write([]byte("🙂tail"))    // 4-byte rune split after 2 bytes
	got := utfCB.String()
	if !utf8.ValidString(got) || got != "     err" {
		t.Errorf("cappedBuffer UTF-8 split = %q (valid=%v), want %q", got, utf8.ValidString(got), "     err")
	}
	if line := firstLine(got); !utf8.ValidString(line) || line != "err" {
		t.Errorf("firstLine after UTF-8 cap split = %q (valid=%v), want %q", line, utf8.ValidString(line), "err")
	}
}

// fakeGrandchild configures fake for a grandchild-spawning mode and guarantees the grandchild
// has exited before the test finishes (otherwise Windows cannot unlink the test binary on exit).
func fakeGrandchild(t *testing.T, mode string) (exe, readyPath, lockPath string) {
	t.Helper()
	exe, _ = fake(t, mode)
	dir := t.TempDir()
	lockPath = filepath.Join(dir, "grandchild.lock")
	readyPath = filepath.Join(dir, "grandchild.ready")
	stopPath := filepath.Join(dir, "grandchild.stop")
	t.Setenv(fakeGrandchildLockEnv, lockPath)
	t.Setenv(fakeGrandchildReadyEnv, readyPath)
	t.Setenv(fakeGrandchildStopEnv, stopPath)
	t.Cleanup(func() {
		_ = os.WriteFile(stopPath, []byte("stop"), 0o600)
		deadline := time.Now().Add(5 * time.Second)
		if b, err := os.ReadFile(readyPath); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
				want := filepath.Clean(exe)
				isSelf := func(got string) bool {
					return strings.EqualFold(filepath.Clean(strings.TrimSuffix(got, " (deleted)")), want)
				}
				for time.Now().Before(deadline) {
					err := proc.End(pid, isSelf)
					if errors.Is(err, proc.ErrGone) || errors.Is(err, proc.ErrOther) || errors.Is(err, proc.ErrCantEnd) {
						break
					}
					time.Sleep(15 * time.Millisecond)
				}
			}
		}
		// Always wait for the OS to release the grandchild's lock (covers pidfd-less Linux sandboxes
		// and race-detector exit delays before TempDir removes stopPath).
		for time.Now().Before(deadline) {
			held, err := proc.Lock(lockPath)
			if err == nil {
				_ = held.Close()
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
	})
	return exe, readyPath, lockPath
}

// assertGrandchildHoldingPipe verifies the grandchild actually started and is still alive holding
// its inherited pipes open after Send returned (proving WaitDelay broke the wait).
func assertGrandchildHoldingPipe(t *testing.T, readyPath, lockPath string) {
	t.Helper()
	if _, err := os.Stat(readyPath); err != nil {
		t.Fatalf("grandchild never became ready (%s): %v", readyPath, err)
	}
	held, err := proc.Lock(lockPath)
	if err == nil {
		_ = held.Close()
		t.Fatalf("grandchild lock %s was already released when Send returned; WaitDelay was not exercised", lockPath)
	}
	if !errors.Is(err, proc.ErrLocked) {
		t.Fatalf("probing grandchild lock %s: %v", lockPath, err)
	}
}

// WaitDelay prevents a grandchild holding inherited stdout/stderr open from blocking Send past
// cancellation, SendTimeout, or exit — while preserving non-ErrNotStarted reporting on timeouts
// so the daemon never retries a possibly-delivered message (#1138).
func TestSendWaitDelayWithGrandchildHoldingPipes(t *testing.T) {
	defer func(wd, st time.Duration) { WaitDelay, sendTimeout = wd, st }(WaitDelay, sendTimeout)
	WaitDelay = 150 * time.Millisecond

	t.Run("parent cancel after grandchild ready keeps Canceled and non-ErrNotStarted", func(t *testing.T) {
		exe, readyPath, lockPath := fakeGrandchild(t, "grandchild-hang")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Send(ctx, exe, "", "thread-1", "x") }()

		waitDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(waitDeadline) {
			if _, err := os.Stat(readyPath); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := os.Stat(readyPath); err != nil {
			cancel()
			t.Fatalf("grandchild did not become ready in time: %v", err)
		}
		cancelAt := time.Now()
		cancel()
		err := <-done
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotStarted) {
			t.Fatalf("error = %v, want context.Canceled without ErrNotStarted", err)
		}
		assertGrandchildHoldingPipe(t, readyPath, lockPath)
		if took := time.Since(cancelAt); took > 5*time.Second {
			t.Errorf("blocked %s waiting on grandchild pipe", took)
		}
	})

	t.Run("SendTimeout with grandchild keeps timeout message and non-ErrNotStarted", func(t *testing.T) {
		sendTimeout = 1500 * time.Millisecond
		defer func() { sendTimeout = SendTimeout }()
		exe, readyPath, lockPath := fakeGrandchild(t, "grandchild-hang")
		start := time.Now()
		err := Send(context.Background(), exe, "", "thread-1", "x")
		if err == nil || errors.Is(err, ErrNotStarted) || !strings.Contains(err.Error(), "took over") {
			t.Fatalf("error = %v, want non-ErrNotStarted timeout error", err)
		}
		assertGrandchildHoldingPipe(t, readyPath, lockPath)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("blocked %s waiting on grandchild pipe", took)
		}
	})

	t.Run("exit 0 with grandchild holding pipe succeeds promptly", func(t *testing.T) {
		exe, readyPath, lockPath := fakeGrandchild(t, "grandchild-exit0")
		start := time.Now()
		if err := Send(context.Background(), exe, "", "thread-1", "x"); err != nil {
			t.Fatalf("exit 0 with grandchild pipe: %v", err)
		}
		assertGrandchildHoldingPipe(t, readyPath, lockPath)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("blocked %s waiting on grandchild pipe", took)
		}
	})

	t.Run("exit 1 with grandchild holding pipe keeps error line and non-ErrNotStarted", func(t *testing.T) {
		exe, readyPath, lockPath := fakeGrandchild(t, "grandchild-fail")
		start := time.Now()
		err := Send(context.Background(), exe, "", "thread-1", "x")
		if err == nil || errors.Is(err, ErrNotStarted) || !strings.Contains(err.Error(), "fatal: thread locked") {
			t.Fatalf("error = %v, want non-ErrNotStarted fatal: thread locked", err)
		}
		assertGrandchildHoldingPipe(t, readyPath, lockPath)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("blocked %s waiting on grandchild pipe", took)
		}
	})
}

// A failure says what T3 printed, its first line only, and never the message.
func TestSendFailureNamesTheFirstLineAndNotTheText(t *testing.T) {
	exe, _ := fake(t, "fail")
	err := Send(context.Background(), exe, "", "thread-1", "SECRET message text")
	if err == nil {
		t.Fatal("Send succeeded")
	}
	if !strings.Contains(err.Error(), "thread not found: nope") || strings.Contains(err.Error(), "second line") ||
		strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error: %v", err)
	}
	if exe, _ := fake(t, "failsilently"); exe != "" {
		if err := Send(context.Background(), exe, "", "thread-1", "x"); err == nil || !strings.Contains(err.Error(), "exit status 1") {
			t.Errorf("a silent failure: %v", err)
		}
	}
}

func TestSendNeedsACommandAndAThread(t *testing.T) {
	for _, c := range [][2]string{{"", "thread"}, {"/bin/t3", ""}} {
		if err := Send(context.Background(), c[0], "", c[1], "x"); err == nil {
			t.Errorf("Send(%q, %q) succeeded", c[0], c[1])
		}
	}
	if err := Send(context.Background(), filepath.Join(t.TempDir(), "no-such-t3"), "", "thread", "x"); err == nil {
		t.Error("a missing command succeeded")
	}
}

// Whether the command started is how a caller knows a second call is safe: `t3 thread send` takes no
// key that would make a repeat harmless, so after any failure past the start the message may be in
// the thread already.
func TestSendSaysWhetherTheCommandStarted(t *testing.T) {
	err := Send(context.Background(), filepath.Join(t.TempDir(), "no-such-t3"), "", "thread", "x")
	if !errors.Is(err, ErrNotStarted) {
		t.Errorf("a command that isn't there: %v", err)
	}
	for _, mode := range []string{"fail", "failsilently"} {
		exe, calls := fake(t, mode)
		err := Send(context.Background(), exe, "", "thread", "x")
		if err == nil || errors.Is(err, ErrNotStarted) || len(calls()) != 1 {
			t.Errorf("%s: a command that ran and failed (%d calls): %v", mode, len(calls()), err)
		}
	}
	// One that was stopped on the way, as by a timeout, may have got the message in before it was.
	exe, _ := fake(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := Send(ctx, exe, "", "thread", "x"); err == nil || errors.Is(err, ErrNotStarted) {
		t.Errorf("a command stopped on the way: %v", err)
	}
}

// A send cancelled before the command could start ran nothing, and says so as well as that it was
// cancelled: a caller that has marked the message as handed over can take the mark off (#82).
func TestSendCancelledBeforeItStartsIsNotStarted(t *testing.T) {
	exe, calls := fake(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Send(ctx, exe, "", "thread-1", "x")
	if !errors.Is(err, ErrNotStarted) || !errors.Is(err, context.Canceled) {
		t.Errorf("error: %v, want one that wraps ErrNotStarted and context.Canceled", err)
	}
	if len(calls()) != 0 {
		t.Errorf("%d calls, want none", len(calls()))
	}
}

// A cancelled context stops a send that hangs, and the error says it was cancelled.
func TestSendStopsWhenCancelled(t *testing.T) {
	exe, _ := fake(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := Send(ctx, exe, "", "thread-1", "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error: %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("took %s to stop", time.Since(start))
	}
}

func TestHomes(t *testing.T) {
	root := t.TempDir()
	cli := filepath.Join(root, "t3data", "bin", "t3.cmd")
	env := filepath.Join(root, "elsewhere")
	userHome, _ := os.UserHomeDir()
	std := filepath.Join(userHome, ".t3")

	got := Homes(cli, env)
	want := []string{env, filepath.Join(root, "t3data"), std}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Homes = %q, want %q", got, want)
	}
	// Repeats go once; a CLI outside a bin directory says nothing of the data's; relative paths are no use.
	if got := Homes(filepath.Join(userHome, ".t3", "bin", "t3"), std); len(got) != 1 || got[0] != std {
		t.Errorf("repeated: %q", got)
	}
	if got := Homes(filepath.Join(root, "tools", "t3"), ""); len(got) != 1 || got[0] != std {
		t.Errorf("no bin dir: %q", got)
	}
	if got := Homes("t3", "relative/home"); len(got) != 1 || got[0] != std {
		t.Errorf("relative: %q", got)
	}
}

func TestStateDB(t *testing.T) {
	if got, want := StateDB(filepath.Join("x", "t3")), filepath.Join("x", "t3", "userdata", "statev2.sqlite"); got != want {
		t.Errorf("StateDB = %q, want %q", got, want)
	}
}

func TestFirstLine(t *testing.T) {
	long := strings.Repeat("é", 300)
	for in, want := range map[string]string{
		"":                  "",
		"\n \n":             "",
		"one\ntwo":          "one",
		"\n\n  spaced  \nx": "spaced",
		long:                strings.Repeat("é", 100),
	} {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%.20q) = %.30q, want %.30q", in, got, want)
		}
	}
}

// Above takes the t3 on PATH where no process above runs t3, and none where there is none on
// PATH either. A run of the tests under T3's headless server has t3 above it, and is skipped.
func TestAboveFallsBackToPath(t *testing.T) {
	for i, p := range proc.Ancestors(os.Getpid(), 9) {
		if i == 0 {
			continue
		}
		if exe, err := proc.Exe(p); err == nil && strings.TrimSuffix(strings.ToLower(filepath.Base(exe)), ".exe") == "t3" {
			t.Skip("a t3 runs these tests")
		}
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if got := Above(os.Getpid(), 8, exec.LookPath); got != "" {
		t.Errorf("with no t3: %q", got)
	}
	name := "t3"
	if runtime.GOOS == "windows" {
		name = "t3.exe"
	}
	fake := filepath.Join(dir, name)
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Above(os.Getpid(), 8, exec.LookPath); got != fake {
		t.Errorf("with t3 on PATH: %q, want %q", got, fake)
	}
}
