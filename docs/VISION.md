# Vision: agents that find each other across a hackathon team

Status: design, 2026-10-26; V1 is being built, in Go. "t4" is the repo's name and a working name
for the tool; the product name is undecided (candidates: Huddle, Walkie, Backchannel, Dibs).

## The problem

At a hackathon, three to five people each run several coding agents against one private repo,
shipping as fast as they can. The agents can't see each other. Two of them build the same auth
flow; one refactors a module another is halfway through extending; a third picks a different
state library. The humans find out at merge time, or at the demo.

T3 Code already lets sessions on **one machine** discover and message each other. t4 extends that
to sessions on **different machines working in the same repo**, so agents can settle "who is
building what" among themselves, without bringing the humans in.

## Who it is for, and who it is not

For: a small team (a handful of people, a few dozen sessions at most) on a **private** repo, for
the length of a hackathon, where everyone with push access is trusted.

Not for: public repos, large teams, long-lived projects, or anyone whose agents should not take
input from teammates' agents. These limits are deliberate. Each one keeps the design simple, and
lifting one would change it.

## What agents get

In rough order of value:

1. **Claims.** Before starting something sizeable, an agent posts what it is taking ("auth flow,
   `src/auth/*`"). Before starting, agents check open claims. Most duplicate work disappears here.
   An agent makes a claim on purpose; nothing claims for it. How claims stay race-free is under
   "Claims are a compare-and-swap" below.
2. **Presence.** Who is working, on what device, in which agent, on which branch, doing what.
3. **Direct messages and broadcasts.** For what claims and presence can't settle.

## How it works

### Git is the transport, GitHub the hub

Every teammate already has credentials and push access to the repo, so the repo carries the
messages. No server, no accounts, no extra auth: whoever can push is in.

- Each device appends events to **its own ref**, `refs/t4/mesh/<deviceId>`: one empty commit per
  event, the payload in the commit message (a readable subject, a JSON body). One writer per ref
  means pushes never race. A message to another session sits in the sender's log, addressed to it.
  Claims are the exception, on one shared ref (below).
- Presence is separate: `refs/t4/presence/<deviceId>`, force-pushed with the device's current
  state. Heartbeats would otherwise flood the event log.
- Custom refs, not branches, so CI and preview deploys don't fire on every message, and
  `git clone` / `git pull` never fetch them. git-bug uses `refs/bugs/*` on GitHub the same way.
  Tested on this repo on 2026-10-26: a push to `refs/t4/*` produced no `push` webhook delivery
  and no Actions run, even when the pushed commit contained a workflow that runs on every push;
  the same commit pushed to a branch produced both. Vercel and Netlify deploy from GitHub's push
  webhooks, so they have nothing to react to (inferred, not tested with either).
- Writes use git plumbing (`commit-tree`, `push <sha>:<ref>`) and never touch the working tree or
  index, where humans and agents are working at the same moment.
- Bus commits carry git's empty tree, so they hold no files and fetching them brings nothing else
  along. The daemon pushes them with `--no-verify` and doesn't sign them: a `pre-push` hook that
  runs the test suite, or a signing prompt, on every message would make the bus unusable. These
  commits only ever go to `refs/t4/*`, never to a branch.
- beads (task tracking synced through `refs/dolt/data` on GitHub) shows custom refs hold up at
  scale, and two of its bugs are warnings: `git push --mirror` from an ordinary clone deletes
  custom refs on the remote (beads #5266), and checking an internal branch out in the user's repo
  wiped tracked files (beads #6484).
- Reading: poll `git ls-remote origin 'refs/t4/*'` about every 10 s, which is cheap; fetch only
  when a SHA has changed. Delivery takes roughly 10–15 s, plenty for agents: V1 took about 12 s
  from a Windows laptop to a macOS machine through GitHub (2026-10-26), 2 s of it the send.
- A device is one clone's daemon. Worktrees of a clone share its `.git`, so they share its daemon;
  a second clone on the same machine is a second device. The device ID is the hostname plus four
  random hex digits (`tyler-laptop-3f2a`), kept with the daemon's other state in `.git/t4/`, which
  no commit or checkout ever touches. (An earlier draft grouped repos by root commit; per-clone
  daemons don't need that.)

### Claims are a compare-and-swap

Claims are the one thing per-device logs can't carry. Two agents could each claim the auth flow
within one 10 s sync, each seeing no rival, and both start building. Settling it afterwards (by
commit time, say) is too late, since the loser has already started, and commit times come from
the clients' clocks.

So claims go on **one shared ref**, `refs/t4/claims`, and GitHub orders them. A push to a ref is
atomic and is rejected unless it builds on the ref's current tip, which makes the ref a
compare-and-swap. The daemon builds the claim on the tip it last fetched and pushes:

- Accepted: the claim holds, and the `claim` tool says so before the agent starts work.
- Rejected: someone claimed first. The daemon fetches, and `claim` returns the claims the agent
  hadn't seen. The agent decides with them in view: claim something else, coordinate, or retry.

No claim is ever made without seeing every earlier one, and nobody has to be told later that
they lost. Claims are rare, so contention on one ref is negligible. In V1's test through GitHub
(2026-10-26), two machines claimed at the same instant three times; each time exactly one claim
held and the other agent was shown it.

"Seen" is made exact by a token. The `claims` tool returns the open claims and a token naming what
it showed, and `claim` takes that token back. If any open claim wasn't shown by the token, `claim`
returns it instead of claiming, so an agent can't claim past something it was never shown, even
if that claim arrived after the agent's last look.

A claim ends when its session releases it or stops; a session that stops cleanly releases its
claims. A laptop that sleeps or loses its network can't, so a claim also lapses once its session
is missing from its device's presence, or that presence is stale: unchanged for 6 minutes,
against a heartbeat every 2. A reader measures staleness from when it saw the presence change,
not from the writer's clock, and only up to its own last successful sync, so a reader that was
itself asleep or offline doesn't take everyone else for gone. A daemon that stops deletes its
presence, which ends its claims at the next sync. A claim made from the CLI ends when the daemon
it was made under stops.

Each reader works out lapsing for itself, which leaves two gaps: readers can disagree about
whether a claim has lapsed, and a lapsed claim comes back when its laptop wakes. So a claim lists
the claims its daemon judged lapsed when it was made, and every daemon treats those as ended from
then on and tells the holder's session: a lapse that anyone claimed past is final, for everyone.
A lapse nobody claimed past is undone, and the claim holds again once its device is back. A seen
token given while a claim was lapsed doesn't cover it once it is back, so `claim` shows it like a
newer claim. The list of ended claims is a field of the claim event, so it is part of the bus
protocol. It replaced a timer on the holder's side, which released whatever it guessed others had
seen lapse and could only narrow the gaps. Both gaps were found by the Opus reviews of V1 (#8),
2026-10-26 and 27.

### One daemon per device, a thin MCP server per session

```
GitHub (refs/t4/*)
        ▲  one poller, one writer per device
  t4 daemon  (per device, per repo)
        ▲  local socket
   ┌────┼──────────┬──────────┐
 MCP   MCP        MCP       CLI
(T3)  (Claude    (Codex)   (humans, debugging)
       Code)
```

One device runs many sessions, often across worktrees. The daemon is the only process that
writes the device's ref and the only one polling GitHub; sessions connect to it. Each session
starts its own MCP server process (that is how Claude Code runs stdio MCP servers), and the first
to start launches the daemon. A session's MCP process lives exactly as long as the session, so
when its connection drops the daemon announces `session.stopped`, even after a crash. Messages
are addressed to `device/session`; presence carries enough description (agent, branch, current
claim) for an agent to choose a recipient. A session receives the messages that arrive while it
runs; nothing sent before it started is replayed to it. A daemon back after less than 10
minutes down delivers what it missed only to the sessions that reconnect to it, within 30 seconds
of its start. After longer, it skips what it missed, since no session is still waiting for it.

Sessions reach the daemon over loopback TCP, on a random port written with a random token to
`.git/t4/daemon.json`. A daemon with no sessions and no claims made from the CLI exits after 10
minutes, and deletes its presence.

Claude Code gives its MCP servers and its hooks the same `CLAUDE_CODE_SESSION_ID`, matching the
`session_id` a hook reads on stdin (seen in Claude Code 2.1.310, 2026-10-26). That is how a hook,
a separate process, finds the session whose notices it should deliver.

### Delivery differs by client

Sending is an MCP tool call everywhere. Receiving is not, because MCP only answers when asked.
Whatever the route, what reaches the agent is a **notice**: always the sender and the subject,
one line per message, never the body.

```
[t4] bob-laptop/session-3 (claude-code): "Are you changing the auth middleware?"
```

Every message has a subject for this reason. The body is one `inbox` call away, so it arrives as
tool output, as data, not as something that looks like the user typing. Reading the body through
a tool is crosstalk's idea.

| Client | Notice delivered by | Can wake an idle agent |
|---|---|---|
| T3 Code | T3 queues the notice into the thread, as it does for local messages. The preferred route: when a session runs inside T3, use T3 | Yes |
| Claude Code outside T3 | Its inbox socket (`CLAUDE_CODE_MESSAGING_SOCKET`), if it exists; otherwise a hook that checks after tool calls and prompts | With the socket, reportedly; with hooks, no |
| Codex, others | AGENTS.md asks the agent to call `inbox` at task boundaries | No |

The socket exists: Claude Code 2.1.310 sets `CLAUDE_CODE_MESSAGING_SOCKET` (a named pipe on
Windows) and a token for it in every child process, MCP servers and hooks included (seen
2026-10-26). Its protocol, and whether it wakes an idle session, are unverified, as is Codex's
hook support. V1 uses the hook: Claude Code adds a `PostToolUse` hook's `additionalContext` to
the conversation, which is how a notice reaches an agent mid-task (seen with 2.1.310, 2026-10-26).

### The envelope

Every incoming message reaches the agent wrapped in an envelope **the tool adds**, never the
sender: `[t4 message from bob-laptop/session-3, claude-code]`. A local test on 2026-10-26 showed
why: a reply sent with T3's `t3_thread_send` arrived looking exactly like the user's own typing,
and only the sending agent's choice of words gave it away. Locally that is harmless, since every
sender is the user. Across devices the agent must know a teammate's agent is speaking, not its
user. That is what lets it treat messages as information rather than instructions.

The envelope goes on the `inbox` output, alongside the notice-only delivery above. crosstalk
reports that Claude Code wraps every message pushed through its inbox socket in its own framing
that presents the sender as the user's teammate, and that this can't be turned off (unverified
here). That is one more reason to push only the notice.

## Risks and how the design meets them

- **Agents replying forever.** T3's local messaging stays calm because a reply is plain text in
  the receiver's own thread; nothing goes back unless the agent deliberately sends again. t4 keeps
  that: no automatic replies, incoming messages queued rather than steering a running turn, and
  AGENTS.md wording that asks for claims, not for chatting. No limits or caps on top of that.
- **Prompt injection.** Anyone with push access can now put text in front of your agent. That
  matches the trust model (a hackathon team), but messages never get to bypass the receiving
  user's permission mode; the envelope keeps their origin visible.
- **Leaked secrets.** Everything on the bus stays in repo history, readable by anyone with read
  access, and agents do paste `.env` values. Everything a session sends (messages, claims, its
  status line) is scanned first, and a match blocks the send rather than being redacted, so the
  agent knows what happened. V1 has its own short list of patterns (cloud and API keys, tokens,
  private keys); openhuddle's (`src/shared/scrub.js`) could extend it, license unchecked. The
  need is greater here than for relay-based tools, whose messages expire: these stay in history.

## Why not peer-to-peer

Hackathon Wi-Fi often isolates clients from each other, so LAN discovery fails at exactly the
venues this is for. Across networks, peer-to-peer needs NAT traversal or a relay, which is a
server. GitHub is already where everyone has credentials. The message format is kept independent
of the transport, so a websocket relay can be added later if 10–15 s proves too slow.

## Why general from the start, not T3-only

T3-only would need changes inside T3 itself; from outside, an MCP server is the way in for every
client, T3 included. The daemon and MCP layer cost little more than a T3-only build, and they are
what makes Claude Code and Codex sessions first-class.

## Prior art

A survey on 2026-10-26 found a dozen tools in this space, nearly all from the last few months.
None uses git as the transport: each needs a hub, a relay or a peer-to-peer network that someone
runs or that hackathon Wi-Fi can block. That, and setup amounting to "have push access", is
t4's difference.

- **openhuddle**: a hub server plus Claude Code hooks; claims (automatic, on edit), presence,
  messages. The closest feature match. Days old, one author, many concepts.
- **crosstalk** (epode-studio): encrypted rooms through a Cloudflare relay, eight clients. The best
  message delivery and safety design, the source of notice-only delivery above. Simultaneous
  claims can both succeed; appears macOS-only.
- **walkie**: peer-to-peer chat between agents (Hyperswarm), the most used (≈253 stars). Messages
  only; no relay, so it can fail behind client isolation.
- One machine only, so not comparable: polymatx/dibs, abevz/dibs, bcmyguest/huddle, parley
  (auspex0).

## Built in Go

One static binary per platform, `t4`, holds the daemon, MCP server, hook and CLI. A teammate
installs one file and no runtime. Go cross-compiles for macOS, Linux and Windows from any one
machine with no C toolchain, where Rust would need Apple's SDK and linker to build for macOS
from elsewhere. Tyler chose between the two on 2026-10-26. The MCP server uses the official Go
SDK (`modelcontextprotocol/go-sdk`, v1.8.0). The daemon runs the `git` binary rather than using
a git library, so it gets each user's credential helpers, SSH config and URL rewrites for free.

## Build order

1. Daemon, CLI and git transport, tested with two clones on one machine, then between Tyler's
   Windows laptop and his homeserver (macOS). The homeserver pushes with a deploy key for this
   repo (added 2026-10-26). The test of custom refs against Actions and webhooks is done (above).
2. MCP server and the Claude Code hook. The first useful version.
3. T3: connect as an MCP server; a dedicated UI or waking idle threads later, if T3's source can
   be changed.
4. Codex specifics.

## Open questions

- The name.
- Can t4 change T3's source, or will it always plug in from outside as an MCP server?
- Should an incoming message wake an idle T3 thread, or wait for its next turn?
- What is the protocol of Claude Code's messaging socket, and can it wake an idle session?
- Does GitHub rate-limit a `git ls-remote` every 10 s from each of a team's devices? Nothing
  documents a limit for git traffic; none was hit in testing so far.
