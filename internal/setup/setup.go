// Package setup is `init`: it puts the section into AGENTS.md and CLAUDE.md that says a repo uses
// Murmur. By default (Push) it makes that change as a commit on the remote's default branch, built
// with plumbing so that no working tree or index is touched, and pushes it there, or to a
// murmur-init branch for a pull request when the server refuses. With --no-push (Init) it edits the
// working tree instead, for a person to review and commit. It is the only part of Murmur that writes
// to a repo's branches or tracked files, and it runs only when a person asks. Joining (package join)
// is a separate, per-user step that commits nothing.
package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/jsonedit"
)

// Init edits the working tree containing dir, as `mur init --no-push` does, for a person to review
// and commit, and returns what it changed, one line per file. Push makes the same edits as a commit
// on the remote's default branch.
func Init(ctx context.Context, dir string) ([]string, error) {
	top, err := Top(ctx, dir)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, s := range steps {
		path := filepath.Join(top, filepath.FromSlash(s.name))
		if err := inTree(top, path, s.name); err != nil {
			if s.optional {
				continue // as Push passes over a config file it can't follow
			}
			return changed, err
		}
		b, err := readRepoFile(path)
		exists := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			if s.optional {
				continue // a config file that can't be read has nothing init can safely clean
			}
			return changed, err
		}
		e := s.edit(b, exists)
		if e.change {
			if err := writeOut(top, path, e); err != nil {
				if !s.optional {
					return changed, err
				}
				e.msg = err.Error()
			}
		}
		if e.msg != "" {
			changed = append(changed, s.name+": "+e.msg)
		}
	}
	return changed, nil
}

// inTree refuses a path that leads out of the working tree at top through a symbolic link, as Push
// refuses one in its commit: the link is the repo's, a commit can point it anywhere (a shell's
// startup file, a JSON file init would rewrite or delete), and init edits only the repo. A link
// into .git is refused too, and so is a link to nothing, which a write would create wherever it
// points. Name is the path as the user knows it.
func inTree(top, path, name string) error {
	real, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		if fi, lerr := os.Lstat(path); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(path)
			return fmt.Errorf("%s is a symbolic link to %s, which isn't there, so init leaves it alone", name, target)
		}
		return nil // nothing there; init creates only AGENTS.md and CLAUDE.md, at the top
	}
	if err != nil {
		return err
	}
	if t, err := filepath.EvalSymlinks(top); err == nil {
		top = t
	}
	rel, err := filepath.Rel(top, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s leads to %s, outside the repo, through a symbolic link, so init leaves it alone", name, real)
	}
	if first, _, _ := strings.Cut(filepath.ToSlash(rel), "/"); isGitDir(top, first) {
		return fmt.Errorf("%s leads to %s, inside .git, through a symbolic link, so init leaves it alone", name, real)
	}
	return nil
}

// isGitDir reports whether first, a name at the top of the working tree, is its .git. On a disk
// that ignores case, as macOS's and Windows' do by default, .GIT is .git too, and on Windows a
// short name can be; so the two are compared as files, and by name ignoring case as well.
func isGitDir(top, first string) bool {
	if strings.EqualFold(first, ".git") {
		return true
	}
	g, err := os.Stat(filepath.Join(top, ".git"))
	if err != nil {
		return false
	}
	f, err := os.Stat(filepath.Join(top, first))
	return err == nil && os.SameFile(f, g)
}

