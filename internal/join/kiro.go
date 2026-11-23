package join

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
	"github.com/a-Fig/murmur/internal/skills"
)

// Kiro (#906), AWS's coding agent CLI (`kiro-cli`, V3 KAS engine `@kiro/agent`). Read in Kiro CLI
// 3.4.0 (~/AppData/Local/Kiro-Cli/kiro-cli.exe and its bundled @kiro/agent/dist/server/acp-server.js)
// and in T3 Code's packages/provider-kiro (2026-11-22).
//
//   - User config directory: $KIRO_HOME when it is an absolute path, not a filesystem root, and
//     does not equal or contain the user's home directory (V3's resolveUserConfigDir); else ~/.kiro.
//     T3 Code has no per-instance homePath setting for Kiro and shares the user's directory (unless
//     an instance sets KIRO_HOME in its environment).
//   - MCP servers are mcpServers.<name> in <dir>/settings/mcp.json (JSONC via comment-json), with
//     command and args. Kiro's MCPConfigManager watches that file (hot-reloading added servers
//     between turns) and its MCPConnection handles notifications/tools/list_changed. Stdio MCP
//     servers inherit process.env (minus bash function exports), so inside T3 Code
//     `mur mcp --agent kiro` inherits T3CODE_THREAD_ID, T3CODE_CLI_PATH and T3CODE_HOME directly.
//   - Tool approval: V3 accepts `autoApprove` in mcp.json schema-wise but treats it as an unused
//     field. Instead, V3 enforces policy rules from <dir>/settings/permissions.yaml if present, else
//     <dir>/settings/permissions.json (plain JSON, requiring a top-level "rules" array). Each MCP
//     tool call checks capability "mcp" against resource path "<serverName>/<toolName>", so a rule
//     with capability "mcp", match ["murmur/*"], effect "allow" allows all of Murmur's tools without
//     prompting in Supervised or standalone CLI sessions. (In T3 Full Access mode, autopilot is on.)
//     Kiro's policy schema has no predicate to bind a rule to a server's executable path, so in a
//     trusted workspace a project .kiro/settings/mcp.json server also named "murmur" inherits this
//     allow rule (the same model as Claude Code, Grok and Antigravity). Because Kiro ignores
//     permissions.json whenever permissions.yaml exists, join and uninstall both leave
//     permissions.json alone when permissions.yaml is present.

var kiroArgs = []string{"mcp", "--agent", "kiro"}

// kiroAllowPattern matches every tool on Murmur's MCP server against Kiro's "<server>/<tool>"
// policy resource path.
var kiroAllowPattern = brand.MCPServer + "/*"

// kiroMurmurTools are every MCP tool Murmur's server registers (internal/mcpserver). Checking
// every tool ensures a per-tool deny/ask rule like "murmur/send" isn't missed when deciding
// whether Kiro's permissions.json overrides our allow rule.
var kiroMurmurTools = []string{"who", "lookup", "status", "tags", "send", "inbox", "mute"}

// kiroPathsInTemp reports whether Kiro's settings, hooks, and skills directories and the files
// join/uninstall edit under dir resolve inside the temporary folder (protecting scratch homes
// against nested symlinks or junctions pointing at the user's real ~/.kiro, #995).
func kiroPathsInTemp(dir string) bool {
	settings := filepath.Join(dir, "settings")
	hooks := filepath.Join(dir, "hooks")
	skillsRoot := filepath.Join(dir, "skills")
	paths := []string{
		settings, filepath.Join(settings, "mcp.json"), filepath.Join(settings, "permissions.json"),
		hooks, filepath.Join(hooks, kiroHookFile),
		skillsRoot,
	}
	for _, s := range skills.All() {
		paths = append(paths, filepath.Join(skillsRoot, s.Name), filepath.Join(skillsRoot, s.Name, "SKILL.md"))
	}
	for _, p := range paths {
		if !inTemp(p, false) {
			return false
		}
	}
	return true
}

