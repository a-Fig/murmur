// Package hook is the Claude Code hook. After each tool call and each prompt it asks the daemon
// for messages this session hasn't been told about, and adds one line per message (sender and
// subject) to the agent's context, with how many are unread; or, where the page's setting says to,
// each message whole, as its turn would read (#204). Before and after each
// tool call that could run a command it also tells the daemon, which finds the commits the call
// made (#21). Those calls and prompts tell the daemon the session is at work. At the end of a turn
// (Stop) the hook asks for what the session was told of and hasn't read, and anything that came
// during the turn, and if there is any it keeps the turn going with it, as each message's turn
// would read; if not, the session is idle (#202).
//
// The agents run it in every project, whatever repo the session works in, so it first finds the
// clone the session is in Murmur in (sessionClone), and does nothing where there is none (#422).
//
// Pi has no hooks of this kind, so Murmur's Pi extension runs this one (`mur hook --agent pi`) at
// the same moments, with a payload of Claude Code's shape whose session_id is the Pi session's
// file, and reads its answer as Claude Code would (internal/join/pi.go, #205).
//
// OpenCode 2 has none either, and Murmur's plugin there does the same (`mur hook --agent opencode`),
// with OpenCode's session ID, and says which sessions another session started
// (internal/join/opencodeplugin.go, #431, #455).
//
// It never blocks or fails the agent: any problem, including no daemon running, means it
// prints nothing and exits 0. Before a tool call it never prints anything at all, and the time it
// can hold the call up is bounded by BracketTimeout.
package hook

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/a-Fig/murmur/internal/client"
	"github.com/a-Fig/murmur/internal/gitx"
	"github.com/a-Fig/murmur/internal/here"
	"github.com/a-Fig/murmur/internal/proc"
	"github.com/a-Fig/murmur/internal/proto"
	"github.com/a-Fig/murmur/internal/view"
)

// input is the part of Claude Code's hook payload the hook reads.
type input struct {
	SessionID string `json:"session_id"`
	// SessionIDCamel is Grok's spelling of it, read only from Grok's hook (xai-org/grok-build,
	// docs/user-guide/10-hooks.md, read 2026-11-09). Codex uses session_id, its thread ID.
	SessionIDCamel string `json:"sessionId"`
	// Reason is, on Grok's Stop, why: "end_turn" at the end of a turn, and something else for the
	// Stop it also fires when the session closes, whose answer it ignores.
	Reason string `json:"reason"`
	// SubagentType is set where Grok runs the hook inside a subagent.
	SubagentType string `json:"subagentType"`
	// TranscriptPath is the file Claude Code keeps the session's conversation in, which also holds
	// its title.
	TranscriptPath string `json:"transcript_path"`
	Event          string `json:"hook_event_name"`
	Cwd            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	// ToolUseID is on PreToolUse, PostToolUse and PostToolUseFailure alike, and the same for one
	// call: it is what pairs the two ends (measured on Claude Code 2.1.312, 2026-10-29).
	ToolUseID string `json:"tool_use_id"`
	// AgentID is set on the hooks of a subagent's tool calls, which carry the parent's session_id
	// (Claude Code 2.1.312, 2026-10-29). What the hook prints for one goes to the subagent.
	AgentID string `json:"agent_id"`
	// Interrupted is Murmur's own, on a Stop from Pi's extension: the work ended without a chance
	// to go on (Escape, an error), so the hook tells the daemon the turn ended and keeps nothing
	// going. Claude Code fires no Stop then.
	Interrupted bool `json:"interrupted"`
	// Child and ParentNative are Murmur's own, from its OpenCode plugin: another session started
	// this one, whose OpenCode session ID ParentNative is (#455).
	Child        bool   `json:"child"`
	ParentNative string `json:"parent_native"`
}

type output struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// stopOutput is a Stop hook's answer that keeps the turn going: Claude Code doesn't stop, and gives
// the agent reason as what to do next.
type stopOutput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Events the hook answers.
const (
	preTool     = "PreToolUse"
	postTool    = "PostToolUse"
	postFailure = "PostToolUseFailure"
	stop        = "Stop"
)

