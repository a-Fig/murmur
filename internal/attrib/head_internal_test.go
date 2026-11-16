package attrib

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Head reads HEAD from the worktree's git directory where it can, and asks git where it can't.
// Both ways must give the same Ref.

func hgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(env), "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sameHead fails unless Head, and Head the long way, agree in dir.
func sameHead(t *testing.T, what, dir string) Ref {
	t.Helper()
	fast, err := Head(context.Background(), dir)
	if err != nil {
		t.Fatalf("%s: Head: %v", what, err)
	}
	slow, err := headByGit(context.Background(), dir, Ref{Time: time.Now()})
	if err != nil {
		t.Fatalf("%s: headByGit: %v", what, err)
	}
	if fast.SHA != slow.SHA || fast.Branch != slow.Branch || !reflect.DeepEqual(fast.tips, slow.tips) {
		t.Errorf("%s: Head = %q on %q with %d tips, git says %q on %q with %d tips", what,
			fast.SHA, fast.Branch, len(fast.tips), slow.SHA, slow.Branch, len(slow.tips))
	}
	return fast
}

func TestHeadReadsTheSameAsGit(t *testing.T) {
	dir := t.TempDir()
	hgit(t, dir, "init", "-q", "-b", "main")
	if r := sameHead(t, "unborn", dir); r.gitDir == "" {
		t.Error("an ordinary repository's HEAD was read by git")
	}
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	sameHead(t, "on a branch", dir)
	os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755)
	sameHead(t, "in a subdirectory", filepath.Join(dir, "sub", "deeper"))
	hgit(t, dir, "tag", "-a", "-m", "t", "v1")
	hgit(t, dir, "switch", "-q", "-c", "feature/with/slashes")
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "on feature")
	hgit(t, dir, "pack-refs", "--all")
	sameHead(t, "on a packed branch", dir)

	linked := filepath.Join(t.TempDir(), "linked worktree")
	hgit(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
	hgit(t, linked, "commit", "-q", "--allow-empty", "-m", "linked")
	if r := sameHead(t, "linked worktree", linked); r.gitDir == "" {
		t.Error("a linked worktree's HEAD was read by git")
	}
	hgit(t, linked, "switch", "-q", "--detach", "HEAD~1")
	sameHead(t, "detached linked worktree", linked)
	hgit(t, linked, "switch", "-q", "--orphan", "fresh")
	sameHead(t, "orphan in a linked worktree", linked)
	sameHead(t, "main worktree beside them", dir)

	// A branch that is itself a symbolic ref: git names the branch it leads to.
	hgit(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/feature/with/slashes")
	hgit(t, dir, "symbolic-ref", "HEAD", "refs/heads/alias")
	sameHead(t, "on a branch that is a symbolic ref", dir)
	hgit(t, dir, "symbolic-ref", "HEAD", "refs/heads/feature/with/slashes")
	hgit(t, dir, "symbolic-ref", "--delete", "refs/heads/alias")

	// A ref at a tree is a tip either way, which git log passes over.
	hgit(t, dir, "update-ref", "refs/custom/tree", hgit(t, dir, "rev-parse", "HEAD^{tree}"))
	sameHead(t, "with a ref at a tree", dir)

	// HEAD at a branch that points at a tree. Reading HEAD itself, Head takes the branch's object
	// as a commit unchecked, as it does a detached HEAD file's, and NewCommits finds nothing in it;
	// git, asked, refuses.
	odd := t.TempDir()
	hgit(t, odd, "init", "-q", "-b", "main")
	hgit(t, odd, "commit", "-q", "--allow-empty", "-m", "root")
	// git won't write a tree to a branch, so the ref is written by hand.
	tree := hgit(t, odd, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(filepath.Join(odd, ".git", "refs", "heads", "treeish"), []byte(tree+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hgit(t, odd, "symbolic-ref", "HEAD", "refs/heads/treeish")
	if r, err := Head(context.Background(), odd); err != nil || r.SHA != tree {
		t.Errorf("Head at a branch at a tree = %+v, %v; want the tree's ID", r, err)
	} else if commits, err := NewCommits(context.Background(), odd, r, r); err != nil || len(commits) != 0 {
		t.Errorf("NewCommits at a branch at a tree = %v, %v; want none", commits, err)
	}
	if _, err := headByGit(context.Background(), odd, Ref{Time: time.Now()}); err == nil {
		t.Error("git read HEAD at a tree as a commit")
	}

	// A reftable repository's HEAD file is a placeholder: git reads it.
	rt := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main", "--ref-format=reftable", rt)
	if cmd.Run() == nil {
		hgit(t, rt, "commit", "-q", "--allow-empty", "-m", "root")
		if r := sameHead(t, "reftable", rt); r.gitDir != "" || r.SHA == "" {
			t.Errorf("a reftable repository's HEAD: %+v", r)
		}
	}

	// GIT_DIR changes how git finds the repository: git is asked.
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	if r := sameHead(t, "with GIT_DIR", linked); r.gitDir != "" {
		t.Error("Head read HEAD itself with GIT_DIR set")
	}
}

// gitStarts counts the git processes a GIT_TRACE2_EVENT file recorded.
func gitStarts(t *testing.T, trace string) int {
	t.Helper()
	b, err := os.ReadFile(trace)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), `"event":"start"`)
}

// Head runs one git process where it reads HEAD itself, and finding the commits after it one more:
// a tool call that could commit waits for the first before it runs, and on Windows a git process
// costs a tenth to a third of a second (#21).
func TestHeadRunsOneGitProcess(t *testing.T) {
	dir := t.TempDir()
	hgit(t, dir, "init", "-q", "-b", "main")
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	linked := filepath.Join(t.TempDir(), "wt")
	hgit(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
	trace := filepath.Join(t.TempDir(), "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	for _, wt := range []string{dir, linked} {
		os.Remove(trace)
		before, err := Head(context.Background(), wt)
		if err != nil {
			t.Fatal(err)
		}
		if n := gitStarts(t, trace); n != 1 {
			t.Errorf("Head in %s ran %d git processes, want 1", wt, n)
		}
		hgit(t, wt, "commit", "-q", "--allow-empty", "-m", "during in "+filepath.Base(wt))
		os.Remove(trace)
		after, err := Head(context.Background(), wt)
		if err != nil {
			t.Fatal(err)
		}
		commits, err := NewCommits(context.Background(), wt, before, after)
		if err != nil || len(commits) != 1 {
			t.Fatalf("NewCommits = %v, %v; before %+v after %+v", commits, err, before, after)
		}
		if n := gitStarts(t, trace); n != 2 {
			t.Errorf("Head and NewCommits after a call in %s ran %d git processes, want 2", wt, n)
		}
	}
}

// Every worktree's detached HEAD is a tip, read from its HEAD file or by git: for-each-ref lists
// none, and a call judged in another worktree than the one it started in would otherwise count
// what only that HEAD reaches as new (#60).
func TestDetachedHeadsOfEveryWorktreeAreTips(t *testing.T) {
	dir := t.TempDir()
	hgit(t, dir, "init", "-q", "-b", "main")
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	other := filepath.Join(t.TempDir(), "other")
	hgit(t, dir, "worktree", "add", "-q", "--detach", other)
	hgit(t, other, "commit", "-q", "--allow-empty", "-m", "on no ref")
	linkedOnly := hgit(t, other, "rev-parse", "HEAD")
	hgit(t, dir, "switch", "-q", "--detach")
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "on no ref either")
	mainOnly := hgit(t, dir, "rev-parse", "HEAD")
	third := filepath.Join(t.TempDir(), "third")
	hgit(t, dir, "worktree", "add", "-q", "-b", "third", third)

	for _, wt := range []string{third, other, dir} {
		fast := sameHead(t, "worktree "+filepath.Base(wt), wt)
		if fast.gitDir == "" {
			t.Errorf("%s: HEAD was read by git", wt)
		}
		for _, sha := range []string{linkedOnly, mainOnly} {
			if !slices.Contains(fast.tips, sha) {
				t.Errorf("Head in %s: the detached HEAD at %s is not among the tips %v", wt, sha, fast.tips)
			}
		}
	}
}

// A tip goes to NewCommits's git log as a revision, so only a full object ID is kept: not the
// all-zero ID a new worktree's HEAD holds for a moment under git before 2.44, and not a short or
// malformed line, which git would read as an expression or refuse.
func TestAddTipsKeepsOnlyObjectIDs(t *testing.T) {
	sha1, sha256 := strings.Repeat("a", 40), strings.Repeat("b", 64)
	var ref Ref
	addTips(&ref, []string{strings.Repeat("0", 40), strings.Repeat("0", 64), sha256, "abc1234",
		strings.Repeat("A", 40), sha1 + "^!", "HEAD~1", "", sha1[:39] + "g", sha1})
	if want := []string{sha1, sha256}; !slices.Equal(ref.tips, want) {
		t.Fatalf("tips = %v, want %v", ref.tips, want)
	}
}

// A worktree's HEAD file can name an object the clone lacks, from a corrupt or hand-copied entry,
// or an ID of the other hash's length. As a tip it made NewCommits's git log fail with "bad object"
// or "bad revision", so no call in any worktree of the clone was credited (#540). A tree or blob
// ID git already passed over. Each must leave a commit made in another worktree credited, whether
// Head reads the HEAD files itself or asks git.
func TestTipsThatAreNoCommitsHereStillCredit(t *testing.T) {
	dir := t.TempDir()
	hgit(t, dir, "init", "-q", "-b", "main")
	hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
	other := filepath.Join(t.TempDir(), "other")
	hgit(t, dir, "worktree", "add", "-q", "-b", "other", other)
	tree := hgit(t, dir, "rev-parse", "HEAD^{tree}")
	file := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(file, []byte("a blob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob := hgit(t, dir, "hash-object", "-w", file)
	missing := strings.Repeat("1", 40)
	heads := map[string]string{
		dir:   filepath.Join(dir, ".git", "HEAD"),
		other: filepath.Join(dir, ".git", "worktrees", "other", "HEAD"),
	}
	cases := []struct {
		name, id string
		corrupt  string // the worktree whose HEAD file is overwritten; the commit is made in the other
		isTip    bool   // whether both ways of reading HEADs record id as a tip
	}{
		{"a missing commit in a linked worktree", missing, other, true},
		{"a missing commit in the main worktree", missing, dir, true},
		{"an ID of the other hash's length", strings.Repeat("2", 64), other, false},
		{"a tree", tree, other, true},
		{"a blob", blob, other, true},
	}
	paths := []struct {
		name string
		head func(string) (Ref, error)
	}{
		{"read from the files", func(wt string) (Ref, error) { return Head(context.Background(), wt) }},
		{"read by git", func(wt string) (Ref, error) {
			return headByGit(context.Background(), wt, Ref{Time: time.Now()})
		}},
	}
	for _, c := range cases {
		for _, p := range paths {
			t.Run(c.name+", "+p.name, func(t *testing.T) {
				saved, err := os.ReadFile(heads[c.corrupt])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(heads[c.corrupt], []byte(c.id+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				defer os.WriteFile(heads[c.corrupt], saved, 0o644)
				wt := dir
				if c.corrupt == dir {
					wt = other
				}
				before, err := p.head(wt)
				if err != nil {
					t.Fatal(err)
				}
				if c.isTip && !slices.Contains(before.tips, c.id) {
					t.Fatalf("%s is not among the tips %v, so this tests nothing", c.id, before.tips)
				}
				// The subtests share one repository, and the main-worktree case commits on other,
				// still at root, the way the linked case before it committed on main. With the
				// same message in the same second, git makes the same commit, already on main, so
				// rule 2 rightly drops it. Each subtest's own message keeps its commit new.
				hgit(t, wt, "commit", "-q", "--allow-empty", "-m", "during: "+c.name+", "+p.name)
				sha := hgit(t, wt, "rev-parse", "HEAD")
				after, err := p.head(wt)
				if err != nil {
					t.Fatal(err)
				}
				commits, err := NewCommits(context.Background(), wt, before, after)
				if err != nil {
					t.Fatalf("NewCommits: %v", err)
				}
				if len(commits) != 1 || commits[0].SHA != sha {
					t.Fatalf("NewCommits = %+v, want only %s", commits, sha)
				}
			})
		}
	}
}

// A ref can name an object the clone lacks: a branch whose commit was lost, or an annotated tag
// whose tag object was. Head's for-each-ref read each ref's object type, and died on such a ref with
// "missing object", so Head failed in every worktree of the clone and no call was credited (#540).
// Each case must leave a commit made on another branch credited, in files and reftable
// repositories, with the refs loose and packed, whether Head reads HEAD itself or asks git. An
// annotated tag whose commit is lost already passed, and is here to keep it so.
func TestRefsAtObjectsTheCloneLacksStillCredit(t *testing.T) {
	for _, format := range []string{"files", "reftable"} {
		for _, lost := range []string{"a branch's commit", "a tag object", "a tag's commit"} {
			for _, packed := range []bool{false, true} {
				name := format + ", " + lost + ", loose"
				if packed {
					name = format + ", " + lost + ", packed"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					// --ref-format came in git 2.45, so the files cases leave it out and still run
					// on the older gits mur supports.
					args := []string{"init", "-q", "-b", "main"}
					if format == "reftable" {
						args = append(args, "--ref-format=reftable")
					}
					if exec.Command("git", append(args, dir)...).Run() != nil {
						t.Skip("this git has no reftable ref format")
					}
					hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "root")
					var gone, tip string
					switch lost {
					case "a branch's commit":
						hgit(t, dir, "switch", "-q", "-c", "side")
						hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "lost")
						gone = hgit(t, dir, "rev-parse", "HEAD")
						tip = gone
					case "a tag object":
						hgit(t, dir, "tag", "-a", "-m", "lost", "v1")
						gone = hgit(t, dir, "rev-parse", "v1")
						tip = gone
					case "a tag's commit":
						hgit(t, dir, "switch", "-q", "--detach")
						hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "lost")
						gone = hgit(t, dir, "rev-parse", "HEAD")
						hgit(t, dir, "tag", "-a", "-m", "kept", "v1")
						tip = hgit(t, dir, "rev-parse", "v1")
					}
					hgit(t, dir, "switch", "-q", "main")
					if packed {
						hgit(t, dir, "pack-refs", "--all")
					}
					loose := filepath.Join(dir, ".git", "objects", gone[:2], gone[2:])
					os.Chmod(loose, 0o644) // git writes objects read-only, which Windows won't delete
					if err := os.Remove(loose); err != nil {
						t.Fatal(err)
					}
					for _, p := range []struct {
						name string
						head func(string) (Ref, error)
					}{
						{"read from the files", func(wt string) (Ref, error) { return Head(context.Background(), wt) }},
						{"read by git", func(wt string) (Ref, error) {
							return headByGit(context.Background(), wt, Ref{Time: time.Now()})
						}},
					} {
						before, err := p.head(dir)
						if err != nil {
							t.Fatalf("%s: Head: %v", p.name, err)
						}
						if !slices.Contains(before.tips, tip) {
							t.Fatalf("%s: %s is not among the tips %v, so this tests nothing", p.name, tip, before.tips)
						}
						hgit(t, dir, "commit", "-q", "--allow-empty", "-m", "during, "+p.name)
						sha := hgit(t, dir, "rev-parse", "HEAD")
						after, err := p.head(dir)
						if err != nil {
							t.Fatalf("%s: Head after: %v", p.name, err)
						}
						commits, err := NewCommits(context.Background(), dir, before, after)
						if err != nil {
							t.Fatalf("%s: NewCommits: %v", p.name, err)
						}
						if len(commits) != 1 || commits[0].SHA != sha {
							t.Fatalf("%s: NewCommits = %+v, want only %s", p.name, commits, sha)
						}
					}
				})
			}
		}
	}
}

// detachedHeadFiles reads its HEAD files several at a time, so give it more entries than readers,
// on and off branches, and some it can't read, and check it finds exactly the detached ones.
func TestDetachedHeadFilesReadsEveryEntry(t *testing.T) {
	common := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	main := strings.Repeat("a", 40)
	write(filepath.Join(common, "HEAD"), main+"\n")
	want := []string{main}
	for i := range 3*headReaders + 1 {
		dir := filepath.Join(common, "worktrees", "w"+strconv.Itoa(i))
		switch i % 4 {
		case 0:
			sha := fmt.Sprintf("%040x", i+1)
			write(filepath.Join(dir, "HEAD"), sha+"\n")
			want = append(want, sha)
		case 1:
			write(filepath.Join(dir, "HEAD"), "ref: refs/heads/b"+strconv.Itoa(i)+"\n")
		case 2:
			write(filepath.Join(dir, "gitdir"), "gone\n") // no HEAD file
		case 3:
			write(filepath.Join(dir, "HEAD"), "not a head\n")
		}
	}
	got := detachedHeadFiles(common)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("detachedHeadFiles = %v, want %v", got, want)
	}
}
