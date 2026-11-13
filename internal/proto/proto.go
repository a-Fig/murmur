// Package proto is the local protocol between the daemon and its clients (MCP servers, the hook,
// the CLI): one JSON request line and one JSON response line per connection, over loopback TCP.
// A session's hello connection is the exception: it stays open for the session's lifetime, and
// its closing is how the daemon learns the session stopped, even after a crash.
package proto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/a-Fig/murmur/internal/tags"
)

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
	// OpTags adds and removes the calling session's own tags (#198).
	OpTags = "tags"
	// OpMute mutes the session called Name for the calling session, or with Unmute undoes that
	// (#27). With no Name it only lists whom the caller has muted.
	OpMute = "mute"
	// OpToolStart and OpToolEnd bracket one tool call, for the hook: the daemon snapshots the
	// worktree at the start and credits the commits that appeared by the end (#21).
	OpToolStart = "toolstart"
	OpToolEnd   = "toolend"
	// OpTurnEnd is the hook at the end of a turn (Claude Code's Stop): what the session was told of
	// and didn't read keeps the turn going, where the hook can do that (Continues), and the session
	// is idle otherwise (#202).
	OpTurnEnd = "turnend"
	// OpPageKey asks for a one-time key to the daemon's page, which another clone's daemon gives
	// its own page so the tab can move to this one's (#348).
	OpPageKey = "pagekey"
	// OpRotate asks for a new team key (#156): when someone in the epoch's recorded collaborators
	// is no longer in Collaborators, or always with Force.
	OpRotate = "rotate"
	// OpNick shows this device's nickname (#190), sets it to Nick, or with Clear takes it off.
	OpNick = "nick"
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
//	3: OpTurnEnd, OpNotices's Unread and Subagent: a session the hook runs in gets notices while it
//	   is at work, and turns only when it isn't (#202)
//	4: OpNotices's Read (on the wire, "delivered"): a busy session may be given its messages whole by the hook (#204)
//	5: OpTurnEnd's Continues and Read: the Stop hook keeps the turn going with the messages
//	   that would have gone in as a turn after it (#202)
//	6: session tags: OpTags, and who's search by tags (#198)
//	7: the page's own token, pageToken in daemon.json, so that an upgraded `mur ui` replaces a
//	   daemon whose page still takes the sessions' token (#42)
//	8: a person's account on the page (#242): an upgraded `mur ui` replaces a daemon whose page
//	   can't send
//	9: the hook's Agent and Ancestors, for Codex's and Grok's hooks, and SessionView's HookOff (#225)
//	10: OpPageKey, so another clone's page can switch to this one's (#348)
//	11: the hook's Native: Pi's extension runs the hook, which names its session by Pi's own ID
//	    (#205)
//	12: the daemon's proof (#371): a ping that carries a nonce is answered with Proof, before any
//	    token is checked, and the connection then takes one more request. A client sends nothing
//	    else to a listener until it has proved itself, so a daemon of 11 or less is replaced where
//	    the client may, and otherwise trusted only while it holds the clone's lock.
//	13: MessageView's Explained: a person's message says in full that they see only what the
//	    agent sends the first time a session has one from them, and ends as an agent's after (#345)
//	14: OpRotate, and a bus encrypted for a team key (#156): a daemon of 13 or less writes in the
//	    clear, so an upgraded mur replaces it where it may.
//	15: OpNick, a device's nickname (#190): a daemon of 14 or less answers `mur nickname` with
//	    "unknown op", so the command replaces it where it may.
//	16: the route's CodexCLI and OpenCodeCLI: a Codex or OpenCode session outside T3 is handed a
//	    message as a turn by its client's own command line (#135)
//	17: groups (#184): OpSend to a group's tag, and OpTags's For. A daemon of 16 or less refuses
//	    the first and, not knowing For, would put the tags meant for others on the caller.
//	18: OpMute (#27): a session's MCP server from this version on asks it of the daemon.
//	19: a hook's Child and ParentNative: the session is one another session started, and goes
//	    through it (#452)
//	20: the route's Child and ParentNative: a Codex tool call says its thread is a subagent's, and
//	    the session is a child from that call on (#487)
const Protocol = 20

// ProofProtocol is the first protocol whose daemon proves itself (Proof).
const ProofProtocol = 12

