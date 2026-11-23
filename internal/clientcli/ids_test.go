package clientcli

import (
	"context"
	"testing"
)

// Every ID the clients were seen to give, and the shapes the other tests use, still goes through
// the checks on what can go on a command line, and onto it as it was.
//
//   - Codex names a thread by a UUID (CODEX_THREAD_ID); the ones here are version 7, the shape of
//     the native IDs in internal/mcpserver's tests, in either case.
//   - OpenCode names a session "ses_" and 26 letters and digits: 12 hex digits of time and
//     counter, then 14 random ones (Identifier.ascending in its src/id/id.ts).
var realThreads = []string{
	"019a5f2c-3b1e-7c42-9d3a-1b2c3d4e5f60",
	"01a190f5-a509-7000-bd17-2120db057488",
	"01A190F5-A509-7000-BD17-2120DB057488",
	"0199aaaa-thread",
	"thread-1",
}

var realSessions = []string{
	"ses_4d6b5a5f2ffeT7h6PZXxwq3fGc",
	"ses_7f1e2d3c4b5a0123456789AbCd",
	"ses_abc123",
	"ses_bob123",
	"ses_parent",
	"ses_old_child",
	"ses_1",
}

func TestRealIDsAreAccepted(t *testing.T) {
	exe, calls := fake(t, "")
	ctx := context.Background()
	for _, thread := range realThreads {
		if err := CodexQueue(ctx, exe, "", thread, "x"); err != nil {
			t.Errorf("CodexQueue(%q): %v", thread, err)
		}
	}
	for _, session := range realSessions {
		if err := OpenCodePrompt(ctx, exe, "", session, "x"); err != nil {
			t.Errorf("OpenCodePrompt(%q): %v", session, err)
		}
	}
	got := calls()
	if len(got) != len(realThreads)+len(realSessions) {
		t.Fatalf("%d calls, want %d", len(got), len(realThreads)+len(realSessions))
	}
	for i, thread := range realThreads {
		if a := got[i].Args; len(a) < 3 || a[2] != thread {
			t.Errorf("thread %q: run as %q", thread, a)
		}
	}
	for i, session := range realSessions {
		if a := got[len(realThreads)+i].Args; len(a) < 4 || a[3] != "sessionID="+session {
			t.Errorf("session %q: run as %q", session, a)
		}
	}
}
