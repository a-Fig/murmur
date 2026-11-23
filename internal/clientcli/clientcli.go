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
//     (POST /api/session/{sessionID}/prompt), the one registered in the state directory the command
//     is run with (XDG_STATE_HOME). "queue" waits behind a running turn; the default,
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
	"sync"
	"time"
	"unicode"
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

// WaitDelay is how long run waits, after the command has exited or been killed, for whatever it
// started that still holds standard error: `opencode api` starts the shared server when none is
// running, and a handoff that never came back would hold its session's messages behind it for good.
// sendTimeout and WaitDelay are variables so tests needn't wait them out.
var (
	sendTimeout = SendTimeout
	WaitDelay   = 5 * time.Second
)

// maxStderr is how much of the command's standard error run keeps: firstLine reads only the first
// line that isn't blank, and a client that printed a lot would otherwise grow the daemon with it.
const maxStderr = 8 << 10

// startLimit is how long the command may take to start (proc.StartWithin).
const startLimit = 30 * time.Second

// CodexQueue queues text for thread with the Codex at exe. home, if not empty, is the Codex home
// the thread is in (CODEX_HOME).
//
// It returns once Codex has queued the message, not when a turn has run. The error says what Codex
// printed, in a line, unless that line quotes the start of the text.
func CodexQueue(ctx context.Context, exe, home, thread, text string) error {
	if exe == "" || !validID(thread) {
		return errors.New("no Codex command or thread to queue for")
	}
	if hasNUL(exe, home, text) {
		return errors.New("codex queue: a NUL byte in the command, the Codex home or the text")
	}
	var env []string
	if home != "" {
		env = []string{"CODEX_HOME", home}
	}
	return run(ctx, "codex queue", exe, []string{"queue", "--thread", thread, "--message=" + text}, env, text)
}

// OpenCodePrompt sends text to session through the background server of the OpenCode at exe. state,
// if not empty, is the state directory that server is registered in (XDG_STATE_HOME): another one
// would have the command ask another server, or start one, without the session (#486).
//
// It returns once the server has taken the message, not when a turn has run. The error is the
// HTTP status the command printed.
func OpenCodePrompt(ctx context.Context, exe, state, session, text string) error {
	if exe == "" || len(session) <= len("ses") || !strings.HasPrefix(session, "ses") || !validID(session) {
		return errors.New("no OpenCode command or session to send to")
	}
	if hasNUL(exe, state, text) {
		return errors.New("opencode api: a NUL byte in the command, the state directory or the text")
	}
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"text": text, "delivery": "queue"}); err != nil {
		return err
	}
	var env []string
	if state != "" {
		env = []string{"XDG_STATE_HOME", state}
	}
	return run(ctx, "opencode api", exe,
		[]string{"api", "session.prompt", "--param", "sessionID=" + session, "-d", strings.TrimSpace(body.String())}, env, text)
}

// run runs exe with args, in this process's environment with env's pairs (name, value) set over
// it. name is what errors call the command, and text what they must not hold.
func run(parent context.Context, name, exe string, args, env []string, text string) error {
	if !fits(exe, args) {
		return fmt.Errorf("%s: %w", name, ErrTooLong)
	}
	timeout := sendTimeout
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	vars := os.Environ()
	for i := 0; i+1 < len(env); i += 2 {
		vars = withEnv(vars, env[i], env[i+1])
	}
	cmd.Env = vars
	// Standard output is left unread: OpenCode prints the server's answer there, which holds the
	// text. Standard error is where both commands say what went wrong.
	errOut := &cappedBuffer{max: maxStderr}
	cmd.Stderr = errOut
	cmd.WaitDelay = WaitDelay
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
	if err := cmd.Wait(); err != nil {
		// No error from here on wraps ErrNotStarted: the command ran, so the client may have the
		// message, and the daemon mustn't try again into a duplicate.
		switch {
		case errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success():
			return nil // it exited 0, and something it started kept the pipe past WaitDelay
		case parent.Err() != nil:
			return fmt.Errorf("%s: %w", name, parent.Err())
		case ctx.Err() != nil:
			return fmt.Errorf("%s took over %s", name, timeout)
		}
		if line := firstLine(errOut.String(), text); line != "" {
			return fmt.Errorf("%s: %v: %s", name, err, line)
		}
		return fmt.Errorf("%s: %v", name, err)
	}
	return nil
}

// validID says id can go on the command line as a thread's or a session's: not empty, not read as
// a flag, and with no space, control character or NUL in it. Codex's thread IDs are UUIDs and
// OpenCode's session IDs "ses_" and letters and digits, so none of this turns a real one away.
func validID(id string) bool {
	if id == "" || strings.HasPrefix(id, "-") || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// hasNUL says one of ss holds a NUL byte, which no command line or environment can carry. exec
// would refuse it as a start that failed, ErrNotStarted, and the daemon would try it again for
// nothing.
func hasNUL(ss ...string) bool {
	for _, s := range ss {
		if strings.IndexByte(s, 0) >= 0 {
			return true
		}
	}
	return false
}

// withEnv is env with any entry for key replaced by key=val. Names are matched without regard to
// case, as Windows matches them: an inherited codex_home left beside CODEX_HOME could be the one
// the command reads.
func withEnv(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, key) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, key+"="+val)
}

// cappedBuffer keeps the first max bytes written to it and drops the rest without an error: an
// error would fail the command's own writes to standard error, or have Wait report one.
type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rem := c.max - c.buf.Len(); len(p) > rem {
		c.buf.Write(p[:max(rem, 0)])
		c.truncated = true
	} else {
		c.buf.Write(p)
	}
	return len(p), nil
}

// String is what was kept, less a character the cut split in two.
func (c *cappedBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.buf.Bytes()
	if c.truncated {
		for i := len(b) - 1; i >= 0 && len(b)-i <= utf8.UTFMax; i-- {
			if utf8.RuneStart(b[i]) {
				if !utf8.FullRune(b[i:]) {
					b = b[:i]
				}
				break
			}
		}
	}
	return string(b)
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
