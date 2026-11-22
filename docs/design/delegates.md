# Settled threads and delegates

Some sessions stay off the mesh: T3 threads that are settled, and threads that another thread
started.

- **Settled T3 threads are offline** (Tyler, 2026-11-07, #189). A thread T3 shows as settled
  (`settledOverride` in the thread's payload), archived or deleted leaves presence, so `who`,
  `lookup` and the page show it gone. Its device lists it in presence's `offline`, and a daemon that
  reads that refuses a `send` to it; a message that reaches the device anyway, from a daemon older
  than the field, is dropped. Typing in the thread unsettles it, and it is back with the next read.
  T3 settles many threads by itself, often the moment a turn ends (671 of 854 threads on the laptop
  were settled, 2026-11-07), so this takes most finished T3 threads off the mesh.
- **Delegates go through their parent** (Tyler, 2026-11-07, #188). A thread another thread started,
  a T3 `delegate_task` child or a provider's subagent thread, has its own MCP server and registered
  as a session of its own, with a name and an inbox (reproduced 2026-11-07). T3 marks such a thread
  with `lineage.parentThreadId` (624 such threads on the laptop that day, none a user started), so
  it is off the mesh the same way as a settled one, and a refusal names its parent's session. It
  can't `send` or set a status; the refusal tells it to ask the thread that started it. Its commits
  are credited to its parent's session. The daemon reads a thread's state as its session registers,
  whether a server or a shell command registers it, so a delegate's first `send` is refused and it
  is never listed (#945). Only if that read fails is it on the mesh until the next refresh, and a
  name it took then is seen by nobody.
  A Claude Code subagent inside one session never registers at all.
- **Pi** (2026-11-11, #457) has no subagents of its own; packages add them. `pi-subagents` (0.31.0)
  runs each as a child `pi` that loads the user's extensions, Murmur's among them, and sets
  `PI_SUBAGENT_CHILD=1`, in which Murmur's extension does nothing (seen with Pi 0.99.0). Not
  covered: `@gotgenes/pi-subagents` (16.6.0) makes its subagents inside the parent's process, where
  nothing tells them apart; nobody has run one to see whether it registers.
- **Muse Code** (2026-11-15, #458): a subagent shares its parent's `MUSE_SESSION_ID` and `mur mcp`,
  so it stays off the mesh and speaks as its parent (seen with Muse 1.4.14 in T3).
- **The subagents Codex spawns are delegates too** (Tyler, 2026-11-12, #454), outside T3 Code, where
  T3's database knows nothing of them. Since #447 such a thread could otherwise be handed a turn
  nobody asked for ([handoff](handoff.md)).
  - *The mark is Codex's own.* Its state database (`state_N.sqlite`, `threads.source`) holds a JSON
    object with a `subagent` key, naming the parent, for a thread Codex made for another (one a
    thread spawned, a review, a guardian), and a plain word for one a person's client started.
  - *The call says so too* (#487, protocol 20). A subagent's tool calls carry `thread_source`
    "subagent" and `parent_thread_id` in `_meta`'s `x-codex-turn-metadata`. Codex starts a
    subagent's MCP server only for its first call to that server's tools, which follows within about
    15 ms, while a person's thread gets its servers when it starts (Codex 0.164 and 0.167, `codex
    exec` and `codex app-server`, with a scratch MCP server, 2026-11-12). So a Codex server outside
    T3 holds its first hello for up to a second for a call, and tells the daemon what the call said:
    a subagent registers as a child, is never listed, has no start or stop logged, and its first
    `send` is refused. The cost: `codex exec` fires its first prompt's hook before the held hello,
    so that hook finds no session.
  - *The thread's ID* comes only from a Murmur tool call. Codex's hook reports one earlier, but a
    hook is paired with a session by process, and a subagent runs in its parent's process, so a
    hook's ID marks nobody.
  - *Inside T3* nothing is read from Codex. Tyler's rule (2026-11-12, #452): a thread shown in the
    user's list of sessions is on the mesh, and for T3's threads T3's database says which those are.
  - *No handling for subagent tools* (Tyler, 2026-11-12, #481). A tool that runs Codex as another
    agent's subagents, such as Tyler's own Claude Code plugin codex-sub, gets threads that Codex
    records as a terminal's. #473 first recognised codex-sub by `threads.originator`; that was
    special handling in a shared tool for one person's plugin, and came out before the merge, so
    such a thread is on the mesh like any Codex session.
  - *What is left.* A parent is named only if the thread that spawned it has called a Murmur tool
    too. Nothing is saved: after a daemon restart a Codex child is marked again at the first read. A
    Codex subagent of a thread in T3 is left to T3. Murmur's own server wasn't run under a live
    Codex subagent: Codex's side was seen with the scratch server, and Murmur's with stand-ins.
- **A hook can say its session is a child** (2026-11-12, #452, protocol 19). Where the client knows
  a session's parent and Murmur has a hook in it (OpenCode's plugin, #455), the hook's request
  carries `child` and, if it knows, `parentNative`, the client's own ID of the session that started
  it. The daemon keeps that by the session's own ID, so it holds for a session whose MCP server
  registers after the hook spoke. Nothing takes the mark off: a session started by another doesn't
  stop being so.
