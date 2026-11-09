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
only collaborators can push, so only they can join and send (#72), but anyone can read the bus.
An encrypted mode for teams that want it private is #156. Public repos were on the list below
until Tyler took them off on 2026-11-03.

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
  along. The daemon pushes them with `--no-verify` and doesn't sign them: a `pre-push` hook that
  runs the test suite, or a signing prompt, on every message would make the bus unusable. These
  commits only ever go to `refs/murmur/*`, never to a branch.
- Nor does a record carry a signature of the device's own, so a device can write records that
  claim to come from another (#155). Only accounts with push access can write `refs/murmur/*`
  (a fork can't), and any of them could already push code. A signature would stop one of them
  posing as another teammate's device, and little else: a message gets no authority from who sent
  it, and an agent can inject a prompt under its own name. A full version was built and set aside
  on 2026-11-03 (Tyler's decision): Ed25519 keys per device, pinned on first sight, with replay
  checks, about 860 lines, on branch `bus-signing` (PR #159, closed unmerged). Encryption (#156)
  would need it back, to know whose a box key is.
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
`.git/murmur/daemon.json`. A daemon with no sessions, no open page (see The page for humans) and no
T3 thread to wake for 10 minutes exits. Any daemon that stops deletes its presence, so other devices drop it at their
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
  | The skills, `murmur` and `murmur-pro` (`internal/skills`) | `skills/<name>/SKILL.md` in the home of Claude Code, Codex, OpenCode and Pi; Muse Code and Grok document no skills folder (2026-11-08) | user |
  | The hook, on `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit` and `Stop` (`setup.HookEvents`) | `.claude/settings.local.json` in each worktree of the clone, kept out of `git status` through the clone's `info/exclude` | clone |
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
  before each push of its own (presence, the names registry) and before each handoff of a message
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
- **Delivery.** Inside T3 the existing `t3 thread send` handoff needed nothing new: an OpenCode and
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
  `.git/murmur/daemon.json`, with a token of its own (`pageToken`, #42): the sessions' token lets
  its holder send as this device, and the page's address, which carries its token, reaches the
  browser's history and extensions. A daemon that can't open the port logs that and keeps running
  without the page; `mur ui` then says there is none.
- **Opened by `mur ui`**, which starts the daemon if it isn't running, opens the default browser
  and prints the address. The page's token is in the address's fragment (`#token=`), which a browser
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
- **How it's laid out** (#179, 2026-11-08), after Discord and Slack: sessions down the left under
  their devices, then a column of conversations (direct messages and group chats in one list, newest
  first, narrowed to either with a switch), then the open conversation. A conversation is two sessions' messages to each other, a device's, or
  everyone's: the bus has no threads. Each face carries a dot: green active in the last five
  minutes, yellow idle, red do not disturb (the `do-not-disturb` tag from #211, or wake-ups set to
  Never), grey when its device is unreachable. A settled session (#189) is absent from presence but
  still in the messages, so the page folds it under its device and keeps its conversations, out of
  the way; a direct message with a settled session folds away too. Sessions in Claude Code, Codex,
  pi or opencode never settle: one left open in a forgotten terminal stays in presence for good.
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
  would repeat it, and a filter shows one group at a time. The page is always dark: the design was only ever drawn dark, and a light one would be a
  second design. Started times, worktrees and committed branches left the page with the old
  Sessions view; `lookup` still has the branches.
- **Settings** (when a session is woken, and what a busy one is given by the hook) are saved in
  `.git/murmur/settings.json`, replaced whole; "Delivering a message as a turn" says what they do.
  Mutes (#27) aren't built, so the page shows none and refuses to unmute.
- **Open page, running daemon.** The page asks for its state every 3 s, 15 s in a hidden tab. A
  call that carries the token, from a page someone can see, counts as an open page for 2 minutes,
  and an open page stops the 10-minute countdown the way a session does: a daemon with no sessions
  and no T3 thread to wake exits about 12 minutes after its page is closed or hidden (a background
  tab, a minimized window, a locked screen). A hidden page marks its calls, so a tab forgotten in
  the background doesn't keep the daemon, and its polling of GitHub, up for good (Opus review,
  2026-10-29). A request without the token doesn't count.

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
  held from before the claims has the first daemon to claim it keep it. The other person's page asks
  them for another, and until they choose one they can't send, as their messages would carry a name
  that reaches someone else. The cost: choosing a name needs the remote, as sending does.
- **A person's direct message wakes a session past the "Within" window**, because they are waiting
  for an answer. It doesn't wake past Never or do not disturb, which a person also chose. A broadcast
  from a person wakes nothing extra: one sentence shouldn't wake every idle session on the mesh. The
  turn says who it is from, that they see only what the agent sends, and how to answer.
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
- **The draft survives a rebuild.** The page rebuilds its DOM on every state change, so each
  conversation's textarea is kept and reused, which keeps its text, caret and focus when a reply
  arrives.

Desktop notifications aren't built: the page's port is random, so a browser would ask permission
again after each daemon restart. The tab's title carries the unread count instead.

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

### Session tags

#198, first part built 2026-11-08: tags set by hand, the model, and search by tags. Groups, rules in
a tag file, subscriptions and the lifecycle state come later, in the issue's order. The first
version also had an availability state (#186): `unavailable` made `send` refuse a session and
broadcasts skip it. Tyler had it taken out before merging (2026-11-08), so no tag carries behaviour:
`unavailable` is a label like any other. Tyler then asked for `do-not-disturb` (2026-11-08), the one
tag with behaviour, below.

- **What a tag is.** Up to 40 lowercase letters, digits and `. _ : / -`, at most 12 on a session
  (`internal/tags`). A tag under `model:` names the session's model, and a session has one at most.
- **`do-not-disturb`** keeps a session's messages queued in its inbox. None is handed over as a
  turn (the daemon treats the session as one it may not wake). The hook delivers neither a notice
  nor the whole message (#204), holds none for the turn's end (#202), and the `Stop` hook doesn't
  keep the turn going with them (#213), even while the session is at work. `inbox` delivers them as
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

**What "delivered" means.** Tyler's rule (2026-11-07, #187): a message is *delivered* only once it
has reached the agent's context. Before that it is *queued*, wherever it waits: in the recipient
daemon's inbox, in a T3 thread's queue behind a running turn, or for the hook's next run. Delivered
is what a sender acts on, so a looser word would let a sender believe an agent knows something it
hasn't seen, and may not see for hours if it is idle and nothing wakes it (#25). The words, in the
order a message meets them:

- *Queued:* the recipient's daemon has put it in the session's inbox. The daemon knows.
- *Handed over:* the daemon has passed it to the session's client, by `t3 thread send` or the
  messaging socket. Still queued, in the client now, and out of the daemon's inbox.
- *Delivered:* it is in the agent's context. Where that moment is, by route:

| Route | Delivered when | Can Murmur see it |
|---|---|---|
| `t3 thread send` | the turn carrying it starts: at once in an idle thread, after the running turn in a busy one (a busy one only where the hook doesn't run, #202) | No. The daemon sees `t3` accept the message, not the turn start; T3's database would show it (`orchestration_v2_projection_messages`), but those tables are T3's own and may change |
| The messaging socket (Claude Code outside T3) | Claude Code takes it as a turn | No: the socket answers nothing |
| The hook's notice (Claude Code) | the hook's `additionalContext` is added, after the agent's next tool call or at its next prompt. That delivers the sender and subject, or the whole message where the page's busy setting says so (#204); otherwise the body is delivered by `inbox` | Partly: the daemon sees the hook fetch the notices (`Noticed`), not the hook's output reach the agent |
| `inbox` | the tool's result returns to the agent | Partly: the daemon sees the messages leave in its answer, not the answer reach the agent through MCP |

Nothing reports a message's state to its sender yet. Whatever does (#26, `who`, `lookup`, the web
page) shows queued, handed over and delivered as different states, and a handed-over message as
delivered only once something can see the turn start.

The daemon hands a message over at once, when it arrives for a session it can reach, unless the
hook has the session at work (see "A session at work", below):

| Where the session runs | Handed over by | Starts a turn in an idle session |
|---|---|---|
| T3 Code: Claude Code, Codex, OpenCode or Muse (and Pi and Grok, not tried) | `t3 thread send <thread> -`, the text on standard input. T3 starts the thread's turn if it is idle and queues the message as its next turn if it is busy, interrupting nothing. A busy session the hook runs in (Claude Code) is not handed the message: the hook tells it | Yes (seen 2026-10-29: Claude Haiku to Claude Haiku, and Claude Haiku to Codex gpt-6.1-sol, in two real T3 threads, nobody typing) |
| Claude Code outside T3 | the session's messaging socket | Unverified: a message Murmur's code wrote to a session's socket arrived as a turn while the session was working (2026-10-29, Claude Code 2.1.312, Windows); an idle one has not been tried |
| Codex, OpenCode, Muse, Pi or Grok outside T3, or any handoff that fails | the hook's notice where the hook runs (Claude Code), and `inbox` | No |

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

- **Out of the inbox when handed over.** A message handed over leaves the receiving session's
  inbox, so neither the hook nor `inbox` shows it again: the client has it now, and delivers it as
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
    agent's last word. The messages are delivered once the daemon answers the hook, so a hook that
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
    read, a long body cut the same way, and the message is then delivered: it leaves the inbox and
    the count, and the page and the daemon's log say it went through the hook. Subject leaves the
    agent's focus to the agent, at the cost that it may pass over a line it needed; whole puts
    every message in front of it mid-task, at full length, whether or not it matters to the task.
    A message given whole counts as delivered once the daemon answers the hook, so a hook that gave
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
  - *Where the hook doesn't run* (Codex, OpenCode, Muse, Pi and Grok in T3, as of 2026-11-08),
    nothing tells the daemon the session is at work, and a message goes to T3 as before: without
    the handoff the session would hear nothing at all. #205 brings those agents in line.
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
SHA-256, and stages a closed, synced executable beside the running binary's resolved symlink
target. Unix renames it atomically over the old file. Windows renames the running exe to a unique
backup, installs the staged file, and restores the backup if installation fails. Backups still
held by processes are cleaned on a later daemon start. A lock beside the executable serializes
clones sharing an install; a daemon whose executable has already been replaced restarts onto it.
Identity is checked again after downloading so a manual reinstall during the download is left alone.
An unwritable install (for example, a package manager's) is logged once and left alone until
restart. Successful updates log the old and new versions in the daemon log.

After installation, the daemon waits until no message handoff is in flight and no page counts as
open, or until the page asks for the restart (#240). It then blocks new handoffs and shuts down
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
public repo's bus is readable by anyone, so once we know the address, its repos, messages and
people can be counted from the bus itself, and the address is already public, so there is no
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
   `site/migrations`).
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
each repo's `refs/murmur/*` and its default branch's commits without their files, and sends the
site counts and hashes, never content: per UTC day, the sessions started (each once, though a
daemon that restarts announces its sessions again), the messages and all records, and a hash of
each device that wrote anything, presence included; a hash of each device's owner, the git user name
its presence shows; and for each contributor, a hash of their GitHub login or email and the day of
their first commit, so one person in two repos counts once. The site only ever raises a count and
keeps every device and owner it has seen, since a bus log can be rewritten and a daemon deletes its
presence when it stops. It runs from GitHub,
not from the Worker, because counting needs git, and a fetch of the whole bus is one request where
the API would need one per commit. Once the bus is encrypted (#257) a record's kind is hidden from a
public reader, so messages stop being told from other records, and only records are counted.

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
  matches the trust model (a hackathon team), and a delivered message is text in front of the
  agent as a turn of its own, passed on as written; messages never get to bypass the receiving
  user's permission mode, and the framing's first line keeps their origin visible.
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
   Done 2026-10-29 (#56, #57): the daemon hands a message over with it.
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