// Proof is what the daemon whose token is token, listening at addr, answers to a ping carrying
// nonce: an HMAC of the address and the nonce, keyed by the token. Only that daemon and the clients
// that read its daemon.json have the token, and the client's nonce is fresh, so a reply that carries
// it comes from the daemon: not from another user's process that took the port of a daemon that died
// (#371), which would otherwise be sent the session's requests and could answer them. The address is
// in it so that a proof made at one listener can't be passed off at another.
func Proof(token, addr, nonce string) string {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("daemon proof v1\x00" + addr + "\x00" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// How long a daemon takes to stop, once asked to: it lets its background work finish, for up to
// DrainTimeout, then deletes its presence, for up to GoodbyeTimeout, and holds the clone's lock until
// then. A client that waits for the lock, to start a daemon in the old one's place, waits for
// ShutdownMax and a little more (#83).
const (
	DrainTimeout   = 20 * time.Second
	GoodbyeTimeout = 15 * time.Second
	ShutdownMax    = DrainTimeout + GoodbyeTimeout
)

// ErrRestarting is OpPageKey's refusal from a daemon that has begun restarting onto an update: the
// caller asks the next daemon.
const ErrRestarting = "restarting onto an update; ask the next daemon"

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
	// PageToken is the page's own secret (#42), apart from Token. The page's address carries it,
	// so it reaches the browser's history and extensions; Token, which lets a caller send as this
	// device, never does. Empty for a daemon from before the page had its own, whose page takes Token.
	PageToken string `json:"pageToken,omitempty"`
}

// Request is one call to the daemon.
type Request struct {
	Token   string `json:"token"`
	Op      string `json:"op"`
	Session string `json:"session,omitempty"` // the caller; "" is the CLI
	// Nonce, on a ping, asks the daemon for its Proof of it. Such a ping carries no token: it is
	// what the client sends before it knows what it is talking to.
	Nonce string `json:"nonce,omitempty"`

	Hello *Hello `json:"hello,omitempty"`

	To      string `json:"to,omitempty"`
	Subject string `json:"subject,omitempty"`
	Body    string `json:"body,omitempty"`
	Doing   string `json:"doing,omitempty"`
	Limit   int    `json:"limit,omitempty"`

	// Collaborators is, for OpRotate, the repo's collaborators as the CLI listed them, or nil if it
	// couldn't; Force rotates whatever the list says.
	Collaborators []string `json:"collaborators,omitempty"`
	Force         bool     `json:"force,omitempty"`

	// Nick is, for OpNick, the nickname to give this device; Clear takes the one it has off. With
	// neither, OpNick only says what the device is called.
	Nick  string `json:"nick,omitempty"`
	Clear bool   `json:"clear,omitempty"`

	// ClaudeSession finds the session for the hook, which only knows Claude Code's ID for it.
	ClaudeSession string `json:"claudeSession,omitempty"`
	// Native finds the session for a hook of another agent instead, by the client's own ID for its
	// conversation, the one its MCP server registered with (Hello's Native): for Pi, the session's
	// file, which Murmur's Pi extension gives the hook (#205). ClaudeSession is empty then.
	Native string `json:"native,omitempty"`
	// ClientPID is the hook's CLAUDE_PID, the Claude Code process it runs under: with
	// ClaudeSession, it tells the daemon which sessions that process's conversation has moved to
	// a new Claude ID. 0 if the hook has none.
	ClientPID int `json:"clientPid,omitempty"`
	// Transcript is the path of the Claude Code session's transcript, the hook's transcript_path,
	// which holds the session's title. OpNotices carries it.
	Transcript string `json:"transcript,omitempty"`
	// Subagent says the hook ran for a subagent's tool call (its payload has an agent_id): the
	// session is at work, but a notice would land in the subagent's context, not the session's,
	// so OpNotices answers with nothing (#188).
	Subagent bool `json:"subagent,omitempty"`
	// Continues says, on OpTurnEnd, that the hook can keep the turn going with messages (Claude
	// Code's Stop hook, by blocking the stop): the reply's Read is what it should give.
	Continues bool `json:"continues,omitempty"`
	// Whole says, on OpNotices, that the hook prints the reply's Read. A hook from before it
	// prints only Messages, so the daemon gives it a message's subject, and keeps the message in the
	// inbox, even where the page says to give it whole (#229). An older daemon ignores it, and gives
	// messages whole to a hook that sends it, which prints them: no new Protocol number.
	Whole bool `json:"whole,omitempty"`
	// Agent is, on a hook's request, the agent it runs for where that isn't Claude Code: "codex" or
	// "grok", from the hook's command line (#225). ClaudeSession then holds that agent's own ID for the
	// conversation (Codex's thread ID, Grok's session ID), which its MCP server reports as the route's
	// Native. Ancestors is the hook's process and those above it, nearest first: such an agent gives a
	// hook no variable naming its process, as Claude Code does with CLAUDE_PID, and the agent that ran
	// the hook is the parent of its MCP server too.
	Agent     string `json:"agent,omitempty"`
	Ancestors []int  `json:"ancestors,omitempty"`
	// Child says, on a hook's request that names its session by Native, that another session
	// started this one (an OpenCode child session, #455): it is off the mesh from then on, as a T3
	// delegate is (#188), and nothing puts it back. ParentNative is the client's own ID of the
	// session that started it, "" if the hook doesn't know; messages for the child are refused
	// with that session's name, and the child's commits are credited to it.
	Child        bool   `json:"child,omitempty"`
	ParentNative string `json:"parentNative,omitempty"`

	// Add and Remove are the tags OpTags adds to the caller and takes from it. Tags is what OpWho
	// searches by: a session is listed when it has every tag named, and none named after a dash
	// (see tags.ParseFilter).
	//
	// For is the sessions OpTags adds to groups in place of the caller (#184), by name or address:
	// Add then holds group tags only, and nothing is removed.
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	For    []string `json:"for,omitempty"`

	// Name is the session OpLookup describes: a name, or device/session. Limit caps the messages.
	// For OpMute it is the name of the session to mute, or with Unmute to unmute.
	Name   string `json:"name,omitempty"`
	Unmute bool   `json:"unmute,omitempty"`

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
	// Codex home. A server names one, ~/.codex where it has no CODEX_HOME; only a server from before
	// #88 leaves it empty, and the daemon then takes its own CODEX_HOME, else ~/.codex.
	CodexHome string `json:"codexHome,omitempty"`
	// CodexCLI and OpenCodeCLI are the client's own program, where its command line can put a
	// message in the session as a turn outside T3 (package clientcli): the Codex that runs the
	// thread Native names, or the OpenCode whose background server has the session Native names.
	// The session's MCP server names the program that started it, and only one it takes for that
	// client. At most one of the two is set.
	CodexCLI    string `json:"codexCli,omitempty"`
	OpenCodeCLI string `json:"openCodeCli,omitempty"`
	// Child says the client started the conversation Native names for another of its own: Codex
	// marks a tool call of a thread it spawned as a subagent's (thread_source "subagent" in
	// x-codex-turn-metadata), and ParentNative is the thread that spawned it, as the call names
	// it. The daemon takes it only outside T3, as it takes Codex's state (#454, #487).
	Child        bool   `json:"child,omitempty"`
	ParentNative string `json:"parentNative,omitempty"`
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
	// what that daemon would have queued for it.
	Reconnect bool `json:"reconnect,omitempty"`
	// Route says how to hand a message over to the session as a turn.
	Route
}

// Response is the daemon's answer. Which fields are set depends on the op.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Protocol is the daemon's protocol number (Protocol), in the reply to a ping.
	Protocol int `json:"protocol,omitempty"`
	// Proof is the reply to a ping's Nonce: Proof(token, addr, nonce).
	Proof string `json:"proof,omitempty"`
	Self  string `json:"self,omitempty"` // the caller's address
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

	Messages []MessageView `json:"messages,omitempty"`
	Events   []EventView   `json:"events,omitempty"`
	// Unread is, in an OpNotices reply, how many messages the session has unread, set when there
	// are new ones to tell or the number changed since the hook last said it; 0 otherwise.
	Unread int `json:"unread,omitempty"`
	// Read is, in an OpNotices reply, the new messages the page's setting gives the session whole
	// while it is busy (#204), and in an OpTurnEnd reply, the messages that keep its turn going
	// (#202), with their bodies. The hook shows each as its turn would read; they are read, and
	// gone from the inbox. Its JSON name is "delivered", the word for this before #325 changed what
	// delivered means (2026-11-09): a hook installed earlier keeps running until its user runs
	// `mur join` again, and reads the field by that name.
	Read []MessageView `json:"delivered,omitempty"`

	Lookup *SessionDetail `json:"lookup,omitempty"`

	// Filter is the search OpWho made, as it read it; empty when it listed everyone. Tags is the
	// caller's tags after an OpTags.
	Filter string     `json:"filter,omitempty"`
	Tags   []tags.Tag `json:"tags,omitempty"`
	// Muted is, after an OpMute, the names of the sessions the caller has muted.
	Muted []string `json:"muted,omitempty"`

	// PageKey is OpPageKey's key: the page trades it once, within a minute, for its token.
	PageKey string `json:"pageKey,omitempty"`
}

