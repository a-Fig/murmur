package join

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/clones"
	"github.com/a-Fig/murmur/internal/publicrepo"
	"github.com/a-Fig/murmur/internal/t3"
)

// Env is the user's machine as join sees it: where it writes, which of the agent programs are
// installed, and which binary the agents should run. EnvFromProcess reads the real one; tests build
// theirs over temporary directories, so none of them can touch the real user's settings.
type Env struct {
	Exe        string // this binary, by absolute path: the agents run it as the MCP server
	ClaudeDir  string // Claude Code's config directory, where its settings.json is
	ClaudeJSON string // the file Claude Code keeps user-scope MCP servers in
	CodexHome  string // Codex's home, where its config.toml is
	// CodexHomeDefault says CODEX_HOME is unset, so CodexHome is ~/.codex.
	CodexHomeDefault bool
	// The other agents' configuration directories, each "" for an agent join leaves alone (a test
	// that isn't about it).
	OpenCodeDir string // OpenCode's, with its opencode.json
	MuseDir     string // Muse Code's, with its settings.json
	PiDir       string // Pi's agent directory
	GrokHome    string // Grok's home, with its config.toml
	GeminiHome  string // Antigravity's home, with its config/mcp_config.json
	Home        string // the user's home directory, for the homes T3 Code's settings name with ~ or leave to the default
	T3Settings  string // T3 Code's settings.json, "" without T3: the other agent homes T3 runs (t3homes.go)
	Getenv      func(string) string
	LookPath    func(string) (string, error)
	// Run runs a program and returns what it printed, for asking an agent its version (OpenCode's,
	// whose two versions want different configs); nil, as tests leave it, runs nothing, and the
	// version isn't known.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Version is mur's, for reporting a public repo (internal/publicrepo); "", as tests leave it,
	// reports nothing.
	Version string
	// Progress is where a join or a leave says what it is still waiting for, before the report;
	// nil, as most tests leave it, says nothing.
	Progress io.Writer
	// RemoveApp, for uninstall, takes out the desktop app's launcher and its folder in Murmur's
	// folder in the config directory (cmd/mur/app.go), adding to the report, and returns what it left
	// for the user. It runs just before that folder is removed, which its own would otherwise keep
	// (#746). nil, as tests leave it, does nothing.
	RemoveApp func(r *Report) (left []string)
	// OutsideHome lets join and uninstall change configurations outside a home in the temporary
	// folder, which they otherwise leave alone as a test's (scratch.go, #995).
	OutsideHome bool
}

// EnvFromProcess is the environment of the running process.
func EnvFromProcess() (Env, error) {
	exe, err := os.Executable()
	if err != nil {
		return Env{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, err
	}
	e := Env{Exe: exe, Home: home, Getenv: os.Getenv, LookPath: exec.LookPath, Run: runProgram,
		T3Settings: t3SettingsPath(servingT3(os.Getenv), os.Getenv("T3CODE_HOME"))}
	// Claude Code puts everything under CLAUDE_CONFIG_DIR when it is set (a second account, a T3
	// provider with its own home); else its settings are in ~/.claude and its state in ~/.claude.json.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		e.ClaudeDir, e.ClaudeJSON = dir, filepath.Join(dir, ".claude.json")
	} else {
		e.ClaudeDir, e.ClaudeJSON = filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
	}
	if e.CodexHome = os.Getenv("CODEX_HOME"); e.CodexHome == "" {
		e.CodexHome, e.CodexHomeDefault = filepath.Join(home, ".codex"), true
	}
	// OpenCode and Muse follow XDG on every OS: ~/.config, not %APPDATA%, on Windows too.
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	e.OpenCodeDir, e.MuseDir = filepath.Join(config, "opencode"), filepath.Join(config, "muse")
	e.PiDir = piDir(os.Getenv, home)
	if e.GrokHome = os.Getenv("GROK_HOME"); e.GrokHome == "" {
		e.GrokHome = filepath.Join(home, ".grok")
	}
	if e.GeminiHome = os.Getenv("GEMINI_HOME"); e.GeminiHome == "" {
		e.GeminiHome = filepath.Join(home, ".gemini")
	}
	return e, nil
}