// events are those the hook acts on. A failing tool call fires PostToolUseFailure, not
// PostToolUse, so a `git commit && git push` whose push failed still has its end seen.
var events = map[string]bool{preTool: true, postTool: true, postFailure: true, stop: true, "UserPromptSubmit": true, "SessionStart": true}

// NoticeTimeout bounds the hook when it only fetches notices: it runs after the call, and a daemon
// that answers slowly is not worth waiting on.
//
// NoticeTimeout and BracketTimeout are variables so that tests on a loaded machine can raise them.
var NoticeTimeout = 3 * time.Second

// BracketTimeout bounds a hook that brackets a tool call. The snapshot at the start has to be taken
// before the call runs, so PreToolUse waits for it; this is the most a call can be held up, and a
// snapshot that misses it just means no commits are credited for that call. The daemon's own limit
// on a snapshot (Options.SnapshotTimeout) is a little shorter.
var BracketTimeout = 5 * time.Second

// cannotRun are the tools that can't run a command, and so can't make a commit. The hook skips
// bracketing them: a snapshot costs git processes, and these are the commonest calls. A tool not
// named here is bracketed, which is the safe way round for one this list has never heard of.
var cannotRun = map[string]bool{
	"Read": true, "Glob": true, "Grep": true, "LS": true,
	"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true,
	"WebFetch": true, "WebSearch": true,
	"TodoWrite": true, "ToolSearch": true, "AskUserQuestion": true,
	"EnterPlanMode": true, "ExitPlanMode": true,
}

