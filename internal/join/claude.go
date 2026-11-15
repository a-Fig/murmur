package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// allowRule is the permission that lets an agent call every tool of Murmur's server without asking.
// `mcp__<server>` with nothing after it is Claude Code's own spelling for that (seen on 2.1.312,
// 2026-10-29).
var allowRule = "mcp__" + brand.MCPServer

// A Claude Code home is the directory its settings.json is in (Dir) and the file it keeps user-scope
// MCP servers in (JSON). One that is a CLAUDE_CONFIG_DIR has that file inside it; the default home
// is ~/.claude with ~/.claude.json beside it.

// configDir is the CLAUDE_CONFIG_DIR that makes `claude` use Claude Code home h, "" for the default.
func (h agentHome) configDir() string {
	if filepath.Clean(h.JSON) == filepath.Join(filepath.Clean(h.Dir), ".claude.json") {
		return h.Dir
	}
	return ""
}

// registerClaude registers in the Claude Code home of the process's own environment.
func (e Env) registerClaude(ctx context.Context) (did []string, fresh bool, err error) {
	return e.registerClaudeAt(ctx, e.defaultHome(agents[0]))
}

// registerClaudeAt makes Claude Code know Murmur's server in home h, for this user in every
// project, lets its tools run without a prompt each time, and adds the notice hook (hook.go). fresh reports whether the server was
// new to that home (or moved), which is when sessions that are already running have never heard of
// it.
func (e Env) registerClaudeAt(ctx context.Context, h agentHome) (did []string, fresh bool, err error) {
	claude, err := e.look("claude")
	if err != nil && h.Bin != "" {
		claude, err = h.Bin, nil
	}
	haveCLI := err == nil
	if !haveCLI && !exists(h.JSON) && !exists(h.Dir) {
		return nil, false, nil // no Claude Code here
	}

	cfg, raw, err := jsonedit.ReadFile(h.JSON)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", h.JSON, err)
	}
	servers := cfg.Object("mcpServers")
	entry := servers.Object(brand.MCPServer) // nil-safe: a nil *Object has no keys
	if !ownsEntry(entry, e.Exe) {
		fresh = true
		switch {
		case haveCLI:
			if entry != nil {
				// A stale entry (the binary moved): `add` refuses a name that exists.
				e.runClaude(ctx, h, claude, "mcp", "remove", "--scope", "user", brand.MCPServer)
			}
			if out, err := e.runClaude(ctx, h, claude, "mcp", "add", "--scope", "user", brand.MCPServer, "--", e.Exe, "mcp"); err != nil {
				return nil, false, fmt.Errorf("`claude mcp add` failed: %v: %s", err, strings.TrimSpace(out))
			}
			did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+h.JSON+")")
		default:
			// Claude Code is configured here but its command isn't on PATH, so write the entry the
			// command would have written.
			if servers == nil {
				servers = jsonedit.New()
				cfg.Set("mcpServers", servers)
			}
			e2 := jsonedit.New()
			e2.Set("type", "stdio")
			e2.Set("command", e.Exe)
			e2.Set("args", []any{"mcp"})
			e2.Set("env", jsonedit.New())
			servers.Set(brand.MCPServer, e2)
			out, err := jsonedit.Format(cfg, raw)
			if err == nil {
				err = writeAtomic(h.JSON, out, 0o600)
			}
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", h.JSON, err)
			}
			did = append(did, h.Label+": registered the "+brand.MCPServer+" MCP server for you, in every project ("+h.JSON+")")
		}
	}

	allowed, err := allowClaudeTools(h.Dir)
	if err != nil {
		return did, fresh, err
	}
	if allowed {
		did = append(did, h.Label+": allowed "+brand.Product+"'s tools, so they don't ask each time ("+filepath.Join(h.Dir, "settings.json")+")")
	}
	hooked, err := installClaudeHook(h.Dir, e.Exe)
	if err != nil {
		return did, fresh, err
	}
	if hooked {
		did = append(did, h.Label+": added "+brand.Product+"'s hook, in every project ("+filepath.Join(h.Dir, "settings.json")+")")
	}
	return did, fresh, nil
}