// runProgram runs name with args, no input, and returns its output. A program that leaves a child
// holding its output open (an npm shim's node, killed at the deadline) is waited for a second more,
// not forever.
func runProgram(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e Env) look(name string) (string, error) {
	if e.LookPath == nil {
		return "", exec.ErrNotFound
	}
	return e.LookPath(name)
}

func (e Env) getenv(key string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(key)
}

// Report is what a join or a leave did and what the agent that ran it should pass on.
type Report struct {
	Did      []string // what changed
	Notes    []string // what was passed over, and why
	Tell     []string // what the agent tells its user, one paragraph each
	Problems []string // what didn't work; the command fails if there are any
	Left     []string // what an uninstall leaves for the user to remove
}

// String is the text the command prints.
func (r Report) String() string {
	var b strings.Builder
	if len(r.Did) > 0 {
		b.WriteString("Done:\n")
		for _, d := range r.Did {
			b.WriteString("  - " + d + "\n")
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\nNot done:\n")
		for _, n := range r.Notes {
			b.WriteString("  - " + n + "\n")
		}
	}
	if len(r.Problems) > 0 {
		b.WriteString("\nProblems:\n")
		for _, p := range r.Problems {
			b.WriteString("  - " + p + "\n")
		}
	}
	if len(r.Left) > 0 {
		b.WriteString("\nLeft for you:\n")
		for _, l := range r.Left {
			b.WriteString("  - " + l + "\n")
		}
	}
	if len(r.Tell) > 0 {
		b.WriteString("\nTell your user:\n")
		for _, t := range r.Tell {
			b.WriteString("  " + t + "\n")
		}
	}
	return b.String()
}

// Join does what a user's yes to joining means for the clone containing dir. It is safe to run
// again, and a rerun only fills in what is missing.
func Join(ctx context.Context, e Env, dir string) (Report, error) {
	var r Report
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, joinWait)
	defer cancel()
	p, err := Locate(ctx, dir)
	if err != nil {
		return r, err
	}
	if !p.Marked() {
		return r, fmt.Errorf("this repo doesn't say it uses %[1]s: neither AGENTS.md nor CLAUDE.md has the %[1]s section, "+
			"here or on the default branch as this clone last fetched it. `%[2]s init` adds it and pushes it to the default branch, "+
			"after the user agrees (%[3]s)", brand.Product, brand.Command, brand.SetupDoc)
	}
	// A step cut short by the deadline fails with it; the problem added at the end says so once.
	cut := func() bool { return ctx.Err() != nil }
	problem := func(what string, err error) {
		if !cut() {
			r.Problems = append(r.Problems, what+": "+err.Error())
		}
	}
	steps := stepper{w: e.Progress, now: "reading which agents this machine has"}
	defer steps.stop()

	// Per user: the MCP server, registered once for every repo this user opens, in every
	// configuration of each agent. Advice about the calling agent depends on its own home.
	byAgent, notes := e.homes()
	r.Notes = append(r.Notes, notes...)
	var claudeFresh, codexFresh, antigravityFresh bool
	var othersFresh []string
	claudeOK := true // no Claude Code home failed to take what join writes there, the hook included
	for i, a := range agents {
		for j, h := range byAgent[i] {
			if cut() {
				break
			}
			if h.Dir == "" { // an agent this Env leaves out, or a home left alone (scratch.go)
				// The older hooks in the working trees stay for a Claude Code home not written.
				claudeOK = claudeOK && a.driver != "claudeAgent"
				continue
			}
			if h.T3Own && !isDir(h.Dir) {
				r.Notes = append(r.Notes, h.Label+": open a thread with this instance to set it up, then run `"+brand.Command+" join` again.")
				continue
			}
			steps.start("adding " + brand.Product + " to " + h.Label)
			did, fresh, err := a.register(e, ctx, h)
			r.Did = append(r.Did, did...)
			if note := approvalNote(""); errors.As(err, &note) {
				r.Notes = append(r.Notes, h.Label+": "+err.Error())
			} else if err != nil {
				problem(h.Label, err)
				claudeOK = claudeOK && a.driver != "claudeAgent"
			}
			// A problem registering the server doesn't prevent installing skills.
			if dir := e.joinSkillsDir(a, h, byAgent[i]); dir != "" {
				did, notes, err := installSkills(dir, h.Label)
				r.Did, r.Notes = append(r.Did, did...), append(r.Notes, notes...)
				if err != nil {
					problem(h.Label+" skills", err)
				}
			}
			switch {
			case !fresh:
			case j == 0 && a.driver == "claudeAgent":
				claudeFresh = true
			case j == 0 && a.driver == "codex":
				codexFresh = true
			case j == 0 && a.driver == "pi" && e.looksLikePi():
				// The advice for the Pi running join covers it.
			// The advice for the Antigravity running join covers its own home. Its other homes
			// (~/.gemini, another instance) fall through, so their open sessions hear to restart.
			case a.driver == "antigravity" && e.looksLikeAntigravity() && sameEntry(h.Dir, e.GeminiHome):
				antigravityFresh = true
			case a.driver != "claudeAgent" && a.driver != "codex" && !slices.Contains(othersFresh, a.label):
				othersFresh = append(othersFresh, a.label)
			}
		}
	}

	// Claude Code's hook is per user now, written above; one an older join put in the clone's
	// working trees comes out, or Claude Code would run both (#422). Not where a home couldn't be
	// written: the older hook is then the only one its sessions have, and stays for the next join.
	if claudeOK && !cut() {
		steps.start("looking for hooks an older " + brand.Command + " put in this clone's working trees")
		trees, hookNotes, err := removeHooks(ctx, p, e.claudeSettings())
		r.Notes = append(r.Notes, hookNotes...)
		for _, t := range trees {
			r.Did = append(r.Did, "Removed the hook an older "+brand.Command+" put in "+filepath.Join(t, filepath.FromSlash(hookFile))+": it is in Claude Code's own settings now")
		}
		if err != nil {
			problem("hook", err)
		}
	}

	// Consent comes before the daemon: the daemon refuses to run in a clone that hasn't joined.
	steps.stop()
	if cut() {
		return e.cutShort(r, parent, steps.now, false)
	}
	news, consentErr := p.RecordConsent()
	if consentErr != nil {
		problem("recording that you joined", consentErr)
	} else if news {
		r.Did = append(r.Did, "Recorded that you joined "+brand.Product+" in this clone ("+p.consentPath()+")")
	}
	// On the record of this machine's clones, for the page's repo picker (#348). The daemon adds
	// its clone too as it starts; this is for one that doesn't start.
	if consentErr == nil {
		if dir, err := clones.DefaultDir(); err == nil {
			err = clones.Add(dir, clones.Clone{GitDir: p.GitDir, Name: clones.NameIn(ctx, p.GitDir)})
			if err != nil {
				r.Notes = append(r.Notes, "This clone isn't on the record of this machine's clones, so another repo's page can't switch to it until its daemon starts: "+err.Error())
			}
		}
	}
	// A daemon left running by an older mur answers, but can't do what this one does (hand a message
	// over as a turn, for one): it is replaced. One from before proof (0.2) can't be asked to stop,
	// and Replace ends it by its process (#394).
	steps.start("starting this clone's daemon")
	c, err := client.Connect(ctx, p.GitDir, false)
	switch {
	case errors.Is(err, client.ErrNoDaemon):
		if _, err := client.Connect(ctx, p.GitDir, true); err != nil {
			problem("starting the daemon", err)
		} else {
			r.Did = append(r.Did, "Started this clone's daemon")
		}
	case err == nil && c.Outdated():
		if _, err := client.Replace(ctx, p.GitDir); err != nil {
			problem("replacing the daemon of an older version", err)
		} else {
			r.Did = append(r.Did, "Replaced this clone's daemon, which was left running by an older version of "+brand.Product)
		}
	}

	steps.stop()
	if cut() {
		return e.cutShort(r, parent, steps.now, consentErr == nil)
	}

	r.Tell = e.advice(claudeFresh, codexFresh, antigravityFresh, len(r.Problems) > 0)
	if consentErr == nil {
		// Even when something else failed: with consent recorded, a daemon may report the repo.
		// Before the last paragraph, which is about the page and leaving, or about the problems.
		last := len(r.Tell) - 1
		r.Tell = append(r.Tell[:last], publicrepo.Statement, r.Tell[last])
		// Best effort, so the deadline cutting it short fails nothing: a daemon tries again.
		steps.start("asking GitHub whether this repo is public, to report it if it is")
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if res, _ := publicrepo.Once(rctx, publicrepo.Options{Version: e.Version, GitDir: p.GitDir}); res == publicrepo.Reported {
			r.Did = append(r.Did, publicrepo.Told)
		}
		cancel()
		steps.stop()
	}
	if len(othersFresh) > 0 && len(r.Problems) == 0 {
		// Before the last paragraph, which is about the page and leaving.
		last := len(r.Tell) - 1
		r.Tell = append(r.Tell[:last], othersAdvice(othersFresh), r.Tell[last])
	}
	if len(r.Did) == 0 && len(r.Problems) == 0 {
		r.Did = []string{"Nothing to do: this clone had already joined " + brand.Product}
	}
	if len(r.Problems) > 0 {
		return r, errors.New("joined, but not everything worked; see above")
	}
	return r, nil
}

