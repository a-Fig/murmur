package msgsock

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSession stands in for Claude Code's messaging socket: it accepts connections and hands every
// line it reads to lines.
type fakeSession struct {
	ln    net.Listener
	lines chan string
}

func serveLines(t *testing.T, ln net.Listener) *fakeSession {
	t.Helper()
	f := &fakeSession{ln: ln, lines: make(chan string, 16)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReaderSize(conn, MaxLine+16)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					f.lines <- strings.TrimSuffix(line, "\n")
				}
			}()
		}
	}()
	return f
}

// unixSession listens on a unix socket, which every platform has, in a directory short enough for
// macOS's limit on the path.
func unixSession(t *testing.T) (*fakeSession, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "murs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	return serveLines(t, ln), path
}

func (f *fakeSession) next(t *testing.T) string {
	t.Helper()
	select {
	case l := <-f.lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("the session got no more lines")
		return ""
	}
}

// The session gets the auth line first, then the user message, one JSON object per line.
func TestSendWritesTheAuthLineThenTheMessage(t *testing.T) {
	f, path := unixSession(t)
	text := "Message from quiet-otter via Murmur:\n\nare you \"on it\"?\n\nReply with `send`.\n"
	if err := Send(context.Background(), path, "tok-secret", text); err != nil {
		t.Fatal(err)
	}
	var auth map[string]string
	if err := json.Unmarshal([]byte(f.next(t)), &auth); err != nil || auth["type"] != "auth" || auth["token"] != "tok-secret" || len(auth) != 2 {
		t.Fatalf("auth line: %v %v", auth, err)
	}
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(f.next(t)), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || msg.Message.Content != text {
		t.Errorf("user line: %+v", msg)
	}
}

// A message with newlines, quotes and a line separator stays one line: the protocol is lines.
func TestSendKeepsTheMessageOnOneLine(t *testing.T) {
	f, path := unixSession(t)
	text := "a\nb\r\nc d \"e\" \\ f"
	if err := Send(context.Background(), path, "t", text); err != nil {
		t.Fatal(err)
	}
	f.next(t) // auth
	line := f.next(t)
	var msg userLine
	if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.Message.Content != text {
		t.Fatalf("got %q, %v", msg.Message.Content, err)
	}
	select {
	case extra := <-f.lines:
		t.Errorf("a third line: %q", extra)
	case <-time.After(100 * time.Millisecond):
	}
}

// Where nothing listens, Send fails, and what it says holds neither the address nor the token.
func TestSendFailureHidesTheAddressAndTheToken(t *testing.T) {
	dir, err := os.MkdirTemp("", "murs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "no-such-socket-9f3a")
	err = Send(context.Background(), path, "tok-secret-9f3a", "hello")
	if err == nil {
		t.Fatal("sending to nothing worked")
	}
	for _, secret := range []string{path, "no-such-socket-9f3a", "tok-secret-9f3a"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error gives away %q: %v", secret, err)
		}
	}
	// Nothing was written, so a caller may try again; it can tell from the error.
	if !errors.Is(err, ErrNotConnected) {
		t.Errorf("the error doesn't say nothing was sent: %v", err)
	}
}

// Without an address or a token there is nothing to try; a message over the limit is not sent.
func TestSendRefusesWhatCannotWork(t *testing.T) {
	f, path := unixSession(t)
	if err := Send(context.Background(), "", "t", "x"); err == nil {
		t.Error("no address: no error")
	}
	if err := Send(context.Background(), path, "", "x"); err == nil {
		t.Error("no token: no error")
	}
	if err := Send(context.Background(), path, "t", strings.Repeat("x", MaxLine)); err == nil {
		t.Error("a message over the limit: no error")
	}
	select {
	case l := <-f.lines:
		t.Errorf("the session got %q", l)
	case <-time.After(100 * time.Millisecond):
	}
}

// A context that has ended stops Send before it connects.
func TestSendStopsWithTheContext(t *testing.T) {
	_, path := unixSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Send(ctx, path, "t", "x"); err == nil {
		t.Error("a cancelled context: no error")
	}
}

// Answers says whether something accepts a connection at the address, and writes nothing there: a
// session's socket is asked it to tell whether the session is still open (#1184).
func TestAnswersSaysWhetherTheSocketTakesAConnection(t *testing.T) {
	f, path := unixSession(t)
	if !Answers(context.Background(), path) {
		t.Fatal("an open socket doesn't answer")
	}
	select {
	case l := <-f.lines:
		t.Errorf("asking wrote %q", l)
	case <-time.After(200 * time.Millisecond):
	}
	f.ln.Close()
	if Answers(context.Background(), path) {
		t.Error("a closed socket answers")
	}
	if Answers(context.Background(), "") {
		t.Error("no address answers")
	}
}

// answering listens on a unix socket that answers each user line with answer after delay, as
// Murmur's Hermes plugin's does (#1258), and closes the connection after it; "" answers nothing
// and hangs up.
func answering(t *testing.T, answer string, delay time.Duration) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "murs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if strings.HasPrefix(line, `{"type":"user"`) {
						time.Sleep(delay)
						if answer != "" {
							conn.Write([]byte(answer + "\n"))
						}
						return
					}
				}
			}()
		}
	}()
	return path
}

// SendAwaiting says what the socket answered: nil once it accepted the message, ErrRefused when it
// refused it, and an error that is neither, and isn't ErrNotConnected, when it said nothing it
// knows: the message may have gone in, so it isn't sent again.
func TestSendAwaitingSaysWhatTheSocketAnswered(t *testing.T) {
	for _, c := range []struct {
		answer  string
		refused bool
		ok      bool
	}{
		{`{"type":"accepted"}`, false, true},
		{`{"type":"refused"}`, true, false},
		{`{"type":"maybe"}`, false, false},
		{`not json`, false, false},
		{"", false, false},
	} {
		err := SendAwaiting(context.Background(), answering(t, c.answer, 0), "t", "hello")
		if (err == nil) != c.ok || errors.Is(err, ErrRefused) != c.refused || errors.Is(err, ErrNotConnected) {
			t.Errorf("answered %q: %v", c.answer, err)
		}
	}
}

// Send doesn't wait for an answer: Claude Code's socket gives none, and keeps the connection open.
func TestSendDoesntWaitForAnAnswer(t *testing.T) {
	_, path := unixSession(t)
	start := time.Now()
	if err := Send(context.Background(), path, "t", "x"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Send took %s", took)
	}
}

// A socket that says nothing gets ReplyTimeout, then an error that is neither accepted nor refused.
func TestSendAwaitingGivesUpOnASilentSocket(t *testing.T) {
	defer func(was time.Duration) { ReplyTimeout = was }(ReplyTimeout)
	ReplyTimeout = 300 * time.Millisecond
	_, path := unixSession(t) // reads the lines and never answers
	start := time.Now()
	if err := SendAwaiting(context.Background(), path, "t", "x"); err == nil || errors.Is(err, ErrRefused) || errors.Is(err, ErrNotConnected) {
		t.Errorf("no answer: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("SendAwaiting waited %s for a %s answer", took, ReplyTimeout)
	}
}

// Once the lines are written, a context that ends doesn't cut the wait for the answer short: the
// session has most likely taken the message, and giving up would tell it twice.
func TestSendAwaitingWaitsOutTheAnswerPastItsContext(t *testing.T) {
	path := answering(t, `{"type":"accepted"}`, 400*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	if err := SendAwaiting(ctx, path, "t", "x"); err != nil {
		t.Errorf("an answer after the context ended: %v", err)
	}
}
