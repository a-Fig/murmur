// Command mur is the whole tool in one binary: the per-clone daemon, the stdio MCP server each
// agent session starts, the Claude Code hook, and the few commands a person runs: setup, joining
// and the page. Messaging is for agents, through the MCP server, and for humans, through the page.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/checkin"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/mcpserver"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/setup"
)

var version = "0.1.0-dev"

// usage spells the names as they are, so its columns line up; the replacer keeps it in step with
// package brand ("Murmur" first, so the "mur" inside it isn't taken for the command).
var usage = strings.NewReplacer("Murmur", brand.Product, "mur", brand.Command).Replace(
	`Murmur: coding agents on teammates' machines, in the same repo, finding each other.

Setup:
  mur init [--no-push]          add the section to AGENTS.md and CLAUDE.md that says this repo uses Murmur,
                                as a commit pushed to the default branch; --no-push only edits the files here
  mur join [--leave]            join Murmur in this repo, after the user agreed (an agent runs this);
                                --leave undoes it

For humans:
  mur ui [--no-open]            open the page showing sessions and messages in a browser; --no-open
                                only prints its address (starts the clone's daemon if it isn't running)
  mur stop                      stop this clone's daemon
  mur encrypt [--force]         encrypt everything this repo's devices send each other from now on,
                                for good; --force goes ahead though some devices are too old to

Started by other programs:
  mur mcp [--agent NAME]        stdio MCP server, one per agent session
  mur hook                      Claude Code hook (reads the hook payload on stdin)
  mur daemon [--git-dir DIR]    the daemon, in the foreground

Every command takes -C DIR to act on the repository at DIR.
`)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd {
	case "hook":
		hook.Run(os.Stdin, os.Stdout)
	case "mcp":
		err = runMCP(ctx, args)
	case "daemon":
		err = runDaemon(ctx, args)
	case "init":
		err = runInit(ctx, args, os.Stdout)
	case "join":
		err = runJoin(ctx, args, os.Stdout)
	case "stop":
		err = runStop(ctx, args, os.Stdout)
	case "encrypt":
		err = runEncrypt(ctx, args, os.Stdout)
	case "ui":
		err = runUI(ctx, args, os.Stdout, browse)
	case "version", "--version":
		fmt.Println(brand.Command, version)
		noteNewer(ctx, os.Stderr)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", brand.Command, err)
		os.Exit(1)
	}
}

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(brand.Command+" "+name, flag.ExitOnError)
	dir := fs.String("C", ".", "act on the repository at this directory")
	return fs, dir
}

func runMCP(ctx context.Context, args []string) error {
	fs, dir := flags("mcp")
	agent := fs.String("agent", "", "the agent's name as teammates see it (default: detected)")
	fs.Parse(args)
	if err := os.Chdir(*dir); err != nil {
		return err
	}
	// stdout carries the protocol; anything else goes to stderr, which clients log.
	log.SetOutput(os.Stderr)
	return mcpserver.Run(ctx, mcpserver.Options{Agent: *agent, Version: version})
}

