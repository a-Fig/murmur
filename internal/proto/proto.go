// Package proto is the local protocol between the daemon and its clients (MCP servers, the hook,
// the CLI): one JSON request line and one JSON response line per connection, over loopback TCP.
// A session's hello connection is the exception: it stays open for the session's lifetime, and
// its closing is how the daemon learns the session stopped, even after a crash.
package proto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
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
	OpRotate        = "rotate"
	OpOwner         = "owner" // shows the mesh owner this device has verified
	OpTransferOwner = "owner.transfer"
	OpRepoMode      = "repo.mode"
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
//	21: receipts (#26): a daemon of 20 or less writes no marks for its sessions, and its page shows
//	    none for its person's messages, so an upgraded mur replaces it where it may.
//	22: a hello's ByThread (#642): the daemon may register a T3 thread at the address its name
//	    already has. One of 21 or less would register it at a new one.
//	23: a send's Files (#570): a daemon of 22 or less would send the message without them.
//	24: a hello's Command (#913): a session `mur who` or `mur send` registers stays while its T3
//	    thread is in use. A daemon of 23 or less would drop it a minute after the command exits.
//	25: a hello's From (#1145): a Pi session that moved to another conversation keeps its address,
//	    and a message to its old name reaches it. A daemon of 24 or less registers a new session.
//	26: a command's hello keyed by Native, with no T3 thread: `mur tool` for Murmur's Hermes plugin
//	    (#975). A daemon of 25 or less refuses it.
//	27: such a session's route names the socket Murmur's Hermes plugin opened for it, and the session
//	    is held while the socket answers (#1184). A daemon of 26 holds it for 30 idle minutes.
//	28: OpOwner and owner-only rotation in meshes set up by a daemon that verifies signed policy
//	    records (#1263). An older local daemon cannot enforce ownership.
//	29: OpTransferOwner carries the next owner's device ID.
//	30: OpRepoMode and per-repo encryption modes (#1264).
const Protocol = 30

// MaxPrompt is how much of a prompt a hook sends as Request.Prompt: a turn Murmur hands over
// begins with its sender and subject, well within it.
const MaxPrompt = 4096

// ProofProtocol is the first protocol whose daemon proves itself (Proof).
const ProofProtocol = 12

// NotRegistered is the daemon's refusal of a request from a session it doesn't have live. It is
// answered before the request does anything, so a shell command makes the request again after a
// new hello (#953).
const NotRegistered = "this session isn't registered with the daemon (it may have just restarted); try again in a few seconds"

// What a send's refusal is about (Response.Refused).
const (
	// RefusedRecipient is a refusal for the recipient alone: its name isn't one, nobody has it, it
	// is offline, it has muted the sender, its device has no key to encrypt to. Another recipient
	// might be sent the same message.
	RefusedRecipient = "recipient"
	// RefusedSend is a refusal of the message or the sender: no subject, a key in the body, a
	// session that isn't registered or is a delegate, a push that failed. Every recipient would be
	// refused the same.
	RefusedSend = "send"
)

// NativeID is the session ID an MCP server makes from native, its client's own ID for the
// conversation (Hello.Native): the same each time, so a server that restarts within the
// conversation takes its session back, and hashed, since Muse's and OpenCode's IDs begin with the
// time they were made, so that two of them often share their first six characters. The daemon
// makes it too, to know which Pi file a session ID is made from (#1145).
func NativeID(native string) string {
	sum := sha256.Sum256([]byte(native))
	return hex.EncodeToString(sum[:3])
}

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
	// EncryptionMode is the signed mode OpRepoMode should set. Empty asks for the current mode.
	EncryptionMode string `json:"encryptionMode,omitempty"`
	AllowPlaintext bool   `json:"allowPlaintext,omitempty"`

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
	// TellMe is, on OpSend, the state the sender asks to be told its message reached: "delivered"
	// or "read" (#276), or "" for nothing. A daemon from before it ignores it, and its reply has no
	// Telling, which is how the sender learns it won't be told.
	TellMe string `json:"tellMe,omitempty"`
	// Files is, for OpSend, the full paths of the files to send with the message (#570). The daemon
	// reads them: it runs as the same user, on the same machine.
	Files []string `json:"files,omitempty"`
	Doing string   `json:"doing,omitempty"`
	Limit int      `json:"limit,omitempty"`

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
	// Prompt is, on OpNotices from a hook's UserPromptSubmit, the start of the prompt the turn
	// began with (MaxPrompt bytes at most). A message handed over as a turn is read once a prompt
	// holds it (#611). An older daemon ignores it.
	Prompt string `json:"prompt,omitempty"`
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

	// Issues is, on the hook's OpNotices after a tool call, the GitHub issues the call filed or
	// closed, as the hook read them from the call's input and output (#510). The daemon keeps those
	// in its own repository for the page's Recent view. An older daemon ignores it, and the page
	// names the issue's GitHub login, as before: no new Protocol number.
	Issues []Issue `json:"issues,omitempty"`

	// Route is what an OpRoute carries: what the session's MCP server has learned about how to
	// reach it.
	Route *Route `json:"route,omitempty"`
}

