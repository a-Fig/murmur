# Handing a message to each client

How the daemon gets a message into each client, once [delivery](delivery.md) has decided to hand it
over.

## T3 Code

- **Finding the thread** (#56). Claude Code's MCP server takes the thread from `T3CODE_THREAD_ID`
  ([session names](session-names.md#one-address-per-t3-thread)). Every other server, and a Claude
  Code one that can't use it, looks the provider's own session up in T3's database:
  `statev2.sqlite` in T3's data directory (`T3CODE_HOME`, the directory the CLI's `bin` is in, or
  `~/.t3`), table
  `orchestration_v2_projection_provider_threads`, where `payload_json.nativeThreadRef.nativeId` is
  the Claude Code session ID or the Codex thread ID, beside `thread_id` (seen on T3 0.1.x,
  2026-10-29). The ID comes from the hook or `CLAUDE_CODE_SESSION_ID` for Claude Code, and from a
  tool call's `_meta.threadId` for Codex. The lookup only reads: SQLite's read-only mode (never
  immutable, which would ignore the write-ahead log and miss the newest threads), at most 1.5 s on a
  lock and 4 s in all, and any trouble leaves the session as it was.
- **SQLite in Go.** The reader is `modernc.org/sqlite`, SQLite translated to Go, so `mur` stays one
  binary built without a C compiler for every platform. The cost is size: with the rest of that
  change, the Windows binary grew from 17.7 MB to 23.8 MB (12.3 MB to 16.2 MB built with `-s -w`) on
  2026-10-29, almost all of it SQLite. `ncruces/go-sqlite3`, SQLite as WebAssembly, added more (10.5
  MB on a probe).
- **The session is its thread**, named after it rather than the provider's session under it, so its
  name survives account switches, `/clear` and restarts ([session names](session-names.md)). The
  daemon keeps the thread, the CLI's path and T3's data directory per session in `state.json`, so a
  message for a session whose MCP server T3 has stopped still finds its thread. These, the messaging
  token and the socket's address stay on the device: never on the bus, the page or the log.
- **When T3 restarts a thread's MCP server**, the new session takes the thread's route and what the
  old one had unread, in the order it arrived. A message for everyone that comes in the second
  between the new session registering and finding its thread is handed over by the old session, and
  not again by the new one (#70, 2026-11-11): the daemon remembers, by event ID, what it handed each
  thread in the last 30 minutes (up to 200 a thread, in memory only). A new session that registers
  before the daemon has seen the old one's connection close leaves the old one its route, as it was
  still running, so both copies can be taken for a handoff at once (#99). The daemon also knows what
  is on its way into each thread, and the second copy waits, with the hook leaving it alone: once
  the first has been handed over it is taken out, and if that handoff failed it goes in
  (2026-11-15). Handing the route over when the
  drop is seen was tried (2026-10-30) and dropped: in three review rounds each version could strand
  a message to the old session's name. A daemon restart forgets what was handed, and a repeat in
  that window is what there was before.
- **Codex under T3** gets its thread from `_meta.threadId`, which every Murmur tool takes, but its
  MCP servers start with a bare environment, so Codex's config must forward `T3CODE_CLI_PATH`
  (`env_vars`; worked with Codex 0.164.0 under T3 0.1.2, 2026-10-29), which `mur join` writes with
  `CODEX_HOME` (#88). Codex silently leaves out a name its own environment lacks
  (`create_env_for_mcp_server` in `codex-rs/rmcp-client`, read 2026-11-09). Without it the server
  doesn't know it is in T3, and the session gets notices only.
- **A thread that is idle** (#87). T3 stops an idle thread's provider process, and Murmur's MCP
  server with it, after about 30 minutes (seen 2026-10-30), so the session of an idle thread is the
  normal case, and the thread most worth waking. Its route is kept in `state.json` (`T3Routes`) for
  as long as its inbox is, 24 hours from the stop, and the daemon stays up while it holds one: a
  daemon that exited 10 minutes after the last session, as it first did, left a message to such a
  thread waiting until someone next used the clone, by when it was too old to wake it. A route
  counts only if the wake setting would let a message through. With no session live and no page open
  the daemon polls every 30 s, about 2,900 `ls-remote` calls in a day of waiting, so a sleeping
  thread wakes up to 30 s later than a working one would; it still pushes presence every 2 minutes,
  about 720 pushes a day. `mur join --leave` and `mur stop` end it at once. A sender is told such a thread is idle and the message wakes it, or that
  the wake setting won't let it (#669); it used to be told the session had stopped, which read as gone.

## Claude Code outside T3: the messaging socket

Claude Code sets `CLAUDE_CODE_MESSAGING_SOCKET` (a unix socket, a named pipe on Windows) and
`CLAUDE_CODE_MESSAGING_TOKEN` in every child process, and the MCP server passes both to its daemon.
The protocol, seen on 2.1.312 on 2026-10-29: JSON lines, an auth line `{"type":"auth","token":...}`,
then `{"type":"user","message":{"role":"user","content":...}}`. The socket answers nothing, so
Murmur knows a message was written, not that the session took it. The token dies with the session's
process, so the socket is used only while the session's MCP server is connected, and nothing like an
idle T3 thread can be woken. Inside T3 the socket is T3's, and Murmur never writes to it.

Claude Code wraps what arrives on the socket in a note of its own saying the message came from
another Claude session, not the user (seen 2026-10-29). It suits Murmur's framing, but on Claude
Code 2.1.316 it is a fixed paragraph of about 540 characters (about 135 tokens) on every message,
repeating what Murmur's own header says, and wrong for a person writing from the page. Murmur can't
remove it, since the socket is the only way to start a turn in an idle Claude Code session outside
T3. anthropics/claude-code#94383 asks for it to be shortened or made optional, and Murmur's case is
added there (Tyler, 2026-11-13). A turn through T3 carries no such note.

## Pi's extension

2026-11-09, #205. Pi has no hooks of Claude Code's kind, so the extension `mur join` writes
(`extensions/murmur.ts`) brings #202's delivery itself; the daemon only learned to name a Pi session
by its file. The extension runs `mur hook --agent pi` at Claude Code's moments: as a run starts
(`before_agent_start`), after each tool call (`tool_result`, where the notice goes in as a message
`deliverAs: "steer"`, after the tool calls and before the next model request), and as the work ends
(`agent_before_settle`, where a `custom_message` and `continue: true` keep it going, as a blocking
Stop does). Unlike Claude Code, Pi says when the work ended with no chance to go on (an error, or
Escape), and the daemon then takes the session for idle and hands what is unread over as a turn. The
extension also opens a socket in the session that takes Claude Code's messaging-socket lines, so the
daemon hands an idle Pi session a turn as it does Claude Code; Pi takes it with `triggerTurn`, and a
busy one with `deliverAs: "followUp"`, when it finishes. What Murmur says is a custom message in the
user's role, never `sendUserMessage`, which Pi treats as the user's typing.

- **Only where joined.** The extension is in every Pi session, so it runs the hook and opens the
  socket only in a clone the user joined; elsewhere each tool call would wait on a Murmur process,
  about 0.1 s (Opus's review). It looks for the consent as the session starts, then each second and
  at each prompt and tool call until it is there, since a user usually joins from inside a running
  session (the re-review's R1, reproduced with a real Pi).
- **Updates.** An update doesn't run `mur join` again, so Pi's MCP server rewrites an extension that
  an older binary wrote. Pi reads extensions as a session starts, so the first session after an
  update still has the old one.
- **Seen** with Pi 1.5.0 against a stand-in model, in RPC mode and in its terminal UI (driven in a
  pty by Opus's review, then here): a notice after a tool call, the message whole at the end of the
  work, a turn when idle and right after an abort, a new socket and session after `/new`, no socket
  in a repo not joined, and an old extension rewritten.
- **Pi in T3 Code** (2026-11-11, #462) needs nothing added: T3 starts `pi --mode rpc`, which loads
  the extension, Pi gives its MCP servers T3's `T3CODE_CLI_PATH`, and T3 records the session's file
  as the thread's native ID. Seen with Pi 0.99.0 on Windows against a stand-in for T3: a notice, the
  whole message at the end of the work, and an idle message as one turn through `t3 thread send`.
  Not seen in T3 itself, where Pi was switched off on the machine. T3's Pi adapter has no case for
  Pi's `custom_message` (T3 0.1.x, read 2026-11-11), so the notice and the end-of-work message
  likely reach the model without showing in the thread.
- **Not covered:** Pi's commits aren't credited, as no call is bracketed, and a session without a
  file (`--no-session`) gets turns and no notices.

## Codex and OpenCode 2 outside T3: their command lines

2026-11-11, #135. Until then these got a message only through `inbox` (and Codex a notice, where its
hook runs). Each has a command that does what `t3 thread send` does, so the daemon runs it:

- *Codex:* `codex queue --thread <thread> --message <text>`, with the session's `CODEX_HOME`. A
  thread that is open takes it as its next turn: on Codex 0.167.0 (2026-11-11) a terminal session
  that sat idle showed the message as a prompt and ran the turn about 3 s later. Codex's hook marks
  a busy session, which gets the notice and its turn's end instead.
- *OpenCode 2:* `opencode api session.prompt --param sessionID=<session>` with `{"text": ...,
  "delivery": "queue"}`, which asks the background server every OpenCode 2 client on the machine
  shares, with the `XDG_STATE_HOME` the session's server found it registered under (#486): the
  command looks for the server there, and the daemon's own may be another session's. `"queue"`
  waits behind a running turn, where the default, `"steer"`, goes into it. Seen on OpenCode 2.2.0
  the same day, with the terminal UI showing the message and the answer. `session.synthetic`
  starts a turn too, but the terminal UI then shows the answer and not what was asked, so it isn't
  used. Since 2026-11-12 OpenCode's plugin runs the hook (#431), so a busy session gets the notice,
  as Codex does.
- *Which program.* The one that started the session's MCP server, if that is a `codex` or an
  `opencode`. Neither command takes the text on standard input, so it goes on the command line, and
  the name on `PATH` won't do: on Windows it is npm's `.cmd` shim, which can't carry a newline. On
  Windows a message whose command line would pass 32,767 characters stays in the inbox.
- *Only while the session's MCP server is connected*, as for the socket. Connected doesn't mean
  watched: Codex's background app server kept a thread and its MCP server after its terminal closed,
  and 40 minutes later a queued message ran a turn there with no window open (seen 2026-11-11). The
  wake setting limits that, as for an idle T3 thread.
- *OpenCode's shared server only*, the one registered in `service.json` in OpenCode's state
  directory: `opencode api` would start the shared server to ask about a private server's session.
  A private server's session is left to the inbox. OpenCode 1 has no `api` command; its plugin
  opens a socket instead, as Pi's extension does ([clients](clients.md), "OpenCode 1's plugin").
- *A failure* is handled as `t3 thread send`'s is. A Codex session has no way in until it has called
  a Murmur tool, which is how its server learns the thread.
- *On the command line* the text is visible, for the second or so the command runs, to the machine's
  other users (`/proc/<pid>/cmdline` on Linux) and to process auditing; every other way in passes it
  over a pipe or a socket. OpenCode's HTTP API and Codex's app server (`thread/queue/add`) would
  avoid that, and the Windows length limit, at the cost of more of each client's insides than its
  documented command.
- *Whoever runs the client.* Any session whose MCP server a `codex` started can be handed a turn its
  driver didn't start, within the wake setting: Codex's desktop app, `codex exec`, a thread another
  tool drives through `codex app-server`. Not tried. Codex's own subagents are taken off the mesh
  ([delegates](delegates.md)).
- *Not covered* (#446): Muse and Grok outside T3, neither installed where this was built. A T3 Codex
  session whose config doesn't forward `T3CODE_CLI_PATH` reads as one outside T3. T3's headless
  server (`t3 serve`, 0.1.9) sets no `T3CODE_CLI_PATH` at all: a Claude Code thread there is known
  by `T3CODE_THREAD_ID` and reached through the `t3` that runs it (its parent, else the one on
  PATH). With none found it gets no messaging socket, which starts no turn under T3; a Claude Code
  whose parent is some other program (tmux, an editor) only inherited the variable and keeps its
  own route (#780, 2026-11-16). OpenCode's one MCP
  process per directory has the environment of whoever started the shared server, so a T3 thread on
  a server a terminal started reads as outside T3, and the other way round (not tried). Codex's
  desktop app wasn't looked at, and only Windows was tried with the real programs.
