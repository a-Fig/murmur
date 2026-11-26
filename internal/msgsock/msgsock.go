// Package msgsock writes a user message into a running Claude Code session through its messaging
// socket, so the message arrives as a turn of the session. Claude Code offers the socket to the
// processes it starts: CLAUDE_CODE_MESSAGING_SOCKET is its address (a unix socket, or a named
// pipe on Windows) and CLAUDE_CODE_MESSAGING_TOKEN opens it. The protocol, as observed 2026-10-29,
// is lines of JSON: an auth line {"type":"auth","token":...}, then a user line
// {"type":"user","message":{"role":"user","content":...}}. The token and the address stay on the
// device, so nothing here puts either in an error. Murmur's Hermes plugin opens a socket that takes
// the same lines and answers each message (SendAwaiting).
package msgsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strings"
	"time"
)

// ErrNotConnected is what Send's error wraps when it could not connect to the socket, so that
// nothing was written: the one failure that is safe to try again. A failure after the connection,
// a write that errors, may have written some or all of the lines, and the socket answers nothing
// that would say which.
var ErrNotConnected = errors.New("couldn't connect")

// MaxLine is the longest line Send writes. The sender's text is cut well below it (see view.Turn).
const MaxLine = 1 << 20

// Timeout is how long Send waits to connect and write when the context sets no sooner end.
const Timeout = 5 * time.Second

type userLine struct {
	Type    string      `json:"type"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Answers says whether something accepts a connection at path, within Timeout or the context's end.
// It writes nothing: the connection is closed at once. Murmur's Hermes plugin keeps a session's
// socket for as long as Hermes can start a turn in the session, so the daemon asks this to know the
// session is still open (#1184).
func Answers(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	conn, err := dial(ctx, path)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Send connects to the socket at path, authenticates with token and writes text as a user message.
// It returns once the lines are written: the session is not asked whether it took them, as
// Claude Code's socket has no reply to ask for.
func Send(ctx context.Context, path, token, text string) error {
	return send(ctx, path, token, text, false)
}

// ErrRefused is what SendAwaiting's error wraps when the socket answered that the session refused
// the message: it took none of it, so the message is still the sender's to give.
var ErrRefused = errors.New("the session refused the message")

// ReplyTimeout is how long SendAwaiting waits for the answer once its lines are written. Hermes'
// inject_message only schedules the turn, so the plugin answers within milliseconds.
const ReplyTimeout = 15 * time.Second

// SendAwaiting is Send to a socket that answers each message with one line, {"type":"accepted"} or
// {"type":"refused"}, as Murmur's Hermes plugin's does (#1258). It returns nil only once the socket
// says the session took the message, and an error wrapping ErrRefused when it says the session
// refused it. Anything else, such as no answer within ReplyTimeout, the connection closing first,
// or a line it doesn't know, is an error that says neither: the session may have taken it.
func SendAwaiting(ctx context.Context, path, token, text string) error {
	return send(ctx, path, token, text, true)
}

func send(ctx context.Context, path, token, text string, await bool) error {
	if path == "" || token == "" {
		return errors.New("no messaging socket or token")
	}
	auth, err := json.Marshal(map[string]string{"type": "auth", "token": token})
	if err != nil {
		return err
	}
	msg, err := json.Marshal(userLine{Type: "user", Message: userMessage{Role: "user", Content: text}})
	if err != nil {
		return err
	}
	if len(msg) > MaxLine {
		return fmt.Errorf("a message of %d bytes is over the %d byte limit of the messaging socket", len(msg), MaxLine)
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	conn, err := dial(ctx, path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotConnected, clean(err, path, token))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	out := make([]byte, 0, len(auth)+len(msg)+2)
	out = append(append(append(append(out, auth...), '\n'), msg...), '\n')
	if _, err := conn.Write(out); err != nil {
		return clean(err, path, token)
	}
	if !await {
		return nil
	}
	// The answer gets its own wait, from the end of the write, cut short only by the caller's
	// context: a daemon that stops doesn't wait on it.
	dl := time.Now().Add(ReplyTimeout)
	if pdl, ok := parent.Deadline(); ok && pdl.Before(dl) {
		dl = pdl
	}
	conn.SetDeadline(dl)
	stop := context.AfterFunc(parent, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	line, err := bufio.NewReaderSize(conn, 4096).ReadSlice('\n')
	if err != nil {
		return fmt.Errorf("no answer from the socket: %w", clean(err, path, token))
	}
	var reply struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &reply); err != nil {
		return fmt.Errorf("an answer from the socket that isn't JSON: %w", err)
	}
	switch reply.Type {
	case "accepted":
		return nil
	case "refused":
		return ErrRefused
	}
	return fmt.Errorf("an answer from the socket of an unknown type %q", reply.Type)
}

// clean strips the address and the token out of an error: the net and os packages put the address
// in theirs.
func clean(err error, path, token string) error {
	var op *net.OpError
	var pe *fs.PathError
	switch {
	case errors.As(err, &op) && op.Err != nil:
		err = fmt.Errorf("%s: %w", op.Op, op.Err)
	case errors.As(err, &pe) && pe.Err != nil:
		err = fmt.Errorf("%s: %w", pe.Op, pe.Err)
	}
	s := err.Error()
	for _, secret := range []string{path, token} {
		s = strings.ReplaceAll(s, secret, "<redacted>")
	}
	if s == err.Error() {
		return err
	}
	return errors.New(s)
}
