# Session names

## Session names

Tyler's decisions, 2026-10-28 (#20). Every session has a name of two words, an adjective and a noun
(`quiet-otter`), unique in the repo and never reused, even after its session ends. The name is the
address: `send` takes it, `who` shows it, and notices and `inbox` name the sender. A message still
carries `device/session` on the bus, so the event format didn't change and a daemon that knows no
names routes by it all the same.

- **Taking a name.** A daemon picks a random name nobody has, commits it on `refs/murmur/names` (one
  commit per record: the name, the device, and Murmur's session ID, or none for a name set aside),
  and pushes with a lease on the registry tip it last read. The name is the device's only if that
  push lands. If another device got in first, the daemon fetches, reads what it missed and picks
  again, backing off after 5 refusals in a row, up to 5 minutes.
- **Names set aside** (#141, 2026-11-01). A name took 2–16 s to land after its session started, with
  the network up, and a session showed as pending all that time; in an outage it waited for the
  network. So a daemon keeps 10 names registered ahead for sessions it has yet to start (`NamePool`;
  3 in v0.2.2, 10 since #145 so a burst of sessions or a long outage doesn't empty it). A session
  takes the oldest at once, without the remote, and the daemon records the assignment and sets
  another aside in the same push.
  - *How others read one.* A later assignment of a reserved name counts only from the device that
    reserved it, and a reservation of a name already in use counts for nothing, so a reader of the
    whole history and one that reads only what is new agree. Until the assignment lands, other
    devices find the session by the name in its device's presence. A daemon from before reservations
    reads one as a name in use, so it never hands the name out either.
  - *Sending to a reserved name* nobody has taken is an error that says that device has set it aside
    for a session it has yet to start.
  - *A device that never comes back* leaves its names set aside for good: at most 10 names a device,
    out of 1.25 million, and names are never reused anyway. A daemon that loses its state file burns
    its pool the same way.
  - *A first start with no network* has no pool, and its sessions are pending, at `device/session`,
    until the remote is reachable.
- **A name follows the client's session ID** ([below](#when-a-session-stays-the-same-session)). The
  key is the T3 thread when the session has found one, else Claude Code's session ID, else the Codex
  thread ID, else the ID Murmur gave the session. The same key keeps its name across the MCP server
  restarting, the daemon restarting and a resume, and different keys never share one. The
  key-to-name map stays in the daemon's state file, so the client's ID is never published. A resume
  on another device is a new session there. Two live sessions with one key would share a name, so
  the later is a session of its own. A key that comes back under another ID while its old session is
  in its grace takes the name with it, so a message to it isn't left in an inbox about to stop
  (2026-10-29).
- **A Codex session outside T3 follows its thread** (#36, 2026-10-30). Codex's MCP server learns the
  thread ID at its first tool call (`_meta.threadId`), and the key is then `codex:<threadId>`. Codex
  keeps that ID across resume and compaction and gives a new one on `/new`, `/clear` and a fork,
  which is the life a name should have. So a Codex CLI session whose MCP server restarts has a new
  name until its first tool call, and then takes the thread's name back, as a T3 thread does.
- **Never reused, whatever happens to the registry.** A daemon keeps every name it has seen, in the
  registry's history or in any presence, and the set never shrinks, so a registry that is deleted,
  overwritten or force-pushed frees nothing. When the tip isn't readable, or its history no longer
  holds what was read before, the daemon writes a new root holding a snapshot of every name it has
  seen, leased on the tip it found, as soon as it notices, so a device joining later learns what was
  taken. A listing with no registry, when one was read before, is checked again before it counts as
  a deletion: a sync lists the remote before it takes the lock that records the daemon's own pushes
  (#80, 2026-10-30; the first version took that for a deletion and replaced the registry's history
  with a snapshot).
  - *What one record can add* (2026-11-09). Since the set never shrinks, a reader bounds what a
    single record adds: a snapshot counts only on the root of a history read from its start, a merge
    that brings in another root is damage to repair, and a presence listing more than 200 sessions
    (`bus.MaxSessions`) is unreadable. Push access can still mark names used, more slowly. Once
    1,000 random picks have all been in use, a daemon walks the lists for a free name.
- **Without the remote**, a session that finds no name set aside works as `device/session`, shown as
  pending, and the daemon keeps trying, at once when the remote is back.
- **Sending to a name.** A name is looked up in this device's sessions, then in this device's record
  of its last holder (below), then in other devices' presence, then in the registry, so a message to
  a session that has stopped is kept for it like any other. A device ID shaped like a name is read
  as a device if no session has that name.
- **A name a session has left.** A session that followed Claude Code to another conversation (#9)
  isn't called its old name any more, but the registry still has that name down for its Murmur ID. A
  send to the old name reaches the session, and says it is called something else now. An error
  saying the conversation ended would drop messages to a session that is alive and could read them
  (2026-10-29).
- **A name held by another session** (#37, #46). A `/resume` back to an earlier conversation can put
  its name on a session whose Murmur ID isn't the one the registry has. A daemon records in its
  state file the last session to hold each name and the last name each of its sessions held. A
  message for a session of this device that isn't running goes to the session that holds the name it
  last had, if that one is registered or stopped within `StoppedInbox` and the name is still the
  last it took: one hop. A session that takes a name also takes the kept inboxes of stopped sessions
  whose last name it was. So a conversation comes back as whichever session takes its name next, and
  a message to the name waits for any of them (2026-10-29). The name sent to isn't on message
  events, so the receiving daemon can't tell which name was used; putting it there is a bus format
  change, Tyler's to decide (2026-10-29).
- **V1 daemons** don't read the registry, and their presence has no names. Their sessions show
  without one and are reached at `device/session`.

## When a session stays the same session

A session's name lasts as long as its client treats it as the same conversation, so it follows the
client's own ID: a resume keeps the name, and `/clear` or a fork gets a new one (Tyler, 2026-10-28).
Each client keeps and changes that ID at different moments. Researched 2026-10-28 on Claude Code
2.1.310 and 2.1.312, T3 Code 0.1.2 and Codex CLI 0.164.0; the evidence is in #19.

| Client | The ID | Kept across | New on | Where Murmur can read it |
|---|---|---|---|---|
| Claude Code | session ID | compaction, every kind of resume, a killed process resumed | `/clear`, `--fork-session`, `/branch`, switching with `/resume` | A hook's `session_id` on stdin, always current |
| T3 Code | thread ID | everything, for good | a fork, which is a new thread | Nowhere, as shipped; Murmur reads T3's database ([handoff](handoff.md)) |
| Codex | `threadId` | compaction, every kind of resume | `/new`, `/clear`, a fork | `_meta.threadId` on every MCP `tools/call`, and a hook's `session_id` |

- **Claude Code's MCP servers can't be trusted with the ID.** `CLAUDE_CODE_SESSION_ID` is set once
  per MCP server process and never updates, so it goes stale after `/clear`, `/branch` or switching
  with `/resume`, and under `claude -p --continue` it is wrong from the start. Hooks always see the
  live ID, and they get `CLAUDE_PID`, the MCP server's parent process. So the daemon learns the live
  ID from a hook and matches it to a session by that PID (#9, built 2026-10-29). The session takes
  the name its new key has had, or a new one, and keeps its Murmur ID, its inbox and its place on
  the bus; nothing is written there, and the daemon's log names each switch by names only, since
  Claude IDs are never published. A PID moves only the sessions that registered with it, so a hook
  takes no session of another process. Where no session has the hook's PID (a wrapper between Claude
  Code and the server, say), the sessions with the hook's Claude ID are the hook's.
  - *Reconnecting.* Every response carries the Claude ID the daemon holds for the session, and the
    server sends it in later hellos instead of its stale environment's. A daemon that restarts
    between a switch and the server's next call can bring the stale ID back until the next hook
    corrects it.
  - *The parent PID.* `mur join` registers mur itself with Claude Code, so the server's parent is
    the Claude Code process unless the user wraps it. Seen 2026-10-29 on Windows with a plain Python
    server: its parent was the `claude.exe` that `CLAUDE_PID` named, with a `cmd.exe` between them
    only where the user's config said `cmd /c`. A wrapper means no PID match, so that session falls
    back to its Claude ID and doesn't follow.
  - *When it happens.* The hook isn't on `SessionStart`, so a session moves at its first tool call
    or prompt after the switch; until then it shows its old name, and what is sent to that reaches
    it.
  - *Subagents.* A subagent's hooks carry its parent session's `session_id` and `CLAUDE_PID`, with
    `agent_id` set (2.1.312, 2026-10-29). One started before a `/clear` may still report the
    conversation the session left (inferred, not seen; #231), so a subagent's hooks never move a
    session.
- **Under T3 the session is the thread**, not the provider's session under it. The Claude session ID
  under one thread changes when the user switches Claude accounts, and has gone A → B → A. T3 gives
  MCP servers and hooks no way to learn the thread ID, and Tyler decided on 2026-10-28 not to change
  T3's source, so Murmur looks the provider's session up in T3's database (#56;
  [handoff](handoff.md)).
- **Codex's MCP servers get a bare environment**, about 20 Windows core variables and no `CODEX_*`
  or `T3CODE_CLI_PATH` unless the Codex config forwards them, as `join` does, so the server learns
  its thread at its first tool call. Hooks under Codex inherit the `CLAUDE_CODE_SESSION_ID` of any
  Claude Code session that started Codex, so that variable doesn't prove a hook runs under Claude
  Code.
- **Two live processes can hold one ID.** Claude Code lets a session be resumed twice at once.
