package attrib_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/attrib"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitEnv(t, dir, nil, args...)
}

// gitEnv is git with extra environment variables, such as GIT_COMMITTER_DATE.
func gitEnv(t *testing.T, dir string, extra []string, args ...string) string {
	t.Helper()
	args = append([]string{"-C", dir, "-c", "user.name=Attribution Test", "-c", "user.email=attrib@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.ToSlash(filepath.Join(dir, "no-hooks"))}, args...)
	cmd := exec.CommandContext(context.Background(), "git", args...)
	// Keep temporary repositories independent of the caller's git environment and config.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(env), "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	return dir
}

func commit(t *testing.T, dir, subject string) string {
	t.Helper()
	git(t, dir, "commit", "-q", "--allow-empty", "-m", subject)
	return git(t, dir, "rev-parse", "HEAD")
}

func head(t *testing.T, dir string) attrib.Ref {
	t.Helper()
	ref, err := attrib.Head(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func wantCommits(t *testing.T, dir string, before, after attrib.Ref, want ...attrib.Commit) {
	t.Helper()
	got, err := attrib.NewCommits(context.Background(), dir, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
		t.Fatalf("NewCommits = %#v, want %#v", got, want)
	}
}

func TestHead(t *testing.T) {
	dir := repo(t)
	ref := head(t, dir)
	if ref.SHA != "" || ref.Branch != "main" {
		t.Fatalf("unborn Head = %#v", ref)
	}
	sha := commit(t, dir, "root")
	ref = head(t, dir)
	if ref.SHA != sha || ref.Branch != "main" {
		t.Fatalf("main Head = %#v", ref)
	}
	linked := filepath.Join(t.TempDir(), "linked worktree")
	git(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
	linkedSHA := commit(t, linked, "linked commit")
	if got := head(t, linked); got.SHA != linkedSHA || got.Branch != "linked" {
		t.Fatalf("linked Head = %#v", got)
	}
	if got := head(t, dir); got.SHA != sha || got.Branch != "main" {
		t.Fatalf("main Head after linked commit = %#v", got)
	}
	git(t, linked, "checkout", "-q", "--detach", sha)
	if got := head(t, linked); got.SHA != sha || got.Branch != "" {
		t.Fatalf("detached Head = %#v", got)
	}
}

func TestNewCommits(t *testing.T) {
	t.Run("unborn and ordinary commits", func(t *testing.T) {
		dir := repo(t)
		before := head(t, dir)
		wantCommits(t, dir, before, before)
		first := commit(t, dir, "first")
		after := head(t, dir)
		wantCommits(t, dir, before, after, attrib.Commit{SHA: first, Subject: "first", Branch: "main"})
		second := commit(t, dir, "second: subject with\ta tab")
		third := commit(t, dir, "third — Unicode")
		wantCommits(t, dir, after, head(t, dir),
			attrib.Commit{SHA: second, Subject: "second: subject with\ta tab", Branch: "main"},
			attrib.Commit{SHA: third, Subject: "third — Unicode", Branch: "main"})
		wantCommits(t, dir, head(t, dir), head(t, dir))
	})
	t.Run("divergent branch switch", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		git(t, dir, "checkout", "-q", "-b", "topic")
		commit(t, dir, "pre-existing topic commit")
		git(t, dir, "checkout", "-q", "main")
		commit(t, dir, "main commit")
		before := head(t, dir)
		git(t, dir, "checkout", "-q", "topic")
		wantCommits(t, dir, before, head(t, dir))
		sha := commit(t, dir, "new topic commit")
		wantCommits(t, dir, before, head(t, dir), attrib.Commit{SHA: sha, Subject: "new topic commit", Branch: "topic"})
	})
	t.Run("new branch plus commit", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		before := head(t, dir)
		git(t, dir, "checkout", "-q", "-b", "new")
		sha := commit(t, dir, "new branch commit")
		wantCommits(t, dir, before, head(t, dir), attrib.Commit{SHA: sha, Subject: "new branch commit", Branch: "new"})
	})
	t.Run("amend", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		commit(t, dir, "original")
		before := head(t, dir)
		git(t, dir, "commit", "-q", "--amend", "--allow-empty", "-m", "amended")
		after := head(t, dir)
		wantCommits(t, dir, before, after, attrib.Commit{SHA: after.SHA, Subject: "amended", Branch: "main"})
	})
	t.Run("reset backwards", func(t *testing.T) {
		dir := repo(t)
		root := commit(t, dir, "root")
		commit(t, dir, "later")
		before := head(t, dir)
		git(t, dir, "reset", "-q", "--hard", root)
		wantCommits(t, dir, before, head(t, dir))
	})
	t.Run("rebase", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		git(t, dir, "checkout", "-q", "-b", "topic")
		commit(t, dir, "topic one")
		commit(t, dir, "topic two")
		git(t, dir, "checkout", "-q", "main")
		commit(t, dir, "upstream")
		git(t, dir, "checkout", "-q", "topic")
		before := head(t, dir)
		git(t, dir, "rebase", "--keep-empty", "main")
		after := head(t, dir)
		one := git(t, dir, "rev-parse", "HEAD^")
		// "upstream" was already on main, a tip in before, so the rebase does not credit it.
		wantCommits(t, dir, before, after,
			attrib.Commit{SHA: one, Subject: "topic one", Branch: "topic"},
			attrib.Commit{SHA: after.SHA, Subject: "topic two", Branch: "topic"})
	})
	t.Run("linked worktree", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		linked := filepath.Join(t.TempDir(), "linked")
		git(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
		before := head(t, linked)
		sha := commit(t, linked, "linked commit")
		commit(t, dir, "unrelated main commit")
		wantCommits(t, linked, before, head(t, linked), attrib.Commit{SHA: sha, Subject: "linked commit", Branch: "linked"})
	})
	t.Run("detached", func(t *testing.T) {
		dir := repo(t)
		commit(t, dir, "root")
		git(t, dir, "checkout", "-q", "--detach")
		before := head(t, dir)
		sha := commit(t, dir, "detached commit")
		wantCommits(t, dir, before, head(t, dir), attrib.Commit{SHA: sha, Subject: "detached commit"})
	})
}

// A call that starts in one worktree and is judged in another, detached there, credits only what
// it made there: not a commit that worktree's HEAD alone reached at the start, though its date
// falls in the slack (#60). The commit is dated a second ahead so that it does, however slow the
// test runs.
func TestAnotherWorktreesDetachedCommitIsNotCredited(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "root")
	other := filepath.Join(t.TempDir(), "other")
	git(t, dir, "worktree", "add", "-q", "--detach", other)
	soon := fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", time.Now().Add(time.Second).Unix())
	gitEnv(t, other, []string{soon}, "commit", "-q", "--allow-empty", "-m", "on no ref")
	before := head(t, dir)
	wantCommits(t, other, before, head(t, other))
	sha := commit(t, other, "during the call")
	wantCommits(t, other, before, head(t, other), attrib.Commit{SHA: sha, Subject: "during the call"})
}