// How long a join may take in all, and how long one step runs before the join says it is still on
// it. Agents run join under a timeout of their own, and one that kills it sees nothing of what it
// was doing (#829): join gives up first, in time to say where. Variables, for the tests.
var (
	joinWait  = 90 * time.Second
	joinQuiet = 5 * time.Second
)

// stepper keeps the step a join is on. One that runs past joinQuiet says so on w.
type stepper struct {
	w    io.Writer
	now  string // the step, as in "still <now>"
	done func()
}

func (s *stepper) start(what string) {
	s.stop()
	s.now = what
	s.done = sayIfSlow(s.w, joinQuiet, brand.Command+" join: still "+what+"\n")
}

func (s *stepper) stop() {
	if s.done != nil {
		s.done()
		s.done = nil
	}
}

// cutShort ends a join the deadline, or the caller, stopped while it was on step: with a problem that
// says where it stopped. Each run gets further, since it only does what is missing, so unlike other
// problems the advice is to run it again before telling the user (Tyler, on #839). Once the user has
// joined, a daemon may report the repo, so the advice says so as a finished join's does.
func (e Env) cutShort(r Report, parent context.Context, step string, joined bool) (Report, error) {
	if parent.Err() != nil {
		r.Problems = append(r.Problems, "stopped while "+step)
	} else {
		r.Problems = append(r.Problems, fmt.Sprintf("gave up after %d seconds, while %s", int(joinWait/time.Second), step))
	}
	r.Tell = []string{"Joining stopped partway. Run `" + brand.Command + " join` again: it picks up where it stopped. If it stops again, tell your user what is under Problems."}
	if joined {
		r.Tell = append([]string{publicrepo.Statement}, r.Tell...)
	}
	return r, errors.New("joining didn't finish; see above")
}

