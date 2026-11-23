package main

import "testing"

// `mur hook` runs for Claude Code with no arguments, for Codex and Grok named as join writes them
// (#225), Antigravity with its event (#650), and for Pi with --agent pi as its extension runs it, in either spelling. Anything else
// does nothing: run as Claude Code, another agent's hook would read CLAUDE_PID and send its own
// session's ID as a Claude Code one.
func TestHookAgent(t *testing.T) {
	for _, c := range []struct {
		args         []string
		agent, event string
		ok           bool
	}{
		{nil, "", "", true},
		{[]string{"--agent", "pi"}, "pi", "", true},
		{[]string{"--agent=pi"}, "pi", "", true},
		{[]string{"-agent", "pi"}, "pi", "", true},
		{[]string{"codex"}, "codex", "", true},
		{[]string{"grok"}, "grok", "", true},
		{[]string{"pi"}, "pi", "", true},
		{[]string{"--agent", "codex"}, "codex", "", true},
		{[]string{"opencode"}, "opencode", "", true},
		{[]string{"--agent", "opencode"}, "opencode", "", true},
		{[]string{"muse"}, "muse", "", true}, // Muse Code has a hook since #461
		{[]string{"kiro"}, "kiro", "", true}, // Kiro has a hook since #910
		{[]string{"--agent", "kiro"}, "kiro", "", true},
		{[]string{"--agent", "pi", "codex"}, "", "", false},
		{[]string{"codex", "grok"}, "", "", false},
		{[]string{"--agent"}, "", "", false},
		{[]string{"--agent", "pi", "extra"}, "", "", false},
		{[]string{"--bogus"}, "", "", false},
		// Antigravity's payload doesn't name its event, so join's command line does (#650).
		{[]string{"--event", "Stop", "antigravity"}, "antigravity", "Stop", true},
		{[]string{"--event", "PreInvocation", "antigravity"}, "antigravity", "PreInvocation", true},
	} {
		if agent, event, ok := hookAgent(c.args); agent != c.agent || event != c.event || ok != c.ok {
			t.Errorf("hookAgent(%q) = %q, %q, %v; want %q, %q, %v", c.args, agent, event, ok, c.agent, c.event, c.ok)
		}
	}
}
