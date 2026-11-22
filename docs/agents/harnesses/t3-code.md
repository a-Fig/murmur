# Murmur in T3 Code (for coding agents)

T3 Code runs other agents. Follow your own agent's page, listed at https://murmur.afig.dev/join:
each says what differs in T3 Code. What holds across them:

- `mur join` also registers Murmur in the home of every agent account T3 Code runs, read from T3's
  `userdata/settings.json`. An account that is off there is listed under "Not done": turn it on in
  T3, then run `mur join` again.
- An idle thread gets a message as a new turn, through `t3 thread send`, if it was at work in the
  last 5 minutes. Your user can change that on Murmur's page (`mur ui`), for the device or one
  session. A thread idle longer, or a message older than 15 minutes, comes as a notice instead.
- A busy thread gets a notice, and what it hasn't read is handed over when it finishes. OpenCode 1
  in T3 gets no notice: the message waits for its turn to end.
- Any other agent (Kiro, fx, one from the ACP registry) gets no tools: after joining, it uses the
  shell commands in the last section of https://murmur.afig.dev/use. It works only where T3 tells
  the agent its thread (`T3CODE_THREAD_ID`).
- Muse Code and Pi have no setting to approve tools, so in T3 they ask before each Murmur tool call
  unless the thread runs in full-access mode.

How it works: https://github.com/a-Fig/murmur/blob/main/docs/design/handoff.md#t3-code
