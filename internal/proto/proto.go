// Package proto is the local protocol between the daemon and its clients (MCP servers, the hook,
// the CLI): one JSON request line and one JSON response line per connection, over loopback TCP.
// A session's hello connection is the exception: it stays open for the session's lifetime, and
// its closing is how the daemon learns the session stopped, even after a crash.
package proto

import "time"

// Ops.
const (
	OpPing    = "ping"
	OpHello   = "hello"
	OpWho     = "who"
	OpStatus  = "status"
	OpSend    = "send"
	OpInbox   = "inbox"
	OpNotices = "notices"
	OpRoute   = "route"
	OpLog     = "log"
	OpStop    = "stop"
	OpLookup  = "lookup"
	// OpEncrypt turns encryption on for the repo's bus (#156), refusing while a device seen lately
	// can't take part, unless Force.
	OpEncrypt = "encrypt"
	// OpToolStart and OpToolEnd bracket one tool call, for the hook: the daemon snapshots the
	// worktree at the start and credits the commits that appeared by the end (#21).
	OpToolStart = "toolstart"
	OpToolEnd   = "toolend"
)

// Protocol is the version of this protocol that the daemon speaks, reported in its ping reply. A
// client that finds a daemon with a lower number than its own stops it and starts its own, since an
// upgraded mur would otherwise keep talking to the daemon the old one left running, which answers
// ping and not what came after. A daemon from before the number reports none, which reads as 0.
// Raise it with any change to a request or a reply that an older daemon can't serve:
//
//	1: OpRoute, and delivering a message as a turn (#56, #57)
//	2: the conversation's title as the default status line, which needs the transcript the hook
//	   reports and the Codex thread ID in the route (#66)
//	3: OpEncrypt, and Encrypted in who's reply (#156)
const Protocol = 3

// How long a daemon takes to stop, once asked to: it lets its background work finish, for up to
// DrainTimeout, then deletes its presence, for up to GoodbyeTimeout, and holds the clone's lock until
// then. A client that waits for the lock, to start a daemon in the old one's place, waits for
// ShutdownMax and a little more (#83).
const (
	DrainTimeout   = 20 * time.Second
	GoodbyeTimeout = 15 * time.Second
	ShutdownMax    = DrainTimeout + GoodbyeTimeout
)

// CLISession is the session ID of requests from the command line. It is always registered on its
// device, so a human can send and read an inbox without an agent.
const CLISession = "cli"

// Info is what the daemon writes to daemon.json for clients to find it.
type Info struct {
	Addr   string `json:"addr"`
	Token  string `json:"token"`
	PID    int    `json:"pid"`
	Device string `json:"device"`
	// Web is the address the daemon serves the page for humans on, host:port on loopback. Empty
	// when it couldn't be served, and for a daemon from before the page.
	Web string `json:"web,omitempty"`
}

