# Delivering a message as a turn

Sending is an MCP tool call everywhere. Receiving is not, because MCP only answers when asked. Until
2026-10-29 a message reached an agent only as a one-line notice that a hook added after one of the
agent's own tool calls, which can't wake an idle agent and leaves a teammate's question unanswered
until the agent happens to call a tool. Tyler's decision (2026-10-29, #57): **a message from another
agent reaches the receiving session as a turn of its own**, the way a message between two T3 threads
does. Murmur's core is two agents talking with nobody typing. How each client is handed a message is
in [handoff](handoff.md).

## Delivered and read

Tyler's rule (2026-11-09, #325, replacing #187's of 2026-11-07): a message is *delivered* once its
recipient knows it is waiting, and *read* once the recipient has the message itself, the same words
for an agent and a person. These are what a sender acts on, so neither may be said early: a looser
word would let a sender believe an agent knows something it hasn't seen, and may not see for hours
if nothing wakes it (#25). #187 used *delivered* for what is now *read*. In the order a message
meets them:

- *Queued:* the recipient's daemon has put it in the session's inbox.
- *Handed over:* the daemon has passed it to the session's client (`t3 thread send`, the messaging
  socket, or the client's command line), and it is out of the daemon's inbox. Still queued, in the
  client now. It is read once the turn starts, where the hook's prompt shows it (#611,
  [receipts](receipts.md)).
- *Delivered:* for an agent, the hook's notice (sender and subject) is in its context. For a person,
  it is in their inbox on the page, counted as unread.
- *Read:* for an agent, the body is in its context. For a person, they opened the conversation (a
  hidden page marks nothing).

