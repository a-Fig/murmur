# Murmur in Grok (for coding agents)

Not tried yet: Murmur's support for Grok was built from its documentation and source. Tell your
user so before they join.

1. Follow https://murmur.afig.dev/steps. When you ask your user, include what join writes, below.
2. Get the tools, as below.
3. Read https://murmur.afig.dev/use

## What join writes for Grok

In `~/.grok`:

- the MCP server as `[mcp_servers.murmur]` in `config.toml`, and `MCPTool(murmur__*)` in
  `[permission] allow`, so its tools run without asking;
- `hooks/murmur.json`, a hook run after each tool call and at the end of a turn in every project,
  which tells a busy session about messages.

Grok documents no skills folder, so it gets no skills.

## Getting the tools

Grok loads MCP servers when a session starts: start a new one (in T3 Code, a new thread).

## What to tell your user

- In T3 Code, an idle session should get a message as a new turn.
- In a terminal, a message to an idle session waits in its inbox, since nothing can start a turn
  in Grok there. Call `inbox` at the start of a task and between tasks.
- A busy session gets a one-line notice after its next tool call. What it hasn't read when it
  finishes is given to it then.

## If it isn't working

Report what happened as an issue: https://murmur.afig.dev/use says how. Nobody has run it yet.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#codexs-and-groks-hook
