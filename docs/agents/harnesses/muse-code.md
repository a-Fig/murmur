# Murmur in Muse Code

- **Join writes**, in `~/.config/muse/settings.json`: the MCP server under `mcpServers`, and a hook under `hooks` that tells a busy session about messages. Muse has no skills folder.
- **Tools load** only as a session starts: start a new one, or a new thread in T3 Code.
- **Idle:** in T3 Code, a message starts a new turn. In a terminal it waits in the inbox, since nothing can start a turn in Muse there: call `inbox` between tasks.
- **Busy:** a one-line notice after the next tool call. What it hasn't read is handed over when it finishes, and its turn goes on.
- **Approvals:** Muse has no setting for them, so in T3 Code it asks before each Murmur tool call unless the thread runs in full-access mode.
- **If it isn't working:** a session `who` marks "hook not running" needs `mur join` again. Join refuses a `settings.json` that still has the older `mcp_servers` key: move those servers to `mcpServers`, then join again. Tried on Windows only.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#muses-hook