// claudePID is the Claude Code process this hook runs under, which Claude Code gives hooks as
// CLAUDE_PID; 0 where it isn't set. The MCP server's parent is the same process, which is how the
// daemon knows the hook's session ID is the one that server's session now has.
func claudePID() int {
	pid, err := strconv.Atoi(os.Getenv("CLAUDE_PID"))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// bracketOp is the daemon op that tells it about the call this event is the start or end of, or ""
// if the event isn't one, or the call is of a tool that can't commit.
func bracketOp(in input) string {
	if in.ToolUseID == "" || cannotRun[in.ToolName] {
		return ""
	}
	switch in.Event {
	case preTool:
		return proto.OpToolStart
	case postTool, postFailure:
		return proto.OpToolEnd
	}
	return ""
}

// findClone is the git common dir of the repository the hook runs in. It is read from the
// repository's files where it can be (gitx.Discover): a git process here would add a tenth of a
// second or more to every tool call on Windows. Git is asked where it can't.
func findClone(ctx context.Context, cwd string) (string, error) {
	if _, common, ok := gitx.Discover(cwd); ok {
		return common, nil
	}
	return client.FindGitDir(ctx, cwd)
}

// sessionClone is the git common dir of the clone the hook's session is in Murmur in, which names
// its daemon; "" where there is none, and the hook does nothing. placed says it came from the
// session's own record, and so may not be the repository the hook runs in.
//
// The session's MCP server publishes where it is (package here), and `mur join -C` can have moved
// it to a clone the session doesn't work in (#200), so the working directory doesn't say. The
// record is found by the agent's process: Claude Code's CLAUDE_PID, or for another agent the
// nearest of the hook's ancestors that started a server (ownRecord). It settles it with no git
// process and no daemon asked: its clone, or nothing for a server outside any repository, which is
// every tool call in a folder that isn't one. A server in a repo that hasn't joined names its
// clone, and the hook stops at the daemon that isn't there.
//
// Where no record is surely the session's, the working directory answers, as it did before #422:
// no directory for records, a server from before they were written or one that isn't running, or
// a process whose sessions can't be told apart. Pi's extension and OpenCode's plugin run the hook
// only in a clone their user joined, by its working directory.
func sessionClone(ctx context.Context, agent string, in input, who proto.Request) (gitDir string, placed bool) {
	if !byExtension(agent) {
		if dir, err := here.DefaultDir(); err == nil {
			pids := who.Ancestors
			if agent == "" {
				pids = nil
				if who.ClientPID != 0 {
					pids = []int{who.ClientPID}
				}
			}
			if rec, ok := ownRecord(agent, in.SessionID, here.Of(dir, pids)); ok {
				return rec.GitDir, true
			}
		}
	}
	gitDir, err := findClone(ctx, in.Cwd)
	if err != nil {
		return "", false
	}
	return gitDir, false
}

// ownRecord picks, from the records of the servers the agent's process started, the one of the
// hook's session, whose ID is native.
//
// Claude Code names its own process (CLAUDE_PID), which runs one session, so its one record is the
// session's. Another agent's hook has only its ancestors to go by, and a record found among them
// doesn't say whose it is. The nearest may be the process of an agent that started this one (a
// Codex run from Claude Code's shell, whose own server has published nothing); or the agent's own
// process with several sessions, each with a server (Codex's app server, in T3 and outside it), of
// which this session's hasn't published. Taking such a record would send the hook to the wrong
// clone's daemon, where it could be taken for the record's session and given its messages
// (internal/daemon, agentHookSessionsLocked), or stop it where that session is in no repository.
// So for another agent the record has to carry the hook's own session ID: Grok's server has it
// from the start, and Codex's learns it from the first call to one of Murmur's tools.
func ownRecord(agent, native string, recs []here.Session) (here.Session, bool) {
	if agent == "" {
		if len(recs) == 1 {
			return recs[0], true
		}
		return here.Session{}, false
	}
	var mine []here.Session
	for _, r := range recs {
		if native != "" && r.Native == native {
			mine = append(mine, r)
		}
	}
	if len(mine) == 1 {
		return mine[0], true
	}
	return here.Session{}, false
}

// Run reads a hook payload from stdin and writes the hook's answer, if any, to stdout. It is
// Claude Code's hook.
func Run(stdin io.Reader, stdout io.Writer) { RunFor("", stdin, stdout) }

// Agents other than Claude Code that join installs the hook in, by the name their hook's command
// line gives (`mur hook codex`). Their hooks take Claude Code's payload and answers, Grok's with
// some fields of its own (#225). Pi's is run by Murmur's own extension, which gives the session's
// file for its ID (#205), and OpenCode's by Murmur's plugin, with OpenCode's session ID (#431).
const (
	Codex    = "codex"
	Grok     = "grok"
	Pi       = "pi"
	OpenCode = "opencode"
)

// KnownAgent says RunFor knows the agent named: "" (Claude Code), codex, grok, pi or opencode.
func KnownAgent(agent string) bool {
	return agent == "" || agent == Codex || agent == Grok || agent == Pi || agent == OpenCode
}

// byExtension says agent's hook is run by code of Murmur's inside the agent, not by the agent's
// own hooks: Pi's extension and OpenCode's plugin. Their payload names the session by the ID its
// MCP server registered with, and may say the work was interrupted.
func byExtension(agent string) bool { return agent == Pi || agent == OpenCode }

// RunFor is Run for agent, "" for Claude Code. It does nothing for an agent it doesn't know, rather
// than take another agent's hook for Claude Code's. Another agent's hook brackets no tool call, as
// join installs it on no PreToolUse (#225), and sends no transcript, which is Claude Code's format.
func RunFor(agent string, stdin io.Reader, stdout io.Writer) {
	defer func() { recover() }()
	if !KnownAgent(agent) {
		return
	}
	var in input
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in); err != nil {
		return
	}
	if in.SessionID == "" && agent == Grok {
		in.SessionID = in.SessionIDCamel
	}
	if in.SessionID == "" || !events[in.Event] {
		return
	}
	if !byExtension(agent) {
		in.Interrupted = false // the extensions' own field: Claude Code fires no Stop then
	}
	if agent != OpenCode {
		in.Child, in.ParentNative = false, ""
	}
	who := identify(agent, in)
	if in.Event == stop {
		turnEnd(agent, in, who, stdout)
		return
	}
	op := bracketOp(in)
	if agent != "" {
		op = ""
	}
	// Neither PreToolUse nor PostToolUseFailure gives notices: the next PostToolUse or prompt
	// does, and a PreToolUse prints nothing at all.
	wantNotices := in.Event != preTool && in.Event != postFailure
	if !wantNotices && op == "" {
		return
	}
	timeout := NoticeTimeout
	if op != "" {
		timeout = BracketTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	gitDir, placed := sessionClone(ctx, agent, in, who)
	if gitDir == "" {
		return
	}
	c, err := client.Connect(ctx, gitDir, false)
	if err != nil {
		return
	}
	// A bracket names the repository the call ran in, which the daemon watches only if it is its
	// own: a session moved into this clone works in another, and its commits there aren't credited.
	callDir := gitDir
	if op != "" && placed {
		callDir, _ = findClone(ctx, in.Cwd)
	}
	// The end of a call is sent alongside the notices request. The daemon answers it at once and
	// finds the call's commits after, so neither holds up the notices or the agent.
	var wg sync.WaitGroup
	if op != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			r := who
			r.Op, r.ToolUseID, r.Cwd, r.GitDir = op, in.ToolUseID, in.Cwd, callDir
			c.Do(ctx, r)
		}()
	}
	var resp proto.Response
	if wantNotices {
		nctx, ncancel := context.WithTimeout(ctx, NoticeTimeout)
		r := who
		r.Op, r.Whole = proto.OpNotices, true
		if agent == "" {
			r.Transcript = in.TranscriptPath
		}
		resp, err = c.Do(nctx, r)
		ncancel()
	}
	wg.Wait()
	if !wantNotices || err != nil {
		return
	}
	text := view.Hook(resp.Read, resp.Messages, resp.Unread)
	if text == "" {
		return
	}
	var out output
	out.HookSpecificOutput.HookEventName = in.Event
	out.HookSpecificOutput.AdditionalContext = text
	json.NewEncoder(stdout).Encode(out)
}

