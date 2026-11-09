package join

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-Fig/murmur/internal/brand"
	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/publicrepo"
)

// Env is the user's machine as join sees it: where it writes, which of the agent programs are
// installed, and which binary the agents should run. EnvFromProcess reads the real one; tests build
// theirs over temporary directories, so none of them can touch the real user's settings.
type Env struct {
	Exe        string // this binary, by absolute path: the agents run it as the MCP server
	ClaudeDir  string // Claude Code's config directory, where its settings.json is
	ClaudeJSON string // the file Claude Code keeps user-scope MCP servers in
	CodexHome  string // Codex's home, where its config.toml is
	// The other agents' configuration directories, each "" for an agent join leaves alone (a test
	// that isn't about it).
	OpenCodeDir string // OpenCode's, with its opencode.json
	MuseDir     string // Muse Code's, with its settings.json
	PiDir       string // Pi's agent directory
	GrokHome    string // Grok's home, with its config.toml
	Home        string // the user's home directory, for the homes T3 Code's settings name with ~ or leave to the default
	T3Settings  string // T3 Code's settings.json, "" without T3: the other agent homes T3 runs (t3homes.go)
	Getenv      func(string) string
	LookPath    func(string) (string, error)
	// Version is mur's, for reporting a public repo (internal/publicrepo); "", as tests leave it,
	// reports nothing.
	Version string
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
	e := Env{Exe: exe, Home: home, Getenv: os.Getenv, LookPath: exec.LookPath,
		T3Settings: t3SettingsPath(os.Getenv("T3CODE_CLI_PATH"), os.Getenv("T3CODE_HOME"))}
	// Claude Code puts everything under CLAUDE_CONFIG_DIR when it is set (a second account, a T3
	// provider with its own home); else its settings are in ~/.claude and its state in ~/.claude.json.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		e.ClaudeDir, e.ClaudeJSON = dir, filepath.Join(dir, ".claude.json")
	} else {
		e.ClaudeDir, e.ClaudeJSON = filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
	}
	if e.CodexHome = os.Getenv("CODEX_HOME"); e.CodexHome == "" {
		e.CodexHome = filepath.Join(home, ".codex")
	}
	// OpenCode and Muse follow XDG on every OS: ~/.config, not %APPDATA%, on Windows too.
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	e.OpenCodeDir, e.MuseDir = filepath.Join(config, "opencode"), filepath.Join(config, "muse")
	// Pi reads a leading ~ in PI_CODING_AGENT_DIR as the home directory (Codex's review of #136).
	if e.PiDir = os.Getenv("PI_CODING_AGENT_DIR"); strings.HasPrefix(e.PiDir, "~") {
		e.PiDir = expandHome(e.PiDir, home)
	}
	if e.PiDir == "" {
		e.PiDir = filepath.Join(home, ".pi", "agent")
	}
	if e.GrokHome = os.Getenv("GROK_HOME"); e.GrokHome == "" {
		e.GrokHome = filepath.Join(home, ".grok")
	}
	return e, nil
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
	p, err := Locate(ctx, dir)
	if err != nil {
		return r, err
	}
	if !p.Marked() {
		return r, fmt.Errorf("this repo doesn't say it uses %[1]s: neither AGENTS.md nor CLAUDE.md has the %[1]s section, "+
			"here or on the default branch as this clone last fetched it. `%[2]s init` adds it and pushes it to the default branch, "+
			"after the user agrees (%[3]s)", brand.Product, brand.Command, brand.SetupDoc)
	}
	problem := func(what string, err error) { r.Problems = append(r.Problems, what+": "+err.Error()) }

	// Per user: the MCP server, registered once for every repo this user opens, in every
	// configuration of each agent. Only the default ones' news shapes the advice: those are the
	// agent running join's.
	byAgent, notes := e.homes()
	r.Notes = append(r.Notes, notes...)
	var claudeFresh, codexFresh bool
	var othersFresh []string
	for i, a := range agents {
		for j, h := range byAgent[i] {
			did, fresh, err := a.register(e, ctx, h)
			r.Did = append(r.Did, did...)
			if err != nil {
				problem(h.Label, err)
			}
			// The home exists once registering found the agent installed, even where it then
			// reported a problem (Pi's conflicting mcp.json, say): the skills don't depend on it.
			if dir := skillsDir(a, h); dir != "" && isDir(h.Dir) {
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
			case a.driver != "claudeAgent" && a.driver != "codex" && !slices.Contains(othersFresh, a.label):
				othersFresh = append(othersFresh, a.label)
			}
		}
	}

	// Per repo: the hook that shows incoming messages, in every working tree of the clone.
	trees, err := installHooks(ctx, p, e.Exe)
	for _, t := range trees {
		r.Did = append(r.Did, "Installed the hook in "+filepath.Join(t, filepath.FromSlash(hookFile))+" (not committed)")
	}
	if err != nil {
		problem("hook", err)
	}

	// Consent comes before the daemon: the daemon refuses to run in a clone that hasn't joined.
	news, consentErr := p.RecordConsent()
	if consentErr != nil {
		problem("recording that you joined", consentErr)
	} else if news {
		r.Did = append(r.Did, "Recorded that you joined "+brand.Product+" in this clone ("+p.consentPath()+")")
	}
	// A daemon left running by an older mur answers, but can't do what this one does (hand a message
	// over as a turn, for one): it is replaced, which Connect does for a client that may start daemons.
	c, err := client.Connect(ctx, p.GitDir, false)
	switch {
	case errors.Is(err, client.ErrNoDaemon):
		if _, err := client.Connect(ctx, p.GitDir, true); err != nil {
			problem("starting the daemon", err)
		} else {
			r.Did = append(r.Did, "Started this clone's daemon")
		}
	case err == nil && c.Outdated():
		if _, err := client.Connect(ctx, p.GitDir, true); err != nil {
			problem("replacing the daemon of an older version", err)
		} else {
			r.Did = append(r.Did, "Replaced this clone's daemon, which was left running by an older version of "+brand.Product)
		}
	}

	r.Tell = e.advice(claudeFresh, codexFresh, len(r.Problems) > 0)
	if consentErr == nil {
		// Even when something else failed: with consent recorded, a daemon may report the repo.
		// Before the last paragraph, which is about the page and leaving, or about the problems.
		last := len(r.Tell) - 1
		r.Tell = append(r.Tell[:last], publicrepo.Statement, r.Tell[last])
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if res, _ := publicrepo.Once(rctx, publicrepo.Options{Version: e.Version, GitDir: p.GitDir}); res == publicrepo.Reported {
			r.Did = append(r.Did, publicrepo.Told)
		}
		cancel()
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

// Leave undoes a join for the clone containing dir: the consent, the hook in each working
// tree, and the daemon. The MCP server stays registered with the agents, which is per user, and does
// nothing in a repo that hasn't joined.
func Leave(ctx context.Context, e Env, dir string) (Report, error) {
	var r Report
	p, err := Locate(ctx, dir)
	if err != nil {
		return r, err
	}
	problem := func(what string, err error) { r.Problems = append(r.Problems, what+": "+err.Error()) }

	// The consent first, so everything that checks it stops from here on, including a server that
	// would start the daemon again the moment it is stopped.
	if had, err := p.ForgetConsent(); err != nil {
		problem("removing the record that you joined", err)
	} else if had {
		r.Did = append(r.Did, "Removed the record that you joined "+brand.Product+" in this clone")
	}
	trees, err := removeHooks(ctx, p)
	for _, t := range trees {
		r.Did = append(r.Did, "Removed the hook from "+filepath.Join(t, filepath.FromSlash(hookFile)))
	}
	if err != nil {
		problem("hook", err)
	}
	stopped, stopErr := client.Stop(ctx, p.GitDir)
	if stopErr != nil {
		problem("stopping the daemon", stopErr)
	}
	// A daemon that is still starting has no daemon.json to stop; it sees the consent gone and exits
	// by itself, which is waited for here so a leave doesn't return while one is about to publish.
	starting, err := client.WaitGone(ctx, p.GitDir, 20*time.Second)
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
		brand.Product + " is off for this repo. Agents already open here lose its tools within seconds in Claude Code; in Codex the tools stay listed until the session ends, and say they are off if called.",
		"The " + brand.Product + " MCP server stays registered with the agents, and its skills installed, since that is per user. The server does nothing in a repo that hasn't joined; " +
			"each skill costs every session of those agents a line of context, in any repo. " +
			"To remove them: " + e.removal() + ".",
	}
	if len(r.Problems) > 0 {
		return r, errors.New("not everything worked; see above")
	}
	return r, nil
}

