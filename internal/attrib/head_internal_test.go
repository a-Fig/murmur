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

	// A tree tag and a ref at a tree are no tips.
	hgit(t, dir, "update-ref", "refs/custom/tree", hgit(t, dir, "rev-parse", "HEAD^{tree}"))
	sameHead(t, "with a ref at a tree", dir)

	// HEAD at a branch that points at a tree: git is asked, and refuses, as before.
	odd := t.TempDir()
	hgit(t, odd, "init", "-q", "-b", "main")
	hgit(t, odd, "commit", "-q", "--allow-empty", "-m", "root")
	// git won't write a tree to a branch, so the ref is written by hand.
	tree := hgit(t, odd, "rev-parse", "HEAD^{tree}")
	if err := os.WriteFile(filepath.Join(odd, ".git", "refs", "heads", "treeish"), []byte(tree+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hgit(t, odd, "symbolic-ref", "HEAD", "refs/heads/treeish")
	if _, err := Head(context.Background(), odd); err == nil {
		t.Error("HEAD at a tree read as a commit")
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

// A new worktree's HEAD holds the all-zero ID for a moment under git before 2.44; it is no
// commit, and as a tip it would make NewCommits's git log fail.
func TestAddTipsSkipsTheZeroID(t *testing.T) {
	var ref Ref
	addTips(&ref, []string{strings.Repeat("0", 40), strings.Repeat("a", 40)})
	if len(ref.tips) != 1 || ref.tips[0] != strings.Repeat("a", 40) {
		t.Fatalf("tips = %v, want only the real one", ref.tips)
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
