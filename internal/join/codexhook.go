package join

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
	"github.com/a-Fig/murmur/internal/setup"
)

// Codex's hook (#225). Join puts Murmur's hook in hooks.json in each Codex home, for every project,
// and records it as trusted in that home's config.toml, so Codex runs it without asking. Tyler's
// decision (2026-11-08): T3 Code, where most Codex sessions run, never shows Codex's "Hooks need
// review" screen (pingdotgg/t3code reads no hook state), so a hook the user had to approve there
// would never run. Codex's own approval writes the same entry (its TUI's `config/batchWrite` of
// `hooks.state`), and join writes it the way Codex computes it (codex-rs/hooks discovery.rs,
// hook_hash, and codex-rs/config fingerprint.rs, version_for_toml, at rust-v0.166.0 and
// rust-v0.167.0). If a Codex
// release computes it differently, the hook stops running with no error: the daemon shows that as
// "hook not running" (hookOffLocked).

// codexHookEvents are the events the hook is installed on, the fewest that do its work: a prompt
// marks the session at work, a tool call too and brings the notices, and Stop ends the turn,
// keeping it going with what wasn't read (#202). No PreToolUse: crediting commits to a Codex
// session (#21) isn't done, and each event is one more hook in Codex's list.
var codexHookEvents = []struct {
	name, label, matcher string
}{
	{"UserPromptSubmit", "user_prompt_submit", ""},
	{"PostToolUse", "post_tool_use", "*"},
	{"Stop", "stop", ""},
}

// codexHookTimeout is the hook's timeout in seconds, which Codex's default (600) would make the
// most a stuck hook could hold up a turn. The hook bounds itself at a few seconds.
const codexHookTimeout = 30

// hookName is what Codex shows for the hook in its hooks list (`statusMessage`; it ignores `name`
// and `description`, seen on 0.166.0), and how join finds its own entries again.
var hookName = brand.Product

// agentHookCommand is the command line of the hook for agent ("codex", "grok") with Murmur's binary
// at exe. The agent runs it through a shell: on Windows Codex uses PowerShell (cmd where it finds
// none, codex-rs shell_detect.rs at rust-v0.167.0), where a quoted path followed by arguments is a
// parse error ("Unexpected token", so the hook exits 1: seen with Codex 0.166.0, 2026-11-08). So
// the path goes bare, with its own backslashes, which both shells run as a command (cmd would read
// a forward slash as a switch); in its short form if it has a character either shell would take
// as syntax; and with PowerShell's call operator if even that has one. Elsewhere it is
// single-quoted for sh.
func agentHookCommand(exe, agent string) string {
	if runtime.GOOS == "windows" {
		if bareSafe(exe) {
			return exe + " hook " + agent
		}
		if short := shortPath(exe); bareSafe(short) {
			return short + " hook " + agent
		}
		return "& " + psQuote(exe) + " hook " + agent
	}
	return "'" + strings.ReplaceAll(exe, "'", `'\''`) + "' hook " + agent
}

// psQuote quotes s as a PowerShell single-quoted string. PowerShell ends one at any of its five
// single quotes (' and four curly ones), and reads a doubled one as itself, so each is doubled.
func psQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		if strings.ContainsRune("'‘’‚‛", r) {
			b.WriteRune(r)
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// bareSafe says a Windows path means only itself, unquoted, to PowerShell and to cmd.
func bareSafe(p string) bool {
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(`\:._-~`, r):
		default:
			return false
		}
	}
	return p != ""
}

// codexHookHandler is the handler join writes for each event, its keys in the order they are
// written.
func codexHookHandler(command string) *jsonedit.Object {
	h := jsonedit.New()
	h.Set("type", "command")
	h.Set("command", command)
	h.Set("timeout", codexHookTimeout)
	h.Set("statusMessage", hookName)
	return h
}

