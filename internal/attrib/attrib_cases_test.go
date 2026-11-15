package attrib_test

// Cases written to grade three builds of this package (2026-10-29), kept because they pin real
// behaviour: many refs, clocks, rewrites, stash, detached and unborn HEADs, shallow clones and
// read-only snapshots. Their helpers carry a "case" prefix to stay apart from the other files'.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/attrib"
)

// caseEnv is the environment for every git the tests run. Identity and config travel in the
// environment, not -c flags, so the gits that rebase, pull and stash spawn inherit them too.
func caseEnv(extra []string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"GIT_AUTHOR_NAME=Attribution Case", "GIT_AUTHOR_EMAIL=case@example.invalid",
		"GIT_COMMITTER_NAME=Attribution Case", "GIT_COMMITTER_EMAIL=case@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		// gc.auto and maintenance.auto: after a commit, git may start maintenance in the background
		// and leave it writing the commit-graph after the test ends, when the test's directory is
		// being removed ("unlinkat …/objects/info/commit-graphs: directory not empty", TestManyBranches
		// on macOS CI, 2026-11-13).
		"GIT_CONFIG_COUNT=7",
		"GIT_CONFIG_KEY_0=commit.gpgsign", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=init.defaultBranch", "GIT_CONFIG_VALUE_1=main",
		"GIT_CONFIG_KEY_2=core.autocrlf", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.hooksPath", "GIT_CONFIG_VALUE_3="+filepath.ToSlash(filepath.Join(os.TempDir(), "murmur-attrib-no-hooks")),
		"GIT_CONFIG_KEY_4=advice.detachedHead", "GIT_CONFIG_VALUE_4=false",
		"GIT_CONFIG_KEY_5=gc.auto", "GIT_CONFIG_VALUE_5=0",
		"GIT_CONFIG_KEY_6=maintenance.auto", "GIT_CONFIG_VALUE_6=false",
	)
	return append(env, extra...)
}

func caseGitIn(t testing.TB, dir string, stdin string, extra []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = caseEnv(extra)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s%s", args, err, out.String(), errb.String())
	}
	return strings.TrimSpace(out.String())
}

func caseGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	return caseGitIn(t, dir, "", nil, args...)
}

// caseDated returns environment entries that date a commit's author and committer at when.
func caseDated(when time.Time) []string {
	d := fmt.Sprintf("@%d +0000", when.Unix())
	return []string{"GIT_COMMITTER_DATE=" + d, "GIT_AUTHOR_DATE=" + d}
}

// caseHourAgo dates a commit well before any command under test began.
func caseHourAgo() []string { return caseDated(time.Now().Add(-time.Hour)) }

func caseRepo(t testing.TB) string {
	t.Helper()
	d := t.TempDir()
	caseGit(t, d, "init", "-q", "-b", "main")
	return d
}

// caseCommit makes an empty commit; env may date it.
func caseCommit(t testing.TB, dir, subject string, env []string) string {
	t.Helper()
	caseGitIn(t, dir, "", env, "commit", "-q", "--allow-empty", "-m", subject)
	return caseGit(t, dir, "rev-parse", "HEAD")
}

func caseHead(t testing.TB, dir string) attrib.Ref {
	t.Helper()
	r, err := attrib.Head(context.Background(), dir)
	if err != nil {
		t.Fatalf("Head(%s): %s", dir, caseShort(err))
	}
	return r
}

// caseRun takes Head before and after action and returns what NewCommits credits.
func caseRun(t testing.TB, dir string, action func()) (before, after attrib.Ref, got []attrib.Commit) {
	t.Helper()
	before = caseHead(t, dir)
	action()
	after = caseHead(t, dir)
	got, err := attrib.NewCommits(context.Background(), dir, before, after)
	if err != nil {
		t.Fatalf("NewCommits: %s", caseShort(err))
	}
	return before, after, got
}

// caseShort keeps a failure readable: git's errors here quote the whole command line.
func caseShort(err error) string {
	s := err.Error()
	if len(s) > 400 {
		s = s[:200] + " ... " + s[len(s)-200:]
	}
	return s
}

