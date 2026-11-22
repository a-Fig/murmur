# Murmur in OpenCode

- **Join writes**, in `~/.config/opencode` and in the shape the installed version loads: the MCP server and an allow rule for `murmur_*` in `opencode.jsonc` (else `opencode.json`), `plugins/murmur.js` to bring messages in, and three skills.
- **Tools load** by themselves: OpenCode reads its config again, and they are there from the next turn.
- **Idle:** a message starts a new turn, once the session has used one of Murmur's tools.
- **Busy:** a one-line notice after the next tool call; OpenCode 2's terminal shows the agent's answer, not the notice. In T3 Code, OpenCode 1 gets no notice: the message waits for the turn to end. OpenCode 2 in T3 Code isn't tried yet.
- **Approvals:** none.
- **If it isn't working:** OpenCode 1 refusing its whole config, or no notices, needs `mur join` again, which rewrites Murmur's entries for the version installed. `opencode run` without `--attach` exits as its turn ends, and drops a message handed over then.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/clients.md#other-agents
