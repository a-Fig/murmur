// Command mur is the whole tool in one binary: the per-clone daemon, the stdio MCP server each
// agent session starts, the Claude Code hook, and the few commands a person runs: setup, joining
// and the page. Messaging is for agents, through the MCP server or, in a harness with no Murmur
// tools, the shell commands (#913), and for humans, through the page.
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
	"github.com/a-Fig/murmur/internal/collaborators"
	"github.com/a-Fig/murmur/internal/daemon"
	"github.com/a-Fig/murmur/internal/desktop"
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
// package brand ("Murmur" first, so the "mur" inside it isn't taken for the command). The MCP tools
// are listed from the server's own list (#1103), once the commands are known, to say which of them
// are commands too.
var usage = withTools(strings.NewReplacer("Murmur", brand.Product, "mur", brand.Command).Replace(
	`Murmur: coding agents on teammates' machines, in the same repo, finding each other.

Setup:
  mur init [--no-push|--refs-only]
                                set this repo up for Murmur, once: make Murmur's empty refs/murmur/names on
                                the remote, which join looks for, and add the section to AGENTS.md and
                                CLAUDE.md that invites agents to join, as a commit pushed to the default
                                branch; --refs-only makes only Murmur's refs; --no-push only edits the
                                files here and pushes nothing, so join refuses until a pushing init runs
  mur join [--leave]            join Murmur in this repo, after the user agreed (an agent runs this); needs
                                the ref mur init makes; the agent's session joins too, even one that
                                started outside the repo; --leave undoes it
  mur uninstall [--leave]       remove Murmur from this machine for you: its server, hooks and skills from
                                every agent, its daemons, its folder in your config directory; refuses
                                while a repo here is joined, unless --leave leaves each first

For humans:
  mur ui [--no-open]            open the page in a browser, to see sessions and messages and to message
                                agents; --no-open only prints its address (starts the clone's daemon
                                if it isn't running)
  mur app                       open the page in a window of its own, with a count of what's unread and
                                notifications of messages to you; in a repo that joined, that repo's page,
                                else the one it last showed
  mur nickname [NAME|--clear]   call this clone's device NAME, such as your own name: teammates see it
                                beside the device's ID; --clear takes it off; alone, says what the
                                device is called
  mur updates [off|on|nightly|back]
                                how Murmur updates itself on this machine: off, stable releases (on),
                                or prereleases and nightlies too; back goes back to the latest stable
                                from a prerelease, until a newer release is out; alone, says what applies
  mur stop                      stop this clone's daemon
  mur owner                     show the mesh owner
  mur owner transfer DEVICE     hand ownership to another device
  mur owner mode [team|private|off]
                                show or change repo encryption. To turn it off, put --allow-plaintext
                                before mode.
  mur rotate [--force]          make a new team key if a collaborator has left the repo (listed with gh),
                                so what is written from now on can't be read by them; --force always does.
                                First remove every other way they can push (deploy keys they added, team
                                access, Apps): whatever can still push is given the new key

For agents with no Murmur tools, in a T3 Code thread's shell:
  mur who [TAG...]              list the sessions in this repo, as the who tool does
  mur send TO TEXT|-            send a message, as the send tool does: TO is a name from who, or several
                                separated by commas, and - reads TEXT from standard input. The subject is
                                TEXT's first line, or its opening words; --subject SUBJECT sets it, and
                                TEXT is then the body
  mur inbox                     read and clear the messages that came as notices
  mur status [TEXT]             set the line others see in who for what you are working on
  mur lookup NAME [--messages N]
                                everything known about a session, as the lookup tool does, with its
                                latest N messages (10 if not given, at most 50)
  mur tags [--add TAG,...] [--remove TAG,...] [--for NAME,...]
                                add or remove your tags, as the tags tool does; --for NAME,... gives
                                the group: tags in --add to those sessions, not to you unless you list
                                your own name too; alone, lists yours
  mur mute [NAME] [--unmute]    stop getting anything NAME sends, as the mute tool does; --unmute undoes
                                it; alone, lists whom you have muted

{tools}

For agents that can't open the links in the repo's section:
  mur docs [PAGE]               print a page of Murmur's instructions for agents, such as join or use,
                                as this version has it; alone, lists the pages

Started by other programs:
  mur mcp [--agent NAME]        stdio MCP server, one per agent session
  mur hook [AGENT]              an agent's hook: Claude Code's (the default), or codex's, grok's,
                                kiro's, muse's, opencode's or pi's; reads the payload on stdin
  mur hook --event EVENT antigravity
                                Antigravity's hook, for EVENT; reads the payload on stdin
  mur tool --agent hermes TOOL  one call of TOOL for an agent whose plugin runs the tools (Hermes);
                                reads its arguments as JSON on stdin. --list lists the tools
  mur daemon [--git-dir DIR]    the daemon, in the foreground

Help:
  mur help [COMMAND|TOOL]       every command and tool, or one's help
  mur version                   mur's version, and a newer one if its daemon has seen it

Every command takes -C DIR to act on the repository at DIR.
`))