// writeOut makes a step's change to the file at path. A file it removes takes its directory with it
// when nothing else is in that, as .claude is when it held only the settings init cleaned.
func writeOut(top, path string, e edited) error {
	if !e.remove {
		// Init reads every file it edits through readRepoFile, and so does the marker check. A file
		// that the edit took past what that reads would no longer count as marked, so init would
		// report success on a repo that doesn't say it uses Murmur.
		if len(e.data) > maxRepoFile {
			return fmt.Errorf("the file would be over %d MiB, more than %s reads", maxRepoFile>>20, brand.Command)
		}
		if err := os.WriteFile(path, e.data, 0o644); err != nil {
			return fmt.Errorf("couldn't rewrite the file: %w", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("couldn't remove the empty file: %w", err)
	}
	if dir := filepath.Dir(path); dir != top {
		os.Remove(dir) // only if nothing else is in it
	}
	return nil
}

// A step is one of init's edits, on one file. It takes the file's content and returns what to do
// with it, so the same steps edit the working tree (Init) and build the commit Push makes.
type step struct {
	name string // the file, slash-separated, from the top of the tree
	// optional is for the config files init only cleans: one that can't be read is left alone,
	// and one that can't be written is reported in the step's message rather than failing init.
	optional bool
	edit     func(b []byte, exists bool) edited
}

// edited is what a step does to its file.
type edited struct {
	msg    string // what to tell the user, after the file's name; "" for nothing
	change bool   // the file becomes data, or goes if remove is set
	remove bool
	data   []byte
}

var steps = []step{
	{name: "AGENTS.md", edit: markerFile},
	{name: "CLAUDE.md", edit: markerFile},
	{name: ".mcp.json", optional: true, edit: cleanMCPConfig},
	{name: ".claude/settings.json", optional: true, edit: cleanClaudeHooks},
}

// Top is the root of the working tree containing dir.
func Top(ctx context.Context, dir string) (string, error) {
	out, err := gitx.RunIn(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not inside a git repository (%s)", dir)
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

const (
	markStart = "<!-- " + brand.Name + ":start -->"
	markEnd   = "<!-- " + brand.Name + ":end -->"
)

// markerFiles are the files init puts the marker in: Codex reads AGENTS.md, Claude Code CLAUDE.md.
var markerFiles = []string{"AGENTS.md", "CLAUDE.md"}

// Marked reports whether the repo says it uses Murmur, as seen from the working tree at top, which
// is what invites teammates' agents to join. Since #830 it turns nothing on: join asks it only to
// say whether the user is the first here. The marker in either file is enough, in the
// working tree or on the clone's remote-tracking default branch (refs/remotes/<remote>/HEAD, read
// without the network): `mur init` puts the section on the default branch, and a worktree on a
// branch made before that would otherwise stay unmarked until it merged the default branch.
func Marked(top string) bool { return markedTree(top) || markedUpstream(top) }

// markedTree reports whether the working tree at top has the marker in either file.
func markedTree(top string) bool {
	for _, name := range markerFiles {
		if b, err := readRepoFile(filepath.Join(top, name)); err == nil && bytes.Contains(b, []byte(markStart)) {
			return true
		}
	}
	return false
}

// maxRepoFile is the most readRepoFile reads. Codex reads only the first 32 KiB of an AGENTS.md by
// default; 4 MiB is over a hundred times that, so no file an agent really reads comes near it.
const maxRepoFile = 4 << 20

// readRepoFile reads a file of the working tree for the marker check and for init: a regular file,
// or a link to one (CLAUDE.md is often a link to AGENTS.md), of at most maxRepoFile. The tree holds
// whatever a commit put there, someone else's pull request included: a link to /dev/zero would be
// read until memory ran out, /dev/stdin would swallow the reader's own input, and a terminal or a
// pipe would block it.
func readRepoFile(path string) ([]byte, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s isn't a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() { // replaced between the two looks
		return nil, fmt.Errorf("%s isn't a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRepoFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRepoFile {
		return nil, fmt.Errorf("%s is over %d MiB, more than any file an agent reads", path, maxRepoFile>>20)
	}
	return b, nil
}

// upstreamRecheck is how long markedUpstream's answer for a working tree stands. Until #830 the MCP
// server asked Marked every 2 seconds, and the answer takes git processes, which on Windows cost a
// tenth of a second or more each (#21); the remote-tracking branch moves only on a fetch anyway.
var upstreamRecheck = 20 * time.Second

var upstream = struct {
	sync.Mutex
	seen map[string]upstreamAnswer
}{seen: map[string]upstreamAnswer{}}

type upstreamAnswer struct {
	at     time.Time
	marked bool
}

// markedUpstream reports whether the remote-tracking default branch of the clone containing top
// has the marker in either file.
func markedUpstream(top string) bool {
	upstream.Lock()
	a, ok := upstream.seen[top]
	upstream.Unlock()
	if ok && time.Since(a.at) < upstreamRecheck {
		return a.marked
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	marked := false
	if g, _, err := gitIn(ctx, top); err == nil {
		if remote, err := bus.RemoteFor(ctx, g); err == nil {
			marked = markedAt(ctx, g, "refs/remotes/"+remote+"/HEAD")
		}
	}
	upstream.Lock()
	upstream.seen[top] = upstreamAnswer{at: time.Now(), marked: marked}
	upstream.Unlock()
	return marked
}

// markedAt reports whether either marker file in the commit rev has the marker. A rev that names
// nothing isn't marked. git grep is one process but reads a symbolic link as the path it holds, so
// without a match the files are read again as Push reads them, following links.
func markedAt(ctx context.Context, g gitx.Git, rev string) bool {
	if _, err := g.Run(ctx, "", append([]string{"grep", "-q", "-F", "-e", markStart, rev, "--"}, markerFiles...)...); err == nil {
		return true
	}
	tip, err := g.Run(ctx, "", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return false
	}
	t := &treeFiles{ctx: ctx, g: g, tip: strings.TrimSpace(tip), files: map[string]*treeFile{}}
	for _, name := range markerFiles {
		if _, b, ok, err := t.read(name); err == nil && ok && bytes.Contains(b, []byte(markStart)) {
			return true
		}
	}
	return false
}

// gitIn is git for the checkout at top, two ways. clone runs in the clone's common git dir, as the
// daemon does (one per clone), so the remote is chosen exactly as the daemon chooses it, whatever
// a worktree's own config says. own runs in the checkout's own git directory, which for a linked
// worktree is the one under the clone's worktrees/, so that what git reads for the commit (a
// user.name set for this worktree alone, an includeIf on its path) is what git would read in that
// checkout. Both are found without running git when they can be.
func gitIn(ctx context.Context, top string) (clone, own gitx.Git, err error) {
	if dir, common, ok := gitx.Discover(top); ok {
		return gitx.Git{Dir: common}, gitx.Git{Dir: dir}, nil
	}
	out, err := gitx.RunIn(ctx, top, "rev-parse", "--path-format=absolute", "--git-common-dir", "--absolute-git-dir")
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r", "")), "\n")
	if err != nil || len(lines) != 2 {
		return gitx.Git{}, gitx.Git{}, fmt.Errorf("not inside a git repository (%s)", top)
	}
	return gitx.Git{Dir: filepath.Clean(lines[0])}, gitx.Git{Dir: filepath.Clean(lines[1])}, nil
}

// section is what both files get. It is short on purpose, since it sits in files every agent reads
// whole: it says the repo uses Murmur, that an agent that hasn't joined should, and points to the
// two pages that say how to join (which an agent does only after asking its user) and how to use
// the tools. The pages are docs/agents/. The wording is Tyler's (2026-11-01, #124), but for the
// bracket, for an agent Murmur has no tools for (#913).
var section = strings.NewReplacer("{name}", brand.Product, "{join}", brand.JoinDoc, "{use}", brand.UseDoc, "{cmd}", brand.Command).
	Replace(markStart + `
## {name}
This repo uses {name}, a cross-device, intra-repo communication channel for contributing coding agents like yourself. If you have not joined {name} yet, you should.

- To join {name} (ask your user first): {join}
- To use {name} (no tools? in T3 Code, ` + "`{cmd} who` and `{cmd} send`" + ` work in your shell): {use}
` + markEnd + "\n")

// markerFile puts the section into a file, which is created if it is missing.
func markerFile(b []byte, exists bool) edited {
	sec := []byte(section)
	if bytes.Contains(b, []byte("\r\n")) {
		sec = bytes.ReplaceAll(sec, []byte("\n"), []byte("\r\n")) // git may check out CRLF
	}
	if start := bytes.Index(b, []byte(markStart)); start >= 0 {
		// Rewrite the section in place, so a repo set up by an older version stops telling
		// agents about tools that no longer exist. A section whose end marker is gone was
		// edited by hand and is left alone.
		n := bytes.Index(b[start:], []byte(markEnd))
		if n < 0 {
			return edited{}
		}
		end := start + n + len(markEnd)
		if bytes.HasPrefix(b[end:], []byte("\r\n")) {
			end += 2
		} else if bytes.HasPrefix(b[end:], []byte("\n")) {
			end++
		}
		if bytes.Equal(b[start:end], sec) {
			return edited{}
		}
		b = append(append(append([]byte{}, b[:start]...), sec...), b[end:]...)
		return edited{msg: "updated the section telling agents the repo uses " + brand.Product, change: true, data: b}
	}
	b = append([]byte{}, b...)
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n\n")) {
		if bytes.HasSuffix(b, []byte("\n")) {
			b = append(b, '\n')
		} else {
			b = append(b, "\n\n"...)
		}
	}
	b = append(b, sec...)
	verb := "added"
	if !exists {
		verb = "created, with"
	}
	return edited{msg: verb + " a section telling agents the repo uses " + brand.Product, change: true, data: b}
}

// HookEvent is one Claude Code event the hook is installed on, and the matcher of its group.
type HookEvent struct {
	Name    string
	Matcher string // "" for an event that has none
}

// HookEvents are the events join installs the hook on. The tool events take every tool, with a
// matcher of "*": the hook itself skips those that can't commit. PreToolUse and PostToolUseFailure
// are for finding the commits a call made (#21), and a hook missing from them loses those commits.
// Stop is the end of a turn: without it the daemon takes a session that has been at work to be at
// work still, and its messages wait for the next prompt (#202).
// Before #55 `mur init` installed these in the committed settings; join does now, so every event
// that gains a use belongs here.
var HookEvents = []HookEvent{
	{"PreToolUse", "*"},
	{"PostToolUse", "*"},
	{"PostToolUseFailure", "*"},
	{"UserPromptSubmit", ""},
	{"Stop", ""},
}

// HookCommand is the notice hook's command line for the binary at exe. Claude Code runs a hook's
// command through a shell, bash on Windows too, so the path has forward slashes and sits in double
// quotes, where only a backslash, a quote, a dollar sign and a backtick mean anything.
func HookCommand(exe string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(filepath.ToSlash(exe)) + `" hook`
}

// IsHookCommand reports whether a hook's command line is Murmur's notice hook, whether written by
// an older init as a bare `mur hook` or by join (HookCommand) with the binary's full path in
// quotes.
func IsHookCommand(command string) bool {
	command = strings.TrimSpace(command)
	exe, rest := command, ""
	if strings.HasPrefix(command, `"`) {
		var word strings.Builder
		i := 1
		for ; i < len(command) && command[i] != '"'; i++ {
			if command[i] == '\\' && i+1 < len(command) && strings.IndexByte("\\\"$`", command[i+1]) >= 0 {
				i++
			}
			word.WriteByte(command[i])
		}
		if i >= len(command) {
			return false
		}
		exe, rest = word.String(), command[i+1:]
	} else if i := strings.IndexAny(command, " \t"); i >= 0 {
		exe, rest = command[:i], command[i:]
	}
	return isBinary(exe) && strings.TrimSpace(rest) == "hook"
}

// isBinary reports whether a command is Murmur's executable, wherever it is installed.
func isBinary(command string) bool {
	if i := strings.LastIndexAny(command, `/\`); i >= 0 {
		command = command[i+1:]
	}
	return strings.TrimSuffix(strings.ToLower(command), ".exe") == brand.Command
}

// cleanMCPConfig takes the Murmur server out of the project's .mcp.json, where an older init put
// it. An entry that isn't one init wrote is left, and so is everything else in the file.
func cleanMCPConfig(raw []byte, exists bool) edited {
	cfg, ok := parseConfig(raw, exists)
	if !ok {
		return edited{}
	}
	servers := cfg.Object("mcpServers")
	entry := servers.Object(brand.MCPServer)
	if entry == nil {
		return edited{}
	}
	if !isOwnServer(entry) {
		return edited{msg: "its " + brand.MCPServer + " server isn't the one an older init wrote, so it was left alone"}
	}
	servers.Delete(brand.MCPServer)
	if servers.Len() == 0 {
		cfg.Delete("mcpServers")
	}
	return dropOrWrite(cfg, raw, "removed the "+brand.MCPServer+" MCP server an older init added")
}

func isOwnServer(entry *jsonedit.Object) bool {
	cmd, _ := entry.Get("command")
	args, _ := entry.Get("args")
	list, _ := args.([]any)
	name, _ := cmd.(string)
	if name == "" || len(list) == 0 || list[0] != "mcp" {
		return false
	}
	return isBinary(name)
}

// cleanClaudeHooks takes Murmur's notice hook out of the project's .claude/settings.json, where an
// older init put it. join puts it in a file that isn't committed instead.
func cleanClaudeHooks(raw []byte, exists bool) edited {
	cfg, ok := parseConfig(raw, exists)
	if !ok || !RemoveHooks(cfg) {
		return edited{}
	}
	return dropOrWrite(cfg, raw, "removed the "+brand.Product+" notice hook an older init added")
}

// RemoveHooks deletes Murmur's notice hook from a Claude Code settings object, along with the
// groups, events and "hooks" key it leaves empty, and reports whether it found any.
func RemoveHooks(cfg *jsonedit.Object) bool { return RemoveHooksExcept(cfg, "") }

// RemoveHooksExcept is RemoveHooks that leaves hooks whose command is exactly keep.
func RemoveHooksExcept(cfg *jsonedit.Object, keep string) bool {
	hooks := cfg.Object("hooks")
	if hooks == nil {
		return false
	}
	found := false
	for _, event := range hooks.Keys() {
		groups, _ := hooks.Get(event)
		list, ok := groups.([]any)
		if !ok {
			continue
		}
		kept, removed := []any{}, false
		for _, g := range list {
			if group, ok := g.(*jsonedit.Object); ok && dropHooks(group, keep) {
				removed = true
				if entries, _ := group.Get("hooks"); len(entries.([]any)) == 0 {
					continue // the group held only Murmur's hook
				}
			}
			kept = append(kept, g)
		}
		switch {
		case !removed:
		case len(kept) == 0:
			hooks.Delete(event)
		default:
			hooks.Set(event, kept)
		}
		found = found || removed
	}
	if found && hooks.Len() == 0 {
		cfg.Delete("hooks")
	}
	return found
}

// dropHooks removes Murmur's hook commands, but not keep, from a group's "hooks" list and reports
// whether it did.
func dropHooks(group *jsonedit.Object, keep string) bool {
	entries, _ := group.Get("hooks")
	list, ok := entries.([]any)
	if !ok {
		return false
	}
	left := []any{}
	for _, e := range list {
		if h, ok := e.(*jsonedit.Object); ok {
			if c, _ := h.Get("command"); IsHookCommand(asString(c)) && c != keep {
				continue
			}
		}
		left = append(left, e)
	}
	if len(left) == len(list) {
		return false
	}
	group.Set("hooks", left)
	return true
}

func asString(v any) string { s, _ := v.(string); return s }

// parseConfig parses a JSON config file; ok is false when it is missing or isn't valid JSON, which
// cleaning treats alike: there is nothing it can safely change.
func parseConfig(raw []byte, exists bool) (cfg *jsonedit.Object, ok bool) {
	if !exists {
		return nil, false
	}
	cfg, err := jsonedit.Parse(raw)
	return cfg, err == nil
}

// dropOrWrite is the change that saves cfg, or deletes the file if cfg is empty, which is when init
// made the whole file. what says what the change does.
func dropOrWrite(cfg *jsonedit.Object, raw []byte, what string) edited {
	if cfg.Len() == 0 {
		return edited{msg: what + "; nothing else was in it, so the file is gone", change: true, remove: true}
	}
	out, err := jsonedit.Format(cfg, raw)
	if err != nil {
		return edited{msg: "couldn't rewrite the file: " + err.Error()}
	}
	return edited{msg: what, change: true, data: out}
}
