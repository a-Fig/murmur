package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// repo makes a git repository with the given files, and returns its root.
func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Git reports the real path (a temp dir may sit behind a symlink); so does Init.
	top, err := Top(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return top
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
	return err == nil
}

func initRepo(t *testing.T, dir string) []string {
	t.Helper()
	changed, err := Init(context.Background(), dir)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	return changed
}

// init writes the marker into both files, creating the ones that are missing, and says the repo
// uses Murmur so an agent there knows to ask its user about joining.
func TestInitWritesTheMarkerIntoBothFiles(t *testing.T) {
	// The section is a few lines that point to the pages with the detail, not the detail (Tyler,
	// 2026-10-29: 5 or 6 lines). Blank lines don't count: his wording of 2026-11-01 (#124) has one.
	n := 0
	for _, line := range strings.Split(section, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	if n > 6 {
		t.Errorf("the section is %d lines, want 6 at most:\n%s", n, section)
	}
	dir := repo(t, map[string]string{"AGENTS.md": "# Notes\nkeep this\n"})
	if changed := initRepo(t, dir); len(changed) != 2 {
		t.Fatalf("changed %q, want AGENTS.md and CLAUDE.md", changed)
	}
	agents, claude := read(t, dir, "AGENTS.md"), read(t, dir, "CLAUDE.md")
	if !strings.HasPrefix(agents, "# Notes\nkeep this\n\n"+markStart) || strings.Count(agents, markStart) != 1 {
		t.Errorf("AGENTS.md:\n%s", agents)
	}
	if claude != section {
		t.Errorf("a CLAUDE.md that didn't exist is just the section:\n%s", claude)
	}
	for _, want := range []string{"ask your user first", brand.JoinDoc, brand.UseDoc} {
		if !strings.Contains(agents, want) {
			t.Errorf("the section doesn't say %q:\n%s", want, agents)
		}
	}
	if !Marked(dir) {
		t.Error("Marked doesn't see the marker init wrote")
	}
	if changed := initRepo(t, dir); len(changed) != 0 {
		t.Errorf("a second init changed %q", changed)
	}
}

// The section sends agents to two pages by address. A page that isn't where the address says, or
// that has lost what the section promises of it, would leave an agent holding a link to nothing.
// Since #934 the page on joining is short: it sends the agent to its own agent's page on the site,
// which is that agent's file with the steps every agent shares after it, so the promises are
// checked along that path.
func TestTheAgentPagesExistAndSayWhatTheSectionPromises(t *testing.T) {
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("a page is missing: %v", err)
		}
		return string(b)
	}
	raw := func(url string) string { return read(url[strings.Index(url, "/main/")+len("/main/"):]) }
	join, use, steps := raw(brand.JoinDoc), raw(brand.UseDoc), read("docs/agents/steps.md")
	siteUse := brand.Site + "/use"
	for _, want := range []string{"Ask your user", brand.InstallSh, brand.InstallPs1, "mur version", "mur join", "mur join --leave", "mur init", FallbackBranch, siteUse} {
		if !strings.Contains(steps, want) {
			t.Errorf("the steps every agent shares don't say %q", want)
		}
	}
	for _, want := range []string{"`who`", "`lookup`", "`status`", "`send`", "`inbox`", "Message from", "via Murmur", brand.JoinDoc} {
		if !strings.Contains(use, want) {
			t.Errorf("the page on using Murmur doesn't say %q", want)
		}
	}
	// join.md lists every agent's page, and says where the same text is on GitHub for when the
	// site can't be reached.
	for _, want := range []string{"Ask your user", "https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/steps.md", "docs/agents/harnesses/"} {
		if !strings.Contains(join, want) {
			t.Errorf("the page on joining doesn't say %q", want)
		}
	}
	for _, name := range []string{"claude-code", "codex", "opencode", "pi", "muse-code", "grok", "antigravity"} {
		if !strings.Contains(join, brand.AgentDoc(name)) {
			t.Errorf("the page on joining doesn't link to %s", brand.AgentDoc(name))
		}
		if text := read("docs/agents/harnesses/" + name + ".md"); !strings.HasPrefix(text, "# Murmur in ") {
			t.Errorf("%s's file doesn't start with its title:\n%s", name, text)
		}
	}
	// The page on setting Murmur up is linked from the README rather than the section; the site
	// serves join.md there, and setup.md, for old links, points to join.md.
	if !strings.Contains(read("docs/agents/setup.md"), brand.JoinDoc) {
		t.Error("setup.md doesn't point to join.md")
	}
	if readme := read("README.md"); !strings.Contains(readme, brand.SetupDoc) {
		t.Errorf("the README doesn't link to the page on setting Murmur up, %s", brand.SetupDoc)
	}
}

