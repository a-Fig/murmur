package gitx

import (
	"context"
	"errors"
	"os/exec"
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
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := Git{Dir: dir}.RunTree(ctx, "-c", "alias.wait=!sleep 30", "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunTree: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("RunTree took %v: git wasn't ended", took)
	}
}
