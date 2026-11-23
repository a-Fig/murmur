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
  the later is a session of its own. Where the two are MCP servers of one client process, it
  stays one for as long as its inbox is kept (`later` in the state file), so each comes back to
  its own name whichever reconnects first. Until 2026-11-16 (#38) the first back took the plain
  key and its sibling's name, and both names changed. A second program on the same conversation
  isn't held to this: once the first has gone it takes the conversation's name. A client that
  doesn't say which process it is counts as one process. A server process that is started
  again has a new ID, and so a new name, as before. A key that comes back under another ID while
  its old session is in its grace takes the name with it, so a message to it isn't left in an
  inbox about to stop (2026-10-29).
- **A Codex session outside T3 follows its thread** (#36, 2026-10-30). Codex's MCP server learns the
  thread ID at its first tool call (`_meta.threadId`), and the key is then `codex:<threadId>`. Codex
  keeps that ID across resume and compaction and gives a new one on `/new`, `/clear` and a fork,
  which is the life a name should have. So a Codex CLI session whose MCP server restarts has a new
  name until its first tool call, and then takes the thread's name back, as a T3 thread does.
- **A Codex thread that moves between programs keeps its name** (#568, 2026-11-16). Codex keeps the
  thread ID when another program resumes a thread, as the app does one begun in the command line
  (read in openai/codex at 5cb38e7; the app is closed source and wasn't run), and each program
  starts an MCP server of its own for it. Once the first program's server has stopped, the thread's
  key is free and the name comes back at the first tool call, as above. While it runs, which can be
  after its terminal closed ([handoff](handoff.md)), there were two live sessions of one key, and
  the later kept the name it registered with, so the conversation showed a new name for having
  moved. Now the session whose tool call showed the thread last takes the key and the name, with
  what the other had unread, and the other gets a new name and keeps it (`ThreadLeft` in the state
  file, so a daemon restart doesn't hand the name back to whichever registers second). Only a
  server's first registration or its first word of the thread does this, not one coming back from
  its grace. Only between client processes: two MCP servers of one Codex process on one thread are
  still two sessions, the later with a name of its own. And only outside T3.
  - *The registry still has the name at the first session's address*, where another device sends
    once no presence shows the name. So what arrives for that session after it has stopped counts
    as the thread's: it is forwarded to the name's holder, or kept for the next session that takes
    the name.
  - *Cost.* A person who goes back to the first program and carries on there talks to a session
    with the new name. Its server said the thread once and says nothing more; a Codex hook does
    report the thread with its process, and nothing acts on that yet. The first session's status
    stays with it, so `who` shows none under the thread's name until the agent sets one. If the
    app forks a thread instead of resuming it, the thread ID is new, and so is the name, as for
    any fork.
  - *Not covered.* Until its first Murmur tool call the second program's session shows the name it
    registered with, as a restarted server's does. Whether a turn can be started in the app's
    session wasn't tried: the command-line route is taken only by a server whose parent program is
    named `codex`.
- **A Claude Code conversation that moves between the command line and T3 keeps its name** (#643,
  2026-11-16). T3 imports a command-line conversation and resumes its Claude session, and `claude
  --resume` goes on with one T3 had, so the Claude session ID stays the same. The name key didn't:
  it is the T3 thread's in T3 and the Claude session's outside it, and each had a name of its own.
  Now a Claude Code session in a T3 thread gives its two keys one name: where one has a name and
  the other none, the other takes it (`shareNameLocked`). With the other program's session still
  open, the rule above applies, with the conversation known by its Claude session ID instead of
  the key: the session that registered last takes the name. A session in T3 keeps its ID across
  its server's restarts (#642), so its `ThreadLeft` record goes at its next first registration,
  and the thread can take its name back then. The later of two MCP servers of one process, a
  session of its own with its own name (#38), gives nothing up. A thread whose name came from the
  command line keeps its own address (`NamedFromCLI`): the registry has that name at the command
  line's session, which goes on using it, so #642's taking of the name's address would have them
  ask for one ID. A T3 session that registers before it knows its thread has an ID made from its
  Claude session, as the command line's is, so the name's address alone can't say which of the two
  the name was given to. A name given first to such a T3 session is recorded (`NamedInT3`), and
  only such a name lets the thread take that address; any other counts as the command line's
  (Sol's reviews). Nothing in T3 itself was run: its import and resume were read in its code for
  #643.
  - *Cost.* After `/clear` in T3 the thread's name stays the old conversation's too, but the
    thread's Claude session is the new one, so the rule above no longer matches the two. Whichever
    of them registers while the other runs gets a name of its own: the old conversation resumed on
    the command line while the thread runs, or the thread back while the old conversation is open
    there. It keeps that name after the other stops, until the daemon works out its key again: its
    server registers again, a hook moves it to another Claude session, or it finds its thread. A
    command-line session whose MCP server restarts after its grace, as `/mcp` can make it, takes
    the name back from a T3 session that is open on the same conversation.
  - *Cost.* A daemon from before #643 recorded nothing in `NamedInT3`. A T3 session from then that
    registered before it knew its thread, and finds it only after the upgrade, gets the thread an
    address of its own, made from the thread, instead of the one its name is at. Its name doesn't
    change.
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
  - *A name only a presence shows* (#40, 2026-11-15). The registry can lose a name while the device
    that holds it can't push, and a daemon that never read the name then learns it only from that
    device's presence; nothing in the history it reads looks damaged. So a presence showing a name
    the daemon has never seen has the next sync read the registry from its start, and write the
    snapshot if the name isn't there. Not at once: a sync's fetch brings a presence as it is then,
    which can be newer than the registry it listed, and the next sync lists after it. A push clears
    a repair only if it is a new root holding every name in use when it lands, since a sync can find
    a name lost while the push is out. An attempt on 2026-10-29 decided at once and cleared the flag
    on any push, and Codex found both races in it.
  - *Trust.* This lets a presence cause a rewrite of the registry, but gives no one more than push
    access already does. Whatever a presence names is used for good on every daemon that reads it,
    and goes into any snapshot written since; a seal shows a presence is its device's, not that
    the device is real, as anyone who can push can seal one for a new device
    ([encryption](encryption.md)). Nor need the presence be fresh: a stale one's names were in use
    all the same. The cost, a new root that every daemon reads from its start, is one push access can
    cause as often by damaging the registry itself.
- **Without the remote**, a session that finds no name set aside works as `device/session`, shown as
  pending, and the daemon keeps trying, at once when the remote is back.
- **Sending to a name.** A name is looked up in this device's sessions, then in this device's record
  of its last holder (below), then in other devices' presence, then in the registry, so a message to
  a session that has stopped is kept for it like any other. A device ID shaped like a name is read
  as a device if no session has that name.
  - *When a presence shows a name that isn't its device's* (#604, 2026-11-15). A presence is
    whatever its device wrote. While the device the registry has the name on is up, only its
    presence counts, and if no session there shows the name the message goes to the registry's
    address, where it is kept. It was the first presence by device ID, so a device that listed
    someone's name was sent their messages when its ID sorted first, or when their session had
    stopped. With the registry's device gone or stale, a presence still being written goes before
    a stale one: a clone that loses its keys file comes back under a new device ID with its names
    ([encryption](encryption.md), #617), and the registry still has them on the old one. Until the
    old one goes stale, a send to such a name still goes to it.
    `who`, `lookup` and the page still show the name on each device that claims it, and a message
    from such a session shows the name as its sender: taking the name off a presence the registry
    disagrees with would hide that clone's names for good.
- **A name a session has left.** A session that followed Claude Code (#9) or Pi (#1145) to another
  conversation isn't called its old name any more, but the registry still has that name down for
  its Murmur ID. A send to the old name reaches the session, and says it is called something else now. An error
  saying the conversation ended would drop messages to a session that is alive and could read them
  (2026-10-29).
- **A session that has left presence is shown by one name** (#502, 2026-11-15), the same in the
  inbox, activity, `lookup` and the mute checks, so the name an agent replies to or mutes is the one
  it was shown. Before, an address that had held several names was shown by whichever a map gave
  last, which could change from one call to the next. One of this device's goes by the name it took
  last, which the device records. Another device's goes by the name its presence last showed it by
  (#602, 2026-11-16): a session waiting on a new name lists the one it went by last (`was`), so
  every device shows it by the name its own device does, and the device keeps that name a month
  after it was last seen (`peerNames`). One never seen in presence goes by the first by spelling
  of the names the registry gave its address: the registry keeps no order of them, and a record's
  time is its writer's clock. That can be an older name; a send to it reaches the same session, as
  above. A session still listed without a name whose name has moved to another session on the same
  device — as after a Codex or Claude Code handoff with no name set aside — is shown by its address
  instead, not by the name the other session holds (#875, 2026-11-20).
- **A name held by another session** (#37, #46). A `/resume` back to an earlier conversation can put
  its name on a session whose Murmur ID isn't the one the registry has. A daemon records in its
  state file the last session to hold each name and the last name each of its sessions held. A
  message for a session of this device that isn't running goes to the session that holds the name it
  last had, if that one is registered or stopped within `StoppedInbox` and the name is still the
  last it took: one hop. A session that takes a name also takes the kept inboxes of stopped sessions
  whose last name it was. So a conversation comes back as whichever session takes its name next, and
  a message to the name waits for any of them (2026-10-29). One ID can hold several names over
  time, after a `/clear`, so a message sent by name also carries the name, as `toName` (#45,
  2026-11-16). The receiving daemon then forwards it by that name, not by the last name the address
  had, even while the session at the address runs, and a message that waits in a kept inbox goes
  to the next session to take the name it carries. A daemon from before that ignores the field and
  goes by the address, as it did. It stays at the address when no other session has taken the name
  since. The sending device's mute, offline and do-not-disturb checks still look at the address's
  session. A session that comes back under its own ID still takes its whole inbox, messages for a
  name it left among them (#847).
- **A long-gone session's name is forgotten after 30 days** (#747, 2026-11-16). The daemon keeps the
  name each of its sessions took last, to label a stopped session and to pass a message for an old
  address on to the name's holder. T3 makes a new session for a thread each time its MCP server
  restarts, so that record only grew, and the whole of `state.json` is written at every save. An
  entry now goes 30 days after its session last took the name, unless the session runs, its inbox
  is kept, it holds the name, or it is the session the registry has the name down for. The cost:
  after that the session's old messages show its address on the page and in `lookup`, and a
  message sent to that address is dropped, as one to any session that is gone.
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
| T3 Code | thread ID | everything, for good | a fork, which is a new thread | `T3CODE_THREAD_ID`, for Claude Code; else T3's database ([handoff](handoff.md)) |
| Codex | `threadId` | compaction, every kind of resume | `/new`, `/clear`, a fork | `_meta.threadId` on every MCP `tools/call`, and a hook's `session_id` |
| Pi | the session's file | `/reload`, its MCP server restarting | `/new`, `/fork`, switching with `/resume` | `MURMUR_PI_SESSION_FILE`, which Murmur's extension gives the server, and after a move the file it left (below) |
| Cursor | chat ID (`conversation_id`) | `agent --resume <id>`, `agent resume` | a new chat | A hook's `conversation_id`, and `CURSOR_CONVERSATION_ID` in its shell commands; none in an MCP server's environment, so the hook names it to the server ([Cursor](clients.md#cursor)) |

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
  under one thread changes when the user switches Claude accounts, and has gone A → B → A. Its name
  and its address both follow the thread ([below](#one-address-per-t3-thread)).
- **Codex's MCP servers get a bare environment**, about 20 Windows core variables and no `CODEX_*`
  or `T3CODE_CLI_PATH` unless the Codex config forwards them, as `join` does, so the server learns
  its thread at its first tool call. Hooks under Codex inherit the `CLAUDE_CODE_SESSION_ID` of any
  Claude Code session that started Codex, so that variable doesn't prove a hook runs under Claude
  Code.
- **A Pi session follows its moves** (#1145, 2026-11-23). Pi runs one session per process and moves
  it to another file on `/new`, `/fork` and `/resume`. Murmur's extension then
  starts the MCP server again with the new file, and the server's ID is made from the file, so the
  move used to make a new session with a new name. The old session's server was gone for good, and
  what was sent to the old name waited in its inbox until its grace ran out, while `send` said it
  was sent. Now the extension passes on the file the session left, which Pi's `session_start`
  names (`previousSessionFile`, in Pi 0.99.0's types), and the server's hello says it (`From`).
  The daemon gives the server the address of the session of the same Pi process that had that
  file. The hello takes that session over, as a server back from its grace does, and its key, and
  so its name, follow the new file, as a Claude Code session's do after a hook. Its address, inbox
  and place on the bus stay, so a message to the old name still reaches it
  ("A name a session has left", above), and `who` shows it once. A `/resume` back to a file that
  had a name takes that name, as in Claude Code. If that file's own session is in its grace, its
  Pi gone, the moved session takes what it had unread when the grace runs out, as the name's
  holder, as after a Claude Code `/resume`.
  - *Which ID each file has.* After a move, the session's ID is no longer the one its file makes,
    and the file it left still makes that ID. So the daemon records both in its state file
    (`PiFiles`): the new file has the session's ID, and the file it left gets a new one. A server
    on either file registers at the recorded ID, with or without `From`. So a `/reload`, which
    starts the server again with no `From`, or Pi started again on the file, finds the session that
    moved there, with what it had unread. Another Pi on the file left is a session of its own at
    once, and is never handed what the moved session had. A server of the same Pi process on the
    file it is on is that process's session there, recorded or not. An entry goes 30 days after
    its last use, once no session runs or is kept at either ID.
  - *Two Pi processes on one file.* They are two sessions. A `/resume` of a file another Pi has
    open, live or in its grace while its server restarts, still moves the session there, but the
    file isn't recorded as the moved session's and nothing of the other's is taken: the other Pi's
    server, started again, is back at its own address with its unread messages. Its name may
    change, as the moved session took the file's name while it was away. Two Pis that open one file
    without a move are as before: the later is refused for about 7 s, then takes an ID of its own.
  - *Order.* Pi closes the old server as it starts the new one. A hello that comes before the
    daemon has seen the old connection close is refused as in use and tried again. After three
    refusals, about 7 s, the server gives up the move and registers at its file's own ID, as
    before.
  - *Older servers.* Only a server that says it goes by the ID the reply names (`TakesSelf`) is
    given a recorded ID. One from before #1145, still running after an update, registers at the ID
    it asks for, as before.
  - *Not covered.* A move the daemon didn't see while the old session was still registered, because
    the daemon restarted or the grace ran out in between, makes a new session, as before. Not run
    with a real Pi: the extension's part was tried with a stand-in for Pi's API.
- **Two live processes can hold one ID.** Claude Code lets a session be resumed twice at once.

## One address per T3 thread

#642, Tyler's choice (2026-11-15). T3 keeps one provider session per thread for each provider
instance (a Claude account or config home), each with its own Claude session ID, and goes back to
an instance's own on a switch back. Until #642 the session's ID was made from the Claude session
ID, so a switch registered a new session at a new address. The name followed the thread, but once
the old session stopped, other devices showed its messages under a bare address, as another
conversation: the registry has a name at the first address that took it, and only the owning device
knew the rest.

- **The ID comes from the thread.** T3 sets `T3CODE_THREAD_ID` for the Claude Code process it runs
  (read in T3 0.1.x's server, 2026-11-15), and Claude Code's MCP servers inherit it. Inside T3
  (`T3CODE_CLI_PATH` set too) such a server's ID is a hash of the thread, and its hello carries the
  thread, so it never looks in T3's database. A switch is then a takeover of the same session, in
  its grace or after it stopped, with its inbox, name and address. T3 sets the variable for ACP
  agents too, but Muse passes on only what `join` lists, and Pi and Grok weren't tried; they and
  Codex, whose servers get a bare environment, find their thread as before.
- **Not the thread's own.** Every process under a thread inherits the variable, so a `claude` run
  from the thread's shell, perhaps in another clone, would pass for the thread. The server ignores
  the variable when an agent's program (`claude`, `codex` and the like) runs above its client,
  looking up through shells and the programs that run commands for them (`node`, `python`, `go`);
  T3 runs the thread's client itself. Not seen: an agent that runs as `node` (an npm install), or
  a `claude` started through any other program. On the thread's own daemon the hello is refused
  anyway while the thread's session is live, as any ID in use is, and after three refusals, about
  7 s, the server registers as before #642: an ID from its Claude session, its thread looked up in
  T3's database.
- **Threads named before #642** have their name in the registry at the address of the session that
  first had it, made from a Claude session ID. A hello that says it was made from the thread
  (`ByThread`, protocol 22) is registered at that address when it is this device's, still goes by
  that name, and is not held by another session (such as the command line resuming the same
  conversation, #873), and the server goes by the ID its reply names. When another session holds
  that address, the thread keeps its own thread-derived address so the conversation can move
  without colliding on one ID. A hello without `ByThread`, from a server started before an update,
  is never moved.
- **Hooks** still find a session by `CLAUDE_PID`, then by Claude session ID, never by thread. A hook
  of the new instance's process finds the session once its server has registered; one of the old
  process, if any runs after that, finds nothing and changes nothing.
- **Published.** The address is 24 bits of a SHA-256 of the thread ID, which stays on the device, in
  place of 24 bits of the Claude session ID (#51). A keyed hash, as #51 proposes, would replace both
  the same way.
- **Not covered.** If T3 started the new instance's process before letting the old one go, the new
  server would give up the thread's address after 7 s, for good, and the thread would split as
  before #642. In the switch seen for #636 the old one had gone about 40 s before. A conversation
  moved between the Claude Code CLI and T3 keeps its name since #643, but not its address: the
  thread's is made from the thread, the command line's from the Claude session.