// kiroHomeDir is Kiro's user configuration directory, following V3's resolveUserConfigDir:
// $KIRO_HOME if it is an absolute, non-root path that doesn't equal or contain home; else ~/.kiro.
func kiroHomeDir(getenv func(string) string, home string) string {
	if dir := strings.TrimSpace(getenv("KIRO_HOME")); dir != "" && filepath.IsAbs(dir) {
		clean := filepath.Clean(dir)
		res := resolved(clean)
		if res == "" {
			res = clean
		}
		if filepath.Dir(res) != res {
			containsHome := false
			if home != "" {
				resHome := resolved(filepath.Clean(home))
				if resHome == "" {
					resHome = filepath.Clean(home)
				}
				containsHome = within(resHome, res)
			}
			if !containsHome {
				return clean
			}
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".kiro")
}

// hasKiro says Kiro is on this machine: its directory, T3's binaryPath, `kiro-cli` on PATH, or
// Kiro's default Windows per-user install location under the user's home.
func (e Env) hasKiro(h agentHome) bool {
	if h.Dir == "" {
		return false
	}
	if isDir(h.Dir) || h.Bin != "" {
		return true
	}
	if _, err := e.look("kiro-cli"); err == nil {
		return true
	}
	if e.Home != "" {
		if fi, err := os.Stat(filepath.Join(e.Home, "AppData", "Local", "Kiro-Cli", "kiro-cli.exe")); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// registerKiroAt puts Murmur's server in settings/mcp.json and, where no permissions.yaml shadows
// it, Murmur's tool allow rule in settings/permissions.json. A tool-approval issue is returned as
// an approvalNote so the server registration still succeeds.
func (e Env) registerKiroAt(h agentHome) (did []string, fresh bool, err error) {
	if !e.hasKiro(h) {
		return nil, false, nil
	}
	prevExe := kiroRegisteredExe(h)
	hookDid, undoHook, hookErr := e.installKiroHook(h, prevExe)
	did = append(did, hookDid...)
	if hookErr != nil && prevExe != "" && !isMurBinary(prevExe) {
		// Keep the previously registered renamed binary in mcp.json so a retry or manual removal
		// still recognizes hooks/murmur.json as owned by Murmur.
		return did, false, hookErr
	}
	mcpPath := filepath.Join(h.Dir, "settings", "mcp.json")
	entry := jsonedit.New()
	entry.Set("command", e.Exe)
	entry.Set("args", toAny(kiroArgs))
	changed, added, err := setJSONIn(mcpPath, []string{"mcpServers", brand.MCPServer}, entry)
	if err != nil {
		if undoHook != nil && !isMurBinary(e.Exe) {
			// Don't leave a newly written renamed-binary hook stranded without a matching mcp.json
			// entry, which would prevent a later standard `mur` join or uninstall from recognizing it.
			if uerr := undoHook(); uerr != nil {
				return did, false, fmt.Errorf("%s: %w, so the server wasn't added (and couldn't roll back %s: %v)", mcpPath, err, filepath.Join(h.Dir, "hooks", kiroHookFile), uerr)
			}
			did = nil
		}
		return did, false, fmt.Errorf("%s: %w, so the server wasn't added", mcpPath, err)
	}
	fresh = added
	if changed {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+mcpPath+")")
	}
	allowed, permPath, permErr := allowKiroTools(h.Dir)
	if allowed {
		if permErr != nil {
			did = append(did, h.Label+": allowed "+brand.Product+"'s other tools ("+permPath+")")
		} else {
			did = append(did, h.Label+": allowed "+brand.Product+"'s tools, so they don't ask each time ("+permPath+")")
		}
	}
	if hookErr != nil {
		// Return hook errors directly rather than joining with an approvalNote, which join.go
		// would downgrade from a Problem to a Note via errors.As.
		return did, fresh, hookErr
	}
	if permErr != nil {
		var note approvalNote
		if errors.As(permErr, &note) {
			return did, fresh, note
		}
		return did, fresh, approvalNote(fmt.Sprintf("couldn't add %q to %s (%v), so %s's tools will ask before each call.", kiroAllowPattern, permPath, permErr, brand.Product))
	}
	return did, fresh, nil
}

// allowKiroTools ensures Murmur's MCP tools are allowed in Kiro's settings/permissions.json,
// unless settings/permissions.yaml is present (which Kiro reads instead of permissions.json).
func allowKiroTools(dir string) (changed bool, path string, err error) {
	yamlPath := filepath.Join(dir, "settings", "permissions.yaml")
	if _, rerr := os.Stat(yamlPath); rerr == nil {
		return false, yamlPath, approvalNote(fmt.Sprintf(
			"%s is present (Kiro reads it instead of permissions.json), so %s left it alone; unless it already allows %q, add a rules entry with capability: mcp, match: [%q], effect: allow there, or %s's tools will ask before each call.",
			yamlPath, brand.Command, kiroAllowPattern, kiroAllowPattern, brand.Product))
	} else if !errors.Is(rerr, os.ErrNotExist) {
		return false, yamlPath, approvalNote(fmt.Sprintf("couldn't check %s (%v), so %s didn't add the allow rule.", yamlPath, rerr, brand.Command))
	}

	jsonPath := filepath.Join(dir, "settings", "permissions.json")
	text, err := readConfig(jsonPath)
	if err != nil {
		return false, jsonPath, err
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	if !json.Valid([]byte(text)) {
		return false, jsonPath, fmt.Errorf("it isn't plain JSON, which is all Kiro reads there")
	}
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return false, jsonPath, err
	}
	var rules []any
	var blockNote error
	if v, ok := doc.Get("rules"); ok {
		if rules, ok = v.([]any); !ok {
			return false, jsonPath, errors.New(`"rules" isn't a list`)
		}
		// In Kiro's Cedar policy engine, deny and ask (compiled as forbid) take precedence over allow.
		// If every Murmur tool is already blocked, skip adding a dead allow rule; if only some tools
		// are blocked (e.g. murmur/send), still add the allow rule for the unaffected tools while
		// reporting each overriding effect via an approvalNote.
		if denied, asked := kiroRulesBlockMurmur(rules); len(denied)+len(asked) > 0 {
			summary := describeKiroOverrides(denied, asked)
			if len(denied)+len(asked) == len(kiroMurmurTools) {
				return false, jsonPath, approvalNote(fmt.Sprintf(
					"%s already has %s, which takes precedence over allow in Kiro; %s didn't add an allow rule, so %s's tools may still be denied or ask before each call.",
					jsonPath, summary, brand.Command, brand.Product))
			}
			blockNote = approvalNote(fmt.Sprintf(
				"%s already has %s, which takes precedence over allow in Kiro, so those tools may still be denied or ask before each call.",
				jsonPath, summary))
		}
		if kiroRulesAllowMurmur(rules) {
			return false, jsonPath, blockNote
		}
	}
	rule := jsonedit.New()
	rule.Set("capability", "mcp")
	rule.Set("match", []any{kiroAllowPattern})
	rule.Set("effect", "allow")
	doc.Set("rules", append(rules, rule))
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return false, jsonPath, err
	}
	if err := writeJSONConfig(jsonPath, string(b), 0o644); err != nil {
		return false, jsonPath, err
	}
	return true, jsonPath, blockNote
}

// describeKiroOverrides phrases the overriding deny and ask rules accurately, keeping each tool
// under its strongest matching effect.
func describeKiroOverrides(denied, asked []string) string {
	var parts []string
	if len(denied) > 0 {
		parts = append(parts, fmt.Sprintf(`a "deny" rule covering %s`, strings.Join(denied, ", ")))
	}
	if len(asked) > 0 {
		parts = append(parts, fmt.Sprintf(`an "ask" rule covering %s`, strings.Join(asked, ", ")))
	}
	return strings.Join(parts, " and ")
}

// kiroRulesBlockMurmur reports which of Murmur's MCP tools are covered by overriding "deny" or
// "ask" rules in rules. A tool covered by both appears only in denied, since deny wins.
func kiroRulesBlockMurmur(rules []any) (denied, asked []string) {
	effect := map[string]string{}
	for _, r := range rules {
		o, ok := r.(*jsonedit.Object)
		if !ok {
			continue
		}
		capVal, _ := o.Get("capability")
		effVal, _ := o.Get("effect")
		if capVal != "mcp" && capVal != "all" {
			continue
		}
		if effVal != "deny" && effVal != "ask" {
			continue
		}
		for _, name := range kiroMurmurTools {
			tool := brand.MCPServer + "/" + name
			if kiroRuleMatchesTool(o, tool) {
				if effVal == "deny" || effect[tool] == "" {
					effect[tool] = effVal.(string)
				}
			}
		}
	}
	for _, name := range kiroMurmurTools {
		tool := brand.MCPServer + "/" + name
		switch effect[tool] {
		case "deny":
			denied = append(denied, tool)
		case "ask":
			asked = append(asked, tool)
		}
	}
	return denied, asked
}

// kiroRuleMatchesTool reports whether Kiro policy rule o matches resource path tool, following
// Kiro's Cedar like-matching semantics (omitted/empty match is unconditional; ** collapses to *;
// any matching exclude suppresses the rule).
func kiroRuleMatchesTool(o *jsonedit.Object, tool string) bool {
	if v, ok := o.Get("exclude"); ok {
		if list, ok := v.([]any); ok {
			for _, item := range list {
				if pat, ok := item.(string); ok && kiroLikeMatch(pat, tool) {
					return false
				}
			}
		}
	}
	m, hasMatch := o.Get("match")
	if !hasMatch {
		return true
	}
	list, ok := m.([]any)
	if !ok {
		return false
	}
	if len(list) == 0 {
		return true
	}
	for _, item := range list {
		if pat, ok := item.(string); ok && kiroLikeMatch(pat, tool) {
			return true
		}
	}
	return false
}

// kiroLikeMatch matches Cedar's `like` wildcard pattern (* matches any string; ** collapses to *).
func kiroLikeMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return s == pattern
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	if !strings.HasSuffix(s, last) {
		return false
	}
	s = s[:len(s)-len(last)]
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return true
}

