package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// g runs git in dir and returns its output, trimmed.
func g(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteWithClone makes a bare remote whose main has an AGENTS.md, a seed repo that pushes to it as
// a teammate would, and a clone of it. Push makes its commit as whoever git says the user is.
func remoteWithClone(t *testing.T) (remote, seed, clone string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	root := t.TempDir()
	remote, seed, clone = filepath.Join(root, "remote.git"), filepath.Join(root, "seed"), filepath.Join(root, "clone")
	g(t, root, "init", "-q", "--bare", "--initial-branch=main", remote)
	g(t, root, "init", "-q", "--initial-branch=main", seed)
	if err := os.WriteFile(filepath.Join(seed, "AGENTS.md"), []byte("# Notes\nkeep this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g(t, seed, "add", "AGENTS.md")
	g(t, seed, "commit", "-q", "-m", "first")
	g(t, seed, "remote", "add", "origin", remote)
	g(t, seed, "push", "-q", "origin", "main")
	g(t, root, "clone", "-q", remote, clone)
	top, err := Top(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	return remote, seed, top
}

func push(t *testing.T, dir string) Pushed {
	t.Helper()
	p, err := Push(context.Background(), dir)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	return p
}

// state is what a person working in a checkout would see: its branch, its index, its changes and
// the files init edits.
func state(t *testing.T, dir string) string {
	t.Helper()
	s := g(t, dir, "rev-parse", "HEAD") + "\n" + g(t, dir, "ls-files", "-s") + "\n" + g(t, dir, "status", "--porcelain") + "\n"
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "work.txt"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		s += name + ":" + string(b) + "\n"
	}
	return s
}

// Run from another branch with work in progress, init lands its commit on the remote's main, on top
// of what was there, and the checkout it ran in is exactly as it was.
func TestPushLandsOnTheDefaultBranchAndLeavesTheCheckoutAlone(t *testing.T) {
	remote, _, clone := remoteWithClone(t)
	before := g(t, remote, "rev-parse", "main")
	g(t, clone, "switch", "-q", "-c", "feature")
	os.WriteFile(filepath.Join(clone, "work.txt"), []byte("staged\n"), 0o644)
	g(t, clone, "add", "work.txt")
	os.WriteFile(filepath.Join(clone, "work.txt"), []byte("staged, then edited\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# Notes\nmid-edit\n"), 0o644)
	was := state(t, clone)

	p := push(t, clone)
	if p.Remote != "origin" || p.Branch != "main" || p.FastForwarded || !strings.Contains(p.Checkout, "isn't on main") {
		t.Errorf("pushed %+v", p)
	}
	if got := g(t, remote, "rev-parse", "main"); got != p.SHA {
		t.Fatalf("remote main is %s, want the commit init made, %s", got, p.SHA)
	}
	if parent := g(t, remote, "rev-parse", "main^"); parent != before {
		t.Errorf("init's commit sits on %s, not on main's tip %s", parent, before)
	}
	agents := g(t, remote, "show", "main:AGENTS.md")
	if !strings.HasPrefix(agents, "# Notes\nkeep this\n\n"+markStart) {
		t.Errorf("AGENTS.md on main:\n%s", agents)
	}
	if claude := g(t, remote, "show", "main:CLAUDE.md"); claude+"\n" != section {
		t.Errorf("CLAUDE.md on main:\n%s", claude)
	}
	if len(p.Changed) != 2 {
		t.Errorf("changed %q", p.Changed)
	}
	if now := state(t, clone); now != was {
		t.Errorf("the checkout changed:\nbefore\n%s\nafter\n%s", was, now)
	}
	if got := g(t, clone, "rev-parse", "refs/remotes/origin/main"); got != p.SHA {
		t.Errorf("the remote-tracking main is %s, not the pushed commit", got)
	}
}

// Run on main with nothing changed, init moves the checkout to its commit, so the next pull isn't
// refused over the files it edited.
func TestPushFastForwardsACheckoutOnTheDefaultBranch(t *testing.T) {
	_, _, clone := remoteWithClone(t)
	p := push(t, clone)
	if !p.FastForwarded || p.Checkout != "" {
		t.Fatalf("pushed %+v", p)
	}
	if head := g(t, clone, "rev-parse", "HEAD"); head != p.SHA {
		t.Errorf("HEAD is %s, want %s", head, p.SHA)
	}
	if status := g(t, clone, "status", "--porcelain"); status != "" {
		t.Errorf("status after the fast-forward:\n%s", status)
	}
	if !markedTree(clone) {
		t.Error("the working tree doesn't have the section")
	}
}

// A local change to a file init edits keeps the checkout where it was, even on main.
func TestPushLeavesACheckoutWithChangesToItsFiles(t *testing.T) {
	_, _, clone := remoteWithClone(t)
	os.WriteFile(filepath.Join(clone, "AGENTS.md"), []byte("# Notes\nmine\n"), 0o644)
	head := g(t, clone, "rev-parse", "HEAD")
	p := push(t, clone)
	if p.FastForwarded || !strings.Contains(p.Checkout, "local changes") {
		t.Errorf("pushed %+v", p)
	}
	if g(t, clone, "rev-parse", "HEAD") != head || read(t, clone, "AGENTS.md") != "# Notes\nmine\n" {
		t.Error("the checkout moved")
	}
}

// When main already has the section, init makes no commit and pushes nothing.
func TestPushWithTheSectionThereAlreadyPushesNothing(t *testing.T) {
	remote, _, clone := remoteWithClone(t)
	first := push(t, clone)
	again := push(t, clone)
	if again.SHA != "" || again.Fallback != "" {
		t.Errorf("a second init pushed %+v", again)
	}
	if got := g(t, remote, "rev-parse", "main"); got != first.SHA {
		t.Errorf("main moved to %s", got)
	}
}

// A teammate's push between init's fetch and its push makes init fetch again and build on theirs.
func TestPushRetriesWhenTheBranchMoves(t *testing.T) {
	remote, seed, clone := remoteWithClone(t)
	var theirs string
	beforePush = func(try int) {
		if try == 1 {
			g(t, seed, "commit", "-q", "--allow-empty", "-m", "a teammate's")
			g(t, seed, "push", "-q", "origin", "main")
			theirs = g(t, seed, "rev-parse", "HEAD")
		}
	}
	t.Cleanup(func() { beforePush = func(int) {} })
	p := push(t, clone)
	if got := g(t, remote, "rev-parse", "main"); got != p.SHA {
		t.Fatalf("main is %s, want init's %s", got, p.SHA)
	}
	if parent := g(t, remote, "rev-parse", "main^"); parent != theirs {
		t.Errorf("init's commit sits on %s, not on the teammate's %s", parent, theirs)
	}
}

// A server that refuses the push to main gets the commit on murmur-init instead, and a second init
// finds it there and pushes nothing.
func TestPushRefusedFallsBackToABranch(t *testing.T) {
	remote, _, clone := remoteWithClone(t)
	hook := "#!/bin/sh\nif [ \"$1\" = refs/heads/main ]; then echo 'main is protected' >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(remote, "hooks", "update"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	before := g(t, remote, "rev-parse", "main")
	p := push(t, clone)
	if p.Fallback != FallbackBranch || p.Refused == "" || p.Pending || p.FastForwarded {
		t.Fatalf("pushed %+v", p)
	}
	if got := g(t, remote, "rev-parse", "main"); got != before {
		t.Errorf("main moved to %s", got)
	}
	if got := g(t, remote, "rev-parse", FallbackBranch); got != p.SHA {
		t.Errorf("%s is %s, want %s", FallbackBranch, got, p.SHA)
	}
	if p.PullURL != "" {
		t.Errorf("a remote that isn't on GitHub got a pull-request address: %s", p.PullURL)
	}
	again := push(t, clone)
	if !again.Pending || again.SHA != p.SHA {
		t.Errorf("a second init: %+v", again)
	}
}

// A CLAUDE.md that is a link to AGENTS.md stays a link, and the section goes into AGENTS.md once.
func TestPushFollowsALinkInTheCommit(t *testing.T) {
	remote, seed, clone := remoteWithClone(t)
	blob := strings.TrimSpace(func() string {
		cmd := exec.Command("git", "hash-object", "-w", "--stdin")
		cmd.Dir, cmd.Stdin = seed, strings.NewReader("AGENTS.md")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}())
	g(t, seed, "update-index", "--add", "--cacheinfo", "120000,"+blob+",CLAUDE.md")
	g(t, seed, "commit", "-q", "-m", "link")
	g(t, seed, "push", "-q", "origin", "main")
	p := push(t, clone)
	if len(p.Changed) != 1 || !strings.HasPrefix(p.Changed[0], "AGENTS.md:") {
		t.Errorf("changed %q", p.Changed)
	}
	if entry := g(t, remote, "ls-tree", "main", "CLAUDE.md"); !strings.HasPrefix(entry, "120000 ") {
		t.Errorf("CLAUDE.md is no longer a link: %s", entry)
	}
	if n := strings.Count(g(t, remote, "show", "main:AGENTS.md"), markStart); n != 1 {
		t.Errorf("AGENTS.md has the section %d times", n)
	}
}

func TestPullURL(t *testing.T) {
	want := "https://github.com/a-Fig/murmur/compare/main...murmur-init?expand=1"
	for _, remote := range []string{"https://github.com/a-Fig/murmur.git", "https://github.com/a-Fig/murmur",
		"git@github.com:a-Fig/murmur.git", "ssh://git@github.com/a-Fig/murmur.git"} {
		if got := pullURL(remote, "main", "murmur-init"); got != want {
			t.Errorf("pullURL(%q) = %q", remote, got)
		}
	}
	for _, remote := range []string{"https://gitlab.com/a/b.git", "/srv/git/repo.git", "https://github.com/a-Fig"} {
		if got := pullURL(remote, "main", "murmur-init"); got != "" {
			t.Errorf("pullURL(%q) = %q, want none", remote, got)
		}
	}
}

// An AGENTS.md or CLAUDE.md the user has locally, untracked and ignored, is theirs: the checkout
// isn't fast-forwarded over it.
func TestPushDoesNotOverwriteAnIgnoredFile(t *testing.T) {
	_, _, clone := remoteWithClone(t)
	exclude := filepath.Join(clone, ".git", "info", "exclude")
	os.MkdirAll(filepath.Dir(exclude), 0o755)
	os.WriteFile(exclude, []byte("CLAUDE.md\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "CLAUDE.md"), []byte("my own notes\n"), 0o644)
	p := push(t, clone)
	if p.FastForwarded || !strings.Contains(p.Checkout, "local changes") {
		t.Errorf("pushed %+v", p)
	}
	if got := read(t, clone, "CLAUDE.md"); got != "my own notes\n" {
		t.Errorf("CLAUDE.md is now:\n%s", got)
	}
}

// A refs/remotes/origin/HEAD left naming an old default branch is pointed at the one init pushed
// to.
func TestPushRepointsAStaleRemoteHead(t *testing.T) {
	_, _, clone := remoteWithClone(t)
	g(t, clone, "update-ref", "refs/remotes/origin/old", "HEAD")
	g(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/old")
	push(t, clone)
	if got := g(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD"); got != "refs/remotes/origin/main" {
		t.Errorf("origin/HEAD names %s", got)
	}
}

// A retry that finds a teammate already put the section on main pushes nothing and says so, not
// the commit of the attempt that was rejected.
func TestPushRetryThatFindsTheSectionPushesNothing(t *testing.T) {
	remote, seed, clone := remoteWithClone(t)
	beforePush = func(try int) {
		if try == 1 {
			os.WriteFile(filepath.Join(seed, "AGENTS.md"), []byte("# Notes\nkeep this\n\n"+section), 0o644)
			os.WriteFile(filepath.Join(seed, "CLAUDE.md"), []byte(section), 0o644)
			g(t, seed, "add", "AGENTS.md", "CLAUDE.md")
			g(t, seed, "commit", "-q", "-m", "a teammate's init")
			g(t, seed, "push", "-q", "origin", "main")
		}
	}
	t.Cleanup(func() { beforePush = func(int) {} })
	p := push(t, clone)
	if p.SHA != "" {
		t.Errorf("reported %s as pushed", p.SHA)
	}
	if got := g(t, remote, "rev-parse", "main"); got != g(t, seed, "rev-parse", "HEAD") {
		t.Errorf("main is %s, not the teammate's commit", got)
	}
}

// Both marker files may be links to one file elsewhere in the repo; the marker there counts.
func TestMarkedFollowsLinksInTheCommit(t *testing.T) {
	_, seed, clone := remoteWithClone(t)
	os.MkdirAll(filepath.Join(seed, "docs"), 0o755)
	os.WriteFile(filepath.Join(seed, "docs", "AI.md"), []byte(section), 0o644)
	g(t, seed, "rm", "-q", "AGENTS.md")
	g(t, seed, "add", "docs/AI.md")
	for _, name := range markerFiles {
		cmd := exec.Command("git", "hash-object", "-w", "--stdin")
		cmd.Dir, cmd.Stdin = seed, strings.NewReader("docs/AI.md")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		g(t, seed, "update-index", "--add", "--cacheinfo", "120000,"+strings.TrimSpace(string(out))+","+name)
	}
	g(t, seed, "commit", "-q", "-m", "links")
	g(t, seed, "push", "-q", "origin", "main")
	g(t, clone, "fetch", "-q")
	gi, _, err := gitIn(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if !markedAt(context.Background(), gi, "refs/remotes/origin/HEAD") {
		t.Error("the marker behind the links isn't seen")
	}
}

// The commit is made as the person in the checkout init ran in, by that worktree's own config.
func TestPushCommitsAsTheWorktreesUser(t *testing.T) {
	remote, _, clone := remoteWithClone(t)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		if had {
			t.Cleanup(func() { os.Setenv(k, old) })
		}
	}
	wt := filepath.Join(filepath.Dir(clone), "wt")
	g(t, clone, "worktree", "add", "-q", "-b", "wt", wt)
	g(t, clone, "config", "extensions.worktreeConfig", "true")
	g(t, wt, "config", "--worktree", "user.name", "Worktree Person")
	g(t, wt, "config", "--worktree", "user.email", "wt@example.com")
	push(t, wt)
	if got := g(t, remote, "log", "-1", "--format=%an <%ae>", "main"); got != "Worktree Person <wt@example.com>" {
		t.Errorf("the commit's author is %s", got)
	}
}

// The remote is the clone's, as the daemon (one per clone) chooses it: a murmur.remote set for one
// worktree alone doesn't send init's commit elsewhere.
func TestPushUsesTheClonesRemoteNotAWorktreesOwn(t *testing.T) {
	remote, _, clone := remoteWithClone(t)
	other := filepath.Join(filepath.Dir(clone), "other.git")
	g(t, clone, "init", "-q", "--bare", "--initial-branch=main", other)
	g(t, clone, "remote", "add", "other", other)
	wt := filepath.Join(filepath.Dir(clone), "wt")
	g(t, clone, "worktree", "add", "-q", "-b", "wt", wt)
	g(t, clone, "config", "extensions.worktreeConfig", "true")
	g(t, wt, "config", "--worktree", "murmur.remote", "other")
	p := push(t, wt)
	if p.Remote != "origin" || g(t, remote, "rev-parse", "main") != p.SHA {
		t.Errorf("pushed %+v", p)
	}
}