// othersAdvice is what to tell about the agents other than Claude Code and Codex that Murmur was
// just added to, by what each was seen to do (2026-11-01): OpenCode watches its config, Muse and Pi
// read their servers when a session starts, and Grok is believed to (its documentation).
func othersAdvice(labels []string) string {
	s := brand.Product + " was just added to " + strings.Join(labels, ", ") + "."
	if slices.Contains(labels, "OpenCode") {
		s += " OpenCode sessions get its tools within seconds, from their next turn."
	}
	var restart []string
	for _, l := range labels {
		if l != "OpenCode" {
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
	return s
}

// advice is what to tell the user after a join, by what this machine and this agent need. Which
// agent is running join decides it: the MCP server is registered once, so a session that started
// after that already runs it (idle, with no tools, until the join), and one that started before
// has never heard of it. What each kind of session picks up was measured, 2026-10-29
// (docs/VISION.md, "Reloading MCP servers").
func (e Env) advice(claudeFresh, codexFresh, problems bool) []string {
	if problems {
		return []string{"Joining didn't finish: tell your user what is under Problems. Running `" + brand.Command + " join` again retries, and only does what is missing."}
	}
	var out []string
	switch {
	case e.getenv("CLAUDECODE") == "1" && e.getenv("T3CODE_CLI_PATH") != "" && claudeFresh:
		out = append(out, brand.Product+" was just added to Claude Code, and this conversation started before that, so it doesn't have the tools yet. "+
			"To load them: press Stop in T3, then send any message (T3 restarts Claude with "+brand.Product+" loaded); starting a new thread works too.")
	case e.getenv("CLAUDECODE") == "1" && claudeFresh:
		out = append(out, brand.Product+" was just added to Claude Code, and this conversation started before that, so it doesn't have the tools yet. "+
			"To load them: exit, and run `claude --continue` in this repo, which resumes this conversation with "+brand.Product+" loaded.")
	case e.getenv("CLAUDECODE") == "1":
		out = append(out, brand.Product+" is on for this repo. Its tools (mcp__"+brand.MCPServer+"__who, status, send, inbox) show up in this conversation "+
			"within a few seconds. If they haven't after a minute, start a new conversation.")
	case e.looksLikeCodex() && e.getenv("T3CODE_CLI_PATH") != "":
		// T3's Codex provider loads the server in a thread created after it was registered, and
		// never in an older one, which T3 doesn't refresh (docs/VISION.md, "Reloading MCP servers").
		out = append(out, brand.Product+" is on for this repo, but Codex loads MCP servers only in a thread that starts after they were added, so this thread doesn't have the tools: "+
			"open a new thread in this project to talk to other agents through "+brand.Product+".")
	case e.looksLikeCodex():
		out = append(out, brand.Product+" is on for this repo. Codex loads MCP servers only when a session starts, so this session doesn't have the tools: "+
			"start a new session in this repo to get them.")
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

// looksLikeCodex reports whether the agent running join is Codex. The variables are the ones Codex
// is believed to set for the commands it runs; no Codex session's environment has been checked
// (docs/VISION.md, "Reloading MCP servers"), so this may miss one, and advice then falls back to
// covering every client.
func (e Env) looksLikeCodex() bool {
	for _, k := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_CI"} {
		if e.getenv(k) != "" {
			return true
		}
	}
	return false
}
