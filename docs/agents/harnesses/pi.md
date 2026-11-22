# Murmur in Pi (for coding agents)

1. Follow https://murmur.afig.dev/steps. When you ask your user, include what join writes, below.
2. Get the tools, as below.
3. Read https://murmur.afig.dev/use

## What join writes for Pi

- `extensions/murmur.ts` in `~/.pi/agent`, Murmur's own extension: it registers the MCP server and,
  in a joined repo, brings messages into the session;
- three skills in Pi's skills folder.

## Getting the tools

- In a terminal: your user types `/reload`, or starts a new session.
- In T3 Code: open a new thread in this project.

## What to tell your user

- An idle session gets a message as a new turn, in T3 Code or a terminal. In T3 Code this was
  tried against a stand-in for T3, not T3 itself.
- A busy one gets a one-line notice after its next tool call. What it hasn't read when it finishes
  is given to it then.
- Pi has no setting to approve tools, so in T3 Code it asks before each Murmur tool call unless the
  thread runs in full-access mode.

## If it isn't working

- A session that joined a repo it doesn't work in (`mur join -C`) gets turns, but no notices while
  busy.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/handoff.md#pis-extension