// Issue is a GitHub issue a tool call filed, or with Closed closed. Repo is its owner/name.
type Issue struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Closed bool   `json:"closed,omitempty"`
}

// Route is how a message can be put into a session's own conversation, as a turn the agent acts
// on, instead of waiting for it to read a notice. The MCP server finds these in its environment
// and, inside T3, in T3's database. They are local to the device, like client IDs: the token opens
// the session's messaging socket, so none of this is logged, shown on the page or put on the bus.
// Every field is optional.
type Route struct {
	// T3CLI is T3's command line: T3CODE_CLI_PATH, set for the agents T3's app runs, or for a
	// thread of its headless server, which sets none, the `t3` that serves (#780). T3Home is
	// T3CODE_HOME where the agent has it. T3Thread is the T3 thread the session is, which is how a
	// message reaches it through T3 (`t3 thread send`) even while the session's MCP server is away.
	T3CLI    string `json:"t3Cli,omitempty"`
	T3Home   string `json:"t3Home,omitempty"`
	T3Thread string `json:"t3Thread,omitempty"`
	// MsgSocket and MsgToken are Claude Code's messaging socket for the session and the token that
	// opens it (CLAUDE_CODE_MESSAGING_SOCKET, CLAUDE_CODE_MESSAGING_TOKEN).
	MsgSocket string `json:"msgSocket,omitempty"`
	MsgToken  string `json:"msgToken,omitempty"`
	// MsgReplies says the socket answers each message with whether the session took it, as
	// Murmur's Hermes plugin's does since #1258 (msgsock.SendAwaiting). Claude Code's doesn't, nor
	// does a Hermes plugin from before, still loaded in a gateway that hasn't restarted.
	MsgReplies bool `json:"msgReplies,omitempty"`
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
	// OpenCodeState is where OpenCodeCLI's command looks for the shared server (XDG_STATE_HOME):
	// the state directory the session's server found that server registered in, ~/.local/state
	// where it has no XDG_STATE_HOME. As with CodexHome, one daemon serves every client, and its own
	// XDG_STATE_HOME is that of whichever session started it (#486). Only a server from before #486
	// leaves it empty, and the command then has the daemon's.
	OpenCodeState string `json:"openCodeState,omitempty"`
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
	// ByThread says ID was made from Route.T3Thread, which T3 gave the server in its environment
	// (#642), and that the server takes the session ID the reply's Self names: the daemon may
	// register the thread at the address its name already has. A server from before it, still
	// running after an update, sends the thread it found too, but keeps its own ID.
	ByThread bool `json:"byThread,omitempty"`
	// Command says a shell command (`mur who`, `mur send`) registers the session, for a harness
	// with no Murmur tools (#913). Its connection closes with the command, so the daemon keeps the
	// session for as long as its T3 thread is in use, which Route must name, or, for `mur tool`
	// from Murmur's Hermes plugin (#975), its Native ID, with no thread: then while it makes calls.
	// If a session is live in that thread, or at that ID, already, the command acts as it, and
	// nothing is registered.
	Command bool `json:"command,omitempty"`
	// From is the client's own ID for the conversation the session was in before this one, where
	// the client moved it to another in the same process: Pi's previous session file after /new,
	// /resume or /fork, which Murmur's extension gives the server (#1145). The daemon may register
	// the server at the address of the session it left, which follows it.
	From string `json:"from,omitempty"`
	// TakesSelf says the server goes by the session ID the reply's Self names, with or without
	// From: after a Pi move, the daemon gives a server on either file the ID it recorded for that
	// file (#1145). A server from before it, still running after an update, keeps its own ID, so
	// the daemon gives it no other.
	TakesSelf bool `json:"takesSelf,omitempty"`
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
	// Telling says, in a reply to OpSend with TellMe, that the sender will be told how far the
	// message got (#276). TellKnown says the daemon knows TellMe at all, telling or not (the note
	// says why not), so a client can tell an older daemon, which passes over it, from one with
	// nobody to wait for.
	Telling   bool `json:"telling,omitempty"`
	TellKnown bool `json:"tellKnown,omitempty"`
	// Refused says what a send's refusal is about (#541): RefusedRecipient or RefusedSend. A send
	// to a list goes on past the first and stops at the second. A daemon from before it says
	// neither, and an MCP server goes by the refusal's words (refusedRecipient), so it needed no
	// new Protocol number.
	Refused string `json:"refused,omitempty"`
	// ClaudeSession is the Claude Code session ID the daemon holds for the calling session, which
	// a hook may have corrected since the MCP server started. The server sends it in any later
	// hello, in place of its environment's.
	ClaudeSession string `json:"claudeSession,omitempty"`
	// ThreadUnread says, in the reply to a hello or an OpRoute that names a T3 thread, that the
	// daemon couldn't read the thread's state, so it doesn't know whether another thread started it
	// (#980). A T3 Codex session's MCP server then tells it the route again before its first change,
	// and goes on whatever the answer (#993). A daemon from before it never says so.
	ThreadUnread bool `json:"threadUnread,omitempty"`

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
	Nick     string    `json:"nick,omitempty"`
	Self     bool      `json:"self,omitempty"`
	Stale    bool      `json:"stale,omitempty"`
	LastSeen time.Time `json:"lastSeen"` // when this daemon last saw its presence change
	// DND says the device's person put it on do not disturb (#681): no message reaches its sessions
	// until it is back online.
	DND      bool          `json:"dnd,omitempty"`
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

// The values of SessionDetail's SendsFiles and TakesFiles.
const (
	FilesOn  = "on"
	FilesOff = "off"
)

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
	// SendsFiles and TakesFiles are what the session's file switches say (#859): FilesOn, FilesOff,
	// or "" where this device can't know. A person's are their own switches, an agent's its device's agents'.
	// Another device's presence says whether it takes files, never whether it sends them, so
	// SendsFiles is set for a session on this device alone.
	SendsFiles   string `json:"sendsFiles,omitempty"`
	TakesFiles   string `json:"takesFiles,omitempty"`
	RepoFilesOff bool   `json:"repoFilesOff,omitempty"`

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
	// IdleInT3 says a stopped session of this device is a T3 thread T3 stopped while it was idle:
	// a message still wakes it through its route, unless NoWake or do not disturb says otherwise,
	// as send says (#669, #1340).
	IdleInT3 bool `json:"idleInT3,omitempty"`
	// Offline says the session is off the mesh, and a message to it is refused: its T3 thread is
	// settled, archived or deleted (#189), or another thread started it (#188). DelegateOf, for a
	// session of this device that another thread started, names the session to message instead,
	// or says that thread isn't on the mesh here.
	Offline    bool   `json:"offline,omitempty"`
	DelegateOf string `json:"delegateOf,omitempty"`
	// NoWake says a message from an agent wouldn't go into the session as a turn now, by its
	// device's wake setting (#545): bus.NoWakeOff or bus.NoWakeIdle, "" if it would or isn't known.
	NoWake string `json:"noWake,omitempty"`

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
	// ends with an agent's line (#345). Always false for an agent's message, and for a message to a
	// group, which ends with the group's line whoever sent it (#484).
	Explained bool `json:"explained,omitempty"`
	// GroupKnown says the session this goes to has been given a message sent to the same group
	// before, which said in full that it is in the group, how to answer and how to leave: this one
	// ends with a short line (#184). Always false for a message that wasn't sent to a group.
	GroupKnown bool `json:"groupKnown,omitempty"`
	// ByCommand says the session this goes to as a turn has the shell commands and no tools
	// (Hello.Command): its turn says to answer with `mur send` (#913).
	ByCommand bool `json:"byCommand,omitempty"`
	// Told is when the hook's notice told the session of this message, for one given whole after:
	// its turn says so (#355). Zero for a message no notice told of.
	Told time.Time `json:"told,omitzero"`
	// Receipt says this is a notice from Murmur itself, not a message from anyone: how far one of
	// the session's messages got, which it asked to be told (#276). Nobody can be answered.
	Receipt bool `json:"receipt,omitempty"`
	// Files is the files the message carries (#570).
	Files []FileView `json:"files,omitempty"`
	// FilesKept is the files the message carries that the session doesn't get, because its user
	// turned Receive files off for agents (#860): their names and sizes, never a Path. Files is
	// empty then. The turn lists them, so the session can tell its user.
	FilesKept    []FileView `json:"filesKept,omitempty"`
	RepoFilesOff bool       `json:"repoFilesOff,omitempty"` // the mesh owner's policy kept these files
}

// FileView is one file a message carries, as the session it is for is told of it: Path is where
// it is saved on this machine, "" if it isn't (it couldn't be fetched, or was kept its time).
type FileView struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Path string `json:"path,omitempty"`
}

// Size is a file's size as people read one: 812 bytes, 14 KB, 2.3 MB.
func Size(n int64) string {
	switch {
	case n < 1<<10:
		return strconv.FormatInt(n, 10) + " bytes"
	case n < 1<<20:
		return strconv.FormatInt((n+512)>>10, 10) + " KB"
	}
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
}

// EventView is one line of `log`.
type EventView struct {
	At   time.Time `json:"at"`
	Line string    `json:"line"`
}
