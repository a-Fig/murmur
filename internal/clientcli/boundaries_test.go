package clientcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/proc"
)

// An ID that isn't one, and a NUL byte anywhere, are refused before anything runs, and not as
// ErrNotStarted: the same call would be refused again, so it isn't one to try twice.
func TestBadArgumentsAreRefusedBeforeAnythingRuns(t *testing.T) {
	exe, calls := fake(t, "")
	ctx := context.Background()
	errs := map[string]error{
		"a NUL in the program":       CodexQueue(ctx, exe+"\x00", "", "thread-1", "x"),
		"a NUL in the Codex home":    CodexQueue(ctx, exe, "home\x00", "thread-1", "x"),
		"a NUL in the Codex text":    CodexQueue(ctx, exe, "", "thread-1", "a\x00b"),
		"a NUL in the thread":        CodexQueue(ctx, exe, "", "thread\x00-1", "x"),
		"a NUL in the OpenCode exe":  OpenCodePrompt(ctx, exe+"\x00", "", "ses_1", "x"),
		"a NUL in the state":         OpenCodePrompt(ctx, exe, "state\x00", "ses_1", "x"),
		"a NUL in the OpenCode text": OpenCodePrompt(ctx, exe, "", "ses_1", "a\x00b"),
		"a NUL in the session":       OpenCodePrompt(ctx, exe, "", "ses_\x001", "x"),
		"only the prefix":            OpenCodePrompt(ctx, exe, "", "ses", "x"),
		"a flag for a session":       OpenCodePrompt(ctx, exe, "", "-ses_1", "x"),
		"a thread that isn't UTF-8":  CodexQueue(ctx, exe, "", "thread-\xff", "x"),
	}
	for _, bad := range []string{" ", "\t", "\n", "\r", "\x01", "\x7f", "\u00a0", "\u2028", "\u0085"} {
		errs["thread with "+fmt.Sprintf("%U", []rune(bad)[0])] = CodexQueue(ctx, exe, "", "thread"+bad+"1", "x")
		errs["session with "+fmt.Sprintf("%U", []rune(bad)[0])] = OpenCodePrompt(ctx, exe, "", "ses_a"+bad+"b", "x")
		errs["session ending with "+fmt.Sprintf("%U", []rune(bad)[0])] = OpenCodePrompt(ctx, exe, "", "ses"+bad, "x")
	}
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if errors.Is(err, ErrNotStarted) {
			t.Errorf("%s: %v reads as not started, and so as worth another try", name, err)
		}
	}
	if n := len(calls()); n != 0 {
		t.Errorf("the command was run %d times", n)
	}
}

// An entry for the variable in another case is replaced too, as Windows reads names without
// regard to case, and one for a longer name is kept.
func TestWithEnvReplacesWithoutRegardToCase(t *testing.T) {
	got := withEnv([]string{"PATH=/bin", "codex_home=/old1", "CODEX_HOME=/old2", "CODEX_HOME_EXTRA=keep", "=C:=C:\\"}, "CODEX_HOME", "/new")
	want := "PATH=/bin|CODEX_HOME_EXTRA=keep|=C:=C:\\|CODEX_HOME=/new"
	if strings.Join(got, "|") != want {
		t.Errorf("withEnv = %q, want %q", strings.Join(got, "|"), want)
	}
}

// Standard error is kept to its first maxStderr bytes, and the first line still makes the error.
func TestStandardErrorIsCapped(t *testing.T) {
	exe, _ := fake(t, "noisy")
	err := OpenCodePrompt(context.Background(), exe, "", "ses_abc", "the secret text")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500 first line") || errors.Is(err, ErrNotStarted) {
		t.Fatalf("err = %v, want the first line, and not ErrNotStarted", err)
	}
	if len(err.Error()) > 300 {
		t.Errorf("the error is %d bytes long", len(err.Error()))
	}

	c := &cappedBuffer{max: 10}
	if n, err := c.Write([]byte("1234567890abcdef")); n != 16 || err != nil {
		t.Errorf("Write = (%d, %v), want (16, nil)", n, err)
	}
	if n, err := c.Write([]byte("more")); n != 4 || err != nil {
		t.Errorf("Write when full = (%d, %v), want (4, nil)", n, err)
	}
	if c.String() != "1234567890" {
		t.Errorf("kept %q", c.String())
	}

	// A character the cut splits is left out, so the line stays UTF-8.
	for _, tail := range []string{"🙂", "日", "é"} {
		c := &cappedBuffer{max: 9}
		c.Write([]byte("     err"))
		c.Write([]byte(tail + "tail"))
		if got := c.String(); !utf8.ValidString(got) || (got != "     err" && got != "     err"+tail) {
			t.Errorf("cut through %q: kept %q", tail, got)
		}
		if line := firstLine(c.String(), "x"); line != "err" && line != "err"+tail {
			t.Errorf("cut through %q: firstLine %q", tail, line)
		}
	}
	// What fits whole isn't cut.
	c = &cappedBuffer{max: 64}
	c.Write([]byte("über 日本語 🙂"))
	if c.String() != "über 日本語 🙂" {
		t.Errorf("kept %q", c.String())
	}

	// Writes from more than one goroutine, as exec's copying makes them, don't race.
	c = &cappedBuffer{max: 1000}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				c.Write([]byte("0123456789"))
			}
		}()
	}
	wg.Wait()
	if len(c.String()) != 1000 {
		t.Errorf("kept %d bytes, want 1000", len(c.String()))
	}
}

