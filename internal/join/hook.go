package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/jsonedit"
	"github.com/a-Fig/murmur/internal/setup"
)

// Claude Code's notice hook goes in the settings.json of each Claude Code home, which Claude Code
// reads in every project (#422). A session can be in Murmur in a clone it doesn't work in
// (`mur join -C`, #200), and no file in a repo reaches it there; the hook itself finds the clone
// its session is in, and does nothing in a session that is in none (internal/hook).
//
// Until #422 the hook was in hookFile, in every working tree of the joined clone. A join and a
// leave take it out of there (removeHooks): Claude Code would run both.
const hookFile = ".claude/settings.local.json"

// installClaudeHook adds the hook to the settings.json of the Claude Code home at dir, and reports
// whether it was new there.
func installClaudeHook(dir, exe string) (bool, error) {
	return installHook(filepath.Join(dir, "settings.json"), setup.HookCommand(exe))
}

// claudeSettings are the settings.json of every Claude Code home join registers in.
func (e Env) claudeSettings() []string {
	byAgent, _ := e.homes()
	var out []string
	for _, h := range byAgent[0] {
		if h.Dir != "" {
			out = append(out, filepath.Join(h.Dir, "settings.json"))
		}
	}
	return out
}

// hasClaudeHook says the Claude Code settings file at path runs Murmur's hook.
func hasClaudeHook(path string) bool {
	cfg, raw, err := jsonedit.ReadFile(path)
	return err == nil && raw != nil && setup.RemoveHooks(cfg)
}

// worktrees lists the working trees of the clone, the one at place first.
func worktrees(ctx context.Context, p Place) []string {
	out, err := gitx.RunIn(ctx, p.Top, "worktree", "list", "--porcelain")
	trees := []string{p.Top}
	if err != nil {
		return trees
	}
	for _, line := range strings.Split(out, "\n") {
		path, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "worktree ")
		if !ok {
			continue
		}
		path = filepath.Clean(path)
		if path == p.Top || !isDir(path) || isBare(ctx, path) {
			continue
		}
		trees = append(trees, path)
	}
	return trees
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// isBare reports whether path is the bare repository entry of a worktree list, which has no
// working tree to put a file in.
func isBare(ctx context.Context, path string) bool {
	out, err := gitx.RunIn(ctx, path, "rev-parse", "--is-bare-repository")
	return err != nil || strings.TrimSpace(out) == "true"
}

// trackedHookPath is what git tracks of the hook file's path in the working tree at top: the file
// itself, or .claude when that is a symbolic link, "" for neither, and whether what it tracks is a
// link. The hook file is meant to be this user's own, and one the repo tracks isn't: a link there
// points wherever the repo chose, the user's global ~/.claude/settings.json included.
//
// Names are matched ignoring case: macOS's and Windows' disks do by default, so a tracked .Claude
// link, or .claude/Settings.Local.json, is the file removeHooks would open. It asks with
// --icase-pathspecs rather than a ":(icase)" pathspec: with GIT_LITERAL_PATHSPECS set, git takes
// the latter for a file's name and finds nothing, but refuses the former, so the check can't pass
// by mistake.
func trackedHookPath(ctx context.Context, top string) (path string, link bool, err error) {
	dir, _, _ := strings.Cut(hookFile, "/") // .claude
	out, err := gitx.RunIn(ctx, top, "--icase-pathspecs", "ls-files", "-s", "-z", "--", dir)
	if err != nil {
		return "", false, fmt.Errorf("%s: couldn't ask git whether it tracks %s: %w", top, hookFile, err)
	}
	// Every entry is looked at, not just the first: the index can hold names differing only in case
	// (a plain .Claude and a .claude link), and on a disk that ignores case only one of them is the
	// file join and leave would open. A link among them is the answer, whichever sorts first.
	for _, rec := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok || !(strings.EqualFold(name, dir) || strings.EqualFold(name, hookFile)) {
			continue
		}
		if strings.HasPrefix(meta, "120000 ") {
			return name, true, nil
		}
		if path == "" {
			path = name
		}
	}
	return path, false, nil
}

