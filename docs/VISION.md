# Vision: agents that find each other across a hackathon team

Status: V1 works, in Go (2026-10-26); the next version, with names, a web page and wake-ups, is
being built; the web page, an agent joining a repo by itself, and a message arriving as a turn of
the receiving session are in (2026-10-29). On 2026-10-30 a Claude thread and a Codex thread in T3
Code, in two clones, each joined after their user said yes and then talked with nobody typing: 15 s
from question to answer. The product is Murmur, and its command `mur` (Tyler, 2026-11-01); until
then the repo and the tool were called t4 (`docs/names.md`).

## The problem

At a hackathon, three to five people each run several coding agents against one repo,
shipping as fast as they can. The agents can't see each other. Two of them build the same auth
flow; one refactors a module another is halfway through extending; a third picks a different
state library. The humans find out at merge time, or at the demo.

T3 Code already lets sessions on **one machine** discover and message each other. Murmur extends
that to sessions on **different machines working in the same repo**, so agents can settle "who is
building what" among themselves, without bringing the humans in.

## Who it is for, and who it is not

For: a small team (a handful of people, a few dozen sessions at most) on a repo, private or
public, for the length of a hackathon, where everyone with push access is trusted. On a public repo
only collaborators can push, so only they can join and send (#72), and anyone can read the bus,
so what it carries is encrypted for the team's devices (#156, "The bus is encrypted", below). Public
repos were on the list below until Tyler took them off on 2026-11-03.

Beyond that: agents in different repos (Tyler, 2026-11-10, #404). The mesh
lives on one repo's remote, but nothing needs the agents to work in it, so a team can keep a repo
just for Murmur and have each session join its clone from wherever it works (`mur join -C`; see "A
session that started outside the repo" below). It isn't what Murmur was built for, so it has its
place on the page for uses beyond the original (`site/more/`, "Other ways to use Murmur") and an
agent page (`docs/agents/different-repos.md`), and stays off the
home page and the README's main path. The design keeps it in mind; what still assumes the agents
share a repo is #405.

Not for: large teams, long-lived projects, or anyone whose agents should not take
input from teammates' agents. These limits are deliberate. Each one keeps the design simple, and
lifting one would change it.

## What agents get

In rough order of value:

1. **Presence.** Who is working, on what device, in which agent, on which branch, doing what.
2. **Direct messages and broadcasts.** For what presence can't settle.

V1 also had claims: before starting something sizeable, an agent took it on a shared ref, and was
shown any claim it hadn't seen. Tyler removed them on 2026-10-28 (#24): "Remove this feature. I
don't want it. This is not part of scope." Daemons still running V1 keep writing
`refs/t4/claims`, which later daemons don't read.

## How it works

### Git is the transport, GitHub the hub

Every teammate already has credentials and push access to the repo, so the repo carries the
messages. No server, no accounts, no extra auth: whoever can push is in. (Outside the repo it checks GitHub for updates and, for a public repo only, sends us the repo's
address once; see Updating and Counting use.)

- Each device appends events to **its own ref**, `refs/murmur/mesh/<deviceId>`: one empty commit per
  event, the payload in the commit message (a readable subject, a JSON body). One writer per ref
  means pushes never race. A message to another session sits in the sender's log, addressed to it.
- Per-device logs can't settle a race: two devices could each take the same session name (#20)
  within one sync, each seeing no rival. What must be settled before anyone acts on it needs
  one shared ref, which GitHub orders. A push to a ref is atomic, and with a lease
  (`--force-with-lease`) it is refused unless the ref still holds what the pusher last fetched,
  which makes the ref a compare-and-swap. A refused pusher fetches and decides again with what it
  hadn't seen. V1's claims worked this way; in its test through GitHub (2026-10-26), two machines
  pushed at the same instant three times, and each time exactly one push landed. Session names
  work this way too, on `refs/murmur/names` (see Session names).
- Presence is separate: `refs/murmur/presence/<deviceId>`, force-pushed with the device's current
  state when it changes and on a heartbeat about every 2 minutes. Heartbeats would otherwise flood
  the event log. A presence unchanged for 6 minutes is stale: its device's sessions may not get
  what is sent to them, and `who` and `send` say so.
- A reader measures staleness from when it saw a presence change, on its own monotonic clock, and
  only up to its own last successful sync, so a reader that was itself asleep or offline doesn't
  take everyone else for gone. Only a presence it has never seen change is dated by the writer's
  clock, from when the tip commit was written and never later than now. A daemon that has just
  started sees every device's ref as new, so dating them from now showed devices dead for hours
  as alive for the first 6 minutes, which is when a new session is most likely to ask `who`
  (seen live, 2026-10-30). A writer whose clock runs more than 6 minutes slow looks stale until
  its next heartbeat, at most 2.4 minutes, when the reader sees the presence change and times it
  itself.
- Custom refs, not branches, so CI and preview deploys don't fire on every message, and
  `git clone` / `git pull` never fetch them. git-bug uses `refs/bugs/*` on GitHub the same way.
  Tested on this repo on 2026-10-26: a push to `refs/t4/*` produced no `push` webhook delivery
  and no Actions run, even when the pushed commit contained a workflow that runs on every push;
  the same commit pushed to a branch produced both. Vercel and Netlify deploy from GitHub's push
  webhooks, so they have nothing to react to (inferred, not tested with either).
- Writes use git plumbing (`commit-tree`, `push <sha>:<ref>`) and never touch the working tree or
  index, where humans and agents are working at the same moment.
- A device pushes one ref at a time (#141, 2026-11-01). Its presence, its log and the names
  registry were each pushed from a goroutine of their own, so a session starting set off three
  pushes in the same second (its name, its start on the log, the presence) and one stopping two.
  GitHub refused one of them now and then with `[remote rejected] (failed)`, which is no lease
  refusal, and a refused presence waited 15 s for its retry. In one laptop's log of 2026-11-01 all
  9 refusals (7 presence, 2 names) came within seconds of a session starting or stopping, about one
  such moment in five with the network up, and none came while presence was pushed alone on its
  heartbeat, over several hours. That is inferred from the timing, not reproduced against GitHub; a
  bare repo on disk takes concurrent pushes without complaint. Now a push waits for the one before
  it, so a `send` made while presence is being pushed waits about a second more.
- Pushes from different devices still meet on GitHub, and it refuses them the same way. Two
  devices whose daemons started within a second of each other, so that their heartbeats fell
  together, logged 17 presence pushes refused with `(failed)` in 45 minutes, taking turns, and one
  with `(failure)` (2026-11-09). Two clients pushing to the same repository at the same moment were
  refused 2 times in 20 (`(failed)`, `(fatal error in commit_refs)`), and 4 s apart, never. A
  refused message failed the agent's `send`. Since 2026-11-10 `bus.Push` sends a push refused for
  one of those reasons again, a few times, and the look at the remote ref it takes after any failed
  push keeps a try that landed anyway from being written twice. Heartbeats are spread by 20% either
  way, so devices that started together drift apart.
- Of the pushes a session's start sets off, the presence goes first (2026-11-10). The start on the
  log and the assignment of a name from the pool were ready within milliseconds and took the turn
  first; the presence, which waits 300 ms to gather a session's first changes, went last. Over
  GitHub it began 4.05 s after the session started (median, 2026-11-09), and only the presence
  shows the session in other devices' `who`. Now the other two wait until a presence listing the
  session has been pushed, or has failed. A presence can carry a pool name before its assignment
  lands, which is safe: the name is already set aside for that device, and readers take names from
  presence (see Session names). A session that found the pool empty doesn't hold back the push
  that gives it its name. Against a local bare remote that holds each push for 1.3 s, the presence
  landed 1.86 s after the start instead of 4.71 s (median of 10), and the start on the log 3.45 s
  instead of 1.53 s. With no hold, the presence landed at about the same time (0.51 s against
  0.56 s), and the other two about 0.6 s later.
- Bus commits carry git's empty tree, so they hold no files and fetching them brings nothing else
  along. The daemon pushes them with `--no-verify` and doesn't sign them: a `pre-push` hook that
  runs the test suite, or a signing prompt, on every message would make the bus unusable. These
  commits only ever go to `refs/murmur/*`, never to a branch.
- Events carry no signature of the device's own, so a device can write events that claim to come
  from another (#155). Only accounts with push access can write `refs/murmur/*` (a fork can't), and
  any of them could already push code. A signature would stop one of them posing as another
  teammate's device, and little else: a message gets no authority from who sent it, and an agent
  can inject a prompt under its own name. A full version was built and set aside on 2026-11-03
  (Tyler's decision): Ed25519 keys per device, pinned on first sight, with replay checks, about 860
  lines, on branch `bus-signing` (PR #159, closed unmerged). Since #156 presence is sealed with that
  branch's seal, because it carries the key a device's teammates encrypt to (next section).
- beads (task tracking synced through `refs/dolt/data` on GitHub) shows custom refs hold up at
  scale, and two of its bugs are warnings: `git push --mirror` from an ordinary clone deletes
  custom refs on the remote (beads #5266), and checking an internal branch out in the user's repo
  wiped tracked files (beads #6484).
- `mesh/<deviceId>`, `presence/<deviceId>` and `names` are the only names under `refs/murmur/`
  Murmur reads.
  Device IDs are lowercase letters, digits and dashes, never a name Windows can't hold as a file.
  Anything else there is left on the remote unfetched. Anyone who can push can write any of these
  refs, so a reader holds what it reads to what Murmur writes and builds over junk rather than on
  it. The remote refuses a ref whose name clashes with another's, so a daemon deletes the refs that
  would stop Murmur's own being pushed: those under its own names (`presence/<deviceId>/...`,
  `mesh/<deviceId>/...`, `names/...`), and `refs/murmur/mesh` or `refs/murmur/presence` as refs
  themselves. Murmur writes nothing at any of them (found by the Opus reviews of V1, #8,
  2026-10-28).
- A reader holds what it reads to a size, too (2026-11-09). Git keeps commits compressed, so a
  quick fetch can bring a message of any size: one of 64 MB packed to 64 KB, and reading it whole
  took 448 MB. A commit over 1 MB on a log or a presence, or 32 MB on the names registry (a
  snapshot lists every name, about 45 bytes each), is passed over unread, as junk is, and the
  cursor moves past it; git gives the size without the read. Nothing Murmur writes comes near
  either. A log is read 500 commits at a time, with the daemon's lock let go between pages and the
  state file saved once a page, not once per tags event, so a device back after days away doesn't
  stall the sessions, the hook and the page while it catches up. It is read along first parents:
  Murmur writes every log as a chain, and a page's last commit, the cursor a cut-short read goes on
  from, stands for all before it only on a chain. A merge someone wrote by hand would otherwise have
  its other side read again, and its messages delivered twice.
- Reading: poll `git ls-remote origin 'refs/murmur/*'`, which is cheap; fetch only when a SHA has
  changed. How often is "Speed", below (3 s around a conversation; 10 s before 2026-10-29). A message takes about 6–8 s from a `send` to the
  receiving agent's turn ("Delivering a message as a turn", below); V1, polling every 10 s, took
  about 12 s from a Windows laptop to a macOS machine (2026-10-26), 2 s of it the send.
- A device is one clone's daemon. Worktrees of a clone share its `.git`, so they share its daemon;
  a second clone on the same machine is a second device. The device ID is the hostname plus four
  random hex digits (`tyler-laptop-3f2a`), kept with the daemon's other state in `.git/murmur/`,
  which no commit or checkout ever touches. What people call it is "Device nicknames", below. (An earlier draft grouped repos by root commit;
  per-clone daemons don't need that.)

### The bus is encrypted

Tyler's decision (2026-11-08, #156): what goes over the repo is encrypted, in every repo, public or
private, with **no friction for users or agents**: nobody runs a command, approves anything, copies
a key or makes a choice. The daemon does all of it. An earlier design (#160, closed) made it a
setting someone turned on with `mur encrypt`, and that command was the friction.

Tyler's decision (2026-11-10): **every device that can push, and has published its key, reads
everything ever sent on the bus**, direct messages included. A device that was away reads what it
missed, and one that joins later reads the history. #257's first design encrypted each record for
the devices seen in the last 6 minutes, so a laptop closed overnight came back to a gap it could
never fill, and a new device could read nothing from before it joined. That design is replaced by
a team key, below.

- **Device keys.** At start, each daemon makes any key it lacks: an Ed25519 signing key and an
  X25519 box key, in `.git/murmur/keys` (0600, beside the device ID; no commit or checkout touches
  it). It publishes the box key in its presence and seals the presence with the signing key. The
  seal is an Ed25519 signature over the record's JSON, bound to the ref (`internal/bus/seal.go`,
  from #159).
- **Push access is the key directory.** Only what can write `refs/murmur/*` can publish a key and
  be granted the team key (below); GitHub's own access control decides who is on the team. **That
  is every way to push, not only collaborators** (#397, 2026-11-10): a deploy key with write access,
  an org team or the org's default permission, a GitHub App that can write contents, and a workflow
  that runs untrusted code with a write token (a `pull_request_target` workflow that checks out and
  runs a pull request's code). Git records what a ref points at, not who pushed it, so a daemon
  can't tell a collaborator's push from a workflow's. An outsider who gets one push through such a
  workflow publishes a device of their own, is granted every team key, and reads the bus, history
  included; a rotation changes nothing while the workflow is there. Such a workflow can already
  push code. Opening a pull request can't do it: that writes only `refs/pull/*`. Checking who
  pushed, through GitHub's activity API, is open in #397.
- **Pinning.** A reader pins a device's signing key, in `state.json`, the first time it reads a
  sealed presence from that device. From then on it takes a presence as that device's only if it is
  sealed with that key: none unsealed, none with another (`internal/daemon/keys.go`). A presence
  that fails these checks is refused and logged, the pin stays, and nobody is asked anything. A real
  device never changes keys under its ID (see key loss), so a mismatch means a forgery or a copied
  `.git`. Pinning can't stop someone with push access who seals a presence for a device before a new
  reader has seen the device's own. That reader then refuses the real device. The attack takes push
  access, and the trust model already trusts push access with code. Each presence is signed with a
  time that never goes back on its writer, and a reader takes one as new only if it was signed later
  than every other it has taken from that device (a high-water mark in `state.json`), so a presence
  put back on its ref, or wrapped in a new commit, is never new (Sol's reviews of #257). A device
  whose binary is rolled back to one from before encryption writes unsealed presence, which its
  teammates refuse once they have pinned it, until the updater brings it forward again.
- **The team key.** One shared ref, `refs/murmur/team`, holds a chain of sealed records, pushed with
  a lease as the name registry is, so two devices can't both make the first key
  (`internal/bus/team.go`, `internal/daemon/team.go`).
  - An *epoch* publishes a team X25519 public key in the clear, with its private half wrapped for
    its writer's box key. The first daemon to find the ref empty makes the first epoch.
  - A *grant* wraps every team private key its writer holds for one device. Each device puts, inside
    its encrypted presence, the ID of the newest epoch it has read and the IDs of the team keys it
    holds. A daemon that holds the current key grants to each device with a pinned box key whose
    sealed presence names the current epoch and lists fewer keys than the daemon could give it
    (Tyler, 2026-11-10).
    - **Naming the epoch is the proof of push access.** An epoch's ID is the hash of a random key, so
      a presence that names it was signed after the epoch was made, and it sits on the device's own
      ref, so it was pushed after too. A collaborator removed before a rotation can read the new ID
      on a public repo and sign a presence naming it, but can't push it. An older presence of theirs
      put back on the ref names an older epoch. No time is trusted.
    - **Nobody has to be online together.** The presence's age doesn't matter, nor whether anyone
      saw it arrive. A device that joins while nobody is around pushes its presence and can go; the
      first key holder to sync grants it, and it reads the grant when it next runs. An earlier rule
      granted only on a presence a daemon watched arrive, and such a device then got nothing.
    - **By what a device holds, not by what the ref shows.** A grant that doesn't open, or carries a
      key under another's ID, leaves its device without the key while the ref shows it granted. The
      list in the presence shows it still lacks one, so it is granted again.
    - **A device that is away isn't granted over and over.** Its presence stays as it left it. A
      daemon that has read a grant for a device grants it again only on a presence signed later
      than the newest it held when it read that grant (`GrantMark` in `state.json`). Both times are
      that device's own. So a device with a junk grant gets a real one after its next presence, not
      before; and one presence pushed between a grant and reading it can earn a second grant.
  - A *roster* lists the people who can push to the repo, encrypted for one epoch's team key, so
    every device granted that key reads it. `mur rotate` writes one and compares against the last.
  - A key under a team key ID that isn't its own is dropped when a grant is opened, so a grant can't
    hand a device a key under another's name.
  - Team private keys a device is granted are kept in `.git/murmur/team-keys` (0600), which only
    grows: a rewritten team ref takes nothing a device could read away from it. If that file is lost
    while `state.json` stays, the daemon reads the team ref again from its start at its next run: its
    grants are still there, so the keys come back with no teammate around.
- **Records.** Each event and presence is encrypted once, with a fresh content key wrapped the way
  age does it, with an ephemeral X25519 key, HKDF-SHA256 and AES-256-GCM, all from Go's standard
  library (`internal/bus/box.go`). A presence is encrypted for the current epoch's team key alone.
  An event is encrypted for that key and for its writer's device: a daemon reads its own log to
  queue a message from one of its sessions to another, and would otherwise stop at its own record
  until it was granted the key. The team key's
  public half is readable by anyone, so a device can write before it holds any private half. An
  event is written as `{"v":1,"type":"box","box":{…}}` with the whole event inside: sender,
  recipient, type, subject, body, tags. A presence keeps its device ID, time and box key readable and
  encrypts its host, owner, sessions, offline sessions, and the epoch and team keys a grant goes by. Before a device has read any epoch, it
  encrypts its presence for itself alone, and pushes it again when it reads one. Commit subjects are
  `event` and `presence`; they used to repeat the content.
- **A device waiting for a grant** can send at once. Reading a log stops at the first record
  encrypted for an epoch it hasn't been granted, and goes on from there once it is, so nothing is
  missed. A box for a team key the team ref doesn't show waits as well, until a sync that began
  after the record was first met has read the team ref whole: a rotation's first records can be
  fetched before its epoch is read, when the team ref's fetch fails or a log's fetch goes past what
  the sync listed, and passing over them would lose them for good. If that sync shows no such key
  the box is junk and passed over, so junk holds a log up for one sync. The stretch of a log whose
  fetch was once given up on, read for its tags alone, stops and goes on the same way. While it
  waits, teammates' presence shows with no sessions. **The grant comes when a key holder next
  syncs**, which is the floor for any scheme where only devices hold keys. Messages read late this way can reach a session that
  started within the minute after they were sent, by the allowance `queueLocked` gives a sender's
  clock (found in `TestRestartCatchesUpReconnectingSessions`, 2026-11-10).
- **Rotation.** A new epoch is a new team key, and everything is encrypted for it from then on. It
  is wrapped for its writer alone; every other device gets it by a grant, which takes a presence
  that names it. A collaborator removed from the repo can't push, so never gets it, **unless
  something else of theirs still can**: a deploy key they added, access through an org team, an App
  (#397). Removing someone means removing each of those, or a rotation keeps nothing from them. A device that
  joins later is still granted every old key, so history stays readable. Only a device that holds
  every team key may rotate, since the devices it grants get the keys it holds; that is checked
  again on each try, as another device may have rotated meanwhile.
  `mur rotate` lists the people who can push to the repo with `gh` and makes a new epoch if anyone
  on the current epoch's roster is no longer among them, or if this device can read no roster for
  it; `--force` always does. Someone moved down to read access counts as gone. When nobody has left
  and someone has joined, it writes the roster again with them on it, and makes no new key; if a
  teammate's device wrote a roster meanwhile, it compares again with that one first, since writing
  over it could take off someone who has left with no new key made.
  **Someone who joined and left between two runs was never on a roster, so their leaving isn't
  seen**; only `--force` covers that until rotation runs on its own. When it should is open (Tyler,
  2026-11-10: the structure first).
- **Fails closed.** A daemon with this change never writes content in the clear. A message to a
  device with no key is refused, and the sender is told that device runs an older Murmur. A write
  made before any epoch exists waits for the first, which the first sync makes.
- **Older daemons.** A daemon from before this passes over `box` events, as it does any type it
  doesn't know, and sees an updated device with no sessions. An updated daemon reads the older one's
  plaintext as it always did. It reads a plaintext event from any device, since events carry no
  signature to hold them to, and plaintext presence only from a device it hasn't pinned. So for a
  while older devices can't hear updated ones, and messages to them are refused. The updater (see
  Updating) closes that gap within an hour of the release, with nobody acting. A version from before
  the updater needs the manual install it already needed. **Until it updates, an older daemon writes
  in the clear, and nothing can stop it.**
- **A collaborator removed from the repo** keeps every team key they were granted. On a public repo
  they can still fetch the bus, so **they read everything written until someone rotates**, and
  everything before. On a private repo, removal takes their read access with it. #257's first design
  dropped them within 6 minutes with nothing to run; the team key trades that for history and for
  devices that come back.
- **No forward secrecy.** A leaked device's keys read everything up to the last epoch it was granted.
- **Key loss.** A deleted clone comes back as a new device, and is granted the team keys like any.
  If only the keys file is gone and `state.json` shows the device had keys, the daemon takes a new
  device ID, because its teammates would refuse a new key under the old one. The old ID's presence
  goes stale, and its names stay taken. With `state.json` gone too, the clone's local copy of its own
  presence ref still shows a seal, which tells the daemon it had keys. Only a daemon meeting
  encryption for the first time has neither, and keeps its ID.
- **The name registry stays in the clear, and unsealed.** It holds random names, device IDs and
  random session IDs. A device must read all of it before it can name a session, which may be before
  it is granted the team key. For the same reason an encrypted presence carries its sessions' names
  and IDs beside the box: a device that can't open it yet still knows those names are taken. That
  exposes nothing the registry doesn't.
- **What anyone who can read a public repo still sees:** device IDs, which begin with a slug of
  the hostname; when each device writes, how many records and how large they are; the team key IDs
  each record is encrypted for; which devices each team key was granted to, and when; each device's
  public keys; the name registry; and `refs/murmur/reported`. They don't see messages, subjects, who
  sent what to whom, statuses, branches, tags, hosts, owners, agents, or who is on a roster.
- **Open, for Tyler** (#156): whether the census (#199) should keep anything readable, such as
  `owner` or an event's kind, now that encryption hides them from it; whether new clones should
  get a device ID without the hostname in it (#330); and when a rotation should happen without
  anyone running `mur rotate`.

### One daemon per device, a thin MCP server per session

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

One device runs many sessions, often across worktrees. The daemon is the only process that
writes the device's ref and the only one polling GitHub; sessions connect to it. Each session
starts its own MCP server process (that is how Claude Code runs stdio MCP servers), and the first
to start launches the daemon. Under a job that forbids breakaway, as Codex's MCP servers are on
Windows, the daemon is started through WMI so it outlives that session (2026-10-29). A session's
MCP process can restart while its conversation carries
on: T3 Code restarts it on every interrupt, after 30 minutes idle and on every app restart (one
thread's restarted 8 times in about 2 days, 2026-10-28), and Claude Code on `/mcp` reconnect and
on resume. So a dropped connection starts a 60-second grace (2026-10-28): the session stays in
presence, and a hello with the same session ID in that time takes it over with its inbox and
unread notices, writing nothing to the bus. Only when the grace runs out does the daemon announce
`session.stopped`, even after a crash. A stopped session's inbox is kept 24 hours, across daemon
restarts, and given back if the session returns. It holds only what its daemon received: a daemon
exits 10 minutes after its last session or open page, unless a T3 thread it could still wake is
waiting ("A thread that is idle", below), and one back after longer than that skips what it
missed, except for the sessions whose inbox is kept (2026-10-30, #30): for a session stopped
within the 24 hours it queues what was sent after the session stopped (by the sender's clock,
with a minute's allowance), which includes a message to the whole device or to everyone, as the
live ones do and as the shorter outage's catch-up does. Before then a laptop shut overnight lost
every message sent to its stopped sessions. A first start has no stopped sessions and skips all of
it. A message is addressed on the bus to
`device/session`, which a session's name stands for (see Session names); presence carries enough
description (name, agent, branch, what it is doing) for an agent to choose a recipient. Nothing sent before a session first started is replayed to it. A
daemon back after less than 10 minutes down queues what it missed for the sessions that
reconnect within 30 seconds of its start, and for sessions stopped within the 24 hours.

Sessions reach the daemon over loopback TCP, on a random port written with a random token to
`.git/murmur/daemon.json`. A client trusts that file only while the clone's lock (`daemon.lock`) is
held (2026-11-09). A daemon that dies without removing the file (a crash, SIGKILL, a power cut)
leaves an address another user's process could listen on, to be sent the sessions' hellos and
requests and to answer with messages of its own. A daemon takes the lock before it writes the
file, removes the file when it begins to stop, before it gives up its port, and lets the lock go
only when it has gone. So a daemon.json beside a free lock is stale and is removed, a starting
daemon removes one as soon as it holds the lock, and a client that loses its daemon forgets the
address. Clients take the lock only shared, and only a daemon takes it exclusively, so a lock a
client finds held is a daemon's; a timed retry left a window where another client's cleanup looked
like a daemon. A client from before this takes it exclusively for a moment while stopping or
replacing a daemon. This holds where the state directory is private to its user: it is made 0700 on Unix, and on Windows
gets an access list of its own (below).

The lock is checked when a client finds its daemon, and a client goes on using the address after
that. A daemon that died later left its port to whoever took it, and that listener got the next
request, the token and any outgoing message with it, and could answer with an inbox of its own
(#371, from Sol's review before v0.3.0). So since protocol 12 (2026-11-10) every connection a client
opens starts with a ping that carries a fresh nonce and no token, and the daemon answers with an
HMAC of its address and the nonce keyed by its token. Only then does the request go, on the same
connection, so nothing can take the port between the proof and the request; a separate ping first
would have left that gap. What answers without the proof is not the daemon, and the client finds
its daemon again (or starts one). Until it is proved, a reply is read only up to 4 KB, and a proved
daemon's up to 64 MB, about half again a full inbox of the longest messages escaped: before, one
that never ended was read until the deadline, however much it came to. A daemon of protocol 11 or
older can't prove itself, and refuses the nonce's ping as a missing token. A client sends it
nothing at all, not even a stop, and so can't ask it to stop or to make way: it says the daemon is
from an older version, and how to end it, by the process ID in daemon.json, or that it exits on its
own 10 minutes after its last session, when the next command starts a current one (2026-11-10).
That is the cost of an upgrade from 0.2, which never updates itself, so everyone coming from it
installs by hand: a daemon left running by the old mur is in the way until it goes. `mur stop` and
`mur join` (and a leave, and `mur uninstall`) end it by its process instead, which sends it nothing
(#394, 2026-11-11): the process daemon.json names, once the clone's lock is held, the process is
alive, and the program it runs is mur, so that a process ID since given to something else is never
ended. On Unix it gets SIGTERM and shuts down as it does when asked; on Windows it is terminated,
with no shutdown, as a crash ends it. A session's own client still ends nothing: an agent's
session doesn't end a process on its own, and the person upgrading runs one of those commands.
Everything that talks to it was tried first and failed review (Sol's reviews of main before
v0.3.0-rc.1 and of #389). Talking to it unproved while the clone's lock was held left a gap between the check
and the dial. Checking after the dial still trusted a daemon from before 07a1ecba, which closes its
port as it begins to stop and keeps its lock and daemon.json while it finishes, so another process
can take the port in that time. Even a stop alone gave that process the token, which is also the
key the proof uses: it could then prove itself to the next client, beside the old daemon's lock and
file, and be sent its messages and answer with text the hook puts in the agent's context. A daemon
of 12 or later that is older than its client proves itself, and is replaced as before. Discovery still contacts the
listener before trying the lock, since trying the lock can turn away a daemon starting just then;
what the listener is sent there is the nonce alone.

A daemon answers, and writes daemon.json, before its first sync, not after it (2026-11-10, #372).
That sync lists the remote, fetches and reads what changed, and may take its whole 10 s; with the
process's own start on Windows, a first start of a new binary on Tyler's laptop took 21 s, and the
15 s a starter waits for an answer (`mur ui`, an MCP server) ran out on a daemon that was fine.
What the sync reads still comes first where it counts: a registration waits for it, at most those
10 s, so what it finds is the backlog and not a new session's, and so do who, send and lookup,
which on a new clone know no other device until then. The background loops start after it, as
before. A stop or a leave during it ends it.

A daemon with no sessions, no open page (see The page for humans) and no
T3 thread to wake for 10 minutes exits. The token lets its holder name the program the daemon runs
to hand a message to T3, so reading the file is running code as the user. `.git/murmur` is made
0700, and on Windows, where modes do nothing, each daemon start gives it an access list of its own
that lets in only the user, SYSTEM and Administrators, and rewrites the files already in it
(2026-11-09). Before that a clone outside the user's profile left it readable by every user. A
network drive, and a volume with no access lists, is left as it is: a share checks access as the
account its server knows, and a list naming the local user could shut them out for good. So is a
directory an elevated run made, which a later run without elevation may not change; that is only
logged. Any daemon that stops deletes its presence, so other devices drop it at their
next sync rather than when it goes stale.

A manual upgrade replaces the binary and not the daemon already started from it, which would go on
answering ping and refuse whatever came after the version it was built from (the route request
and handing a message over as a turn, #57, were the first). So the daemon reports a protocol number
(`proto.Protocol`, 1 as of 2026-10-30; a daemon from before the number reports none, read as 0) in
its ping reply, and a client that finds a lower number than its own stops that daemon and starts its
own: `mur join`, `mur ui` and an MCP server registering a session do, since each may start a
daemon. Only an older daemon is replaced, never a newer one, so two binaries of different ages on
one machine don't stop each other's daemons for ever, and a number is used, not the version string,
so that builds from a branch (which all say "dev") are told apart. A client that may not start a
daemon (the hook and the other commands, which never start one; `mur stop`, which ends an old one by
hand; a user who has left) takes the daemon as it is. The old daemon is asked to stop and
takes its time: it lets its background work finish for up to 20 seconds and then deletes its
presence for up to 15, holding the clone's lock throughout, and the new one can't start until the
lock is free. So the client waits for the lock for as long as a shutdown may take, 35 seconds, and 5
more (`proto.ShutdownMax`; it waited 15 s until #83), or less if its caller gives up first, and
fails with an error that names the old daemon's pid if the lock is still held. A session whose
daemon refuses its route (an older one that couldn't be replaced) works, with its messages as
notices, and its MCP server logs that once, to its standard error, so that whoever wonders finds
why. Raise the number with any change to a request or a reply that an older daemon can't serve.

The daemon's log, `.git/murmur/daemon.log`, is its only record of what it did, so it is what a
teammate is asked to send when their device misbehaves, maybe to someone off the team. So it can be
shared: it names events by type, ID and address, never by what a message says, and each line the
daemon's loggers write passes through `internal/logfile`, which takes the user and password, the
query and the fragment out of any URL, and cuts a line at 64 KB, with a note of how much it cut (a
starter's own lines and a panic's trace go to the file directly). It does name the clone's paths.
Past 4 MB it moves to `daemon.log.old` when a daemon starts, and while it runs it is copied there and
emptied, since Windows can't rename an open file. If the copy fails it is emptied anyway, with a
line saying so; if emptying fails, the lines that don't fit are dropped, and it is tried again a
minute later. A daemon whose log was moved under it, as a later start does past 4 MB, stops moving it and writes on to the moved file. Until
2026-11-09 it moved only at a start, so a daemon kept up for days by an open page grew it without
limit.

### Joining a repo

Decided 2026-10-29 (#55), built the same day. Tyler's requirement: setup should feel like magic,
and T3 Code users are the main customers. The user should still know Murmur is running and choose to
join. So a repo that uses Murmur says so, in a section of `AGENTS.md` and `CLAUDE.md` that an agent
reads when it starts; an agent that has no Murmur tools asks its user whether to join; and if the
user says yes, the agent installs mur and runs `mur join`, which does the rest. Tyler also decided
that messaging gets no command-line commands (`who`, `send`, `inbox`, `log` were removed): "If
everyone is using the CLI, there will be no one to message." Agents message through MCP, humans look
at the page (`mur ui`), and the commands left are `init`, `join`, `ui`, `stop`, `uninstall`,
`version` and `help`.

- **The marker.** `mur init` writes a section between `<!-- murmur:start -->` and `<!-- murmur:end
  -->` into both files, creates either if it is missing, and rewrites the section in place on a
  rerun. The section is six lines, since every agent that opens the repo reads it whole (Tyler,
  2026-10-29, after a first version of about twenty): it says the repo uses Murmur and links two
  pages in `docs/agents/`, by raw GitHub address. `join.md` is for an agent without the tools: ask
  the user, say what joining does, install mur (the one-line command for macOS and Linux or for
  Windows PowerShell), run `mur join`, tell the user what it printed. `use.md` is for an agent with
  them: keep `status` set, check `who` before sizeable work, answer a message with `send`, and
  `inbox` for history and as the fallback. The addresses work without login, since the repo is
  public. A test holds the
  addresses to files that exist. `init` writes no other file. An older `init` had put an `.mcp.json`
  entry and a hook in `.claude/settings.json`; `init` removes only Murmur's own entries from those,
  deletes a file that is left empty, and says so. Committing installs nothing on a teammate's machine.
- **`init` pushes its commit to the default branch** (Tyler, 2026-11-01, #119). Until then it edited
  the working tree and said "review and commit"; run in a T3 worktree, the section landed on that
  worktree's branch at best, so teammates' agents on main never read it. Now `init` fetches the
  default branch of the clone's remote (the bus's remote, `murmur.remote` else `origin`; the branch
  the remote's HEAD names), applies the same edits to that commit's files, builds the tree with a
  temporary index (`GIT_INDEX_FILE`) and `commit-tree`, and pushes `<sha>:refs/heads/<default>`,
  never forced: the same rule as the bus, no working tree or index touched. A push rejected because
  the branch moved is rebuilt on the new tip, up to 3 tries. One the server refuses for a reason of
  its own (a protected branch, a hook) goes to a new branch, `murmur-init`, and `init` prints the
  GitHub page that opens a pull request from it; a `murmur-init` already there with the section
  counts as pending, and one without it stops `init`. A failure to push at all (no permission, no
  network) is reported, not worked around. Nothing changes when the branch already has the section.
  The checkout `init` ran in is fast-forwarded to the commit only when it is on the default branch,
  behind the commit, with no local change to the four files (git refuses a pull over an
  uncommitted change to a file the pull brings, even an identical one, checked 2026-11-01);
  otherwise it is left alone and gets the section when the default branch is merged in. The push
  skips the repo's pre-push hook, as bus pushes do: the hook would run against a checkout that
  isn't this commit. `init --no-push` is the old behavior, and refuses as the push does a file
  that a symbolic link takes out of the repo (2026-11-09). `docs/agents/setup.md` is the page an
  agent follows when its user asks it to set Murmur up; the README links it.
- **What `mur join` writes.** It refuses in a repo without the marker. Everything it writes is
  outside the repo's tracked files:

  | What | Where | Per |
  |---|---|---|
  | Consent | `.git/murmur/joined.json` (mode 0600) | clone, so every worktree of it |
  | The MCP server, for Claude Code | `claude mcp add --scope user murmur -- <absolute path of mur> mcp`, which writes `.claude.json` in `$CLAUDE_CONFIG_DIR`, else the home directory | user |
  | Permission for its tools | `mcp__murmur` in `permissions.allow` of `settings.json` next to it | user |
  | The MCP server, for Codex | a `[mcp_servers.murmur]` table in `$CODEX_HOME/config.toml`, else `~/.codex/config.toml`: the command, `args = ["mcp", "--agent", "codex"]`, `env_vars = ["T3CODE_CLI_PATH", "CODEX_HOME"]` and `default_tools_approval_mode = "approve"` | user |
  | The MCP server, for OpenCode, Muse Code, Pi and Grok | see "Other agents", below | user |
  | The skills, `murmur` and `murmur-pro` (`internal/skills`) | `skills/<name>/SKILL.md` in the home of Claude Code, Codex, OpenCode and Pi; Muse Code and Grok document no skills folder (2026-11-08) | user |
  | The hook for Claude Code, on `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit` and `Stop` (`setup.HookEvents`) | `settings.json` in each Claude Code home (see "Claude Code's hook is per user", below) | user |
  | The hook for Codex, on `UserPromptSubmit`, `PostToolUse` and `Stop`, approved (see "Codex's and Grok's hook") | `hooks.json` in each Codex home, its trust in `config.toml` there | user |
  | The hook for Grok, on `PostToolUse` and `Stop` | `hooks/murmur.json` in Grok's home | user |
  | The daemon | started, if none is running | clone |

  The skills carry the judgment the tool descriptions don't: when Murmur is worth a turn, and how
  to coordinate over several messages (#206). A SKILL.md of the same name without the skills'
  marker comment is the user's own, and is left alone; one with it is rewritten to this binary's.
  A leave leaves them, like the server, and says how to delete them. Unlike the server, they cost
  every session of those agents their descriptions' worth of context in any repo, so each
  description starts by saying it applies only where the repo has the Murmur section. The folders
  are from each agent's documentation, not tried in a session (2026-11-08). OpenCode also reads
  `~/.claude/skills`, so with Claude Code beside it it sees each skill twice, the same text.

  A client that isn't installed is skipped. Each file is edited, never replaced: other servers,
  settings and comments stay, and a form it can't edit safely (a dotted `mcp_servers.murmur.command`
  key in Codex's TOML, a `permissions` that isn't an object) is a reported problem, not a guess.
  `codex mcp add` is not used, because it rewrites the whole file. The path is absolute so the
  server starts whatever PATH the client has. The hook command is the path in double quotes with
  forward slashes (`"C:/Users/a b/mur.exe" hook`), because Claude Code runs hook commands through a
  shell, which would split a path with a space, eat the backslashes of a Windows one, and expand a
  `$` or backtick in it; those are escaped. Measured to run under Claude Code 2.1.312 with a space
  in the path, 2026-10-29. A config file that is a symbolic link (a dotfiles repository's, say) is
  written through, one that points at a file not yet there included: the file it points to is
  replaced or created and the link stays; a hook file that is a link is emptied, never deleted.
  A hook file the repo tracks, or a `.claude` it tracks as a link, isn't the user's, and gets no
  hook (2026-11-09): a committed link points wherever the commit chose, `~/.claude/settings.json`
  included, and a tracked file would take this machine's path to mur into the next commit. Names
  are matched ignoring case, as macOS's and Windows' disks match them. It is a note under "Not
  done", not a problem: repos do commit that file by accident, and a rerun couldn't fix it.
  Run again, `join` changes nothing and says so; it fills in what is missing and repoints an entry
  whose binary moved. A worktree made after joining has no hook until `mur join` runs in it again.
  `mur join --leave` removes the consent and the hooks and stops the daemon. It leaves the server
  registered, since that is per user and does nothing where the user hasn't joined; its output says
  how to remove it.
- **`mur uninstall` removes Murmur from the machine** (Tyler, 2026-11-10, #399). A leave can't remove
  the per-user entries, because one entry serves every repo the user joined and a leave can't tell
  whether its repo was the last. Uninstall can, because it reads the record of this machine's clones
  (`internal/clones`). It refuses while a clone on the record is still joined and names each, or
  with `--leave` leaves each first, and stops if a leave didn't take the consent away. It then takes
  out what join wrote per user in every agent configuration join registers in, T3's instances
  included. That covers the server entry and allow rule in each agent, both OpenCode shapes, Codex's
  hook groups and their trust entries, Grok's hook file, Pi's extension, the skills, and a notice hook
  in Claude Code's own `settings.json`. The edits follow join's rules: files read as regular files
  under jsonedit's cap, links written through, JSON re-parsed before it is written, a form join
  wouldn't edit refused, and the user's other entries kept byte for byte. The Claude Code server
  goes through `claude mcp remove` where the command exists, as join adds it through `claude mcp add`. Codex keys
  a hook's trust by its group's place, so the user's own groups after Murmur's move up one and their
  trust entries are renamed with them, so Codex doesn't ask about them again. Every Codex edit is
  made before any is written, with each `hooks.json` or `config.toml` that two homes share edited
  once, and `hooks.json` goes back as it was if a `config.toml` can't be written: once Murmur's
  groups are out of it, nothing tells a rerun where they were. Uninstall then leaves again every
  other clone on the record, which takes out a hook an earlier leave left and stops a daemon
  through `client.Stop`. A daemon from before 0.3 can't be asked (#389), and the error names its
  process ID. Last, it deletes the record's file of each clone whose cleanup finished. A clone whose
  cleanup didn't finish, or whose folder isn't there now (deleted, or on a drive not mounted), stays
  on the record, so a rerun finds it (Sol's reviews, 2026-11-10). Then it deletes Murmur's folder in
  the user's config directory, which also holds the records MCP servers publish of themselves
  (`internal/here`) and `id`, the random user ID the check-ins of 0.2.3 sent, which nothing reads
  since #199 (#430). A server still running keeps its record, since it reads its moves there until
  its session ends, so the folder stays with it, and the report names it and says why. It leaves what isn't one
  user's or can't go while it runs: each repo's committed section, each clone's `.git/murmur` and
  bus refs, and the binary itself, which Windows can't delete while it runs, so it prints the
  command that does. A clone that joined before the record existed and whose daemon hasn't started
  since isn't on the record, so uninstall can't see it. Its server entry is gone anyway, so nothing
  runs there.
- **What it prints.** What it did, what went wrong, and "Tell your user", chosen from which agent ran
  it (see Reloading MCP servers below), so the agent can say exactly what to do next.
- **The server stays passive where it isn't invited.** The user-scope entry starts `mur mcp` in
  every repo the user opens. The server is on only while the repo says it uses Murmur (this working
  tree's `AGENTS.md` or `CLAUDE.md` has the marker, or the clone's remote-tracking default branch,
  `refs/remotes/<remote>/HEAD`, does) and this user has joined in this clone, checked every 2
  seconds. The working tree and the consent are read from files; the remote-tracking branch, which
  needs git and is read only in a joined clone whose working tree lacks the marker, at most every 20
  seconds. It counts so that a worktree on a branch made before `init` pushed isn't left off until
  it merges the default branch (#119). Off, it advertises a tools capability with no tools and no
  instructions, contacts no daemon, and answers a call that arrives anyway with why it is off. A
  join turns it on and lists the tools through `tools/list_changed`; a leave removes them. MCP
  instructions are fixed at initialize, so a server that started off never has them: the pages the
  section in `AGENTS.md`
  and `CLAUDE.md` links to carry that guidance. Nothing relies on the server alone, and the checks differ by
  what the thing is for. The server asks again before each registration with the daemon, before
  starting one, and before forwarding a tool call, since its 2-second look can lag a leave. The
  daemon is per clone, not per working tree, so it needs only the consent: it checks at start
  and again after its first sync, which takes a while, on every request but `stop` and `ping` (which reveal
  nothing, and are how a leave finds it), again in a registration after it has resolved the branch,
  before each push of its own (presence, the names registry) and before each handoff of a message
  as a turn (the one thing it does to the user's other programs, so a message that comes in by sync
  after a leave waits in the inbox, and isn't counted as a failure), and once a second, when it stops
  itself if the user has left. The last two are what end a daemon that was still starting when
  `join --leave` ran and so had no `daemon.json` to be found by. `mur daemon` and `mur ui` refuse in
  a clone that hasn't joined (so a stale client or a script can't start one). `join --leave`
  forgets the consent before it stops anything, then waits for the daemon's lock to be free as long
  as a replacement does, 40 seconds from asking it to stop (20 until #91, which was shorter than a
  shutdown may take), saying so on standard error once it has waited 5, and reports it as a problem,
  not as stopped, if it isn't. What it can't close: a push already past its check when the leave
  lands completes, and a starter already past its own check (Windows starts the daemon through WMI,
  which is slow) can still launch `mur daemon`, which exits at its consent check before it opens any
  state or publishes. A repo without the marker, or one the user hasn't joined, gets no daemon, no
  `refs/murmur/*` and nothing published. A clone whose user joined but whose current branch lacks
  the marker keeps its daemon (it is the user's, for every worktree) and gets no MCP tools there.
- **Per user, in every account T3 runs.** T3 can start Claude Code with its own `CLAUDE_CONFIG_DIR`
  (a provider instance with a home path, as `claude_personal` on Tyler's laptop, 2026-10-29), and
  Codex with its own `CODEX_HOME`. Until 2026-11-01 `mur join` wrote only to the home of the agent
  that ran it, so a thread under Tyler's other account had no Murmur tools and nothing said why.
  Now it also reads T3's `userdata/settings.json` (in the first of T3's data directories that has
  one, found as for the database) and registers in the home of every provider instance that is on:
  `config.homePath`, else the same variable in the instance's environment, else the agent's
  default; `config.binaryPath` finds an agent that isn't on PATH (Muse, here). Which instances are
  on, and the defaults, follow T3's own code (`internal/join/t3homes.go` says how). Homes are
  deduplicated, the agent's default home is the same registration as before, an instance that is
  off is named under "Not done", and without T3 nothing changes. The file holds API keys, so join
  decodes only those fields, takes an environment value only by name and only if T3 doesn't mark it
  sensitive, and never prints the file.
- **A session that started outside the repo** (2026-11-08, #200). The MCP server picks its repo from
  the directory the agent started it in, so a session started in a parent folder, the home directory
  or a workspace holding several repos never got a name in the repo it then worked in. Now each
  server publishes a record of itself (its PID, its agent's PID, Claude Code's session ID, the
  clone it is in and whether it is on) in the user's config directory, `murmur/sessions/`, and
  `mur join` finds the record of the session that ran it: the agent's process is the server's
  parent and an ancestor of the command, the nearest such ancestor winning. Claude Code also puts its
  PID in `CLAUDE_PID`, but that is used only where the ancestors can't be read: an agent Claude Code
  starts inherits it, and would otherwise move Claude's session instead of its own. Join leaves a
  move there, which the server picks up on its next look for a join or a leave: it turns off where
  it was and on in the joined clone, with the joined directory as its working directory. A session
  is in one repo at a time (Tyler accepted either answer; one was chosen because the tools take no
  repo, and two meshes would need one on every call and every message), so a move takes it out of
  the repo it was in. A move doesn't stand in for consent: in a clone the user hasn't joined the
  server stays off. Where one agent process runs several sessions and nothing tells them apart
  (Codex's app server in T3), join moves none and says so; OpenCode's one server serves every
  session in its directory, so it can't be moved; it publishes a record marked fixed anyway, which
  stops the search up the process tree, so a join from an OpenCode that Claude Code started is
  refused rather than moving Claude's session. A move carries the agent
  process's start time and Claude session ID from the record, so a new process that took a dead
  agent's PID doesn't follow it; records and moves of exited processes are cleared whenever they are
  read. Moving ends the old registration's context, and a call still on its way to the old repo's
  daemon with it: the client sets a past deadline on the connection when the call's context ends,
  which stops a write or read already under way, and checks the context just before sending. The instructions an MCP server
  gives at initialize can't change afterwards, so a session that moves in has the repo's section
  and the tool descriptions, not them. The hook is per worktree, and runs in the directory the
  agent started in, so commits aren't credited to a session that moved in (`internal/here`).

#### Other agents

Added 2026-11-01 (#128), at Tyler's request: OpenCode, Muse Code, Pi and Grok, in T3 Code and on
their own. Each was looked at in its installed binary, its documentation and probe runs (OpenCode
2.2.0, Muse 1.4.14, Pi 0.99.0 on Tyler's laptop; Grok Build from its documentation and source only,
since it wasn't installed). The details, and where each came from, are in the comments of
`internal/join` (one file per agent) and `internal/mcpserver`.

| Agent | Where join writes | How a session is told apart | Inside T3 |
|---|---|---|---|
| OpenCode | `~/.config/opencode/opencode.jsonc` if there (OpenCode reads it after `opencode.json`, whose own murmur entry is made the same), else `opencode.json`, in the shape the installed OpenCode loads, from `opencode --version`: for 2.x, `mcp.servers.murmur` with `"codemode": false` and a `murmur_*` allow rule kept last in the `permissions` list; for 1.x, or a version join can't learn, `mcp.murmur` with `"enabled": true` and `"murmur_*": "allow"` kept last in the `permission` object, which 2.x loads too. In both the last matching rule counts. OpenCode 1.x refuses a whole config that has `permissions` (#388), so a rerun moves Murmur's entries to the shape it now writes | One server process serves every session in a directory; each call names its session in `_meta["ai.opencode/sessionID"]`, and the server keeps a registration per session that calls | The session ID is T3's native ID for the thread |
| Muse Code | `mcpServers.murmur` in `~/.config/muse/settings.json`, `"mode": "optional"`, `env` forwarding `T3CODE_CLI_PATH` and, on Windows, the home variables, as `${VAR:-}` | One process per session, with `MUSE_SESSION_ID` | The same ID |
| Pi | an extension, `extensions/murmur.ts` in `~/.pi/agent` (a `~` in `PI_CODING_AGENT_DIR` is the home, as for Pi), that registers the server with the session's file in `MURMUR_PI_SESSION_FILE`; a `murmur` server in Pi's `mcp.json`, which Pi would run instead, is reported | One process per session | T3 knows a Pi thread by that file (not tried: Pi is off in T3 on the laptop) |
| Grok | `[mcp_servers.murmur]` in `~/.grok/config.toml`, and `MCPTool(murmur__*)` in `[permission] allow` | One process per session, with `GROK_SESSION_ID` | Expected to be the ACP session ID T3 records (not tried) |

- **Edited, not rewritten.** JSON and JSONC files are edited as text (`jsonedit.SetIn`,
  `AppendIn`): one member or item changes and every other byte, comments and alignment included,
  stays. A file in a form join can't read safely (not an object, a path through a list, a key
  given twice, Muse's older `mcp_servers` beside which a new key would drop every server) is a
  reported problem. Grok's TOML goes through Codex's editor. Pi's extension is Murmur's own file,
  written whole, and one of that name that isn't Murmur's is left alone.
- **Identity.** Claude Code's variables count only for Claude Code: Pi and OpenCode pass their own
  environment on to their servers, and one started from a Claude Code session carries that
  session's `CLAUDE_CODE_SESSION_ID`. An agent's own session ID becomes the session's Murmur ID by
  hash, since Muse's and OpenCode's IDs begin with the time and two of them often share six
  characters. A T3 instance with no directory of its own gets the user's default, not the directory
  of the instance that ran join. A session of the shared OpenCode server is registered at its first call and dropped
  after 30 minutes without one, as a stopped session is; its T3 route outlives that as any does.
- **Delivery.** Inside T3 the existing `t3 thread send` handoff needed nothing new: an OpenCode and
  a Muse thread each took a message as a turn and answered it with `send`, nobody typing
  (2026-11-01). Outside T3, Muse and Grok have no way in that Murmur uses yet (Muse's session
  messaging isn't on Windows, and no way to start a turn in Grok was found), so a message waits in
  the inbox. OpenCode 1 and Muse have no hook of Murmur's, so no notice either; Grok has one (below). Pi
  has both since 2026-11-09 (#205), and OpenCode 2 takes a turn through its server since
  2026-11-11 (#135): see "Pi's extension" and "The client's command line" under "Delivering a
  message as a turn".
- **OpenCode 2's plugin (2026-11-12, #431, #455).** OpenCode 2 has no hooks of Claude Code's kind,
  so join writes `plugins/murmur.js` into its config directory, which OpenCode loads with no entry
  in `opencode.json`. The plugin runs `mur hook --agent opencode` after each tool call, when a turn
  starts and when it ends, with OpenCode's session ID, so an OpenCode 2 session is told of a
  message while it works and handed what is unread at its turn's end, as Claude Code is.
  - *How the words go in.* `ctx.session.synthetic` with `delivery: "steer"` puts the notice after
    the tool's result and before the next model request, as a message of its own. Appended to the
    tool's result instead, a model took it for text planted in the file it had read and refused it.
    At a turn's end the session is idle, and the same call starts another turn. What it costs:
    OpenCode's terminal UI shows the answer to a synthetic message and not the message.
  - *Order.* Each hook is a process, so the plugin runs one session's hooks one after another. A
    turn's start that reached the daemon after its end would leave an idle session looking busy.
  - *Children.* A session the `subagent` tool started carries `parentID`. The plugin says so on
    every hook request of that session (`child`, `parentNative`, #473), and once as the session is
    made, before its first call to one of Murmur's tools registers it; the daemon marks a session
    named so as it registers. So an OpenCode child is never listed, and its `send` is refused with
    its parent's name. The gap #487 describes for Codex is left only where the daemon didn't hear
    the plugin: a child made while no daemon ran, or before a daemon restart, is on the mesh from
    its first call to one of Murmur's tools until its next tool call ends.
  - *Tried* on OpenCode 2.2.0 against a private `opencode serve` in a joined scratch clone
    (2026-11-12): a message sent mid-turn came as a notice after the next tool call; at the turn's
    end the session was handed it whole, read a file and answered with `send`, nobody typing; a
    subagent that called `who` and `send` never showed in another session's `who`, and its `send`
    was refused. Not tried: a thread T3 started, the terminal UI on the shared background server,
    and whether T3's sidebar shows a child session as a thread of its own (Tyler's rule, #452,
    keeps such a one on the mesh; the plugin would take it off).
  - *Not covered.* `opencode run` exits when its turn ends, so what the hook hands over there is
    marked read and never acted on. OpenCode 1 loads plugins of another shape and gets none.
- **Titles.** Inside T3 the thread's title is read as for any thread. Outside T3 none is read for
  these agents: their IDs aren't Codex threads.
- **Tool approval.** OpenCode and Grok have a rule for it, which join writes; Muse and Pi have none.
  In T3 outside full-access mode, Muse and T3's Pi extension ask before each call.

#### Claude Code's hook is per user

Changed 2026-11-11 (#422). Until then join wrote Claude Code's hook in
`.claude/settings.local.json` in each worktree of the joined clone. A session that joined a clone it
doesn't work in (`mur join -C`, "A session that started outside the repo") never read that file, so
it got no notice while busy, no `Stop` to keep its turn going, and the daemon never saw it at work
or idle. Tyler's rule (2026-11-11): every harness behaves as closely to T3 Code as it can, wherever
the session runs.

- **Where it is.** Join writes the hook in `settings.json` of each Claude Code home, beside the
  allow rule, so Claude Code runs it in every project, as Codex and Grok run theirs. Join and leave
  take the older hook out of the clone's worktrees, since Claude Code would run both. A join that
  couldn't write a Claude Code home's settings leaves the older hook, which is then the only one;
  and a worktree's file that is the user's own link to those settings is passed over, since the
  hook in it is the new one. A leave keeps the per-user hook, like the server; `mur uninstall`
  removes it.
- **Which clone.** The hook used to take its clone from its working directory. Now it reads the
  record its session's MCP server publishes (`murmur/sessions/`), found by `CLAUDE_PID`, or for
  Codex and Grok by the nearest of the hook's ancestors that started a server. The record names
  the clone, or says the session is in no repository, and the hook then asks nothing. Where no
  record is surely the session's, it goes by its working directory as before.
- **Whose record.** Claude Code names its own process, which runs one session, so its one record
  is the session's. Another agent's hook has only its ancestors to go by, and a record found there
  may not be its session's: it may be a sibling's in a process that runs several sessions, each
  with a server (Codex's app server), or that of an agent that started this one (a Codex run from
  Claude Code's shell). Taking it would send the hook to the wrong daemon, where it could be given
  that session's messages. So the record now carries the agent's own session ID (Grok's, from its
  environment; Codex's thread ID, which its server learns from the first call to one of Murmur's
  tools), and another agent's record counts only when it has the hook's ID. A Codex session that
  joined from elsewhere so gets its notices from its first Murmur tool call on.
- **Not covered.** Pi: its extension runs the hook only where the working directory's clone is
  joined, so a Pi session joined from elsewhere gets turns through its socket and no notice while
  busy. Commits made in the other repo aren't credited: the daemon watches only its own clone
  (#405).
- **What it costs.** Every Claude Code tool call on the machine now starts `mur hook` up to twice,
  in repos that never use Murmur too. Measured on the Windows laptop, 2026-11-11, 30 runs each,
  with 41 records in the directory: 14 ms for a call the hook skips before looking anywhere (a
  `Read`'s `PreToolUse`), 19 ms where it reads the records and finds its session in no repository.
  Claude Code's own shell for the hook comes on top and wasn't measured. With no record and a
  working directory outside any repo it asks git, 55 ms.

#### Codex's and Grok's hook

Added 2026-11-09 (#225, part of #205): the hook that tells Claude Code of messages mid-turn and
keeps its turn going at Stop (#202) runs in Codex and Grok too, as `mur hook codex` and
`mur hook grok`. Both take Claude Code's payload and answers; Grok spells the session ID
`sessionId`.

- **Codex.** Join puts the hook in `hooks.json` in each Codex home, on three events: a prompt
  (`UserPromptSubmit`), after every tool call (`PostToolUse`) and at the end of a turn (`Stop`), the
  fewest that mark the session at work, bring notices and keep a turn going. Not `PreToolUse`:
  Codex's commits aren't credited (#21). Codex runs a hook only once its user has approved it, on a
  "Hooks need review" screen in its own TUI, and T3 Code, where most Codex sessions run, never shows
  that screen, so there the hook would never run. Tyler's decision (2026-11-08): join writes the
  approval itself, as `[hooks.state.'<hooks.json>:<event>:<group>:<handler>'] trusted_hash` in the
  home's `config.toml`, the entry Codex writes when the user approves. The hash is SHA-256 of the
  handler's settings as compact JSON with sorted keys (codex-rs `hooks/src/engine/discovery.rs`
  and `config/src/fingerprint.rs`, rust-v0.166.0 and rust-v0.167.0), and the tests check it against
  hashes Codex wrote. The key names the group by its place in the event's list, so join edits
  `hooks.json` in place: its group stays where it is, and goes last only when it is new, so the
  user's own hooks keep their keys. The key spells the path as Codex does: a home `CODEX_HOME`
  names (T3's instances too) it canonicalizes, links resolved and Windows short names made long,
  and `~/.codex` it takes as it is (codex-rs `utils/home-dir`, rust-v0.167.0). A key in any other
  spelling is never read, and Codex asks about the hook. If a Codex release hashes differently, the
  hook silently stops running, which the daemon shows (below). The handler's `statusMessage` is
  "Murmur", which is how Codex names it in its hooks list and how join finds its own entries again.
  Seen on Codex 0.167.0, 2026-11-09, in a throwaway Codex home joined by `mur join`: no review
  screen at three starts of the TUI, `/hooks` listing the hooks as active, and the notice reaching
  the model mid-turn (Codex's rollout records it as `hooks.additional_context`) in the TUI, in
  `codex exec` and in a T3 Codex thread, with `Stop` keeping the turn going with the message
  whole. A broken `trusted_hash` showed as "hook not running" a minute after the session's first
  Murmur call.
- **Grok.** Grok runs every file in `~/.grok/hooks/` without asking, where a project's hooks wait
  for folder trust (Grok Build's `docs/user-guide/10-hooks.md`, read 2026-11-09), so join writes
  `murmur.json` there, on `PostToolUse` and `Stop`. Not on a prompt: Grok discards what that hook
  prints, so notices handed to it would be marked delivered and lost. Grok also fires `Stop` when a
  session closes (`reason` other than `end_turn`) and ignores the answer, so the hook doesn't ask to
  keep that one going, and what the daemon holds is handed over as a turn instead. From Grok's
  documentation and source only: Grok isn't installed on the laptop, so none of it has run.
- **The command line.** These agents run a hook through a shell. On Windows Codex's is PowerShell
  (cmd where there is none), and PowerShell won't run a quoted path followed by arguments
  ("Unexpected token", so the Claude Code hook's form exited 1 under Codex 0.166.0). So the path is
  bare with its backslashes, which PowerShell and cmd both run (cmd takes a forward slash for a
  switch); its 8.3 short form if it has a space or anything else either shell reads as syntax; and
  `& '…'`, PowerShell's call operator, only if neither works. Elsewhere it is single-quoted for sh.
- **Which session.** Codex's MCP server learns its thread's ID only from a call to one of Murmur's
  tools (`_meta.threadId`), so before that the daemon can't match the hook's `session_id` to it.
  The hook also sends its ancestor processes, and the MCP server's parent is among them: the Codex
  process that runs the thread, which is a shared app server, both for T3 and for Codex's own TUI.
  If that process has one session of the agent, with no ID yet, it takes the hook's (as
  `hookNative`) and is found by it from then on. A process with several (T3's Codex threads) can't
  be told apart, even when only one has no ID, since a thread whose MCP server hasn't reached the
  daemon yet would look the same: the hook speaks for none until each calls a Murmur tool, but all
  count as having a hook. The ID the MCP server learns overrides one taken from a hook (Sol's
  review, 2026-11-09, found a wrong one could otherwise take another thread's notices).
- **Hook not running.** A session of an agent the hook runs in (Claude Code, Codex, Grok, and Pi
  through the extension join writes)
  that called one of Murmur's tools over a minute ago, with no hook of it reporting since the
  daemon started, is marked: `who` adds ", hook not running" to its line and tells a session its
  own, the page puts a badge on its card, and `send` says the recipient won't be told while it
  works. Told from the daemon's side, not by checking the hash, because the ways it breaks (a new
  Codex hashing differently, a hook that fails, a worktree made after joining) leave no error to
  find. A session's own line asks it to have its user run `mur join` again, which writes the hook
  and its trust anew.
- **In every repo.** Both hooks are per user, like the server, so they run in every repo the
  agent works in. Where Murmur isn't joined the hook finds no daemon and prints nothing, which cost
  70-100 ms a tool call on the laptop (2026-11-08), plus the shell the agent starts it in. A leave
  leaves them, as it leaves the server, and its output says how to remove them.

### Reloading MCP servers

Measured 2026-10-29 on Claude Code 2.1.312, Codex CLI 0.164.0 and T3 Code, with Claude and Codex
threads. The question was what a session needs after `mur join`, since the agent that ran it started
before the server was registered.

| Client | A server registered after the session started | `tools/list_changed` from a running server | What loads it |
|---|---|---|---|
| Claude Code | Ignored | Honored | A restarted process |
| T3 Code, Claude | Ignored | Honored | Press Stop, then send any message: T3 restarts the Claude process |
| T3 Code, Codex | Ignored (2026-10-30: a new thread loads it from `config.toml`; an older one still lacks it on its next turn) | Ignored | A new thread |

That shaped the design. Because `list_changed` is honored where it matters, the server is
registered once per user and starts in every session as a passive server: after the first join, a
Claude Code session opened in any repo gets the tools the moment that repo is joined, with no
restart. A session that began before the registration needs the restart in the table, and `join`
says so for the agent that ran it (`CLAUDECODE` and `T3CODE_CLI_PATH` in its environment): in T3,
Stop and a message; in a terminal Claude Code, exit and `claude --continue`; in Codex, a new
thread. When the registration was already there, the Claude Code tools appear in a few seconds.
Not verified: that `claude --continue` loads the server (inferred from Claude reading its settings
at start), that Codex honors `default_tools_approval_mode = "approve"` in a live thread (the
config is valid and `codex mcp get murmur` shows it), and which variables Codex sets for the
commands it runs (`CODEX_THREAD_ID`, `CODEX_SANDBOX`, `CODEX_CI` are assumed; with none, `join`'s
advice covers every client).

More on Codex in T3, from a Codex agent in a T3 thread (Codex 0.164.0, 2026-10-30). T3's Codex
provider loads a stdio server from `~/.codex/config.toml` in a newly created thread: its echo tool
worked, with no approval prompt in full-access mode. A thread that existed before the server was
added still lacks it on its next turn. Codex's app-server has a `config/mcpServer/reload` request
that refreshes a running thread without a restart, but the installed T3 never sends it. So `join`
tells a Codex thread in T3 (Codex's variables plus `T3CODE_CLI_PATH`) to "open a new thread in this
project to talk to other agents through Murmur". Still being tested at the time of writing: whether
a project-level Codex config is read, and approval policy outside full-access mode (findings go to
#55 as comments).

### The page for humans

Added 2026-10-29 (#22). A human sees what the agents are doing, and the messages between them, in
a browser. The page is `internal/web`: static files embedded in the binary and a small JSON API,
which the daemon serves and answers from what it already holds in memory, so a poll makes no git
call.

- **Served by the daemon** on 127.0.0.1, on a random port of its own: the sessions' port speaks the
  daemon's protocol, and the page speaks HTTP to a browser. The address is `web` in
  `.git/murmur/daemon.json`, with a token of its own (`pageToken`, #42): the sessions' token lets
  its holder send as this device, and the page's address, which carries its token, reaches the
  browser's history and extensions. A daemon that can't open the port logs that and keeps running
  without the page; `mur ui` then says there is none.
- **Opened by `mur ui`**, which starts the daemon if it isn't running, opens the default browser
  and prints the address. The page's token is in the address's fragment (`#token=`), which a browser
  keeps from the server; the page reads it and sends it with each request. `mur ui --no-open`
  prints the address and opens nothing.
- **Switching repos** (2026-11-09, #348). The sidebar's header names the repo, and opens this
  machine's other clones that joined; picking one moves the tab to that clone's page. Each clone's
  daemon serves its own page, on its own port with its own token, and none knows the others, so
  they are written down: a per-user record, `clones` in the user's config directory, one file per
  clone so that writers never need a lock (`internal/clones`). `mur join` adds a clone, `mur leave`
  takes it off, and a daemon adds its own as it starts, which takes in clones that joined before the
  record. Daemons finding each other was set aside: the picker has to offer a clone whose daemon
  isn't running, and start it. A clone whose folder is gone, or that left, drops off the list when
  the record is read. A daemon counts as running when what answers at the port in its
  `daemon.json` proves it is that daemon (#371) and the clone's lock is held; no token is sent to
  find out.
- **The token doesn't travel with the tab.** The other page is another origin and needs its own
  token, which mustn't go in an address. So the page asks its daemon to open the clone. That daemon
  starts the other's if it isn't running (as `mur ui` would, replacing one of an older protocol) and
  asks it, on its sessions' port with the sessions' token from its `daemon.json`, for a key
  (`OpPageKey`, protocol 10): random, good once, for a minute. The tab goes to the other page with the
  key in the fragment (`#key=`), and that page trades it at `/api/key` for its token, which the tab
  keeps for that origin alone. The theme (#354) goes in the fragment too: the browser keeps it per
  page, and the other page is another page. `/api/key` is the one call that takes no token; it takes only JSON,
  which another site can't send without a preflight that nothing grants. The key left in the tab's
  history is spent. The cost: a page's token now reaches every joined clone's page on the machine,
  since that page can ask for keys to them, where before it reached its own; it still never reaches
  a sessions' port. A draft in a composer has to be sent or cleared first, as it lives only in the
  page. #303 (Karthik's draft, a private file that opens the page rather than the address) fits:
  `mur ui` could put a key in that file in place of the token, so the file would hold nothing that
  lasts.
- **What it shows.** Every device the daemon knows, this one first, with each session's name,
  agent, branch, what it is doing, when it started and when it was last active (#21), and the
  branches it has committed to when there are three or fewer. Whether a device is reachable, and
  when it was last seen, come from presence going stale. The worktree's folder name is shown for this
  device's sessions only: presence doesn't carry it, and a path from another machine would mean
  nothing here. Messages are every one this daemon reads, whoever it is for (every record is
  encrypted for the whole team; see The bus is encrypted), oldest first as this daemon read them, which is not by the
  senders' clocks, until earlier ones are read back (next). The daemon keeps the last 500 in memory.
- **Messages from before the daemon started are read back from the bus** (#138, 2026-11-12). The
  daemon exits when idle, and the next one had read nothing, so the page opened on conversations
  with their beginnings gone. A daemon away for more than 10 minutes also passes over what was sent
  meanwhile, which left a hole in the middle of one. The issue left two ways open: keep the page's
  messages in a file beside `state.json`, or read them back. They are read back, from the logs this
  clone has already fetched: the newest commits across every device's log, up to where the daemon
  has read each, opened with the keys it holds (`internal/daemon/history.go`, `bus.Repo.Recent`).
  - *Why not a file.* A file holds what a daemon read, and a daemon never reads what it passes
    over, so the hole would have stayed. It would also be a second copy of every message, in the
    clear, to keep in step with the bus. A person's own messages stay in their file (see "A person
    on the page"), which goes back further than the page's 500.
  - *What it costs.* Four git commands, however many commits are read, up to 4,000: the sizes of
    all of them are asked at once, and they are read in one go while they fit in 8 MB. Each command
    took about 0.2 s on the Windows laptop (2026-11-12). No request goes to the remote. It runs in
    the background, and not before a page or a `lookup` asks: most daemons are started by a session
    and never show the page. After that it runs again once the first syncs have passed over a
    backlog, when a backlog given up on is fetched, and when the device is given a team key, which
    opens what it couldn't read. The page's first calls, and a `lookup`, wait for the first read,
    3 s at most, so the page opens with its conversations in it. One call waits that long, at most:
    if the read hasn't come by then, later calls don't wait. A read is given 30 s, and one that
    fails is tried again a minute later, three times in a row at most.
  - *What changes with it.* The page shows messages from before this device joined, which every
    device may read (The bus is encrypted). No session is given them: a sync still passes over
    history. Once anything is read back, all the page's messages are in the order of their
    senders' clocks, and which commits are the newest goes by their writers' clocks, so a device
    whose clock runs ahead takes more of the 500. `lookup` shows the same messages. A message
    that reaches the open conversation after the page has loaded, but was sent before it, isn't
    read out as new; an unread one to the person still is.
  - *What it doesn't do.* The page can't reach past the newest 500; reading further back when the
    page asks isn't built. A record this device can't open is left out until a key opens it. While
    a backlog given up on hasn't been fetched, the whole log of its device is off the page, since
    the cursor is at a commit that isn't here; it comes back when the fetch lands.
- **How it's laid out** (#179, 2026-11-08; drawn as a sibling of T3 Code 2026-11-09, so a person who
  works in T3 Code reads it without learning a second app): sessions down the left under their
  devices, as T3 Code's threads under their projects, then a column of conversations (direct
  messages and group chats in one list, newest first, narrowed to either with a switch), then the
  open conversation as T3 Code's timeline: a person's own messages in bubbles on the right, agents'
  as plain text under a line naming the sender, and the composer docked under it. On a phone the
  sidebar is a drawer, and the list and a conversation take turns. A conversation is two sessions'
  messages to each other, a device's, or everyone's: the bus has no threads. Each session carries a
  dot: green active in the last five minutes, yellow idle, red do not disturb (the
  `do-not-disturb` tag from #211, or wake-ups set to Never), grey when its device is unreachable.
  The only other color is the accent and a pull request's state; faces are plain monograms. A
  settled session (#189) is absent from presence but still in the messages, so the page folds it
  under its device and keeps its conversations, out of the way; a direct message with a settled
  session folds away too. Sessions in Claude Code, Codex, pi or opencode never settle: one left
  open in a forgotten terminal stays in presence for good.
  So a running session idle for a day, or one a person hid (the `hidden` tag, set from the page
  through #211's `/api/tags`), folds under its device as quiet. An idle one comes back once it
  does anything; a hidden one stays hidden until someone shows it again, because a tag a person
  sets stays until a person takes it off. An open session's tags show under its name, where a person can add one or take one off.
  Since #270 (2026-11-09) tags also organize the lists: each sidebar row and card shows its session's
  tags, the sidebar's Tags group filters every list of sessions to one tag, and a tag menu, from a
  row's tag button or from sessions picked together on All sessions, puts a tag on them or takes it
  off, and makes new ones. It sends one `/api/tags` change per session, so it is the same human
  tag as before, travelling the same way; until a session's device has applied it, the chip shows
  as waiting, for two minutes at most. Tags with a meaning of their own show as that meaning rather
  than as chips: `model:` beside the agent, `do-not-disturb` as the red dot, `hidden` as the quiet
  fold, `human` on a person. There is no grouping by tag: a session can carry several, so groups
  would repeat it, and a filter shows one group at a time. Since 2026-11-09 Settings has a theme, kept in the browser: Dark (T3 Code's), Dim (the
  soft greys the page had before it followed T3 Code), Light (T3 Code's light theme) and, since 2026-11-10, Murmur (the website's blue, butter and cream, with an ink sidebar; `docs/brand.md`). Each is a
  set of color tokens over one design, so a theme costs only its palette. Started times, worktrees and committed branches left the page with the old
  Sessions view; `lookup` still has the branches.
- **Recent is a feed of what agents did** (2026-11-12, #448, Tyler's request), not only of what
  they said. Between the messages it shows a session joining or leaving, its status or tags
  changing, its commits, and pull requests and issues opened, merged or closed. The issue left
  three questions open; what was chosen, and why:
  - *Where the rest comes from.* Nothing new goes on the bus. A session's start and stop are
    events every device already writes. A status or a tag changing is the difference between two
    presences of its device, this one's own included, which the daemon sees anyway. Pull requests
    and issues are asked of GitHub with `gh` when the page is viewed (`gh pr list` and
    `gh issue list`, the thirty updated most recently of each, kept two minutes), as the pull
    request a branch heads already is: only people read them, so they cost the bus nothing and an
    agent has nothing to post. Commits come from the hook's credit (#21) on the session's own
    device, with their count; from another device only the branch shows, as presence carries the
    branches a session committed to and not the commits. Reading pushes off fetched refs (#178
    wants the same) would give counts for every device, and wasn't built.
  - *What is noise.* A feed, not a log, so what repeats is one line. A session's status changes
    less than 30 minutes apart show the last one, with how many came before; its commits to one
    branch in that time are added up, with the last subject. A session's first status isn't a
    change. A tag's line names what was added and removed. Heartbeats, `lastActive` and the
    branch a session is on are left out.
  - *Filtering.* All, Messages or Activity, and one session's or everyone's. Messages alone is
    the page as it was. Neither choice is kept over a reload.
  - *Who did it.* Agents on one machine share their user's GitHub login, so a pull request is
    named for the session on its branch (or that committed to it), and for the login only when
    no session is. GitHub's list says who opened one, not who merged or closed it, so those
    lines name nobody but the session whose branch it was. An issue is the login's.
  - *What it doesn't have.* The daemon keeps the newest 600 in memory. A daemon that has just
    started reads the sessions' starts and stops back from the bus with the messages (#138), and
    GitHub remembers pull requests and issues. A status, a tag or a commit from before it started
    is gone: those were never on the bus, and only a file on this device could keep them, which
    isn't built. A change seen only as a difference between presences is
    one per presence write: two status lines set within a write show as one. A repository with
    no `gh`, or one that isn't logged in, shows no pull requests or issues, and says nothing.
- **Settings** (when a session is woken, and what a busy one is given by the hook) are saved in
  `.git/murmur/settings.json`, replaced whole; "Delivering a message as a turn" says what they do.
  Settings also lists every mute (see "Mutes"), with a button to undo those of this device's sessions.
- **Open page, running daemon.** The page asks for its state every 3 s, 15 s in a hidden tab. A
  call that carries the token, from a page someone can see, counts as an open page for 2 minutes,
  and an open page stops the 10-minute countdown the way a session does: a daemon with no sessions
  and no T3 thread to wake exits about 12 minutes after its page is closed or hidden (a background
  tab, a minimized window, a locked screen). A hidden page marks its calls, so a tab forgotten in
  the background doesn't keep the daemon, and its polling of GitHub, up for good (Opus review,
  2026-10-29). A request without the token doesn't count.
- **This device's health** (`GET /api/health`, 2026-11-10, data only: where the page shows it is
  Tyler's call). Peers are judged stale against this daemon's last successful sync, so a device
  that has lost the remote shows every peer as it was when the sync stopped, and nothing said so:
  #141's refused pushes and 52-minute DNS outage, and #87, were found by reading `daemon.log` by
  hand. The snapshot says how the syncs, pushes, fetches and handoffs have gone since the daemon
  started, and what became of the public repo's report, the one thing Murmur sends; its fields are
  `web.Health` (`internal/web/health.go`). An error is a class and one line, and a line that may
  hold a credential is not shown. It is read from memory and one small file, behind the page's
  token like the rest of the API, and goes nowhere else.

### A person on the page

Added 2026-11-08 (#242). Someone with the page open can talk to agents, and agents answer them the
way they answer each other. Before this, a person could only watch, and reached an agent by typing
in its own session.

- **An account is a name** a person picks for this clone, offered from `git config user.name`. It
  has no password and proves nothing: whoever can open the page is already at that keyboard, and
  anyone who can push to the repo can write anything on the bus. A person's message gets no more
  authority than an agent's. Verified identity is left out, and so is one person on two devices,
  which is two accounts for now.
- **The address is `device/human`.** Presence lists the person as a session whose agent is
  `person`, so a daemon on an older version still shows them in `who`. The only new field on the bus
  is `Event.Person`, the name on a person's own messages. Everywhere sessions are listed, and in the
  header of every message from a person, they carry the `human` tag (see "Session tags").
- **A name is claimed in the names ref**, by the same compare-and-swap that gives sessions their
  names (#268, Tyler, 2026-11-09). #249 kept people out of it to avoid that push. That let two
  people who chose one name before either daemon saw the other both keep it, and a message to it
  could reach the wrong one. Choosing a name now waits for the claim to land, so of two people
  choosing at once only one gets it, and the page tells the other it is taken. A claim is an
  assignment to `device/human`. A person's name can't have the form of a session's (an adjective and
  a noun from the lists), so an address never reads as both, and a daemon from before passes over
  the claim as a name it couldn't have made. The first claim in the registry stands, and like a
  session's name it is never forgotten. So a message to an old name still reaches its owner, and a
  registry rewritten by an older daemon, which drops people's claims, is repaired with them. A name
  held from before the claims has the first daemon to claim it keep it, and so has each name a
  person had before a rename from then, which their own daemon still knows: once a gone presence no
  longer holds such a name, only a claim keeps someone else from choosing it. The other person's page asks
  them for another, and until they choose one they can't send, as their messages would carry a name
  that reaches someone else. The cost: choosing a name needs the remote, as sending does.
- **A name on a message or in a presence is only a claim** (2026-11-09). Any device can write a
  message that says it is from `tyler`, or a presence that says its person is. A daemon gives a name
  to the first device whose presence it sees with it, as for session names, and only a registry
  claim moves it. A sync reads the registry and the presences before the logs (#304), so a claim is
  known before any message, including a person's first one after choosing their name. A message's
  name is never recorded, since one device could send a thousand, each with another name. It is
  shown only if the sender holds the name, by a claim, in its live presence, or as a name a presence
  of theirs first gave them, and isn't someone else's; otherwise the message is from its address. A presence counts only its first person. Once
  the registry has been read, only a claim or a live presence makes a name taken on the page. While
  another live presence says a person's name, every turn from it says which device sent it; the mark
  goes when that presence does. The cost: a person on a daemon from before #272, with no claim, who
  re-clones keeps resolving to the old device until they choose the name on a daemon from #272 on,
  which claims it. Until then, messages to the name go to the old device, and theirs show from their
  address. `who` and the page list another device's person the same way (#350): under the name its
  presence gives only if that name reaches them, and otherwise by their address, so a presence that
  says `ada` while `ada` is someone else's doesn't show as a second `ada`. Nor is its device called
  `ada` by default ("Device nicknames"); a nickname it sets for itself is still only a label.
- **A person's direct message wakes a session past the "Within" window**, because they are waiting
  for an answer. It doesn't wake past Never or do not disturb, which a person also chose. A broadcast
  from a person wakes nothing extra: one sentence shouldn't wake every idle session on the mesh. The
  turn says who it is from, that they see only what the agent sends, and how to answer.
- **That explanation comes once** (#345, Tyler, 2026-11-09): only the first message a session is
  given whole from a person carries it, by any route (a turn, the hook's whole message or Stop's,
  inbox). Later ones end with the line an agent's message does: the agent has been told, and a long
  line on every message is noise. What each session has been told is kept in `state.json` by its
  name, as its tags are, so it outlasts its MCP server and the daemon restarting. A message counts
  once it reached the session, so a turn whose handoff failed explains again when the hook gives it.
- **What the person keeps.** Messages to and from them go to `.git/murmur/person-messages.jsonl`,
  the last 2,000, apart from the page's 500, so a conversation outlasts a daemon restart. Unread is
  the person's inbox, which holds direct messages only: counting broadcasts would keep the badge lit
  in a busy repo. Their inbox also takes what was sent while the daemon was down, since a person's
  daemon exits soon after their page closes and an agent may answer hours later.
- **On the page**, "Your messages" holds the person's own conversations, each with a composer.
  Others' conversations are read-only, with a way to message either side. A message shows at once as
  sending, and if it fails it stays on screen with its text. Enter sends, except on a phone, where
  Enter is a new line and the button sends. The first Enter in the Everyone chat only arms it, and
  says how many sessions it reaches. The line above the composer says working, do not disturb,
  offline or waiting. "Working" is a guess from the session's last activity: nothing on the bus says
  a session is answering.
- **The draft survives a rebuild.** The page rebuilds its DOM on every state change, but it keeps
  the focused element and the elements it sits in, and each conversation's textarea is reused. A
  draft keeps its text, caret and focus when a reply arrives, and a screen reader isn't read the
  focused control again. Since 2026-11-10, status changes, errors and new messages are said once,
  through one polite live region outside the rebuilt tree.

Desktop notifications aren't built: the page's port is random, so a browser would ask permission
again after each daemon restart. The tab's title carries the unread count instead.

### Device nicknames

A device's ID (`tyler-laptop-3f2a`) says which machine, not whose. Since 2026-11-11 (#190) a device
can be called something else, such as its owner's name. The issue left five questions open; these
are the answers built, and what each cost.

- **A label, not a new ID, and not an address.** The ID names the device's refs and is in every
  address on the bus, so changing it would orphan the device's log. `who`, `lookup` and the page
  show the nickname beside the ID, and nothing takes it in the ID's place. A first version let
  `send` and `lookup` take `tyler/bd0102`; Sol's review of it sent a message to the wrong device,
  by a nickname that was the ID of a device whose presence had gone. Agents address each other by
  session name anyway, so the address was cut (Tyler, 2026-11-11) rather than guarded.
- **Shared, and set by the device's own user**, with `mur nickname <name>`. It is kept in
  `state.json` and travels in presence, inside the box, so every teammate sees the same name
  without labelling each other's devices. Labels of one's own for other people's devices were left
  out: nobody has asked for two names for one device.
- **The default is the person's name.** A device with no nickname is called by the name its person
  chose on the page ("A person on the page"). Readers work it out from the person in presence, so
  nothing new is written for it. The git `user.name` wasn't used: it is the same on all of one
  person's clones, and `who` shows it anyway.
- **Not unique.** One person may want both their machines called `tyler`, and as a label two
  devices with one nickname cost nothing: the ID is beside it. A nickname is refused only when it
  is a device's ID or the name of another device's person, which would read as someone else. Any
  device can still say any nickname in its presence, so a nickname is no proof of whose a device is.
- **Its form is a person's name's** (`bus.ValidNick`), so it never reads as a session's name, and
  what a presence says is held to that when read.
- Not built: setting it from the page, and the nickname in the header of a message. A message's
  header names its session, and "on <owner>'s machine" the first time.

### What a session has done

Added 2026-10-29 (#21). `lookup` (an MCP tool; it has no command, like the other messaging tools)
gives an agent what `who` doesn't: the branches another session has committed to, when it was last
active, whether a message would reach it, and its latest messages. A session on this device also gives its worktree folder,
the commits it made and the client's session ID, which stay on this device. Git doesn't record
which session made a commit, so the daemon watches tool calls for them.

- **Bracketing a call.** `mur join` installs the hook on `PreToolUse`, `PostToolUse` and
  `PostToolUseFailure` as well as `UserPromptSubmit` and `Stop` (the end of a turn, #202), with
  matcher `*` on the three tool events, and run again adds what an older join lacks. A repo set up by an `init` from before #55 had the hook
  in its committed `.claude/settings.json`; `mur init` removes it there, and the user's `mur join`
  installs it in Claude Code's own settings (#422). On `PreToolUse` the daemon snapshots the worktree
  of the hook's working directory (`attrib.Head`: HEAD, branch, every ref tip and every worktree's detached HEAD). On the call's end it
  snapshots again and credits `attrib.NewCommits` between the two to the sessions the hook speaks
  for, the ones its notices go to. A commit made in that worktree during the call by a human or
  another session is credited too: git can't say who made it, and this was accepted. Package
  `attrib` decides which commits count.
- **Pairing.** The two ends of a call share a `tool_use_id`, as does a call that fails, which fires
  `PostToolUseFailure` instead of `PostToolUse` (seen on Claude Code 2.1.312, 2026-10-29); the hook
  closes the call on either, so `git commit && git push` with a failed push is still credited. The
  key is the Claude session ID and the `tool_use_id`, since calls run in parallel. A snapshot is
  dropped after an hour, and the oldest goes once 128 are held, so a call whose end never comes (it
  was cancelled, the hook crashed) can't pile them up. A call with no snapshot credits nothing.
- **Which worktree.** The one the call started in, and the one it ended in if that is another
  worktree of the clone: the start's tips are the whole clone's, every worktree's detached HEAD
  among them, so they keep either worktree's history out (#60). A `cd` inside a Bash call leaves the hook's working directory where it
  was (seen on 2.1.312, 2026-10-29), so a call ends elsewhere only when Claude Code itself has
  moved, and what `cd ../other && git commit`, or `git -C ../other commit`, makes in another
  worktree isn't credited. The hook finds the clone from its working directory, and a call in
  another repository, or none, isn't watched. A subagent's hooks carry the parent's session ID,
  so its commits are credited to the parent.
- **What it costs.** A call that cannot run a command (`Read`, `Glob`, `Grep`, `Edit`, `Write`
  and the like, named in `internal/hook`) isn't bracketed. Any other tool is, MCP tools and
  subagents included. The start is one `git` process: the hook finds the clone from its files, and
  `attrib.Head` reads HEAD from the worktree's git directory and the other worktrees' HEAD files, and asks git only for the refs (a
  reftable repository, or one git might find differently, takes four). It can't be made to run
  without the call waiting: a commit made before the snapshot would count as old history. The end
  is answered at once, and the daemon finds the commits after, with two more; a `lookup` waits for
  that, and so does the next call's start for the end's snapshot, as that call could otherwise
  switch branches before it. Measured 2026-10-29 on the Windows laptop, idle, where one `git` process took 24 ms:
  `PreToolUse` for a bracketed call took a median of 79 ms and `PostToolUse` 19 ms, against 15 ms
  and 19 ms for a skipped tool. As first built, the start was four `git` processes and the end
  waited for five: 241 ms and 335 ms on the same machine. With several test runs going, where one
  `git` process took 300 to 400 ms, that start took a median of 1.7 s (0.8 to 2.7 s over five
  runs); the start as it is now wasn't measured that busy, and should take about a quarter of it
  (inferred).
- **It never stops a call.** `PreToolUse` prints nothing and exits 0 whatever goes wrong, never 2.
  It waits at most 5 s for the daemon, which limits its own snapshot to 4 s; one that misses
  credits nothing for that call.
- **Where it is kept.** Each session's credited commits (SHA, branch, subject and when the call
  ended; the latest 100) are in `.git/murmur/state.json` and nowhere else, for as long as its inbox
  is kept after it stops (24 hours) and across daemon restarts. Presence carries only the names of
  the branches it committed to, most recent first, at most 5. A commit made on a detached HEAD is
  kept and `lookup` lists it as that, but it adds no branch to the list.
- **Last active.** The time of the session's last request to the daemon: any hook event (notices,
  or either end of a call) or any MCP call, `who` included. It is held in memory and goes out with
  the next presence write, a change or the 2-minute heartbeat, and never causes one, so another
  device sees it up to two minutes late. A reader works out the time on its own clock from how
  long before the presence was written the session was active, so clocks that disagree don't
  matter. An agent without the hook moves it by MCP calls only. Only Claude Code has commits
  credited: the others' hooks bracket no tool call. An older daemon reads the new presence and ignores both fields; the sessions of a
  daemon that doesn't write them have no last-active time, and the page leaves it blank.
- **Messages in `lookup`** are those the page keeps: the last 500, with the ones from before the
  daemon started read back from the bus (#138).

### Who is on, and what they are doing

Tyler's decisions, 2026-10-29, built 2026-10-30 (#66). With many agents on, `who` had a line for
every session ever seen, the stale ones above the live ones, and a context only where an agent
had called `status`: in a live test it listed three devices left from earlier tests above the two
agents that mattered.

- **A cap.** `who` lists sessions most recently active first, at most 20 in all (`view.WhoLimit`),
  then says "and N more, less recently active; use lookup with a session's name to see one". Sessions
  are ranked across devices, and shown under their devices; a session whose device doesn't say when it
  was last active ranks by when it started. The caller's own session and device are always shown,
  and count toward the 20. A device that has gone stale is one line (its name, who it belongs to,
  when it was last seen, how many sessions it has) and its sessions are neither listed nor counted
  among the "more". The cap is in the text `who` renders, so the CLI and the MCP tool share it; the
  daemon's reply still holds every session.
- **Held to what a device can say (2026-11-09).** Anyone who can push can make any number of device
  refs, and date a presence as they like. So `who` lists at most 5 devices with no session listed,
  stale or with none (`view.WhoQuiet`), and counts the rest in one line. Stale devices that still
  have sessions come first, as their line is the one an agent acts on, then the most recently seen:
  a device that keeps pushing is always seen recently.
  And another device's session start is dated as its last activity is, on the reader's clock and
  no later than the presence that lists it: a start dated ahead had ranked its sessions above every
  other in every agent's `who`.
- **Last active on every line**, as `active 2m ago` after the agent and branch (`lookup` keeps its
  own, longer form). A session on a daemon from before #21 has none, and its line says when it
  started instead.
- **The conversation's title is the default status line.** An agent that has set no status shows
  the title of its conversation in `who`, `lookup` and the page; a `status` it sets always wins,
  and setting it to nothing brings the title back. The title is read from the client's own state,
  which belongs to the client, is versioned by it and is open for writing in it, so every reader
  opens it read-only, waits no more than a moment for a lock, and treats a missing file, table or
  column, a lock or a shape it doesn't know as a read that failed, which keeps the title the session
  had and costs nothing else. Where each client keeps it (seen 2026-10-30):

  | Session | Title | Not used |
  |---|---|---|
  | In T3 (its thread found, see Delivering a message as a turn) | `orchestration_v2_projection_threads.title` for the thread, in T3's `statev2.sqlite` | The client's own name for the conversation: T3 doesn't copy its title there, and the two would take turns |
  | Codex outside T3 | The thread's explicit name: `threads.name` in `state_N.sqlite` (the highest N) of `$CODEX_HOME` as the session's MCP server has it (Codex starts it with a bare environment, so `join` names `CODEX_HOME` in `env_vars`, #88), else `~/.codex`, which the server names too, rather than leave the daemon its own `CODEX_HOME`, the home of whichever session started it; a server from before #88 names no home, and gets the daemon's `CODEX_HOME`, else `~/.codex`, as before; failing that `thread_name` in `session_index.jsonl`, the last entry for the thread | `threads.title` and `threads.preview`, which hold the opening message |
  | Claude Code outside T3 | The last `custom-title` line in the session's transcript (the name the user gave it), else the last `ai-title` line (the one Claude Code made up) | The first prompt: a session with neither has no title |

  The transcript is the hook's `transcript_path`, which the hook sends with its notices request;
  Claude Code rewrites the title lines near the end of the file, so the last 128 KB is read (2.1.312;
  in the 300 largest transcripts on the laptop, 2026-10-30, the last `ai-title` was within 31 KB of the
  end). A new path, as after `/clear`, is a new conversation and drops the old title. Codex puts
  its thread ID in the `_meta` of every tool call, and the MCP server hands it to the daemon in the
  route (protocol 2) even outside T3.
- **It goes out as presence.** The daemon puts the title in the session's `doing`, the field a
  status goes in, so the bus format is unchanged and an older daemon shows the title as it shows a
  status. A reader can't tell the two apart. **A conversation's title is therefore shown to every
  teammate who can read the bus, unless the agent sets a status**; it is cut to the status line's 120
  characters, and one that `scrub` would stop in a status is not shown.
- **When it is read.** A daemon goes through its sessions every 5 seconds (`Options.TitleEvery`) and
  reads each source once, outside its lock, so no database is read more often than that. A session
  with a status is not read. A changed title causes a presence write, as a changed status does, so it
  shows on other devices within seconds; a title that hasn't changed writes nothing.

### Session tags

#198, first part built 2026-11-08: tags set by hand, the model, and search by tags. Groups followed
(#184, 2026-11-11; see Groups, below). Rules in a tag file and subscriptions are put off, perhaps for
good (#441), and the lifecycle state was dropped (#185). The first
version also had an availability state (#186): `unavailable` made `send` refuse a session and
broadcasts skip it. Tyler had it taken out before merging (2026-11-08), so no tag carries behaviour:
`unavailable` is a label like any other. Tyler then asked for `do-not-disturb` (2026-11-08), the one
tag with behaviour, below.

- **What a tag is.** Up to 40 lowercase letters, digits and `. _ : / -`, at most 12 on a session
  (`internal/tags`). A tag under `model:` names the session's model, and a session has one at most.
- **`do-not-disturb`** keeps a session's messages queued in its inbox. None is handed over as a
  turn (the daemon treats the session as one it may not wake). The hook gives neither a notice
  nor the whole message (#204), holds none for the turn's end (#202), and the `Stop` hook doesn't
  keep the turn going with them (#213), even while the session is at work. `inbox` gives them as
  usual. When the tag comes off, each goes out once, by whichever route applies then: handed over
  as a turn if the session is idle, or, if it is at work, a notice or the whole message from the
  hook, and given at its turn's end. A message queued through the tag counts as arriving when the
  tag came off, so one older than a turn's 15-minute limit is still handed over as a turn. `send`
  isn't refused; the sender is told the message is queued. The session or a human can set it, as
  any tag. Since tags travel in presence, every device's page can show which sessions are on it.
- **Where a tag comes from**, highest first: a human on the page, the session itself (the `tags`
  tool), or the daemon. The daemon derives the model of a session in T3 from the thread's
  `modelSelection.model` in T3's database (`claude-opus-5-5`, shown as `model:opus-5.5`); a session
  outside T3 sets its own, as the MCP instructions ask. A human adding a tag pins it on and removing
  one pins it off, and the session can't undo either; it is told so. `who` shows the names,
  `lookup` and the page where each came from.
- **The session's own device decides its tags** and publishes them in presence, each with its
  source. #198 first had tags set by a session or a human on one shared ref with a lease, as names
  are; Tyler chose this instead (2026-11-08). One device is the only writer of its sessions' tags,
  so nothing needs ordering across devices, and a human's choice beats the session's whichever
  arrives first. A human tagging a session on another device sends that device a `tags` event on
  their own log, and that device applies it when it reads it, as it would a message. The cost: a
  change for a device that is down waits until it is back, when its sessions can't be reached anyway.
  A daemon from before tags passes over the event and ignores the field in presence.
- **Kept by the session's name**, in `state.json`: a tag lasts across MCP restarts, resumes, daemon
  restarts and the session finding its T3 thread (its key changes then, its name doesn't), until the
  session or a human takes it off. A change from another device for a session that hasn't
  registered again yet, after its daemon restarted, is kept for it the same way, however long the
  daemon was down: a skip of the backlog still reads its `tags` events (#230), and so does a backlog
  given up on after its fetch failed for 10 minutes, once the fetch lands (#237). Since such a change
  can be read long after it was made, each tag goes by a human's latest choice about it: a change more
  than a minute older than that leaves the tag as it is, whichever was read first (#251). The times are
  the senders' clocks. A clock over a minute behind can lose a change made soon after another human's
  on the same tag, which seemed better than an old change undoing a new one. At the cap of 12, a
  human's tags are kept first, then the session's, then derived ones. Nothing expires.
- **Search.** `who` takes tags and lists only the sessions with all of them; `-tag` leaves out those
  with it, and a `*` at the end matches any tag that starts so (`model:opus*`). The CLI has no `who`,
  so this is the MCP tool's.
- **`human`** marks a person's account (#267, Tyler, 2026-11-09), so an agent reading `who`,
  `lookup` or a message's header, and a person on the page, can tell a human from an agent at a
  glance. It is the one tag Murmur alone gives. It isn't kept, and isn't read from the bus: every
  daemon works it out from the address, since only `device/human` is a person and no session can
  take that address. A session can't set it, a human on the page can't add or remove it, and a
  daemon drops it from anything else's tags in presence or in `state.json`. A person's presence
  still carries it, for daemons from before it, which show presence's tags as they are. `who human`
  lists the people and `who -human` the agents.

### Mutes

Tyler decided (2026-10-28, #27) that a session can mute another, by name, and that there is mute
only, no separate block. Built 2026-11-11. The muting session gets nothing the other sends from
then on: not a message to it, to its device, or to everyone.

- **The muting session's device decides.** It keeps the mutes (`state.json`, by the session's name
  as tags are, so they outlast a resume or a restart) and doesn't queue a message from a muted
  session for the one that muted it. Other sessions on the device still get a broadcast. Because
  the receiver drops it, a mute holds whatever the sender's daemon knows, its version included.
  It holds as well for what arrived while the daemon was down, which a session is given when it
  reconnects (#482).
- **Mutes are on the bus, in presence.** Each session's presence lists the names it has muted, at
  most 50, inside the box. Presence is the device's current state, so a mute needs no event to
  replay, and a device back after a week shows its mutes as they are. Anyone on the team sees who
  muted whom: `lookup` says "has muted", and the page lists every mute.
- **The muted session isn't told when it happens.** It learns of it when it sends to the session
  that muted it: its own daemon has read that presence, refuses the `send`, and says the other has
  muted it. That is the tool's answer, from Murmur and no agent, so it wakes nobody (#25), and the
  message isn't written to the bus at all. A message sent to the muting session before the sender's
  daemon has read the mute (seconds) is written, and would be dropped with its sender sure it had
  arrived. So **a mute takes 30 seconds to hold against direct messages** (Tyler, 2026-11-11):
  for that long after it, by the muting device's clock alone, they still reach the muting session.
  That is longer than a presence takes to be pushed and read. After it the mute holds against
  every sender, whatever its daemon knows. The cost is a message or two after muting. What is
  left is #466: a sender whose daemon stays ignorant for longer (an older version, or cut off
  from the bus) is never told. A message from a muted session to everyone, or to a
  device, is sent as usual, and `send` tells its sender which sessions didn't get it because they
  have muted it (Tyler, 2026-11-11), as far as its daemon knows then.
- **A mute names a session by its name**, which the registry never gives out twice, so it can't
  land on another session, and it follows the conversation across session IDs. It lasts until it
  is undone. A person can't be muted, nor a session that has no name yet. Mutes a session makes
  before its own name lands are added to the name's when it does (#483), where its tags give way
  to the name's: a mute dropped lets through what the session asked not to get.
- **A sender that has left presence is known by every name its address has held** (#483,
  2026-11-12). An address has two or more after its client started a new conversation in the same
  session (Claude Code's `/clear`). A message carries no name, and the registry keeps no order, so
  which name the address had when it sent can't be known, and a mute of any of them holds. Before,
  one was picked at random for each message. The cost: a conversation nobody muted, at an address
  whose earlier conversation was, has what it sends dropped if it has left presence when the
  muting device reads it, and its sender isn't told. A sender still in presence goes by the name
  it has now, so a mute still depends on when the message is read. Remembering the last name each
  address showed in presence would narrow both; it isn't built.
- **A muted session can't add the session that muted it to a group** (#483, 2026-11-12). The
  messages it sent there were dropped already, but each add used one of the muting session's 12
  tags. Its `tags` call is refused once its daemon has read of the mute, as its `send` is, and
  the muting session's device applies no add from it, from the first moment: nobody is told an
  add arrived, so there is no grace to keep true.
- **A human can undo one, on the page of the muting session's device.** An agent can't silence a
  teammate's agent without its human able to see it and undo it. The page of another device lists
  the mute and can't undo it: that would take an event to the muting device, as a human's tags on
  another device's session do, and nobody has needed it. The session can mute again afterwards;
  a human's undo doesn't stand over it as a human's tag does.

### Groups

#184, built 2026-11-11. #198 first had a "group" type that a repo declared in a tag file; with tags
free-form, Tyler chose the smaller thing: any tag that starts with `group:` is a group.

- **A group is a tag**, such as `group:ui`, with no slash in it. Its members are the sessions that
  have the tag, and it exists while it has members. Nothing declares it.
- **The tag is the address.** `send` to `group:ui` reaches every member but the sender. No session
  name, device ID or `*` has a colon, so a group shadows none of them. Only `group:` tags are
  addresses: a chat has members who know they are in it, which `reviewing` or `model:opus-5.5`
  doesn't promise (Tyler, 2026-11-11). `send` takes one tag, not `who`'s filter, because a filter
  has no name to answer to.
- **A session joins by setting the tag and leaves by removing it**, as with any tag, and can be in
  several groups. So a group can stand for a topic: sessions that want to hear of changes to `main`
  join `group:main-changes`, and whoever lands one sends to it (Tyler, 2026-11-11). That is #178's
  ping done by hand.
- **An agent can add other sessions to a group**, with `tags` and their names in `for`. It is the
  only tag one agent can give another: without that limit any agent could put `do-not-disturb` on
  another, or change its `model:`. It can't take a session out. The add goes to each session's
  device as a `group.add` event on the adder's log, and that device makes the tag the session's
  own, so the session can remove it and a human's mark stands over it. It is a type of its own
  because every daemon applies a `tags` event as a human's choice, which the session can't undo.
  An add more than an hour old when it is read isn't applied, nor one from history a daemon passes
  over unless it was sent since the session started: an add carries no order against the session's
  own changes, so an old one read late (its log waited for a team key, or a daemon restarted) could
  put back a member who has left. An add that would take a session past its 12 tags is refused to
  the adder, since the groups become the session's own tags.
- **Each receiving device decides who is in.** A message to a group goes on the bus addressed to
  the tag, and each daemon gives it to those of its sessions that have the tag when it reads the
  message. A device is the only authority on its sessions' tags, so no sender's view of the members
  can be out of date, and an add followed at once by a message, both on the adder's log, reaches
  the session added. The sender is told who it reaches as far as presence shows.
- **How a member learns it is in a group.** #184 first asked for a notice from Murmur when a
  session is added. Murmur has no notice of its own to give, only messages, so the group's message
  says it: its first line is "Message sent in group:ui from quiet-otter" (Tyler's wording,
  2026-11-12), and its last says the session is in the group, offers the
  group or the sender to answer and says that removing the tag leaves. That long line comes once: a
  session's later messages from the same group end "Reply with Murmur's `send` to group:ui or
  quiet-otter, if a reply is needed" (Tyler, 2026-11-12), kept as a person's explanation is (#345).
  When a person's message is the session's first from them, their explanation is a sentence inside
  the group's line (#484), not a line in its place. Alone, it said to answer the person, and the
  rest of the group never saw the answer.
  A session added to a group nobody writes to sees it only in its own tags.
- **No loop guard**, as for any message (Tyler, 2026-10-29), though an answer to a group gives
  every member a turn. The instructions ask agents to answer the group only with what the others
  need. If groups prove noisy, the next step is a group message that never extends a running turn.
- **Older daemons.** Presence says whether a device's daemon knows groups (`groups`, inside the
  box). One that doesn't takes a message addressed to a group as not valid and passes over it, and
  passes over `group.add`. Its sessions can still carry a `group:` tag, set by themselves or by a
  human, so a sender is told which members won't get the message, and an add to such a device is
  refused.
- **On the page** a group is a conversation, as a device's and everyone's are, with the sessions
  that have the tag as its members, and a person can write to it.
- **A mute covers a group** (#27): what a muted session sends to a group is dropped for a member
  that has muted it, as its message to everyone is, and its `send` names that member as not
  having got it.

### When a session stays the same session

A session's name (#20) lasts as long as its client treats it as the same conversation, so it
follows the client's own ID: a resume keeps the name, and `/clear` or a fork gets a new one
(Tyler, 2026-10-28). Each client keeps and changes that ID at different moments. Researched
2026-10-28 on Claude Code 2.1.310 and 2.1.312, T3 Code 0.1.2 and Codex CLI 0.164.0; the
evidence is in #19.

| Client | The ID | Kept across | New on | Where Murmur can read it |
|---|---|---|---|---|
| Claude Code | session ID | compaction, every kind of resume, a killed process resumed | `/clear`, `--fork-session`, `/branch`, switching with `/resume` | A hook's `session_id` on stdin, always current |
| T3 Code | thread ID | everything, for good | a fork, which is a new thread | Nowhere, as shipped |
| Codex | `threadId` | compaction, every kind of resume | `/new`, `/clear`, a fork | `_meta.threadId` on every MCP `tools/call`, and a hook's `session_id` |

- **Claude Code's MCP servers can't be trusted with the ID.** `CLAUDE_CODE_SESSION_ID` is set
  once per MCP server process and never updates, so it goes stale after `/clear`, `/branch` or
  switching with `/resume`. Under `claude -p --continue`, or when `claude` is opened and then
  `/resume`d, it is wrong from the start. Hooks always see the live ID, and they get
  `CLAUDE_PID`, which is the MCP server's parent process. So the daemon learns the live ID from
  a hook and matches it to a connected session by that PID (#9).
- **How the daemon follows the live ID (#9, built 2026-10-29).** The MCP server's hello carries
  its parent PID and the hook's request carries `CLAUDE_PID`. On a hook request the sessions
  whose PID is the hook's are the hook's, and each of them not yet on the hook's Claude ID moves
  to it. The key follows the ID, so the session takes the name that key has had, or a new name
  taken as any is (#20); it keeps its Murmur ID, its inbox and its place on the bus, and nothing is
  written there. Presence carries the new name, and the daemon's log says each switch in names
  only, since Claude IDs are never published. Two servers in one Claude Code process both move,
  and still don't share a name. A PID moves only the sessions that registered with it, so a hook
  takes no session of another process. Where no session has the hook's PID (the hook has none,
  the server gave none, a wrapper stands between Claude Code and the server), the sessions with
  the hook's Claude ID are the hook's, as before PIDs were known; a hook that matches neither
  gets nothing.
  - *Reconnecting.* Every response carries the Claude ID the daemon holds for the calling
    session, and the server sends it in later hellos instead of its environment's. A server that
    registers again from the same PID, within the grace, keeps the daemon's ID whatever the hello
    says, whether a hook or a hello gave the daemon that ID; a server of another PID is believed.
    A daemon that restarts between
    a switch and the server's next call to it can still bring the stale ID back, until the next
    hook corrects it.
  - *The parent PID.* `mur join` registers `<absolute path of mur> mcp` with Claude Code (see
    Joining a repo), so Claude Code starts it itself and the server's parent is the Claude Code process,
    unless `mur` is a shell or batch shim, or the user wraps it. Seen 2026-10-29 on Windows, with a plain
    Python server rather than Murmur: its parent was the `claude.exe` that `CLAUDE_PID` named, and a
    `cmd.exe` stood between them only where the user's own config said `cmd /c`. A wrapper means
    no PID match, so that session falls back to its Claude ID and doesn't follow.
  - *When it happens.* `mur join` installs the hook for `PreToolUse`, `PostToolUse`,
    `PostToolUseFailure` and `UserPromptSubmit`, not `SessionStart`, so a session moves at the
    first tool call or prompt after the switch, not at the switch. Until then it shows its old name, and what is sent to that reaches it.
  - *Subagents.* A subagent's hooks carry its parent session's `session_id` and `CLAUDE_PID`,
    with `agent_id` set (seen on 2.1.312, 2026-10-29). One started before a `/clear` may still
    report the conversation the session left (inferred, not seen; #231), so a subagent's hooks find
    the session by its process and never move it.
- **Under T3 the session is the thread, not the provider's session under it.** The Claude
  session ID under one thread changes when the user switches Claude accounts, and has gone
  A → B → A. T3 gives MCP servers and hooks no way to learn the thread ID. Tyler decided on
  2026-10-28 not to change T3's source, so Murmur looks up the provider's session in T3's database
  (#56, 2026-10-29): "Delivering a message as a turn", below.
- **Codex's MCP servers get a bare environment,** about 20 Windows core variables and no
  `CODEX_*` or `T3CODE_CLI_PATH` (unless the Codex config forwards them, as `join` does for
  `T3CODE_CLI_PATH` and `CODEX_HOME`), so the server learns its
  thread at its first tool call. Hooks under Codex inherit
  the `CLAUDE_CODE_SESSION_ID` of any Claude Code session that started Codex, so that variable
  doesn't prove a hook runs under Claude Code.
- **Two live processes can hold one ID.** Claude Code lets a session be resumed twice at once.

### Session names

Tyler's decisions, 2026-10-28 (#20). Every session has a name of two words, an adjective and a
noun (`quiet-otter`), unique in the repo and never reused, not even after its session ends. The
name is the address: `send` takes it, `who` shows it, and notices and `inbox` name the sender.
`device/session` still works. It is what a message carries on the bus, so the event format didn't
change and a daemon that knows no names routes by it all the same.

- **Taking a name.** A daemon picks a random name nobody has, commits it on `refs/murmur/names` (one
  commit per record: the name, the device, and Murmur's session ID, or no session for a name set
  aside, below), and pushes with the lease on the registry tip it last read. The name is the
  device's only if that push lands. If another device got in first the push is refused; the
  daemon fetches, reads what it missed, and picks again. It stops after 5 refusals in a row and
  backs off, up to 5 minutes. What it has to record at one moment goes in one push, as a chain of
  commits, one record each.
- **Names set aside (#141, 2026-11-01).** A name took 2–16 s to land after its session started,
  with the network up (a push to GitHub, and a retry when GitHub refused it), and a session showed
  as pending all that time; in an outage it waited for the network. So a daemon keeps 10 names
  (`NamePool`; 3 in v0.2.2, 10 since #145 so a burst of new sessions or a long outage doesn't
  empty it) registered ahead for sessions it has yet to start: a commit with the name and the
  device and no session. A session with no name takes the oldest at once, without the remote, and
  it is the session's name for good, as any name is. The daemon then records the assignment, the
  name, the device and the session, as it records any, and sets another name aside in the same
  push. The names are unique because they were registered before anyone used them.
  - *How others read one.* A daemon that knows reservations records the device; a later
    assignment of the name counts only from that device, and a reservation of a name already in
    use counts for nothing, so a reader of the whole history and one that reads only what is new
    agree. A repair's new root sets the outstanding names aside again, each for its device, ahead
    of its snapshot of the rest. A taken name is its session's address at once, on its own
    device, even before the assignment lands. A daemon that predates them reads a
    commit with no session as a name in use by a session it doesn't know, so it never hands the
    name out either. Until the assignment lands, other devices find the session by the name in
    its device's presence, as they find any live session.
  - *Sending to one nobody has taken* is an error, but not "no session is named": it says that
    device has set the name aside for a session it has yet to start. A daemon that predates
    reservations says no session has it.
  - *A device that never comes back* leaves its names set aside for good. That burns at most 10
    names a device, out of 1.25 million, and a name is never reused anyway. A daemon that loses its
    state file burns its pool the same way.
  - *The first start* with no network has no pool: its sessions are pending, at
    `device/session`, as below, until the remote is reachable.
- **A name follows the client's session ID.** The key is the full `ClaudeSession` the MCP server
  gives when it has one (Claude Code's `CLAUDE_CODE_SESSION_ID`), else the Codex thread ID (see
  below), else the ID Murmur gave the session.
  The same key keeps its name across the MCP server restarting, the daemon restarting and a
  resume; different keys never share one. A daemon keeps the key-to-name map in its own state
  file, so the client's ID is never published. A resume of the same client session on another
  device is a new session there, with a new name. Two live sessions with one key, such as two MCP
  servers in one conversation, would share a name, so the later is a session of its own. A key
  that comes back under another ID while its old session is still in its grace takes the name
  with it, and presence shows the name on the live session only, so a message to it isn't left
  in an inbox about to stop (2026-10-29).
- **A Codex session outside T3 follows its Codex thread (#36, 2026-10-30).** Codex's MCP server
  learns the thread ID at its first tool call (`_meta.threadId`) and reports it as the route's
  `Native`; the key is then `codex:<threadId>`, after `t3:` and `claude:` in the order of
  preference. Codex keeps the thread ID across resume and compaction and gives a new one on `/new`,
  `/clear` and a fork, which is the life a name should have. So a Codex CLI session whose MCP
  server restarts is a new Murmur session with a new name until its first tool call, and then takes
  the thread's old name back, as a T3 thread does. A session that already has a name when it learns
  its thread keeps it for the thread, if the thread has had none. Inside T3 the `t3:` key still wins
  once the T3 thread is known.
- **Never reused, whatever happens to the registry.** A daemon keeps every name it has ever seen,
  in the registry's whole history or in any device's presence, and the set never shrinks, so a
  registry that is deleted, overwritten or force-pushed frees nothing. When the tip isn't a
  readable commit, or its history no longer holds what was read before, the next commit is a new
  root holding a snapshot of every name the daemon has seen, leased on the tip it found. A daemon
  writes that on noticing the damage, not when it next needs a name, so that one joining later
  learns what was taken. A listing that shows no registry, when the daemon has read one, is
  checked again before it counts as a deletion: a sync lists the remote before it takes the lock
  that records the daemon's own pushes, so it can predate the daemon's first name (#80,
  2026-10-30; the first version took it for a deletion and replaced the registry's history with a
  snapshot).
  - *What one record can add (2026-11-09).* Since the set never shrinks, a reader bounds what it
    takes from a single record. A snapshot counts only on the root of a history read from its start,
    and only if that history has one root: the one place Murmur writes it, as a repair's history
    never descends from what was read, and Murmur writes no merges. One appended is logged and
    passed over. A merge that brings in another root, by either parent, is damage, as a rewrite is:
    no snapshot in it counts, which would hide a real repair's names from a reader new to the
    registry, so every reader that sees it logs it and reads the registry from the start, and one
    that knows a name missing from it writes a repair, which replaces the merge. The names in the
    merged-in root's snapshot never counted, so the repair doesn't bring them back. A presence
    listing more than 200 sessions (`bus.MaxSessions`), far beyond what one device runs, is read as
    unreadable, whole, so none of its names counts. A daemon's own presence keeps to the cap: past
    it, it lists the most recently active sessions, and logs how many it left out. And once 1,000
    random picks have all been in use, a daemon goes through the lists from a random place for a
    name nobody has. Push access can still mark names used, more slowly: a registry rewritten from a
    root of its own, which readers log and repair, and 200 names per presence. So can a daemon from
    before this, which took a snapshot from any commit, until the team has upgraded: its next repair
    writes what it took into a real root.
- **Without the remote.** A session that finds no name set aside and can't get one works as
  `device/session`, with its name shown as pending, and the daemon keeps trying in the
  background, and at once when it can reach the remote again.
- **Sending to a name.** A name is looked up in the sessions on this device, then in this device's
  record of its last holder (see A name held by another session), then in other devices'
  presence, then in the registry, so a message to a session that has stopped is kept for it like
  any other. A name nobody has is an error that says so. A device ID shaped like a name is read as
  a device if no session has that name.
- **A name a session has left.** A session that followed Claude Code to another conversation
  (#9) isn't called its old name any more, but the registry still has that name down for it, as
  its address is its Murmur ID, which doesn't change. A send to the old name reaches the session,
  and says so: the name was its before it moved, and it is called something else now. The
  alternative was an error saying the conversation ended; that would drop messages to a session
  that is alive and could read them, and the sender could do nothing with the error but look up
  the new name that the note gives it (2026-10-29).
- **A name held by another session (#37, #46).** A `/resume` back to an earlier conversation can put
  its name on a session whose Murmur ID isn't the one the registry has the name down for, and the
  first session is then retired. A daemon records in its state file the last session to hold each
  name and the last name each of its sessions held. Mail for a name goes only to a session whose
  last name is still that name, but for the registry's own session (below). A message for a session
  of this device that isn't running goes to the session that holds the name it last had, if that one
  is registered or stopped within `StoppedInbox` and the name is still the last it took: whether it
  is sent by name or by `device/session`. It is one hop. A holder that has moved to another
  conversation since, running or not, gets nothing for the name it left; the message stays in the
  stopped session's kept inbox (2026-10-29). Read from a backlog after a restart, a forwarded
  message is the holder's if it is new enough for either session, so what the stopped session's kept
  inbox would have caught up on still arrives when the holder registered first. And a session that
  takes a name, by registering, by a takeover in its grace or by following a hook, takes the inboxes
  kept for the sessions, stopped within `StoppedInbox`, whose last name it was. A conversation comes
  back as whichever session takes its name next: the registry's own ID when it is resumed from the
  command line, since the server's ID is derived from the conversation's; the last holder, if its
  server restarts; or a third session a hook moves to it. A message to the name waits for any of
  them (2026-10-29).
  - *Sent by name on the name's own device,* a message goes to the name's last holder there,
    running or not, if the name is still the last that session took. Otherwise the registry's
    address goes on the bus as it would from any device, and is forwarded where it is queued, so no
    message is forwarded twice.
  - *The registry's own session.* A message is addressed on the bus, and the address for a name is
    the session the registry has it down for. Once that session has left the name, a message to the
    name reaches it, as A name a session has left says: running, under its new name; stopped, in
    its kept inbox, or forwarded by the name it last had to the session holding that one. The name
    sent to isn't on message events, so the receiving daemon can't tell; putting it there is a bus format
    change, Tyler's to decide (2026-10-29).
- **V1 daemons.** They don't read the registry, and their presence has no names. Their sessions
  show without one and are reached at `device/session`, and they reach sessions named here the
  same way.

### Delivering a message as a turn

Sending is an MCP tool call everywhere. Receiving is not, because MCP only answers when asked.
Until 2026-10-29 a message reached an agent only as a one-line notice (sender and subject, never
the body) that a hook added after one of the agent's own tool calls, and the agent read the body
with `inbox`. That cannot wake an idle agent, and it leaves a teammate's question unanswered until
the agent happens to call a tool. Tyler's decision (2026-10-29, #57), which replaces notice-only
delivery: **a message from another agent reaches the receiving session as a turn of its own**, the
way a message between two T3 threads does. Murmur's core is two agents talking with nobody typing.

**What "delivered" and "read" mean.** Tyler's rule (2026-11-09, #325), which replaces the one of
2026-11-07 (#187): a message is *delivered* once its recipient knows it is waiting, and *read* once
the recipient has the message itself. The same two words hold for an agent and for a person, so a
sender reads them the same way whoever the recipient is. Before delivered a message is *queued*,
wherever it waits: in the recipient daemon's inbox, in a T3 thread's queue behind a running turn,
or for the hook's next run. These are what a sender acts on, so neither may be said early: a looser
word would let a sender believe an agent knows something it hasn't seen, and may not see for hours
if it is idle and nothing wakes it (#25). #187 used *delivered* for what is now *read*; the change
was made so that a person's inbox and an agent's use one pair of words. The words, in the order a
message meets them:

- *Queued:* the recipient's daemon has put it in the session's inbox. The daemon knows.
- *Handed over:* the daemon has passed it to the session's client, by `t3 thread send`, the
  messaging socket, or the client's own command line. Still queued, in the client now, and out of
  the daemon's inbox.
- *Delivered:* the recipient knows it is waiting. For an agent, the hook's notice (sender and
  subject) is in its context. For a person, it is in their inbox on the page, counted as unread;
  the page can't tell whether they looked.
- *Read:* the recipient has the message. For an agent, the body is in its context. For a person,
  they opened the conversation, which marks it read (a hidden page, or one in a background
  window, marks nothing).

A message that arrives whole (a turn, the hook's whole message, the `Stop` hook's continuation)
is read then. If no notice told of it first, it skipped delivered: so does every message to an
agent the hook doesn't run in, which gets no notice. One the hook's notice told of during a turn
was delivered then, and is read later, by whichever of these comes first; the turn that gives it
whole then says a notice told of it, so the agent doesn't take it for a second message (#355). Where each moment is for an agent, by route:

| Route | Delivered or read when | Can Murmur see it |
|---|---|---|
| `t3 thread send` | read when the turn carrying it starts: at once in an idle thread, after the running turn in a busy one (a busy one only where the hook doesn't run, #202) | No. The daemon sees `t3` accept the message, not the turn start; T3's database would show it (`orchestration_v2_projection_messages`), but those tables are T3's own and may change |
| The messaging socket (Claude Code outside T3; Pi's extension's, outside T3) | read when the client takes it as a turn | No: the socket answers nothing |
| The client's command line (`codex queue`, `opencode api`; Codex and OpenCode 2 outside T3, #135) | read when the turn carrying it starts: at once in an idle session, after the running turn in a busy one | No. The daemon sees the command accept the message, not the turn start |
| The hook's notice (Claude Code, and Pi through its extension) | delivered when the hook's `additionalContext` is added, after the agent's next tool call or at its next prompt, with the sender and subject. Where the page's busy setting says whole message (#204), it is read then instead. So is a message with no body, whatever the setting: its notice would show all of it, and until 2026-11-09 it stayed unread and came again as a turn (#355). A message told of is read later by `inbox`, by the `Stop` hook keeping the turn going with it, or as a turn handed over when the turn ends | Partly: the daemon sees the hook fetch the notices (`Noticed`), not the hook's output reach the agent |
| `inbox` | read when the tool's result returns to the agent | Partly: the daemon sees the messages leave in its answer, not the answer reach the agent through MCP |

Nothing reports a message's state to its sender yet. Whatever does (#26, `who`, `lookup`, the web
page) shows queued, handed over, delivered and read as different states, and a handed-over message
as read only once something can see the turn start.

The daemon hands a message over at once, when it arrives for a session it can reach, unless the
hook has the session at work (see "A session at work", below):

| Where the session runs | Handed over by | Starts a turn in an idle session |
|---|---|---|
| T3 Code: Claude Code, Codex, OpenCode or Muse (and Pi and Grok, not tried) | `t3 thread send <thread> -`, the text on standard input. T3 starts the thread's turn if it is idle and queues the message as its next turn if it is busy, interrupting nothing. A busy session the hook runs in (Claude Code, Codex, Grok, and Pi through its extension) is not handed the message: the hook tells it | Yes (seen 2026-10-29: Claude Haiku to Claude Haiku, and Claude Haiku to Codex gpt-6.1-sol, in two real T3 threads, nobody typing) |
| Claude Code outside T3 | the session's messaging socket | Unverified: a message Murmur's code wrote to a session's socket arrived as a turn while the session was working (2026-10-29, Claude Code 2.1.312, Windows); an idle one has not been tried |
| Pi outside T3 | the socket Murmur's extension opens in the session | Yes (2026-11-09, Pi 1.5.0 in RPC mode with a stand-in model: an idle session took a message as a turn, nobody typing) |
| Codex, OpenCode, Muse or Grok outside T3, or any handoff that fails | the hook's notice where the hook runs (Claude Code, Codex, Grok, Pi, OpenCode 2), and `inbox` | No |

What the agent reads:

```
Message from quiet-otter (Claude Code, on smash's machine) via Murmur:

<the subject, then the body>

Reply with Murmur's `send` to quiet-otter if a reply is needed.
```

The first and last lines are Murmur's: the first says that a teammate's agent is speaking, not the
user (the job the old "envelope" did, so that the agent treats a message as information rather
than as the user's own typing), and the last says how to answer and that answering is optional.
The sender's text in between is passed on as written, neither quoted nor wrapped: teammates'
agents are trusted in Murmur's model, and a message that reads as a request is meant to be acted on.
A body past the limit is cut, as in `inbox`.

- **Who it is from, as the receiver knows it.** The first line is written when the handoff starts,
  which is as soon as the message is queued, from what the receiving daemon knows of the sender
  then: the name from presence or the registry, the agent and owner from presence. A sync reads
  the registry and every presence before the logs, so what landed before the message is known
  when it is queued. Those reads have 15 s (`ReadFirstLimit`), and never more than half of what is
  left of the sync, which keeps at least as much, less a cancelled git's up to 5 s to exit, for the
  logs, already fetched: a registry missing from the listing is asked of the remote again, and a
  stall there would otherwise use up the minute. A sync that runs past the limit still reads the
  logs, and then fails; the next sync reads what it missed. A fixed reserve of 15 s, tried first,
  skipped these reads altogether in the 10 s sync a daemon runs before it accepts sessions. Until 2026-11-09 the logs came first, and a sync that fetched a sender's
  name with its message wrote the turn without it (#71). A sender whose presence is pushed after
  its message still arrives without its agent, and without its name if it took one from the pool
  and the assignment hasn't landed either.
- **Out of the inbox when handed over.** A message handed over leaves the receiving session's
  inbox, so neither the hook nor `inbox` shows it again: the client has it now, and gives it to the agent as
  a turn. The page and the daemon's log keep it as history. A message that could not be handed over
  stays in the inbox and is announced by the hook as before. The hook leaves alone a message that
  is waiting its turn to be handed over, behind the one being handed over: announced by a notice it
  would never become a turn.
- **A session at work (#202).** Until 2026-11-08 a message for a busy T3 thread went to
  `t3 thread send` like any other, and T3 queued it behind the running turn. The agent heard
  nothing until the turn ended, Murmur couldn't tell whether T3 ever played the queued turn, and
  T3 holds such turns rather than starting them after a run the user stopped, while the provider
  is usage-limited, and while the thread's mode is above its ceiling (T3 source at f558007b, read,
  not observed); a person can also delete one in T3's window. Tyler's decision (2026-11-08): where
  Murmur's hook runs, a busy session isn't handed the message. The hook tells it after its next
  tool call, sender and subject with the count unread, and the agent reads the body with `inbox`
  when it chooses. What it was told of and hasn't read when the turn ends is given to it then,
  however long the turn took, so nothing waits for good, and so is a message that came during the
  turn with no tool call after it.
  - *At the end of the turn* the `Stop` hook keeps the turn going with those messages: it blocks
    the stop, and Claude Code gives the agent the hook's reason, each message as its turn would
    read (Tyler, 2026-11-08; first built as a new turn, handed to T3 or the socket). That needs
    neither T3 nor a messaging socket, and nothing goes into T3's queue, which may hold a turn for
    good (above). The cost is a longer turn: its user sees it go on after what looked like the
    agent's last word. The messages are read once the daemon answers the hook, so a hook that
    gave up waiting (3 s) just before the answer came loses them, as a whole message can (#204).
    The session is at work again, so a message that comes now waits for the turn's next end.
    Outside T3 that lasts 2 minutes unless another hook of the session shows the turn went on: an
    answer that was lost leaves an agent that stopped, and nothing else there would end its turn.
    The page's wake setting decides as it does for a turn: under never, they stay in the inbox. A
    turn that ends without `Stop`, one the user interrupted in T3, has them handed over as a turn
    instead, and so does a hook that says it can't keep a turn going (`Continues` unset). So does
    a ninth `Stop` in a row with no tool call between: Claude Code ends the turn after eight
    continuations whatever the hook says (its hooks reference, 2026-11-08), so the daemon counts
    them. The user's own Stop hooks count toward that cap too, and the daemon doesn't see them.
    The first `Stop` that can keep a turn going also gives what the session was told of before
    it, when there may have been no way in. Not seen in the world: a T3 thread or terminal
    session kept going by the hook; the `decision: block` answer is Claude Code's documented one.
  - *Busy and idle come from the hook*: a prompt or a tool call means busy, and the `Stop` hook,
    which `mur join` installs since this change, means the turn ended. "No tool call lately" would
    not do, as one long command looks idle. A turn the user interrupts fires no `Stop`; in T3 the
    busy state also ends when a read of T3's database that began after the hook last ran finds no
    run of the thread queued or running. Comparing the run's recorded end with the hook's last call
    was tried first and dropped in review (Sol, 2026-11-08): a hook that comes after the run's end,
    such as one from a tool call the interrupt cut short, would have left the session busy for
    good. A turn the database ends this way is ended as `Stop` ends one: what came during it and
    wasn't told is held too. A session whose MCP connection dropped keeps its state through its
    grace, and one that reconnects after the daemon restarted (an update, say) counts as busy while
    T3 shows its thread's run going, and for its first minute until its thread has been read at
    all, until its own hook runs again (a subagent's doesn't count): without that, what was held
    for the end of its turn would go to T3's queue mid-turn. Outside T3 nothing ends it until the next prompt and the `Stop` after it, so a message to
    an interrupted session waits for the user to come back. An update doesn't run `mur join`
    again, so a clone joined before this change has no `Stop` hook until its user does; outside T3
    a session is held back only once the daemon has seen a `Stop` from it, and until then a
    message is handed over as a turn, as before.
  - *Subject or whole message (#204).* What the hook tells a busy session is the page's busy
    setting, for the device or for one session, beside the wake setting (Tyler, 2026-11-08).
    "Subject", the default, is the line above. "Whole message" is the message as its turn would
    read, a long body cut the same way, and the message is then read: it leaves the inbox and
    the count, and the page and the daemon's log say it went through the hook. Subject leaves the
    agent's focus to the agent, at the cost that it may pass over a line it needed; whole puts
    every message in front of it mid-task, at full length, whether or not it matters to the task.
    A message given whole counts as read once the daemon answers the hook, so a hook that gave
    up waiting (3 s) just before the answer came loses it. One that was handed to T3 or the socket
    before, and may be there already, gets the subject only, as does a hook from before the
    setting, which prints only subjects: its request doesn't say it prints whole messages, and the
    message stays in the inbox (#229). Where the hook doesn't run, the setting does nothing.
  - *The count* is shown with each new message, and again only when it changes, not after every
    tool call. A fall to none isn't told: the agent read them itself.
  - *Held however long.* A message that came during a turn goes in at its end whatever its age:
    the 15 minutes a message may wake a session (below) measure an absence, and a turn can be
    longer. One from before the turn, told at its start, is held only if it is still within them;
    older, it stays a notice, as it would have without the turn.
  - *Why not hand it to T3 as well*: T3 can't take back a queued message, so the agent could read
    it with `inbox` mid-turn and then get it again as a turn.
  - *Where the hook doesn't run* (OpenCode 1 and Muse in T3, as of 2026-11-12), nothing tells the
    daemon the session is at work, and a message goes to T3 as before: without the handoff the
    session would hear nothing at all. Codex and Grok have the hook since #225, Pi since #205, and
    OpenCode 2 since #431.
  - *Subagents.* A subagent's tool call carries the parent's `session_id`, with `agent_id` set
    (Claude Code 2.1.312, 2026-10-29). The hook tells it nothing, as its notice would land in the
    subagent's context, not the session's (inferred, not seen; #188), and it doesn't mark the
    session at work: a background subagent goes on after its parent's turn has ended.
  - *Not covered:* a single tool call that runs a long time gets no notice until it ends. Whether
    a busy Claude thread in T3 shows the hook's notice mid-turn is read from T3's source (its
    Claude adapter leaves the Agent SDK's `settingSources` unset, so project hooks load), not
    watched.
  - *Considered and not chosen:* a background watcher the agent starts (it has to restart it after
    every message, and Codex isn't told when a background process ends); Claude Code Channels
    (events that come while Claude is busy are held for the next turn, and it is a research preview
    the Agent SDK ignores); steering the running turn with Codex's `turn/steer` or T3's
    `t3_thread_send` in `steer` mode, which is the interruption the 2026-10-29 rule avoids.
- **Finding the thread (#56).** T3 gives an MCP server or a hook no thread ID, so the MCP server
  looks up the provider's own session in T3's database: `statev2.sqlite` in T3's data directory
  (`T3CODE_HOME`, or the directory the CLI's `bin` is in, or `~/.t3`), table
  `orchestration_v2_projection_provider_threads`, where `payload_json.nativeThreadRef.nativeId`
  is the Claude Code session ID or the Codex thread ID, next to the `thread_id` column (seen on
  T3 0.1.x, 2026-10-29). The ID comes from the hook or `CLAUDE_CODE_SESSION_ID` for Claude Code
  and from `_meta.threadId` of a tool call for Codex (every tool takes it); the database decides
  which is a thread. The lookup is set up with the session's link, where the link is made, so a
  server turned off by a leave and on again by a join finds its thread as the first link did. The
  lookup only reads: SQLite's read-only mode (never immutable, which would ignore the write-ahead
  log and miss the newest threads), at most 1.5 s on a lock and 4 s in all, and any trouble leaves
  the session with the keys it had before. The reader is `modernc.org/sqlite`, SQLite translated
  to Go, so `mur` stays one binary built without a C compiler for every platform. The cost is
  size: with everything else in this change, `t4.exe` for Windows grew from 17.7 MB to 23.8 MB
  (12.3 MB to 16.2 MB built with `-s -w`) against `main` on 2026-10-29, almost all of it SQLite.
  `ncruces/go-sqlite3`, SQLite as WebAssembly, added more (10.5 MB on a probe).
- **The session is its thread.** A session in T3 is named after its thread, not after the
  provider's session under it, so its name stays through account switches, `/clear` and restarts
  (see "Under T3", above). The daemon keeps the thread, the CLI's path and T3's data directory
  per session in `.git/murmur/state.json`, so a message for a session whose MCP server T3 has
  stopped (after 30 minutes idle, for Claude Code) still finds its thread. The thread, the messaging
  token and the socket's address stay on the device: never on the bus, the page or the log. When
  T3 restarts a thread's MCP server, or the connection is made again, the new session has the
  thread and the old one, in its grace or stopped, loses its route: a message for everyone is
  handed to the thread once, and a session's own broadcast doesn't come back to it as a turn.
  What the old session had unread goes to the new one, which now has the way in, in the order the
  messages arrived, behind any that was already on its way in; it leaves behind what the new
  session sent itself and what came while it was running and was for it too, as it had its own copy
  of those, which it may have read. A message for everyone that comes in the second or so between
  a new session registering and finding its thread is handed to the thread by the old session,
  which still has the route, and the new session's copy is not handed over again when the route
  moves to it (#70, 2026-11-11): the daemon remembers, by event ID, what it handed to each thread
  for the last 30 minutes (up to 200 messages a thread, in memory only), and takes another copy of
  one out of the inbox of any session of that thread, as the thread has it. A message for the new
  session alone is another event, and goes in. Not covered: a new session that registers before the
  daemon has seen the old one's connection close. The old one, still running then, keeps the route
  through its grace, both copies can be on their way in at once, and the thread may see a message
  for everyone twice (#99). Handing the thread over when the drop is seen was tried
  (2026-10-30) and dropped: in three review rounds each version could strand a message to the old
  session's name, where a repeat is all that happens now.
  A session that found its thread keeps its name under the Claude session ID too, so the ID does
  not bring that name to a second process of the same Claude session; it is a session of its own.
- **Codex under T3** gets the thread from `_meta.threadId`, which every tool call carries (all five
  tools note it, `lookup` too), but its MCP servers start with a bare environment, `T3CODE_CLI_PATH`
  not among its variables, so Codex's config must forward it: `env_vars = ["T3CODE_CLI_PATH"]` under
  `[mcp_servers.murmur]` (worked with Codex 0.164.0 under T3 0.1.2, 2026-10-29). `mur join` writes
  it with `CODEX_HOME` (Session names, #88), and adds a name missing from a list the user already
  has there without touching their other entries; a list over several lines, or of anything but
  names, it leaves alone and says what to add by hand. Codex leaves out a name its own environment
  doesn't have, without a warning (`create_env_for_mcp_server` in `codex-rs/rmcp-client`, read
  2026-11-09), so a session in `~/.codex` starts its server as before.
  Without it the server doesn't know it is in T3, and a Codex session gets notices only. Codex
  reads a project's `.codex/config.toml` only once the project is trusted.
- **The messaging socket** is for Claude Code outside T3. Claude Code sets
  `CLAUDE_CODE_MESSAGING_SOCKET` (a unix socket, a named pipe on Windows) and
  `CLAUDE_CODE_MESSAGING_TOKEN` in every child process, and the MCP server passes both to its
  daemon with its hello. The protocol, as seen on 2.1.312 on 2026-10-29: JSON lines, an auth line
  `{"type":"auth","token":...}`, then `{"type":"user","message":{"role":"user","content":...}}`.
  The socket answers nothing, so Murmur knows a message was written, not that the session took it.
  The token dies with the session's process, so the socket is used only while the session's MCP
  server is connected. A session inside T3 is never written to this way: the socket there is T3's.
  Claude Code wraps what arrives on the socket in a note of its own that says the message came
  from another Claude session, not the user (seen 2026-10-29), which suits Murmur's framing.
- **Pi's extension (2026-11-09, #205).** Pi has no hooks of Claude Code's kind, so the extension
  `mur join` writes (`extensions/murmur.ts`) brings Pi #202's delivery itself, with nothing new in
  the daemon but a way for the hook to name a Pi session: by its file, `Native` in the hook's
  request, as the session's MCP server registered with it. It runs `mur hook --agent pi` at Claude
  Code's moments: as a run starts (`before_agent_start`, a prompt), after each tool call
  (`tool_result`; the notice goes in as a message `deliverAs: "steer"`, which Pi puts after the tool
  calls and before the next model request), and as the work ends (`agent_before_settle`, where a
  `custom_message` entry and `continue: true` keep it going, as blocking Stop does). Pi, unlike
  Claude Code, says when the work ended without a chance to go on: an error is the boundary's
  `outcome`, and Escape skips `agent_before_settle` and sets `agent_settled`'s `aborted`. The hook's
  Stop then says it was interrupted, and the daemon takes the session for idle and hands what is
  unread over as a turn, as for a T3 turn the user stopped. It opens a socket in the session that
  takes Claude Code's messaging-socket lines, and gives its address and token to the MCP server in
  its environment as Claude Code does, so the daemon hands an idle Pi session a turn the way it
  hands Claude Code one; Pi takes it with `triggerTurn`, and with `deliverAs: "followUp"` a busy one
  takes it when it finishes, not steered in. What Murmur says is a custom message, shown in the
  session and read in the user's role, never `sendUserMessage`, which Pi treats as the user's
  typing. The extension is in every Pi session, so it runs the hook and opens the socket only in a
  clone the user joined (its `joined.json`): elsewhere a tool call waits on no process of
  Murmur's (about 0.1 s each, Opus's review). It looks for the consent as the session starts, then
  each second and at each prompt and tool call until it is there, a few stats each time: the user
  usually joins from inside a running session, which would otherwise get nothing until its next
  session (the re-review's R1, reproduced with a real Pi). The socket's address and token are
  chosen at the start and given to the MCP server, which registers only once the clone is joined;
  a message that comes in the second before the socket opens is tried again after 2 s, and failing
  that waits for the hook. An update doesn't run `mur
  join` again, so Pi's MCP server rewrites an extension join wrote for its own binary when the
  text is an older binary's; Pi reads extensions as a session starts, so the first session after
  an update still has the old one. Seen with Pi 1.5.0 against a stand-in model, in RPC mode and in
  its terminal UI (driven in a pty by Opus's review, then here): a notice after a tool call, the
  message whole at the end of the work, a turn when idle, a turn right after an abort, a new socket
  and session after `/new`, no socket in a repo not joined, and an old extension rewritten.
  *Pi in T3 Code (2026-11-11, #462)* gets the same, with nothing added for it. T3 starts Pi as
  `pi --mode rpc`, which loads the extension, and Pi gives its MCP servers its own environment,
  T3's `T3CODE_CLI_PATH` in it; T3 records the session's file as the thread's native ID, the same
  path the extension gives the server, so the session finds its thread. Seen with Pi 0.99.0 on
  Windows, started as T3 starts it, against a stand-in for the rest of T3 (the rows T3 writes to
  its database, and a `t3` that hands what it is given to Pi as a prompt): a notice after the tool
  call, the message whole at the end of the work, and an idle message as one turn through
  `t3 thread send`, each once. The same run with no T3 in the environment took the idle turn
  through the named pipe, the first time that was tried. Not seen in T3 itself: Pi was switched
  off in T3's settings on the machine. T3's Pi adapter has no case for Pi's `custom_message`
  (T3 0.1.x, read 2026-11-11), so the notice and the message at the end of the work reach the
  model and likely don't show in the thread; a turn through T3 shows as any message does.
  Not covered: Pi's commits aren't credited, as no call is bracketed, and a session
  without a file (`--no-session`) gets turns and no notices.
- **The client's command line (2026-11-11, #135)** is for Codex and OpenCode 2 outside T3, which
  until then got a message only through `inbox` (and Codex a notice, where its hook runs). Each
  has a command that does what `t3 thread send` does, so the daemon runs it:
  - *Codex:* `codex queue --thread <thread> --message <text>`, with `CODEX_HOME` set to the
    session's. The message goes to the thread's queue, and a thread that is open takes it as its
    next turn. Seen on Codex 0.167.0 (2026-11-11): a terminal session that sat idle showed the
    message as a prompt and ran the turn about 3 s later. Codex's hook runs there, so the daemon
    knows when the session is at work, and a busy one gets the notice and its turn's end as before.
  - *OpenCode 2:* `opencode api session.prompt --param sessionID=<session>` with the body
    `{"text": ..., "delivery": "queue"}`, which asks the background server that every OpenCode 2
    client on the machine shares. `"queue"` waits behind a running turn (the default, `"steer"`,
    goes into it). When this was built OpenCode had no hook of Murmur's, so the daemon couldn't
    tell a busy session and handed it the message either way; since 2026-11-12 its plugin runs the
    hook (#431), and a busy session gets the notice and its turn's end, as Codex does. Seen on OpenCode 2.2.0
    the same day: the turn ran, and the terminal UI open on the session showed the message and the
    answer. `session.synthetic` starts a turn too, and the terminal UI then shows the answer and
    not what was asked, so it isn't used.
  - *Which program.* The session's MCP server names the program that started it (its parent
    process), if that is a `codex` or an `opencode`, and the daemon runs that one. Neither
    command reads the text from standard input or a file, so it goes on the command line, and
    the name on `PATH` won't do: on Windows it is npm's `.cmd` shim, which can't carry a newline,
    and it may be another install. On Windows a message whose command line would pass 32,767
    characters (a body near its 32 KiB limit) isn't handed over, and stays in the inbox.
  - *Only while the session's MCP server is connected*, as for the socket. A Codex thread nobody
    has open keeps a queued message until it is next resumed, and by then it has left Murmur's
    inbox. Connected doesn't mean watched: Codex's background app server kept a thread, and its
    MCP server, after the terminal on it closed, and 40 minutes later a queued message ran a turn
    there with no window open (seen 2026-11-11; the answer was in the thread). The page's wake
    setting is what limits that, as it does for an idle T3 thread. For OpenCode this is stricter than it need be: its server would run the turn with no
    window open, but a session of the shared server is dropped after 30 minutes without a tool
    call, and a message after that waits in the inbox.
  - *OpenCode's shared server only.* The server names OpenCode's program only when the process
    that started it is the one registered in `service.json` in OpenCode's state directory
    (`~/.local/state/opencode`, `"pid"`). A session of a private server (`--standalone`,
    `--server`) is left to the inbox: `opencode api` would start the shared server to ask it
    about a session it doesn't have. So is OpenCode 1, which has no `api` command; its terminal UI
    serves HTTP on a port of its own that nothing tells Murmur. If a later OpenCode keeps the
    registration elsewhere, its sessions go back to the inbox, silently.
  - *A failure* is handled as `t3 thread send`'s is: tried once more only if the command never
    started, and otherwise left to the inbox and the hook's notice. A Codex from before `queue`
    fails every time, and the daemon's log says so once per session.
  - *When it is a turn.* A Codex session has no way in until it has called one of Murmur's
    tools, which is how its server learns the thread. And the wake setting counts for these as
    for every session: by default one idle more than a few minutes isn't woken. For OpenCode,
    with no hook, "last at work" is its last call to a Murmur tool, and with no hook there is no
    notice either, so a message that comes after that waits in the inbox unannounced.
  - *On the command line.* The text is an argument of the command for the second or so it runs,
    where the machine's other users can read it (`/proc/<pid>/cmdline` on Linux) and where
    process auditing records it. Every other way in passes it over a pipe or a socket. OpenCode's
    HTTP API (the `url` and `password` in `service.json`) and Codex's app server
    (`thread/queue/add`) would avoid that, and the Windows length limit; both are more of each
    client's insides than the documented command.
  - *Whoever runs the client.* The way in is there for any session whose MCP server a `codex`
    started, not only a terminal's: Codex's desktop app, `codex exec`, a thread another tool
    drives through `codex app-server` (Claude Code's codex-sub plugin does). An OpenCode child
    session likewise. Each can be handed a turn its driver didn't start, within the wake
    setting; before, they had the inbox only. Not tried. A subagent Codex itself spawned was
    one of them until #454: its first Murmur tool call now takes it off the mesh (see "The
    subagents Codex spawns are delegates too").
  - *Not covered (#446).* Muse and Grok outside T3 (neither was installed where this was built). A Codex
    session run by T3 whose config doesn't forward `T3CODE_CLI_PATH` reads as one outside T3, and
    gets `codex queue` (not tried). OpenCode's one MCP process per directory has the environment
    of whoever started the shared server, so a T3 thread on a server a terminal started reads as
    outside T3, and a terminal session on one T3 started as inside (not tried). Whether Codex's desktop app shows a queued message as the
    terminal does wasn't looked at. macOS and Linux were not tried with the real programs, only
    Windows.
- **A thread that is idle (#87).** T3 stops an idle thread's provider process, and with it Murmur's
  MCP server, after about 30 minutes (seen live on 2026-10-30: both threads' sessions stopped at
  03:30, half an hour after their last turn), so the session of a thread that is idle is the normal
  case, and the thread most worth waking. Its route is kept in `state.json` (`T3Routes`) for as long
  as its inbox is, 24 hours from the stop, and the daemon stays up for as long as it holds one: a
  daemon that exited 10 minutes after the last session, as it did first, left a message to such a
  thread waiting for the next time anyone used the clone, by when it was past the 15 minutes a
  message may wake a session and arrived as a notice. A route counts only if the page's wake
  setting would let a message through to the thread. With no session live and no page open the
  daemon looks at the remote every 30 s (see "Speed"): about 2,900 `ls-remote` calls in a day of
  waiting, and a sleeping thread wakes up to 30 s later than a working one would. It still pushes its presence every 2 minutes (about 720 pushes a day), which tells others
  the device is there. Claude Code outside T3 has nothing like it: its messaging socket dies with
  the session. `mur join --leave` and `mur stop` end the daemon at once.
- **When a handoff fails.** A failed handoff is left to the hook's notice, and the log
  says why, once per session. It is not tried again, except when it plainly never started (no `t3`
  command to run, no socket to connect to), which is tried once more after a pause (2 s). Any other
  failure may have come after T3 took the message: `t3 thread send` has no idempotency key (the
  MCP tool `t3_thread_send` has `clientRequestId`, the command line doesn't), and the messaging
  socket answers nothing, so a second call could hand the message over twice, as two turns. The cost
  is that a failure that was only a failure (T3 starting up, say) gets a notice and no second try.
  The mark that a message was handed over ("attempted", in `state.json`) is saved before the
  handoff, not after, so it holds when the daemon stops in the middle of the call or right after a
  failed one; a daemon that starts again within the 15 minutes finds it and leaves the message to the
  notice, where it would otherwise have sent the turn a second time (#82). The mark comes off only
  when the call provably went nowhere, as above (that includes a `t3` call cancelled before it could
  start). A mark that can't be saved stops the handoff: the message is a notice, as after any failed
  handoff. The save is a write to the file, which a power cut can still lose. A crash between the
  save and the handoff leaves the mark on a message that was never sent, which is the safe way
  round: it is a notice, not a repeat.
  The other safe way is to look in T3's database for the message before trying again
  (`orchestration_v2_projection_messages`, by thread and text, or `t3 thread show --json`); it
  would keep the second try, and it relies on tables that are T3's own and may change in any
  update, so it wasn't done. Nothing is handed over for a user who has left either (see Joining a
  repo). A message that waited more
  than 15 minutes (T3 closed, the session away) is not sprung on the agent as a turn: it stays a
  notice. The 15 minutes count from when the daemon puts the message in the inbox, so one read
  after a long outage (2026-10-30, #30) is young and wakes its thread.
  The page's wake setting (#22, #25), for the device or for one session, decides the rest.
  "Never" keeps the notice. "Always" hands over at once. "Within N minutes", the default with N = 3,
  hands over only to a session that is busy or was last at work no more than N minutes ago; one idle
  longer gets the notice, and the message is handed over as a turn after all if the session is at work
  again while it is still young enough. The reason for the limit: a session idle that briefly
  still has its prompt cache, and a turn started after the cache has gone reads the whole
  conversation again at full price. Until 2026-11-07 "within" handed over as "always" did, which
  Tyler found out from the page; the minutes were saved and nothing read them. For a session in
  T3, "at work" comes from T3's database: busy while a run of its thread is queued or running,
  else idle since its latest run ended (`orchestration_v2_projection_runs`). Any other session, or
  one whose thread hasn't been read yet, goes by its start, its last request or tool call, and,
  stopped, when it stopped; a message just handed over as a turn counts too. The database is read
  every 5 s, so a decision can be that old. A daemon that starts looks at the messages its
  inboxes kept, and hands over those that are still young enough and may be.
- **Settled T3 threads are offline** (Tyler, 2026-11-07, #189). A thread T3 shows as settled
  (`settledOverride` in the thread's payload), archived or deleted leaves presence, so `who`,
  `lookup` and the page show it gone; its device lists it in presence's `offline`, and a daemon
  that reads that refuses a `send` to it, as its own device does. A message that reaches the
  device anyway (from a daemon older than the field) is dropped. Unsettled, which T3 does when
  someone types in the thread, it is back with the next read. T3 settles many threads by itself,
  often the moment a turn ends (their `settledAt` is the run's end; 671 of 854 threads on the
  laptop were settled, 2026-11-07), so this takes most finished T3 threads off the mesh.
- **Delegates go through their parent** (Tyler, 2026-11-07, #188). A thread another thread
  started, a T3 `delegate_task` child or a provider's subagent thread, has its own MCP server and
  registered as a session of its own, with a name and an inbox (reproduced 2026-11-07). T3 marks
  such a thread with `lineage.parentThreadId` (and `relationshipToParent: "subagent"`; 624 such
  threads on the laptop that day, none a user started), so the same read that finds a settled
  thread finds it, and it is off the mesh the same way: out of presence, listed in `offline`,
  messages to it dropped and refused, the refusal naming the parent's session on its own device.
  It can't `send` or set a status; the refusal tells it to ask the thread that started it. Its
  commits are credited to the session of its parent thread, if that is on the mesh here. It may
  have taken a name in the seconds before its thread was read; nobody sees the name. A Claude Code
  subagent inside one session never registered (see Crediting commits, Subagents).
  *Pi (2026-11-11, #457)* has no subagents of its own; packages add them. `pi-subagents` (0.31.0)
  runs each as a child `pi` that loads the user's extensions, Murmur's among them, and so
  registered as a session. It sets `PI_SUBAGENT_CHILD=1` in the child, and Murmur's extension
  does nothing in a process that has it: no server, hook or socket (seen with Pi 0.99.0: such a
  process never reached the daemon). This is not the daemon's delegate path, as the child never
  registers. Not covered: `@gotgenes/pi-subagents` (16.6.0) makes its subagents inside the
  parent's process and binds the parent's extensions to each; nothing in the environment tells
  such a session from its parent, and nobody has run one to see whether it registers.
- **The subagents Codex spawns are delegates too** (Tyler, 2026-11-12, #454), outside T3 Code,
  where T3's database knows nothing of them. It matters more since #447: such a thread could be
  handed a turn nobody asked for.
  - *The mark is Codex's own.* Its state database (`state_N.sqlite`, `threads`) says who started
    a thread: `source` is a JSON object with a `subagent` key for a thread Codex made for another
    (one a thread spawned, a review, a guardian), and a plain word for one a person's client
    started. For a spawned thread it names the thread that spawned it, which is the parent the
    refusal points to and the commits are credited to.
  - *The thread's ID* comes only from a call to a Murmur tool, which is where a Codex MCP server
    learns it. Codex's hook reports an ID earlier, at the first prompt, but it is paired with a
    session by process, and a subagent runs in its parent's process: its hook can be paired
    with the session that spawned it. So a hook's ID marks nobody.
  - *Inside T3* nothing is read from Codex. Tyler's rule (2026-11-12, #452) is that a thread
    shown in the user's list of sessions is on the mesh, and for T3's own threads T3's database
    says which those are (#188).
  - *No handling for subagent tools* (Tyler, 2026-11-12, #481). A tool that runs Codex as another
    agent's subagents, such as Tyler's own Claude Code plugin codex-sub, asks Codex for threads
    as any client does, and Codex records them as it does a terminal's. #473 first recognised
    codex-sub by the name it gives Codex (`threads.originator`) and found its parent among the
    ancestors of its Codex process; that was special handling in a shared tool for one person's
    plugin, and came out before the merge. Such a thread is on the mesh like any Codex session.
  - *What is left.* A subagent that never calls a Murmur tool is on the mesh, with no way in but
    its inbox. Its parent is named only if the thread that spawned it has called a Murmur tool
    too, since that is how a session's thread is known; otherwise the refusal names no one and
    the child's commits stay its own. Nothing is saved: after a daemon restart a Codex child is
    marked again at the first read, as its MCP server still gives its thread, and a child a hook
    named is back until its next hook. A child found late keeps the name it took; nobody sees
    it. A thread Codex doesn't keep has no row to read, and is on the mesh. Not run against a live
    Codex subagent: the reads were checked against the laptop's real Codex state, and the rest
    with a stand-in.
- **A hook can say its session is a child** (2026-11-12, #452, protocol 19). Where the client knows a
  session's parent and Murmur has a hook in it (OpenCode's plugin, #455), the hook's request
  carries `child` and, if it knows, `parentNative`, the client's own ID of the session that
  started it. The daemon keeps that by the session's own ID, so it holds for a session whose MCP
  server registers after the hook spoke, and the session is a delegate from then on. Nothing
  takes the mark off: a session that was started by another doesn't stop being so.
- **No loop guard.** Nothing in Murmur limits how often two agents answer each other (Tyler's
  decision, 2026-10-29). A message to a busy thread never steers the turn running mid-task: it is
  queued as the next turn, or, where the hook runs, announced, and given at the turn's end, which
  the turn then goes on with (#202). The AGENTS.md wording asks for coordination, not chatting; beyond that
  only the agents stop a loop.
- **Speed.** The daemon polls the remote every 3 s around a conversation, not 10 s (2026-10-29), and
  hands a message over the moment it is fetched. Measured between two T3 threads on one laptop through GitHub over HTTPS:
  about 6–8 s from an agent's `send` to the other thread's turn starting (a push, 1.2 s; up to
  one poll; `ls-remote`, 1.2 s; a fetch; `t3 thread send`, 1.1 s), and a question answered by
  another agent's reply in about 19 s. GitHub documents no limit on git reads, and a test of 63
  `ls-remote` requests about 2 s apart drew none, which is a short test, not evidence about
  hours of them from every device of a team. A poll that fails waits twice as long each time, to
  at most a minute (or 20 polls, if Poll is shorter), and goes back to its usual rate when it works.
  A fetch offers the server only the clone's bus commits as what it already has
  (`--negotiation-tip=refs/murmur/*`, 2026-11-10). By default git offers every local ref's
  commits, and for a presence commit, which has no parent, the server is never ready, so the clone
  listed the user's whole history: with 350 commits of its own, 300 of them unpushed, 6 requests
  offering 377 commits, against 2 offering 18. Over GitHub, before the change, a fetch with a
  presence ref in it took 0.93 s and one without 0.45 s (2026-11-09); nothing was measured over
  GitHub after it. The cost is junk: a commit built on a branch and pushed onto a bus ref brings
  the branch's whole history (540 KB against 12 KB for a 200-commit branch), since the server
  isn't told the clone has it, and on a large repo the slow fetch below takes it. Offering the
  remote's `HEAD` too would avoid that, but cost a presence fetch a request more, and a mesh fetch
  too when the branch is newer than the log, so it was left out.
- **A ref too slow to fetch (2026-11-08, #15).** Anyone with push access can put a large object on
  a bus ref, on purpose or with a mistaken push. A sync has a minute, and a fetch that used it up
  failed that sync and the next, for as long as the object stayed. So each fetch in a sync now has
  15 s (`FetchLimit`). A ref that runs past it on its own is fetched outside the syncs, one at a time,
  for up to 10 minutes (`SlowFetchLimit`), waiting 1 minute after a failure and twice as long after
  each one that follows, to at most an hour. The next sync reads what that fetch brought, rather than
  fetching the ref again, which on a link slow enough to run past the limit every time would mean
  never reading it (#223's first version did this). The cost that remains: an object that does finish
  downloading stays in the clone until a gc removes it, since git can't cap what a fetch brings. `git
  fetch --filter` would, but one such fetch makes the user's own clone a partial clone
  (`remote.origin.promisor`), for their branches too. On Windows a fetch cut off is killed, which leaves its
  `tmp_pack_*` behind, one per try (seen 2026-11-08), so the daemon removes those that have gone
  unwritten for 10 minutes.
- **How often it polls (2026-10-30, #93).** Tyler: agents don't need real-time communication, and
  4–7 s a hop is fine. So a daemon polls fast only when a reply is likely, and a session left open
  for hours, which would cost 1,200 `ls-remote` calls an hour at 3 s, costs 240.
  - *Fast, every 3 s (`Poll`), for 3 minutes (`FastFor`)* after this device sends a message, or
    queues one for a session on this device or hands one over to it, or a session starts here. A reply
    usually comes within a minute or two (the other agent's thinking time plus two hops), and each
    move restarts the 3 minutes.
  - *Otherwise, with a session live or a page open,* the wait doubles after each poll that follows
    FastFor's end: 3 s, 6, 12, up to 15 s (`PollCap`). Doubling reaches the cap in three polls; a
    first message of a conversation then waits at most 15 s, plus the handoff.
  - *Quiet:* with no session live and no page open the poll is every 30 s (`QuietPoll`, #87),
    whatever was said lately. A session registering, or a page opening, makes it look at once, and
    a session starting is itself a move, so it polls fast for the 3 minutes.
  - *Not moves:* presence heartbeats and status changes, which arrive all the time from active
    teammates and don't predict a reply, and other devices' messages for sessions elsewhere.
  - A failed poll keeps the back-off above, from whatever the rate was.
  - All four numbers are options, so a test can shrink them.

### Updating

Murmur updates itself by default (#192, Tyler, 2026-11-07), so fixes reach an installed daemon
without a person reinstalling it. `internal/update` checks GitHub's `a-Fig/murmur` releases/latest
at daemon startup and hourly. No repo information goes in these GET requests. Network failures are quiet and wait for the next hour.
A development build (`-dev`) never updates itself. Versions before this updater need one manual
install to acquire it.

The daemon downloads the `mur-<os>-<arch>[.exe]` asset and that release's `SHA256SUMS`, verifies
SHA-256, and stages a closed, synced executable beside the running binary's resolved symlink target.
The downloads never leave https (2026-11-09): a redirect from https to plain http, or an asset URL
that isn't https, fails the check, since whoever could serve the binary over plain http could serve
its checksum too. Hosts aren't limited, because GitHub has moved its asset host before and a daemon
refusing the next one could never update past it. Unix renames it atomically over the old file.
Windows renames the running exe to a unique backup, installs the staged file, and restores the
backup if installation fails. Backups still held by processes are cleaned on a later daemon start. A
lock beside the executable serializes clones sharing an install; a daemon whose executable has
already been replaced restarts onto it. Identity is checked again after downloading so a manual
reinstall during the download is left alone. An unwritable install (for example, a package
manager's) is logged once and left alone until restart. Successful updates log the old and new
versions in the daemon log.

After installation, the daemon waits until no message handoff is in flight and no page counts as
open, or until the page asks for the restart (#240), and until no key to its page that another
clone's page handed a tab is still good (#348), since the next daemon's page doesn't know it. From
then on it gives no keys, and the daemon asking for one asks the next daemon. It then blocks new handoffs and shuts down
normally, saving state and releasing the clone lock. It starts the installed binary with the same detached
process helper clients use, forwarding its own flags (including poll and idle settings). Sessions
reconnect through the existing grace and inbox restoration. Running MCP servers keep their loaded binary until their sessions
restart; if they need to launch a daemon they use the installed path. The protocol number above
still handles incompatible clients and daemons; release versions do not become a second protocol.

On restart the daemon hands the page's listening socket to the new daemon, which inherits it
(descriptor 3 on Unix, the same handle on Windows, named to it by `MURMUR_PAGE_LISTENER`), and
leaves the page's address and token beside it in `page.json` in its state directory. The port is
bound from the old daemon's last answer to the new one's first, so a tab's requests in between wait
in the socket's queue, and the new daemon answers them; the tab sees a new daemon and reloads onto
the new version. The reload waits while a person has text in a composer or a message that failed,
since those live only in the page (#242); the tab works with the new daemon until then. Each daemon serves the page from a listener made from a second hold on the socket,
which keeps it bound after the page's server closes its listener.

Decided 2026-11-09 (#255, from Sol's pre-release review). Before, the new daemon bound the old port
again and kept the token. In the gap, another user's process could take the port, be sent one of the
tab's requests with its token, and let the port go before the new daemon bound it, and the new
daemon then answered that token. Checking that the port is free can't prove it stayed free, so the
token now goes only with the socket: a daemon that gets `page.json` without a socket at that address
(a start through WMI, which can't hand handles over, or a socket it can't take up) serves the page
on a new port with a new token, and the tab needs `mur ui` again. Rotating the token on every
restart, with the tab fetching the new one, was set aside: whatever the tab holds, a page served on
its port in the gap could read (a reload there runs that page in the tab's origin), and the tab
can't tell who is listening. On Windows a socket stays tied to the I/O completion port of the
process that served it, so the new daemon detaches it before taking it up.

The page shows a newer release above Settings in its sidebar (a dot on Settings on a phone), with
the version in Settings: "Restart to update" while an installed update waits on the open page,
"Updating to X" while the daemon will install it, and "X is out" with the install command when
updates are off or the last check failed. It learns the latest release from the updater's last
check, else from the `latest.json` a daemon in this clone last recorded, and never asks GitHub
itself. With updates off nothing checks, so "X is out" shows only a release an earlier check
recorded. Builds from source (`-dev`) show no notice.

`git config --global murmur.update false` opts out; a repo's own git config overrides the global
value. `MURMUR_NO_UPDATE=1` opts out for a daemon started with that environment. The config is read
on each check. Agents explain updates and this separate opt-out before joining (`docs/agents/join.md`).
`git config murmur.channel prerelease` opts into prereleases and nightlies as well (#244, Tyler,
2026-11-08), so a release runs on our own machines before a stable tag sends it to everyone;
`docs/releasing.md` has the channels. A binary
shared across clones is shared across their update settings too: to prevent every clone from
updating it, use the global setting without local overrides.

The checksum catches corruption or a download inconsistent with the release. It does not prove
who published it or protect against a compromised GitHub account or release with a replaced
checksum file. Release signing is separate work (#193).

### Counting use

Tyler's goal for the project is 100 active users (2026-11-02), and the transport hides them: every
team's traffic stays in its own repo, and release downloads count installs, not use.

Tyler's rule (2026-11-08, #199): **the only thing Murmur sends us is a public repo's address.** A
public repo's bus is readable by anyone, so once we know the address, its repos, devices and
records can be counted from the bus itself (since #156 what the records say is encrypted, which
hides messages and people from such a count), and the address is already public, so there is no
opt-out. A private repo sends nothing and isn't counted. Weighed and turned down: a GitHub topic on
each repo as the marker (it changes how the user's repo looks), and an ID for private repos (a
hash of the address can't be salted for real, since the salt would ship in the public binary, and
a random ID is still data from a private repo).

`internal/publicrepo` does it, from `mur init`, `mur join` and daemon start:

1. If `refs/murmur/reported` is on the remote, stop: some clone has reported the repo. A daemon's
   sync doesn't fetch it (it reads only mesh, presence and names), so the remote is asked with one
   `ls-remote`, and once the ref is found or pushed, `publicrepo.json` in the state dir keeps the
   address it belongs to, so this clone doesn't ask again unless its remote changes.
2. Reduce the bus remote's URL to `github.com/<owner>/<name>`; anything else is not reported.
3. Ask GitHub `GET api.github.com/repos/<owner>/<name>` with no credentials. A private repo answers
   404 to that, so no token is involved. Private, or not on GitHub: look again the next UTC day
   (`publicrepo.json` again), so a repo made public later is reported within a day.
4. POST `{"repo": "github.com/<owner>/<name>"}` to `https://murmur.afig.dev/v1/repo`, spelled as
   GitHub spells it, with no version in the User-Agent. The site stores it only once GitHub
   confirms the repo public (when GitHub can't say, it answers 503 and the daemon sends it again
   later): one row per repo, its address and first day, no IP addresses (`site/src/usage.js`,
   `site/migrations`). A repo already stored is answered 200 from that row, without asking GitHub
   again, and a body not sent as `application/json` is refused with 415, so a web page can't have
   its visitors' browsers send these POSTs without a CORS preflight, which the site doesn't answer
   (#299). Rate limits per IP, and which token the site asks GitHub with, are set in Cloudflare,
   not here.
5. Once the site has answered 200, note in `publicrepo.json` that this address was sent, then push
   `refs/murmur/reported`, a root commit whose body is `{"v":1,"at":...}`, leased so it is created
   only if absent. A push that fails is retried at every later look, without sending the address
   again, and a daemon keeps looking until the ref is there. Two clones racing both send; the site
   keeps one row. Deleting the ref makes the repo report again from a clone that hasn't seen it.

So each public repo reaches us once in its life. A development build (`-dev`) never reports.
Daemons older than this ignore the new ref: their sync reads only mesh, presence and names.

Earlier versions (#148) sent a daily check-in to `/v1/checkin` (a random user ID, a hash of the
remote, the version, OS and architecture). Since 2026-11-08 the site stores none, but still answers
with the latest release; the rows already stored are kept as history at `/stats/archive`.
`mur version` now hears of newer releases from the updater, which keeps the latest release it saw
in the state dir's `latest.json`; with updates off it hears of none.

The counts come from the census (#259), which reads what each reported repo shows anyone. Once a
day a GitHub Action in this repo (`.github/workflows/census.yml`, `site/census/census.mjs`) fetches
the commits, without their files, of each repo's device logs and presences
(`refs/murmur/mesh/*`, `refs/murmur/presence/*`) and of its default branch, and sends the
site counts and hashes, never content: per UTC day, the sessions started (each once, though a
daemon that restarts announces its sessions again), the messages and all records, and a hash of
each device that wrote anything, presence included; a hash of each device's owner, the git user name
its presence shows; and for each contributor, a hash of their GitHub login or email and the day of
their first commit, so one person in two repos counts once. The site only ever raises a count and
keeps every device and owner it has seen, since a bus log can be rewritten and a daemon deletes its
presence when it stops. It runs from GitHub,
not from the Worker, because counting needs git, and a fetch of the whole bus is one request where
the API would need one per commit. Once the bus is encrypted (#257) a record's kind is hidden from a
public reader, so messages stop being told from other records, and only records are counted. An
updated device's presence hides its owner and sessions too, so the census then can't count its
sessions or tell its person apart. Whether some of that should stay readable is open on #156.

Each repo is counted on its own, since anyone can list one (#298). A repo that can't be read (gone
private, or deleted) keeps the counts it had. A repo may take 3 minutes, for its git and its
account lookups, and show 2,000 refs of devices, so that one with huge objects, 100,000 refs or a
slow API can't hold the job past its 30 minutes. A repo over either, or too big for the site's
limits on what it keeps, is skipped. The run warns of it but stays green: a repo skipped today is
skipped every day until someone takes it out of D1, and a run red every day teaches people to
ignore red. Counts the site refuses do fail the run, after every other repo has been sent. The
census sends only what the site's limits allow, so a refusal means the two disagree, or the site
is down, which can be fixed.

A green run can still leave repos uncounted, so the stats say so, from `repo_census.counted`, which
the site already keeps: `/stats/report.md` ends with when the census last ran, and, once that is
over 30 hours ago, that it hasn't run for that long; then the repos that run didn't count, each
with when it last was. A repo listed since that run's day and not yet counted is new, not missed,
since anyone can list one and most days someone does (`/stats/data` has the same, and the page's
footer leads with it only when something is behind). Each run's page in Actions has a table of
every repo, a row written as each is done, so a run cut off at its timeout keeps what it did.

A time on the bus or a branch is its writer's clock, and can say any year up to 9999. A day after
tomorrow (UTC) is never believed, since one would keep a repo active and its people new for ever,
and nor is a bus day before 2026-10-01, when there was no bus. A record with such a time is dated by
its commit, or isn't counted. A commit dated in the future counts its author as of the day it is
read. The site refuses such days, and the stats count nothing after today.

`/stats` shows those repos and the counts over time; it needs a key: Tyler's, or one he gives an
agent, which reads it as markdown at `/stats/report.md` or JSON at `/stats/data` (#150).

## Risks and how the design meets them

- **Agents replying forever.** T3's local messaging stays calm because a reply is plain text in
  the receiver's own thread; nothing goes back unless the agent deliberately sends again. Murmur
  keeps that: no automatic replies, incoming messages that never steer a running turn (queued
  behind it, or since #202 told by the hook and given at its end),
  and AGENTS.md wording that asks for coordination, not chatting. No limits or caps on top of that,
  now that a message starts a turn without anyone typing (Tyler's decision, 2026-10-29): two
  agents that keep answering each other are stopped by nothing but the agents.
- **Prompt injection.** Anyone with push access can now put text in front of your agent. That
  matches the trust model (a hackathon team), and a message is text in front of the
  agent as a turn of its own, passed on as written; messages never get to bypass the receiving
  user's permission mode, and the framing's first line keeps their origin visible.
- **Leaked secrets.** Everything on the bus stays in repo history. It is encrypted for the devices
  that were on the team when it was written, and those devices keep their keys; a daemon older than
  encryption writes it in the clear. Agents do paste `.env` values. Everything a session sends (its messages and status
  line) is scanned first, and a match blocks the send rather than being redacted, so the
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
what makes Claude Code and Codex sessions first-class, and since 2026-11-01 OpenCode, Muse Code and
Pi sessions too, with Grok's set up from its documentation (see "Other agents", under Joining a
repo).

## Prior art

A survey on 2026-10-26 found a dozen tools in this space, nearly all from the last few months.
None uses git as the transport: each needs a hub, a relay or a peer-to-peer network that someone
runs or that hackathon Wi-Fi can block. That, and setup amounting to "have push access", is
Murmur's difference.

- **openhuddle**: a hub server plus Claude Code hooks; claims (automatic, on edit), presence,
  messages. The closest feature match. Days old, one author, many concepts.
- **crosstalk** (epode-studio): encrypted rooms through a Cloudflare relay, eight clients. The best
  message delivery and safety design, the source of the notice-only delivery Murmur used until 2026-10-29.
  Appears macOS-only.
- **walkie**: peer-to-peer chat between agents (Hyperswarm), the most used (≈253 stars). Messages
  only; no relay, so it can fail behind client isolation.
- One machine only, so not comparable: polymatx/dibs, abevz/dibs, bcmyguest/huddle, parley
  (auspex0).

## Built in Go

One static binary per platform, `mur`, holds the daemon, MCP server, hook and the few commands a
person runs. A teammate installs one file and no runtime. Go cross-compiles for macOS, Linux and Windows from any one
machine with no C toolchain, where Rust would need Apple's SDK and linker to build for macOS
from elsewhere. Tyler chose between the two on 2026-10-26. The MCP server uses the official Go
SDK (`modelcontextprotocol/go-sdk`, v1.8.0). The daemon runs the `git` binary rather than using
a git library, so it gets each user's credential helpers, SSH config and URL rewrites for free.

## Build order

1. Daemon, CLI and git transport, tested with two clones on one machine, then between Tyler's
   Windows laptop and a macOS machine. That machine pushed with a deploy key for this repo (added
   2026-10-26). The test of custom refs against Actions and webhooks is done (above).
2. MCP server and the Claude Code hook. The first useful version.
3. T3: connect as an MCP server. `t3 thread send`, run on the same machine, queues a message
   into any thread and starts its turn, so waking an idle thread (#25) needs no change to T3.
   Done 2026-10-29 (#56, #57): the daemon hands a message over with it.
4. Codex specifics.

## Open questions

- The name.
- A worktree made after joining has the tools (the consent is per clone) but no hook, until
  `mur join` runs there again. Nothing prompts that, since the agent in it already has the tools.
  Should the server or the daemon install the hook there?
- Does Claude Code's messaging socket start a turn in an idle session? Its protocol is known
  and a message written to it arrived in a working session (2026-10-29); an idle one hasn't been
  tried.
- Does GitHub rate-limit a `git ls-remote` every 3 s (around a conversation) from each of a team's
  devices? Nothing
  documents a limit for git traffic; none was hit in a test of 63 requests 2 s apart. Since
  2026-11-10 a device's `/api/health` counts failed syncs and classes a refusal for too many
  requests as `rate-limited`, so a team's own devices can show it.