// The page on using Murmur quotes the turn a message arrives as. An agent that reads the
// quote and then meets a header worded differently has no way to tell Murmur's turn from a user's,
// so the quote is the turn view.Turn renders, word for word, and this fails if either one is
// reworded.
func TestTheUsePageQuotesTheTurnWordForWord(t *testing.T) {
	rel := brand.UseDoc[strings.Index(brand.UseDoc, "/main/")+len("/main/"):]
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	use := strings.ReplaceAll(string(b), "\r\n", "\n")
	turn := view.Turn(proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", FromAgent: "claude-code in T3", FromOwner: "smash",
		To: "dev-0002/def456", Subject: "<the subject>", Body: "<the body, if the sender wrote one>"})
	var quoted []string
	for _, line := range strings.Split(strings.TrimSuffix(turn, "\n"), "\n") {
		if line != "" {
			line = "    " + line
		}
		quoted = append(quoted, line)
	}
	if want := strings.Join(quoted, "\n"); !strings.Contains(use, want) {
		t.Errorf("the page on using Murmur doesn't quote the turn; it should contain:\n%s", want)
	}
	// A message to everyone is the one variation in the header, and the page says so.
	everyone := view.Turn(proto.MessageView{From: "dev-0001/abc123", FromName: "quiet-otter", To: "*", Subject: "x"})
	first, _, _ := strings.Cut(everyone, ":\n")
	if !strings.HasSuffix(first, ", sent to everyone") || !strings.Contains(use, ", sent to everyone") {
		t.Errorf("the page doesn't match the header of a message to everyone: %q", first)
	}
}

// A marker in either file is enough for a repo to count as one that uses Murmur, and no marker is
// not.
func TestMarkedNeedsAMarker(t *testing.T) {
	if Marked(repo(t, map[string]string{"AGENTS.md": "# Notes\n", "CLAUDE.md": "Murmur is mentioned here\n"})) {
		t.Error("a repo with no marker is marked")
	}
	if !Marked(repo(t, map[string]string{"CLAUDE.md": markStart + "\nold\n" + markEnd + "\n"})) {
		t.Error("a repo with a marker in CLAUDE.md isn't marked")
	}
}

// A repo set up by an older init gets the current section in place of its old one, and nothing
// around it changes.
func TestInitRewritesTheSectionInPlace(t *testing.T) {
	old := "before\n\n" + markStart + "\n- call `claims`, then `claim` what you take\n" + markEnd + "\nafter\n"
	dir := repo(t, map[string]string{"AGENTS.md": old, "CLAUDE.md": "mine\n"})
	initRepo(t, dir)
	if want := "before\n\n" + section + "after\n"; read(t, dir, "AGENTS.md") != want {
		t.Errorf("AGENTS.md:\n%s\nwant:\n%s", read(t, dir, "AGENTS.md"), want)
	}
	if want := "mine\n\n" + section; read(t, dir, "CLAUDE.md") != want {
		t.Errorf("CLAUDE.md:\n%s", read(t, dir, "CLAUDE.md"))
	}
	if changed := initRepo(t, dir); len(changed) != 0 {
		t.Errorf("a second init changed %q", changed)
	}
}

