package gitx

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"time"
)

// treeGone is how long RunTree waits, after ending a command's processes, for them to be gone.
const treeGone = 10 * time.Second

// RunTree runs git as Run does, but in a process tree of its own, and when ctx ends, or git exits
// first, it ends every process in that tree and waits for them to be gone before it returns (#726).
// Run ends only git itself: a fetch it stops leaves its children running (git-remote-https, which
// over protocol 0 or 1 has a fetch-pack of its own downloading, and index-pack, writing the pack),
// and whoever cleans up after it can find their files still open, or written to after. On Windows
// the tree is a job object; elsewhere, a process group. It returns git's stdout, and an *Error
// whose Err is context.Cause(ctx) if ctx ended first.
//
// It is for a command that is to be stopped partway, the files fetch: one run late, or a git
// killed outright, is what it is for, so it has neither Run's retry of a start that hangs nor its
// interrupt.
func (g Git) RunTree(ctx context.Context, args ...string) (string, error) {
	args = append([]string{"--git-dir=" + g.Dir}, args...)
	cmd := exec.Command("git", args...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"), g.Env...)
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	// Wait waits for whoever holds the output pipes, which the tree's ending closes; this is for a
	// process that got out of the tree (where the system lets one).
	cmd.WaitDelay = treeGone
	t, err := startTree(cmd)
	if err != nil {
		return "", &Error{Args: args, Err: err}
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err = <-waited:
		t.end() // what git left behind
	case <-ctx.Done():
		t.end()
		<-waited
		err = context.Cause(ctx)
	}
	if err != nil {
		return out.String(), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.String(), nil
}
