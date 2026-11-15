package join

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// OpenCode keeps its user config in opencode.json or opencode.jsonc in its config directory,
// ~/.config/opencode, or $XDG_CONFIG_HOME/opencode, the same place for OpenCode 1 and 2. Either may
// be JSONC, with comments and trailing commas, and people edit it by hand, so join edits it as text
// (jsonedit): one member added, replaced or taken out, every other byte left.
//
// The two versions want different shapes (opencode.ai/v1/docs and opencode.ai/v2/docs, read
// 2026-11-10; OpenCode 1.19.5 and 2.2.0 seen):
//
//   - OpenCode 1: the server is mcp.<name>, {"type": "local", "command": [...], "enabled": true},
//     and tools are allowed in the top-level "permission" object, {"murmur_*": "allow"}, where the
//     last matching key counts, so join keeps Murmur's last. 1.19.5 refuses the whole file, with
//     "V2 permissions are not supported by OpenCode V1", when it has a top-level "permissions"
//     (#388), so Murmur never connected there.
//   - OpenCode 2: the server is mcp.servers.<name>, and "codemode": false puts its tools on the
//     agent's own list, as murmur_who and so on; by default OpenCode 2 hides an MCP server's tools
//     behind one `execute` tool of its own, and the agent has to know to look. Rules are a top-level
//     "permissions" list of {"action", "resource", "effect"}, the last match counting: join keeps
//     {"action": "murmur_*", "resource": "*", "effect": "allow"} last.
//   - OpenCode 2 reads version 1's shapes too, quietly (opencode.ai/docs/migrate-v1): only
//     "codemode" is lost, so the tools sit behind `execute`. So join writes version 2's shape where
//     `opencode --version` says 2, and version 1's everywhere else, a version it can't learn
//     included: that one loads in both.
//
// Both versions install as `opencode`; OpenCode 2's npm package also installs `opencode2`, which
// 1.19.5's error names. The rest is the same for both:
//
//   - OpenCode starts one server process per server and directory, which every session there
//     shares, and names the session in each call's _meta (see internal/mcpserver, sharedAgent). The
//     process gets OpenCode's environment, T3CODE_CLI_PATH included when T3 started OpenCode.
//   - With both opencode.json and opencode.jsonc there, OpenCode reads the .jsonc last, and its
//     entries win (Codex's review of #136, 2026-11-01): join writes there.
//   - It watches its config: a server added while it runs connects within seconds, and a session
//     already open has its tools from its next turn (seen on 2.2.0).

var openCodeArgs = []string{"mcp", "--agent", "opencode"}

// openCodeShape is where Murmur's entries go in one of OpenCode's two config formats.
type openCodeShape struct {
	name   string   // what the report calls the version
	server []string // the server's path
}

var (
	openCodeV1 = openCodeShape{name: "OpenCode 1", server: []string{"mcp", brand.MCPServer}}
	openCodeV2 = openCodeShape{name: "OpenCode 2", server: []string{"mcp", "servers", brand.MCPServer}}
)

func (s openCodeShape) v2() bool { return len(s.server) == 3 }

func (s openCodeShape) other() openCodeShape {
	if s.v2() {
		return openCodeV1
	}
	return openCodeV2
}

func (s openCodeShape) entry(exe string) *jsonedit.Object {
	o := jsonedit.New()
	o.Set("type", "local")
	o.Set("command", append([]any{exe}, toAny(openCodeArgs)...))
	if s.v2() {
		o.Set("codemode", false)
	} else {
		o.Set("enabled", true)
	}
	return o
}

// The allow rules: version 1's key in "permission", version 2's item in "permissions".
var (
	openCodeV1Rule = []string{"permission", brand.MCPServer + "_*"}
	openCodeV2Rule = func() *jsonedit.Object {
		r := jsonedit.New()
		r.Set("action", brand.MCPServer+"_*")
		r.Set("resource", "*")
		r.Set("effect", "allow")
		return r
	}()
)

// openCodeVersionWait is how long join waits for `opencode --version`. OpenCode answers in about a
// second; one that takes longer is a version join can't learn.
const openCodeVersionWait = 10 * time.Second

var openCodeVersionRE = regexp.MustCompile(`(\d+)\.\d+\.\d+`)