// Device is one device as `who` shows it.
type Device struct {
	ID    string `json:"id"`
	Host  string `json:"host"`
	Owner string `json:"owner,omitempty"`
	// Nick is what the device is called (#190): the nickname its user gave it, else the name of its
	// person. "" if it has neither.
	Nick     string        `json:"nick,omitempty"`
	Self     bool          `json:"self,omitempty"`
	Stale    bool          `json:"stale,omitempty"`
	LastSeen time.Time     `json:"lastSeen"` // when this daemon last saw its presence change
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
	// Tags is the session's tags, in order of name (#198).
	Tags []tags.Tag `json:"tags,omitempty"`
	// HookOff says Murmur's hook doesn't run for the session: it has called a tool of Murmur's, and
	// the hook, which runs after every tool call, never reported (#225). It isn't told of messages
	// while it works.
	HookOff bool `json:"hookOff,omitempty"`
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
	Nick   string `json:"nick,omitempty"` // what the device is called, as Device.Nick
	Agent  string `json:"agent,omitempty"`
	You    bool   `json:"you,omitempty"`

	Branch    string     `json:"branch,omitempty"`
	Committed []string   `json:"committed,omitempty"` // branches it has committed to, most recent first
	Doing     string     `json:"doing,omitempty"`
	Started   time.Time  `json:"started,omitzero"`
	Tags      []tags.Tag `json:"tags,omitempty"`  // in order of name (#198)
	Muted     []string   `json:"muted,omitempty"` // the sessions it has muted, by name (#27)

	// Running says the session is in a device's presence (or registered here), or in its grace.
	// Reachable says a message sent now will be queued in the session's inbox: it is connected, and its device's
	// presence is current. A device that is Stale was last seen at DeviceSeen; a session Dropped is
	// waiting out its grace for its MCP server to come back.
	Running    bool      `json:"running"`
	Reachable  bool      `json:"reachable"`
	Stale      bool      `json:"stale,omitempty"`
	Dropped    bool      `json:"dropped,omitempty"`
	DeviceSeen time.Time `json:"deviceSeen,omitzero"`
	LastActive time.Time `json:"lastActive,omitzero"`
	StoppedAt  time.Time `json:"stoppedAt,omitzero"` // a session of this device that has stopped
	// Offline says the session is off the mesh, and a message to it is refused: its T3 thread is
	// settled, archived or deleted (#189), or another thread started it (#188). DelegateOf, for a
	// session of this device that another thread started, names the session to message instead,
	// or says that thread isn't on the mesh here.
	Offline    bool   `json:"offline,omitempty"`
	DelegateOf string `json:"delegateOf,omitempty"`

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
	FromOwner string `json:"fromOwner,omitempty"`
	// FromDevice is the sender's device, set when another device's live presence says the sender's
	// name too, so a turn says which one sent it; a name says nothing of who is behind it.
	FromDevice string    `json:"fromDevice,omitempty"`
	To         string    `json:"to"` // the address the message was sent to, whatever it was sent by
	ToName     string    `json:"toName,omitempty"`
	Subject    string    `json:"subject"`
	Body       string    `json:"body,omitempty"`
	At         time.Time `json:"at"`
	// Introduced says the session this goes to as a turn has had a turn from the same sender before,
	// so the turn needn't say again what agent the sender is and whose machine it is on (#216).
	Introduced bool `json:"introduced,omitempty"`
	// Explained says the session this goes to has been given a message from the same sender, a
	// person, before, which said in full that the person sees only what the session sends: this one
	// ends with an agent's line (#345). Always false for an agent's message.
	Explained bool `json:"explained,omitempty"`
	// GroupKnown says the session this goes to has been given a message sent to the same group
	// before, which said in full that it is in the group, how to answer and how to leave: this one
	// ends with a short line (#184). Always false for a message that wasn't sent to a group.
	GroupKnown bool `json:"groupKnown,omitempty"`
	// Told is when the hook's notice told the session of this message, for one given whole after:
	// its turn says so (#355). Zero for a message no notice told of.
	Told time.Time `json:"told,omitzero"`
}

// EventView is one line of `log`.
type EventView struct {
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}