// Leave undoes a join for the clone containing dir: the consent, the daemon, and the hook an older
// join put in each working tree. The MCP server and the hooks stay with the agents, which is per
// user, and do nothing in a repo that hasn't joined.
func Leave(ctx context.Context, e Env, dir string) (Report, error) {
	p, err := Locate(ctx, dir)
	if err != nil {
		// dir may be the git directory of a clone made with --separate-git-dir, which is where
		// uninstall's refusal names such a clone (Clone.Top): no working tree is found from it,
		// but it can be left from there (#749).
		gitDir, gerr := client.FindGitDir(ctx, dir)
		abs, aerr := filepath.Abs(dir)
		if gerr != nil || aerr != nil || !sameEntry(gitDir, abs) {
			return Report{}, err
		}
		p = Place{GitDir: gitDir}
	}
	return e.leavePlace(ctx, p)
}

// leavePlace is Leave for p. A Place with no Top, a git directory whose working tree can't be found
// from it, is left all the same but for the hooks an older join put in its working trees.
func (e Env) leavePlace(ctx context.Context, p Place) (Report, error) {
	var r Report
	problem := func(what string, err error) { r.Problems = append(r.Problems, what+": "+err.Error()) }

	// The consent first, so everything that checks it stops from here on, including a server that
	// would start the daemon again the moment it is stopped.
	if had, err := p.ForgetConsent(); err != nil {
		problem("removing the record that you joined", err)
	} else if had {
		r.Did = append(r.Did, "Removed the record that you joined "+brand.Product+" in this clone")
	}
	if dir, err := clones.DefaultDir(); err == nil {
		if err := clones.Remove(dir, p.GitDir); err != nil {
			problem("taking this clone off the record of this machine's clones", err)
		}
	}
	if p.Top != "" {
		trees, notes, err := removeHooks(ctx, p, e.claudeSettings())
		r.Notes = append(r.Notes, notes...)
		for _, t := range trees {
			r.Did = append(r.Did, "Removed the hook from "+filepath.Join(t, filepath.FromSlash(hookFile)))
		}
		if err != nil {
			problem("hook", err)
		}
	}
	// A daemon asked to stop holds the clone's lock until it has finished its work and said goodbye,
	// which may take client.StopWait, and so may one still starting, which sees the consent gone
	// within a second and stops the same way. The lock is waited for that long from when the stop
	// was asked, so a daemon only taking its time isn't reported as a problem (#91). A long wait is
	// said out loud, so that the leave doesn't sit silent for half a minute.
	end := time.Now().Add(leaveWait)
	waited := sayIfSlow(e.Progress, leaveQuiet, fmt.Sprintf("Waiting for this clone's daemon to stop: it finishes what it is doing first, which can take up to %d seconds.\n", int(leaveWait/time.Second)))
	stopped, stopErr := client.Stop(ctx, p.GitDir)
	if stopErr != nil {
		problem("stopping the daemon", stopErr)
	}
	// A daemon that is still starting has no daemon.json to stop; it sees the consent gone and exits
	// by itself, which is waited for here so a leave doesn't return while one is about to publish.
	starting, err := waitGone(ctx, p.GitDir, time.Until(end))
	waited()
	if err != nil {
		problem("waiting for the daemon to exit", fmt.Errorf("%w; it has no consent left and refuses requests, and stops by itself", err))
	}
	if stopped || starting {
		if stopErr == nil && err == nil {
			r.Did = append(r.Did, "Stopped this clone's daemon")
		}
	}

	if len(r.Did) == 0 && len(r.Problems) == 0 {
		r.Did = []string{"Nothing to undo: this clone hadn't joined " + brand.Product}
	}
	r.Tell = []string{
		brand.Product + " is off for this repo. Open Claude Code, Pi and Antigravity sessions lose its tools within seconds. In Codex, the tools stay listed until the session ends and report that they are off if called.",
		"The " + brand.Product + " MCP server stays registered with the agents, with its hook and its skills, since that is per user. The server and the hook do nothing in a repo that hasn't joined; " +
			"each skill costs every session of those agents a line of context, in any repo. " +
			"`" + brand.Command + " uninstall` removes them, and the rest of " + brand.Product + ", from this machine once no repo here is joined. " +
			"To remove them by hand: " + e.removal() + ".",
	}
	if len(r.Problems) > 0 {
		return r, errors.New("not everything worked; see above")
	}
	return r, nil
}

