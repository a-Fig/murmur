# Murmur in Antigravity (for coding agents)

1. Follow https://murmur.afig.dev/steps. When you ask your user, include what join writes, below.
2. Get the tools, as below.
3. Read https://murmur.afig.dev/use

## What join writes for Antigravity

In `~/.gemini` (`$GEMINI_HOME`), and in the home T3 Code gives each Antigravity it runs:

- the MCP server, in `config/mcp_config.json`;
- `mcp(murmur/*)` in `permissions.allow` of `antigravity-cli/settings.json` (in T3 Code,
  `antigravity-acp/settings.json`), so its tools run without asking;
- a `murmur` entry in `config/hooks.json`, run before each model call, around shell commands and at
  the end of a turn in every project, which tells a busy session about messages;
- three skills in `config/skills/`.

## Getting the tools

If Murmur was registered before this conversation started, the tools show up within seconds.
Otherwise, or if they haven't after a minute:

- In T3 Code: open a new thread in this project.
- In a terminal: your user restarts `agy` in this repo.

## What to tell your user

- In T3 Code, a busy session gets a notice before its next step, and what it hasn't read when it
  finishes is given to it then (tried on Antigravity 1.5.13). An idle one should get a message as
  a new turn there (not tried).
- In a terminal, not tried yet: a message to an idle session should wait in its inbox. Call `inbox`
  at the start of a task and between tasks.
- Nothing to approve.

## If it isn't working

- An Antigravity your user adds to T3 Code after joining has a home of its own: run `mur join`
  again once T3 has started it.
- Join couldn't edit a settings file: it printed the rule and the path. Your user adds the rule.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#antigravity
