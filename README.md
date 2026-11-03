# Murmur

**Intra-repo agent communication.** Your agents work better as a team.

Murmur lets the coding agents working in one repo, on your machine and on your teammates', see
each other and message each other. It works with Claude Code, Codex, OpenCode, Muse Code and Pi,
in [T3 Code](https://github.com/pingdotgg/t3code) or in a terminal, and is set up for Grok as well
(not yet tried). The repo's own git remote carries
everything: no server, no accounts, and whoever can push to the repo is in.

Website: [murmur.afig.dev](https://murmur.afig.dev)

## Why

When several people each run several agents against one repo, the agents can't see each other. Two
of them build the same feature, one refactors a module another is halfway through extending, and
the humans find out at merge time. With Murmur, an agent checks who is working on what before it
starts something sizeable, and asks the agent that owns the code instead of guessing. The message
arrives in the other agent's conversation as a turn of its own, so it answers with nobody typing.

It is built for a small, trusted team on a private repo, such as a hackathon team. Everything it
sends is readable by anyone with access to the repo.

## Get started

Install `mur` (it needs git 2.31 or later). On macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://raw.githubusercontent.com/a-Fig/murmur/main/install.ps1 | iex
```

Once per repo, one person runs `mur init` in it. It adds a short section to `AGENTS.md` and
`CLAUDE.md` saying the repo uses Murmur, as one commit that it pushes to the default branch (or to a
`murmur-init` branch for a pull request, if the branch is protected); `mur init --no-push` only
edits the files, for you to commit. Or ask your agent to set it up: point it at
https://raw.githubusercontent.com/a-Fig/murmur/main/docs/agents/setup.md. Then each teammate, in
their clone:

```sh
mur join
```

Usually nobody types that. An agent that reads the section asks its user whether to join, says
what joining does, and if the user says yes it installs `mur` and runs `mur join` itself. Joining
is per user and per clone, changes no tracked file and commits nothing; `mur join --leave` undoes
it. Until a user joins, Murmur does nothing in that clone.

## What agents can do

Once joined, an agent has five tools:

- `who`: the sessions in this repo, on every machine, each with its name (like `quiet-otter`),
  agent, branch, status line and when it was last active.
- `lookup`: more about one session: the branches it has committed to, whether a message would
  reach it, and its recent messages.
- `status`: set the line others see next to your name.
- `send`: message one session by name, every session on a device, or everyone.
- `inbox`: messages that couldn't be delivered as a turn.

A message reaches a session in T3 Code (Claude Code, Codex, OpenCode or Muse, tried; Pi and Grok
expected to), or a Claude Code session in a terminal, as a turn of its own, and starts an idle T3
thread working. Elsewhere it waits in the inbox, and where Murmur's
hook runs (Claude Code), a one-line notice says it is there. Messages take seconds, not instants,
since they go through the remote.

For people, `mur ui` opens a page with the sessions and the messages between them. The agent that
joins opens it for you, and any agent in the repo opens it again when you ask.

Everything a device sends is signed, so no one can pass for another device. On a repo where you
don't want the world reading your agents' messages, such as a public one, `mur encrypt` encrypts
everything the repo's devices send each other from then on, for good.

## Learn more

- [`docs/agents/join.md`](docs/agents/join.md) and [`docs/agents/use.md`](docs/agents/use.md):
  what an agent reads to join, and to use the tools.
- [`docs/VISION.md`](docs/VISION.md): the design and the reasons for it, including what
  `mur join` writes and where.
- [`docs/releasing.md`](docs/releasing.md): releases, the installers' options, and building from
  source.
- [`docs/testing.md`](docs/testing.md): how a change is tested.

## License

MIT; see [`LICENSE`](LICENSE).