// openCodeShapeFor is the shape the OpenCode of h loads, asked of its command as T3 runs it, else
// of the one on PATH: version 2's where `--version` says 2 or later (2.2.0 prints
// "opencode v2.2.0"), or where the only command there is opencode2; version 1's otherwise, a
// version join can't learn included.
func (e Env) openCodeShapeFor(ctx context.Context, h agentHome) openCodeShape {
	bin := h.Bin
	if bin == "" {
		bin, _ = e.look("opencode")
	}
	if bin == "" {
		if _, err := e.look("opencode2"); err == nil {
			return openCodeV2
		}
		return openCodeV1
	}
	if e.Run != nil {
		ctx, cancel := context.WithTimeout(ctx, openCodeVersionWait)
		defer cancel()
		if out, err := e.Run(ctx, bin, "--version"); err == nil {
			if m := openCodeVersionRE.FindStringSubmatch(out); m != nil {
				if major, _ := strconv.Atoi(m[1]); major >= 2 {
					return openCodeV2
				}
				return openCodeV1
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(filepath.Base(bin)), "opencode2") {
		return openCodeV2
	}
	return openCodeV1
}

// openCodeFile is the config file in dir join edits: opencode.jsonc if it is there, which OpenCode
// reads after opencode.json, so that what it says wins; else opencode.json, new if need be. other
// is opencode.json when both are there, "" otherwise.
func openCodeFile(dir string) (path, other string) {
	json, jsonc := filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")
	switch {
	case exists(jsonc) && exists(json):
		return jsonc, json
	case exists(jsonc):
		return jsonc, ""
	}
	return json, ""
}

// registerOpenCodeAt puts Murmur's server in OpenCode's config in h, in the shape its OpenCode
// loads, and allows its tools; for OpenCode 2 it also writes Murmur's plugin there, which runs its
// hook (opencodeplugin.go). OpenCode isn't touched where neither its command nor the directory is
// there.
func (e Env) registerOpenCodeAt(ctx context.Context, h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" {
		return nil, false, nil
	}
	if !e.hasOpenCode() && h.Bin == "" && !isDir(h.Dir) {
		return nil, false, nil
	}
	shape := e.openCodeShapeFor(ctx, h)
	did, fresh, err = e.registerOpenCodeConfigAt(h, shape)
	if err != nil {
		return did, fresh, err
	}
	if !shape.v2() {
		// OpenCode 1 loads plugins of another shape, and gets none: one join wrote for an OpenCode 2
		// that was here before goes.
		if gone, _, _ := removeOpenCodePlugin(h.Dir); gone {
			did = append(did, h.Label+": removed "+brand.Product+"'s plugin, which is for OpenCode 2 ("+openCodePluginPath(h.Dir)+")")
		}
		return did, fresh, nil
	}
	wrote, err := writeOpenCodePlugin(h.Dir, e.Exe)
	if wrote {
		did = append(did, h.Label+": added "+brand.Product+"'s plugin, which tells a session of its messages while it works ("+openCodePluginPath(h.Dir)+")")
	}
	return did, fresh || wrote, err
}

// registerOpenCodeConfigAt is registerOpenCodeAt's edit of OpenCode's config files, in shape.
func (e Env) registerOpenCodeConfigAt(h agentHome, shape openCodeShape) (did []string, fresh bool, err error) {
	entry := shape.entry(e.Exe)
	path, other := openCodeFile(h.Dir)
	text, err := readOpenCode(path)
	if err != nil {
		return nil, false, err
	}
	ed, err := editOpenCode(text, shape, entry, true)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w, so the server wasn't added", path, err)
	}
	// OpenCode merges the two files, so an entry for the server in the one read first would add its
	// keys to the one join writes (a "disabled": true, say): it is made the same. Murmur's entries
	// of the other shape go from it too, since OpenCode 1 refuses a whole config over a
	// "permissions".
	var otherDid []string
	if other != "" {
		otext, err := readOpenCode(other)
		if err != nil {
			return nil, false, err
		}
		oed, err := editOpenCode(otext, shape, entry, false)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", other, err)
		}
		if oed.out != otext {
			if err := parses(oed.out); err != nil {
				return nil, false, fmt.Errorf("%s: %w", other, err)
			}
			if err := writeAtomic(other, []byte(oed.out), 0o644); err != nil {
				return nil, false, fmt.Errorf("%s: %w", other, err)
			}
			if oed.moved {
				otherDid = append(otherDid, h.Label+": moved "+brand.Product+"'s entries in "+other+" to the shape "+shape.name+" loads")
			}
			if oed.set {
				otherDid = append(otherDid, h.Label+": made the "+brand.MCPServer+" server in "+other+" the same as in "+path)
			}
			if oed.ruleOut {
				otherDid = append(otherDid, h.Label+": took "+brand.Product+"'s permission rule out of "+other+", so that the one in "+path+" comes last")
			}
		}
	}
	if ed.out == text {
		return otherDid, len(otherDid) > 0, nil
	}
	if err := parses(ed.out); err != nil {
		return nil, false, fmt.Errorf("%s: %w, so the server wasn't added", path, err)
	}
	if err := writeAtomic(path, []byte(ed.out), 0o644); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	did = otherDid
	if ed.moved {
		did = append(did, h.Label+": moved "+brand.Product+"'s entries in "+path+" to the shape "+shape.name+" loads")
	}
	if ed.set {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	if ed.allowed {
		did = append(did, h.Label+": allowed "+brand.Product+"'s tools, so they don't ask each time ("+path+")")
	}
	return did, ed.set || ed.moved || len(otherDid) > 0, nil
}

