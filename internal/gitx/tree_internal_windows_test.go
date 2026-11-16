package gitx

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Where git can't be put in a job, stopping RunTree still ends git itself, as Run does, rather than
// nothing (Sol review).
func TestRunTreeWithNoJobEndsGit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for git's alias to run")
	}
	useJob = false
	defer func() { useJob = true }()
	tmp := t.TempDir()
	t.Setenv("TMP", tmp) // where RunTree's output files go
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	// The sleep, git's child, outlives git, as nothing ends it, with git's output open.
	_, err := Git{Dir: dir}.RunTree(ctx, "-c", "alias.wait=!sleep 2", "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunTree: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("RunTree took %v: git wasn't ended", took)
	}
	// Once the sleep has gone, so are the output files it held (Sol review).
	for range 100 {
		if left, _ := filepath.Glob(filepath.Join(tmp, "mur-git-*")); len(left) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "mur-git-*"))
	t.Fatalf("output files left: %q", left)
}