// How long a leave waits for the daemon to let go of the clone, how long it waits before saying so,
// and the wait itself. Variables, for the tests: the wait's, so that a test acts once the leave is
// waiting rather than after a guessed time, which the git commands before it can outlast.
var (
	leaveWait  = client.StopWait
	leaveQuiet = 5 * time.Second
	waitGone   = client.WaitGone
)

// sayIfSlow writes line to w unless the function it returns is called within d. That function
// returns only once nothing more will be written. A nil w says nothing.
func sayIfSlow(w io.Writer, d time.Duration, line string) func() {
	if w == nil {
		return func() {}
	}
	done, said := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(said)
		select {
		case <-done:
		case <-time.After(d):
			io.WriteString(w, line)
		}
	}()
	return func() {
		close(done)
		<-said
	}
}

// othersAdvice is what to tell about the agents other than Claude Code and Codex that Murmur was
// just added to, by what each was seen to do (2026-11-01): OpenCode watches its config, Muse reads
// its servers when a session starts, as does Antigravity (1.5.13, 2026-11-20). Pi reads them then
// and on /reload in its terminal (2026-11-17, #864). Grok documents loading them at startup.
func othersAdvice(labels []string) string {
	s := brand.Product + " was just added to " + strings.Join(labels, ", ") + "."
	if slices.Contains(labels, "OpenCode") {
		s += " OpenCode sessions get its tools within seconds, from their next turn."
	}
	var restart []string
	for _, l := range labels {
		if l != "OpenCode" && l != "Pi" {
			restart = append(restart, l)
		}
	}
	if len(restart) > 0 {
		verb := " loads"
		if len(restart) > 1 {
			verb = " load"
		}
		s += " " + strings.Join(restart, ", ") + verb + " MCP servers only when a session starts: a session already open needs a new one (in T3 Code, a new thread)."
	}
	if slices.Contains(labels, "Pi") {
		s += " In a Pi session already open, type /reload to get its tools (in T3 Code, open a new thread)."
	}
	return s
}

