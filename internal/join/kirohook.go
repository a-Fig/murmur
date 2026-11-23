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

// isKiroHookFile says b is byte-for-byte (ignoring UTF-8 BOM and CRLF) a hooks/murmur.json file
// produced by kiroHooks for a valid Murmur `hook kiro` command. Comparing against kiroHooks's
// canonical rendering rejects any user edit — including case-folded or duplicate JSON keys,
// changed timeouts, extra fields, or customized commands — so join and uninstall never overwrite
// or delete a customized file.
func isKiroHookFile(b []byte) bool {
	norm := bytes.ReplaceAll(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), []byte("\r\n"), []byte("\n"))
	var doc kiroHookDoc
	if json.Unmarshal(norm, &doc) != nil || len(doc.Hooks) != 2 {
		return false
	}
	cmd := doc.Hooks[0].Action.Command
	if !isKiroHookCommand(cmd) {
		return false
	}
	want, err := kiroHooks(cmd)
	return err == nil && bytes.Equal(norm, want)
}

// isKiroHookCommand says cmd has one of the exact `<mur> hook kiro` shapes kiroHookCommand writes
// (sh single-quoted, Windows bare-safe, or cmdQuotePath-quoted), with Murmur's binary as the command.
func isKiroHookCommand(cmd string) bool {
	rest, ok := strings.CutSuffix(cmd, " hook kiro")
	if !ok || rest == "" {
		return false
	}
	if strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") && len(rest) >= 2 {
		inner := rest[1 : len(rest)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			return false
		}
		return isMurBinary(strings.ReplaceAll(inner, `'\''`, "'"))
	}
	if bareSafe(rest) {
		return isMurBinary(rest)
	}
	parts := strings.Split(rest, "^%")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(part, `"`, "")
	}
	exe := strings.Join(parts, "%")
	return cmdQuotePath(exe) == rest && isMurBinary(exe)
}

// installKiroHook writes join's hook file in Kiro home h if missing or outdated. A non-regular
// entry (such as a live or dangling symlink) or a customized file is left alone with an error,
// matching removeOwned's ownership rules.
func (e Env) installKiroHook(h agentHome) (did []string, err error) {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	want, err := kiroHooks(kiroHookCommand(e.Exe))
	if err != nil {
		return nil, err
	}
	if fi, lerr := os.Lstat(path); lerr == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s already exists and isn't a regular file (a link, say), so it was left alone", path)
		}
		have, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, fmt.Errorf("%s: %w", path, rerr)
		}
		if bytes.Equal(have, want) {
			return nil, nil
		}
		if !isKiroHookFile(have) {
			return nil, fmt.Errorf("%s already exists and doesn't look like %s's hook, so it was left alone", path, brand.Product)
		}
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, lerr)
	}
	if err := writeAtomic(path, want, 0o644); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []string{h.Label + ": added " + brand.Product + "'s hook, in every project (" + path + ")"}, nil
}

// removeKiroHook removes join's hook file from Kiro home h via removeOwned, leaving a symlink or
// foreign file alone with a note.
func removeKiroHook(h agentHome) (did, notes []string, err error) {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	removed, note, err := removeOwned(path, isKiroHookFile)
	if err != nil {
		return nil, nil, err
	}
	if note != "" {
		return nil, []string{h.Label + ": " + note}, nil
	}
	if removed {
		return []string{h.Label + ": removed " + brand.Product + "'s hook (" + path + ")"}, nil, nil
	}
	return nil, nil, nil
}

// kiroHookRemoval says how to remove join's hook file by hand, "" if none is present.
func kiroHookRemoval(h agentHome) string {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	if have, err := os.ReadFile(path); err == nil && isKiroHookFile(have) {
		return "delete " + path
	}
	return ""
}
