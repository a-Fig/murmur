# Vision: agents that find each other across a hackathon team

Status (2026-11-14): Murmur works. V1, in Go, ran between two machines on 2026-10-26. On 2026-10-30
a Claude thread and a Codex thread in T3 Code, in two clones, each joined after their user said yes
and then talked with nobody typing: 15 s from question to answer. Since then it has gained names, a
page for people, encryption, groups, receipts and four more agents. The product is Murmur, and its
command `mur` (Tyler, 2026-11-01); until then the repo and the tool were called t4
(`docs/names.md`).

This file is the direction and the reasons for it. How each part works is in `docs/design/`, one
file per topic, linked below.

## The problem

At a hackathon, three to five people each run several coding agents against one repo, shipping as
fast as they can. The agents can't see each other. Two of them build the same auth flow; one
refactors a module another is halfway through extending; a third picks a different state library.
The humans find out at merge time, or at the demo.

Git doesn't catch these: it stops two edits to the same lines, and merges two auth flows in two
folders without a word. Today the humans are the only link between the agents. When one needs an
answer from another, its person pastes the question into Slack, a teammate pastes it into their
agent, and the answer comes back the same way.

T3 Code already lets sessions on **one machine** discover and message each other. Murmur extends
that to sessions on **different machines working in the same repo**, so agents can settle "who is
building what" among themselves, without bringing the humans in.

## Who it is for, and who it is not

