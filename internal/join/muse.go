package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Muse Code (Meta's agent) keeps its user settings in settings.json in $XDG_CONFIG_HOME/muse, else
// ~/.config/muse (%USERPROFILE%\.config\muse on Windows). Seen on Muse 1.4.14, 2026-11-01:
//
//   - The file is strict JSON: a comment in it, and Muse refuses to start. join edits it as text
//     (jsonedit.SetIn), writing JSON only.
//   - A server is mcpServers.<name>: command, args, env (strings), and mode. A server is "required"
//     unless its mode says "optional", and Muse won't start a session whose required server fails,
//     so Murmur's is optional. A file that has the older mcp_servers too, or a server with both
//     required and mode, loses every MCP server: join refuses a file with mcp_servers.
//   - Muse starts one server process per session and gives it MUSE_SESSION_ID, the session's ID,
//     which T3 Code also keeps for the thread. Of its own environment it passes on only a few
//     variables (PATH, TEMP, HOME, SYSTEMROOT and the like) and what env names; a ${VAR:-} there is
//     Muse's variable, empty if unset (a ${VAR} with no default that is unset makes Muse skip the
//     server, silently). So env forwards T3CODE_CLI_PATH and T3CODE_HOME, which say the session is
//     in T3, and, on Windows, the variables Go and git find the user's home and credentials by.
//   - There is no setting found that allows a server's tools without asking: in T3, outside
//     full-access mode, each Murmur call asks once until "Always allow" is chosen.
//   - Servers are read when a session starts; a session already open doesn't get one added later.
//
// Murmur's hook (#461) goes in the same file, under hooks, as Claude Code's goes in its settings
// (internal/hook/muse.go has what Muse gives it). Seen on Muse 1.4.14, 2026-11-16:
//
//   - The events are Claude Code's names (PostToolUse), not the camelCase of Muse's protocol, and a
//     group is {matcher, hooks: [{type: "command", command}]}.
//   - On Windows Muse runs the command through cmd, and a token in double quotes breaks it: a
//     quoted path and a quoted argument each kept the hook from running. So the path goes bare, or
//     in its short form (museHookCommand).

var museArgs = []string{"mcp", "--agent", "muse"}

// museEnv are the variables Murmur's server needs from Muse's environment.
func museEnv() []string {
	vars := []string{t3CLIEnv, "T3CODE_HOME"}
	if runtime.GOOS == "windows" {
		vars = append(vars, "USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH", "USERNAME", "PROGRAMDATA")
	}
	return vars
}

// registerMuseAt puts Murmur's server and hook in Muse's settings.json in h. Muse isn't touched
// where neither its command nor the directory is there. A hook join can't write (a path Muse can't
// run, hooks it can't read) is an error after the server is written, so the server still goes in.
func (e Env) registerMuseAt(h agentHome) (did []string, fresh bool, err error) {
	if h.Dir == "" {
		return nil, false, nil
	}
	if _, lerr := e.look("muse"); lerr != nil && h.Bin == "" && !isDir(h.Dir) {
		return nil, false, nil
	}
	path := filepath.Join(h.Dir, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, false, err
	}
	text := string(b)
	if _, legacy, err := jsonedit.GetIn(text, []string{"mcp_servers"}); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	} else if legacy {
		return nil, false, fmt.Errorf("%s: %w", path, errors.New("it has the older mcp_servers key, and Muse drops every server when it finds that and mcpServers both: move its servers to mcpServers, and run join again"))
	}
	if len(b) == 0 {
		text = "{\n  \"schema_version\": 1\n}\n"
	}
	env := jsonedit.New()
	for _, v := range museEnv() {
		env.Set(v, "${"+v+":-}")
	}
	entry := jsonedit.New()
	entry.Set("command", e.Exe)
	entry.Set("args", toAny(museArgs))
	entry.Set("env", env)
	entry.Set("mode", "optional")
	out, changed, err := jsonedit.SetIn(text, []string{"mcpServers", brand.MCPServer}, entry)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w, so the server wasn't added", path, err)
	}
	hooked := false
	command, herr := museHookCommand(e.Exe)
	if herr == nil {
		var withHook string
		if withHook, hooked, herr = editMuseHooks(out, command); herr == nil {
			out = withHook
		}
	}
	if herr != nil {
		herr = fmt.Errorf("%s: %w, so %s's hook wasn't added", path, herr, brand.Product)
	}
	if !changed && !hooked {
		return nil, false, herr
	}
	if err := writeAtomic(path, []byte(out), 0o644); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	if changed {
		did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+path+")")
	}
	if hooked {
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project ("+path+")")
	}
	return did, changed, herr
}

// museHookEvents are the events join puts the hook on, the fewest that do its work, as in Codex: a
// prompt marks the session at work, and its words mark a message handed over as the turn read
// (#611); a tool call brings the notices; and Stop ends the turn, keeping it going with what wasn't
// read (#202).
var museHookEvents = []struct{ name, matcher string }{
	{"UserPromptSubmit", ""},
	{"PostToolUse", "*"},
	{"Stop", ""},
}