func TestReadOnly(t *testing.T) {
	dir := repo(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("staged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "file")
	before := head(t, dir)
	commit(t, dir, "root")
	if err := os.WriteFile(file, []byte("staged change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "file")
	content := []byte("unstaged change\n")
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	indexPath := git(t, dir, "rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(dir, indexPath)
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	after := head(t, dir)
	wantCommits(t, dir, before, after, attrib.Commit{SHA: after.SHA, Subject: "root", Branch: "main"})
	for path, want := range map[string][]byte{file: content, indexPath: index} {
		got, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s changed: %v", path, err)
		}
	}
}

func TestSwitchToHistoryFromAnyRef(t *testing.T) {
	for _, kind := range []string{"remote", "lightweight tag", "annotated tag", "nested tag", "custom ref"} {
		t.Run(kind, func(t *testing.T) {
			dir := repo(t)
			commit(t, dir, "root")
			git(t, dir, "checkout", "-q", "-b", "source")
			old := commit(t, dir, "existing history")
			switch kind {
			case "remote":
				git(t, dir, "update-ref", "refs/remotes/origin/source", old)
			case "lightweight tag":
				git(t, dir, "tag", "saved", old)
			case "annotated tag", "nested tag":
				git(t, dir, "tag", "-a", "-m", "saved", "saved", old)
				if kind == "nested tag" {
					git(t, dir, "tag", "-a", "-m", "nested", "nested", "saved")
					git(t, dir, "tag", "-d", "saved")
				}
			case "custom ref":
				git(t, dir, "update-ref", "refs/custom/saved", old)
			}
			git(t, dir, "checkout", "-q", "main")
			git(t, dir, "branch", "-D", "source")
			before := head(t, dir)
			git(t, dir, "checkout", "-q", "-b", "new", old)
			wantCommits(t, dir, before, head(t, dir))
			sha := commit(t, dir, "new commit")
			wantCommits(t, dir, before, head(t, dir), attrib.Commit{SHA: sha, Subject: "new commit", Branch: "new"})
		})
	}
}

func TestNonCommitRefs(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "root")
	git(t, dir, "tag", "-a", "-m", "tree", "tree", "HEAD^{tree}")
	git(t, dir, "update-ref", "refs/custom/tree", git(t, dir, "rev-parse", "HEAD^{tree}"))
	before := head(t, dir)
	git(t, dir, "checkout", "-q", "-b", "topic")
	sha := commit(t, dir, "new commit")
	wantCommits(t, dir, before, head(t, dir), attrib.Commit{SHA: sha, Subject: "new commit", Branch: "topic"})
}

func TestErrors(t *testing.T) {
	dir := repo(t)
	commit(t, dir, "root")
	if _, err := attrib.Head(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Head outside repository succeeded")
	}
	// Refs from Head with a bad SHA swapped in, so each case reaches git rather than stopping
	// at NewCommits' check for a Ref that Head did not read.
	ref := head(t, dir)
	bad := ref
	bad.SHA = strings.Repeat("1", 40)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := attrib.Head(ctx, dir); err == nil {
		t.Fatal("Head with canceled context succeeded")
	}
	if _, err := attrib.NewCommits(ctx, dir, ref, ref); err == nil {
		t.Fatal("NewCommits with canceled context succeeded")
	}
	if _, err := attrib.NewCommits(context.Background(), dir, bad, ref); err == nil {
		t.Fatal("NewCommits with invalid before succeeded")
	}
	if _, err := attrib.NewCommits(context.Background(), dir, ref, bad); err == nil {
		t.Fatal("NewCommits with invalid after succeeded")
	}
}
