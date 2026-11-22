package daemon

import (
	"context"
	"io"
	"log"
	"maps"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
)

// A real repair's snapshot is the only record of names whose assignments it replaced. A root merged in
// on top of it leaves that history with two roots, where no snapshot counts, so a reader new to the
// registry would never learn those names and could give one out again. A daemon already reading the
// registry sees the merge bring in a root, reads it as damage, and writes a repair: one new root with
// every name it knows. A reader new to the registry then knows them all, and the merged-in names none.
func TestARootMergedInIsRepaired(t *testing.T) {
	d, remote := namedDaemon(t)
	ctx := context.Background()
	fresh := freshNames(t, 73, 2)
	kept, injected := fresh[0], fresh[1]
	push := func(sha string) {
		t.Helper()
		runGit(t, t.TempDir(), "--git-dir", d.opt.GitDir, "push", "-q", "--force", remote, sha+":"+brand.NamesRef)
		if err := d.repo.UpdateLocal(ctx, brand.NamesRef, sha); err != nil {
			t.Fatal(err)
		}
		d.advMu.Lock()
		d.processNames(ctx, sha)
		d.advMu.Unlock()
	}
	root := func(used map[string]string) string {
		t.Helper()
		rec := bus.NameRecord{V: bus.Version, At: time.Now().UTC(), Used: used}
		msg, err := bus.Encode(rec, rec.Subject())
		if err != nil {
			t.Fatal(err)
		}
		sha, err := d.repo.NewCommit(ctx, "", msg)
		if err != nil {
			t.Fatal(err)
		}
		return sha
	}

	// A repair, as a daemon writes one: a new root with every name in use, kept among them.
	used := maps.Clone(d.st.Used)
	used[kept] = ""
	repair := root(used)
	push(repair)
	if _, ok := d.st.Used[kept]; !ok || d.st.NamesRepair {
		t.Fatalf("the repair wasn't read (knows %s: %v), or left one to make (%v)", kept, ok, d.st.NamesRepair)
	}

	tree, err := d.repo.EmptyTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	merge := strings.TrimSpace(runGit(t, t.TempDir(), "--git-dir", d.opt.GitDir, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit-tree", tree, "-p", repair, "-p", root(map[string]string{injected: ""}), "-m", "merge"))
	push(merge)
	if !d.st.NamesRepair {
		t.Fatal("a root merged in over a repair wasn't taken for damage to repair")
	}
	if err := d.pushNames(ctx, d.nextNames()); err != nil {
		t.Fatal(err)
	}

	// A device new to the registry.
	clone := filepath.Join(t.TempDir(), "clone")
	runGit(t, t.TempDir(), "clone", "-q", "--no-local", remote, clone)
	opt := Options{GitDir: filepath.Join(clone, ".git"), Log: log.New(io.Discard, "", 0), NamePool: -1}
	opt.defaults()
	reader, err := open(ctx, opt, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.repo.Fetch(ctx, []string{brand.NamesRef}); err != nil {
		t.Fatal(err)
	}
	local, err := reader.repo.LocalRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if local[brand.NamesRef] == merge {
		t.Fatal("the repair wasn't pushed over the merge")
	}
	reader.advMu.Lock()
	reader.processNames(ctx, local[brand.NamesRef])
	reader.advMu.Unlock()
	if _, ok := reader.st.Used[kept]; !ok {
		t.Errorf("a reader new to the repaired registry doesn't know %s", kept)
	}
	if _, ok := reader.st.Used[injected]; ok {
		t.Errorf("a reader new to the repaired registry took %s, from the root merged in, for used", injected)
	}
}
