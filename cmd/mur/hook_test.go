package main

import "testing"

// `mur hook` runs for Claude Code with no arguments, for Codex and Grok named as join writes them
// (#225), and for Pi with --agent pi as its extension runs it, in either spelling. Anything else
// does nothing: run as Claude Code, another agent's hook would read CLAUDE_PID and send its own
// session's ID as a Claude Code one.
func TestHookAgent(t *testing.T) {
	for _, c := range []struct {
		args  []string
		agent string
		ok    bool
	}{
		{nil, "", true},
		{[]string{"--agent", "pi"}, "pi", true},
		{[]string{"--agent=pi"}, "pi", true},
		{[]string{"-agent", "pi"}, "pi", true},
		{[]string{"codex"}, "codex", true},
		{[]string{"grok"}, "grok", true},
		{[]string{"pi"}, "pi", true},
		{[]string{"--agent", "codex"}, "codex", true},
		{[]string{"opencode"}, "opencode", true},
		{[]string{"--agent", "opencode"}, "opencode", true},
		{[]string{"muse"}, "", false},
		{[]string{"--agent", "pi", "codex"}, "", false},
		{[]string{"codex", "grok"}, "", false},
		{[]string{"--agent"}, "", false},
		{[]string{"--agent", "pi", "extra"}, "", false},
		{[]string{"--bogus"}, "", false},
	} {
		if agent, ok := hookAgent(c.args); agent != c.agent || ok != c.ok {
			t.Errorf("hookAgent(%q) = %q, %v; want %q, %v", c.args, agent, ok, c.agent, c.ok)
		}
	}
}
