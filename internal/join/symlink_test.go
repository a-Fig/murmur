package join

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// linkOrSkip makes a symbolic link, and skips the test where links can't be made: on Windows that
// takes a privilege or Developer Mode. GitHub's runners can make them, Windows' included, and there
// a failure fails the test rather than skip it: CI is where join's handling of links, nested ones
// in .claude among them, is shown to hold on Windows, and a skip there would hide that it wasn't
// (#371).
func linkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("making a symbolic link on CI: %v", err)
		}
		t.Skipf("no symbolic links here: %v", err)
	}
}

func mustBeLink(t *testing.T, path string) {
	t.Helper()
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s isn't a link any more (%v)", path, err)
	}
}

// A link to a file that doesn't exist yet (a dotfiles repository's, not created) is written
// through as well: the file appears where the link points and the link stays (Codex review,
// 2026-10-29). EvalSymlinks fails on such a link, and a write that gave up there replaced it.
func TestRegisterCodexWritesThroughADanglingSymlink(t *testing.T) {
	sb := newSandbox(t, false)
	link := filepath.Join(sb.CodexHome, "config.toml")
	linkOrSkip(t, filepath.Join("..", "dotfiles", "codex.toml"), link)
	if _, _, err := sb.registerCodex(); err != nil {
		t.Fatal(err)
	}
	mustBeLink(t, link)
	if got := read(t, filepath.Join(sb.root, "dotfiles", "codex.toml")); !strings.Contains(got, "[mcp_servers.murmur]") {
		t.Errorf("the file the link points to:\n%s", got)
	}
}

// A hook file that is a link was written through when an older join put the hook in, and is again
// when it comes out, even when what is left is nothing: the link is the user's and stays, with the
// file it points to emptied rather than deleted (Codex review, 2026-10-29).
func TestHookFileThatIsALinkIsKeptWhenTheHookComesOut(t *testing.T) {
	p, _ := clone(t)
	target := filepath.Join(t.TempDir(), "settings.json")
	write(t, target, "{}\n")
	link := filepath.Join(p.Top, ".claude", "settings.local.json")
	linkOrSkip(t, target, link)

	oldHooks(t, p, filepath.Join(t.TempDir(), "bin", "mur"))
	mustBeLink(t, link)
	if events := hookEvents(t, target); !reflect.DeepEqual(events, wantHookEvents()) {
		t.Errorf("the file the link points to has the hook on %v", events)
	}

	if _, _, err := removeHooks(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	mustBeLink(t, link)
	if got := read(t, target); strings.Contains(got, "hook") {
		t.Errorf("the hook is still in the file the link points to:\n%s", got)
	}
}

// A link whose target goes through another link and back up with `..` is followed the way the
// system follows it, not as a path cleaned on paper (Codex review, 2026-10-29).
func TestRegisterCodexFollowsDotDotThroughALink(t *testing.T) {
	sb := newSandbox(t, false)
	elsewhere := filepath.Join(sb.root, "dotfiles", "subdir")
	write(t, filepath.Join(sb.root, "dotfiles", "settings.toml"), "model = \"real\"\n")
	write(t, filepath.Join(sb.CodexHome, "settings.toml"), "model = \"sibling\"\n")
	linkOrSkip(t, elsewhere, filepath.Join(sb.CodexHome, "alias"))
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(sb.CodexHome, "config.toml")
	// Not filepath.Join: it cleans `alias/..` away on paper, which is the very thing under test.
	sep := string(filepath.Separator)
	linkOrSkip(t, "alias"+sep+".."+sep+"settings.toml", link)
	if _, _, err := sb.registerCodex(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(sb.root, "dotfiles", "settings.toml")); !strings.Contains(got, "[mcp_servers.murmur]") {
		t.Errorf("the file the link leads to:\n%s", got)
	}
	if got := read(t, filepath.Join(sb.CodexHome, "settings.toml")); strings.Contains(got, "mcp_servers") {
		t.Errorf("the file next to the link was written:\n%s", got)
	}
	// The hook's trust goes in the same file, once per hook, in the one spelling of the home Codex
	// uses (CI's temporary directory on Windows has a short name, RUNNER~1).
	got := read(t, filepath.Join(sb.root, "dotfiles", "settings.toml"))
	if n := strings.Count(got, "[hooks.state."); n != 3 || !strings.Contains(got, codexKeyPath(filepath.Join(sb.CodexHome, "hooks.json"), true)+":stop:0:0") {
		t.Errorf("%d trust entries:\n%s", n, got)
	}
}
