package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A subject or a status goes to the daemon cut: a subject to maxSubject (#876), a status to what
// the daemon keeps of it, 120 characters. The daemon reads a request's line only up to a cap, so
// a subject far longer than that beside a full body would otherwise make the send too long to be
// read at all (Sol review of #313, 2026-11-09).
func TestSubjectAndStatusAreCutBeforeTheyAreSent(t *testing.T) {
	f := &fakeDaemon{}
	p := repo(t, true, f)
	if _, err := p.RecordConsent(); err != nil {
		t.Fatal(err)
	}
	cs, _ := open(t, p.Top, time.Hour)
	waitFor(t, "the session to register", func() bool { return f.helloCount() == 1 })
	ctx := context.Background()
	long := strings.Repeat("<", 12<<10)
	for _, call := range []*mcp.CallToolParams{
		{Name: "send", Arguments: map[string]any{"to": "quiet-otter", "subject": long, "body": strings.Repeat("<", 32<<10)}},
		{Name: "status", Arguments: map[string]any{"doing": long}},
	} {
		if res, err := cs.CallTool(ctx, call); err != nil || res.IsError {
			t.Fatalf("%s: %v %+v", call.Name, err, res)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 2 {
		t.Fatalf("the daemon saw %d requests, want 2", len(f.requests))
	}
	if n := utf8.RuneCountInString(f.requests[0].Subject); n != maxSubject {
		t.Errorf("the subject went as %d characters, want %d", n, maxSubject)
	}
	if n := utf8.RuneCountInString(f.requests[1].Doing); n != 120 {
		t.Errorf("the status went as %d characters, want 120", n)
	}
}