func runDaemon(ctx context.Context, args []string) error {
	fs, dir := flags("daemon")
	gitDir := fs.String("git-dir", "", "the clone's git common dir (default: found from -C)")
	poll := fs.Duration("poll", 0, "how often to check the remote (default 10s)")
	idle := fs.Duration("idle", 0, "exit after this long with no sessions (default 10m; negative, e.g. -1s: never)")
	logPath := fs.String("log", "", "append output to this file, not stderr (for starters that can't pass a handle)")
	fs.Parse(args)
	if *logPath != "" {
		if err := redirectOutput(*logPath); err != nil {
			return err
		}
	}
	if *gitDir == "" {
		var err error
		if *gitDir, err = client.FindGitDir(ctx, *dir); err != nil {
			return err
		}
	}
	// A daemon publishes this machine's presence to the team, which only a user who joined in this
	// clone has agreed to. Whoever started this one (a stale client, a script) doesn't change that.
	if !join.Joined(*gitDir) {
		return fmt.Errorf("this clone hasn't joined %s, so its daemon won't run (`%s join` joins, after asking the user)", brand.Product, brand.Command)
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if off, why := checkin.Off(ctx, os.Getenv, *gitDir); off {
		logger.Printf("no daily check-in: %s", why)
	}
	err := daemon.Run(ctx, daemon.Options{GitDir: *gitDir, Poll: *poll, Idle: *idle,
		Consent: func() bool { return join.Joined(*gitDir) }, Log: logger,
		CheckIn: func(ctx context.Context) {
			checkin.Run(ctx, checkin.Options{Version: version, GitDir: *gitDir, Log: logger})
		}})
	if errors.Is(err, daemon.ErrRunning) {
		return nil // another client started one first; that one serves
	}
	return err
}

// noteNewer says so when the clone in the current directory last heard of a newer release, from its
// daily check-in. It writes to stderr, so stdout stays the one line scripts read.
func noteNewer(ctx context.Context, w io.Writer) {
	gitDir, err := client.FindGitDir(ctx, ".")
	if err != nil {
		return
	}
	if latest := checkin.Newer(gitDir, version); latest != "" {
		fmt.Fprintf(w, "%s %s is out. To update, run the install again:\n  %s\n", brand.Product, latest, installLine())
	}
}

func installLine() string {
	if runtime.GOOS == "windows" {
		return "irm " + brand.InstallPs1 + " | iex"
	}
	return "curl -fsSL " + brand.InstallSh + " | sh"
}

// redirectOutput sends this process's stdout and stderr to the file, appending. WMI gives a process
// no handles, so a starter that uses it names the log file instead.
func redirectOutput(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	// A panic trace is written to stderr, which here is the log: no other stderr reaches it.
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

func runInit(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("init")
	noPush := fs.Bool("no-push", false, "only edit the files in this working tree, for you to review and commit")
	fs.Parse(args)
	if *noPush {
		return initWorkTree(ctx, *dir, out)
	}
	p, err := setup.Push(ctx, *dir)
	if err != nil {
		return err
	}
	short := p.SHA
	if len(short) > 7 {
		short = short[:7]
	}
	list := func() {
		for _, c := range p.Changed {
			fmt.Fprintln(out, "  "+c)
		}
	}
	switch {
	case p.SHA == "":
		fmt.Fprintf(out, "Already set up on %s's %s; nothing pushed.\n", p.Remote, p.Branch)
		if p.Checkout != "" {
			fmt.Fprintf(out, "%s%s.\n", strings.ToUpper(p.Checkout[:1]), p.Checkout[1:])
		}
		return nil
	case p.Pending:
		fmt.Fprintf(out, "%s refused the push to %s: %s\n", p.Remote, p.Branch, p.Refused)
		fmt.Fprintf(out, "Its branch %s already has the section (%s) from an earlier init; nothing pushed.\n", p.Fallback, short)
	case p.Fallback != "":
		fmt.Fprintf(out, "%s refused the push to %s: %s\n", p.Remote, p.Branch, p.Refused)
		fmt.Fprintf(out, "Pushed %s to a new branch on %s, %s, instead:\n", short, p.Remote, p.Fallback)
		list()
	default:
		fmt.Fprintf(out, "Pushed %s to %s's %s:\n", short, p.Remote, p.Branch)
		list()
	}
	if p.Fallback != "" {
		if p.PullURL != "" {
			fmt.Fprintf(out, "\nOpen a pull request from it, for someone who may merge into %s: %s\n", p.Branch, p.PullURL)
		} else {
			fmt.Fprintf(out, "\nMerge %s into %s to finish; someone who may push to %s can.\n", p.Fallback, p.Branch, p.Branch)
		}
	} else if p.FastForwarded {
		fmt.Fprintf(out, "\nThis checkout's %s is now at %s.\n", p.Branch, short)
	} else {
		fmt.Fprintf(out, "\nThe section is on %s; %s.\n", p.Branch, p.Checkout)
	}
	fmt.Fprintf(out, "\nA teammate's agent that reads the section asks its user whether to join, and if the user says yes\n"+
		"it installs %[1]s and sets it up itself. The section installs nothing, and %[1]s does nothing in a\n"+
		"clone until its user has joined there.\n", brand.Product)
	return nil
}

// initWorkTree is init before it pushed, kept as --no-push: it edits this working tree and leaves
// committing to the person.
func initWorkTree(ctx context.Context, dir string, out io.Writer) error {
	changed, err := setup.Init(ctx, dir)
	for _, c := range changed {
		fmt.Fprintln(out, c)
	}
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		fmt.Fprintln(out, "Already set up; nothing changed.")
		return nil
	}
	fmt.Fprintf(out, "\nReview and commit these files. A teammate's agent that reads them asks its user whether to join,\n"+
		"and if the user says yes it installs %[1]s and sets it up itself. Committing installs nothing, and %[1]s\n"+
		"does nothing in a clone until its user has joined there.\n", brand.Product)
	return nil
}

func runJoin(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("join")
	leave := fs.Bool("leave", false, "undo joining in this clone: forget the consent, remove the hook, stop the daemon")
	fs.Parse(args)
	env, err := join.EnvFromProcess()
	if err != nil {
		return err
	}
	do := join.Join
	if *leave {
		do = join.Leave
	}
	report, err := do(ctx, env, *dir)
	fmt.Fprint(out, report.String())
	return err
}

// runEncrypt turns encryption on for the repo's bus (#156), through the clone's daemon, which
// it starts if it isn't running.
func runEncrypt(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("encrypt")
	force := fs.Bool("force", false, "go ahead though some devices are too old to encrypt")
	fs.Parse(args)
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := client.Connect(ctx, gitDir, true)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, proto.Request{Op: proto.OpEncrypt, Force: *force})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Fprintln(out, resp.Note)
	return nil
}

func runStop(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("stop")
	fs.Parse(args)
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stopped, err := client.Stop(ctx, gitDir)
	if err != nil {
		return err
	}
	if !stopped {
		fmt.Fprintln(out, "No daemon is running for this clone.")
		return nil
	}
	fmt.Fprintln(out, "Stopped the daemon.")
	return nil
}
