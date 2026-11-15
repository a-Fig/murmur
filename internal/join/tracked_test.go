package join

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitLink commits, in the working tree at top, a symbolic link at name to target.
func commitLink(t *testing.T, top, name, target string) {
	t.Helper()
	linkOrSkip(t, target, filepath.Join(top, filepath.FromSlash(name)))
	git(t, top, "add", "-f", "--", name)
	git(t, top, "commit", "-q", "-m", "link")
}

// A hook file the repo tracks as a link points wherever the commit chose, the user's global Claude
// Code settings included. Taking an older join's hook out doesn't write through it, and says so.
func TestHookFileTheRepoTracksAsALinkIsLeftAlone(t *testing.T) {
	p, _ := clone(t)
	outside := filepath.Join(t.TempDir(), "settings.json")
	commitLink(t, p.Top, ".claude/settings.local.json", outside)

	write(t, outside, "{\"keep\": true}\n")
	_, notes, err := removeHooks(context.Background(), p, nil)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "wasn't written through") {
		t.Errorf("on a tracked link: notes %q, err %v", notes, err)
	}
	if got := read(t, outside); got != "{\"keep\": true}\n" {
		t.Errorf("the tracked link was written through:\n%s", got)
	}
}

// .claude itself committed as a link, and the same links under names that differ only in case, lead
// to the same file on a disk that ignores case, as macOS's and Windows' do by default, so they are
// found too, there and everywhere else.
func TestLinksTheRepoTracksAreFoundWhateverTheirCase(t *testing.T) {
	for _, name := range []string{".claude", ".Claude", ".claude/Settings.Local.json", ".CLAUDE/SETTINGS.LOCAL.JSON"} {
		t.Run(name, func(t *testing.T) {
			p, _ := clone(t)
			outside := t.TempDir()
			target := outside
			if strings.Contains(name, "/") {
				target = filepath.Join(outside, "settings.json")
			}
			commitLink(t, p.Top, name, target)
			_, notes, err := removeHooks(context.Background(), p, nil)
			if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "symbolic link the repo tracks") {
				t.Errorf("on a tracked %s link: notes %q, err %v", name, notes, err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Errorf("something was written where the tracked %s link points: %v", name, entries)
			}
		})
	}
}

// A plain hook file the repo tracks keeps what else it holds: the hook an older join put there
// comes out, and the file stays.
func TestHookComesOutOfAHookFileTheRepoTracks(t *testing.T) {
	p, _ := clone(t)
	path := filepath.Join(p.Top, ".claude", "settings.local.json")
	write(t, path, "{\n  \"permissions\": {\"allow\": [\"Bash(ls)\"]}\n}\n")
	git(t, p.Top, "add", "-f", "--", ".claude/settings.local.json")
	git(t, p.Top, "commit", "-q", "-m", "settings")

	oldHooks(t, p, filepath.Join(t.TempDir(), "bin", "mur"))
	removed, notes, err := removeHooks(context.Background(), p, nil)
	if err != nil || len(notes) != 0 || len(removed) != 2 {
		t.Fatalf("removed %q, notes %q, err %v", removed, notes, err)
	}
	if got := read(t, path); strings.Contains(got, "hook") || !strings.Contains(got, "Bash(ls)") {
		t.Errorf("the tracked file, after:\n%s", got)
	}
}

// The index can track names that differ only in case. A plain .Claude sorts before a .claude link,
// and on a disk that ignores case the link may be what removeHooks opens: a link among the
// entries counts, whichever comes first (Sol's review). Index entries alone show it, with no link
// on disk, so this runs where links can't be made.
func TestATrackedLinkIsFoundBehindAPlainEntryInAnotherCase(t *testing.T) {
	p, _ := clone(t)
	empty := filepath.Join(t.TempDir(), "empty")
	write(t, empty, "")
	git(t, p.Top, "hash-object", "-w", empty)
	const blob = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" // the empty blob, just written
	for _, c := range []struct{ plain, link string }{
		{".Claude", ".claude"},
		{".claude/Settings.local.json", ".claude/settings.local.json"},
	} {
		git(t, p.Top, "rm", "-q", "--cached", "-r", "--ignore-unmatch", "--", ".claude", ".Claude")
		git(t, p.Top, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+c.plain)
		git(t, p.Top, "update-index", "--add", "--cacheinfo", "120000,"+blob+","+c.link)
		path, link, err := trackedHookPath(context.Background(), p.Top)
		if err != nil || !link || path != c.link {
			t.Errorf("%s beside a %s link: got %q, link %v, err %v", c.plain, c.link, path, link, err)
		}
	}
}

// With GIT_LITERAL_PATHSPECS set, git reads a ":(icase)" pathspec as a file's name; the check must
// still find a tracked link, or fail, never say nothing is tracked.
func TestTrackedHookLinkIsFoundWithLiteralPathspecs(t *testing.T) {
	p, _ := clone(t)
	outside := filepath.Join(t.TempDir(), "settings.json")
	commitLink(t, p.Top, ".Claude/settings.local.json", outside)
	t.Setenv("GIT_LITERAL_PATHSPECS", "1")
	path, _, err := trackedHookPath(context.Background(), p.Top)
	if err == nil && path == "" {
		t.Fatal("a tracked hook link went unseen with GIT_LITERAL_PATHSPECS set")
	}
	if _, _, err := removeHooks(context.Background(), p, nil); err == nil && exists(outside) {
		t.Errorf("the tracked link was written through:\n%s", read(t, outside))
	}
}
