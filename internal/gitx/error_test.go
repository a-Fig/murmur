package gitx_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/gitx"
)

// A failed git command's message carries what git printed to stderr, the remote's "remote:" lines
// among them, and it reaches people's terminals: none of it may act on the terminal (#1332).
func TestErrorCleansWhatTheRemotePrinted(t *testing.T) {
	stderr := "remote: \x1b]8;;https://evil.example/\x1b\\https://github.com/a/b\x1b]8;;\x1b\\\n" +
		"remote: \x1b[1A\x1b[2K\x1b]0;pwned\x07main is protected\n"
	e := &gitx.Error{Args: []string{"push", "origin"}, Stderr: stderr, Err: errors.New("exit status 1")}
	msg := e.Error()
	if strings.ContainsAny(msg, "\x1b\x07") {
		t.Errorf("the message keeps a terminal escape: %q", msg)
	}
	want := "git push origin: remote: https://github.com/a/b\nremote: main is protected"
	if msg != want {
		t.Errorf("the message is %q, want %q", msg, want)
	}
	if e.Stderr != stderr {
		t.Errorf("Stderr changed to %q; code that reads it gets it as git printed it", e.Stderr)
	}
}