// installHook adds the hook to the Claude Code settings file at path and reports whether that
// changed the file. A hook already there, with this command, is left as it is.
func installHook(path, command string) (bool, error) {
	cfg, raw, err := jsonedit.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	changed := false
	if hooks := cfg.Object("hooks"); hooks == nil {
		if _, taken := cfg.Get("hooks"); taken {
			return false, fmt.Errorf("%s: \"hooks\" isn't an object", path)
		}
	}
	// A hook of ours that names another path (the binary moved) is replaced, not doubled.
	if setup.RemoveHooksExcept(cfg, command) {
		changed = true
	}
	hooks := cfg.Object("hooks")
	if hooks == nil {
		hooks = jsonedit.New()
		cfg.Set("hooks", hooks)
	}
	for _, ev := range setup.HookEvents {
		event := ev.Name
		var groups []any
		if v, ok := hooks.Get(event); ok {
			var isList bool
			if groups, isList = v.([]any); !isList {
				return false, fmt.Errorf("%s: hooks.%s isn't a list", path, event)
			}
		}
		if hasCommand(groups, command) {
			continue
		}
		entry := jsonedit.New()
		entry.Set("type", "command")
		entry.Set("command", command)
		group := jsonedit.New()
		if ev.Matcher != "" {
			group.Set("matcher", ev.Matcher)
		}
		group.Set("hooks", []any{entry})
		hooks.Set(event, append(groups, group))
		changed = true
	}
	if !changed {
		return false, nil
	}
	out, err := jsonedit.Format(cfg, raw)
	if err == nil {
		err = writeAtomic(path, out, 0o644)
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return true, nil
}

func hasCommand(groups []any, command string) bool {
	for _, g := range groups {
		group, _ := g.(*jsonedit.Object)
		list, _ := group.Get("hooks")
		entries, _ := list.([]any)
		for _, e := range entries {
			h, _ := e.(*jsonedit.Object)
			if c, _ := h.Get("command"); c == command {
				return true
			}
		}
	}
	return false
}

// removeHooks takes the hook an older join wrote out of every working tree of the clone and returns
// the trees it changed. A file that held only the hook is deleted, with its directory if that is
// then empty. A link the repo tracks is not written through, as that join didn't, and notes say so;
// a plain file it tracks is edited, since taking out a hook a join put there only undoes that join.
//
// keep are the settings files the per-user hook is in. A working tree's hook file that is one of
// them, through a link of the user's own, is passed over: the hook there is the per-user one.
func removeHooks(ctx context.Context, p Place, keep []string) (changed, notes []string, err error) {
	for _, top := range worktrees(ctx, p) {
		tracked, link, terr := trackedHookPath(ctx, top)
		if terr != nil {
			err = errors.Join(err, terr)
			continue
		}
		if link {
			notes = append(notes, fmt.Sprintf("%s is a symbolic link the repo tracks, so it wasn't written through; "+
				"if an older %s join put the hook where it points, remove the hook there by hand",
				filepath.Join(top, filepath.FromSlash(tracked)), brand.Command))
			continue
		}
		path := filepath.Join(top, filepath.FromSlash(hookFile))
		if slices.ContainsFunc(keep, func(k string) bool { return sameEntry(resolveLinks(path), resolveLinks(k)) }) {
			continue
		}
		cfg, raw, rerr := jsonedit.ReadFile(path)
		if rerr != nil {
			err = errors.Join(err, fmt.Errorf("%s: %w", path, rerr)) // unreadable: leave it, go on
			continue
		}
		if raw == nil || !setup.RemoveHooks(cfg) {
			continue
		}
		// A link is the user's, and its target is written, not deleted; a file the repo tracks stays too.
		if cfg.Len() == 0 && !isLink(path) && tracked == "" {
			if rmerr := os.Remove(path); rmerr != nil {
				err = errors.Join(err, rmerr)
				continue
			}
			os.Remove(filepath.Dir(path)) // only if nothing else is in it
		} else {
			out, ferr := jsonedit.Format(cfg, raw)
			if ferr == nil {
				ferr = writeAtomic(path, out, 0o644)
			}
			if ferr != nil {
				err = errors.Join(err, fmt.Errorf("%s: %w", path, ferr))
				continue
			}
		}
		changed = append(changed, top)
	}
	return changed, notes, err
}
