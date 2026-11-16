---
name: murmur-name
description: Only when the user types /murmur-name, or asks which Murmur name this session has. Says this session's Murmur name, status and tags.
---

<!-- Installed by `mur join`, which rewrites this file: edits here are lost. Source: internal/skills in a-Fig/murmur. -->

# This session's Murmur name

Your user wants to know which Murmur name this session has: a teammate wrote "ask brisk-finch", or
they are telling someone who to message.

Call Murmur's `who`. Its first line says who you are, and your row, marked `(you)`, has your status
and tags. Answer with one line and stop:

**brisk-finch** · moving auth middleware to src/auth/ · tags: backend, model:opus-5.5

- A name still pending: say so, and give the device/session address `who` shows, which reaches you
  until the name lands.
- No status, or no tags: leave that part out.
- No Murmur tools in this session: say it hasn't joined Murmur, and that `mur join`, run in the
  repo, sets it up.
