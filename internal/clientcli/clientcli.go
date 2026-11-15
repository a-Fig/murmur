// Package clientcli hands a message to a session through its client's own command line, for the
// two clients that have one for it outside T3 Code (#135). Each puts the text in the session as a
// message from the user: an idle session starts a turn, and a busy one takes it as its next turn,
// interrupting nothing.
//
//   - Codex: `codex queue --thread <thread> --message <text>` adds the message to the thread's
//     queue, in the Codex home the thread is kept in. A thread that is open takes it at once: seen
//     on Codex 0.167.0 (2026-11-11), in a terminal session that sat idle, where the message showed
//     as a prompt and the turn ran. A thread nobody has open keeps it until it is next resumed,
//     which is why the daemon asks for the session's MCP server to be connected first.
//   - OpenCode 2: `opencode api session.prompt --param sessionID=<session>` with the body
//     {"text": ..., "delivery": "queue"} asks the background server every OpenCode 2 client shares
//     (POST /api/session/{sessionID}/prompt). "queue" waits behind a running turn; the default,
//     "steer", goes into it. Seen on OpenCode 2.2.0 the same day: the turn ran, and the terminal
//     UI open on the session showed the message and the answer. `session.synthetic` takes the
//     same body and starts a turn too, but the terminal UI doesn't show its text, so the user
//     would see their agent answer something they can't read.
//
// Neither command reads the text from its standard input or a file (`opencode api -d -` and
// `-d @file` were tried), so it goes on the command line. That is why the program has to be the
// client's real one (proc.Exe of the process that started the MCP server) and not the name on
// PATH: npm's Windows shims are .cmd files, and cmd.exe has no quoting that carries a newline.
//
// A command line can be read by the machine's other users while the command runs (about a second;
// /proc/<pid>/cmdline on Linux), and is recorded by process auditing where that is on. The other
// ways in give the text to the client over a pipe or a socket.
package clientcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-Fig/murmur/internal/proc"
)

// ErrNotStarted is what an error wraps when the command never started, so that nothing can have
// reached the client: the one failure that is safe to try again. After any other the client may
// have the message, and neither command takes a key that would make a second call harmless.
var ErrNotStarted = errors.New("couldn't start the command")

// ErrTooLong is what an error wraps when the text doesn't fit on a command line (Windows allows
// 32,767 characters). Nothing was started.
var ErrTooLong = errors.New("the message is too long for a command line")

// SendTimeout bounds one command. Each took about a second and a half here (2026-11-11).
const SendTimeout = 30 * time.Second

// startLimit is how long the command may take to start (proc.StartWithin).
const startLimit = 30 * time.Second

// CodexQueue queues text for thread with the Codex at exe. home, if not empty, is the Codex home
// the thread is in (CODEX_HOME).
//
// It returns once Codex has queued the message, not when a turn has run. The error says what Codex
// printed, in a line, unless that line quotes the start of the text.
func CodexQueue(ctx context.Context, exe, home, thread, text string) error {
	if exe == "" || thread == "" || strings.HasPrefix(thread, "-") {
		return errors.New("no Codex command or thread to queue for")
	}
	var env []string
	if home != "" {
		env = []string{"CODEX_HOME=" + home}
	}
	return run(ctx, "codex queue", exe, []string{"queue", "--thread", thread, "--message=" + text}, env, text)
}

// OpenCodePrompt sends text to session through the background server of the OpenCode at exe.
//
// It returns once the server has taken the message, not when a turn has run. The error is the
// HTTP status the command printed.
func OpenCodePrompt(ctx context.Context, exe, session, text string) error {
	if exe == "" || !strings.HasPrefix(session, "ses") {
		return errors.New("no OpenCode command or session to send to")
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"text": text, "delivery": "queue"}); err != nil {
		return err
	}
	return run(ctx, "opencode api", exe,
		[]string{"api", "session.prompt", "--param", "sessionID=" + session, "-d", strings.TrimSpace(body.String())}, nil, text)
}

// run runs exe with args, and env added to this process's environment. name is what errors call
// the command, and text what they must not hold.
func run(parent context.Context, name, exe string, args, env []string, text string) error {
	if !fits(exe, args) {
		return fmt.Errorf("%s: %w", name, ErrTooLong)
	}
	ctx, cancel := context.WithTimeout(parent, SendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Env = append(os.Environ(), env...)
	// Standard output is left unread: OpenCode prints the server's answer there, which holds the
	// text. Standard error is where both commands say what went wrong.
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	// Without a WaitDelay, Wait also waits for whatever the command started that still holds
	// standard error: `opencode api` starts the shared server when none is running, and a handoff
	// that never came back would hold its session's messages behind it for good.
	cmd.WaitDelay = 5 * time.Second
	hideWindow(cmd)
	if err := proc.StartWithin(cmd, startLimit, cancel); err != nil {
		if errors.Is(err, proc.ErrStartHung) {
			// Not ErrNotStarted, so it isn't tried again: a start that comes back late may still
			// hand the message over (see t3.Send).
			return fmt.Errorf("%s: %w", name, err)
		}
		if parent.Err() != nil {
			return fmt.Errorf("%s: %w: %w", name, ErrNotStarted, parent.Err())
		}
		return fmt.Errorf("%s: %w: %v", name, ErrNotStarted, err)
	}
	if err := cmd.Wait(); err != nil && !errors.Is(err, exec.ErrWaitDelay) { // that one: it exited cleanly, and something it started kept the pipe
		switch {
		case parent.Err() != nil:
			return fmt.Errorf("%s: %w", name, parent.Err())
		case ctx.Err() != nil:
			return fmt.Errorf("%s took over %s", name, SendTimeout)
		}
		if line := firstLine(errOut.String(), text); line != "" {
			return fmt.Errorf("%s: %v: %s", name, err, line)
		}
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

// firstLine is the first line of what the command printed that isn't blank, kept short, and "" if
// it quotes the start of text: an error is logged, and a message's text shouldn't be. A line that
// quoted only a later part of the text would get through; neither command was seen to quote any.
func firstLine(s, text string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if quoted := firstWords(text); quoted != "" && strings.Contains(line, quoted) {
				return ""
			}
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

// firstWords is the start of text's first line, as much as would show that a line quotes it.
func firstWords(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	if len(text) > 40 {
		text = text[:40]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}
