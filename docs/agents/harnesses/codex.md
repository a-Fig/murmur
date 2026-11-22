# Murmur in Codex

- **Join writes**, in each Codex home (`$CODEX_HOME`, else `~/.codex`, and any T3 Code runs): the MCP server in `config.toml` with its tools approved, a hook in `hooks.json` that tells a busy session about messages, the hook's approval in `config.toml` so Codex runs it without a review screen, and three skills.
- **Tools load** only as a session starts: open a new thread in T3 Code, or start a new session in a terminal.
- **Idle:** a message starts a new turn. In a terminal, only once the session has used one of Murmur's tools.
- **Busy:** a one-line notice after the next tool call. What it hasn't read is handed over when it finishes, and its turn goes on.
- **Approvals:** none.
- **If it isn't working:** a session `who` marks "hook not running" needs `mur join` again; if that doesn't fix it, a new Codex may approve hooks differently, so report it. In T3 Code, `mur join -C` can't move a Codex thread to another repo: start the thread there.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#codexs-and-groks-hook
