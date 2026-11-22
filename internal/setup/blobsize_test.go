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
// is (#947): markedAt reads the remote-tracking branch's in every session's MCP server, and a blob
// read whole sat in memory in each. One under the limit, and a link to it, are read as before.
func TestABlobOverTheLimitIsNotRead(t *testing.T) {
	dir := repo(t, map[string]string{
		"AGENTS.md": strings.Repeat("x", maxRepoFile+1),
		"small.md":  "a small file\n",
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
	if real, b, ok, err := files.read("CLAUDE.md"); err != nil || !ok || real != "small.md" || string(b) != "a small file\n" {
		t.Errorf("a link to a small file: %q, %q, %v, %v", real, b, ok, err)
	}
}