// isMurmurGroup says a hooks.json matcher group is join's: one handler, which is Murmur's by its
// name (statusMessage) or its command (`mur hook codex`, or the Claude Code hook's form, which a
// user may have copied there by hand and which doesn't run under Codex on Windows).
func isMurmurGroup(g any) bool {
	o, _ := g.(*jsonedit.Object)
	list, _ := o.Get("hooks")
	hs, _ := list.([]any)
	if len(hs) != 1 {
		return false
	}
	h, _ := hs[0].(*jsonedit.Object)
	if s, _ := h.Get("statusMessage"); s == hookName {
		return true
	}
	c, _ := h.Get("command")
	cmd, _ := c.(string)
	return setup.IsHookCommand(cmd) || isAgentHookCommand(cmd)
}

// isAgentHookCommand says a command line is `<mur> hook codex`, `<mur> hook grok`,
// `<mur> hook muse` or `<mur> hook cursor`, however the path is written.
func isAgentHookCommand(cmd string) bool {
	for _, a := range []string{"codex", "grok", "muse", "cursor"} {
		if rest, ok := strings.CutSuffix(strings.TrimSpace(cmd), " hook "+a); ok {
			rest = strings.Trim(strings.TrimPrefix(strings.TrimSpace(rest), "& "), `'"`)
			return isMurBinary(rest)
		}
	}
	return false
}

// isMurBinary says a path names Murmur's executable.
func isMurBinary(p string) bool {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(p), ".exe") == brand.Command
}

// codexTrust is one hook join wrote and the trust entry that goes with it.
type codexTrust struct{ key, hash string }

// editCodexHooks returns hooks.json's text with Murmur's hook in it for command, and the trust
// entries of its handlers, keyed under keyPath (the file's path as Codex names it). Codex
// keys a hook's trust by its event and its group's place in the list, so join's group keeps its
// place when it is there, and goes last when it isn't: moving it would move the keys of the user's
// own hooks after it, and Codex would ask about them again. Everything else stays, its keys in
// their order (jsonedit). A file that isn't a JSON object with an object of hooks is an error:
// join won't replace what it can't read.
func editCodexHooks(raw []byte, keyPath, command string) (out []byte, trust []codexTrust, changed bool, err error) {
	doc, err := jsonedit.Parse(raw)
	if err != nil {
		return nil, nil, false, fmt.Errorf("isn't a JSON object (%v); fix it or move it aside, then run join again", err)
	}
	hooks := doc.Object("hooks")
	if hooks == nil {
		if _, taken := doc.Get("hooks"); taken {
			return nil, nil, false, errors.New(`has a "hooks" that isn't an object`)
		}
		hooks = jsonedit.New()
		doc.Set("hooks", hooks)
		changed = true
	}
	for _, ev := range codexHookEvents {
		var groups []any
		if g, ok := hooks.Get(ev.name); ok {
			if groups, ok = g.([]any); !ok {
				return nil, nil, false, fmt.Errorf("has %s hooks that aren't a list", ev.name)
			}
		}
		group := jsonedit.New()
		if ev.matcher != "" {
			group.Set("matcher", ev.matcher)
		}
		group.Set("hooks", []any{codexHookHandler(command)})
		at := -1
		for i, g := range groups {
			if isMurmurGroup(g) {
				at = i
				break
			}
		}
		if at < 0 {
			at = len(groups)
			groups = append(groups, group)
			changed = true
		} else if !sameJSONValue(groups[at], group) {
			groups[at] = group
			changed = true
		}
		hooks.Set(ev.name, groups)
		hash, err := codexHookHash(ev.label, ev.matcher, command)
		if err != nil {
			return nil, nil, false, err
		}
		trust = append(trust, codexTrust{key: keyPath + ":" + ev.label + ":" + strconv.Itoa(at) + ":0", hash: hash})
	}
	if !changed {
		return raw, trust, false, nil
	}
	out, err = jsonedit.Format(doc, raw)
	return out, trust, true, err
}

