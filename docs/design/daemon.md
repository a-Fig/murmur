# One daemon per device, a thin MCP server per session

```
GitHub (refs/murmur/*)
        ▲  one poller, one writer per device
  mur daemon  (per device, per repo)
        ▲  local socket
   ┌────┼──────────┬──────────┐
 MCP   MCP        MCP       page
(T3)  (Claude    (Codex)   (humans)
       Code)
```

One device runs many sessions, often across worktrees. The daemon is the only process that writes
the device's refs and the only one polling GitHub; sessions connect to it. Each session starts its
own MCP server process (that is how Claude Code runs stdio MCP servers), and the first to start
launches the daemon. Under a job that forbids breakaway, as Codex's MCP servers are on Windows, the
daemon is started through WMI so it outlives that session (2026-10-29).

## Sessions that come and go

- **A 60-second grace** (2026-10-28). A session's MCP process can restart while its conversation
  carries on: T3 Code restarts it on every interrupt, after 30 minutes idle and on every app restart
  (one thread's restarted 8 times in about 2 days, 2026-10-28), and Claude Code on `/mcp` reconnect
  and on resume. So a dropped connection keeps the session in presence for 60 s, and a hello with
  the same session ID in that time takes it over with its inbox and unread notices, writing nothing
  to the bus. Only when the grace runs out does the daemon announce `session.stopped`, even after a
  crash.
- **A start or stop that fails to push is written later** (#434, 2026-11-15). Presence lists a
  session either way, so `who` and `lookup` don't need these events. Every device's Recent view and
  its log notes do, though, and later events build on the log's last tip that landed, so nothing
  else would carry a lost one. They wait in the state file (`unwritten`, at most 100) and are written
  again, in order, after the next presence that is pushed, in this run or the next. A write that
  failed may have landed all the same, so a start or stop can be on the log twice: the Recent view
  keeps one by its ID, and the log notes show the line twice. One written late shows at its own
  time, older than what came meanwhile.
- **A send whose push ends unsure is pushed again, not refused** (#781, 2026-11-16). Tyler: "just
  keep retrying automatically, and dont hand the message back as unsent until we've tried alot and
  are giving up." A push that timed out or lost its connection under way may land yet, so its
  commit stays as the tip that later writes build on, and the sender is told it went, with a note
  not to send it again. The daemon pushes that tip again, waiting 5 s, then twice as long each time
  up to a minute. Pushing the same commits again is safe whether or not the first push landed, and
  any later write that lands carries the pending one with it. `unwritten`, above, makes a new commit
  on each try, which would put a message that landed late on the log twice. After 30 minutes (the
  same as a `tell_me` wait) it is given up: the sender gets a notice in its inbox saying whether the
  remote has it. A daemon that stops gives up what is pending, and keeps nothing across a restart
  (`internal/daemon/pending.go`).
- **A stopped session's inbox is kept 24 hours**, across daemon restarts, and given back if the
  session returns. A session still connected when the daemon stops counts as stopped from then
  (#736): until 2026-11-16 no stop was recorded for it, and one that didn't reconnect within two
  minutes of the next daemon's start lost its inbox and its T3 route. A daemon that is killed
  still records none. For its first minute the next daemon hands such a session's thread nothing
  while T3 shows a run of it going or its thread is unread, so its server can reconnect first.
  Before 2026-10-30 (#30) a laptop shut overnight lost every message sent to its
  stopped sessions. A daemon back after more than 10 minutes skips what it missed, except for
  sessions stopped within the 24 hours: for those it queues what was sent after they stopped (by the
  sender's clock, with a minute's allowance), messages to the device or to everyone included. One
  back within 10 minutes also queues what it missed for the sessions that reconnect within 30 s of
  its start. A first start has no stopped sessions and skips all of it, and nothing sent before a
  session first started is replayed to it.
- **Addresses.** A message is addressed on the bus to `device/session`, which a session's name
  stands for ([session names](session-names.md)). Presence carries enough description (name, agent,
  branch, what it is doing) for an agent to choose a recipient.
- **Exit.** A daemon with no sessions, no open page ([the page](page.md)) and no T3 thread it could
  still wake ("A thread that is idle" in [handoff](handoff.md)) exits after 10 minutes. Any daemon
  that stops deletes its presence, so other devices drop it at their next sync rather than when it
  goes stale.

## The local connection

Sessions reach the daemon over loopback TCP, on a random port written with a random token to
`.git/murmur/daemon.json`. The token lets its holder name the program the daemon runs to hand a
message to T3, so reading the file is running code as the user.

- **The state directory is private.** `.git/murmur` is made 0700, and on Windows, where modes do
  nothing, each daemon start gives it an access list of its own that lets in only the user, SYSTEM
  and Administrators, and rewrites the files already in it (2026-11-09). Before that, a clone
  outside the user's profile left it readable by every user. A network drive, or a volume with no
  access lists, is left as it is: a share checks access as the account its server knows, and a list
  naming the local user could shut them out for good. So is a directory an elevated run made, which
  a later run may not be allowed to change; that is only logged.
- **daemon.json counts only while the clone's lock is held** (2026-11-09). A daemon that dies
  without removing the file (a crash, SIGKILL, a power cut) leaves an address another user's process
  could listen on, to be sent the sessions' hellos and requests and to answer with messages of its
  own. A daemon takes the lock before it writes the file, removes the file as it begins to stop,
  before it gives up its port, and lets the lock go only when it has gone. So a daemon.json beside a
  free lock is stale and removed. Clients take the lock only shared and only a daemon takes it
  exclusively, so a lock a client finds held is a daemon's; a timed retry left a window where
  another client's cleanup looked like a daemon.
- **Every connection proves the daemon** (#371, from Sol's review before v0.3.0; protocol 12,
  2026-11-10). A daemon that died after the lock was checked left its port to whoever took it, and
  that listener got the next request, the token and any outgoing message with it, and could answer
  with an inbox of its own. Now each connection starts with a ping that carries a fresh nonce and no
  token, and the daemon answers with an HMAC of its address and the nonce, keyed by its token. Only
  then does the request go, on the same connection, so nothing can take the port between the proof
  and the request; a separate ping first would have left that gap. What answers without the proof is
  not the daemon. Until it is proved, a reply is read only up to 4 KB, and after up to 64 MB, about
  half again a full inbox of the longest messages; before, one that never ended was read until the
  deadline.
- **Daemons from before the proof** (protocol 11 or older) can't prove themselves, so a client sends
  them nothing at all, not even a stop, and says how to end one. That is the cost of an upgrade from
  0.2, which never updates itself. `mur stop`, `mur join`, a leave and `mur uninstall` end it by its
  process instead (#394, 2026-11-11), once the clone's lock is held and only if the process
  daemon.json names is alive and runs mur, so a reused process ID is never ended; an agent's session
  never ends a process on its own. Everything that talked to the old daemon failed review first
  (Sol's reviews before v0.3.0-rc.1 and of #389): even a stop could hand the token, which is also
  the proof's key, to a process that had taken the port while the old daemon finished.
- **It answers before its first sync** (2026-11-10, #372). That sync may take its whole 10 s, and
  with Windows' process start a new binary's first start on Tyler's laptop took 21 s, past the 15 s
  a starter waits. A registration, `who`, `send` and `lookup` still wait for the sync, at most 10 s,
  so what they see includes the backlog.

- **A send doesn't wait out a slow sync** (#738, 2026-11-16). After its push lands, a write to this
  device's log queues its own event for the sessions on this device, which needs the lock a sync
  holds through its fetches. One slow ref stretched that to most of a minute: a send that had
  reached the remote answered its agent that much later, sometimes past the client's deadline, so
  the agent took it for failed, and every other write to the log waited behind it. The write now
  waits 2 s, longer than a sync takes when nothing is slow, then leaves the queueing to that sync
  and answers. The cost: during a slow sync, what this device's own writes do here waits for the
  sync's end. A message to a session on the same device reaches its inbox then, not when the send
  answers, and a session just added to a group isn't yet counted in the note a send to that group
  gets.

## Protocol numbers

A manual upgrade replaces the binary and not the daemon already started from it, which would go on
answering ping and refuse whatever came after the version it was built from (the route request and
handing a message over as a turn, #57, were the first). So the daemon reports a protocol number in
its ping reply (`proto.Protocol`; a daemon from before the number reports none, read as 0), and a
client that may start a daemon (`mur join`, `mur ui`, an MCP server registering a session) stops one
with a lower number and starts its own. Only an older daemon is replaced, never a newer one, so two
binaries of different ages on one machine don't stop each other's daemons for ever, and a number is
used rather than the version so that builds from a branch, which all say "dev", are told apart. The
old daemon lets its background work finish for up to 20 s and then deletes its presence for up to
15, holding the clone's lock throughout, so the client waits 40 s for the lock (`proto.ShutdownMax`;
15 s until #83), then fails with an error that names the old daemon's pid. A session whose daemon
refuses its route (an older one that couldn't be replaced) works, with its messages as notices, and
its MCP server logs why once. The number goes up with any change to a request or a reply that an
older daemon can't serve.

## The log

`.git/murmur/daemon.log` is the daemon's only record of what it did, so it is what a teammate is
asked to send when their device misbehaves, maybe to someone off the team. So it can be shared: it
names events by type, ID and address, never by what a message says, and each line passes through
`internal/logfile`, which takes the user and password, the query and the fragment out of any URL,
and cuts a line at 64 KB. It does name the clone's paths. Past 4 MB it moves to `daemon.log.old`, at
a start and while running (copied and emptied, since Windows can't rename an open file). Until
2026-11-09 it moved only at a start, so a daemon kept up for days by an open page grew it without
limit.