// museHookCommand is the hook's command line for Murmur's binary at exe. On Windows Muse runs it
// through cmd and can't run a quoted path, so a path that needs quotes even in its short form is an
// error, rather than a hook that never runs.
func museHookCommand(exe string) (string, error) {
	command := agentHookCommand(exe, "muse")
	if runtime.GOOS == "windows" && strings.HasPrefix(command, "& ") {
		return "", fmt.Errorf("%s has characters Muse can't run unquoted, and Muse can't run a quoted path", exe)
	}
	return command, nil
}

// museGroup is a hook group in Muse's settings: what its one handler runs, and its matcher. ours
// says it is join's, by that command (`mur hook muse`).
func museGroup(g any) (command, matcher string, ours bool) {
	o, _ := g.(*jsonedit.Object)
	list, _ := o.Get("hooks")
	hs, _ := list.([]any)
	if len(hs) != 1 {
		return "", "", false
	}
	h, _ := hs[0].(*jsonedit.Object)
	c, _ := h.Get("command")
	command, _ = c.(string)
	m, _ := o.Get("matcher")
	matcher, _ = m.(string)
	ours = strings.HasSuffix(strings.TrimSpace(command), " hook muse") && isAgentHookCommand(command)
	return command, matcher, ours
}

// editMuseHooks returns the settings' text with join's hook on each of museHookEvents, running
// command. A group of join's with another command (the binary moved) is replaced in its place, one
// as join would write it is left, and the user's own hooks stay as they are.
func editMuseHooks(text, command string) (out string, changed bool, err error) {
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return "", false, err
	}
	hooks := doc.Object("hooks")
	if hooks == nil {
		if _, taken := doc.Get("hooks"); taken {
			return "", false, errors.New(`"hooks" isn't an object`)
		}
		hooks = jsonedit.New()
		doc.Set("hooks", hooks)
	}
	for _, ev := range museHookEvents {
		var groups []any
		if v, ok := hooks.Get(ev.name); ok {
			if groups, ok = v.([]any); !ok {
				return "", false, fmt.Errorf("hooks.%s isn't a list", ev.name)
			}
		}
		handler := jsonedit.New()
		handler.Set("type", "command")
		handler.Set("command", command)
		group := jsonedit.New()
		if ev.matcher != "" {
			group.Set("matcher", ev.matcher)
		}
		group.Set("hooks", []any{handler})
		kept, placed := []any{}, false
		for _, g := range groups {
			c, m, ours := museGroup(g)
			switch {
			case !ours:
				kept = append(kept, g)
			case placed:
				changed = true // a second group of join's
			case c == command && m == ev.matcher:
				kept, placed = append(kept, g), true
			default:
				kept, placed, changed = append(kept, group), true, true
			}
		}
		if !placed {
			kept, changed = append(kept, group), true
		}
		hooks.Set(ev.name, kept)
	}
	if !changed {
		return text, false, nil
	}
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// removeMuseHooks returns the settings' text without join's hook groups, an event, and hooks, left
// with none taken out too. changed is false where there were none.
func removeMuseHooks(text string) (out string, changed bool, err error) {
	doc, err := jsonedit.Parse([]byte(text))
	if err != nil {
		return "", false, err
	}
	hooks := doc.Object("hooks")
	for _, ev := range hooks.Keys() {
		v, _ := hooks.Get(ev)
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		var kept []any
		for _, g := range groups {
			if _, _, ours := museGroup(g); !ours {
				kept = append(kept, g)
			}
		}
		switch {
		case len(kept) == len(groups):
			continue
		case len(kept) == 0:
			hooks.Delete(ev)
		default:
			hooks.Set(ev, kept)
		}
		changed = true
	}
	if !changed {
		return text, false, nil
	}
	if hooks.Len() == 0 {
		doc.Delete("hooks")
	}
	b, err := jsonedit.Format(doc, []byte(text))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// unregisterMuse takes Murmur's hook out of Muse's settings.json, then its server
// (unregisterMuseAt).
func (e Env) unregisterMuse(ctx context.Context, hs []agentHome) (did, notes []string, err error) {
	path := filepath.Join(hs[0].Dir, "settings.json")
	text, err := readConfig(path)
	if err != nil {
		return nil, nil, err
	}
	if text != "" {
		out, changed, err := removeMuseHooks(text)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		if changed {
			if err := writeJSONConfig(path, out, 0o644); err != nil {
				return nil, nil, err
			}
			did = append(did, hs[0].Label+": removed "+brand.Product+"'s hook ("+path+")")
		}
	}
	more, notes, err := e.unregisterMuseAt(ctx, hs)
	return append(did, more...), notes, err
}

func museRemoval(_ Env, h agentHome) string {
	path := filepath.Join(h.Dir, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var steps []string
	if _, found, _ := jsonedit.GetIn(string(b), []string{"mcpServers", brand.MCPServer}); found {
		steps = append(steps, "delete \""+brand.MCPServer+"\" from mcpServers in "+path)
	}
	if _, hooked, _ := removeMuseHooks(string(b)); hooked {
		steps = append(steps, "delete the hooks that run `"+brand.Command+" hook muse` from "+path)
	}
	return strings.Join(steps, "; ")
}