// inT3 says whether join runs in a session T3 runs: T3's app names its command line
// (T3CODE_CLI_PATH), and its headless server gives the thread's ID alone (t3.ThreadEnv, #790).
func (e Env) inT3() bool {
	return e.getenv(t3CLIEnv) != "" || e.getenv(t3.ThreadEnv) != ""
}

// servingT3 is T3's command line for the session running join: T3CODE_CLI_PATH where T3's app set
// it; in a thread of its headless server, which names none, the t3 that runs it, a few processes up
// (join runs in the agent's shell, under the agent T3 started); else "".
func servingT3(getenv func(string) string) string {
	if cli := getenv(t3CLIEnv); cli != "" || getenv(t3.ThreadEnv) == "" {
		return cli
	}
	return t3.Above(os.Getpid(), 8, exec.LookPath)
}

// advice is what to tell the user after a join, by what this machine and this agent need. Which
// agent is running join decides it: the MCP server is registered once, so a session that started
// after that already runs it (idle, with no tools, until the join), and one that started before
// has never heard of it. What each kind of session picks up was measured, 2026-10-29
// (docs/design/joining.md, "Reloading MCP servers").
func (e Env) advice(claudeFresh, codexFresh, antigravityFresh, problems bool) []string {
	if problems {
		return []string{"Joining didn't finish: tell your user what is under Problems. Running `" + brand.Command + " join` again retries, and only does what is missing."}
	}
	var out []string
	switch {
	case e.getenv("CLAUDECODE") == "1" && e.inT3() && claudeFresh:
		out = append(out, brand.Product+" was just added to Claude Code, and this conversation started before that, so it doesn't have the tools yet. "+
			"To load them: press Stop in T3, then send any message (T3 restarts Claude with "+brand.Product+" loaded); starting a new thread works too.")
	case e.getenv("CLAUDECODE") == "1" && claudeFresh:
		out = append(out, brand.Product+" was just added to Claude Code, and this conversation started before that, so it doesn't have the tools yet. "+
			"To load them: exit, and run `claude --continue` in this repo, which resumes this conversation with "+brand.Product+" loaded.")
	case e.getenv("CLAUDECODE") == "1":
		out = append(out, brand.Product+" is on for this repo. Its tools (mcp__"+brand.MCPServer+"__who, status, send, inbox) show up in this conversation "+
			"within a few seconds. If they haven't after a minute, start a new conversation.")
	// Pi loads extensions as a session starts, and again on /reload, a command of its terminal
	// UI that T3's Pi, in RPC mode, doesn't have; once loaded, the server's tools come and go
	// with tools/list_changed (Pi 0.99.0, 2026-11-17, #864). Whether this session loaded the
	// extension can't be told from here: one that started before any join wrote it, even another
	// session's, needs /reload. So the advice is the same whether or not this join wrote it.
	case e.looksLikePi() && e.inT3():
		out = append(out, brand.Product+" is on for this repo. If this thread doesn't have its tools (mcp__"+brand.MCPServer+"__who, status, send, inbox), "+
			"open a new thread in this project: Pi in T3 Code loads them only as a thread starts.")
	case e.looksLikePi():
		out = append(out, brand.Product+" is on for this repo. If this session doesn't have its tools (mcp__"+brand.MCPServer+"__who, status, send, inbox), "+
			"type /reload in Pi to load them.")
	case e.looksLikeCodex() && e.inT3():
		// T3's Codex provider loads the server in a thread created after it was registered, and
		// never in an older one, which T3 doesn't refresh (docs/design/joining.md, "Reloading MCP
		// servers").
		out = append(out, brand.Product+" is on for this repo, but Codex loads MCP servers only in a thread that starts after they were added, so this thread doesn't have the tools: "+
			"open a new thread in this project to talk to other agents through "+brand.Product+".")
	case e.looksLikeCodex():
		out = append(out, brand.Product+" is on for this repo. Codex loads MCP servers only when a session starts, so this session doesn't have the tools: "+
			"start a new session in this repo to get them.")
	case e.looksLikeAntigravity():
		restart := "restart `agy` in this repo"
		if e.inT3() {
			restart = "open a new thread in this project"
		}
		if antigravityFresh {
			out = append(out, brand.Product+" was just added to Antigravity. To load its tools, "+restart+".")
		} else {
			out = append(out, brand.Product+" is on for this repo. Antigravity should show its tools within a few seconds. If they're still missing after a minute, "+restart+".")
		}
	default:
		out = append(out, brand.Product+" is on for this repo. Claude Code sessions pick up its tools within seconds if "+brand.Product+" was already registered with Claude Code, "+
			"and otherwise after a restart (press Stop and send a message in T3; `claude --continue` in a terminal). "+
			"Codex loads them only in a new thread or session.")
	}
	if codexFresh && !e.looksLikeCodex() {
		out = append(out, "Codex sessions already open won't have it; ones started from now on will.")
	}
	return append(out, "They can run `"+brand.Command+" ui` to see the agents and messages on a page. To stop, `"+brand.Command+" join --leave` in this repo.")
}

