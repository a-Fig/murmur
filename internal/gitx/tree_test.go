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
	for what, c := range map[string]struct {
		alias string
		stop  bool // ended by ctx while git runs; otherwise git exits, and RunTree with it
	}{
		// git runs the alias through sh, which runs the loop: git's grandchild, then.
		"stopped": {`!f() { while :; do echo . >> "$1"; sleep 0.1; done; }; f`, true},
		// The loop runs on after git has exited, holding git's output open; git waits until it has
		// written, so the test sees it stopped and not merely not yet begun.
		"left behind": {`!f() { (while :; do echo . >> "$1"; sleep 0.1; done) & sleep 0.5; }; f`, false},
	} {
		t.Run(what, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "log")
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			stop := errors.New("stopped by the test")
			if c.stop {
				time.AfterFunc(time.Second, func() { cancel(stop) })
			}
			start := time.Now()
			_, err := g.RunTree(ctx, "-c", "alias.loop="+c.alias, "loop", filepath.ToSlash(log))
			took := time.Since(start)
			switch {
			case c.stop && !errors.Is(err, stop):
				t.Fatalf("RunTree: %v, want the context's cause", err)
			case !c.stop && err != nil:
				t.Fatalf("RunTree of a git that exited: %v", err)
			case c.stop && took > 8*time.Second, !c.stop && took > 3*time.Second:
				t.Fatalf("RunTree took %v", took)
			}
			if !c.stop && size(log) == 0 {
				t.Fatal("the loop never wrote")
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
