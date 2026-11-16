package gitx_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-Fig/murmur/internal/gitx"
)

// A tree's processes are gone when RunTree returns: none writes after, whether ctx ended while git
// ran or git exited and left a child running (#726).
func TestRunTreeEndsEveryProcess(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for git's alias to run")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	g := gitx.Git{Dir: dir}
	for what, alias := range map[string]string{
		// git runs the alias through sh, which runs the loop: git's grandchild, then.
		"stopped": `!f() { while :; do echo . >> "$1"; sleep 0.1; done; }; f`,
		// The loop runs on after git has exited.
		"left behind": `!f() { (while :; do echo . >> "$1"; sleep 0.1; done) & }; f`,
	} {
		t.Run(what, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			ctx, cancel := context.WithCancelCause(context.Background())
			stop := errors.New("stopped by the test")
			time.AfterFunc(time.Second, func() { cancel(stop) })
			defer cancel(nil)
			start := time.Now()
			_, err := g.RunTree(ctx, "-c", "alias.loop="+alias, "loop", filepath.ToSlash(log))
			if what == "stopped" && !errors.Is(err, stop) {
				t.Fatalf("RunTree: %v, want the context's cause", err)
			}
			if took := time.Since(start); took > 8*time.Second {
				t.Fatalf("RunTree took %v", took)
			}
			waitGrown(t, log)
			before := size(log)
			time.Sleep(600 * time.Millisecond)
			if after := size(log); after != before {
				t.Fatalf("written to after RunTree returned: %d bytes, then %d", before, after)
			}
		})
	}
}

// waitGrown waits for the loop to have written: the left-behind one may start a moment late.
func waitGrown(t *testing.T, path string) {
	t.Helper()
	for range 50 {
		if size(path) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func size(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
