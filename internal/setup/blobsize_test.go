package setup

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-Fig/murmur/internal/gitx"
)

// A marker file in a commit is held to maxRepoFile before it is read, as one in the working tree
// is (#947): a blob read whole sat in memory. One under the limit, and a link to it, are read as before.
func TestABlobOverTheLimitIsNotRead(t *testing.T) {
	dir := repo(t, map[string]string{
		"AGENTS.md": strings.Repeat("x", maxRepoFile+1),
		"small.md":  "a small file\n",
		"edge.md":   strings.Repeat("y", maxRepoFile),
	})
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "add", ".")
	// CLAUDE.md, a symbolic link to small.md, made in the index: Windows may have no way to make
	// one in the working tree.
	link := run("small.md", "hash-object", "-w", "--stdin")
	run("", "update-index", "--add", "--cacheinfo", "120000,"+link+",CLAUDE.md")
	run("", "commit", "-q", "-m", "files")
	files := &treeFiles{ctx: context.Background(), g: gitx.Git{Dir: filepath.Join(dir, ".git")}, tip: run("", "rev-parse", "HEAD"), files: map[string]*treeFile{}}

	if _, b, _, err := files.read("AGENTS.md"); err == nil || !strings.Contains(err.Error(), "MiB") || b != nil {
		t.Errorf("a blob over the limit: %d bytes read, %v", len(b), err)
	}
	if _, b, ok, err := files.read("edge.md"); err != nil || !ok || len(b) != maxRepoFile {
		t.Errorf("a blob at the limit: %d bytes, %v, %v", len(b), ok, err)
	}
	if real, b, ok, err := files.read("CLAUDE.md"); err != nil || !ok || real != "small.md" || string(b) != "a small file\n" {
		t.Errorf("a link to a small file: %q, %q, %v, %v", real, b, ok, err)
	}
}

// A commit init would push isn't one whose marker file goes over the limit: the next init, or the
// marker check, would refuse to read it.
func TestBuildCommitRefusesAFileTheSectionTakesOverTheLimit(t *testing.T) {
	full := strings.Repeat(strings.Repeat("x", 63)+"\n", maxRepoFile/64)
	dir := repo(t, map[string]string{"AGENTS.md": full})
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("config", "user.name", "t") // so that only the size can stop the commit
	run("config", "user.email", "t@example.invalid")
	run("add", ".")
	run("commit", "-q", "-m", "a full AGENTS.md")
	g := gitx.Git{Dir: filepath.Join(dir, ".git")}
	if _, _, _, err := buildCommit(context.Background(), g, g, run("rev-parse", "HEAD")); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Errorf("a commit taking AGENTS.md over the limit: %v", err)
	}
}