A message that arrives whole (a turn, the hook's whole message, the `Stop` hook's continuation) is
read then, and skips delivered if no notice told of it first. One a notice told of during a turn is
read later, and the turn that gives it whole says a notice told of it, so the agent doesn't take it
for a second message (#355).

| Route | Delivered or read when | Can Murmur see it |
|---|---|---|
| `t3 thread send` | read when the turn carrying it starts | No: the daemon sees `t3` accept the message, not the turn start. T3's database would show it, but those tables are T3's own and may change |
| The messaging socket (Claude Code, and Pi's extension, outside T3) | read when the client takes it as a turn | No: the socket answers nothing |
| The client's command line (Codex and OpenCode 2 outside T3) | read when the turn carrying it starts | No: the daemon sees the command accept it |
| The hook's notice | delivered when the notice is added, after the agent's next tool call or at its next prompt; read then if the busy setting says whole message, or the message has no body, since its notice would show all of it (it came again as a turn until 2026-11-09, #355) | Partly: the daemon sees the hook fetch the notice, not the hook's output reach the agent |
| `inbox` | read when the tool's result returns to the agent | Partly: the daemon sees the messages leave in its answer |

[Receipts](receipts.md) report these states back to the sender.

## Handing over

The daemon hands a message over at once when it arrives for a session it can reach, unless the hook
has the session at work (below).

| Where the session runs | Handed over by | Starts a turn in an idle session |
|---|---|---|
| T3 Code: Claude Code, Codex, OpenCode or Muse (and Pi, Grok and Antigravity, not tried) | `t3 thread send <thread> -`, the text on standard input. T3 starts the thread's turn if it is idle and queues the message as its next turn if busy, interrupting nothing | Yes (seen 2026-10-29: Claude Haiku to Claude Haiku, and Claude Haiku to Codex gpt-6.1-sol, in two real T3 threads, nobody typing) |
| Claude Code outside T3 | the session's messaging socket | Unverified: a message written to the socket arrived as a turn while the session was working (2026-10-29, Claude Code 2.1.312, Windows); an idle session hasn't been tried |
| Pi outside T3 | the socket Murmur's extension opens in the session | Yes (2026-11-09, Pi 1.5.0 in RPC mode with a stand-in model) |
| Codex or OpenCode 2 outside T3 | the client's own command line | Yes (2026-11-11) |
| Muse, Grok or Antigravity outside T3 (Antigravity not tried), Cursor anywhere ([#923](https://github.com/a-Fig/murmur/issues/923), [#924](https://github.com/a-Fig/murmur/issues/924)), or any handoff that fails | the hook's notice where the hook runs, and `inbox` | No |

What the agent reads:

```
Message from quiet-otter (Claude Code, on smash's machine) via Murmur:

> <the subject>

> <the body>

Reply with Murmur's `send` to quiet-otter if a reply is needed.
```

The first and last lines are Murmur's. The first says a teammate's agent is speaking, not the user,
so the agent treats the message as information rather than its user's typing; the last says how to
answer and that answering is optional. The sender's text between them, subject and body, is quoted:
every line behind `> `, as `inbox` quotes it, by the one function (`quote` in `internal/view`) both
use. Anything not behind `> ` is Murmur's, so nothing a sender writes can pass for a header (#297),
a notice or the note below. Until 2026-11-23 only the lines that read as a Murmur header were
quoted (#297, Tyler, 2026-11-15; Fable's three reviews of #710), judged in lower case past spacing,
Markdown marks and invisible characters; a list of what a header may look like leaks somewhere
(a dash or a bracket after "via Murmur", look-alike letters from other scripts), and quoting every
line has no such list (#1070). Teammates' agents are still trusted in Murmur's model, and a message
that reads as a request is meant to be acted on: quoting says whose words they are, not to ignore
them.

When Receive files is off for the agents, a message's files don't reach the session (`files.md`),
and the turn says so after the quote, as Murmur's own lines: `Attached:` with each file's name,
type and size, then that the session can't receive them because receiving files is off in
Murmur's settings. The names are the sender's, so the turn makes each safe again as it prints it
(`bus.FileName`: letters, digits and `. _ -`, at most 60), and the type is the extension's, where
the machine knows it.
A body past the limit is cut, as in `inbox`.

- **Who it is from, as the receiver knows it.** The first line is written when the message is
  queued, from what the receiving daemon then knows of the sender. A sync reads the registry and
  every presence before the logs, so what landed before the message is known; until 2026-11-09 the
  logs came first, and a sync that fetched a sender's name with its message wrote the turn without
  it (#71). Those reads get at most half of what is left of the sync (`ReadFirstLimit`), so the logs
  are always read.
- **Out of the inbox when handed over**, so neither the hook nor `inbox` shows it again; the page
  and the daemon's log keep it as history. A message that couldn't be handed over stays in the
  inbox, and the hook announces it.

## A session at work

#202. Until 2026-11-08 a message for a busy T3 thread went to `t3 thread send`, and T3 queued it
behind the running turn. The agent heard nothing until the turn ended, Murmur couldn't tell whether
T3 ever played the queued turn, and T3 holds such turns rather than starting them after a run the
user stopped, while the provider is usage-limited, and while the thread's mode is above its ceiling
(T3 source at f558007b, read, not observed); a person can also delete one. Tyler's decision
(2026-11-08): where Murmur's hook runs, a busy session isn't handed the message. The hook tells it
after its next tool call (sender, subject and the count unread), and the agent reads the body with
`inbox` when it chooses. What it hasn't read when the turn ends is given to it then, however long
the turn took, so nothing waits for good.

- **At the end of the turn** the `Stop` hook keeps the turn going with those messages: it blocks the
  stop, and Claude Code gives the agent the hook's reason, each message as its turn would read
  (Tyler, 2026-11-08; first built as a new turn). That needs neither T3 nor a socket, and puts
  nothing in T3's queue, which may hold a turn for good. The cost is a longer turn: its user sees it
  go on after what looked like the agent's last word. They are handed over as a turn instead after a
  turn that ends without `Stop` (one the user interrupted in T3), and at a ninth `Stop` in a row
  with no tool call between, since Claude Code ends a turn after eight continuations whatever the
  hook says (its hooks reference, 2026-11-08). Not seen in the world: a session kept going by the
  hook; `decision: block` is Claude Code's documented answer.
- **Busy and idle come from the hook**: a prompt or a tool call means busy, and `Stop` means the
  turn ended. "No tool call lately" wouldn't do, as one long command looks idle. A turn the user
  interrupts fires no `Stop`; in T3 the busy state also ends when a read of T3's database, begun
  after the hook last ran, finds no run of the thread going. Comparing the run's end with the hook's
  last call was dropped in review (Sol, 2026-11-08): a hook from a tool call the interrupt cut short
  would have left the session busy for good. Outside T3 nothing ends an interrupted turn until the
  next prompt and its `Stop`, so a message waits for the user to come back.
- **A turn in T3 starts before the hook sees it** (#607). T3 marks a run queued or running when it
  takes a prompt, and Claude Code fires `UserPromptSubmit` only when it starts the turn. Until
  2026-11-15 the session looked idle in between, and a message was handed to T3, which queued it
  behind the run as a turn of its own: what Tyler saw. T3's database on his laptop showed about 20
  such handoffs from 2026-11-11 to 11-15, all to Claude threads. In all but two the run had been
  going 2 s to 14 min and Claude's turn started 1 s to 3 min after the handoff; four of the runs
  were a message the daemon had just handed over itself. So a session whose hook has no turn going
  is at work when a read of T3's database that began after its turn ended finds a run going, and,
  until a read that began after it, when the daemon has just handed a message to its thread (at
  most a minute, so a read that keeps failing doesn't hold its messages for good). A prompt that
  is the handed turn, seen while the send was still running, holds nothing after (#677,
  2026-11-16): a T3 slow to answer let the turn end first, and the next message waited out the
  minute for a turn already over. Nothing else counts: a tool call or a Stop then may be of an
  earlier turn T3's database had ended, with this one queued behind it. The daemon reads
  the thread again just before each handoff, as the reads every 5 s would let a prompt T3 took
  since the last one through. The same read catches a turn a user's own `Stop` hook kept going
  after Murmur's saw `Stop`, up to 5 s late.
  - A read from before the turn's end, or less than a second after it, doesn't count: it may have
    seen the run `Stop` ended, which T3 closed within 0.4 s of the turn's end in 99% of 1,212
    Claude runs (2026-11-11 to 11-15).
  - The run is dated from when T3 took the prompt, as its database says. What came after counts as
    come during the turn, and is held for its end however long it waited: Claude's turn once began
    27 minutes after T3 took the prompt (2026-11-13).
  - A read that finds no run, after the run was seen or a message handed over, ends it as `Stop`
    would: the run was cancelled, failed, or was the one `Stop` ended.
  - A run no hook has shown going for 15 minutes no longer counts, and messages go to T3 as before:
    T3 can leave a run queued for good (one from 2026-11-03 was still queued on 11-15), and no hook
    would ever tell the session of them.
  - A run counts only while the thread's newest run is queued or running. One T3 left queued
    behind later runs would otherwise start the 15 minutes again at every turn's end. The same
    holds wherever T3's database says a session is at work: a session that reconnected after a
    restart, a turn the hook saw start, and how long a session has been idle (#655). A cancelled
    run isn't the newest: it is a follow-up the user took out of T3's queue, mostly while the run
    before it was still going, and that run is still a turn (#672).
- **Subject or whole message** (#204), the page's busy setting, per device or session (Tyler,
  2026-11-08). "Subject", the default, leaves the agent's focus to the agent, at the cost that it
  may pass over a line it needed; "Whole message" puts each message in front of it mid-task at full
  length, whether or not it matters, and the message is then read.
- **The count** is shown with each new message, and again only when it changes.
- **Held however long.** A message that came during a turn goes in at its end whatever its age: the
  15 minutes a message may wake a session (below) measure an absence, and a turn can be longer. One
  from before the turn is held only if it is still within them.
- **Why not hand it to T3 as well:** T3 can't take back a queued message, so the agent could read it
  with `inbox` mid-turn and then get it again as a turn.
- **Where the hook doesn't run** (OpenCode 1 in T3, as of 2026-11-16, and a Muse session whose user
  hasn't run join since #461; Codex and Grok have it since #225, Pi since #205, OpenCode 2 since
  #431, OpenCode 1 outside T3 since #446, its plugin doing nothing in T3, and Muse since #461),
  T3's database alone says the session is at work (#699): while it
  shows the thread's run going, and from a handoff until a read that began after it, at most a
  minute. The session's messages wait in the inbox meanwhile, where `inbox` finds them mid-turn, and
  when a read finds the run over, everything it has waiting goes in as one turn, each message as its
  own turn would read, up to 256 KB. A session's first hook ends this: from then on it is held as
  any hooked session is, and what waited during the run is held for the turn's end. Until then each message went to `t3 thread send` at once, and T3
  queued it as a turn of its own: on 2026-11-15 four messages to a busy Muse thread were four queued
  turns, and the agent could find none of them with `inbox`, as they had left it. Nothing tells such
  a session of a message mid-turn, so what came during the run goes in at its end whatever its age,
  as T3's queue gave it before; what waited from before the run goes only while young enough. A
  thread whose database has never been read holds nothing, and its messages go to T3 as before. No
  15-minute cap, unlike a run no hook has shown going: no hook will come either way, and a message
  T3 queued behind a run that never ends waits as long as one in the inbox, where `inbox` and the
  page still have it. The one turn is for these sessions only: a hooked session that becomes idle
  gets the rest of what waits by its hook, at its turn's end.
- **Subagents.** A subagent's tool call carries its parent's `session_id`, with `agent_id` set
  (Claude Code 2.1.312, 2026-10-29). The hook tells it nothing, as the notice would land in the
  subagent's context and not the session's (inferred, not seen; #188), and doesn't mark the session
  at work, since a background subagent goes on after its parent's turn has ended.
- **Not covered:** a single tool call that runs a long time gets no notice until it ends. That a
  busy Claude thread in T3 shows the notice mid-turn is read from T3's source (its Claude adapter
  leaves the Agent SDK's `settingSources` unset, so project hooks load), not watched.
- **Considered and not chosen:** a background watcher the agent starts (it has to restart it after
  every message, and Codex isn't told when a background process ends); Claude Code Channels (events
  that come while Claude is busy are held for the next turn, and it is a research preview the Agent
  SDK ignores); steering the running turn with Codex's `turn/steer` or T3's `steer` mode, the
  interruption the 2026-10-29 rule avoids.

## When a handoff fails, and when to wake

- **A failed handoff is left to the hook's notice**, and the log says why, once per session. It is
  tried again only when it plainly never started (no `t3` command to run, no socket to connect to),
  once, after 2 s. Any other failure may have come after T3 took the message: `t3 thread send` has
  no idempotency key (the MCP tool `t3_thread_send` has one, the command line doesn't), and the
  messaging socket answers nothing, so a second call could hand the message over twice, as two
  turns. The cost: a failure that was only a failure (T3 starting up, say) gets a notice and no
  second try.
- **The mark is saved before the handoff** ("attempted", in `state.json`), so a daemon stopped
  mid-call and started again leaves the message to the notice instead of sending the turn a second
  time (#82). A crash between the save and the handoff leaves a notice, not a repeat, the safe way
  round. Looking in T3's database for the message before trying again would keep the second try, but
  relies on tables that are T3's own and may change in any update. A mark that can't be saved (a
  full disk, say) means nothing is sent: the message waits, and every handoff on the device waits
  with it, 2 s after the first such failure, doubling to a minute, until a save works (#952). Until
  then the message is a notice only to a session at work, as for any message still to be handed
  over; past fifteen minutes it is a notice either way.
- **Nothing is handed over for a user who has left** ([joining](joining.md)).
- **Fifteen minutes.** A message that waited more than 15 minutes (T3 closed, the session away)
  isn't sprung on the agent as a turn; it stays a notice. The 15 minutes count from when the daemon
  queues it, so one read after a long outage (#30, 2026-10-30) is young and wakes its thread.
- **The wake setting** (#22, #25), for the device or for one session, decides the rest. "Never"
  keeps the notice. "Always" hands over at once. "Within N minutes", the default with N = 5, hands
  over only to a session that is busy or was last at work within N minutes; one idle longer gets the
  notice, and the message is handed over after all if the session gets to work again while it is
  young. The reason for the limit: a session idle that briefly still has its prompt cache, and a
  turn started after the cache has gone reads the whole conversation again at full price. Until
  2026-11-07 "within" acted as "always", which Tyler found out from the page. For a T3 session, "at
  work" comes from T3's database (`orchestration_v2_projection_runs`, read every 5 s); for any
  other, from its start, its last request or tool call, a turn just handed over and, stopped, when
  it stopped. A daemon that starts hands over what its inboxes kept that is still young enough.
- **The sender is told** (#545, 2026-11-15). On 2026-11-13 three messages to a T3 session idle for
  13 minutes started no turn, as its device's wake setting said, while `send` said "Sent" and
  `lookup` "reachable", so the sender took them for read. Each session's presence now carries
  what its device would do with an agent's message now (`noWake`: `off` for never, `idle` for idle
  past the setting's minutes), and `send` and `lookup` say "won't be woken", and why, on every
  device. A message to a group, a device or everyone names the sessions it won't wake, three at
  most and the rest counted. A person sending to one session is told only of `off`, as their
  message wakes it however long it has been idle; their message to many doesn't (`personAsks`), so
  there they are told of `idle` too.
  - `idle` changes with time, so the daemon looks every 5 s (`TitleEvery`, with the threads) and
    writes presence when a session's answer changes, rather than leaving it to the 2-minute
    heartbeat: about two pushes more each time a session stays idle past its limit, one as it
    crosses it and one as it gets to work again.
  - A session with no way in at all (Muse or Grok outside T3) is judged as any other: under the
    default nothing is said for its first 3 idle minutes, and `idle` after, with that reason, though
    it would not be woken at any time. `who`'s agent column shows it. Not counted: do not disturb,
    which says so itself, and a session of this device that has stopped, which `send` already calls
    stopped.
  - No protocol bump. An older daemon ignores the field and writes none, so a sender is told
    nothing of its sessions, as before.
- **No loop guard.** Nothing in Murmur limits how often two agents answer each other (Tyler's
  decision, 2026-10-29). A message to a busy session never steers its running turn: it is queued as
  the next turn, or announced and given at the turn's end. The AGENTS.md wording asks for
  coordination, not chatting; beyond that only the agents stop a loop.
