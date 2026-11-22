package clientcli

import (
	"context"
	"os"
	"testing"
)

// These run the real programs, and so only where a person asks: each puts a message in a real
// session, which costs its user a model call. They are how the package's claims about Codex and
// OpenCode were checked (docs/design/handoff.md).
//
//	MURMUR_REAL_CODEX=<codex program> MURMUR_REAL_CODEX_THREAD=<thread> go test ./internal/clientcli -run RealCodex
//	MURMUR_REAL_OPENCODE=<opencode program> MURMUR_REAL_OPENCODE_SESSION=<session> go test ./internal/clientcli -run RealOpenCode

// realText has what a command line could lose: quotes, a backslash before one, newlines, a percent
// sign, and characters outside ASCII.
const realText = "Message from quiet-otter via Murmur (a test of clientcli):\n\n" +
	"Reply with exactly this line: \"MANGO\" \\\"100%\\\" über 日本語\n\nNo reply through Murmur is needed.\n"

func TestRealCodexQueue(t *testing.T) {
	exe, thread := os.Getenv("MURMUR_REAL_CODEX"), os.Getenv("MURMUR_REAL_CODEX_THREAD")
	if exe == "" || thread == "" {
		t.Skip("MURMUR_REAL_CODEX and MURMUR_REAL_CODEX_THREAD aren't set")
	}
	if err := CodexQueue(context.Background(), exe, os.Getenv("CODEX_HOME"), thread, realText); err != nil {
		t.Fatal(err)
	}
}

func TestRealOpenCodePrompt(t *testing.T) {
	exe, session := os.Getenv("MURMUR_REAL_OPENCODE"), os.Getenv("MURMUR_REAL_OPENCODE_SESSION")
	if exe == "" || session == "" {
		t.Skip("MURMUR_REAL_OPENCODE and MURMUR_REAL_OPENCODE_SESSION aren't set")
	}
	if err := OpenCodePrompt(context.Background(), exe, os.Getenv("XDG_STATE_HOME"), session, realText); err != nil {
		t.Fatal(err)
	}
}
