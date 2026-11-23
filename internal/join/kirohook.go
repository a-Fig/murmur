package join

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
)

// Kiro's hook (#910). Kiro CLI 3.4.0 (V3 KAS @kiro/agent, read 2026-11-22) loads every *.json file
// in <userConfigDir>/hooks/ (~/.kiro/hooks/) matching the v1 standalone hook schema:
// {"version": "v1", "hooks": [{"name", "trigger", "action": {"type": "command", "command"}, "timeout"}]}.
// It watches that directory for changes and executes command actions with
// child_process.spawn(command, {shell: true}) (cmd.exe on Windows, sh elsewhere), passing JSON on
// stdin with session_id, hook_event_name, cwd (and prompt on UserPromptSubmit).
//
// Two triggers:
//   - UserPromptSubmit: exit 0 forwards raw stdout into the turn context, marks the session active,
//     and marks any message handed over as the turn read (#611).
//   - Stop: exit 0 with {"decision":"block","reason":"..."} continues the turn with unread messages
//     whole (#202); empty stdout lets the turn end and marks the session idle.
//
// PostToolUse is intentionally omitted: in Kiro 3.4.0 resolveHookOutput discards both stdout and
// stderr for PostToolUse command hooks, so fetching notices there could mark whole messages read
// and lose them.

// kiroHookFile is join's standalone hook file in Kiro's hooks/ directory.
var kiroHookFile = brand.MCPServer + ".json"

type kiroHookDoc struct {
	Version string          `json:"version"`
	Hooks   []kiroHookEntry `json:"hooks"`
}

type kiroHookEntry struct {
	Name    string         `json:"name"`
	Trigger string         `json:"trigger"`
	Action  kiroHookAction `json:"action"`
	Timeout int            `json:"timeout,omitempty"`
}

type kiroHookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// kiroHookCommand formats the command line Kiro runs via cmd.exe on Windows and sh elsewhere.
func kiroHookCommand(exe string) string { return cmdHookCommand(exe, " hook kiro", "", "") }

// kiroHooks is the JSON content of join's ~/.kiro/hooks/murmur.json running command.
func kiroHooks(command string) ([]byte, error) {
	doc := kiroHookDoc{
		Version: "v1",
		Hooks: []kiroHookEntry{
			{
				Name:    brand.MCPServer + "-prompt",
				Trigger: "UserPromptSubmit",
				Action:  kiroHookAction{Type: "command", Command: command},
				Timeout: codexHookTimeout,
			},
			{
				Name:    brand.MCPServer + "-stop",
				Trigger: "Stop",
				Action:  kiroHookAction{Type: "command", Command: command},
				Timeout: codexHookTimeout,
			},
		},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// isKiroHookFile says b is a hooks/murmur.json file written by join (so join/uninstall never
// overwrite or delete a user's unrelated file of the same name).
func isKiroHookFile(b []byte) bool {
	var doc kiroHookDoc
	if json.Unmarshal(b, &doc) != nil || doc.Version != "v1" || len(doc.Hooks) == 0 {
		return false
	}
	for _, h := range doc.Hooks {
		if !strings.HasPrefix(h.Name, brand.MCPServer+"-") || h.Action.Type != "command" ||
			!strings.HasSuffix(strings.TrimSpace(h.Action.Command), " hook kiro") {
			return false
		}
	}
	return true
}

// installKiroHook writes join's hook file in Kiro home h if missing or outdated.
func (e Env) installKiroHook(h agentHome) (did []string, err error) {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	want, err := kiroHooks(kiroHookCommand(e.Exe))
	if err != nil {
		return nil, err
	}
	if have, rerr := os.ReadFile(path); rerr == nil {
		if bytes.Equal(have, want) {
			return nil, nil
		}
		if !isKiroHookFile(have) {
			return nil, fmt.Errorf("%s already exists and doesn't look like %s's hook, so it was left alone", path, brand.Product)
		}
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, rerr)
	}
	if err := writeAtomic(path, want, 0o644); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []string{h.Label + ": added " + brand.Product + "'s hook, in every project (" + path + ")"}, nil
}

// removeKiroHook removes join's hook file from Kiro home h, leaving a foreign file alone with a note.
func removeKiroHook(h agentHome) (did, notes []string, err error) {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	have, rerr := os.ReadFile(path)
	if errors.Is(rerr, os.ErrNotExist) {
		return nil, nil, nil
	}
	if rerr != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, rerr)
	}
	if !isKiroHookFile(have) {
		return nil, []string{h.Label + ": left " + path + " alone: it doesn't look like " + brand.Product + "'s hook"}, nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return []string{h.Label + ": removed " + brand.Product + "'s hook (" + path + ")"}, nil, nil
}

// kiroHookRemoval says how to remove join's hook file by hand, "" if none is present.
func kiroHookRemoval(h agentHome) string {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	if have, err := os.ReadFile(path); err == nil && isKiroHookFile(have) {
		return "delete " + path
	}
	return ""
}