func caseSubjects(cs []attrib.Commit) []string {
	var s []string
	for _, c := range cs {
		s = append(s, c.Subject)
	}
	return s
}

func caseWant(t testing.TB, got []attrib.Commit, subjects ...string) {
	t.Helper()
	if g, w := strings.Join(caseSubjects(got), "|"), strings.Join(subjects, "|"); g != w {
		t.Errorf("credited %q, want %q", caseSubjects(got), subjects)
	}
}

// caseFastImport writes n hour-old empty commits onto refs/heads/main in one process, and, when
// refFor is set, a ref at each: a branch, or an annotated tag when annotated is true.
func caseFastImport(t testing.TB, dir string, n int, refFor func(i int) string, annotated bool) {
	t.Helper()
	ts := time.Now().Add(-time.Hour).Unix()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		msg := fmt.Sprintf("old %d\n", i)
		fmt.Fprintf(&b, "commit refs/heads/main\nmark :%d\ncommitter U <u@example.invalid> %d +0000\ndata %d\n%s\n", i, ts, len(msg), msg)
	}
	if refFor != nil {
		for i := 1; i <= n; i++ {
			if annotated {
				fmt.Fprintf(&b, "tag %s\nfrom :%d\ntagger U <u@example.invalid> %d +0000\ndata 2\nt\n\n", refFor(i), i, ts)
			} else {
				fmt.Fprintf(&b, "reset %s\nfrom :%d\n\n", refFor(i), i)
			}
		}
	}
	caseGitIn(t, dir, b.String(), nil, "fast-import", "--quiet")
}

// An amend that changes nothing, in the same second as the original, rebuilds the same object.
func TestAmendNoChangeSameSecond(t *testing.T) {
	t.Run("original made earlier", func(t *testing.T) {
		d := caseRepo(t)
		caseCommit(t, d, "base", caseHourAgo())
		same := caseDated(time.Now())
		orig := caseCommit(t, d, "draft", same)
		before, after, got := caseRun(t, d, func() { caseGitIn(t, d, "", same, "commit", "-q", "--amend", "--allow-empty", "--no-edit") })
		if after.SHA != orig || before.SHA != orig {
			t.Fatalf("amend made a new object: %s -> %s", orig, after.SHA)
		}
		caseWant(t, got)
	})
	t.Run("original made by the command", func(t *testing.T) {
		d := caseRepo(t)
		caseCommit(t, d, "base", caseHourAgo())
		same := caseDated(time.Now())
		_, _, got := caseRun(t, d, func() {
			caseCommit(t, d, "draft", same)
			caseGitIn(t, d, "", same, "commit", "-q", "--amend", "--allow-empty", "--no-edit")
		})
		caseWant(t, got, "draft")
	})
}

// Rebasing commits a remote-tracking branch already reaches: the rewrites are new objects with
// new committer dates, so they are credited; the originals and the new base are not.
func TestRebaseOfPushedCommits(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	caseGit(t, root, "init", "-q", "--bare", "-b", "main", origin)
	d := filepath.Join(root, "dev")
	caseGit(t, root, "clone", "-q", origin, d)
	caseCommit(t, d, "base", caseHourAgo())
	caseGit(t, d, "push", "-q", "origin", "main")
	caseGit(t, d, "checkout", "-q", "-b", "feature")
	f1 := caseCommit(t, d, "f1", caseHourAgo())
	f2 := caseCommit(t, d, "f2", caseHourAgo())
	caseGit(t, d, "push", "-q", "-u", "origin", "feature")
	caseGit(t, d, "checkout", "-q", "main")
	caseCommit(t, d, "main-2", caseHourAgo())
	caseGit(t, d, "push", "-q", "origin", "main")
	caseGit(t, d, "checkout", "-q", "feature")
	_, after, got := caseRun(t, d, func() {
		caseGit(t, d, "rebase", "-q", "--keep-empty", "main")
		caseGit(t, d, "push", "-q", "--force", "origin", "feature")
	})
	caseWant(t, got, "f1", "f2")
	for _, c := range got {
		if c.SHA == f1 || c.SHA == f2 {
			t.Errorf("credited the pushed original %s", c.SHA)
		}
	}
	if len(got) == 2 && got[1].SHA != after.SHA {
		t.Errorf("last credited %s, want HEAD %s", got[1].SHA, after.SHA)
	}
}

