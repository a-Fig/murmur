package join

import (
	"strings"
	"testing"
)

func envWith(vars map[string]string) Env {
	return Env{Getenv: func(k string) string { return vars[k] }}
}

// A Codex thread in T3 Code that existed before Murmur was added to Codex's config keeps lacking
// the tools, and T3 never refreshes it, so the agent is told to open a new thread, in these words
// (Tyler, 2026-10-30, from a Codex agent's measurements in T3).
func TestAdviceForACodexThreadInT3SaysToOpenANewThread(t *testing.T) {
	const want = "open a new thread in this project to talk to other agents through Murmur"
	for _, fresh := range []bool{true, false} {
		e := envWith(map[string]string{"CODEX_THREAD_ID": "019a", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"})
		got := strings.Join(e.advice(false, fresh, false), "\n")
		if !strings.Contains(got, want) {
			t.Errorf("codexFresh %v: advice doesn't say %q:\n%s", fresh, want, got)
		}
		if strings.Contains(got, "claude --continue") || strings.Contains(got, "Stop in T3") {
			t.Errorf("codexFresh %v: advice for Codex tells about Claude:\n%s", fresh, got)
		}
	}
}

// Codex outside T3 has sessions, not threads, and the advice for it says so.
func TestAdviceForCodexOutsideT3SaysToStartANewSession(t *testing.T) {
	got := strings.Join(envWith(map[string]string{"CODEX_THREAD_ID": "019a"}).advice(false, true, false), "\n")
	if !strings.Contains(got, "start a new session") || strings.Contains(got, "open a new thread") {
		t.Errorf("advice:\n%s", got)
	}
}

// Claude Code in T3 still gets Claude's advice, though T3 is there too.
func TestAdviceForClaudeInT3IsNotCodexs(t *testing.T) {
	e := envWith(map[string]string{"CLAUDECODE": "1", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"})
	got := strings.Join(e.advice(true, false, false), "\n")
	if !strings.Contains(got, "press Stop in T3") || strings.Contains(got, "open a new thread in this project to talk") {
		t.Errorf("advice:\n%s", got)
	}
}
