package mcpserver

import (
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proto"
)

// akiaJoined is a key with a zero-width space after it: cleaned, the space goes and the key runs
// into "note", with no word boundary for the check to find, so only the text as written shows it.
const akiaJoined = "AKIA" + "IOSFODNN7EXAMPLE\u200bnote"

// A subject is checked for secrets as written, as a body is (#1044): only its cleaned line goes to
// the daemon, and a key a zero-width space joins to the next word is found only in the raw text.
func TestASubjectIsCheckedAsWritten(t *testing.T) {
	for _, c := range []struct{ name, subject, body string }{
		{"a one-line text", akiaJoined, ""},
		{"a subject with a body", akiaJoined, "the body"},
		{"the first line of several", akiaJoined + "\nmore", ""},
	} {
		if _, _, err := subjectAndBody(c.subject, c.body); err == nil || !strings.Contains(err.Error(), "an AWS access key") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	for _, args := range [][]string{{"a", akiaJoined}, {"a", "--subject", akiaJoined, "body"}, {"a", akiaJoined, "body"}} {
		if _, err := commandRequest("send", args, strings.NewReader(""), io.Discard); err == nil || !strings.Contains(err.Error(), "an AWS access key") {
			t.Errorf("mur send %q: %v", args, err)
		}
	}
}

// The send tool refuses it without asking the daemon, and doesn't offer a file for it, as it does
// for a message too long to send.
func TestTheSendToolRefusesARawSecretInTheSubject(t *testing.T) {
	f := &fakeDaemon{}
	res := callListTool(t, f, "send", map[string]any{"to": "quiet-otter", "subject": akiaJoined})
	text := res.Content[0].(*mcp.TextContent).Text
	if !res.IsError || !strings.Contains(text, "an AWS access key") || strings.Contains(text, "put the text in a file") {
		t.Errorf("send of a raw key as the subject: error %v, %q", res.IsError, text)
	}
	if got := f.requestsOf(proto.OpSend); len(got) != 0 {
		t.Errorf("the daemon was asked to send %+v", got)
	}
	res = callListTool(t, f, "send", map[string]any{"to": "quiet-otter", "subject": strings.Repeat("x", bus.MaxBody+1024)})
	if text := res.Content[0].(*mcp.TextContent).Text; !res.IsError || !strings.Contains(text, "message is over 32 KB") || !strings.Contains(text, "put the text in a file") {
		t.Errorf("send of a text too long: error %v, %q", res.IsError, text)
	}
}

// What the check lets through as a body, it lets through as a subject: ordinary text with
// invisible characters in it, and #1041's must-pass shapes.
func TestASubjectAsWrittenPassesWhatABodyDoes(t *testing.T) {
	for _, s := range []string{
		"ready\u200b to merge",
		"\u200b\u200bheads up: #1043 is green",
		`{"path": "C:\\Users\\tyler\\murmur\\internal\\scrub"}`,
		`{"url": "https:\/\/github.com\/a-Fig\/murmur\/pull\/7"}`,
		"API_KEY = process.env.API_KEY",
		"the sk- prefix is for keys",
	} {
		if _, _, err := subjectAndBody(s, ""); err != nil {
			t.Errorf("blocked %q: %v", s, err)
		}
		if _, _, err := subjectAndBody(s, "a body"); err != nil {
			t.Errorf("blocked %q with a body: %v", s, err)
		}
	}
}