func TestInitKeepsCRLF(t *testing.T) {
	old := "before\r\n\r\n" + markStart + "\r\nold\r\n" + markEnd + "\r\nafter\r\n"
	dir := repo(t, map[string]string{"AGENTS.md": old})
	initRepo(t, dir)
	got := read(t, dir, "AGENTS.md")
	if strings.Count(got, "\n") != strings.Count(got, "\r\n") {
		t.Errorf("mixed line endings:\n%q", got)
	}
	if changed := initRepo(t, dir); len(changed) != 0 {
		t.Errorf("a second init changed %q", changed)
	}
}

// A section whose end marker was deleted by hand is left as it is.
func TestInitLeavesABrokenSection(t *testing.T) {
	old := markStart + "\nhand-edited, no end marker\n"
	dir := repo(t, map[string]string{"AGENTS.md": old})
	initRepo(t, dir)
	if got := read(t, dir, "AGENTS.md"); got != old {
		t.Errorf("changed:\n%s", got)
	}
}

const oldMCP = `{
    "mcpServers": {
        "other": {"command": "other", "args": ["serve"]},
        "murmur": {"command": "mur", "args": ["mcp"]}
    },
    "extra": [1, 2]
}
`

const oldSettings = `{
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "*", "hooks": [{"type": "command", "command": "mur hook"}]},
      {"matcher": "Edit", "hooks": [{"type": "command", "command": "other-hook"}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "mur hook"}]}
    ]
  }
}
`

// init no longer puts the server or the hook into files that are committed. Where an older init
// did, it takes out those entries and nothing else.
func TestInitCleansAnOldInstall(t *testing.T) {
	dir := repo(t, map[string]string{".mcp.json": oldMCP, ".claude/settings.json": oldSettings})
	changed := initRepo(t, dir)
	if got := strings.Join(changed, "\n"); !strings.Contains(got, ".mcp.json") || !strings.Contains(got, ".claude/settings.json") {
		t.Errorf("init didn't say it cleaned both files:\n%s", got)
	}

	wantMCP := `{
    "mcpServers": {
        "other": {
            "command": "other",
            "args": [
                "serve"
            ]
        }
    },
    "extra": [
        1,
        2
    ]
}
`
	if got := read(t, dir, ".mcp.json"); got != wantMCP {
		t.Errorf(".mcp.json:\n%s\nwant:\n%s", got, wantMCP)
	}
	settings := read(t, dir, ".claude/settings.json")
	if strings.Contains(settings, "mur hook") || !strings.Contains(settings, "other-hook") || !strings.Contains(settings, `"Bash(ls)"`) {
		t.Errorf(".claude/settings.json:\n%s", settings)
	}
	if strings.Contains(settings, "UserPromptSubmit") {
		t.Errorf("an event left with no hooks should be gone:\n%s", settings)
	}
	if changed := initRepo(t, dir); len(changed) != 0 {
		t.Errorf("a second init changed %q", changed)
	}
}

// A file that held nothing but what the older init wrote is deleted, with its directory.
func TestInitRemovesFilesItOnlyHeld(t *testing.T) {
	dir := repo(t, map[string]string{
		".mcp.json":             `{"mcpServers": {"murmur": {"command": "mur", "args": ["mcp"]}}}`,
		".claude/settings.json": `{"hooks": {"PostToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "mur hook"}]}]}}`,
	})
	initRepo(t, dir)
	if exists(dir, ".mcp.json") || exists(dir, ".claude/settings.json") || exists(dir, ".claude") {
		t.Error("the emptied files are still there")
	}
}

