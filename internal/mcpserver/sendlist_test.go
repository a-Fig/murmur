package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-Fig/murmur/internal/proto"
)

// send's to names one recipient, or several separated by commas, or a JSON array a model wrote
// into the string (#520).
func TestRecipients(t *testing.T) {
	for _, c := range []struct {
		to   string
		want []string
	}{
		{"quiet-otter", []string{"quiet-otter"}},
		{" quiet-otter , brisk-finch,", []string{"quiet-otter", "brisk-finch"}},
		{`["quiet-otter", "dev/aaaaaa"]`, []string{"quiet-otter", "dev/aaaaaa"}},
		{"quiet-otter,quiet-otter, quiet-otter", []string{"quiet-otter"}},
		{"[not json", []string{"[not json"}},
		{" , ", nil},
		{"", nil},
	} {
		if got := recipients(c.to); !slices.Equal(got, c.want) {
			t.Errorf("recipients(%q) = %q, want %q", c.to, got, c.want)
		}
	}
}

// listDaemon answers sends as a daemon would for two sessions: quiet-otter at dev/aaaaaa and
// brisk-finch at dev/bbbbbb. Anything else is a name nobody has.
func listDaemon() *fakeDaemon {
	at := map[string]string{"quiet-otter": "dev/aaaaaa", "brisk-finch": "dev/bbbbbb", "dev/aaaaaa": "dev/aaaaaa", "dev/bbbbbb": "dev/bbbbbb"}
	return &fakeDaemon{sendReply: func(req proto.Request) proto.Response {
		addr, ok := at[req.To]
		if !ok {
			return proto.Response{Error: "no session is called " + req.To + "; see who"}
		}
		name := ""
		if addr != req.To {
			name = req.To
		}
		return proto.Response{OK: true, Messages: []proto.MessageView{{ID: "id-" + strings.TrimPrefix(addr, "dev/"), To: addr, ToName: name, Subject: req.Subject}}}
	}}
}

func (f *fakeDaemon) sends() []proto.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []proto.Request
	for _, r := range f.requests {
		if r.Op == proto.OpSend {
			out = append(out, r)
		}
	}
	return out
}

func callSend(t *testing.T, to string) (*fakeDaemon, *mcp.CallToolResult) {
	t.Helper()
	f := listDaemon()
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, time.Hour)
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "send",
		Arguments: map[string]any{"to": to, "subject": "the API changed", "body": "details"}})
	if err != nil {
		t.Fatal(err)
	}
	return f, res
}

// A send to a list is a send to each: one ordinary send per recipient, with the same subject and
// body. One that fails, a group, and a session named twice, once by name and once by address, don't
// stop the rest, and the result says what became of each, in the order named.
func TestSendToAListSendsToEachOnce(t *testing.T) {
	f, res := callSend(t, "dev/aaaaaa, quiet-otter, group:ui, calm-heron, brisk-finch, *")
	if res.IsError {
		t.Fatalf("a send that reached two recipients is an error: %+v", res)
	}
	var got []string
	for _, r := range f.sends() {
		if r.Subject != "the API changed" || r.Body != "details" {
			t.Errorf("a send carried %q / %q", r.Subject, r.Body)
		}
		got = append(got, r.To)
	}
	// Names first, so the address of a session already sent to by name is known.
	if want := []string{"quiet-otter", "calm-heron", "brisk-finch"}; !slices.Equal(got, want) {
		t.Errorf("the daemon was asked to send to %q, want %q", got, want)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	want := "Sent to 2 of 6, each as a message of its own:\n" +
		"- dev/aaaaaa is quiet-otter, so it got the message once.\n" +
		"- Sent id-aaaaaa to quiet-otter (dev/aaaaaa).\n" +
		"- Not sent to group:ui: a list is for sessions and people, each of whom gets a message of their own; send to group:ui on its own\n" +
		"- Not sent to calm-heron: no session is called calm-heron; see who\n" +
		"- Sent id-bbbbbb to brisk-finch (dev/bbbbbb).\n" +
		"- Not sent to *: a list is for sessions and people, each of whom gets a message of their own; send to * on its own\n"
	if text != want {
		t.Errorf("result:\n%s\nwant:\n%s", text, want)
	}
}

// A list none of whose recipients got the message is an error, so the sender can't take it for sent.
func TestSendToAListNobodyGotIsAnError(t *testing.T) {
	_, res := callSend(t, "calm-heron, group:ui")
	if !res.IsError {
		t.Errorf("a list nobody got isn't an error: %+v", res.Content[0].(*mcp.TextContent).Text)
	}
}

// One recipient, even with a comma after it, is the send it always was.
func TestSendToOneIsUnchanged(t *testing.T) {
	f, res := callSend(t, "quiet-otter,")
	if got := f.sends(); len(got) != 1 || got[0].To != "quiet-otter" {
		t.Fatalf("sends = %+v", got)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != "Sent id-aaaaaa to quiet-otter (dev/aaaaaa).\n" {
		t.Errorf("result = %q", text)
	}
}

// Past the limit, nothing is sent.
func TestSendToTooManyIsRefused(t *testing.T) {
	var many []string
	for i := range maxRecipients + 1 {
		many = append(many, fmt.Sprintf("dev/s%d", i))
	}
	f, res := callSend(t, strings.Join(many, ","))
	if !res.IsError || len(f.sends()) != 0 {
		t.Errorf("a send to %d: error %v, %d sends", len(many), res.IsError, len(f.sends()))
	}
}
