# Murmur in Antigravity

- **Join writes**, in `~/.gemini` and in the home T3 Code gives each Antigravity it runs: the MCP server in `config/mcp_config.json`, `mcp(murmur/*)` in `permissions.allow` so its tools run without asking, and a `murmur` entry in `config/hooks.json` that tells a busy session about messages. Three skills go in `~/.gemini/config/skills`, which T3 shares.
- **Tools load** within seconds if Murmur was added before the conversation started. If not: open a new thread in T3 Code, or restart `agy` in a terminal.
- **Idle:** in T3 Code, a message should start a new turn. In a terminal it should wait in the inbox: call `inbox` between tasks. Neither is tried yet.
- **Busy:** in T3 Code, a notice before the next step, and what it hasn't read is handed over when it finishes (tried on Antigravity 1.5.13).
- **Approvals:** none.
- **If it isn't working:** an Antigravity added to T3 Code after joining has a home of its own, so run `mur join` again once T3 has started it. If join couldn't edit a settings file, it printed the rule and the path to add it to.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#antigravity