// Request is one call to the daemon.
type Request struct {
	Token   string `json:"token"`
	Op      string `json:"op"`
	Session string `json:"session,omitempty"` // the caller; "" is the CLI

	Hello *Hello `json:"hello,omitempty"`

	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	Doing   string `json:"doing,omitempty"`
	Limit   int    `json:"limit,omitempty"`

	// ClaudeSession finds the session for the hook, which only knows Claude Code's ID for it.
	ClaudeSession string `json:"claudeSession,omitempty"`
	// ClientPID is the hook's CLAUDE_PID, the Claude Code process it runs under: with
	// ClaudeSession, it tells the daemon which sessions that process's conversation has moved to
	// a new Claude ID. 0 if the hook has none.
	ClientPID int `json:"clientPid,omitempty"`
	// Transcript is the path of the Claude Code session's transcript, the hook's transcript_path,
	// which holds the session's title. OpNotices carries it.
	Transcript string `json:"transcript,omitempty"`

	// Name is the session OpLookup describes: a name, or device/session. Limit caps the messages.
	Name string `json:"name,omitempty"`
	// Force turns encryption on (OpEncrypt) though some devices can't take part.
	Force bool `json:"force,omitempty"`

	// For OpToolStart and OpToolEnd, which carry ClaudeSession and ClientPID too. ToolUseID pairs
	// the two ends of one call; Claude Code gives both the same one, and calls run in parallel.
	// Cwd is the hook's working directory, and GitDir the clone the hook found for it, so the
	// daemon can tell a call in another repository without running git to find out.
	ToolUseID string `json:"toolUseId,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	GitDir    string `json:"gitDir,omitempty"`

	// Route is what an OpRoute carries: what the session's MCP server has learned about how to
	// reach it.
	Route *Route `json:"route,omitempty"`
}

// Route is how a message can be put into a session's own conversation, as a turn the agent acts
// on, instead of waiting for it to read a notice. The MCP server finds these in its environment
// and, inside T3, in T3's database. They are local to the device, like client IDs: the token opens
// the session's messaging socket, so none of this is logged, shown on the page or put on the bus.
// Every field is optional.
type Route struct {
	// T3CLI is T3CODE_CLI_PATH, set for the agents T3 runs, and T3Home is T3CODE_HOME where the
	// agent has it. T3Thread is the T3 thread the session is, which is how a message reaches it
	// through T3 (`t3 thread send`) even while the session's MCP server is away.
	T3CLI    string `json:"t3Cli,omitempty"`
	T3Home   string `json:"t3Home,omitempty"`
	T3Thread string `json:"t3Thread,omitempty"`
	// MsgSocket and MsgToken are Claude Code's messaging socket for the session and the token that
	// opens it (CLAUDE_CODE_MESSAGING_SOCKET, CLAUDE_CODE_MESSAGING_TOKEN).
	MsgSocket string `json:"msgSocket,omitempty"`
	MsgToken  string `json:"msgToken,omitempty"`
	// Native is the client's own ID for the conversation where its environment doesn't have one:
	// the thread ID Codex puts in every tool call. Outside T3 it is how the daemon finds the
	// thread's name in Codex's state.
	Native string `json:"native,omitempty"`
	// CodexHome is the Codex state directory of that client (CODEX_HOME, else ~/.codex), as its MCP
	// server sees it: one daemon serves every client on the device, and they may not share a
	// Codex home. Empty takes the daemon's own.
	CodexHome string `json:"codexHome,omitempty"`
}

// Hello registers a session.
type Hello struct {
	ID            string `json:"id"`
	Agent         string `json:"agent"`
	Cwd           string `json:"cwd"`
	ClaudeSession string `json:"claudeSession,omitempty"`
	// ClientPID is the MCP server's parent process, which is the Claude Code process when Claude
	// Code starts the server itself: the hook's CLAUDE_PID is how the daemon links the two.
	ClientPID int `json:"clientPid,omitempty"`
	// Reconnect says the session was registered with a daemon that went away, so it should get
	// what that daemon would have delivered.
	Reconnect bool `json:"reconnect,omitempty"`
	// Route says how to deliver a message into the session as a turn.
	Route
}

// Response is the daemon's answer. Which fields are set depends on the op.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Protocol is the daemon's protocol number (Protocol), in the reply to a ping.
	Protocol int    `json:"protocol,omitempty"`
	Self     string `json:"self,omitempty"` // the caller's address
	// Name is the caller's name, and NamePending says it has none yet: the session works, and is
	// addressed as Self until the name lands.
	Name        string `json:"name,omitempty"`
	NamePending bool   `json:"namePending,omitempty"`
	Note        string `json:"note,omitempty"`
	// ClaudeSession is the Claude Code session ID the daemon holds for the calling session, which
	// a hook may have corrected since the MCP server started. The server sends it in any later
	// hello, in place of its environment's.
	ClaudeSession string `json:"claudeSession,omitempty"`

	Devices []Device `json:"devices,omitempty"`
	// Encrypted says the repo's bus is encrypted, in who's reply (#156).
	Encrypted bool `json:"encrypted,omitempty"`

	Messages []MessageView `json:"messages,omitempty"`
	Events   []EventView   `json:"events,omitempty"`

	Lookup *SessionDetail `json:"lookup,omitempty"`
}

// Device is one device as `who` shows it.
type Device struct {
	ID       string    `json:"id"`
	Host     string    `json:"host"`
	Owner    string    `json:"owner,omitempty"`
	Self     bool      `json:"self,omitempty"`
	Stale    bool      `json:"stale,omitempty"`
	LastSeen time.Time `json:"lastSeen"` // when this daemon last saw its presence change
	// Unsealed says the device's records carry no seal: its daemon predates #155, and nothing
	// proves what it writes is its own. Forged is when this daemon last ignored a record that
	// claimed to be the device's and failed its seal; zero if none has since it started.
	Unsealed bool          `json:"unsealed,omitempty"`
	Forged   time.Time     `json:"forged,omitzero"`
	Sessions []SessionView `json:"sessions"`
}

// SessionView is one session as `who` shows it.
type SessionView struct {
	Addr string `json:"addr"`
	// Name is the session's name; empty for a session of a daemon that doesn't give names, and,
	// with Pending set, for one on this device still waiting for its own.
	Name    string `json:"name,omitempty"`
	Pending bool   `json:"pending,omitempty"`
	Agent   string `json:"agent"`
	Branch  string `json:"branch,omitempty"`
	// Doing is the status line the agent set or, where it set none, its conversation's title.
	Doing   string    `json:"doing,omitempty"`
	Started time.Time `json:"started"`
	// LastActive is when the session last did anything, on the daemon's clock; zero where the
	// session's device doesn't say.
	LastActive time.Time `json:"lastActive,omitzero"`
	You        bool      `json:"you,omitempty"`
}

// SessionDetail is everything the daemon knows about one session, as `lookup` shows it. Where a
// field comes from decides who has it: the bus carries the first group; the second is on the
// session's own device only, and never leaves it.
type SessionDetail struct {
	Name   string `json:"name,omitempty"`
	Addr   string `json:"addr"`
	Device string `json:"device"`
	Host   string `json:"host,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Agent  string `json:"agent,omitempty"`
	You    bool   `json:"you,omitempty"`

	Branch    string    `json:"branch,omitempty"`
	Committed []string  `json:"committed,omitempty"` // branches it has committed to, most recent first
	Doing     string    `json:"doing,omitempty"`
	Started   time.Time `json:"started,omitzero"`

	// Running says the session is in a device's presence (or registered here), or in its grace.
	// Reachable says a message sent now will be delivered: it is connected, and its device's
	// presence is current. A device that is Stale was last seen at DeviceSeen; a session Dropped is
	// waiting out its grace for its MCP server to come back.
	Running    bool      `json:"running"`
	Reachable  bool      `json:"reachable"`
	Stale      bool      `json:"stale,omitempty"`
	Dropped    bool      `json:"dropped,omitempty"`
	DeviceSeen time.Time `json:"deviceSeen,omitzero"`
	LastActive time.Time `json:"lastActive,omitzero"`
	StoppedAt  time.Time `json:"stoppedAt,omitzero"` // a session of this device that has stopped

	// Local says the session is on the daemon's own device, which is what gives it the rest.
	Local         bool         `json:"local,omitempty"`
	Worktree      string       `json:"worktree,omitempty"` // the folder's name, never the path
	ClientSession string       `json:"clientSession,omitempty"`
	CommitCount   int          `json:"commitCount,omitempty"` // all it is credited with; Commits is the latest few
	Commits       []CommitView `json:"commits,omitempty"`     // newest last

	Messages []MessageView `json:"messages,omitempty"` // to and from it, oldest first
}

// CommitView is one commit credited to a session.
type CommitView struct {
	SHA     string    `json:"sha"`
	Branch  string    `json:"branch,omitempty"` // empty: made on a detached HEAD
	Subject string    `json:"subject,omitempty"`
	At      time.Time `json:"at"`
}

// MessageView is one received message. Notices carry no Body.
type MessageView struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	FromName   string `json:"fromName,omitempty"`
	FromAgent  string `json:"fromAgent,omitempty"`
	FromBranch string `json:"fromBranch,omitempty"`
	// FromOwner is the sender's person: the git user name on its device, where known.
	FromOwner string    `json:"fromOwner,omitempty"`
	To        string    `json:"to"` // the address the message was sent to, whatever it was sent by
	ToName    string    `json:"toName,omitempty"`
	Subject   string    `json:"subject"`
	Body      string    `json:"body,omitempty"`
	At        time.Time `json:"at"`
}

// EventView is one line of `log`.
type EventView struct {
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}
