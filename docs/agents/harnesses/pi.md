# Murmur in Pi

- **Join writes** `extensions/murmur.ts` in `~/.pi/agent`, Murmur's own extension, which registers the MCP server and brings messages in; and three skills.
- **Tools load** on `/reload` in a terminal, or in a new session; in T3 Code, open a new thread.
- **Idle:** a message starts a new turn. In T3 Code this was tried against a stand-in for T3, not T3 itself.
- **Busy:** a one-line notice after the next tool call. What it hasn't read is handed over when it finishes.
- **Approvals:** Pi has no setting for them, so in T3 Code it asks before each Murmur tool call unless the thread runs in full-access mode.
- **If it isn't working:** a session joined to a repo it doesn't work in (`mur join -C`) gets turns, but no notices while busy.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/handoff.md#pis-extension
