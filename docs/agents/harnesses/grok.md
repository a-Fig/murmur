# Murmur in Grok

Not tried yet: Murmur's support for Grok was built from its documentation and source.

- **Join writes**, in `~/.grok`: the MCP server in `config.toml`, `MCPTool(murmur__*)` in `[permission] allow` so its tools run without asking, and `hooks/murmur.json`, a hook that tells a busy session about messages. Grok has no skills folder.
- **Tools load** as a session starts: start a new one, or a new thread in T3 Code.
- **Idle:** in T3 Code, a message should start a new turn. In a terminal it waits in the inbox, since nothing can start a turn in Grok there: call `inbox` between tasks.
- **Busy:** a one-line notice after the next tool call. What it hasn't read is handed over when it finishes.
- **Approvals:** none.
- **If it isn't working:** report what happened as an issue on a-Fig/murmur.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#codexs-and-groks-hook