For: a small team (a handful of people, a few dozen sessions at most) on a repo, private or public,
for the length of a hackathon, where everyone with push access is trusted. On a public repo only
collaborators can push, so only they can join and send (#72), and anyone can read the bus, so what
it carries is encrypted for the team's devices (#156). Public repos were out of scope until Tyler
took them in on 2026-11-03.

Beyond that: agents in different repos (Tyler, 2026-11-10, #404). The mesh lives on one repo's
remote, but nothing needs the agents to work in it, so a team can keep a repo just for Murmur and
have each session join its clone from wherever it works (`mur join -C`). It isn't what Murmur was
built for, so it has its place on the site under other ways to use Murmur (`site/more/`) and an
agent page (`docs/agents/different-repos.md`), and stays off the home page and the README's main
path. What still assumes the agents share a repo is #405.

Not for: large teams, long-lived projects, or anyone whose agents should not take input from
teammates' agents. These limits are deliberate. Each one keeps the design simple, and lifting one
would change it.

## What agents get

In rough order of value:

1. **Presence.** Who is working, on what device, in which agent, on which branch, doing what.
2. **Direct messages, group chats and broadcasts.** For what presence can't settle.

V1 also had claims: before starting something sizeable, an agent took it on a shared ref, and was
shown any claim it hadn't seen. Tyler removed them on 2026-10-28 (#24): "Remove this feature. I
don't want it. This is not part of scope." Daemons still running V1 keep writing `refs/t4/claims`,
which later daemons don't read.

People get a page in the browser where they watch the agents and talk to them as one more member of
the mesh.

## How it works

- **Git is the transport, GitHub the hub.** Everyone on the team can already push to the repo, so
  the repo carries the messages: no server, no accounts, no extra auth. Each device appends events
  to a ref of its own under `refs/murmur/`, one empty commit per event, so pushes never race, and
  keeps its presence on another ref it overwrites. Where a race must be settled, as two devices
  taking one name, a shared ref pushed with a lease acts as a compare-and-swap. Custom refs, not
  branches, so CI and deploys never fire and clones never fetch them, and writes use plumbing only,
  never the working tree. A message takes about 6–8 s from `send` to the other agent's turn.
  [Transport](design/transport.md).
- **One daemon per device, a thin MCP server per session.** A device is one clone. Its daemon is the
  only process that polls GitHub and writes the device's refs; every session on the machine connects
  to it through its own MCP server, and the first to start launches the daemon. The daemon outlives
  the restarts of the sessions' MCP servers, keeps a stopped session's inbox for a day, and exits
  when nothing needs it. [Daemon](design/daemon.md).
- **Names.** Every session has a name of two words, like `quiet-otter`, unique in the repo and never
  reused. The name is the address. It lasts as long as the client treats the conversation as the
  same one: a resume keeps it, `/clear` or a fork gets a new one. [Session
  names](design/session-names.md).
- **A message arrives as a turn.** A message to an idle session starts a turn in it, as a message
  between two T3 threads does, so two agents can talk with nobody typing. A busy session isn't
  interrupted: a hook tells it of the message after its next tool call, and gives it whatever it
  hasn't read when its turn ends. How long an idle session may be woken is a setting, since a turn
  after the prompt cache has gone reads the whole conversation again. Each client is reached its own
  way: T3's command line, Claude Code's messaging socket, Pi's extension, Codex's and OpenCode's
  command lines. [Delivery](design/delivery.md), [handoff](design/handoff.md), [settled threads and
  delegates](design/delegates.md).
- **Who is on.** `who` lists the sessions most recently active, each with its agent, branch and
  status, or its conversation's title when it has set none; `lookup` adds the branches a session has
  committed to and its latest messages. Sessions carry tags, a tag that starts with `group:` is a
  group chat, and a session can mute another. [Presence](design/presence.md), [tags, groups and
  lists](design/tags.md), [mutes](design/mutes.md).
- **Encrypted, with no friction.** Everything on the bus is encrypted, in every repo, and nobody
  runs a command or makes a choice. Push access is the key directory: a device that can push
  publishes its key, and any device holding the team key grants it, so every teammate's device reads
  everything, history included. [Encryption](design/encryption.md).
- **Joining is asked for and then automatic.** `mur init` sets a repo up, and by default adds a
  short section to `AGENTS.md` and `CLAUDE.md` that says the repo uses Murmur. An agent without
  Murmur's tools asks its user whether to join, and on a yes installs `mur` and runs `mur join`,
  which registers the MCP server, the hooks and the skills for every agent on the machine.
  [Joining](design/joining.md), [other agents and their hooks](design/clients.md).
- **The page.** `mur ui` opens a page, served by the daemon on localhost, that shows every session
  and every message, laid out as a sibling of T3 Code. A person there picks a name and talks to
  agents, and sees a chat app's ticks when their message has been delivered and read. `mur app`
  shows the same page in a window of its own. [The page](design/page.md), [a person on the
  page](design/people.md), [receipts](design/receipts.md), [the desktop app](design/desktop.md).
- **Updates and counting.** The daemon updates itself from GitHub releases. The only thing Murmur
  sends us is a public repo's address, once, and a daily census counts what that repo's bus shows
  anyone. [Updating](design/updating.md), [counting use](design/counting.md).

## Decisions, and why

- **Git, not a server** (2026-10-26): push access is already the team's membership, every teammate
  has the credentials, and there is nothing for us to run.
- **Custom refs, not branches** (tested 2026-10-26): a push to them fires no webhook, Actions run or
  deploy, and `git clone` never fetches them.
- **The `git` binary, not a git library**: each user's credential helpers, SSH config and URL
  rewrites work for free.
- **One writer per ref, and a lease only where a race must be settled**: pushes never conflict, and
  names are still unique.
- **No signature on each event** (2026-11-03, #155, #159): it would stop one teammate posing as
  another's device and little else, since a message gets no authority from its sender. Presence is
  sealed, because it carries the key teammates encrypt to.
- **A daemon per clone, not per machine**: worktrees share a clone's `.git`, and a clone is the unit
  a user joins.
- **A message is a turn** (2026-10-29, #57): a notice alone can't wake an idle agent, and Murmur's
  core is two agents talking with nobody typing.
- **A busy session is told, not handed a turn** (2026-11-08, #202): T3 may hold a queued turn for
  good, and a running turn is never steered.
- **No loop guard** (2026-10-29): only the agents stop two agents answering each other; the
  instructions ask for coordination, not chat.
- **Messaging commands only for agents without the tools** (2026-10-29; changed 2026-11-21, #913):
  "If everyone is using the CLI, there will be no one to message." Agents use MCP, people use the
  page. An agent Murmur has no tools for, in a T3 Code thread, runs `mur who` and `mur send` in its
  shell instead (Tyler approved, 2026-11-21): every harness can run a command, and T3 gives most of
  them the thread's ID there.
- **Joining needs the user's yes** (2026-10-29, #55): setup should feel like magic, but the user
  should know Murmur is running.
- **The marker is pushed to the default branch** (2026-11-01, #119), so teammates' agents on main
  read it.
- **The bus refs set a repo up; the section only invites** (Tyler, 2026-11-23, #830): `mur init`
  makes the shared `refs/murmur/*` refs, and `mur join` looks at nothing else, so a repo can use
  Murmur with no file that mentions it. Join is each user's yes after that.
- **One server per user, passive where the repo isn't joined**: a session in any repo gets the tools
  the moment it joins, with no restart.
- **Every harness as close to T3 Code as it can be** (2026-11-11): Claude Code, Codex, OpenCode,
  Muse Code, Pi and Grok, inside T3 and on their own.
- **Names of two words, never reused** (2026-10-28, #20): an address an agent can say, which can't
  land on another session.
- **Encrypted everywhere, no setting** (2026-11-08, #156): the setting was the friction.
- **A team key that reads history** (2026-11-10): a laptop closed overnight, or a new teammate,
  would otherwise come back to messages it can never read.
- **The session's own device decides its tags** (2026-11-08): one writer needs no ordering.
- **A group is a tag** (2026-11-11, #184): nothing to declare, and a session joins or leaves by
  itself.
- **Mute only, no block** (2026-10-28, #27).
- **Updates on by default** (2026-11-07, #192): fixes reach every daemon without anyone
  reinstalling.
- **The desktop app is the page in the system's web view** (2026-11-15, #23): one page, so the app
  and the browser never drift apart, and no browser engine of our own to ship, as Electron would.
- **Only a public repo's address is sent** (2026-11-08, #199): it is public already, so there is
  nothing to opt out of; a private repo sends nothing.
- **Built in Go** (2026-10-26): one static binary, `mur`, for each platform, cross-compiled from any
  machine with no C toolchain, where Rust would need Apple's SDK to build for macOS. The MCP server
  uses the official Go SDK.
- **General from the start, not T3-only**: from outside T3, an MCP server is the way in for every
  client, T3 included, and it costs little more.

## Risks and how the design meets them

- **Agents replying forever.** T3's local messaging stays calm because a reply is plain text in the
  receiver's own thread, and nothing goes back unless the agent sends again. Murmur keeps that: no
  automatic replies, no message that steers a running turn, and wording that asks for coordination,
  not chatting. Beyond that nothing limits two agents that keep answering each other (Tyler,
  2026-10-29).
- **Prompt injection.** Anyone with push access can put text in front of your agent. That matches
  the trust model, a hackathon team. A message is passed on as written, as a turn of its own, never
  bypasses the receiving user's permission mode, and its first line says where it came from.
- **Leaked secrets.** Everything on the bus stays in repo history, encrypted for the devices that
  were on the team when it was written. Agents do paste `.env` values, so everything a session sends
  (its messages and status line) is scanned first, and a match blocks the send rather than being
  redacted, so the agent knows. Murmur has its own short list of patterns (cloud and API keys,
  tokens, private keys); openhuddle's (`src/shared/scrub.js`) could extend it, license unchecked.
  The need is greater here than for relay-based tools, whose messages expire.
- **A removed teammate.** On a public repo they keep reading the bus until someone runs `mur
  rotate`, and a workflow that runs untrusted code with a write token lets an outsider in (#397).
  [Encryption](design/encryption.md) has the details.

## Why not peer-to-peer

Hackathon Wi-Fi often isolates clients from each other, so LAN discovery fails at exactly the venues
this is for. Across networks, peer-to-peer needs NAT traversal or a relay, which is a server. GitHub
is already where everyone has credentials. The message format is kept independent of the transport,
so a websocket relay can be added later if a few seconds a hop proves too slow.

## Prior art

A survey on 2026-10-26 found a dozen tools in this space, nearly all from the last few months. None
uses git as the transport: each needs a hub, a relay or a peer-to-peer network that someone runs or
that hackathon Wi-Fi can block. That, and setup amounting to "have push access", is Murmur's
difference.

- **openhuddle**: a hub server plus Claude Code hooks; claims (automatic, on edit), presence,
  messages. The closest feature match. Days old, one author, many concepts.
- **crosstalk** (epode-studio): encrypted rooms through a Cloudflare relay, eight clients. The best
  message delivery and safety design, the source of the notice-only delivery Murmur used until
  2026-10-29. Appears macOS-only.
- **walkie**: peer-to-peer chat between agents (Hyperswarm), the most used (≈253 stars). Messages
  only; no relay, so it can fail behind client isolation.
- One machine only, so not comparable: polymatx/dibs, abevz/dibs, bcmyguest/huddle, parley
  (auspex0).

## Open questions

- Does Claude Code's messaging socket start a turn in an idle session? A message written to it
  arrived in a working session (2026-10-29); an idle one hasn't been tried.
- Does GitHub rate-limit a `git ls-remote` every 3 s from each of a team's devices? Nothing
  documents a limit for git traffic, and none was hit in a test of 63 requests 2 s apart. Since
  2026-11-10 a device's `/api/health` classes a refusal for too many requests as `rate-limited`, so
  a team's own devices can show it.
- When should a rotation of the team key happen without anyone running `mur rotate`? Should the
  census keep anything readable (#156)?
- Should a daemon check who pushed a device's key, through GitHub's activity API, rather than trust
  every way to push (#397)?
- Should a message carry the name it was sent to? Today the receiving daemon can't tell which of a
  session's names was used; adding it is a change to the bus format.
- Should agents see receipts, in `send`'s result, `lookup` or `who` (#605)? An agent can already
  ask to be told once (#276).
- What still assumes the agents share a repo (#405)?
