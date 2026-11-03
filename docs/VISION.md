# Vision: agents that find each other across a hackathon team

Status: V1 works, in Go (2026-10-26); the next version, with names, a web page and wake-ups, is
being built; the web page, an agent joining a repo by itself, and delivery of a message as a turn of
the receiving session are in (2026-10-29). On 2026-10-30 a Claude thread and a Codex thread in T3
Code, in two clones, each joined after their user said yes and then talked with nobody typing: 15 s
from question to answer. The product is Murmur, and its command `mur` (Tyler, 2026-11-01); until
then the repo and the tool were called t4 (`docs/names.md`).

## The problem

At a hackathon, three to five people each run several coding agents against one private repo,
shipping as fast as they can. The agents can't see each other. Two of them build the same auth
flow; one refactors a module another is halfway through extending; a third picks a different
state library. The humans find out at merge time, or at the demo.

T3 Code already lets sessions on **one machine** discover and message each other. Murmur extends
that to sessions on **different machines working in the same repo**, so agents can settle "who is
building what" among themselves, without bringing the humans in.

## Who it is for, and who it is not

For: a small team (a handful of people, a few dozen sessions at most) on a **private** repo, for
the length of a hackathon, where everyone with push access is trusted.

Not for: public repos, large teams, long-lived projects, or anyone whose agents should not take
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
messages. No server, no accounts, no extra auth: whoever can push is in. (One thing does leave the
repo: a daily check-in that counts active users; see Counting active users.)

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
  state when it changes and on a heartbeat every 2 minutes. Heartbeats would otherwise flood the
  event log. A presence unchanged for 6 minutes is stale: its device's sessions may not get what
  is sent to them, and `who` and `send` say so.
- A reader measures staleness from when it saw a presence change, on its own monotonic clock, and
  only up to its own last successful sync, so a reader that was itself asleep or offline doesn't
  take everyone else for gone. Only a presence it has never seen change is dated by the writer's
  clock, from when the tip commit was written and never later than now. A daemon that has just
  started sees every device's ref as new, so dating them from now showed devices dead for hours
  as alive for the first 6 minutes, which is when a new session is most likely to ask `who`
  (seen live, 2026-10-30). A writer whose clock runs more than 6 minutes slow looks stale until
  its next heartbeat, at most 2 minutes, when the reader sees the presence change and times it
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
  it, so a `send` made while presence is being pushed waits about a second more. Pushes from
  different devices can still meet on GitHub; a refused one is retried as before.
- Bus commits carry git's empty tree, so they hold no files and fetching them brings nothing else
  along. The daemon pushes them with `--no-verify` and doesn't sign them with git: a `pre-push`
  hook that runs the test suite, or a signing prompt, on every message would make the bus
  unusable. It seals what is in them instead (see Seals). These commits only ever go to
  `refs/murmur/*`, never to a branch.
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
- Reading: poll `git ls-remote origin 'refs/murmur/*'`, which is cheap; fetch only when a SHA has
  changed. How often is "Speed", below (3 s around a conversation; 10 s before 2026-10-29). A message takes about 6–8 s from a `send` to the
  receiving agent's turn ("Delivering a message as a turn", below); V1, polling every 10 s, took
  about 12 s from a Windows laptop to a macOS machine (2026-10-26), 2 s of it the send.
