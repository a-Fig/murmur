package clones

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
)

// The picker hands the page each clone's slug. A clone recorded before slugs were has none until
// its daemon next starts, and the list reads its slug from its remote meanwhile, so the list doesn't
// show some repos as owner/name and the rest as names.
func TestThePickerListsEachClonesSlug(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "record")
	clone := func(name, remote string) string {
		top := filepath.Join(root, name)
		if err := os.MkdirAll(top, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			if out, err := exec.Command("git", append([]string{"-C", top}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return filepath.Join(top, ".git")
	}
	a := clone("a", "git@github.com:a-Fig/murmur.git")
	b := clone("b", "https://gitlab.com/group/sub/old.git")
	c := clone("c", "/srv/plain.git")
	for _, rec := range []Clone{{GitDir: a, Name: "murmur", Slug: "a-Fig/murmur"}, {GitDir: b, Name: "old"}, {GitDir: c, Name: "plain"}} {
		if err := Add(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Picker{Dir: dir, Self: a, Joined: func(string) bool { return true }}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slugs := map[string]string{}
	for _, r := range got {
		slugs[r.Name] = r.Slug
	}
	want := map[string]string{"murmur": "a-Fig/murmur", "old": "group/sub/old", "plain": "plain"}
	for name, slug := range want {
		if slugs[name] != slug {
			t.Errorf("%s's slug is %q, want %q (all: %v)", name, slugs[name], slug, slugs)
		}
	}
}

// A slug the list had to ask git for is written to the record, so the next listing starts no git
// for it (#1164): the page lists the repos on every load.
func TestThePickerRecordsASlugItHadToAskFor(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "record")
	clone := func(name, remote string) string {
		top := filepath.Join(root, name)
		if err := os.MkdirAll(top, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
			if out, err := exec.Command("git", append([]string{"-C", top}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return filepath.Join(top, ".git")
	}
	self := clone("self", "git@github.com:a-Fig/murmur.git")
	old := clone("old", "https://gitlab.com/group/sub/old.git")
	for _, rec := range []Clone{{GitDir: self, Name: "murmur", Slug: "a-Fig/murmur"}, {GitDir: old, Name: "old"}} {
		if err := Add(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
	joined := func(string) bool { return true }
	if _, err := (Picker{Dir: dir, Self: self, Joined: joined}).List(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorded, err := List(dir, joined)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Clone{
		filepath.Clean(self): {GitDir: filepath.Clean(self), Name: "murmur", Slug: "a-Fig/murmur"},
		filepath.Clean(old):  {GitDir: filepath.Clean(old), Name: "old", Slug: "group/sub/old"},
	}
	if len(recorded) != len(want) {
		t.Errorf("the record has %d clones, want %d: %+v", len(recorded), len(want), recorded)
	}
	for _, c := range recorded {
		if c != want[c.GitDir] {
			t.Errorf("after a listing the record has %+v, want %+v", c, want[c.GitDir])
		}
	}

	// A clone that has left by the time git answers isn't put back with a slug.
	left := clone("left", "https://github.com/a-Fig/left.git")
	if err := Add(dir, Clone{GitDir: left, Name: "left"}); err != nil {
		t.Fatal(err)
	}
	// Joined when the list reads the record, and not when it is asked again before the write.
	var asked atomic.Int32
	gone := func(gitDir string) bool {
		return filepath.Clean(gitDir) != filepath.Clean(left) || asked.Add(1) == 1
	}
	all := Picker{Dir: dir, Self: self, Joined: gone}
	if _, err := all.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	every, err := List(dir, joined)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range every {
		if c.Name == "left" && c.Slug != "" {
			t.Errorf("a clone that left was written to the record again: %+v", c)
		}
	}
}

// Clones in the temporary folder are tests' and demos' fixtures, which a real page's picker leaves
// out (#1278); a page that is one of them lists them all, so tests and demos see their siblings.
func TestThePickerLeavesOutClonesInTheTemporaryFolder(t *testing.T) {
	root := t.TempDir()
	tmp := filepath.Join(root, "Temp")
	old := tempRoots
	tempRoots = func() []string { return []string{tmp} }
	t.Cleanup(func() { tempRoots = old })
	dir := filepath.Join(root, "record")
	clone := func(top string) string {
		gitDir := filepath.Join(top, ".git")
		if err := os.MkdirAll(gitDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := Add(dir, Clone{GitDir: gitDir, Name: filepath.Base(top), Slug: filepath.Base(top)}); err != nil {
			t.Fatal(err)
		}
		return gitDir
	}
	real := clone(filepath.Join(root, "work", "murmur"))
	clone(filepath.Join(root, "work", "cue"))
	fixture := clone(filepath.Join(tmp, "fixture", "policy-demo"))
	joined := func(string) bool { return true }
	names := func(self string) []string {
		got, err := Picker{Dir: dir, Self: self, Joined: joined}.List(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range got {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(real); !slices.Equal(got, []string{"cue", "murmur"}) {
		t.Errorf("a real clone's picker lists %v, want [cue murmur]", got)
	}
	if got := names(fixture); !slices.Equal(got, []string{"cue", "murmur", "policy-demo"}) {
		t.Errorf("a fixture's picker lists %v, want all three", got)
	}
}