// Stash writes commits under refs/stash, none reachable from HEAD.
func TestStashAndPop(t *testing.T) {
	d := caseRepo(t)
	if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	caseGit(t, d, "add", "f.txt")
	caseCommit(t, d, "base", caseHourAgo())
	os.WriteFile(filepath.Join(d, "f.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(d, "untracked.txt"), []byte("u\n"), 0o644)
	_, _, got := caseRun(t, d, func() {
		caseGit(t, d, "stash", "push", "-q", "-u")
		if caseGit(t, d, "rev-parse", "--verify", "-q", "refs/stash") == "" {
			t.Fatal("stash made no commit")
		}
		caseGit(t, d, "stash", "pop", "-q")
	})
	caseWant(t, got)
	// A stash left in place is no different.
	_, _, got = caseRun(t, d, func() { caseGit(t, d, "stash", "push", "-q", "-u") })
	caseWant(t, got)
}

// A detached HEAD that moves back: to an ancestor, which rule 2 drops, and to a commit no ref
// reaches any more, which only rule 3 drops.
func TestDetachedMovesToOldCommit(t *testing.T) {
	d := caseRepo(t)
	caseCommit(t, d, "c1", caseHourAgo())
	caseCommit(t, d, "c2", caseHourAgo())
	caseCommit(t, d, "c3", caseHourAgo())
	orphaned := caseCommit(t, d, "orphaned", caseHourAgo())
	caseGit(t, d, "reset", "-q", "--hard", "HEAD~1")
	caseGit(t, d, "checkout", "-q", "--detach")
	_, after, got := caseRun(t, d, func() { caseGit(t, d, "checkout", "-q", "--detach", "HEAD~2") })
	if after.Branch != "" {
		t.Fatalf("Branch = %q, want detached", after.Branch)
	}
	caseWant(t, got)
	_, _, got = caseRun(t, d, func() { caseGit(t, d, "checkout", "-q", "--detach", orphaned) })
	caseWant(t, got)
}

// Commits on a detached HEAD that no ref reaches, made in the same second as the snapshot: only
// before's own SHA keeps the earlier one from being credited to the next command.
func TestDetachedCommitsNoRefReaches(t *testing.T) {
	d := caseRepo(t)
	caseCommit(t, d, "base", caseHourAgo())
	caseGit(t, d, "checkout", "-q", "--detach")
	now := caseDated(time.Now())
	caseCommit(t, d, "earlier", now)
	_, _, got := caseRun(t, d, func() { caseCommit(t, d, "later", now) })
	caseWant(t, got, "later")
}

type caseFile struct {
	sum   [32]byte
	mod   time.Time
	mode  fs.FileMode
	isDir bool
}

// caseSnapshot records every path under dir, .git included, and each file's bytes and modification
// time.
func caseSnapshot(t testing.TB, dir string) map[string]caseFile {
	t.Helper()
	m := map[string]caseFile{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if e.IsDir() {
			// A directory's time on Windows comes from its parent's index, updated lazily, so
			// it can change between two reads with no write; created or removed entries show
			// up below as paths anyway.
			m[rel] = caseFile{isDir: true}
			return nil
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		f := caseFile{mod: info.ModTime(), mode: info.Mode()}
		{
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			f.sum = sha256.Sum256(b)
		}
		m[rel] = f
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// Head and NewCommits run while someone else is editing: staged, unstaged and untracked changes,
// with a file rewritten in the same second as the index, which a refresh would rewrite.
func TestReadOnlyWithUncommittedChanges(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(fmt.Sprintf("linked=%v", linked), func(t *testing.T) {
			d := caseRepo(t)
			for _, f := range []string{"a.txt", "b.txt"} {
				os.WriteFile(filepath.Join(d, f), []byte(f+"\n"), 0o644)
			}
			caseGit(t, d, "add", ".")
			caseCommit(t, d, "base", caseHourAgo())
			wt := d
			if linked {
				wt = filepath.Join(t.TempDir(), "wt")
				caseGit(t, d, "worktree", "add", "-q", "-b", "side", wt)
			}
			os.WriteFile(filepath.Join(wt, "a.txt"), []byte("staged\n"), 0o644)
			caseGit(t, wt, "add", "a.txt")
			os.WriteFile(filepath.Join(wt, "a.txt"), []byte("unstaged\n"), 0o644)
			os.WriteFile(filepath.Join(wt, "b.txt"), []byte("B.txt\n"), 0o644) // same size, racy
			os.WriteFile(filepath.Join(wt, "new.txt"), []byte("untracked\n"), 0o644)
			repoBefore, wtBefore := caseSnapshot(t, d), caseSnapshot(t, wt)
			before := caseHead(t, wt)
			after := caseHead(t, wt)
			if _, err := attrib.NewCommits(context.Background(), wt, before, after); err != nil {
				t.Fatal(err)
			}
			for name, pair := range map[string][2]map[string]caseFile{
				"repository": {repoBefore, caseSnapshot(t, d)},
				"worktree":   {wtBefore, caseSnapshot(t, wt)},
			} {
				was, now := pair[0], pair[1]
				for p, f := range was {
					if g, ok := now[p]; !ok {
						t.Errorf("%s: %s removed", name, p)
					} else if g != f {
						t.Errorf("%s: %s changed", name, p)
					}
				}
				for p := range now {
					if _, ok := was[p]; !ok {
						t.Errorf("%s: %s created", name, p)
					}
				}
			}
		})
	}
}

// A linked worktree on an orphan branch is unborn; its first commit is credited and nothing else.
func TestUnbornLinkedWorktree(t *testing.T) {
	d := caseRepo(t)
	caseCommit(t, d, "base", caseHourAgo())
	caseCommit(t, d, "main-2", caseHourAgo())
	wt := filepath.Join(t.TempDir(), "wt")
	caseGit(t, d, "worktree", "add", "-q", "--detach", wt)
	caseGit(t, wt, "checkout", "-q", "--orphan", "fresh")
	before, after, got := caseRun(t, wt, func() { caseCommit(t, wt, "first", nil) })
	if before.SHA != "" || before.Branch != "fresh" {
		t.Errorf("before = %q on %q, want unborn on fresh", before.SHA, before.Branch)
	}
	caseWant(t, got, "first")
	if len(got) == 1 && (got[0].Branch != "fresh" || got[0].SHA != after.SHA) {
		t.Errorf("credited %+v, want HEAD on fresh", got[0])
	}
	if m := caseHead(t, d); m.Branch != "main" {
		t.Errorf("main worktree Branch = %q", m.Branch)
	}
}

// An orphan branch made from a branch with history: its root commit is credited, and switching
// back to the old branch credits nothing.
func TestOrphanBranch(t *testing.T) {
	d := caseRepo(t)
	caseCommit(t, d, "c1", caseHourAgo())
	caseCommit(t, d, "c2", caseHourAgo())
	_, _, got := caseRun(t, d, func() {
		caseGit(t, d, "checkout", "-q", "--orphan", "fresh")
		caseCommit(t, d, "new root", nil)
	})
	caseWant(t, got, "new root")
	_, _, got = caseRun(t, d, func() { caseCommit(t, d, "next", nil) })
	caseWant(t, got, "next")
	_, _, got = caseRun(t, d, func() { caseGit(t, d, "checkout", "-q", "main") })
	caseWant(t, got)
}

// A shallow clone: a commit on it, deepening it while committing, and pulling into it.
func TestShallowClone(t *testing.T) {
	up := caseRepo(t)
	for i := 1; i <= 5; i++ {
		caseCommit(t, up, fmt.Sprintf("up %d", i), caseHourAgo())
	}
	d := filepath.Join(t.TempDir(), "shallow")
	caseGit(t, up, "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(up), d)
	if caseGit(t, d, "rev-parse", "--is-shallow-repository") != "true" {
		t.Fatal("clone is not shallow")
	}
	_, _, got := caseRun(t, d, func() { caseCommit(t, d, "mine", nil) })
	caseWant(t, got, "mine")
	_, _, got = caseRun(t, d, func() {
		caseGit(t, d, "fetch", "-q", "--unshallow")
		caseCommit(t, d, "mine 2", nil)
	})
	caseWant(t, got, "mine 2")

	d2 := filepath.Join(t.TempDir(), "shallow2")
	caseGit(t, up, "clone", "-q", "--depth", "1", "file://"+filepath.ToSlash(up), d2)
	caseCommit(t, up, "up 6", caseHourAgo())
	_, _, got = caseRun(t, d2, func() { caseGit(t, d2, "pull", "-q", "--ff-only") })
	caseWant(t, got)
}

// Windows caps a command line near 32K characters. 2,000 branches at distinct commits are
// about 82K of "^sha" arguments if every tip goes on the command line.
func TestManyBranches(t *testing.T) {
	d := caseRepo(t)
	caseFastImport(t, d, 2000, func(i int) string { return fmt.Sprintf("refs/heads/many/b%04d", i) }, false)
	_, _, got := caseRun(t, d, func() { caseCommit(t, d, "new", nil) })
	caseWant(t, got, "new")
}

// Release tags are annotated. A repo with 2,000 of them must not make Head slow: it runs before
// and after every shell command an agent runs.
func TestManyAnnotatedTags(t *testing.T) {
	d := caseRepo(t)
	caseFastImport(t, d, 2000, func(i int) string { return fmt.Sprintf("v0.%d", i) }, true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	before, err := attrib.Head(ctx, d)
	if err != nil {
		t.Fatalf("Head with 2,000 annotated tags, after %v: %s", time.Since(start), caseShort(err))
	}
	t.Logf("Head with 2,000 annotated tags took %v", time.Since(start))
	caseCommit(t, d, "new", nil)
	after := caseHead(t, d)
	got, err := attrib.NewCommits(ctx, d, before, after)
	if err != nil {
		t.Fatalf("NewCommits: %s", caseShort(err))
	}
	caseWant(t, got, "new")
}

// A teammate whose clock runs an hour ahead pushes; the command pulls. Rule 3 alone lets the
// fetched commit through, since its date is after the snapshot.
func TestFutureDatedFetch(t *testing.T) {
	t.Run("fast-forward", func(t *testing.T) {
		up := caseRepo(t)
		caseCommit(t, up, "base", caseHourAgo())
		d := filepath.Join(t.TempDir(), "clone")
		caseGit(t, up, "clone", "-q", up, d)
		caseCommit(t, up, "from the future", caseDated(time.Now().Add(time.Hour)))
		_, _, got := caseRun(t, d, func() { caseGit(t, d, "pull", "-q", "--ff-only") })
		caseWant(t, got)
	})
	t.Run("merge", func(t *testing.T) {
		up := caseRepo(t)
		caseCommit(t, up, "base", caseHourAgo())
		d := filepath.Join(t.TempDir(), "clone")
		caseGit(t, up, "clone", "-q", up, d)
		caseCommit(t, d, "local", caseHourAgo())
		caseCommit(t, up, "from the future", caseDated(time.Now().Add(time.Hour)))
		_, _, got := caseRun(t, d, func() { caseGit(t, d, "pull", "-q", "--no-rebase", "--no-edit") })
		if len(got) != 1 || !strings.HasPrefix(got[0].Subject, "Merge") {
			t.Errorf("credited %q, want only the merge commit", caseSubjects(got))
		}
	})
}