// sameJSONValue says two JSON values, as jsonedit holds them, mean the same.
func sameJSONValue(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && sameJSON(string(x), string(y))
}

// sameJSON says two JSON texts hold the same value.
func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

// codexHookHash is the hash Codex takes over a command hook's normalized identity, which its trust
// entry must hold: SHA-256 over compact JSON with every object's keys sorted, of the event's
// snake_case name, the group's matcher (only for events that have one), and the one handler as
// Codex normalizes it (type, command, timeout, async, statusMessage; commandWindows dropped).
// Codex's serde_json escapes no character Go's encoder does once HTML escaping is off, except
// U+2028 and U+2029, which a command line has no use for.
func codexHookHash(event, matcher, command string) (string, error) {
	return codexHandlerHash(event, matcher, command, codexHookTimeout, hookName)
}

// codexHandlerHash is codexHookHash for any command handler: status "" is a handler with no
// statusMessage, which Codex leaves out of the identity.
func codexHandlerHash(event, matcher, command string, timeout int, status string) (string, error) {
	handler := map[string]any{"type": "command", "command": command, "timeout": timeout, "async": false}
	if status != "" {
		handler["statusMessage"] = status
	}
	identity := map[string]any{"event_name": event, "hooks": []any{handler}}
	if matcher != "" {
		identity["matcher"] = matcher
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b) // a map's keys come out sorted
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(b.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// editCodexTrust returns config.toml's text with a trust entry for each of trust: a
// [hooks.state.'<key>'] table holding trusted_hash, the form Codex writes. An entry whose table is
// there gets its trusted_hash set in place; one written in a form join doesn't edit (dotted keys or
// an inline table) is an error, as a second definition would make the file invalid, and Codex then
// fails to load any of its config.
func editCodexTrust(text string, trust []codexTrust) (out string, changed bool, err error) {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	out = text
	for _, t := range trust {
		out, err = setTrustedHash(out, nl, t.key, t.hash)
		if err != nil {
			return "", false, err
		}
	}
	return out, out != text, nil
}

// setTrustedHash is editCodexTrust for one entry.
func setTrustedHash(text, nl, key, hash string) (string, error) {
	want := []string{"hooks", "state", key}
	line := "trusted_hash = " + tomlString(hash)
	lines := strings.Split(text, "\n")
	var table []string
	header, at := -1, -1
	inString := "" // the quotes that end the multi-line string a line is in
	for i, raw := range lines {
		l := strings.TrimRight(raw, "\r")
		was := inString
		if inString = tomlLineEnd(l, inString); was != "" {
			continue // a line that starts inside a string is neither a header nor a key
		}
		if path, ok := tableHeader(l); ok {
			table = path
			if header >= 0 && at < 0 {
				at = i // the table ended without a trusted_hash
			}
			if slicesEqual(path, want) {
				header = i
			}
			continue
		}
		path, _, ok := keyValue(l)
		if !ok {
			continue
		}
		full := append(append([]string(nil), table...), path...)
		switch {
		case slicesEqual(table, want) && len(path) == 1 && path[0] == "trusted_hash":
			if inString != "" {
				return "", errors.New("config.toml sets the trust of " + brand.Product + "'s hook as a multi-line string, which join doesn't edit")
			}
			lines[i] = line + raw[len(l):]
			return strings.Join(lines, "\n"), nil
		case slicesEqual(table, want):
			// Another key of the hook's own table, like enabled: kept as it is.
		case hasPrefix(full, want) || hasPrefix(want, full) && len(full) < len(want):
			// `state = {...}` under [hooks], or `'<key>'.trusted_hash = ...` under [hooks.state].
			return "", errors.New("config.toml sets the trust of " + brand.Product + "'s hook, or hooks.state, in a form join doesn't edit (a dotted key or an inline table)")
		}
	}
	if header >= 0 {
		if at < 0 {
			at = len(lines)
		}
		for at > header+1 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		cr := ""
		if nl == "\r\n" {
			cr = "\r"
		}
		add := line + cr
		if at == len(lines) {
			// After the file's last line, which has no newline: that line takes the CRLF it lacks,
			// and the new one goes without one, as it did. A \r of its own at the very end would be
			// a lone CR, which TOML forbids, and Codex would then refuse the file (#946, as #783).
			if !strings.HasSuffix(lines[at-1], "\r") {
				lines[at-1] += cr
			}
			add = line
		}
		lines = append(lines[:at], append([]string{add}, lines[at:]...)...)
		return strings.Join(lines, "\n"), nil
	}
	var b strings.Builder
	b.WriteString(text)
	final := text == "" || strings.HasSuffix(text, "\n") // a file without a final newline stays so
	if !final {
		b.WriteString(nl)
	}
	if strings.TrimSpace(text) != "" {
		b.WriteString(nl)
	}
	b.WriteString("[hooks.state." + tomlKey(key) + "]" + nl + line)
	if final {
		b.WriteString(nl)
	}
	return b.String(), nil
}

// tomlKey quotes a TOML key: a literal string where it can, as Codex writes it, else a basic one.
func tomlKey(k string) string {
	if !strings.ContainsAny(k, "'\n\r") {
		return "'" + k + "'"
	}
	return tomlString(k)
}

func slicesEqual(a, b []string) bool {
	return len(a) == len(b) && hasPrefix(a, b)
}

// hasPrefix says a starts with all of b.
func hasPrefix(a, b []string) bool {
	if len(a) < len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// installCodexHook puts Murmur's hook in Codex home h and trusts it.
func (e Env) installCodexHook(h agentHome) (did []string, err error) {
	// Read where writeAtomic writes: Windows follows a link with `..` in its target lexically, so
	// reading through the link could read another file than the one written.
	path := filepath.Join(h.Dir, "hooks.json")
	b, err := os.ReadFile(resolveLinks(path))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out, trust, changed, err := editCodexHooks(b, codexKeyPath(path, h.Named), agentHookCommand(e.Exe, "codex"))
	if err != nil {
		return nil, fmt.Errorf("%s %w", path, err)
	}
	if changed {
		if err := writeAtomic(path, out, 0o600); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	cfg := filepath.Join(h.Dir, "config.toml")
	cb, err := os.ReadFile(resolveLinks(cfg))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	text, trustChanged, err := editCodexTrust(string(cb), trust)
	if err == nil && trustChanged {
		err = checkTOMLEdit(string(cb), text)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg, err)
	}
	if trustChanged {
		if err := writeAtomic(cfg, []byte(text), 0o600); err != nil {
			return nil, fmt.Errorf("%s: %w", cfg, err)
		}
	}
	switch {
	case changed:
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project, and marked it trusted, as approving it in Codex would ("+path+")")
	case trustChanged:
		did = append(did, h.Label+": marked "+brand.Product+"'s hook trusted, as approving it in Codex would ("+cfg+")")
	}
	return did, nil
}

// codexKeyPath is hooks.json's path as Codex names it in its trust keys. A home CODEX_HOME names
// (named) Codex canonicalizes: links resolved and, on Windows, short names made long, as
// filepath.EvalSymlinks does (codex-rs/utils/home-dir at rust-v0.167.0). The default ~/.codex it
// takes as it is. A key in another spelling would never be read, and Codex would ask about the hook.
func codexKeyPath(path string, named bool) string {
	if named {
		if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
			return filepath.Join(dir, filepath.Base(path))
		}
	}
	return path
}

// hasMurmurCodexHook says the hooks.json at path has a group of join's.
func hasMurmurCodexHook(path string) bool {
	doc, _, err := jsonedit.ReadFile(path)
	hooks := doc.Object("hooks")
	if err != nil || hooks == nil {
		return false
	}
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		groups, _ := v.([]any)
		for _, g := range groups {
			if isMurmurGroup(g) {
				return true
			}
		}
	}
	return false
}