// kiroRulesAllowMurmur reports whether rules already has an allow rule covering Murmur's MCP tools.
func kiroRulesAllowMurmur(rules []any) bool {
	for _, r := range rules {
		o, ok := r.(*jsonedit.Object)
		if !ok {
			continue
		}
		capVal, _ := o.Get("capability")
		effVal, _ := o.Get("effect")
		if (capVal != "mcp" && capVal != "all") || effVal != "allow" {
			continue
		}
		if hasNonEmptyList(o, "exclude") {
			continue
		}
		m, hasMatch := o.Get("match")
		if !hasMatch {
			return true
		}
		list, ok := m.([]any)
		if !ok {
			continue
		}
		if len(list) == 0 || slices.Contains(list, any("*")) || slices.Contains(list, any(kiroAllowPattern)) {
			return true
		}
	}
	return false
}

func hasNonEmptyList(o *jsonedit.Object, key string) bool {
	v, ok := o.Get(key)
	if !ok {
		return false
	}
	list, ok := v.([]any)
	return ok && len(list) > 0
}

// removeKiroAllow removes kiroAllowPattern from MCP allow rules in Kiro's permissions.json text.
// Because Kiro's addUserRule merges single-match rules of the same capability and effect into one
// rule's match list, a rule with other patterns keeps them; one with only kiroAllowPattern is
// removed. The top-level "rules" array stays even when empty, since Kiro requires it.
func removeKiroAllow(text string) (out string, changed bool, err error) {
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return "", false, err
	}
	v, ok := doc.Get("rules")
	if !ok {
		return text, false, nil
	}
	rules, ok := v.([]any)
	if !ok {
		return text, false, nil
	}
	kept := make([]any, 0, len(rules))
	for _, r := range rules {
		o, ok := r.(*jsonedit.Object)
		if !ok {
			kept = append(kept, r)
			continue
		}
		capVal, _ := o.Get("capability")
		effVal, _ := o.Get("effect")
		if capVal != "mcp" || effVal != "allow" || hasNonEmptyList(o, "exclude") {
			kept = append(kept, r)
			continue
		}
		m, hasMatch := o.Get("match")
		list, ok := m.([]any)
		if !hasMatch || !ok || !slices.Contains(list, any(kiroAllowPattern)) {
			kept = append(kept, r)
			continue
		}
		changed = true
		rest := slices.DeleteFunc(slices.Clone(list), func(x any) bool { return x == any(kiroAllowPattern) })
		if len(rest) == 0 {
			continue
		}
		o.Set("match", rest)
		kept = append(kept, o)
	}
	if !changed {
		return text, false, nil
	}
	doc.Set("rules", kept)
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// unregisterKiroAt takes Murmur's server and tool allow rule out of Kiro home hs[0]. When
// settings/permissions.yaml is present, settings/permissions.json is shadowed (and skipped by
// join), so uninstall leaves permissions.json alone too.
func (e Env) unregisterKiroAt(_ context.Context, hs []agentHome) (did, notes []string, err error) {
	h := hs[0]
	var problems []error
	prevExe := kiroRegisteredExe(h)
	hookDid, hookNotes, herr := removeKiroHook(h, e.Exe, prevExe)
	if herr != nil {
		problems = append(problems, herr)
	} else {
		did = append(did, hookDid...)
		notes = append(notes, hookNotes...)
	}
	// If removing a hook written by a renamed binary failed (e.g. transient lock/permission error),
	// keep mcpServers.murmur in mcp.json so a retry or manual removal still recognizes the hook.
	keepMCP := herr != nil && prevExe != "" && !isMurBinary(prevExe)
	mcpPath := filepath.Join(h.Dir, "settings", "mcp.json")
	if !keepMCP {
		if text, rerr := readConfig(mcpPath); rerr != nil {
			problems = append(problems, rerr)
		} else if text != "" {
			out, changed, uerr := jsonSteps(text, deleteStep("mcpServers", brand.MCPServer), emptyStep("mcpServers"))
			switch {
			case uerr != nil:
				problems = append(problems, fmt.Errorf("%s: %w", mcpPath, uerr))
			case changed:
				if werr := writeJSONConfig(mcpPath, out, 0o644); werr != nil {
					problems = append(problems, werr)
				} else {
					did = append(did, h.Label+": removed the "+brand.MCPServer+" MCP server ("+mcpPath+")")
				}
			}
		}
	}
	yamlPath := filepath.Join(h.Dir, "settings", "permissions.yaml")
	permPath := filepath.Join(h.Dir, "settings", "permissions.json")
	if _, yerr := os.Stat(yamlPath); errors.Is(yerr, os.ErrNotExist) {
		if text, rerr := readConfig(permPath); rerr != nil {
			problems = append(problems, rerr)
		} else if text != "" {
			out, changed, uerr := removeKiroAllow(text)
			switch {
			case uerr != nil:
				problems = append(problems, fmt.Errorf("%s: %w", permPath, uerr))
			case changed:
				if werr := writeJSONConfig(permPath, out, 0o644); werr != nil {
					problems = append(problems, werr)
				} else {
					did = append(did, h.Label+": removed the rule that allowed "+brand.Product+"'s tools ("+permPath+")")
				}
			}
		}
	} else if yerr == nil {
		if text, rerr := readConfig(permPath); rerr == nil && text != "" {
			if _, found, _ := removeKiroAllow(text); found {
				notes = append(notes, h.Label+": left "+permPath+" alone because "+yamlPath+" takes precedence over it; if you remove permissions.yaml later, remove \""+kiroAllowPattern+"\" from "+permPath)
			}
		}
	}
	return did, notes, errors.Join(problems...)
}

// kiroRemoval says how to take out by hand what join put in Kiro home h.
func kiroRemoval(e Env, h agentHome) string {
	var steps []string
	mcpPath := filepath.Join(h.Dir, "settings", "mcp.json")
	if b, err := os.ReadFile(mcpPath); err == nil {
		if _, found, _ := jsonedit.GetIn(string(b), []string{"mcpServers", brand.MCPServer}); found {
			steps = append(steps, "delete \""+brand.MCPServer+"\" from mcpServers in "+mcpPath)
		}
	}
	if step := kiroHookRemoval(h, e.Exe, kiroRegisteredExe(h)); step != "" {
		steps = append(steps, step)
	}
	if _, yerr := os.Stat(filepath.Join(h.Dir, "settings", "permissions.yaml")); errors.Is(yerr, os.ErrNotExist) {
		permPath := filepath.Join(h.Dir, "settings", "permissions.json")
		if text, err := readConfig(permPath); err == nil && text != "" {
			if _, found, _ := removeKiroAllow(text); found {
				steps = append(steps, "remove \""+kiroAllowPattern+"\" from the MCP allow rule in "+permPath)
			}
		}
	}
	return strings.Join(steps, "; ")
}