// Claude Code, Codex or Pi started from Antigravity inherit its shell markers and take precedence.
// The markers are the same in reverse, so Antigravity started from one of them gets their advice.
func (e Env) looksLikeAntigravity() bool {
	return (e.getenv("ANTIGRAVITY_AGENT") == "1" || e.getenv("ANTIGRAVITY_CONVERSATION_ID") != "") &&
		e.getenv("CLAUDECODE") != "1" && !e.looksLikeCodex() && !e.looksLikePi()
}

// looksLikePi reports whether the agent running join is Pi, which sets PI_CODING_AGENT=true for
// every process it starts (its docs/environment-variables.md, 0.99.0). A Claude Code or Codex that
// a Pi started inherits it, so their own variables win (#870's review). The variables can't say
// which agent started which, so a Pi run from Claude Code's or Codex's shell, the rarer case, gets
// their advice. No variable tells a Pi under Codex from a Codex under Pi: Pi also sets AI_AGENT=pi,
// but Codex (0.169.0) sets none, so both see AI_AGENT=pi, PI_CODING_AGENT and CODEX_THREAD_ID.
func (e Env) looksLikePi() bool {
	return e.getenv("PI_CODING_AGENT") == "true" && e.getenv("CLAUDECODE") != "1" && !e.looksLikeCodex()
}

// looksLikeCodex reports whether the agent running join is Codex. The variables are the ones Codex
// is believed to set for the commands it runs; no Codex session's environment has been checked
// (docs/design/joining.md, "Reloading MCP servers"), so this may miss one, and advice then falls
// back to covering every client.
func (e Env) looksLikeCodex() bool {
	for _, k := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_CI"} {
		if e.getenv(k) != "" {
			return true
		}
	}
	return false
}