func main() {
	argv := os.Args[1:]
	if desktop.FromBundle() {
		argv = []string{"app", "--from-launcher"} // started from Murmur.app, which passes nothing
	}
	dirs, args, err := leadingDirs(argv)
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
		if agent, event, ok := hookAgent(args); ok && agent == hook.Hermes {
			// It registers the session first (#1184). 3 tells Murmur's plugin, and only it, that it
			// couldn't.
			if !mcpserver.HermesHook(event, os.Stdin, os.Stdout) {
				os.Exit(3)
			}
		} else if ok {
			hook.RunForEvent(agent, event, os.Stdin, os.Stdout)
		}
	case "mcp":
		err = runMCP(ctx, args)
	case "tool":
		err = runTool(ctx, args, os.Stdin, os.Stdout)
		if mcpserver.ToolFailed(err) {
			os.Exit(1) // its text, on stdout, says why
		}
	case "daemon":
		err = runDaemon(ctx, args)
	case "init":
		err = runInit(ctx, args, os.Stdout)
	case "join":
		err = runJoin(ctx, args, os.Stdout)
	case "nickname":
		err = runNickname(ctx, args, os.Stdout)
	case "updates":
		err = runUpdates(ctx, args, os.Stdout)
	case "stop":
		err = runStop(ctx, args, os.Stdout)
	case "rotate":
		err = runRotate(ctx, args, os.Stdout)
	case "owner":
		err = runOwner(ctx, args, os.Stdout)
	case "uninstall":
		err = runUninstall(ctx, args, os.Stdout)
	case "who", "send", "inbox", "status", "lookup", "tags", "mute":
		err = mcpserver.Command(ctx, cmd, args, os.Stdin, os.Stdout, os.Stderr)
	case "ui":
		err = runUI(ctx, args, os.Stdout, browse)
	case "app":
		err = runApp(ctx, args, os.Stdout)
	case "docs":
		err = runDocs(args, os.Stdout)
	case "version", "--version":
		fmt.Println(brand.Command, version)
		noteNewer(ctx, os.Stderr)
	case "help", "-h", "--help":
		err = runHelp(args, os.Stdout)
	default:
		if tool := toolOnly(cmd); tool != "" {
			fmt.Fprintln(os.Stderr, tool)
		} else {
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		}
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
func hookAgent(args []string) (agent, event string, ok bool) {
	fs := flag.NewFlagSet(brand.Command+" hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	a := fs.String("agent", "", "")
	// --event names the event for an agent whose payload doesn't: Antigravity's.
	e := fs.String("event", "", "")
	if fs.Parse(args) != nil {
		return "", "", false
	}
	switch {
	case fs.NArg() == 1 && *a == "":
		*a = fs.Arg(0)
	case fs.NArg() != 0:
		return "", "", false
	}
	if !hook.KnownAgent(*a) {
		return "", "", false
	}
	return *a, *e, true
}

func flags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(brand.Command+" "+name, flag.ExitOnError)
	dir := fs.String("C", ".", "act on the repository at this directory")
	return fs, dir
}

// runTool is `mur tool`: one call of one of Murmur's tools, or with --list the tools, for an agent
// whose plugin runs them (Hermes, #975).
func runTool(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet(brand.Command+" tool", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agent := fs.String("agent", "", "")
	list := fs.Bool("list", false, "")
	usage := fmt.Errorf("usage: %[1]s tool --agent hermes TOOL, with its arguments as JSON on stdin; or %[1]s tool --list", brand.Command)
	if fs.Parse(args) != nil {
		return usage
	}
	if *list {
		if fs.NArg() != 0 {
			return usage
		}
		// The Hermes plugin lists the tools as Hermes' gateway loads it, and is per user, like Pi's
		// extension, and older after an update: brought up to this binary's for the gateway's next start.
		if did, err := join.RefreshHermes(); err != nil {
			fmt.Fprintf(os.Stderr, "%s tool: couldn't update its Hermes plugin: %v\n", brand.Command, err)
		} else if did {
			fmt.Fprintf(os.Stderr, "%s tool: updated its Hermes plugin, which Hermes loads at its gateway's next start\n", brand.Command)
		}
		return mcpserver.ToolList(ctx, stdout)
	}
	if fs.NArg() != 1 || *agent == "" {
		return usage
	}
	return mcpserver.Tool(ctx, *agent, fs.Arg(0), stdin, stdout)
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
	poll := fs.Duration("poll", 0, "how often to check the remote (default 3s)")
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
	updates := &updateSetting{running: version, gitDir: *gitDir, check: make(chan struct{}, 1), install: make(chan string, 1), releases: releases}
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
	themePath, err := web.ThemePath()
	if err != nil {
		logger.Printf("couldn't find the shared theme file: %v", err)
	}
	err = run(ctx, daemon.Options{GitDir: *gitDir, Poll: *poll, Idle: *idle, PageListener: inherited,
		Clones: record, Joined: join.Joined, Repos: repos, ThemePath: themePath,
		Consent: func() bool { return join.Joined(*gitDir) }, Log: logger,
		Update: func(ctx context.Context, restart func()) {
			update.Run(ctx, update.Options{Version: version, GitDir: *gitDir, Log: logger, Running: running, Report: releases.report, Check: updates.check, Install: updates.install}, restart)
			releases.stop()
		},
		Version: releases.page,
		Updates: updates,
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
	noPush := fs.Bool("no-push", false, "only edit the files in this working tree, for you to review and commit; nothing is pushed, so join refuses until a pushing init runs")
	refsOnly := fs.Bool("refs-only", false, "make only the refs/murmur/* refs join looks for: no file, commit or branch changes")
	fs.Parse(args)
	if *noPush && *refsOnly {
		return errors.New("--no-push edits the files and --refs-only leaves them alone; choose one")
	}
	if *noPush {
		return initWorkTree(ctx, *dir, out)
	}
	if *refsOnly {
		return initRefsOnly(ctx, *dir, out)
	}
	// Set up the bus before pushing the invitation, so join works even if the invitation goes to
	// a branch for a pull request or the repo has no branches yet.
	refs, err := setup.RefsOnly(ctx, *dir)
	if err != nil {
		return err
	}
	made := ""
	if len(refs.Made) > 0 {
		var lines strings.Builder
		for _, ref := range refs.Made {
			fmt.Fprintf(&lines, "Made %s on %s.\n", ref, refs.Remote)
		}
		made = lines.String()
	}
	p, err := setup.Push(ctx, *dir)
	if errors.Is(err, setup.ErrNoBranch) {
		// The repo is set up all the same, as --refs-only would leave it, so this is no failure:
		// exit 0, as an agent following setup.md goes on to join. It says the section is missing.
		fmt.Fprintf(out, "%s has no branch yet, so there is no commit to add the section to.\n%s", refs.Remote, made)
		fmt.Fprintf(out, "\nThe repo is set up: `%[1]s join` works here. Nothing in its files mentions %[2]s, so teammates'\n"+
			"agents won't know to join unless someone tells them. For a repo that is to stay without files,\n"+
			"`%[1]s init --refs-only` is the form to use; once it has a first commit, `%[1]s init` adds the section.\n",
			brand.Command, brand.Product)
		return nil
	}
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
		if made == "" {
			fmt.Fprintf(out, "Already set up on %s's %s; nothing pushed.\n", p.Remote, p.Branch)
		} else {
			fmt.Fprintf(out, "%s's %s already has the section.\n%s", p.Remote, p.Branch, made)
		}
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
	fmt.Fprint(out, made)
	if p.Fallback != "" {
		if p.PullURL != "" {
			fmt.Fprintf(out, "\nThe repo can be joined now. Teammates' agents see the section once it is merged: open a pull\n"+
				"request from it, for someone who may merge into %s: %s\n", p.Branch, p.PullURL)
		} else {
			fmt.Fprintf(out, "\nThe repo can be joined now. Teammates' agents see the section once %s is merged into %s;\n"+
				"someone who may push to %s can merge it.\n", p.Fallback, p.Branch, p.Branch)
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

// initRefsOnly is `init --refs-only` (#830): the repo is set up by the shared bus refs, made in their
// own commits, and nothing on any branch mentions Murmur.
func initRefsOnly(ctx context.Context, dir string, out io.Writer) error {
	s, err := setup.RefsOnly(ctx, dir)
	if err != nil {
		return err
	}
	if len(s.Made) == 0 {
		fmt.Fprintf(out, "Already set up: %s has %s; nothing changed.\n", s.Remote, strings.Join(s.Had, " and "))
		return nil
	}
	for _, ref := range s.Made {
		fmt.Fprintf(out, "Made %s on %s.\n", ref, s.Remote)
	}
	if len(s.Had) > 0 {
		fmt.Fprintf(out, "%s was there already and is left as it is.\n", strings.Join(s.Had, " and "))
	}
	fmt.Fprintf(out, "No file, commit or branch changed.\n\n"+
		"Each teammate joins for themselves, with `%[2]s join`. Nothing in the repo mentions %[1]s, so their agents\n"+
		"won't know to ask unless someone tells them. Joining takes %[2]s 0.5.4 or later.\n", brand.Product, brand.Command)
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
	// Nothing is pushed, so join can't find the repo set up yet (#830).
	notYet := fmt.Sprintf("Nothing was pushed, so `%[1]s join` refuses here until someone runs `%[1]s init` or `%[1]s init --refs-only`.\n", brand.Command)
	if len(changed) == 0 {
		fmt.Fprintln(out, "The files here already have the section; nothing changed.")
		fmt.Fprint(out, notYet)
		return nil
	}
	fmt.Fprintf(out, "\nReview and commit these files. A teammate's agent that reads them asks its user whether to join,\n"+
		"and if the user says yes it installs %[1]s and sets it up itself. Committing installs nothing, and %[1]s\n"+
		"does nothing in a clone until its user has joined there.\n", brand.Product)
	fmt.Fprint(out, notYet)
	return nil
}

func runJoin(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("join")
	leave := fs.Bool("leave", false, "undo joining in this clone: forget the consent, remove the hook, stop the daemon")
	outside := fs.Bool("outside-home", false, "write agent settings outside a home in the temporary folder, which join otherwise leaves alone as a test's")
	fs.Parse(args)
	env, err := join.EnvFromProcess()
	if err != nil {
		return err
	}
	env.OutsideHome = *outside
	env.Version = version
	env.Progress = os.Stderr // a line while a step is slow, or a leave waits for the daemon; stdout is the report
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

func runOwner(ctx context.Context, args []string, out io.Writer) error {
	fs, dir := flags("owner")
	allowPlaintext := fs.Bool("allow-plaintext", false, "confirm that turning encryption off writes permanent plaintext")
	fs.Parse(args)
	req := proto.Request{Op: proto.OpOwner}
	switch {
	case fs.NArg() == 0:
	case fs.NArg() == 2 && fs.Arg(0) == "transfer":
		req.Op, req.To = proto.OpTransferOwner, fs.Arg(1)
	case fs.Arg(0) == "mode" && fs.NArg() <= 2:
		req.Op, req.EncryptionMode, req.AllowPlaintext = proto.OpRepoMode, fs.Arg(1), *allowPlaintext
		if req.EncryptionMode == bus.EncryptionOff && !req.AllowPlaintext {
			return errors.New("Off writes permanent plaintext. Anyone with access to the remote can read it. In a public repo that includes anyone on the internet. To confirm, run mur owner --allow-plaintext mode off.")
		}
	default:
		return errors.New("use mur owner, mur owner transfer DEVICE, or mur owner mode [team|private|off]")
	}
	gitDir, err := client.FindGitDir(ctx, *dir)
	if err != nil {
		return err
	}
	c, err := client.Connect(ctx, gitDir, true)
	if err != nil {
		return err
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Fprintln(out, resp.Note)
	if req.Op == proto.OpRepoMode && req.EncryptionMode != "" {
		fmt.Fprintln(out, "Existing messages keep the protection they had when written.")
	}
	return nil
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
	list, err := collaborators.List(ctx, addr)
	if err != nil {
		return nil, err.Error()
	}
	return list, ""
}

func runUninstall(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(brand.Command+" uninstall", flag.ExitOnError)
	leave := fs.Bool("leave", false, "leave every repo on this machine that is still joined, first")
	outside := fs.Bool("outside-home", false, "change agent settings outside a home in the temporary folder, which uninstall otherwise leaves alone as a test's")
	fs.Parse(args)
	if fs.NArg() > 0 {
		return errors.New("uninstall takes no arguments, only --leave and --outside-home")
	}
	env, err := join.EnvFromProcess()
	if err != nil {
		return err
	}
	env.OutsideHome = *outside
	env.Version = version
	env.Progress = os.Stderr // a line while a leave waits for a daemon; stdout is the report
	env.RemoveApp = uninstallApp
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
	stopped, err := client.StopWaiting(ctx, gitDir)
	if err != nil {
		return err
	}
	if !stopped {
		fmt.Fprintln(out, "No daemon is running for this clone.")
		return nil
	}
	fmt.Fprintln(out, "Stopped the daemon.")
	if appShows(gitDir) {
		// The window starts its clone's daemon again within desktop.FollowEvery (#639): someone who
		// stopped it on purpose would otherwise see it come back with no word why.
		fmt.Fprintf(out, "%s's app has this repo open, so it starts the daemon again within %d seconds.\n",
			brand.Product, int(desktop.FollowEvery/time.Second))
	}
	return nil
}

// appShows says the app's window is open on the clone at gitDir: its record names a live window,
// the clone it last showed (last.json) is this one, and the clone hasn't left, as the window starts
// no daemon for one that has. Neither file is made if it isn't there. last.json can lag the window
// by up to desktop.FollowEvery: a move from the page's repo menu is recorded at the window's next
// check, and a window just opened records its clone once that clone's daemon has started.
func appShows(gitDir string) bool {
	if !join.Joined(gitDir) {
		return false
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return false
	}
	dir := filepath.Join(cfg, brand.Name, "app")
	shown := lastClone(dir)
	if shown == "" || !sameDir(shown, gitDir) {
		return false
	}
	return desktop.WindowOpen(filepath.Join(dir, "window.json"))
}

// sameDir says a and b are one directory, however each is written.
func sameDir(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}
