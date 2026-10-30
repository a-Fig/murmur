# t4

**Intra-repo agent communication.**
**Your agents work better as a team.**

Coding agents on different teammates' machines, working in the same repo, that can find each
other and coordinate: who is building what, and messages between them when that isn't enough.
Built for small teams at hackathons, on private repos.

[T3 Code](https://github.com/pingdotgg/t3code) already lets sessions on one machine message each
other. t4 extends that across machines, and to Claude Code and Codex sessions as well as T3's.

**Status:** V1, 2026-10-26: the daemon, CLI, MCP server and Claude Code hook work between two
machines through GitHub. "t4" is a working name.

## How it works, in brief

- The repo is the message bus: each device appends events to its own git ref
  (`refs/t4/mesh/<device>`) on GitHub. No server, no accounts; whoever can push is in. Pushes to
  these refs trigger no Actions run and no push webhook (tested), so deploy services that listen
  for pushes shouldn't fire either (inferred). `git clone` / `git pull` never fetch them.
- One small daemon per clone syncs those refs; each agent session connects to it through an MCP
  server (or a human through the CLI).
- Agents see each other's **presence** (who is working where, on what) and send **messages**.
  A message shows up as a one-line notice (sender and subject); the agent reads the body with
  the `inbox` tool.

[`docs/VISION.md`](docs/VISION.md) covers the problem, the design and the reasons for it, the
risks, and the build order.

## Using it

Each teammate needs Go 1.26 or later once, to build, and git 2.31 or later:

```sh
git clone https://github.com/a-Fig/t4 && cd t4
go build -o ~/bin/t4 ./cmd/t4      # anywhere on PATH; t4.exe on Windows
```

Then, once per project repo, one person runs `t4 init` in it and commits what it writes:
`.mcp.json` (the MCP server for Claude Code), `.claude/settings.json` (the hook, which delivers
notices and watches for commits) and a section of `AGENTS.md`. Claude Code asks each teammate
once whether to trust the MCP server. After that, agents use the `who`, `lookup`, `status`, `send` and `inbox` tools; nothing else needs
starting. A repo set up by an earlier version needs `t4 init` run again for the new hook events.

Humans get the same through the CLI (`t4 help`): `t4 who`, `t4 send <to> <subject> [body]`,
`t4 inbox`, `t4 log`, and `t4 lookup <name>` for what is known of one session. `t4 ui` opens a
web page with the sessions and the messages between them.

## Working on t4

`go test ./...` runs everything, including tests that run two devices against a local bare
remote through real git; `docs/testing.md` says how a change is checked. Code lives under
`internal/`; `internal/daemon` is the core.

## Not for

Public repos, large teams, long-lived projects, or teams whose members don't trust each other's
agents. See "Who it is for" in the vision doc.
