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
		got := strings.Join(e.advice(false, fresh, false, false), "\n")
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
	got := strings.Join(envWith(map[string]string{"CODEX_THREAD_ID": "019a"}).advice(false, true, false, false), "\n")
	if !strings.Contains(got, "start a new session") || strings.Contains(got, "open a new thread") {
		t.Errorf("advice:\n%s", got)
	}
}

// Claude Code in T3 still gets Claude's advice, though T3 is there too.
func TestAdviceForClaudeInT3IsNotCodexs(t *testing.T) {
	e := envWith(map[string]string{"CLAUDECODE": "1", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"})
	got := strings.Join(e.advice(true, false, false, false), "\n")
	if !strings.Contains(got, "press Stop in T3") || strings.Contains(got, "open a new thread in this project to talk") {
		t.Errorf("advice:\n%s", got)
	}
}

// T3's headless server names no command line, only the thread (#790): a Claude Code thread there is
// told to press Stop in T3, as in T3's app, not to exit and run `claude --continue`.
func TestAdviceForClaudeInHeadlessT3SaysStopInT3(t *testing.T) {
	e := envWith(map[string]string{"CLAUDECODE": "1", "T3CODE_THREAD_ID": "thread-1", "T3CODE_HOME": "/home/u/.t3"})
	got := strings.Join(e.advice(true, false, false, false), "\n")
	if !strings.Contains(got, "press Stop in T3") || strings.Contains(got, "claude --continue") {
		t.Errorf("advice:\n%s", got)
	}
}

// A Pi session that ran join is told about Pi, not Claude Code or Codex (#864): /reload in a
// terminal, and a new thread in T3, whose Pi has no /reload (Pi 0.99.0, 2026-11-17). With no
// promise that the tools come by themselves: a session that started before any join wrote the
// extension, even another session's join, never gets them without one (#870's review).
func TestAdviceForPi(t *testing.T) {
	cases := []struct {
		name      string
		vars      map[string]string
		want, not string
	}{
		{"in a terminal", map[string]string{"PI_CODING_AGENT": "true"}, "type /reload in Pi to load them", "new thread"},
		{"in T3", map[string]string{"PI_CODING_AGENT": "true", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"}, "open a new thread in this project", "/reload"},
	}
	for _, c := range cases {
		got := strings.Join(envWith(c.vars).advice(false, false, false, false), "\n")
		if !strings.Contains(got, c.want) || strings.Contains(got, c.not) || strings.Contains(got, "Claude Code") || strings.Contains(got, "Codex") ||
			strings.Contains(got, "seconds") || strings.Contains(got, "minute") {
			t.Errorf("%s: advice:\n%s", c.name, got)
		}
	}
	// A Claude Code or Codex that a Pi started inherits PI_CODING_AGENT, and is the agent running
	// join: the innermost agent's advice wins. A Pi that Codex started has the same variables, and
	// gets Codex's advice too: nothing tells the two apart (looksLikePi).
	for _, c := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"Claude Code under Pi", map[string]string{"PI_CODING_AGENT": "true", "CLAUDECODE": "1"}, "this conversation"},
		{"Codex under Pi", map[string]string{"PI_CODING_AGENT": "true", "CODEX_THREAD_ID": "019a"}, "Codex loads MCP servers only when a session starts"},
		{"Codex under Pi, in T3", map[string]string{"PI_CODING_AGENT": "true", "CODEX_THREAD_ID": "019a", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"}, "open a new thread in this project to talk"},
	} {
		e := envWith(c.vars)
		got := strings.Join(e.advice(false, false, false, false), "\n")
		if e.looksLikePi() || strings.Contains(got, "Pi") || !strings.Contains(got, c.want) {
			t.Errorf("%s: advice:\n%s", c.name, got)
		}
	}
}

func TestAdviceForAntigravity(t *testing.T) {
	for _, c := range []struct {
		name  string
		vars  map[string]string
		fresh bool
		want  string
	}{
		{"first join in T3", map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_THREAD_ID": "thread-1"}, true, "Open a new thread in this project"},
		{"first join in a terminal", map[string]string{"ANTIGRAVITY_CONVERSATION_ID": "conv-1"}, true, "Restart `agy` in this repo"},
		{"already registered in T3", map[string]string{"ANTIGRAVITY_AGENT": "1", "T3CODE_CLI_PATH": "/opt/t3/bin/t3"}, false, "in this session within a few seconds"},
		{"already registered in a terminal", map[string]string{"ANTIGRAVITY_CONVERSATION_ID": "conv-1"}, false, "in this session within a few seconds"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := envWith(c.vars)
			got := e.advice(false, false, c.fresh, false)[0]
			if !e.looksLikeAntigravity() || !strings.Contains(got, "Antigravity") || !strings.Contains(got, c.want) {
				t.Errorf("advice: %q", got)
			}
		})
	}
}

func TestAntigravityYieldsToNestedAgents(t *testing.T) {
	for _, c := range []struct{ key, value, want string }{
		{"CLAUDECODE", "1", "show up in this conversation"},
		{"CODEX_THREAD_ID", "codex-1", "Codex"},
		{"PI_CODING_AGENT", "true", "type /reload in Pi"},
	} {
		e := envWith(map[string]string{"ANTIGRAVITY_AGENT": "1", "ANTIGRAVITY_CONVERSATION_ID": "conv-1", c.key: c.value})
		got := e.advice(false, false, true, false)[0]
		if e.looksLikeAntigravity() || strings.Contains(got, "Antigravity") || !strings.Contains(got, c.want) {
			t.Errorf("%s: advice %q", c.key, got)
		}
	}
	if envWith(map[string]string{"ANTIGRAVITY_AGENT": "0"}).looksLikeAntigravity() {
		t.Error("ANTIGRAVITY_AGENT=0 identified as Antigravity")
	}
}

// Join's own T3 command line: T3CODE_CLI_PATH where T3's app set it; none outside T3, where no
// t3 on PATH or above is looked for.
func TestServingT3(t *testing.T) {
	vars := map[string]string{"T3CODE_CLI_PATH": "/opt/t3/bin/t3", "T3CODE_THREAD_ID": "thread-1"}
	if got := servingT3(func(k string) string { return vars[k] }); got != "/opt/t3/bin/t3" {
		t.Errorf("in T3's app: %q", got)
	}
	if got := servingT3(func(string) string { return "" }); got != "" {
		t.Errorf("outside T3: %q", got)
	}
}
