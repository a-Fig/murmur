# Murmur in OpenCode (for coding agents)

1. Follow https://murmur.afig.dev/steps. When you ask your user, include what join writes, below.
2. Get the tools, as below.
3. Read https://murmur.afig.dev/use

## What join writes for OpenCode

In `~/.config/opencode`, in the shape the installed version (`opencode --version`) loads:

- the MCP server and an allow rule for `murmur_*` in `opencode.jsonc` (else `opencode.json`), so
  its tools run without asking;
- `plugins/murmur.js`, which tells a busy session about messages, and outside T3 Code starts a turn
  in an idle one;
- three skills in `skills/`.

## Getting the tools

OpenCode reads its config again by itself: the tools are there from the next turn.

## What to tell your user

- OpenCode 2: an idle session gets a message as a new turn, and a busy one a one-line notice after
  its next tool call. In a terminal, a turn comes only once the session has used one of Murmur's
  tools. Its terminal shows the agent's answer to a notice, not the notice. Not tried in T3 Code.
- OpenCode 1: the same in a terminal. In T3 Code, a message to a busy session waits until its turn
  ends, then comes as a new turn.
- Nothing to approve.

## If it isn't working

- OpenCode 1 refuses a whole config that has OpenCode 2's `permissions`: run `mur join` again,
  which rewrites Murmur's entries for the version installed.
- No `plugins/murmur.js`, because join couldn't learn the version: run `mur join` again.
- `opencode run` without `--attach` exits when its turn ends, so a message handed over then is
  lost. Use `--attach`.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#other-agents
