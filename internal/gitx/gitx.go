// Package gitx runs the git binary. Running git, rather than linking a git library, gives Murmur
// each user's credential helpers, SSH config and URL rewrites with no configuration of its own.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/proc"
)

// Git runs commands against one repository's git directory.
type Git struct {
	// Dir is passed as --git-dir, so no command it runs can read or write a working tree or an
	// index. Humans and agents are editing the working tree at the same moment.
	Dir string
	// Env is added to the environment of every command, such as a GIT_INDEX_FILE of the caller's
	// own, so that building a tree doesn't use the index the working tree's users share.
	Env []string
}

// Error is a failed git command, with what it printed to stderr.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs git with the given stdin and returns its stdout.
func (g Git) Run(ctx context.Context, stdin string, args ...string) (string, error) {
	return run(ctx, "", stdin, g.Env, append([]string{"--git-dir=" + g.Dir}, args...)...)
}

// RunIn runs git in a working directory. It is for the few reads that need one, such as finding
// a repository or a session's current branch. The one write that goes through it is `mur init`
// fast-forwarding the checkout it was run in, which a person asked for.
func RunIn(ctx context.Context, dir string, args ...string) (string, error) {
	return run(ctx, dir, "", nil, args...)
}

// startLimit is how long git may take to start (proc.StartWithin). It takes milliseconds: under the
// race detector, with twelve starting at once, never over 0.7 s (#217).
const startLimit = 10 * time.Second

// lateSafe are the git commands whose start, if it hangs, is given up on, and the command tried
// once more (#217). Run late, after the caller has moved on, each is harmless: it reads, writes an
// object nothing names yet, or (fetch) brings the remote's refs as they are when it runs. Any other
// command waits for its start, however long, as one run late could undo what came after it: a
// push or an update-ref carries a value that would overwrite a newer one, and a presence pushed
// late would bring back a device that had said goodbye.
var lateSafe = map[string]bool{
	"cat-file": true, "commit-tree": true, "fetch": true, "for-each-ref": true, "hash-object": true,
	"log": true, "ls-remote": true, "merge-base": true, "mktree": true, "rev-list": true,
	"rev-parse": true, "show-ref": true,
}

// subcommand is the git command args run, after git's own options (--git-dir=..., -c name=value).
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// Observe, if set, is told of every git that ran: its command, when it started and how long it
// took. Nil outside tests; the daemon's tests set it to say, when a wait gives up, whether git was
// slow (#513).
var Observe func(subcommand string, start time.Time, took time.Duration)

func run(ctx context.Context, dir, stdin string, env []string, args ...string) (string, error) {
	if Observe != nil {
		began := time.Now()
		defer func() { Observe(subcommand(args), began, time.Since(began)) }()
	}
	out, err := runOnce(ctx, dir, stdin, env, args...)
	if errors.Is(err, proc.ErrStartHung) && ctx.Err() == nil {
		out, err = runOnce(ctx, dir, stdin, env, args...)
	}
	return out, err
}

func runOnce(ctx context.Context, dir, stdin string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	// A git killed outright can leave a ref's .lock file behind, and every later write to that
	// ref fails until it is removed. Where the platform allows, interrupt it instead, so git
	// removes its own locks, and kill it only if it hasn't exited soon after.
	if interrupt != nil {
		cmd.Cancel = func() error { return interrupt(cmd.Process) }
	}
	// Without a WaitDelay, Wait also waits for grandchildren (git-remote-https, ssh) that hold
	// the output pipes, so even a killed git could keep its caller blocked.
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir
	// The daemon runs detached, with nobody to answer a prompt: fail instead of waiting.
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never"), env...)
	hideWindow(cmd)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	start := cmd.Start
	if lateSafe[subcommand(args)] {
		start = func() error { return proc.StartWithin(cmd, startLimit, cancel) }
	}
	if err := start(); err != nil {
		return "", &Error{Args: args, Err: err}
	}
	if err := cmd.Wait(); err != nil {
		return out.String(), &Error{Args: args, Stderr: errb.String(), Err: err}
	}
	return out.String(), nil
}
