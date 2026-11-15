// Package t3 is Murmur's way into T3 Code, the app that runs many agent sessions on one machine. A
// session T3 runs is a thread, and T3's own command line, `t3 thread send`, puts a message in one:
// at once if the thread is idle, as its next turn if it is busy, interrupting nothing. A message to
// a thread is how T3's agents talk to each other, and it is how Murmur hands a teammate's message
// to the agent it is for (docs/design/handoff.md, "T3 Code").
package t3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/proc"
)

// ErrNotStarted is what Send's error wraps when the command never started, so that nothing can have
// reached T3: the one failure that is safe to try again. Any other failure leaves open whether T3
// took the message before it failed (it timed out, or exited with an error after it had accepted
// it, or its output was lost), and `t3 thread send` has no key that would make a second call
// harmless: it would be a second turn.
var ErrNotStarted = errors.New("couldn't start the command")

// SendTimeout bounds one `t3 thread send`. It takes about a second where T3 is running; a T3 that
// isn't running makes it fail sooner than this.
const SendTimeout = 30 * time.Second

// startLimit is how long `t3` may take to start (proc.StartWithin).
const startLimit = 30 * time.Second

// Homes lists the directories T3 may keep its data in, most likely first: the one T3CODE_HOME
// names, the parent of the directory the command line lives in (T3 puts it at ~/.t3/bin/t3), and
// ~/.t3, where T3 keeps its data unless told otherwise. home is T3CODE_HOME as the caller saw it.
func Homes(cli, home string) []string {
	var out []string
	add := func(dir string) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		for _, o := range out {
			if o == dir {
				return
			}
		}
		out = append(out, dir)
	}
	add(home)
	if cli != "" && strings.EqualFold(filepath.Base(filepath.Dir(cli)), "bin") {
		add(filepath.Dir(filepath.Dir(cli)))
	}
	if h, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(h, ".t3"))
	}
	return out
}

// StateDB is the path of T3's state database in a data directory.
func StateDB(home string) string { return filepath.Join(home, "userdata", "statev2.sqlite") }

// Send puts text in thread as a message from the user, the way T3's own command line does. The
// text goes through the command's standard input, so its quotes, newlines and length never meet
// Windows command-line quoting. home, if not empty, is T3's data directory (T3CODE_HOME).
//
// It returns once T3 has taken the message, not when the agent has finished with it. The error
// says what T3 printed, in a line; it never holds the text. An error that wraps ErrNotStarted means
// the message did not reach T3; any other means it may have.
func Send(ctx context.Context, cli, home, thread, text string) error {
	if cli == "" || thread == "" {
		return errors.New("no T3 command line or thread to send to")
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, SendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli, "thread", "send", thread, "-")
	cmd.Stdin = strings.NewReader(text)
	cmd.Env = os.Environ()
	if home != "" {
		cmd.Env = append(cmd.Env, "T3CODE_HOME="+home)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	hideWindow(cmd)
	if err := proc.StartWithin(cmd, startLimit, cancel); err != nil {
		if errors.Is(err, proc.ErrStartHung) {
			// Not ErrNotStarted, so it isn't tried again: a start that comes back after this gave
			// up is stopped, but may hand the message over first. The session may then have it
			// twice, as the hook's notice and as a turn, which is better than every later handoff
			// to it waiting behind this one for good.
			return fmt.Errorf("t3 thread send: %w", err)
		}
		if parent.Err() != nil { // cancelled before it could start: nothing ran, and the caller's cancellation is still the reason
			return fmt.Errorf("t3 thread send: %w: %w", ErrNotStarted, parent.Err())
		}
		return fmt.Errorf("t3 thread send: %w: %v", ErrNotStarted, err)
	}
	if err := cmd.Wait(); err != nil {
		switch {
		case parent.Err() != nil:
			return fmt.Errorf("t3 thread send: %w", parent.Err())
		case ctx.Err() != nil:
			return fmt.Errorf("t3 thread send took over %s", SendTimeout)
		}
		if line := firstLine(out.String()); line != "" {
			return fmt.Errorf("t3 thread send: %v: %s", err, line)
		}
		return fmt.Errorf("t3 thread send: %v", err)
	}
	return nil
}

// firstLine is the first line of what the command printed that isn't blank, kept short.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if len(line) > 200 {
				line = line[:200]
				for !utf8.ValidString(line) {
					line = line[:len(line)-1]
				}
			}
			return line
		}
	}
	return ""
}