// turnEnd tells the daemon the turn ended, and keeps it going with the messages the daemon gives
// back, if any, unless it was interrupted: then the daemon gives none back. They are read once
// the daemon answers: a hook that gave up waiting (NoticeTimeout) just before the answer came loses
// them, the risk #204's whole messages take too.
func turnEnd(agent string, in input, who proto.Request, stdout io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), NoticeTimeout)
	defer cancel()
	gitDir, _ := sessionClone(ctx, agent, in, who)
	if gitDir == "" {
		return
	}
	c, err := client.Connect(ctx, gitDir, false)
	if err != nil {
		return
	}
	// Grok's Stop at the session's close can't keep anything going, nor can Pi's or OpenCode's after
	// Escape or an error: the messages are left to be handed over as a turn, as after a turn the user interrupted.
	who.Op, who.Continues = proto.OpTurnEnd, (in.Reason == "" || in.Reason == "end_turn") && !in.Interrupted
	resp, err := c.Do(ctx, who)
	if err != nil || len(resp.Read) == 0 || !who.Continues {
		return
	}
	json.NewEncoder(stdout).Encode(stopOutput{Decision: "block", Reason: view.Hook(resp.Read, nil, 0)})
}

// identify is what every request of the hook carries to say whose it is: the conversation's ID and,
// for Claude Code, its process (CLAUDE_PID); for another agent, the agent's name and the hook's
// ancestors, among which is the agent's process (proc.Ancestors).
func identify(agent string, in input) proto.Request {
	if byExtension(agent) {
		// The session is named by its file (Pi) or its session ID (OpenCode), which its MCP server
		// registered with too, never by a Claude Code ID or process: a Pi started from a Claude Code
		// session's shell inherits that session's CLAUDE_PID. Nothing of theirs is a transcript of
		// Claude Code's form, and no call is bracketed, so their commits aren't credited to them.
		return proto.Request{Native: in.SessionID, Child: in.Child, ParentNative: in.ParentNative}
	}
	r := proto.Request{ClaudeSession: in.SessionID, Subagent: in.AgentID != "" || in.SubagentType != ""}
	if agent == "" {
		r.ClientPID = claudePID()
	} else {
		// The directory goes too: the daemon matches a Codex hook to its session by it, among others
		// (#453).
		r.Agent, r.Ancestors, r.Cwd = agent, proc.Ancestors(os.Getppid(), maxAncestors), in.Cwd
	}
	return r
}

// maxAncestors bounds how far up identify looks: the agent is the hook's grandparent where it runs
// the hook through a shell, and a wrapper or two (a login shell, T3's launcher) may come between.
const maxAncestors = 6
