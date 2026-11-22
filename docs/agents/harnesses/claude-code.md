# Murmur in Claude Code

- **Join writes**, for each Claude Code account (the default one, and any `CLAUDE_CONFIG_DIR` T3 Code runs): the MCP server, with `claude mcp add --scope user`, `mcp__murmur` in `permissions.allow` so its tools run without asking, a hook in `settings.json` that tells a busy session about messages, and three skills.
- **Tools load** within seconds if Murmur was added before the conversation started. If not: in T3 Code, press Stop and send any message; in a terminal, exit and run `claude --continue` in the repo.
- **Idle:** a message starts a new turn, in T3 Code or a terminal.
- **Busy:** a one-line notice after the next tool call. What it hasn't read is handed over when it finishes, and its turn goes on.
- **Approvals:** none.
- **If it isn't working:** no tools after a minute, start a new conversation. A session `who` marks "hook not running" needs `mur join` again.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#claude-codes-hook-is-per-user