- A device is one clone's daemon. Worktrees of a clone share its `.git`, so they share its daemon;
  a second clone on the same machine is a second device. The device ID is the hostname plus four
  random hex digits (`tyler-laptop-3f2a`), kept with the daemon's other state in `.git/murmur/`,
  which no commit or checkout ever touches. (An earlier draft grouped repos by root commit;
  per-clone daemons don't need that.)

### Seals

Added 2026-11-03 (#155). A record says which device wrote it, and without a seal nothing backs
that: anyone who can push can write any ref, git tells a reader nothing about who pushed (a
commit's author is whatever its writer put), and GitHub's push webhook and its repository
activity API, which do name the pusher, say nothing about custom refs (the activity API returned
nothing for `refs/murmur/names` on 2026-11-03). Some pushers are deploy keys, with no account at
all. So a message could name any sender, a presence any sessions. Tyler decided on 2026-11-03 that
every device seals everything it writes, in every repo.

- **The key.** Each device has an Ed25519 key, made with its ID and kept beside it in
  `.git/murmur/key`. A device whose key is lost while its state shows it had one takes a new ID,
  since its peers would refuse a new key under the old one; one that never had a key, from before
  seals, keeps its ID.
- **What a seal covers.** The record's JSON as written, byte for byte, bound to the ref it goes on
  and the commit it follows. A seal copied to another ref, or pushed again after another commit,
  doesn't hold. The seal is the JSON's last member, `"seal":{"by","key","sig"}`, and the signed
  bytes are the object without it, so a field a reader doesn't know is covered too. Daemons from
  before seals ignore the member, as they ignore any field they don't know.
- **Pinned on first sight.** A reader takes a device's key from the first sealed record it reads
  for the device, and keeps it in `state.json`. From then on it refuses any record claiming that
  device but not sealed with that key: unsealed (so a seal can't be stripped off), or sealed with
  another. Until a device has sealed anything, its unsealed records are read as before, since
  daemons from before seals write nothing else; `who` and the page show it as unverified.
- **The name registry** is one ref every device writes, and seals do less for it. A sealed record
  is checked against the device that sealed it, which may give names only to its own sessions, and
  may set names aside for any device, as a daemon repairing the registry does for all of them. An
  unsealed record is read as before, whichever device it names: refusing those for a device that
  seals would refuse its records from before it did, which a reader joining later reads after
  pinning its key, and would gain little, since a snapshot, which any device may write, can list
  any name for any address. A name already known to be someone's never moves, sealed or not.
- **What it doesn't stop.** Whoever seals first for a device is pinned: someone who can push could
  seal a record for a device before the device does, and its own records would then be refused.
  That is what a team of people who can all push already trust each other with. A presence has no
  parent, so a seal can't pin its place; a sealed presence no newer, by its writer's clock, than
  the last one taken from the device is refused as a replay. A reader that restarts takes the
  first it reads. And
  anyone who can push can still delete or overwrite refs, stopping delivery; seals stop
  impersonation, not interference. A record refused is logged, and `who` and the page show the
  device's last refused one.
- **Refusing unsealed records.** Devices that have never sealed are still read. When to refuse
  them, once every daemon in use seals, is Tyler's to decide.

### Encrypted repos

Added 2026-11-03 (#156). On a public repo only collaborators can write the bus, but anyone can
read it: every message, status line and branch, in history for good. Tyler decided that day that
a repo can choose to encrypt its bus, in the same binary, while seals stay everyone's default.

- **Turning it on.** `mur encrypt` writes a sealed record on `refs/murmur/config`. A daemon that
  reads one, sealed by a device it takes as that device's, encrypts everything it writes from then
  on, and keeps that in `state.json`. Nothing turns it off: anyone who can push could delete the
  ref or write another, so a daemon never goes back to writing in the clear.
- **Box keys.** Each device has an X25519 key beside its signing key, `.git/murmur/box`, and
  publishes its public half in every presence, encrypted repo or not, so that keys are known
  before encryption is turned on. A presence is sealed, so a box key is as much its device's as its
  signing key is; only a sealed presence's box key is taken.
- **What is encrypted, and for whom.** An event is written as type `encrypted`, which older
  daemons pass over: its ID, sender and time are readable, the rest (recipient, subject, body;
  agent and branch) is in a box. A message to a session or device is encrypted for that device and
  the sender's own; a broadcast, and session events, for every device whose key is known. A
  presence keeps its device, time and box key readable, and encrypts its host, owner and sessions
  for every device whose key is known. Each record has a fresh content key, wrapped for each
  recipient by X25519 and HKDF-SHA256, as age does it, with AES-256-GCM, all in Go's standard
  library. The box is bound to its event's ID and sender, or to its presence's device.
- **Failing closed.** A message for a device whose box key isn't known is refused, never sent in
  the clear. A broadcast goes to the devices whose keys are known.
- **What stays readable.** Which device writes, when, and which devices each record is for. The
  name registry stays as it is: names made at random, and the addresses they stand for, which
  every device needs.
- **Older daemons.** One from before encryption knows nothing of it and goes on writing in the
  clear, and nothing can stop it. `mur encrypt` refuses while a device seen lately is unsealed or
  has no box key, and names it; `--force` goes ahead and says which devices will leak.
- **What a device can read.** Only what was encrypted for it: a device that joins later can't read
  what was sent before, nor can its page or `lookup` show messages between others.

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
missed, except for the sessions whose inbox is kept (2026-10-30, #30): to a session stopped
within the 24 hours it delivers what was sent after the session stopped (by the sender's clock,
with a minute's allowance), which includes a message to the whole device or to everyone, as the
live ones do and as the shorter outage's catch-up does. Before then a laptop shut overnight lost
every message sent to its stopped sessions. A first start has no stopped sessions and skips all of
it. A message is addressed on the bus to
`device/session`, which a session's name stands for (see Session names); presence carries enough
description (name, agent, branch, what it is doing) for an agent to choose a recipient. Nothing sent before a session first started is replayed to it. A
daemon back after less than 10 minutes down delivers what it missed to the sessions that
reconnect within 30 seconds of its start, and to sessions stopped within the 24 hours.

Sessions reach the daemon over loopback TCP, on a random port written with a random token to
`.git/murmur/daemon.json`. A daemon with no sessions, no open page (see The page for humans) and no
T3 thread to wake for 10 minutes exits. Any daemon that stops deletes its presence, so other devices drop it at their
next sync rather than when it goes stale.

An upgrade replaces the binary and not the daemon already started from it, which would go on
answering ping and refuse whatever came after the version it was built from (the route request
and delivering a turn, #57, were the first). So the daemon reports a protocol number
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

### Joining a repo

Decided 2026-10-29 (#55), built the same day. Tyler's requirement: setup should feel like magic,
and T3 Code users are the main customers. The user should still know Murmur is running and choose to
join. So a repo that uses Murmur says so, in a section of `AGENTS.md` and `CLAUDE.md` that an agent
reads when it starts; an agent that has no Murmur tools asks its user whether to join; and if the
user says yes, the agent installs mur and runs `mur join`, which does the rest. Tyler also decided
that messaging gets no command-line commands (`who`, `send`, `inbox`, `log` were removed): "If
everyone is using the CLI, there will be no one to message." Agents message through MCP, humans look
at the page (`mur ui`), and the commands left are `init`, `join`, `ui`, `stop`, `version` and
`help`.

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
  isn't this commit. `init --no-push` is the old behavior. `docs/agents/setup.md` is the page an
  agent follows when its user asks it to set Murmur up; the README links it.
- **What `mur join` writes.** It refuses in a repo without the marker. Everything it writes is
  outside the repo's tracked files:

  | What | Where | Per |
  |---|---|---|
  | Consent | `.git/murmur/joined.json` (mode 0600) | clone, so every worktree of it |
  | The MCP server, for Claude Code | `claude mcp add --scope user murmur -- <absolute path of mur> mcp`, which writes `.claude.json` in `$CLAUDE_CONFIG_DIR`, else the home directory | user |
  | Permission for its tools | `mcp__murmur` in `permissions.allow` of `settings.json` next to it | user |
  | The MCP server, for Codex | a `[mcp_servers.murmur]` table in `$CODEX_HOME/config.toml`, else `~/.codex/config.toml`: the command, `args = ["mcp", "--agent", "codex"]` and `default_tools_approval_mode = "approve"` | user |
  | The MCP server, for OpenCode, Muse Code, Pi and Grok | see "Other agents", below | user |
  | The hook, on `PreToolUse`, `PostToolUse`, `PostToolUseFailure` and `UserPromptSubmit` (`setup.HookEvents`) | `.claude/settings.local.json` in each worktree of the clone, kept out of `git status` through the clone's `info/exclude` | clone |
  | The daemon | started, if none is running | clone |

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
  Run again, `join` changes nothing and says so; it fills in what is missing and repoints an entry
  whose binary moved. A worktree made after joining has no hook until `mur join` runs in it again.
  `mur join --leave` removes the consent and the hooks and stops the daemon. It leaves the server
  registered, since that is per user and does nothing where the user hasn't joined; its output says
  how to remove it.
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
  (after its first sync, which takes a while), on every request but `stop` and `ping` (which reveal
  nothing, and are how a leave finds it), again in a registration after it has resolved the branch,
  before each push of its own (presence, the names registry) and before each delivery of a message
  as a turn (the one thing it does to the user's other programs, so a message that comes in by sync
  after a leave waits in the inbox, and isn't counted as a failure), and once a second, when it stops
  itself if the user has left. The last two are what end a daemon that was still starting when
  `join --leave` ran and so had no `daemon.json` to be found by. `mur daemon` and `mur ui` refuse in
  a clone that hasn't joined (so a stale client or a script can't start one). `join --leave`
  forgets the consent before it stops anything, then waits up to 20 seconds for the daemon's lock to
  be free, and reports it as a problem, not as stopped, if it isn't. What it can't close: a push
  already past its check when the leave lands completes, and a starter already past its own
  check (Windows starts the daemon through WMI, which is slow) can still launch `mur daemon`, which
  exits at its consent check before it opens any state or publishes. A repo without the marker, or
  one the user hasn't joined, gets no daemon, no `refs/murmur/*` and nothing published. A clone
  whose user joined but whose current branch lacks the marker keeps its daemon (it is the user's,
  for every worktree) and gets no MCP tools there.
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

#### Other agents

Added 2026-11-01 (#128), at Tyler's request: OpenCode, Muse Code, Pi and Grok, in T3 Code and on
their own. Each was looked at in its installed binary, its documentation and probe runs (OpenCode
2.2.0, Muse 1.4.14, Pi 0.99.0 on Tyler's laptop; Grok Build from its documentation and source only,
since it wasn't installed). The details, and where each came from, are in the comments of
`internal/join` (one file per agent) and `internal/mcpserver`.

| Agent | Where join writes | How a session is told apart | Inside T3 |
|---|---|---|---|
| OpenCode | `mcp.servers.murmur` in `~/.config/opencode/opencode.jsonc` if there (OpenCode reads it after `opencode.json`, whose own murmur entry is made the same), else `opencode.json`; `"codemode": false`; a `murmur_*` allow rule kept last in `permissions`, where the last matching rule counts | One server process serves every session in a directory; each call names its session in `_meta["ai.opencode/sessionID"]`, and the server keeps a registration per session that calls | The session ID is T3's native ID for the thread |
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
- **Delivery.** Inside T3 the existing `t3 thread send` delivery needed nothing new: an OpenCode and
  a Muse thread each took a message as a turn and answered it with `send`, nobody typing
  (2026-11-01). Outside T3 none of the four has a way in that Murmur uses yet (OpenCode has an HTTP
  API and plugins, Pi extensions can start a turn, Muse's session messaging isn't on Windows), so a
  message waits in the inbox, with no notice: no hook of Murmur's runs in them.
- **Titles.** Inside T3 the thread's title is read as for any thread. Outside T3 none is read for
  these agents: their IDs aren't Codex threads.
- **Tool approval.** OpenCode and Grok have a rule for it, which join writes; Muse and Pi have none.
  In T3 outside full-access mode, Muse and T3's Pi extension ask before each call.

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
  `.git/murmur/daemon.json`, and the page uses the daemon's token, the same one the sessions use. A
  daemon that can't open the port logs that and keeps running without the page; `mur ui` then says
  there is none.
- **Opened by `mur ui`**, which starts the daemon if it isn't running, opens the default browser
  and prints the address. The token is in the address's fragment (`#token=`), which a browser
  keeps from the server; the page reads it and sends it with each request. `mur ui --no-open`
  prints the address and opens nothing.
- **What it shows.** Every device the daemon knows, this one first, with each session's name,
  agent, branch, what it is doing, when it started and when it was last active (#21), and the
  branches it has committed to when there are three or fewer. Whether a device is reachable, and
  when it was last seen, come from presence going stale. The worktree's folder name is shown for this
  device's sessions only: presence doesn't carry it, and a path from another machine would mean
  nothing here. Messages are every one this daemon reads, whoever it is for (anyone who can read
  the repo can read them all already), oldest first as this daemon read them, which is not by the
  senders' clocks. The daemon keeps the last 500 in memory, so they don't outlast a restart, and
  it never read what was sent while it was away for more than 10 minutes, except what is kept for a
  stopped session's inbox (above).
- **Settings** (when a session is woken) are saved in `.git/murmur/settings.json`, replaced whole.
  Nothing acts on them yet: waking is #25. Mutes (#27) aren't built, so the page shows none and
  refuses to unmute.
- **Open page, running daemon.** The page asks for its state every 3 s, 15 s in a hidden tab. A
  call that carries the token, from a page someone can see, counts as an open page for 2 minutes,
  and an open page stops the 10-minute countdown the way a session does: a daemon with no sessions
  and no T3 thread to wake exits about 12 minutes after its page is closed or hidden (a background
  tab, a minimized window, a locked screen). A hidden page marks its calls, so a tab forgotten in
  the background doesn't keep the daemon, and its polling of GitHub, up for good (Opus review,
  2026-10-29). A request without the token doesn't count.

### What a session has done

Added 2026-10-29 (#21). `lookup` (an MCP tool; it has no command, like the other messaging tools)
gives an agent what `who` doesn't: the branches another session has committed to, when it was last
active, whether a message would reach it, and its latest messages. A session on this device also gives its worktree folder,
the commits it made and the client's session ID, which stay on this device. Git doesn't record
which session made a commit, so the daemon watches tool calls for them.

- **Bracketing a call.** `mur join` installs the hook on `PreToolUse`, `PostToolUse` and
  `PostToolUseFailure` as well as `UserPromptSubmit`, with matcher `*` on the three tool events, and
  run again adds what an older join lacks. A repo set up by an `init` from before #55 had the hook
  in its committed `.claude/settings.json`; `mur init` removes it there, and the user's `mur join`
  installs it in `.claude/settings.local.json`. On `PreToolUse` the daemon snapshots the worktree
  of the hook's working directory (`attrib.Head`: HEAD, branch and every ref tip). On the call's end it
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
  worktree of the clone: the start's ref tips are the whole clone's, so they keep either
  worktree's history out, all but a detached HEAD's in the other (#60). A `cd` inside a Bash call leaves the hook's working directory where it
  was (seen on 2.1.312, 2026-10-29), so a call ends elsewhere only when Claude Code itself has
  moved, and what `cd ../other && git commit`, or `git -C ../other commit`, makes in another
  worktree isn't credited. The hook finds the clone from its working directory, and a call in
  another repository, or none, isn't watched. A subagent's hooks carry the parent's session ID,
  so its commits are credited to the parent.
- **What it costs.** A call that cannot run a command (`Read`, `Glob`, `Grep`, `Edit`, `Write`
  and the like, named in `internal/hook`) isn't bracketed. Any other tool is, MCP tools and
  subagents included. The start is one `git` process: the hook finds the clone from its files, and
  `attrib.Head` reads HEAD from the worktree's git directory and asks git only for the refs (a
  reftable repository, or one git might find differently, takes three). It can't be made to run
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
  matter. Codex, and any agent without the hook, moves it by MCP calls only, and has no commits
  credited. An older daemon reads the new presence and ignores both fields; the sessions of a
  daemon that doesn't write them have no last-active time, and the page leaves it blank.
- **Messages in `lookup`** are those this daemon has read, the same as the page keeps: the last
  500, none from before it started.

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
  | Codex outside T3 | The thread's explicit name: `threads.name` in `state_N.sqlite` (the highest N) of `$CODEX_HOME` as the session's MCP server has it (Codex starts it with a bare environment, and `join` doesn't forward `CODEX_HOME` yet), else the daemon's, else `~/.codex`; failing that `thread_name` in `session_index.jsonl`, the last entry for the thread | `threads.title` and `threads.preview`, which hold the opening message |
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
    with `agent_id` set, so a subagent's tool calls don't move the session (seen on 2.1.312,
    2026-10-29).
- **Under T3 the session is the thread, not the provider's session under it.** The Claude
  session ID under one thread changes when the user switches Claude accounts, and has gone
  A → B → A. T3 gives MCP servers and hooks no way to learn the thread ID. Tyler decided on
  2026-10-28 not to change T3's source, so Murmur looks up the provider's session in T3's database
  (#56, 2026-10-29): "Delivering a message as a turn", below.
- **Codex's MCP servers get a bare environment,** about 20 Windows core variables and no
  `CODEX_*` or `T3CODE_CLI_PATH` (unless the Codex config forwards it), so the server learns its
  thread at its first tool call. Hooks under Codex inherit
  the `CLAUDE_CODE_SESSION_ID` of any Claude Code session that started Codex, so that variable
  doesn't prove a hook runs under Claude Code.
- **Two live processes can hold one ID.** Claude Code lets a session be resumed twice at once.

### Session names

Tyler's decisions, 2026-10-28 (#20). Every session has a name of two words, an adjective and a
noun (`quiet-otter`), unique in the repo and never reused, not even after its session ends. The
name is the address: `send` takes it, `who` shows it, and notices and `inbox` name the sender.
`device/session` still works. It is what a message carries on the bus, so the event format didn't
change and a daemon that knows no names delivers by it all the same.

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
    address goes on the bus as it would from any device, and is forwarded on delivery, so no
    message is forwarded twice.
  - *The registry's own session.* A message is addressed on the bus, and the address for a name is
    the session the registry has it down for. Once that session has left the name, a message to the
    name reaches it, as A name a session has left says: running, under its new name; stopped, in
    its kept inbox, or forwarded by the name it last had to the session holding that one. The name
    sent to isn't on message events, so delivery can't tell; putting it there is a bus format
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

The daemon delivers at once, when a message arrives for a session it can reach:

| Where the session runs | Delivered by | Starts a turn in an idle session |
|---|---|---|
| T3 Code: Claude Code, Codex, OpenCode or Muse (and Pi and Grok, not tried) | `t3 thread send <thread> -`, the text on standard input. T3 starts the thread's turn if it is idle and queues the message as its next turn if it is busy, interrupting nothing | Yes (seen 2026-10-29: Claude Haiku to Claude Haiku, and Claude Haiku to Codex gpt-6.1-sol, in two real T3 threads, nobody typing) |
| Claude Code outside T3 | the session's messaging socket | Unverified: a message Murmur's code wrote to a session's socket arrived as a turn while the session was working (2026-10-29, Claude Code 2.1.312, Windows); an idle one has not been tried |
| Codex, OpenCode, Muse, Pi or Grok outside T3, or any delivery that fails | the hook's notice where the hook runs (Claude Code), and `inbox` | No |

What the agent reads:

```
Message from quiet-otter (Claude Code, on smash's machine) via Murmur:

<the subject, then the body>

Reply with Murmur's `send` to quiet-otter if a reply is needed.
```

The first and last lines are Murmur's: the first says that a teammate's agent is speaking, not the
user (the job the old "envelope" did, so that the agent treats a message as information rather
than as the user's own typing), and the last says how to answer and that answering is optional.
The sender's text in between is delivered as written, neither quoted nor wrapped: teammates'
agents are trusted in Murmur's model, and a message that reads as a request is meant to be acted on.
A body past the limit is cut, as in `inbox`.

- **Marked read.** A delivered message leaves the receiving session's inbox, so neither the hook
  nor `inbox` shows it again; the page and the daemon's log keep it as history. A message that
  could not be delivered stays in the inbox and is announced by the hook as before. The hook
  leaves alone a message that is waiting its turn to be delivered, behind the one being delivered:
  announced by a notice it would never become a turn.
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
  delivered to the thread once, and a session's own broadcast doesn't come back to it as a turn.
  What the old session had unread goes to the new one, which now has the way in, in the order the
  messages arrived, behind any that was already on its way in; it leaves behind what the new
  session sent itself and what came while it was running and was for it too, as it had its own copy
  of those, which it may have read. Not covered: a message for everyone that comes in the second or
  so between a new session registering and finding its thread goes to the old session's thread
  while that has the route, and to the new one when it has it, so the thread may see it twice.
  Nor is a new session that registers before the daemon has seen the old one's connection close:
  the old one, still running then, keeps the route through its grace, and the thread may see a
  message for everyone twice (#99). Handing the thread over when the drop is seen was tried
  (2026-10-30) and dropped: in three review rounds each version could strand a message to the old
  session's name, where a repeat is all that happens now.
  A session that found its thread keeps its name under the Claude session ID too, so the ID does
  not bring that name to a second process of the same Claude session; it is a session of its own.
- **Codex under T3** gets the thread from `_meta.threadId`, which every tool call carries (all five
  tools note it, `lookup` too), but its MCP servers start with a bare environment, `T3CODE_CLI_PATH`
  not among its variables, so Codex's config must forward it: `env_vars = ["T3CODE_CLI_PATH"]` under
  `[mcp_servers.murmur]` (worked with Codex 0.164.0 under T3 0.1.2, 2026-10-29). `mur join` writes
  it, and adds the name to a list the user already has there without touching their other entries; a
  list over several lines, or of anything but names, it leaves alone and says what to add by hand.
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
- **When a message isn't delivered.** A failed delivery is left to the hook's notice, and the log
  says why, once per session. It is not tried again, except when it plainly never started (no `t3`
  command to run, no socket to connect to), which is tried once more after a pause (2 s). Any other
  failure may have come after T3 took the message: `t3 thread send` has no idempotency key (the
  MCP tool `t3_thread_send` has `clientRequestId`, the command line doesn't), and the messaging
  socket answers nothing, so a second call would deliver the message twice, as two turns. The cost
  is that a failure that was only a failure (T3 starting up, say) gets a notice and no second try.
  The mark that a message was handed over ("attempted", in `state.json`) is saved before the
  handoff, not after, so it holds when the daemon stops in the middle of the call or right after a
  failed one; a daemon that starts again within the 15 minutes finds it and leaves the message to the
  notice, where it would otherwise have sent the turn a second time (#82). The mark comes off only
  when the call provably went nowhere, as above (that includes a `t3` call cancelled before it could
  start). A mark that can't be saved stops the handoff: the message is a notice, as after any failed
  delivery. The save is a write to the file, which a power cut can still lose. A crash between the
  save and the handoff leaves the mark on a message that was never sent, which is the safe way
  round: it is a notice, not a repeat.
  The other safe way is to look in T3's database for the message before trying again
  (`orchestration_v2_projection_messages`, by thread and text, or `t3 thread show --json`); it
  would keep the second try, and it relies on tables that are T3's own and may change in any
  update, so it wasn't done. Nothing is delivered for a user who has left either (see Joining a
  repo). A message that waited more
  than 15 minutes (T3 closed, the session away) is not sprung on the agent as a turn: it stays a
  notice. The 15 minutes count from when the daemon puts the message in the inbox, so one read
  after a long outage (2026-10-30, #30) is young and wakes its thread.
  The page's wake setting (#22) set to "never", for the device or for one session, keeps
  the notice too; otherwise delivery is at once. A daemon that starts looks at the messages its
  inboxes kept, and delivers those that are still young enough.
- **No loop guard.** Nothing in Murmur limits how often two agents answer each other (Tyler's
  decision, 2026-10-29). A message to a busy thread is queued as its next turn, not injected into
  the one running, and the AGENTS.md wording asks for coordination, not chatting; beyond that
  only the agents stop a loop.
- **Speed.** The daemon polls the remote every 3 s around a conversation, not 10 s (2026-10-29), and
  delivers the moment a message is fetched. Measured between two T3 threads on one laptop through GitHub over HTTPS:
  about 6–8 s from an agent's `send` to the other thread's turn starting (a push, 1.2 s; up to
  one poll; `ls-remote`, 1.2 s; a fetch; `t3 thread send`, 1.1 s), and a question answered by
  another agent's reply in about 19 s. GitHub documents no limit on git reads, and a test of 63
  `ls-remote` requests about 2 s apart drew none, which is a short test, not evidence about
  hours of them from every device of a team. A poll that fails waits twice as long each time, to
  at most a minute (or 20 polls, if Poll is shorter), and goes back to its usual rate when it works.
- **How often it polls (2026-10-30, #93).** Tyler: agents don't need real-time communication, and
  4–7 s a hop is fine. So a daemon polls fast only when a reply is likely, and a session left open
  for hours, which would cost 1,200 `ls-remote` calls an hour at 3 s, costs 240.
  - *Fast, every 3 s (`Poll`), for 3 minutes (`FastFor`)* after this device sends a message, or
    delivers or receives one for a session on this device, or a session starts here. A reply
    usually comes within a minute or two (the other agent's thinking time plus two hops), and each
    move restarts the 3 minutes.
  - *Otherwise, with a session live or a page open,* the wait doubles after each poll that follows
    FastFor's end: 3 s, 6, 12, up to 15 s (`PollCap`). Doubling reaches the cap in three polls; a
    first message of a conversation then waits at most 15 s, plus the delivery.
  - *Quiet:* with no session live and no page open the poll is every 30 s (`QuietPoll`, #87),
    whatever was said lately. A session registering, or a page opening, makes it look at once, and
    a session starting is itself a move, so it polls fast for the 3 minutes.
  - *Not moves:* presence heartbeats and status changes, which arrive all the time from active
    teammates and don't predict a reply, and other devices' messages for sessions elsewhere.
  - A failed poll keeps the back-off above, from whatever the rate was.
  - All four numbers are options, so a test can shrink them.

### Counting active users

Tyler's goal for the project is 100 active users (2026-11-02), and the transport hides them: every
team's traffic stays in its own repo, and release downloads count installs, not use. So each
daemon checks in (#148, `internal/checkin`): when it starts and every hour after, if this clone
hasn't checked in this UTC day, it posts to `https://murmur.afig.dev/v1/checkin`:

- `user`: 16 random bytes in hex, made once per user account on the machine and kept in the user
  config dir (`murmur/id`), so every clone of that account is one user;
- `team`: the first 16 bytes of a SHA-256 of the bus remote's address, reduced to host and path
  (https and ssh clones of one repo agree). It isn't salted, so whoever holds the stats can confirm
  a repo they can name; nobody can list the repos from it;
- `version`, `os`, `arch`.

The answer is the latest release, kept in the state dir's `checkin.json`; `mur version` says so on
stderr when it is newer. A daemon runs only while a joined clone has a session open, or for 10
minutes after, so a check-in means someone used Murmur that day. "Active" is a check-in in the last
7 days. The site keeps one row per user, team and day in D1 (`site/migrations`), no IP addresses,
and shows the counts on a page at `/stats` that only a key opens (`site/src/usage.js`): Tyler's, or one
he gives an agent, which reads them as markdown at `/stats/report.md` or JSON at `/stats/data` (#150).
`DO_NOT_TRACK` or `MURMUR_NO_USAGE` in the daemon's environment, or `murmur.usage` false in git
config, turns check-ins off; a development build (`-dev`) never sends one. join.md tells the user
before they agree.

What it can't see: a person with two machines is two users, and one who turned check-ins off is none.

## Risks and how the design meets them

- **Agents replying forever.** T3's local messaging stays calm because a reply is plain text in
  the receiver's own thread; nothing goes back unless the agent deliberately sends again. Murmur
  keeps that: no automatic replies, incoming messages queued rather than steering a running turn,
  and AGENTS.md wording that asks for coordination, not chatting. No limits or caps on top of that,
  now that a message starts a turn without anyone typing (Tyler's decision, 2026-10-29): two
  agents that keep answering each other are stopped by nothing but the agents.
- **Prompt injection.** Anyone with push access can now put text in front of your agent. That
  matches the trust model (a hackathon team), and a delivered message is text in front of the
  agent as a turn of its own, delivered as written; messages never get to bypass the receiving
  user's permission mode, and the framing's first line keeps their origin visible. Seals make
  that line true: a message from a device that seals can't be forged by another (see Seals).
- **Leaked secrets.** Everything on the bus stays in repo history, readable by anyone with read
  access, and agents do paste `.env` values. Everything a session sends (its messages and status
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
   Done 2026-10-29 (#56, #57): the daemon delivers a message with it.
4. Codex specifics.

## Open questions

- The name.
- Should the page's messages outlast a daemon restart? `mur ui` often starts a fresh daemon, which
  has read nothing yet, and the page opens empty. Recent bus commits could be read at start; no code
  does that yet.
- A worktree made after joining has the tools (the consent is per clone) but no hook, until
  `mur join` runs there again. Nothing prompts that, since the agent in it already has the tools.
  Should the server or the daemon install the hook there?
- Does Claude Code's messaging socket start a turn in an idle session? Its protocol is known
  and a message written to it arrived in a working session (2026-10-29); an idle one hasn't been
  tried.
- Does GitHub rate-limit a `git ls-remote` every 3 s (around a conversation) from each of a team's
  devices? Nothing
  documents a limit for git traffic; none was hit in a test of 63 requests 2 s apart.