// ownsEntry reports whether a Claude Code server entry runs this binary as Murmur's MCP server.
func ownsEntry(entry *jsonedit.Object, exe string) bool {
	if entry == nil {
		return false
	}
	cmd, _ := entry.Get("command")
	args, _ := entry.Get("args")
	list, _ := args.([]any)
	return cmd == exe && len(list) == 1 && list[0] == "mcp"
}

// allowClaudeTools adds the allow rule to the settings.json in dir, and reports whether it was new.
func allowClaudeTools(dir string) (bool, error) {
	path := filepath.Join(dir, "settings.json")
	cfg, raw, err := jsonedit.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	perms := cfg.Object("permissions")
	if perms == nil {
		if _, taken := cfg.Get("permissions"); taken {
			return false, fmt.Errorf("%s: \"permissions\" isn't an object, so the allow rule wasn't added", path)
		}
		perms = jsonedit.New()
		cfg.Set("permissions", perms)
	}
	var allow []any
	if v, ok := perms.Get("allow"); ok {
		var isList bool
		if allow, isList = v.([]any); !isList {
			return false, fmt.Errorf("%s: \"permissions.allow\" isn't a list, so the allow rule wasn't added", path)
		}
	}
	for _, r := range allow {
		if r == allowRule {
			return false, nil
		}
	}
	perms.Set("allow", append(allow, allowRule))
	out, err := jsonedit.Format(cfg, raw)
	if err == nil {
		err = writeAtomic(path, out, 0o644)
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

// runClaude runs the claude command against home h: with CLAUDE_CONFIG_DIR set to it, or unset for
// the default home, whatever this process's own environment says.
func (e Env) runClaude(ctx context.Context, h agentHome, claude string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, args...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "CLAUDE_CONFIG_DIR=") {
			env = append(env, kv)
		}
	}
	if dir := h.configDir(); dir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+dir)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// claudeRemoval is how to take the server and the hook out of Claude Code home h, for a leave's
// advice.
func (e Env) claudeRemoval(h agentHome) string {
	var steps []string
	if cfg, _, err := jsonedit.ReadFile(h.JSON); err == nil && cfg.Object("mcpServers").Object(brand.MCPServer) != nil {
		step := "`claude mcp remove --scope user " + brand.MCPServer + "`"
		switch dir, mine := h.configDir(), e.getenv("CLAUDE_CONFIG_DIR"); {
		case dir != "" && !samePath(dir, mine):
			step += " with CLAUDE_CONFIG_DIR set to " + dir
		case dir == "" && mine != "":
			step += " with CLAUDE_CONFIG_DIR unset"
		}
		steps = append(steps, step)
	}
	if path := filepath.Join(h.Dir, "settings.json"); hasClaudeHook(path) {
		steps = append(steps, "delete the hooks that run `"+brand.Command+" hook` from "+path)
	}
	return joinSteps(steps)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// resolveLinks follows path through symbolic links to the file a write should replace, a link whose
// target doesn't exist yet included: filepath.EvalSymlinks gives up there, and a write that did too
// would replace the link with a file. The walk below is for that case only; it joins relative
// targets lexically, so a `..` after a link in the target would be taken from the wrong place.
func resolveLinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved // the usual case, and the one that follows `..` through a link correctly
	}
	for range 40 {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return path
		}
		path = filepath.Join(dir, filepath.Base(path))
		if !isLink(path) {
			return path
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		path = target
	}
	return path
}

func isLink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// writeAtomic replaces path with data so a reader, or a crash, never sees half a file. The file
// keeps the permissions it had; a new one gets perm. A path that is a symbolic link (a dotfiles
// repository's, say) is written through: the file it points to is replaced, and the link stays.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	path = resolveLinks(path)
	if fi, err := os.Stat(path); err == nil {
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
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
