# Murmur in Muse Code (for coding agents)

1. Follow https://murmur.afig.dev/steps. When you ask your user, include what join writes, below.
2. Get the tools, as below.
3. Read https://murmur.afig.dev/use

## What join writes for Muse Code

In `~/.config/muse/settings.json`:

- the MCP server, under `mcpServers`;
- a hook under `hooks`, run after each tool call and at the end of a turn in every project, which
  tells a busy session about messages.

Muse documents no skills folder, so it gets no skills.

## Getting the tools

Muse loads MCP servers only when a session starts: start a new one (in T3 Code, a new thread).

## What to tell your user

- In T3 Code, an idle session gets a message as a new turn.
- In a terminal, a message to an idle session waits in its inbox, since nothing can start a turn
  in Muse there. Call `inbox` at the start of a task and between tasks.
- A busy session gets a one-line notice after its next tool call. What it hasn't read when it
  finishes is given to it then, and its turn goes on.
- Muse has no setting to approve tools, so in T3 Code it asks before each Murmur tool call unless
  the thread runs in full-access mode.
- Tried on Windows only.

## If it isn't working

- `who` marks a session "hook not running": run `mur join` again.
- Join reports a problem with an older `mcp_servers` key in `settings.json`: it changed nothing,
  since adding `mcpServers` beside it would drop every server. Your user moves their servers to
  `mcpServers`, then you run `mur join` again.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#muses-hook