// A repo set up with the hook on every event (an init from before #55 put it in the committed
// settings, and added events as the hook learned them) loses all of it, and keeps what isn't
// Murmur's.
func TestInitRemovesTheHookFromEveryEventItWasOn(t *testing.T) {
	var events []string
	for _, ev := range HookEvents {
		events = append(events, `"`+ev.Name+`": [{"hooks": [{"type": "command", "command": "mur hook"}]}]`)
	}
	dir := repo(t, map[string]string{".claude/settings.json": `{
  "permissions": {"allow": ["Bash(go test:*)"]},
  "hooks": {
    ` + strings.Join(events, ",\n    ") + `,
    "Notification": [{"hooks": [{"type": "command", "command": "other-tool"}]}]
  }
}`})
	initRepo(t, dir)
	got := read(t, dir, ".claude/settings.json")
	if strings.Contains(got, "mur hook") {
		t.Errorf("Murmur's hook is still there:\n%s", got)
	}
	for _, kept := range []string{"Bash(go test:*)", "other-tool"} {
		if !strings.Contains(got, kept) {
			t.Errorf("lost %q:\n%s", kept, got)
		}
	}
	for _, ev := range HookEvents {
		if strings.Contains(got, `"`+ev.Name+`"`) {
			t.Errorf("an empty %s is left:\n%s", ev.Name, got)
		}
	}
}

// A Murmur entry that isn't the one init wrote (another program with that name, or the user's own
// settings) is not init's to remove.
func TestInitLeavesEntriesThatAreNotItsOwn(t *testing.T) {
	mcp := `{"mcpServers": {"murmur": {"command": "node", "args": ["my-mur.js"]}}}`
	settings := `{"hooks": {"PostToolUse": [{"hooks": [{"type": "command", "command": "other mur hook"}]}]}}`
	dir := repo(t, map[string]string{".mcp.json": mcp, ".claude/settings.json": settings})
	initRepo(t, dir)
	if read(t, dir, ".mcp.json") != mcp || read(t, dir, ".claude/settings.json") != settings {
		t.Error("init changed an entry it didn't write")
	}
}

// What join writes is recognised, wherever the binary is, and nothing else is.
func TestIsHookCommand(t *testing.T) {
	for command, want := range map[string]bool{
		"mur hook":                        true,
		`"C:/Users/a b/bin/mur.exe" hook`: true,
		`"/home/a/.local/bin/mur" hook`:   true,
		`"/home/a/\$tools/mur" hook`:      true,
		`"/home/a/\"q\"/mur" hook`:        true,
		"/usr/local/bin/mur hook":         true,
		"mur hook --extra":                false,
		"mur who":                         false,
		"other mur hook":                  false,
		`"/home/a/bin/not-mur" hook`:      false,
		`"unterminated mur hook`:          false,
		"":                                false,
	} {
		if got := IsHookCommand(command); got != want {
			t.Errorf("IsHookCommand(%q) = %v, want %v", command, got, want)
		}
	}
}

// A shell runs the hook command, so a path with a space, a dollar sign, a backtick or a quote in
// it must reach the binary as written: HookCommand is run through a real shell here, and then
// recognised again by IsHookCommand (Codex review, 2026-10-29).
func TestHookCommandReachesTheBinaryThroughAShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the command in")
	}
	names := []string{"a b", "$HOME", "x`echo wrong`", "$(echo wrong)", `back\slash`}
	if runtime.GOOS != "windows" {
		names = append(names, `q"uote`) // not a legal file name there
	}
	for _, name := range names {
		bin := filepath.Join(t.TempDir(), name, "mur")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("#!/bin/sh\necho ran\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		command := HookCommand(bin)
		out, err := exec.Command(sh, "-c", command).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "ran" {
			t.Errorf("%s: the shell ran something else: %q, %v\n%s", name, command, err, out)
		}
		if !IsHookCommand(command) {
			t.Errorf("%s: %q isn't recognised as Murmur's hook", name, command)
		}
	}
}

// On Windows the hook's path has forward slashes, which bash reads and a backslash it would not.
func TestHookCommandUsesForwardSlashesOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only Windows paths have backslashes to convert")
	}
	if got, want := HookCommand(`C:\Users\a b\bin\mur.exe`), `"C:/Users/a b/bin/mur.exe" hook`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
