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
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/hook"
	"github.com/a-Fig/murmur/internal/join"
	"github.com/a-Fig/murmur/internal/logfile"
	"github.com/a-Fig/murmur/internal/mcpserver"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/publicrepo"
	"github.com/a-Fig/murmur/internal/setup"
	"github.com/a-Fig/murmur/internal/update"
	"github.com/a-Fig/murmur/internal/web"
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
                                the agent's session joins too, even one that started outside the repo;
                                --leave undoes it
  mur uninstall [--leave]       remove Murmur from this machine for you: its server, hooks and skills from
                                every agent, its daemons, its folder in your config directory; refuses
                                while a repo here is joined, unless --leave leaves each first

For humans:
  mur ui [--no-open]            open the page in a browser, to see sessions and messages and to message
                                agents; --no-open only prints its address (starts the clone's daemon
                                if it isn't running)
  mur nickname [NAME|--clear]   call this clone's device NAME, such as your own name: teammates see it
                                beside the device's ID; --clear takes it off; alone, says what the
                                device is called
  mur stop                      stop this clone's daemon
  mur rotate [--force]          make a new team key if a collaborator has left the repo (listed with gh),
                                so what is written from now on can't be read by them; --force always does.
                                First remove every other way they can push (deploy keys they added, team
                                access, Apps): whatever can still push is given the new key

Started by other programs:
  mur mcp [--agent NAME]        stdio MCP server, one per agent session
  mur hook [AGENT]              an agent's hook: Claude Code's, or codex's, grok's, or pi's and
                                opencode's through Murmur's extension and plugin (reads the
                                payload on stdin)
  mur daemon [--git-dir DIR]    the daemon, in the foreground

Every command takes -C DIR to act on the repository at DIR.
`)

func main() {
	dirs, args, err := leadingDirs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
		os.Exit(2)
	}
	for _, dir := range dirs {
		if err := os.Chdir(dir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", brand.Command, err)
			os.Exit(1)
		}
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := args[0], args[1:]
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "hook":
		// Never an error: a hook that fails would fail the agent's tool call. One it can't read
		// does nothing.
		if agent, ok := hookAgent(args); ok {
			hook.RunFor(agent, os.Stdin, os.Stdout)
		}
	case "mcp":
		err = runMCP(ctx, args)
	case "daemon":
		err = runDaemon(ctx, args)
	case "init":
		err = runInit(ctx, args, os.Stdout)
	case "join":
		err = runJoin(ctx, args, os.Stdout)
	case "nickname":
		err = runNickname(ctx, args, os.Stdout)
	case "stop":
		err = runStop(ctx, args, os.Stdout)
	case "rotate":
		err = runRotate(ctx, args, os.Stdout)
	case "uninstall":
		err = runUninstall(ctx, args, os.Stdout)
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

// leadingDirs takes the -C DIR options that come before the command, as git does: `mur -C DIR stop`
// acts where `mur stop -C DIR` does (#373). Until 2026-11-10 only the second worked, and the first
// said `unknown command "-C"`. main changes to each directory in turn, each relative to the one
// before, as git's do, so every command takes it, the hook included, and a -C after the command is
// then relative to it. The flag package's other spellings, --C and -C=DIR, are taken too.
func leadingDirs(args []string) (dirs, rest []string, err error) {
	for len(args) > 0 {
		name, value, hasValue := strings.Cut(args[0], "=")
		if name != "-C" && name != "--C" {
			break
		}
		if !hasValue {
			if len(args) < 2 {
				return nil, nil, fmt.Errorf("%s needs a directory", name)
			}
			value, args = args[1], args[1:]
		}
		if value == "" {
			return nil, nil, fmt.Errorf("%s needs a directory", name)
		}
		dirs, args = append(dirs, value), args[1:]
	}
	return dirs, args, nil
}

// hookAgent is the agent `mur hook` runs for, from its arguments: "" for Claude Code, or the one
// named, as join writes it for Codex and Grok (`mur hook codex`, #225) or as Pi's extension runs it
// (`mur hook --agent pi`, #205). ok is false for arguments it can't read or an agent the hook doesn't
// know.
func hookAgent(args []string) (agent string, ok bool) {
	fs := flag.NewFlagSet(brand.Command+" hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	a := fs.String("agent", "", "")
	if fs.Parse(args) != nil {
		return "", false
	}
	switch {
	case fs.NArg() == 1 && *a == "":
		*a = fs.Arg(0)
	case fs.NArg() != 0:
		return "", false
	}
	if !hook.KnownAgent(*a) {
		return "", false
	}
	return *a, true
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
	return runDaemonWith(ctx, args, daemon.Run, client.StartDaemon)
}

func runDaemonWith(ctx context.Context, args []string, run func(context.Context, daemon.Options) error, start func(exe, gitDir string, page *os.File, flags ...string) error) error {
	// A daemon that restarted onto an update may have handed this one the page's socket (#255).
	// It is taken first, so that it is closed however this returns, and no child of this process
	// is told of it.
	page := proc.Inherited(brand.PageListenerEnv, "page")
	defer func() {
		if page != nil {
			page.Close()
		}
	}()
	// The updater has to know the binary this process came from. It starts only after the daemon's
	// first sync, and a reinstall in that time must not pass for this binary, so this comes first.
	exe, exeErr := os.Executable()
	if exeErr == nil {
		exe, exeErr = filepath.EvalSymlinks(exe)
	}
	var running os.FileInfo
	if exeErr == nil {
		running, _ = update.Identity(exe) // on failure, update.Run tries for itself
	}
	fs, dir := flags("daemon")
	gitDir := fs.String("git-dir", "", "the clone's git common dir (default: found from -C)")
	poll := fs.Duration("poll", 0, "how often to check the remote (default 10s)")
	idle := fs.Duration("idle", 0, "exit after this long with no sessions (default 10m; negative, e.g. -1s: never)")
	logPath := fs.String("log", "", "append output to this file, not stderr (for starters that can't pass a handle)")
	fs.Parse(args)
	// A replacement starts in the clone root, which may differ from this cwd.
	// Resolve path flags before recording them for the next daemon.
	for _, path := range []*string{dir, gitDir, logPath} {
		if *path != "" {
			resolved, err := filepath.Abs(*path)
			if err != nil {
				return err
			}
			*path = resolved
		}
	}
	var restartFlags []string
	fs.Visit(func(f *flag.Flag) {
		restartFlags = append(restartFlags, "--"+f.Name+"="+f.Value.String())
	})
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
	// The log is kept under a size while the daemon runs, and credentials in URLs out of it, so
	// that it can be sent to whoever helps debug this device.
	logs := *logPath
	if logs == "" {
		logs = filepath.Join(client.StateDir(*gitDir), "daemon.log") // where StartDaemon points stderr
	}
	out := logfile.New(os.Stderr, logs, logfile.Max)
	log.SetOutput(out)
	logger := log.New(out, "", log.LstdFlags)
	releases := newReleaseWatch(version, *gitDir)
	inherited := page
	page = nil // Run takes it
	// The record of this machine's clones, for the page's repo picker (#348). Without it the page
	// shows only its own repo.
	var record string
	var repos web.Repos
	if d, err := clones.DefaultDir(); err != nil {
		logger.Printf("no record of this machine's clones, so the page can't switch repos: %v", err)
	} else {
		record = d
		repos = clones.Picker{Dir: d, Self: *gitDir, Joined: join.Joined}
	}
	err := run(ctx, daemon.Options{GitDir: *gitDir, Poll: *poll, Idle: *idle, PageListener: inherited,
		Clones: record, Repos: repos,
		Consent: func() bool { return join.Joined(*gitDir) }, Log: logger,
		Update: func(ctx context.Context, restart func()) {
			update.Run(ctx, update.Options{Version: version, GitDir: *gitDir, Log: logger, Running: running, Report: releases.report}, restart)
		},
		Version: releases.page,
		Report: func(ctx context.Context) {
			publicrepo.Run(ctx, publicrepo.Options{Version: version, GitDir: *gitDir, Log: logger})
		}})
	if errors.Is(err, daemon.ErrUpdated) {
		// The page's socket, held open by Run, is closed here once the new daemon has its own
		// hold on it, or has failed to start.
		var up *daemon.Updated
		if errors.As(err, &up) {
			page = up.Page
		}
		if exeErr != nil {
			return exeErr
		}
		return start(exe, *gitDir, page, restartFlags...)
	}
	if errors.Is(err, daemon.ErrRunning) {
		return nil // another client started one first; that one serves
	}
	return err
}

// noteNewer says so when the clone in the current directory last heard of a newer release, from its
// daemon's update check. It writes to stderr, so stdout stays the one line scripts read.
func noteNewer(ctx context.Context, w io.Writer) {
	gitDir, err := client.FindGitDir(ctx, ".")
	if err != nil {
		return
	}
	if latest := update.Newer(gitDir, version); latest != "" {
		fmt.Fprintf(w, "%s %s is out. Murmur updates automatically while its daemon runs, unless disabled.\nTo update by hand, run the install again:\n  %s\n", brand.Product, latest, installLine(latest))
	}
}

// installLine installs latest. A prerelease (seen on the prerelease channel) is pinned, since the
// installers' default is the latest stable release, which is older.
func installLine(latest string) string {
	pin := strings.Contains(latest, "-")
	if runtime.GOOS == "windows" {
		if pin {
			return "$env:MURMUR_VERSION='v" + latest + "'; irm " + brand.InstallPs1 + " | iex"
		}
		return "irm " + brand.InstallPs1 + " | iex"
	}
	if pin {
		return "curl -fsSL " + brand.InstallSh + " | MURMUR_VERSION=v" + latest + " sh"
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
		reportPublic(ctx, *dir, out)
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
	reportPublic(ctx, *dir, out)
	return nil
}

// reportPublic tells murmur.afig.dev this repo's address if the repo is public and nobody has yet
// (internal/publicrepo), and says so. A failure is left for a daemon to try again.
func reportPublic(ctx context.Context, dir string, out io.Writer) {
	gitDir, err := client.FindGitDir(ctx, dir)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if res, _ := publicrepo.Once(ctx, publicrepo.Options{Version: version, GitDir: gitDir}); res == publicrepo.Reported {
		fmt.Fprintf(out, "\n%s\n", publicrepo.Told)
	}
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
	env.Version = version
	env.Progress = os.Stderr // a line while a leave waits for the daemon; stdout is the report
	do := join.Join
	if *leave {
		do = join.Leave
	}
	report, err := do(ctx, env, *dir)
	if !*leave && err == nil {
		bringCaller(ctx, *dir, &report)
	}
	fmt.Fprint(out, report.String())
	return err
}

// runRotate asks the daemon for a new team key (#156) when the repo's collaborators, listed with gh,
// have lost someone since the current key was made. gh is the GitHub CLI: Murmur makes no request to
// GitHub of its own for this, and a repo that isn't on GitHub, or a gh that isn't signed in, leaves
// only --force.
func runRotate(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("rotate")
	force := fs.Bool("force", false, "make a new team key whoever the collaborators are")
	fs.Parse(args)
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	collab, why := listCollaborators(ctx, gitDir)
	if collab == nil && !*force {
		return fmt.Errorf("%s; with --force, %s rotate makes a new team key anyway", why, brand.Command)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := client.Connect(ctx, gitDir, true)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, proto.Request{Op: proto.OpRotate, Collaborators: collab, Force: *force})
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Fprintln(out, resp.Note)
	return nil
}

// listCollaborators lists the GitHub logins of the people who can push to the bus's repo, with gh, or
// says why it couldn't. Only push access counts: someone moved down to read access is still a
// collaborator to GitHub, and has left as far as the bus goes.
func listCollaborators(ctx context.Context, gitDir string) ([]string, string) {
	g := gitx.Git{Dir: gitDir}
	name, err := bus.RemoteFor(ctx, g)
	if err != nil {
		return nil, err.Error()
	}
	url, err := g.Run(ctx, "", "remote", "get-url", name)
	if err != nil {
		return nil, "couldn't read the remote's address"
	}
	addr := publicrepo.Address(strings.TrimSpace(url))
	if addr == "" {
		return nil, "the repo isn't on GitHub, so its collaborators can't be listed"
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, "listing the collaborators needs the GitHub CLI, gh, which isn't installed"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "--paginate", "repos/"+strings.TrimPrefix(addr, "github.com/")+"/collaborators", "--jq", ".[] | select(.permissions.push) | .login")
	b, err := cmd.Output()
	if err != nil {
		return nil, "gh couldn't list the collaborators (is it signed in, with access to the repo?)"
	}
	var logins []string
	for _, l := range strings.Fields(string(b)) {
		logins = append(logins, strings.ToLower(l))
	}
	if len(logins) == 0 {
		return nil, "gh listed no collaborators who can push"
	}
	return logins, ""
}

func runUninstall(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(brand.Command+" uninstall", flag.ExitOnError)
	leave := fs.Bool("leave", false, "leave every repo on this machine that is still joined, first")
	fs.Parse(args)
	if fs.NArg() > 0 {
		return errors.New("uninstall takes no arguments, only --leave")
	}
	env, err := join.EnvFromProcess()
	if err != nil {
		return err
	}
	env.Version = version
	env.Progress = os.Stderr // a line while a leave waits for a daemon; stdout is the report
	report, err := join.Uninstall(ctx, env, *leave)
	fmt.Fprint(out, report.String())
	return err
}

// runNickname gives this clone's device a nickname (#190), takes it off, or says what it has.
func runNickname(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("nickname")
	clear := fs.Bool("clear", false, "take the device's nickname off")
	fs.Parse(args)
	if fs.NArg() > 1 || *clear && fs.NArg() > 0 {
		return fmt.Errorf("nickname takes one name, or --clear")
	}
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	c, err := client.Connect(ctx, gitDir, true)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, proto.Request{Op: proto.OpNick, Nick: fs.Arg(0), Clear: *clear})
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
