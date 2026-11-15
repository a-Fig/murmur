package join

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/setup"
)

// hookEvents reads which events a settings file runs a mur hook on, with the matcher of each.
func hookEvents(t *testing.T, path string) map[string]string {
	t.Helper()
	var cfg struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(read(t, path)), &cfg); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for event, groups := range cfg.Hooks {
		for _, g := range groups {
			for _, h := range g.Hooks {
				if !setup.IsHookCommand(h.Command) {
					continue
				}
				if _, dup := out[event]; dup {
					t.Errorf("%s has the hook twice", event)
				}
				out[event] = g.Matcher
			}
		}
	}
	return out
}

// wantHookEvents is what the hook must be installed on, written out so that a change to
// setup.HookEvents has to change this too. PreToolUse and PostToolUseFailure are how the daemon
// finds the commits a tool call made (#21); a join that leaves them out loses those commits.
func wantHookEvents() map[string]string {
	return map[string]string{"PreToolUse": "*", "PostToolUse": "*", "PostToolUseFailure": "*", "UserPromptSubmit": "", "Stop": ""}
}

// oldHooks puts the hook in every working tree of the clone, as a join before #422 did.
func oldHooks(t *testing.T, p Place, exe string) {
	t.Helper()
	for _, top := range worktrees(context.Background(), p) {
		if _, err := installHook(filepath.Join(top, filepath.FromSlash(hookFile)), setup.HookCommand(exe)); err != nil {
			t.Fatal(err)
		}
	}
}

// A join over settings that have the hook on some of its events only adds the events it lacks, and
// doesn't double the ones it has or touch the user's own hooks.
func TestJoinCompletesAHookInstall(t *testing.T) {
	home := t.TempDir()
	exe := filepath.Join(t.TempDir(), "bin", "mur")
	command, err := json.Marshal(setup.HookCommand(exe))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "settings.json")
	write(t, path, `{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "other-tool"}]},
      {"matcher": "*", "hooks": [{"type": "command", "command": `+string(command)+`}]}
    ],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": `+string(command)+`}]}]
  }
}
`)
	changed, err := installClaudeHook(home, exe)
	if err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	if events := hookEvents(t, path); !reflect.DeepEqual(events, wantHookEvents()) {
		t.Errorf("the hook is on %v, want %v", events, wantHookEvents())
	}
	if got := read(t, path); strings.Count(got, "other-tool") != 1 {
		t.Errorf("the user's own hook was changed:\n%s", got)
	}
	if again, err := installClaudeHook(home, exe); err != nil || again {
		t.Errorf("a second install changed the file: %v (err %v)", again, err)
	}
}