func (e Env) hasOpenCode() bool {
	for _, name := range []string{"opencode", "opencode2"} {
		if _, err := e.look(name); err == nil {
			return true
		}
	}
	return false
}

// readOpenCode is a config file's text, "" for one that isn't there. Anything but a regular file,
// or a link to one, is refused, and a huge one is cut short, so it doesn't parse and isn't written.
func readOpenCode(path string) (string, error) {
	b, err := jsonedit.ReadText(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return string(b), nil
}

// parses checks an edit's result before it is written: the editors keep every other byte, and a
// slip there would leave a config OpenCode can't load at all.
func parses(text string) error {
	if _, _, err := jsonedit.GetIn(text, nil); err != nil {
		return fmt.Errorf("an edit would leave it unreadable (%w), so it was left alone", err)
	}
	return nil
}

// openCodeEdit is what editOpenCode did to a file's text.
type openCodeEdit struct {
	out     string
	set     bool // the server's entry added or changed
	moved   bool // an entry of the other shape taken out
	allowed bool // the allow rule added, or moved last
	ruleOut bool // version 1's rule taken out of the file read first
}

// editOpenCode puts Murmur's entries in text in shape, and takes out any of the other shape's.
// main is the file join writes, which gets the server and the rule whatever it had; the other file
// gets the server only where it had one, of either shape, and no rule.
func editOpenCode(text string, shape openCodeShape, entry *jsonedit.Object, main bool) (ed openCodeEdit, err error) {
	old := shape.other()
	ed.out = text
	_, had, err := jsonedit.GetIn(text, shape.server)
	if err != nil {
		return ed, err
	}
	_, hadOld, err := jsonedit.GetIn(text, old.server)
	if err != nil {
		return ed, err
	}
	if main || had || hadOld {
		if ed.out, ed.set, err = jsonedit.SetIn(ed.out, shape.server, entry); err != nil {
			return ed, err
		}
	}
	// The other shape's server and rule go, and what held them if nothing else is left in it: an
	// empty "permissions" is still one OpenCode 1 refuses.
	type step func(string) (string, bool, error)
	var steps []step
	if old.v2() {
		steps = []step{
			func(t string) (string, bool, error) { return jsonedit.DeleteIn(t, old.server) },
			func(t string) (string, bool, error) { return deleteIfEmpty(t, []string{"mcp", "servers"}) },
			func(t string) (string, bool, error) {
				return jsonedit.RemoveIn(t, []string{"permissions"}, openCodeV2Rule)
			},
			func(t string) (string, bool, error) { return deleteIfEmpty(t, []string{"permissions"}) },
		}
	} else {
		steps = []step{
			func(t string) (string, bool, error) { return jsonedit.DeleteIn(t, old.server) },
			func(t string) (string, bool, error) { return jsonedit.DeleteIn(t, openCodeV1Rule) },
			func(t string) (string, bool, error) { return deleteIfEmpty(t, []string{"permission"}) },
		}
	}
	for _, s := range steps {
		var did bool
		if ed.out, did, err = s(ed.out); err != nil {
			return ed, err
		}
		ed.moved = ed.moved || did
	}
	if !main {
		// OpenCode 1 merges "permission" keeping each key where the file read first has it, so a
		// "murmur_*" there would stay ahead of a "*" after it, wherever join puts its own: it goes
		// (Sol's second review of #396; OpenCode's config.ts and remeda's mergeDeep, 1.19.5).
		if !shape.v2() {
			for _, s := range []step{
				func(t string) (string, bool, error) { return jsonedit.DeleteIn(t, openCodeV1Rule) },
				func(t string) (string, bool, error) { return deleteIfEmpty(t, []string{"permission"}) },
			} {
				var did bool
				if ed.out, did, err = s(ed.out); err != nil {
					return ed, err
				}
				ed.ruleOut = ed.ruleOut || did
			}
		}
		return ed, nil
	}
	if shape.v2() {
		ed.out, ed.allowed, err = jsonedit.AppendIn(ed.out, []string{"permissions"}, openCodeV2Rule)
		return ed, err
	}
	// Version 1's "permission" may be one word for every tool. "allow" needs no rule; any other is
	// made the object that says the same, "*" first, so that Murmur's key after it counts.
	raw, found, err := jsonedit.GetIn(ed.out, []string{"permission"})
	if err != nil {
		return ed, err
	}
	var word string
	if found && json.Unmarshal([]byte(raw), &word) == nil {
		if word == "allow" {
			return ed, nil
		}
		o := jsonedit.New()
		o.Set("*", word)
		o.Set(openCodeV1Rule[1], "allow")
		ed.out, ed.allowed, err = jsonedit.SetIn(ed.out, []string{"permission"}, o)
		return ed, err
	}
	ed.out, ed.allowed, err = jsonedit.SetLastIn(ed.out, openCodeV1Rule, "allow")
	return ed, err
}

// deleteIfEmpty takes out the object or list at path if there is nothing in it.
func deleteIfEmpty(text string, path []string) (string, bool, error) {
	raw, found, err := jsonedit.GetIn(text, path)
	if err != nil || !found {
		return text, false, nil // not there, or under something that isn't an object: nothing to do
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return text, false, nil
	}
	switch v := v.(type) {
	case map[string]any:
		if len(v) > 0 {
			return text, false, nil
		}
	case []any:
		if len(v) > 0 {
			return text, false, nil
		}
	default:
		return text, false, nil
	}
	return jsonedit.DeleteIn(text, path)
}

// openCodeRemoval names Murmur's entries, of either shape, in each of the two files (Codex's second
// review of #136: join keeps an entry in each when both are there).
func openCodeRemoval(_ Env, h agentHome) string {
	var steps []string
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		path := filepath.Join(h.Dir, name)
		text, err := readOpenCode(path)
		if err != nil || text == "" {
			continue
		}
		var what []string
		if _, found, _ := jsonedit.GetIn(text, openCodeV2.server); found {
			what = append(what, "\""+brand.MCPServer+"\" from mcp.servers")
		}
		if _, found, _ := jsonedit.GetIn(text, openCodeV1.server); found {
			what = append(what, "\""+brand.MCPServer+"\" from mcp")
		}
		if len(what) == 0 {
			continue
		}
		if _, removed, _ := jsonedit.RemoveIn(text, []string{"permissions"}, openCodeV2Rule); removed {
			what = append(what, "the "+brand.MCPServer+"_* rule from permissions")
		}
		if _, found, _ := jsonedit.GetIn(text, openCodeV1Rule); found {
			what = append(what, "\""+openCodeV1Rule[1]+"\" from permission")
		}
		steps = append(steps, "delete "+strings.Join(what, ", ")+" in "+path)
	}
	if path := openCodePluginPath(h.Dir); exists(path) {
		steps = append(steps, "delete "+path)
	}
	return strings.Join(steps, "; ")
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
