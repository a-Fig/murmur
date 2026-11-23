// This file is who the session is, as its hello tells the daemon: its ID, its agent's name, and
// the route to its client, read from the environment and from the processes above the server.

package mcpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/a-Fig/murmur/internal/bus"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/t3"
)

// helloFor is the registration this process makes. The Claude Code session ID in its environment
// is only what it was when this process started: the daemon corrects it from the hook (see
// link.learn). The parent's PID is the Claude Code process when Claude Code started this server
// itself, and the hook's CLAUDE_PID is how the daemon pairs the two.
//
// Claude Code's variables count only for Claude Code itself (the agent "claude-code", or none
// given): Pi and OpenCode pass their own environment on to their MCP servers, and one started from
// a Claude Code session has that session's CLAUDE_CODE_SESSION_ID, which isn't theirs (seen
// 2026-11-01). The agents that give their session's ID in the environment (Muse, Pi, Grok, and
// OpenCode 1 through Murmur's plugin; see nativeEnv) are registered with it, and an ID made from it.
func helloFor(agent, cwd string) proto.Hello {
	var claude string
	if isClaude(agent) {
		claude = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	t3 := t3Of(agent)
	h := proto.Hello{ID: sessionID(claude), Agent: agentLabel(agent, t3.cli), Cwd: cwd, ClaudeSession: claude,
		ClientPID: os.Getppid(), Route: routeWith(agent, t3)}
	if v := nativeEnv[agent]; v != "" {
		if native := os.Getenv(v); native != "" {
			h.ID, h.Native = nativeID(native), native
		}
	}
	if t3.thread && h.T3CLI != "" {
		thread := os.Getenv(threadEnv)
		h.ID, h.T3Thread, h.ByThread = threadID(thread), thread, true
	}
	return h
}

// threadEnv is t3.ThreadEnv, the thread's ID, which T3 gives the Claude Code and Antigravity
// processes it runs and so their MCP servers (#642, #886). Only their servers take it: Codex's
// start with a bare environment, Muse passes on only what join lists, OpenCode shares one server
// across threads, and Pi and Grok weren't tried.
const threadEnv = t3.ThreadEnv

// agentAbove says whether an agent's program runs above the client that started this server. Every
// process under a T3 thread inherits threadEnv, so a claude run from the thread's shell, perhaps in
// another clone, has the thread's variable without being the thread: its own client has an agent
// above it (the thread's), through shells and the like, where T3 runs the thread's client itself.
// The walk stops at any other program, or one it can't read: past T3 is the desktop's process,
// whose parent may have exited and its PID gone to anything since. A variable, for tests: their
// own process runs under whatever agent started them. An agent run through a program the walk
// stops at isn't seen; on one daemon the thread's address being in use catches it.
var agentAbove = func(agent string) bool {
	pids := proc.Ancestors(os.Getppid(), 10)
	return agentInChainFor(agent, len(pids), func(i int) (exe, comm string, ok bool) {
		exe, err := proc.Exe(pids[i])
		return exe, commOf(pids[i]), err == nil
	})
}

// agentInChain is agentAbove's walk over n processes, the client first, each read by at: its
// program's path, its comm (or ""), and whether it could be read. The client itself is skipped.
func agentInChain(n int, at func(i int) (exe, comm string, ok bool)) bool {
	return agentInChainFor("", n, at)
}

// agentInChainFor is agentInChain for agent's MCP server. Kiro CLI 3.4.0 runs its V3 engine in
// node (acp-server.js) under kiro-cli, and node spawns stdio MCP servers directly; for
// agent == "kiro", that kiro-cli parent of node is the client itself, not an enclosing agent.
func agentInChainFor(agent string, n int, at func(i int) (exe, comm string, ok bool)) bool {
	start := min(1, n)
	if agent == "kiro" && n >= 2 {
		exe0, _, ok0 := at(0)
		exe1, comm1, ok1 := at(1)
		if ok0 && ok1 && programName(exe0) == "node" && (programName(exe1) == "kiro-cli" || comm1 == "kiro-cli") {
			start = 2
		}
	}
	for i := start; i < n; i++ {
		exe, comm, ok := at(i)
		if !ok {
			return false
		}
		name := programName(exe)
		if agentPrograms[name] || agentPrograms[comm] {
			return true
		}
		if !passThrough[name] {
			return false
		}
	}
	return false
}

// agentPrograms are the agents' programs agentAbove looks for, by name. Antigravity, as T3 runs it
// in release 1.4.5, is agy_acp_server, whose localharness_external runs the agent's shell commands:
// seen on Windows, 2026-11-16 (#709), as powershell under localharness_external.exe under
// agy_acp_server.exe. Elsewhere T3's agy_acp_server is agy_acp_server.par, not listed: what runs it,
// and so the name the walk would read, hasn't been seen; localharness_external, a native program
// there too, is reached first. Release 1.5.13 runs directly as agy (seen on Windows, 2026-11-20,
// #889: powershell.exe under agy.exe under T3), so agy is listed too alongside the 1.4.5 names.
// Kiro CLI 3.4.0 runs as kiro-cli (which launches node acp-server.js, seen on Windows 2026-11-22).
var agentPrograms = map[string]bool{"claude": true, "codex": true, "grok": true, "muse": true,
	"opencode": true, "pi": true, "gemini": true, "agy": true, "agy_acp_server": true, "localharness_external": true,
	"kiro-cli": true}

// passThrough are the programs between an agent and a claude it runs that agentAbove looks past:
// shells, and what runs a command for one.
var passThrough = map[string]bool{"bash": true, "sh": true, "dash": true, "zsh": true, "fish": true,
	"ksh": true, "pwsh": true, "powershell": true, "cmd": true, "env": true, "sudo": true,
	"timeout": true, "nohup": true, "script": true, "node": true, "npx": true, "npm": true,
	"python": true, "python3": true, "uv": true, "go": true, "make": true}

// commOf is the name Linux keeps for process pid as it was started (/proc/<pid>/comm), "" elsewhere.
// Its program's path resolves links, and Claude Code's native install runs as a link named claude
// to a file named by its version.
func commOf(pid int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// programName is exe's file name, lower case, without .exe or Linux's " (deleted)".
func programName(exe string) string {
	return strings.TrimSuffix(strings.ToLower(filepath.Base(strings.TrimSuffix(exe, " (deleted)"))), ".exe")
}

// threadID is the session's Murmur ID inside T3, made from its thread (#642). A thread keeps its ID
// for good, while the client's session under it changes when T3 moves the thread to another
// provider instance (another Claude account or config home): each instance has a session of its
// own for the thread. So a server that registers by the thread is the thread's session again after
// a move, at the same address, and other devices never see an address they can't name. Hashed, as
// nativeID is, and kept apart from it.
func threadID(thread string) string { return nativeID("t3:" + thread) }

// isClaude says whether the server runs for Claude Code: `mcp` with no agent, as join registers it
// there, or with claude-code.
func isClaude(agent string) bool { return agent == "" || agent == "claude-code" }

// takesThreadEnv says whether agent's MCP server may take its T3 thread from threadEnv: Claude
// Code (#642), Antigravity (#886) and Kiro (#908), each of which runs one server per thread and
// passes T3's environment on to it.
func takesThreadEnv(agent string) bool {
	return isClaude(agent) || agent == "antigravity" || agent == "kiro"
}

// nativeEnv names, for each agent that has one, the variable its MCP servers get the session's own
// ID in: the ID T3 Code keeps for the thread's provider session, and so how the thread is found.
// Muse sets MUSE_SESSION_ID, which T3 makes when it starts the thread (seen on Muse 1.4.14,
// 2026-11-01); Grok sets GROK_SESSION_ID (read in its source, not seen run); Antigravity sets
// ANTIGRAVITY_CONVERSATION_ID in what it runs (seen in a T3 thread's shell, 2026-11-15). Pi has no
// such variable: the extension join writes for it registers the server with the session's file in
// MURMUR_PI_SESSION_FILE, and the file's path is what T3 records (internal/join/pi.go). OpenCode 1
// names no session in a call, as OpenCode 2 does in its _meta, and the plugin join writes for it
// gives the server MURMUR_OPENCODE_SESSION, an ID it makes for the process's sessions, which are
// one to Murmur (internal/join/opencodeplugin.go).
var nativeEnv = map[string]string{"muse": "MUSE_SESSION_ID", "pi": "MURMUR_PI_SESSION_FILE",
	"grok": "GROK_SESSION_ID", "opencode": "MURMUR_OPENCODE_SESSION", "antigravity": "ANTIGRAVITY_CONVERSATION_ID"}

// nativeID is the session's Murmur ID made from its client's own: the same each time, so a server
// that restarts within the conversation takes its session back, and hashed, since Muse's and
// OpenCode's IDs begin with the time they were made, so that two of them often share their first
// six characters.
func nativeID(native string) string {
	sum := sha256.Sum256([]byte(native))
	return hex.EncodeToString(sum[:3])
}

// routeFromEnv is what this process's environment says about reaching its client: inside T3, T3's
// command line; in Claude Code, the messaging socket and the token for it; in Pi and OpenCode 1,
// the socket Murmur's extension or plugin opens, which takes the same lines (internal/join/pi.go,
// opencodeplugin.go); for Codex and OpenCode 2 outside T3, the client's own program, whose command
// line takes a message (#135, package clientcli). T3's thread is in the environment of most agents
// T3 runs (threadEnv), and helloFor takes it; where it isn't, link.seek finds it. The daemon keeps
// all of this to itself (see proto.Route).
func routeFromEnv(agent string) proto.Route { return routeWith(agent, t3Of(agent)) }

// routeWith is routeFromEnv, with what t3Of found.
func routeWith(agent string, t3 t3Seen) proto.Route {
	r := proto.Route{T3CLI: t3.cli, T3Home: os.Getenv("T3CODE_HOME")}
	switch {
	case isClaude(agent):
		// A session T3 runs is reached through T3 only (see the daemon's viaOf): a line written to its
		// messaging socket starts no turn, and the daemon would take the message as read. One whose
		// T3 command line can't be found gets no socket, so its messages wait as notices (#780).
		if r.T3CLI == "" && t3.thread {
			break
		}
		r.MsgSocket, r.MsgToken = os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"), os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN")
	case agent == "pi":
		r.MsgSocket, r.MsgToken = os.Getenv("MURMUR_PI_MESSAGING_SOCKET"), os.Getenv("MURMUR_PI_MESSAGING_TOKEN")
	case agent == "codex" && r.T3CLI == "":
		r.CodexCLI = clientProgram("codex")
	case agent == "opencode":
		r.MsgSocket, r.MsgToken = os.Getenv("MURMUR_OPENCODE_MESSAGING_SOCKET"), os.Getenv("MURMUR_OPENCODE_MESSAGING_TOKEN")
		if r.T3CLI == "" {
			r.OpenCodeCLI, r.OpenCodeState = openCodeService()
		}
	}
	return r
}

// t3Seen is what the environment says of T3: its command line, if the session is in T3, and
// whether T3 runs the client (Claude Code or Antigravity) as a thread (see threadEnv).
type t3Seen struct {
	cli    string
	thread bool
}

// t3Of looks once at whether agent's session is in T3: the walk up the processes in agentAbove is
// made once, so the hello's parts can't disagree. T3's app names its command line in
// T3CODE_CLI_PATH, but its headless server (`t3 serve`, 0.1.9, seen 2026-11-16) doesn't: it gives
// the Claude Code it runs the thread's ID and T3CODE_HOME. Taken for outside T3 without one, such a
// session was handed messages through the messaging socket, which starts no turn under T3, and
// they were lost (#780). Antigravity receives T3CODE_THREAD_ID from T3 too (seen 2026-11-20, #886),
// so takesThreadEnv covers both Claude Code and Antigravity. Codex's server has neither variable,
// as Codex starts it with only what join forwards, and the thread's ID reaches only the commands
// Codex runs: there T3CODE_HOME, which join forwards too, says to look for the t3 above Codex, and
// the thread comes from the tool calls' _meta as in T3's app (#802). A codex run from a thread's
// shell inherits T3CODE_HOME: agentAbove finds the thread's own codex above it, and codexT3 takes
// only a t3 that runs Codex itself, for the walk can stop short of that codex, at a tmux, say.
func t3Of(agent string) t3Seen {
	seen := t3Seen{cli: os.Getenv("T3CODE_CLI_PATH")}
	seen.thread = takesThreadEnv(agent) && os.Getenv(threadEnv) != "" && !agentAbove(agent)
	switch {
	case seen.cli != "":
	case seen.thread:
		seen.cli, seen.thread = servingT3(agent)
	case agent == "codex" && os.Getenv("T3CODE_HOME") != "" && !agentAbove(agent):
		seen.cli = codexT3()
	}
	return seen
}

// servingT3 is the `t3` that runs the client (the server's parent), as t3Of finds it, and whether
// the client is the thread at all. A variable, so that tests needn't have T3.
var servingT3 = func(agent string) (cli string, thread bool) {
	pids := proc.Ancestors(os.Getppid(), 3)
	parent, read := servingT3Parent(agent, len(pids), func(i int) (exe, comm string, ok bool) {
		exe, err := proc.Exe(pids[i])
		return exe, commOf(pids[i]), err == nil
	})
	return t3.Serving(parent, read, exec.LookPath)
}

// servingT3Parent picks the process above the client that T3 would be, skipping kiro-cli when
// Kiro's V3 node engine (acp-server.js) sits directly under kiro-cli.
func servingT3Parent(agent string, n int, at func(i int) (exe, comm string, ok bool)) (parent string, read bool) {
	idx := 1
	if agent == "kiro" && n >= 3 {
		exe0, _, ok0 := at(0)
		exe1, comm1, ok1 := at(1)
		if ok0 && ok1 && programName(exe0) == "node" && (programName(exe1) == "kiro-cli" || comm1 == "kiro-cli") {
			idx = 2
		}
	}
	if n <= idx {
		return "", false
	}
	exe, _, ok := at(idx)
	return exe, ok
}

// codexT3 is the `t3` that runs Codex, the server's parent: Codex's own parent, only if it is t3,
// as `t3 serve` runs Codex's native program itself (seen 2026-11-16, #802). Unlike Serving, it
// takes no node and no t3 from PATH on a guess: npm's codex runs the native one under node, so a
// codex that tmux started in a thread's shell would be taken for the thread. A t3 an update has
// deleted gives way to the one on PATH, as in Serving. A variable, so that tests needn't have T3.
var codexT3 = func() string {
	pids := proc.Ancestors(os.Getppid(), 2)
	if len(pids) != 2 {
		return ""
	}
	exe, err := proc.Exe(pids[1])
	if err != nil || programName(exe) != "t3" {
		return ""
	}
	cli, _ := t3.Serving(exe, true, exec.LookPath)
	return cli
}

// parentProgram is the process that started this server and the program it runs. A variable, for
// tests.
var parentProgram = func() (pid int, exe string) {
	pid = os.Getppid()
	exe, _ = proc.Exe(pid)
	return pid, exe
}

// clientProgram is the program that started this server, if it is the client name: the Codex or
// OpenCode that runs the session, which is the one whose command line reaches it. The name on
// PATH may be another install, and on Windows it is npm's .cmd shim, which can't carry a
// message's text. "" for a server started by anything else (a wrapper, a shell): the session then
// has no such way in, as before.
func clientProgram(name string) string {
	_, exe := parentProgram()
	return programNamed(exe, name)
}

// programNamed is exe if it is a program called name, and "" otherwise. The name is matched whole:
// Codex's helpers (codex-command-runner, codex-code-mode-host) sit beside it and take no `queue`.
// Linux marks the path of a program replaced while it runs with " (deleted)", after an update,
// say; the path without it is the program that replaced it.
func programNamed(exe, name string) string {
	exe = strings.TrimSuffix(exe, " (deleted)")
	if exe == "" || !filepath.IsAbs(exe) || programName(exe) != name {
		return ""
	}
	return exe
}

// openCodeClient is the OpenCode program that started this server, whose version says which of
// Murmur's plugins it loads: OpenCode 2's npm package installs it as opencode2 too. "" for a
// server started by anything else.
func openCodeClient() string {
	_, exe := parentProgram()
	if c := programNamed(exe, "opencode"); c != "" {
		return c
	}
	return programNamed(exe, "opencode2")
}

// openCodeService is the OpenCode that started this server, if it is the background server every
// OpenCode 2 client shares, which is the one `opencode api` asks, and the state directory that
// server is registered in. OpenCode 2 registers that server's process in service.json in its state
// directory ($XDG_STATE_HOME/opencode, else ~/.local/state/opencode, on Windows too; {"pid": ...},
// seen on 2.2.0, 2026-11-11), and `opencode api` looks for it there, in the XDG_STATE_HOME it is
// run with (read in 2.3.0's source, 2026-11-15). The server keeps the state directory it started
// with, and this process has its environment, so the directory is the one to run the command
// with: the daemon's may be another session's (#486). It is named in full where XDG_STATE_HOME is
// unset, for the same reason. A session of any other server gets "": one started with
// --standalone or --server, where `opencode api` would start the shared server only to be told it
// has no such session, and OpenCode 1, which has no `api` command. If a later OpenCode keeps the
// file elsewhere, its sessions are back to the inbox.
func openCodeService() (exe, state string) {
	pid, exe := parentProgram()
	if exe = programNamed(exe, "opencode"); exe == "" {
		return "", ""
	}
	state = os.Getenv("XDG_STATE_HOME")
	if state == "" || !filepath.IsAbs(state) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", ""
		}
		state = filepath.Join(home, ".local", "state")
	}
	b, err := os.ReadFile(filepath.Join(state, "opencode", "service.json"))
	if err != nil {
		return "", ""
	}
	var reg struct {
		PID int `json:"pid"`
	}
	if json.Unmarshal(b, &reg) != nil || reg.PID == 0 || reg.PID != pid {
		return "", ""
	}
	return exe, state
}

// sessionID derives the session's ID from Claude Code's, when there is one, so the ID stays the
// same if the MCP server restarts within the session, or the conversation is resumed. It is hashed,
// as nativeID is for the other agents: it used to be the first six hex characters of Claude Code's
// ID, which put 24 bits of it in every address on the bus (#51). Otherwise it is random.
func sessionID(claude string) string {
	if claude == "" {
		return bus.RandHex(6)
	}
	return nativeID("claude:" + claude)
}

func agentName(flag string) string { return agentLabel(flag, t3Of(flag).cli) }

// agentLabel is agentName, with T3's command line as t3Of found it.
func agentLabel(flag, t3CLI string) string {
	agent := flag
	if agent == "" {
		agent = "agent"
		if os.Getenv("CLAUDECODE") == "1" {
			agent = "claude-code"
		}
	}
	if t3CLI != "" {
		agent += " in T3"
	}
	return agent
}
