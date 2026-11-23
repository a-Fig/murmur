# Other agents and their hooks

## Other agents

Added 2026-11-01 (#128), at Tyler's request: OpenCode, Muse Code, Pi and Grok, in T3 Code and on
their own. Each was looked at in its installed binary, its documentation and probe runs (OpenCode
2.2.0, Muse 1.4.14, Pi 0.99.0 on Tyler's laptop; Grok Build from its documentation and source only,
since it wasn't installed). The details, and where each came from, are in the comments of
`internal/join` (one file per agent) and `internal/mcpserver`.

| Agent | Where join writes | How a session is told apart | Inside T3 |
|---|---|---|---|
| OpenCode | `~/.config/opencode/opencode.jsonc` if there, else `opencode.json`, in the shape the installed version loads: for 2.x, `mcp.servers.murmur` with `"codemode": false` and a `murmur_*` allow rule last in `permissions`; for 1.x, or a version join can't learn, `mcp.murmur` with `"murmur_*": "allow"` last in `permission`, which 2.x loads too. OpenCode 1.x refuses a whole config that has `permissions` (#388), so a rerun moves Murmur's entries to the shape it now writes | One server process serves every session in a directory; each call names its session in `_meta["ai.opencode/sessionID"]`. OpenCode 1 names none, and its sessions in a directory are one, by an ID Murmur's plugin gives the server | The session ID is T3's native ID for the thread |
| Muse Code | `mcpServers.murmur` in `~/.config/muse/settings.json`, `"mode": "optional"`, `env` forwarding `T3CODE_CLI_PATH` and, on Windows, the home variables; the hook under `hooks` there (below) | One process per session, with `MUSE_SESSION_ID` | The same ID |
| Pi | an extension, `extensions/murmur.ts` in `~/.pi/agent`, that registers the server with the session's file in `MURMUR_PI_SESSION_FILE` | One process per session | T3 knows a Pi thread by that file |
| Grok | `[mcp_servers.murmur]` in `~/.grok/config.toml`, and `MCPTool(murmur__*)` in `[permission] allow` | One process per session, with `GROK_SESSION_ID` | Expected to be the ACP session ID T3 records (not tried) |
| Antigravity | `mcpServers.murmur` in `config/mcp_config.json` in `$GEMINI_HOME` (`~/.gemini`), and in T3's own home for each instance (below) | One `agy` (1.5.13+; `agy_acp_server` in 1.4.5) per thread; `T3CODE_THREAD_ID` in T3, and `conversationId` in hooks | `T3CODE_THREAD_ID` (seen 2026-11-20), or `conversationId` via `t3db.ThreadOf` |
| Cursor (beta) | `mcpServers.murmur` in `~/.cursor/mcp.json`, the hook in `~/.cursor/hooks.json`, `Mcp(murmur:*)` in `cli-config.json` (below) | One `agent` process per chat; the chat's ID reaches only hooks and shell commands, so the hook names it to the server | Not yet: T3 runs every Cursor thread in one process of its own (#924) |
| Kiro (beta) | `mcpServers.murmur` in `settings/mcp.json` in `$KIRO_HOME` (`~/.kiro`), a `murmur/*` MCP allow rule in `settings/permissions.json` when `settings/permissions.yaml` is absent (Kiro's policy rules match by server name, not executable, so in a trusted workspace a project `murmur` server inherits the rule), `hooks/murmur.json` (`UserPromptSubmit` and `Stop`; Kiro 3.4.0 discards `PostToolUse` command output), and `skills/<name>/SKILL.md` | One `kiro-cli` -> `node` (`acp-server.js`) per session; `T3CODE_THREAD_ID` in T3, and `session_id` in hooks | `T3CODE_THREAD_ID` (Kiro CLI 3.4.0 V3, read 2026-11-22) |

- **Edited, not rewritten.** JSON and JSONC files are edited as text (`jsonedit`), so comments and
  alignment stay. A file in a form join can't read safely (a key given twice, or Muse's older
  `mcp_servers`, beside which a new key would drop every server) is a reported problem. Grok's TOML
  goes through Codex's editor. Pi's extension is Murmur's own file, and one of that name that isn't
  Murmur's is left alone.
- **Identity.** Claude Code's variables count only for Claude Code: Pi and OpenCode pass their
  environment on to their servers, so one started from a Claude Code session carries that session's
  `CLAUDE_CODE_SESSION_ID`. An agent's own session ID becomes the Murmur ID by hash, since Muse's
  and OpenCode's IDs begin with the time and two of them often share six characters. Claude Code's
  is hashed too, since 2026-11-16 (#51): its ID used to be the first six characters of Claude
  Code's, which put 24 bits of that in every address on the bus. A conversation from before then,
  resumed, comes back at a new address with its name, and the old one forwards to it. The registry
  keeps the name at the old address, so once that conversation stops, other devices label its
  later messages by the new address; T3 threads avoid this by taking the registry's address back
  (`threadIDLocked`), which here would mean keeping the old ID. A session of
  the shared OpenCode server is registered at its first call and dropped after 30 minutes without
  one.
- **Delivery.** Inside T3, `t3 thread send` needed nothing new: an OpenCode and a Muse thread each
  took a message as a turn and answered it with `send`, nobody typing (2026-11-01). Outside T3, Muse
  and Grok have no way in that Murmur uses (Muse's session messaging isn't on Windows, and no way to
  start a turn in Grok was found), so a message to an idle session waits in the inbox; a busy one
  is told by the hook (Grok's since #225, Muse's since #461). Pi has notices and turns since
  2026-11-09 (#205), OpenCode 2 turns since 2026-11-11 (#135) and notices since 2026-11-12 (#431),
  and OpenCode 1 both since 2026-11-15 (#446): see [handoff](handoff.md) and below.
- **OpenCode 2's plugin** (2026-11-12, #431, #455). OpenCode 2 has no hooks of Claude Code's kind,
  so join writes `plugins/murmur.js` into its config directory, which OpenCode loads with no entry
  in its config. The plugin runs `mur hook --agent opencode` after each tool call and as a turn
  starts and ends, so an OpenCode 2 session is told of a message while it works and handed what is
  unread at its turn's end, as Claude Code is.
  - *How the words go in.* `ctx.session.synthetic` with `delivery: "steer"` puts the notice after
    the tool's result, as a message of its own. Appended to the tool's result instead, a model took
    it for text planted in the file it had read, and refused it. At a turn's end the same call
    starts another turn. The cost: OpenCode's terminal UI shows the answer to a synthetic message
    and not the message.
  - *Order.* The plugin runs one session's hooks one after another: a turn's start that reached the
    daemon after its end would leave an idle session looking busy.
  - *Children.* A session the `subagent` tool started carries `parentID`, and the plugin says so on
    every hook request (`child`, `parentNative`, #473) and once as the session is made, so an
    OpenCode child is never listed and its `send` is refused with its parent's name. A child made
    while no daemon ran is on the mesh from its first Murmur call until its next tool call ends.
  - *Tried* on OpenCode 2.2.0 against a private `opencode serve` (2026-11-12): a notice mid-turn,
    the message whole at the turn's end answered with `send`, nobody typing, and a subagent kept out
    of `who`. Not tried: a thread T3 started, and whether T3's sidebar shows a child session as a
    thread of its own (Tyler's rule, #452, keeps such a one on the mesh; the plugin would take it
    off).
- **OpenCode 1's plugin** (2026-11-15, #446, #205). OpenCode 1 loads the same `plugins/` folder, but
  a plugin of another shape (`server(input)` where OpenCode 2 has `setup(ctx)`), and each version
  refuses the other's or, with both in one file, 1.19.5 runs both. So join writes the one `opencode
  --version` names, at the same path, and none where it can't learn the version. The refresh as
  Murmur's server starts asks the OpenCode that started the server for its version and rewrites
  the file for it, so after an upgrade from 1 to 2 the file is the one that loads. With both
  installed, sharing one config folder, each server start rewrites the file for its own version,
  and it suits the one started last: OpenCode 2 loads the new file at once, so a running OpenCode 2
  loses its plugin when an OpenCode 1 starts (before OpenCode 1 had a plugin, an OpenCode 2 there
  had none at all), and OpenCode 1 loads it at its next start. Where it can't learn the version,
  the file keeps its own (its line `// For OpenCode 1.` says which). The refresh only rewrites a
  plugin join wrote and never adds one where there is none, since adding one is join's to do:
  after a join that couldn't learn the version (`opencode --version` took over 10 s, as under
  heavy load), rerunning `mur join` adds it.
  - *Who the session is.* OpenCode 1 names no session in a call to an MCP server, so its one server
    process per directory was always one session to Murmur. The plugin's `config` hook, which
    OpenCode runs before it starts any MCP server, gives Murmur's server an ID for that session
    (`MURMUR_OPENCODE_SESSION`), and the plugin's hook requests carry it, so the daemon finds the
    session as it finds OpenCode 2's, and needs nothing new. What the hook gives goes to the
    OpenCode session at work, or the last one that was.
  - *The moments.* `tool.execute.after` is PostToolUse, a `session.status` of busy is
    UserPromptSubmit, and `session.idle` is Stop, interrupted where a `session.error` came first
    (Escape, a failure). Notices and what is unread at a turn's end go in with
    `client.session.promptAsync` as synthetic text, which the model reads and the terminal UI
    doesn't show: a prompt that comes while a turn runs is answered before that turn ends, and one
    in an idle session starts a turn. Each goes in with the agent and model of the session's last
    prompt, or OpenCode would take its default agent.
  - *A turn when idle.* The plugin opens a socket that takes Claude Code's messaging-socket lines,
    as Pi's extension does, and gives its address and token to the MCP server beside the ID, so the
    daemon hands an idle session its message as it does Pi's. The plugin puts it in as a prompt the
    terminal UI shows.
  - *Children.* A session the `task` tool started has a `parentID`. It runs no hook and gets no
    turn, so it never shows as a session of its own (read in the source, not tried). Its Murmur
    calls still go through its parent's server, as its parent's: it sends as the parent and reads
    the parent's inbox, as before the plugin. `tool.execute.before` has the call's session, so the
    plugin could refuse them there (not tried).
  - *Under T3* the plugin does nothing (`T3CODE_CLI_PATH` in OpenCode's environment): no ID, socket
    or hook, so an OpenCode 1 thread is as it was before, and T3 hands it a message as a turn when
    its turn ends. Nobody has tried the plugin in T3, where its turn at an idle session's Stop would
    be one T3 didn't start.
  - *Tried* on OpenCode 1.19.5 (2026-11-15), `opencode serve` in a scratch clone joined with a local
    remote, a free model, and turns started with `opencode run --attach`. In the session's first
    turn a message sent during a `sleep` came in whole through the socket, since the daemon hadn't
    yet seen a turn of it end, and the model answered it with `send`. Idle, the next message started
    a turn with nobody typing. In a later turn a message sent during a `sleep` came as the notice
    after that call, and the model read it with `inbox` and answered. Not tried: the terminal UI.
  - *Not covered.* Two sessions at work at once in one OpenCode 1 process (`opencode serve` with
    two clients) share one session's messages, and a turn goes to the one at work last. A server
    killed by a signal leaves the socket's file in the temporary directory. Not tried on Windows,
    where the socket is a named pipe opened by OpenCode's runtime, Bun.
- **`opencode run` without `--attach`**, in either version, exits when its turn ends, so what the
  hook hands over at the turn's end is marked read and never acted on.
- **Titles and approval.** Outside T3 no title is read for these agents. OpenCode and Grok have a
  rule for tool approval, which join writes; Muse and Pi have none, so in T3 outside full-access
  mode they ask before each call.

## Claude Code's hook is per user

Changed 2026-11-11 (#422). Join used to write Claude Code's hook in `.claude/settings.local.json` in
each worktree of the joined clone, so a session that joined a clone it doesn't work in (`mur join
-C`) never read it: no notice while busy, no `Stop` to keep its turn going, and the daemon never saw
it at work or idle. Tyler's rule (2026-11-11): every harness behaves as closely to T3 Code as it
can, wherever the session runs.

- **Where it is.** In `settings.json` of each Claude Code home, beside the allow rule, so Claude
  Code runs it in every project, as Codex and Grok run theirs. Join and leave take the older hook
  out of the clone's worktrees, since Claude Code would run both, unless join couldn't write a
  home's settings and the older hook is the only one. A leave keeps the per-user hook, like the
  server; `mur uninstall` removes it.
- **Which clone.** The hook reads the record its session's MCP server publishes
  (`murmur/sessions/`), found by `CLAUDE_PID`, or for Codex and Grok by the nearest of the hook's
  ancestors that started a server. The record names the clone, or says the session is in no
  repository and the hook then asks nothing. Where no record is surely the session's, it goes by its
  working directory.
- **Whose record.** Claude Code's process runs one session, so its record is the session's. Another
  agent's hook has only its ancestors to go by, and a record found there may be a sibling's in a
  process that runs several sessions (Codex's app server), or that of an agent that started this
  one; taking it could hand the hook that session's messages. So the record carries the agent's own
  session ID (Codex's from the first Murmur tool call), and another agent's record counts only when
  it has the hook's ID.
- **Not covered.** Pi's extension runs the hook only where the working directory's clone is joined,
  so a Pi session joined from elsewhere gets turns and no notices while busy. Commits made in the
  other repo aren't credited: the daemon watches only its own clone (#405).
- **What it costs.** Every Claude Code tool call on the machine now starts `mur hook` up to twice,
  in repos that never use Murmur too. On the Windows laptop, 2026-11-11, with 41 records in the
  directory: 14 ms for a call the hook skips at once (a `Read`'s `PreToolUse`), 19 ms where it finds
  its session in no repository, and 55 ms with no record outside any repo, where it asks git. Claude
  Code's own shell comes on top and wasn't measured.

## Codex's and Grok's hook

Added 2026-11-09 (#225, part of #205): the hook that tells Claude Code of messages mid-turn and
keeps its turn going at Stop (#202) runs in Codex and Grok too, as `mur hook codex` and `mur hook
grok`. Both take Claude Code's payload and answers.

- **Codex.** Join puts the hook in `hooks.json` in each Codex home, on `UserPromptSubmit`,
  `PostToolUse` and `Stop`, the fewest that mark the session at work, bring notices and keep a turn
  going. Not `PreToolUse`, so Codex's commits aren't credited (#21).
  - *Approval.* Codex runs a hook only once its user has approved it on a review screen in its own
    TUI, which T3 Code, where most Codex sessions run, never shows. Tyler's decision (2026-11-08):
    join writes the approval itself, as `[hooks.state.'<hooks.json>:<event>:<group>:<handler>']
    trusted_hash` in the home's `config.toml`, the entry Codex writes when the user approves. The
    hash is SHA-256 of the handler's settings as compact JSON with sorted keys (codex-rs
    `hooks/src/engine/discovery.rs` and `config/src/fingerprint.rs`, rust-v0.166.0 and
    rust-v0.167.0), checked in the tests against hashes Codex wrote.
  - *The key* names the group by its place in the event's list, so join edits `hooks.json` in place
    and the user's own hooks keep their keys. It spells the path as Codex does (codex-rs
    `utils/home-dir`, rust-v0.167.0); a key in any other spelling is never read. If a Codex release
    hashes differently, the hook silently stops running, which the daemon shows (below).
  - *Seen* on Codex 0.167.0, 2026-11-09, in a throwaway home: no review screen, and the notice
    reaching the model mid-turn in the TUI, in `codex exec` and in a T3 Codex thread, with `Stop`
    keeping the turn going.
- **Grok.** Grok runs every file in `~/.grok/hooks/` without asking, where a project's hooks wait
  for folder trust (Grok Build's `docs/user-guide/10-hooks.md`, read 2026-11-09), so join writes
  `murmur.json` there, on `PostToolUse` and `Stop`. Not on a prompt: Grok discards what that hook
  prints, so notices handed to it would be marked delivered and lost. Grok also fires `Stop` when a
  session closes and ignores the answer, so the hook doesn't ask to keep that one going. From
  documentation and source only: none of it has run.
- **The command line.** These agents run a hook through a shell. On Windows Codex's is PowerShell
  (cmd where there is none), which won't run a quoted path followed by arguments, so the Claude Code
  hook's form exited 1 under Codex 0.166.0. The path is bare with its backslashes, which both shells
  run; its 8.3 short form if it has a space or anything either shell reads as syntax; and `& '…'`
  only if neither works. Elsewhere it is single-quoted for sh.
- **Which session.** Codex's MCP server learns its thread's ID only from a Murmur tool call, so
  before that the daemon can't match the hook's `session_id`. The hook also sends its ancestor
  processes, and if the Codex process among them has one session with no ID yet, that session takes
  the hook's. A process with several (T3's Codex threads) can't be told apart, so the hook speaks
  for none of them until each calls a Murmur tool. The ID the MCP server learns overrides one taken
  from a hook (Sol's review, 2026-11-09: a wrong one could otherwise take another thread's notices).
- **What the hook reads of a tool call** (2026-11-15, #510). After a call, Claude Code's and
  Codex's hooks are given its input and output as `tool_input` and `tool_response`, a shell command
  as `Bash` in both (codex-rs rust-v0.169.0); Grok's as `toolName` and `toolInput`, its shell as
  `run_terminal_command`, with `tool_response` beside its own `toolResult` (its hooks docs, read
  2026-11-15). Codex's output is the command's own, and it has none for a call still running when
  it yields. The hook reads them only to find an issue the call filed or closed ([page](page.md)).
  Murmur's Pi extension passes them under Claude Code's names for a `bash`, `powershell` or MCP
  call, the output shaped as an MCP tool's result (Pi's content blocks and `isError`, so a failed
  close isn't credited), and nothing for a call over 512 KB, as the hook reads at most 1 MiB (#864). OpenCode's plugin passes neither.
- **Hook not running.** A session of an agent the hook runs in that called a Murmur tool over a
  minute ago, with no hook of it since the daemon started, is marked in `who`, on the page and in
  `send`'s answer, and asked to have its user run `mur join` again. It is judged from the daemon's
  side, not by checking the hash, because the ways it breaks (a new Codex hashing differently, a
  hook that fails) leave no error to find.
- **In every repo.** Both hooks are per user. Where Murmur isn't joined the hook finds no daemon and
  prints nothing, which cost 70–100 ms a tool call on the laptop (2026-11-08), plus the shell the
  agent starts it in.

## Muse's hook

Added 2026-11-16 (#461). Muse takes Claude Code's hooks, payload and answers, so `mur hook muse` is
the Claude Code hook with Muse's identity. Seen on Muse 1.4.14 on Windows, in a scratch config and
then in a joined sandbox, with `muse exec`: a message sent while the session slept in a shell
command came as the notice after that call, and at the turn's end `Stop` blocked with the message
whole and the turn went on. A Muse thread in T3 (`muse serve`) works too (Tyler, 2026-11-21). Not
tried: Muse on macOS or Linux.

- **Where.** `hooks` in the same `settings.json` as the server, on `UserPromptSubmit`, `PostToolUse`
  (matcher `*`) and `Stop`, Claude Code's event names, not the camelCase of Muse's protocol. Each
  of those answers reaches the model: `additionalContext` on a prompt or after a tool call, and
  `{"decision": "block", "reason": ...}` at `Stop`, whose next `Stop` comes with
  `stop_hook_active`. A user's own hooks there stay, and uninstall takes out only the groups that
  run `mur hook muse`.
- **The command line.** On Windows Muse runs a hook through cmd, and a token in double quotes broke
  it, a quoted path and a quoted argument alike. So the path is bare, or its 8.3 short form where it
  has a space; a path neither works for is a join problem, not a hook that never runs.
- **Which session.** The payload's `session_id` is the session's `MUSE_SESSION_ID`, which its MCP
  server registered with, so the daemon finds the session by it, as Grok's. Muse also runs side
  sessions in a turn (a reminder model calling `submit_reminder_decision`), whose hooks carry their
  own `session_id` while `MUSE_SESSION_ID` is still the session's; some come after the turn's
  `Stop`. The hook asks nothing for them, as the notice would land in the side session and a call
  after `Stop` would mark the session at work again. A subagent shares its parent's
  `MUSE_SESSION_ID` ([delegates](delegates.md)) and so is skipped the same way, if its hooks carry
  an ID of its own (not tried).
- **Its environment.** A hook gets COMSPEC, HOME, PATH, PATHEXT, SYSTEMROOT, TEMP, TMP, WINDIR and
  Muse's own variables, no APPDATA. The hook looks for its session's record by APPDATA, so on
  Windows it takes the folders Windows records for any of APPDATA, LOCALAPPDATA and USERPROFILE that
  are unset. A user who moved APPDATA for Muse's MCP server alone isn't followed, and the hook then
  goes by its working directory's clone.
- **In T3.** A Muse thread is held by T3's database alone until its hook first runs (#699); from
  then on it is a hooked session, as Claude Code's, and what came during the run is held for the
  turn's end. A Muse session that called Murmur's tools with no hook since is marked "hook not
  running", which a join fixes.

## Antigravity

Added 2026-11-15 (#650), at Tyler's request. Tested in T3 Code on Antigravity 1.5.13 on
2026-11-20. `PreInvocation` delivered notices, `Stop` continued the turn, and an already
registered MCP server picked up its tools from `notifications/tools/list_changed`. Reports from
1.5.13 also cover hook payloads (#888, #892, #894).

- **Homes.** T3 runs each Antigravity instance with a home of its own,
  `userdata/providers/antigravity/<sha256 of the instance ID>`, as its `GEMINI_HOME`. T3 says this
  isolates credentials, hooks and MCP servers, and nothing in its settings moves it. Join writes
  per-instance MCP and hook configuration once T3 has made the home. Standalone Antigravity gets
  its own registration. T3 links each instance's skills to `~/.gemini/config/skills`. Antigravity
  under T3 doesn't read a project's `.agents/mcp_config.json` (tried, 2026-11-15).
- **Which session.** T3 passes `T3CODE_THREAD_ID` to Antigravity (seen 2026-11-20, #886; earlier
  T3 did not, pingdotgg/t3code#34024) and keeps `ANTIGRAVITY_CONVERSATION_ID`, which Antigravity
  sets in its shell commands, as the thread's provider session. Antigravity's MCP server takes its
  thread and identity from `T3CODE_THREAD_ID` when `!agentAbove()`, including headless `t3 serve`,
  as Claude Code does. Antigravity gives `ANTIGRAVITY_CONVERSATION_ID` to shell commands and hooks,
  not to MCP servers. The hook finds a session moved by `mur join -C` by T3's thread ID when
  available, or by the sole Antigravity server record of its client process (#888). The daemon pairs
  the hook's `conversationId` with the server among the hook's ancestors (`hookNative`) and, under
  older T3 without `T3CODE_THREAD_ID`, resolves the T3 thread via `t3db.ThreadOf` as a fallback.
- **Tool approval.** Join adds `mcp(murmur/*)` to `permissions.allow` in
  `antigravity-acp/settings.json` in a T3 instance's home or `antigravity-cli/settings.json` in a
  standalone CLI's home (Antigravity 1.5.13). A settings file join can't edit leaves a note with the rule and path.
- **Its programs** (#709, 2026-11-16; #889, 2026-11-20). T3 downloads the release itself, into
  `~/.t3/tools/antigravity-acp/<platform>/versions/<sha256>/`. Release 1.4.5 ran `agy_acp_server`
  (`.exe` on Windows, `.par` elsewhere) over `localharness_external`, which ran shell commands
  (seen on Windows as powershell under `localharness_external.exe` under `agy_acp_server.exe` under
  T3). Release 1.5.13 runs directly as `agy` (`T3 Code (Beta).exe -> agy.exe -> powershell.exe`).
  `agy`, `agy_acp_server`, and `localharness_external` are all in `agentPrograms` for `agentAbove`,
  so an agent run from an Antigravity thread's shell does not steal the enclosing T3 thread's
  `T3CODE_THREAD_ID`.
- **The hook.** The `murmur` entry in `config/hooks.json` names the event on the command line
  for older versions, as in `mur hook --event Stop antigravity`.
  - `PreInvocation` returns notices as a system message (`injectSteps`) before each model call.
    On invocation 0, `lastUserInput` is the prompt #611 matches.
  - `Stop` can continue a turn after `NO_TOOL_CALL` when background work is done. Older payloads
    can omit `fullyIdle`.
  - `PreToolUse` and `PostToolUse` bracket `run_command` calls for commit credit. The snapshots
    use the command's `Cwd` when supplied.

  Every answer is a JSON object. Hooks run with `cmd /c` on Windows, so a path with a space is
  double-quoted.

## Cursor

Added 2026-11-22 (#918), at Tyler's request. Read in the current CLI, `agent` 2026.11.22-e9042b2
(its package unpacked and run in a scratch home, never installed), in `@cursor/sdk` 1.3.1, which T3
0.1.11 runs Cursor's threads with, and in cursor.com/docs. The Cursor on Tyler's laptop is only an
old editor's leftover `~/.cursor` (last written 2026-06-22, version unknown); nothing here comes from
it. No Cursor account was at hand, so no model has run a turn: the CLI started Murmur's server from
what join wrote and listed its tools, and a stand-in for Cursor's process ran the hook as Cursor
does, against a real daemon. Details and sources are in `internal/join/cursor.go` and
`internal/hook/cursor.go`.

- **Which session.** Cursor starts an MCP server with only PATH, TEMP and the home's variables, and
  no ID for the chat. Hooks get it (`conversation_id`), and so do shell commands
  (`CURSOR_CONVERSATION_ID`). So the hook leaves it in the per-user records (`here.Named`, for the
  one server of Cursor's process, which is both the server's parent and the hook's ancestor), and
  the server waits up to two minutes for it before it registers, by an ID made from it. Only where
  that process is Cursor's command line (`here.CursorCLI`, by the `cursor-agent` launcher beside
  its node), which runs one chat. A new chat's `sessionStart` names it as Cursor starts; a resumed
  chat, which fires none, is named by its first prompt, and comes back at its old address with its
  name and unread messages. A tool call ends the wait, as a call waits for the registration. One
  registered unnamed goes on looking, and tells the daemon the chat when it is named, which keys
  the session by it from then on; until then the daemon pairs its hook with it by process, as
  Antigravity's.
- **The hook.** `sessionStart`, `beforeSubmitPrompt` and `postToolUse` answer `additional_context`,
  which reaches the model; `stop` with status `completed` answers `followup_message`, which Cursor
  submits as the next user message (up to `loop_limit`, 5, in a row). Cursor runs a hook through
  PowerShell on Windows, so the command line is Codex's.
- **Claude Code's hook.** Cursor also runs the hooks in `~/.claude/settings.json` ("Include
  Third-Party Plugins, Skills, and Other Configs", on by default), Murmur's among them, with its own
  payload. Claude Code's hook steps aside for a payload with `cursor_version`.
- **Approval.** `cli-config.json` (in `CURSOR_CONFIG_DIR`, else `$XDG_CONFIG_HOME/cursor`, else
  `~/.cursor`) is strict JSON to Cursor: with a comment in it, Cursor wrote its defaults over it. So
  join writes plain JSON there and leaves a file with a comment alone. A server in the user's
  `mcp.json` needs no approval; only a project's does.
- **Not covered.** A turn for an idle chat (#923; `agent --resume <id> -p` is the way in to try). T3
  Code (#924): one process runs every Cursor thread, and nothing in the hook's payload or the
  server's environment says which thread is which. Its process isn't Cursor's command line, so
  neither the hook nor the daemon pairs a chat with a server there, the server doesn't wait, and
  their messages wait in `inbox`. Past `loop_limit` follow-ups in a row Cursor runs no `stop` hook, so the
  daemon keeps that session at work until its next prompt. A
  second chat started in the same `agent` process is unknown to the daemon, as its process's
  session already has a chat. Commits aren't credited, nor issues filed from Cursor's shell. The
  editor reads the same files and wasn't tried: it isn't Cursor's command line, so it is handled as
  T3's is. `mur who` and `mur
  send` in Cursor's shell, which has the chat's ID, aren't wired up.

## Any agent in T3 Code: the shell commands

2026-11-21, #913 (Tyler approved option 2 that day). `mur join` gives tools only to agents it knows,
so an agent T3 runs that Murmur has no join for (Kiro, fx, any agent from the ACP registry) had
none. Every harness can run a command, so `mur who`, `mur send`, `mur inbox` and `mur status` do
what the tools of those names do (`internal/mcpserver/commands.go`, rendered by `internal/view`).
`lookup`, `tags` and `mute` stay tools only.

- **Which session.** The command's shell has the thread's ID in `T3CODE_THREAD_ID`. Read in T3
  0.1.11's source (2026-11-21, not run): its Claude, Codex, Pi, Antigravity, Kiro, fx and
  ACP-registry drivers set it in the agent's process, so in its shell; Grok's drops it, and
  OpenCode, Muse and Cursor never set it. There the commands stop and say so. The session's ID is
  `threadID(thread)`, the one a Claude or Antigravity thread's own server registers, so the
  address and name are the thread's whichever registers first. If a session is live in the thread
  already (its MCP server's), the command acts as it. A command under two agents' programs (an
  agent started from another's thread shell) has the outer thread's variable, and stops. The walk
  knows only the agents in `agentPrograms`: an unknown harness started from a Claude thread's shell
  isn't seen, and acts as that thread. So does a person typing in T3's terminal for the thread,
  which has the variable too: the thread's session, as its agent is.
- **Lifetime.** A command's hello closes with the command, so the daemon holds the session
  (`proto.Hello.Command`, protocol 24): live while the thread is in use, which is a run going in
  T3's database or a command or run in the last 30 minutes, about as long as T3 keeps an idle
  provider, and so an MCP session, running. A thread T3 shows settled ends it sooner. Then it stops
  as any session does, and its route stays kept, so a message still wakes the thread (handoff.md, "A
  thread that is idle"); the next command registers it again at the same address. A server of the
  thread's own takes the session over from a hold. A daemon restart ends holds, and the next command
  makes one again.
- **A takeover mid-command** (2026-11-22, #953). A command says hello, then makes its requests,
  one per name for a send to several. If the thread's server registers in between at an ID of its
  own, as Codex's does, the daemon lets the command's session go, and the next request is refused
  as not registered (`proto.NotRegistered`). The command then says hello again, which finds the
  server's session, and makes that request again as it, once per command. A daemon that restarted
in between is met the same way. The daemon refuses that
  way before a request does anything, so nothing is sent twice. No other failure is retried: a
  lost connection may come after the daemon sent the message. Resolving the thread's owner in the
  daemon at each request was the other fix; it would need the thread on every request, a protocol
  change, where this one is in the command alone.
- **Delivery.** A turn through `t3 thread send`, as for any T3 thread, and its last line says to
  answer with `mur send NAME "TEXT"`, written out. There is no hook, so no notice mid-turn: busy or
  idle comes from T3's database (#699), and a message waits for the turn's end.
- **One text** (2026-11-22, #1027). `mur send TO TEXT` takes the whole message, and `subjectFor`
  in `internal/mcpserver` makes its subject as the page does (`web.SplitText`): the first line if
  it fits, else the opening words, with the whole text in the body. Agents wrote
  `mur send NAME "<paragraph>"` to the old `TO SUBJECT [BODY]` anyway, so the paragraph was the
  subject and was cut at 120 characters (#1021). `--subject` gives one of the agent's own. Three
  words after `send` still mean SUBJECT BODY, the form v0.5.1 documents, with a note on stderr
  giving the new one. A call that worked before sends what it did, unless it was `TO -` (now
  stdin) or one of its words was `--` or the option (`--subject`, `-subject`, `--subject=…`),
  which now mean what they say here. The send tool's
  subject-only call goes through `subjectFor` too, so a subject a model writes (#1023) would change
  that function alone.
- **Not covered.** Files and `tell_me` in `mur send`; a harness outside T3, which has no thread ID
  to name its session by.
