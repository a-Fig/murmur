package join

import (
	"bytes"
	"crypto/sha256"
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
// {"version": "v1", "hooks": [{"name", "description", "trigger", "action": {"type": "command", "command"}, "timeout"}]}.
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

// kiroHookDescription marks the hook entries as written by join (Kiro's v1 standalone hook schema
// carries description directly on each hook entry) and binds the marker to a digest of command, so
// hooks/murmur.json is self-identifying even for a renamed Murmur binary while still detecting a
// user who edits the command in place.
func kiroHookDescription(command string) string {
	sum := sha256.Sum256([]byte(command))
	return fmt.Sprintf("Managed by %s (%s join, %x)", brand.Product, brand.Command, sum[:6])
}

type kiroHookDoc struct {
	Version string          `json:"version"`
	Hooks   []kiroHookEntry `json:"hooks"`
}

type kiroHookEntry struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Trigger     string         `json:"trigger"`
	Action      kiroHookAction `json:"action"`
	Timeout     int            `json:"timeout,omitempty"`
}

type kiroHookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// kiroHookCommand formats the command line Kiro runs via cmd.exe on Windows and sh elsewhere.
// It preserves exe verbatim (without 8.3 shortPath substitution).
func kiroHookCommand(exe string) string { return cmdHookCommand(exe, " hook kiro", "", "", false) }

// kiroHooks is the JSON content of join's ~/.kiro/hooks/murmur.json running command.
func kiroHooks(command string) ([]byte, error) {
	desc := kiroHookDescription(command)
	doc := kiroHookDoc{
		Version: "v1",
		Hooks: []kiroHookEntry{
			{
				Name:        brand.MCPServer + "-prompt",
				Description: desc,
				Trigger:     "UserPromptSubmit",
				Action:      kiroHookAction{Type: "command", Command: command},
				Timeout:     codexHookTimeout,
			},
			{
				Name:        brand.MCPServer + "-stop",
				Description: desc,
				Trigger:     "Stop",
				Action:      kiroHookAction{Type: "command", Command: command},
				Timeout:     codexHookTimeout,
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
// produced by kiroHooks for a canonical `<exe> hook kiro` command. Comparing against kiroHooks's
// canonical rendering (which embeds kiroHookDescription on both hooks) identifies files join wrote
// — even from a renamed binary such as mur-dev — while rejecting any user file or customization
// (changed timeout, extra fields, case-folded/duplicate JSON keys, or wrapper commands).
func isKiroHookFile(b []byte) bool {
	norm := bytes.ReplaceAll(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), []byte("\r\n"), []byte("\n"))
	var doc kiroHookDoc
	if json.Unmarshal(norm, &doc) != nil || len(doc.Hooks) != 2 {
		return false
	}
	cmd := doc.Hooks[0].Action.Command
	if _, ok := parseKiroHookCommand(cmd); !ok {
		return false
	}
	want, err := kiroHooks(cmd)
	return err == nil && bytes.Equal(norm, want)
}

// isKiroHookCommand says cmd has one of the exact `<mur> hook kiro` shapes kiroHookCommand writes
// (sh single-quoted, Windows bare-safe, or cmdQuotePath-quoted), with Murmur's binary as the command.
func isKiroHookCommand(cmd string) bool {
	exe, ok := parseKiroHookCommand(cmd)
	return ok && isMurBinary(exe)
}

// parseKiroHookCommand extracts the executable path from a canonical `<exe> hook kiro` command
// written by kiroHookCommand (sh single-quoted, Windows bare-safe, or cmdQuotePath-quoted).
func parseKiroHookCommand(cmd string) (string, bool) {
	rest, ok := strings.CutSuffix(cmd, " hook kiro")
	if !ok || rest == "" {
		return "", false
	}
	if strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") && len(rest) >= 2 {
		inner := rest[1 : len(rest)-1]
		if strings.Contains(strings.ReplaceAll(inner, `'\''`, ""), "'") {
			return "", false
		}
		return strings.ReplaceAll(inner, `'\''`, "'"), true
	}
	if bareSafe(rest) {
		return rest, true
	}
	parts := strings.Split(rest, "^%")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(part, `"`, "")
	}
	exe := strings.Join(parts, "%")
	if cmdQuotePath(exe) != rest {
		return "", false
	}
	return exe, true
}

// installKiroHook writes join's hook file in Kiro home h if missing or outdated. A non-regular
// entry (such as a live or dangling symlink) or a customized file is left alone with an error,
// matching removeOwned's ownership rules. New files are created exclusively (O_CREATE|O_EXCL) so
// they never replace a concurrently created file or symlink; existing owned files are updated
// atomically via writeOwnedFile without resolving a leaf symlink.
func (e Env) installKiroHook(h agentHome) (did []string, err error) {
	path := filepath.Join(h.Dir, "hooks", kiroHookFile)
	want, err := kiroHooks(kiroHookCommand(e.Exe))
	if err != nil {
		return nil, err
	}
	existed := false
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
		existed = true
	} else if !errors.Is(lerr, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", path, lerr)
	}
	if !existed {
		if err := createOwnedFile(path, want, 0o644); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if err := writeOwnedFile(path, want, 0o644, isKiroHookFile); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []string{h.Label + ": added " + brand.Product + "'s hook, in every project (" + path + ")"}, nil
}

// createOwnedFile writes the complete file to a sibling temp file and publishes it at path via
// publishNewFile (MoveFileW on Windows, os.Link on Unix) so an interrupted write never leaves a
// partial hooks/murmur.json behind and publishing fails atomically if path already exists.
func createOwnedFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".murmurtmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = publishNewFile(tmp, path)
	}
	return err
}

// writeOwnedFile atomically replaces an existing owned regular file at path without following or
// replacing a symlink at path itself (unlike writeAtomic, which writes through user config
// symlinks). It re-verifies ownership immediately before renaming.
func writeOwnedFile(path string, data []byte, perm os.FileMode, ours func([]byte) bool) error {
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s isn't a regular file (a link, say), so it was left alone", path)
		}
		perm = fi.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".murmurtmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		if fi, lerr := os.Lstat(path); lerr != nil {
			err = lerr
		} else if !fi.Mode().IsRegular() {
			err = fmt.Errorf("%s isn't a regular file (a link, say), so it was left alone", path)
		} else if cur, rerr := os.ReadFile(path); rerr != nil {
			err = rerr
		} else if !ours(cur) {
			err = fmt.Errorf("%s changed and doesn't look like %s's hook, so it was left alone", path, brand.Product)
		} else {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
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
