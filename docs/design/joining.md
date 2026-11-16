# Joining a repo

Decided 2026-10-29 (#55), built the same day. Tyler's requirement: setup should feel like magic, and
T3 Code users are the main customers, but the user should still know Murmur is running and choose to
join. So a repo that uses Murmur says so in a section of `AGENTS.md` and `CLAUDE.md`; an agent
without Murmur's tools asks its user whether to join; and on a yes it installs mur and runs `mur
join`, which does the rest. Tyler also decided that messaging gets no commands (`who`, `send`,
`inbox` and `log` were removed): "If everyone is using the CLI, there will be no one to message."
Agents message through MCP, and people use the page.

## The marker

- **`mur init`** writes a section between `<!-- murmur:start -->` and `<!-- murmur:end -->` into
  both files, creating either if missing and rewriting the section in place on a rerun. It is six
  lines, since every agent that opens the repo reads it whole (Tyler, 2026-10-29, after a first
  version of about twenty): the repo uses Murmur, and two links into `docs/agents/` by raw GitHub
  address, which work without login. `join.md` is for an agent without the tools (ask the user, say
  what joining does, install, run `mur join`); `use.md` is for one with them. A test holds the links
  to files that exist. An older `init` put an `.mcp.json` entry and a hook in
  `.claude/settings.json`; `init` now removes Murmur's own entries from those. Committing installs
  nothing on a teammate's machine.
- **`init` pushes its commit to the default branch** (Tyler, 2026-11-01, #119). Run in a T3
  worktree, the old "review and commit" landed the section on that worktree's branch at best, so
  teammates' agents on main never read it. Now `init` builds the commit on the remote's default
  branch with a temporary index and `commit-tree`, touching no working tree, and pushes it, never
  forced; a push rejected because the branch moved is rebuilt on the new tip, up to 3 tries. One the
  server refuses for its own reasons (a protected branch) goes to a branch `murmur-init`, and `init`
  prints the page that opens a pull request from it. The checkout is fast-forwarded only when
  nothing local stands in the way (git refuses a pull over an uncommitted change to a file the pull
  brings, even an identical one, checked 2026-11-01). `init --no-push` is the old behavior.
  `docs/agents/setup.md` is the page an agent follows when asked to set Murmur up.

## What `mur join` writes

It refuses in a repo without the marker. Everything it writes is outside the repo's tracked files:

| What | Where | Per |
|---|---|---|
| Consent | `.git/murmur/joined.json` (0600) | clone, so every worktree of it |
| The MCP server, for Claude Code | `claude mcp add --scope user murmur -- <absolute path of mur> mcp` | user |
| Permission for its tools | `mcp__murmur` in `permissions.allow` of Claude Code's `settings.json` | user |
| The MCP server, for Codex | `[mcp_servers.murmur]` in `config.toml` of `$CODEX_HOME`, else `~/.codex`, with `env_vars = ["T3CODE_CLI_PATH", "CODEX_HOME"]` and `default_tools_approval_mode = "approve"` | user |
| The MCP server, for OpenCode, Muse Code, Pi, Grok and Antigravity | see [other agents](clients.md) | user |
| The skills, `murmur`, `murmur-pro` and `murmur-manager` (`internal/skills`) | `skills/<name>/SKILL.md` in the home of Claude Code, Codex, OpenCode and Pi, and `config/skills/` in Antigravity's; Muse Code and Grok document no skills folder (2026-11-08) | user |
| The hook for Claude Code, on `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `UserPromptSubmit` and `Stop` | `settings.json` in each Claude Code home ([Claude Code's hook is per user](clients.md#claude-codes-hook-is-per-user)) | user |
| The hook for Codex, on `UserPromptSubmit`, `PostToolUse` and `Stop`, approved | `hooks.json` in each Codex home, its trust in `config.toml` there ([Codex's and Grok's hook](clients.md#codexs-and-groks-hook)) | user |
| The hook for Grok, on `PostToolUse` and `Stop` | `hooks/murmur.json` in Grok's home | user |
| The hook for Muse Code, on `UserPromptSubmit`, `PostToolUse` and `Stop` | `hooks` in each Muse home's `settings.json` ([Muse's hook](clients.md#muses-hook)) | user |
| The hook for Antigravity, on `PreInvocation` and `Stop` | a `murmur` entry in `config/hooks.json` in each Antigravity home ([other agents](clients.md#antigravity)) | user |
| The daemon | started, if none is running | clone |

- **The skills** carry the judgment the tool descriptions don't: when Murmur is worth a turn, and
  how to coordinate over several messages (#206). The third, `murmur-manager`, has a session run a
  team of agents as its user's COO (#548); a model may load it on its own, so it opens by saying to
  follow it only when the user has made the session the manager in words. A SKILL.md of the same name without the skills'
  marker comment is the user's own and is left alone. They cost every session of those agents their
  descriptions' worth of context in any repo, so each description says it applies only where the
  repo has the Murmur section. The folders are from each agent's documentation, not tried in a
  session (2026-11-08). OpenCode also reads `~/.claude/skills`, so beside Claude Code it sees each
  skill twice.
- **Edited, never replaced.** A client that isn't installed is skipped. Other servers, settings and
  comments stay, and a form join can't edit safely is reported, not guessed at; `codex mcp add`
  isn't used because it rewrites the whole file. The server's path is absolute, so it starts
  whatever PATH the client has. Claude Code runs hook commands through a shell, which would split a
  path with a space and eat a Windows path's backslashes, so the hook command is the path in double
  quotes with forward slashes (measured under Claude Code 2.1.312, 2026-10-29). A config file that
  is a symbolic link (a dotfiles repo's) is written through. A hook file the repo tracks gets no
  hook (2026-11-09), since it would carry this machine's path to mur into the next commit.
- **Run again**, `join` changes nothing and says so; it fills in what is missing and repoints an
  entry whose binary moved.
- **`mur join --leave`** removes the consent and the hooks and stops the daemon. It leaves the
  server registered, since that is per user and does nothing where the user hasn't joined, and says
  how to remove it.
- **What it prints**: what it did, what went wrong, and "Tell your user", chosen from which agent
  ran it (see [reloading MCP servers](#reloading-mcp-servers)), so the agent can say exactly what to
  do next.

## `mur uninstall`

Tyler, 2026-11-10, #399. A leave can't remove the per-user entries, because one entry serves every
repo the user joined and a leave can't tell whether its repo was the last. Uninstall can, because it
reads the record of this machine's clones (`internal/clones`).

- It refuses while a clone on the record is still joined, or with `--leave` leaves each first. It
  then takes out what join wrote per user in every agent configuration join registers in, T3's
  instances included, by join's rules: links written through, JSON re-parsed before it is written, a
  form join wouldn't edit refused, and the user's other entries kept byte for byte.
- Codex keys a hook's trust by its group's place, so the user's own groups after Murmur's move up
  one and their trust entries are renamed with them, and Codex doesn't ask about them again.
  `hooks.json` goes back as it was if a `config.toml` can't be written: once Murmur's groups are
  out, nothing tells a rerun where they were.
- A clone whose cleanup didn't finish, or whose folder isn't there now, stays on the record so a
  rerun finds it (Sol's reviews, 2026-11-10). Murmur's folder in the user's config directory goes
  last, unless a running MCP server still needs its record there; it also held `id`, the random user
  ID 0.2.3's check-ins sent, which nothing reads since #199 (#430).
- It leaves each repo's committed section, each clone's `.git/murmur` and bus refs, and the binary,
  which Windows can't delete while it runs, so it prints the command that does. A clone that joined
  before the record existed and whose daemon hasn't started since isn't on the record; its server
  entry is gone anyway, so nothing runs there.

## The server stays passive where it isn't invited

The user-scope entry starts `mur mcp` in every repo the user opens. The server is on only while the
repo says it uses Murmur (this working tree's `AGENTS.md` or `CLAUDE.md` has the marker, or the
clone's remote-tracking default branch does, so a worktree on a branch made before `init` pushed
isn't left out, #119) and this user has joined in this clone, checked every 2 seconds (the
remote-tracking branch, which needs git, at most every 20). Off, it advertises a tools capability
with no tools, contacts no daemon, and answers a stray call with why it is off. A join turns it on
through `tools/list_changed`, and a leave turns it off. MCP instructions are fixed at initialize, so
a server that started off never has them; the pages the section links to carry that guidance.

Nothing relies on the server alone, and each check sits where its harm would be:

- The server asks again before each registration, before starting a daemon and before forwarding a
  tool call, since its 2-second look can lag a leave.
- The daemon is per clone, so it needs only the consent. It checks at start, after its first sync,
  on every request but `stop` and `ping` (which reveal nothing, and are how a leave finds it),
  before each push of its own, before each handoff of a message as a turn (the one thing it does to
  the user's other programs), and once a second, stopping itself if the user has left. `mur daemon`
  and `mur ui` refuse in a clone that hasn't joined.
- `join --leave` forgets the consent before it stops anything, then waits up to 40 s for the
  daemon's lock (20 until #91, shorter than a shutdown may take), and reports a problem if it is
  still held. What it can't close: a push already past its check completes, and a starter already
  past its own check can still launch a daemon, which exits at its consent check before it opens any
  state.

A repo without the marker, or one the user hasn't joined, gets no daemon, no `refs/murmur/*` and
nothing published. A clone whose user joined but whose current branch lacks the marker keeps its
daemon and gets no tools there.

## Every account T3 runs

T3 can start Claude Code with its own `CLAUDE_CONFIG_DIR` (a provider instance with a home path, as
`claude_personal` on Tyler's laptop, 2026-10-29) and Codex with its own `CODEX_HOME`. Until
2026-11-01 join wrote only to the home of the agent that ran it, so a thread under Tyler's other
account had no tools and nothing said why. Now it also reads T3's `userdata/settings.json` and
registers in the home of every provider instance that is on, following T3's own code for which are
on and where their homes are (`internal/join/t3homes.go`). An instance that is off is named under
"Not done". The file holds API keys, so join decodes only those fields, takes an environment value
only by name and only if T3 doesn't mark it sensitive, and never prints the file.

## A session that started outside the repo

2026-11-08, #200. The MCP server picks its repo from the directory the agent started it in, so a
session started in a parent folder, the home directory or a workspace of several repos never got a
name in the repo it then worked in. Now each server publishes a record of itself (its PID, its
agent's PID, Claude Code's session ID, its clone, whether it is on) in the user's config directory,
`murmur/sessions/`, and `mur join` finds the record of the session that ran it: the nearest ancestor
of the command that is a server's parent. `CLAUDE_PID` is used only where ancestors can't be read,
since an agent Claude Code starts inherits it and would move Claude's session instead of its own.
Join leaves a move, which the server picks up on its next look: it turns off where it was and on in
the joined clone.

- **One repo at a time** (Tyler accepted either answer; one was chosen because the tools take no
  repo, and two meshes would need one on every call and every message), so a move takes the session
  out of the repo it was in. A move doesn't stand in for consent.
- **Where sessions can't be told apart**, nothing moves. Where one process runs several sessions
  (Codex's app server in T3), join moves none and says so. OpenCode's one server serves every
  session in its directory, so it can't be moved; it publishes a record marked fixed, so a join from
  an OpenCode that Claude Code started is refused rather than moving Claude's session.
- A move carries the agent's start time and session ID, so a new process that took a dead agent's
  PID doesn't follow it. Moving ends the old registration, and any call still on its way to the old
  daemon. A session that moves in lacks the new repo's MCP instructions, which can't change after
  initialize.
- It lets a team keep a repo just for Murmur and join its clone from wherever they work (`mur join
  -C`, `docs/agents/different-repos.md`). Commits made in the other repo aren't credited ([Claude
  Code's hook is per user](clients.md#claude-codes-hook-is-per-user)).

## Reloading MCP servers

Measured 2026-10-29 on Claude Code 2.1.312, Codex CLI 0.164.0 and T3 Code: what a session needs
after `mur join`, since the agent that ran it started before the server was registered.

| Client | A server registered after the session started | `tools/list_changed` from a running server | What loads it |
|---|---|---|---|
| Claude Code | Ignored | Honored | A restarted process |
| T3 Code, Claude | Ignored | Honored | Press Stop, then send any message: T3 restarts the Claude process |
| T3 Code, Codex | Ignored (2026-10-30: a new thread loads it from `config.toml`; an older one still lacks it on its next turn) | Ignored | A new thread |

Because `list_changed` is honored where it matters, the server is registered once per user and
starts in every session as a passive server: after the first join, a Claude Code session in any repo
gets the tools the moment that repo is joined, with no restart. A session that began before the
registration needs the restart in the table, and `join` says which for the agent that ran it
(`CLAUDECODE` and `T3CODE_CLI_PATH` in its environment): in T3, Stop and a message; in a terminal
Claude Code, exit and `claude --continue`; in Codex, a new thread. Not verified: that `claude
--continue` loads the server, that Codex honors `default_tools_approval_mode = "approve"` in a live
thread, and which variables Codex sets for its commands (with none, `join`'s advice covers every
client).

In T3 (Codex 0.164.0, 2026-10-30), a new Codex thread loads a stdio server from
`~/.codex/config.toml` with no approval prompt in full-access mode, and a thread from before the
server was added lacks it on its next turn. Codex's app server has a `config/mcpServer/reload`
request that would refresh a running thread, but T3 never sends it, so `join` tells a Codex thread
in T3 to open a new thread. Whether a project-level Codex config is read, and approval outside
full-access mode, were left to #55.