// A command that ran past SendTimeout may have handed the message over before it was killed: the
// error says it took too long, and never wraps ErrNotStarted, so the daemon doesn't send it again
// into a duplicate (internal/daemon's notReached).
func TestTimeoutIsNotNotStarted(t *testing.T) {
	defer func(d time.Duration) { sendTimeout = d }(sendTimeout)
	sendTimeout = 5 * time.Second
	exe, calls := fake(t, "hang")
	for name, send := range map[string]func() error{
		"codex":    func() error { return CodexQueue(context.Background(), exe, "", "thread-1", "x") },
		"opencode": func() error { return OpenCodePrompt(context.Background(), exe, "", "ses_1", "x") },
	} {
		start := time.Now()
		err := send()
		if err == nil || !strings.Contains(err.Error(), "took over 5s") {
			t.Errorf("%s: err = %v, want a timeout", name, err)
		}
		if errors.Is(err, ErrNotStarted) || errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v reads as not started, or as the caller's deadline", name, err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: took %s", name, took)
		}
	}
	// The stand-in had the message before it hung: it reached the client.
	if n := len(calls()); n != 2 {
		t.Errorf("%d calls recorded, want 2", n)
	}
}

// grandchild sets the stand-in up to leave a process holding its standard error, and makes sure
// that process has gone before the test ends (Windows can't remove a test binary still running).
func grandchild(t *testing.T, mode string) (exe, lock string) {
	t.Helper()
	saved := WaitDelay
	t.Cleanup(func() { WaitDelay = saved })
	WaitDelay = time.Second
	exe, _ = fake(t, mode)
	dir := t.TempDir()
	lock, alive := filepath.Join(dir, "grandchild.lock"), filepath.Join(dir, "grandchild.alive")
	if err := os.WriteFile(alive, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeLockEnv, lock)
	t.Setenv(fakeAliveEnv, alive)
	t.Cleanup(func() {
		os.Remove(alive)
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(15 * time.Millisecond) {
			if held, err := proc.Lock(lock); err == nil {
				held.Close()
				return
			}
		}
		t.Errorf("the grandchild still holds %s", lock)
	})
	return exe, lock
}

// stillHeld says the grandchild was still running, holding the pipe, when the send returned: the
// send didn't wait for it.
func stillHeld(t *testing.T, lock string) {
	t.Helper()
	held, err := proc.Lock(lock)
	if err == nil {
		held.Close()
		t.Fatal("the grandchild had gone when the send returned, so WaitDelay wasn't what ended it")
	}
	if !errors.Is(err, proc.ErrLocked) {
		t.Fatal(err)
	}
}

// A command that exits 0, leaving a process it started holding standard error, has sent: the send
// returns once WaitDelay is up, with no error.
func TestExitZeroWithAPipeStillHeldSucceeds(t *testing.T) {
	exe, lock := grandchild(t, "grandchild-exit0")
	start := time.Now()
	if err := OpenCodePrompt(context.Background(), exe, "", "ses_1", "x"); err != nil {
		t.Fatalf("err = %v", err)
	}
	stillHeld(t, lock)
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("took %s", took)
	}
}

// One that fails, leaving the same, says what it printed, and isn't ErrNotStarted.
func TestFailureWithAPipeStillHeldSaysWhy(t *testing.T) {
	exe, lock := grandchild(t, "grandchild-fail")
	err := CodexQueue(context.Background(), exe, "", "thread-1", "x")
	if err == nil || !strings.Contains(err.Error(), "Error: thread is locked") || errors.Is(err, ErrNotStarted) {
		t.Fatalf("err = %v, want the line it printed, and not ErrNotStarted", err)
	}
	stillHeld(t, lock)
}
