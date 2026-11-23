package clones

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, c := range recorded {
		if c.Name == "old" && c.Slug != "group/sub/old" {
			t.Errorf("after a listing the record has %+v, want its slug kept", c)
		}
		if c.Name == "old" && c.GitDir != filepath.Clean(old) {
			t.Errorf("the record's git dir changed to %q", c.GitDir)
		}
	}
	if len(recorded) != 2 {
		t.Errorf("the record has %d clones, want 2: %+v", len(recorded), recorded)
	}
}
