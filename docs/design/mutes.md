# Mutes

Tyler decided (2026-10-28, #27) that a session can mute another, by name, and that there is mute
only, no separate block. Built 2026-11-11. The muting session gets nothing the other sends from then
on: not a message to it, to its device, to a group it is in, or to everyone.

- **The muting session's device decides.** It keeps the mutes (`state.json`, by the session's name,
  as tags are, so they outlast a resume or a restart) and doesn't queue a message from a muted
  session for the one that muted it, including what arrived while the daemon was down (#482).
  Because the receiver drops it, a mute holds whatever the sender's daemon knows, its version
  included. Other sessions on the device still get a broadcast.
- **Mutes are on the bus, in presence.** Each session's presence lists the names it has muted, at
  most 50, inside the box. Presence is the device's current state, so a mute needs no event to
  replay. Anyone on the team sees who muted whom: `lookup` says "has muted", and the page lists
  every mute.
- **The muted session isn't told when it happens.** It learns when it sends to the session that
  muted it: its daemon has read that presence, refuses the `send` and says why, an answer from
  Murmur that wakes nobody (#25), and nothing is written to the bus.
- **A mute takes 30 seconds to hold against direct messages** (Tyler, 2026-11-11). A message sent
  before the sender's daemon has read the mute would be dropped with its sender sure it had arrived,
  so for 30 s, by the muting device's clock, direct messages still get through; that is longer than
  a presence takes to be pushed and read. The cost is a message or two after muting. Left open
  (#466): a sender whose daemon stays ignorant longer (an older version, or cut off) is never told.
  A muted session's message to everyone or to a device goes out, and `send` names who didn't get it.
- **A mute names a session by its name**, which the registry never gives out twice, so it can't land
  on another session, and it follows the conversation across session IDs until it is undone. A
  person can't be muted, nor a session with no name yet. Mutes a session makes before its own name
  lands carry over to the name (#483), and hold while its inbox is kept if it stops before then
  (#503).
- **A sender that has left presence is known by every name its address has held** (#483,
  2026-11-12), as after Claude Code's `/clear`: a message carries no name and the registry keeps no
  order, so a mute of any of them holds. For one of this device's sessions that includes the name it
  took last, which the registry may have down for another session ID after a resume, so the name the
  inbox shows for it is always one a mute holds against (#502). A sender's own device refuses it, or
  names the muters of a message to many, by the same names: the one it has in presence, or every
  one its address has held while it has none (#602), so a sender waiting on a new name isn't sent
  past a mute of its old one. The cost: a later conversation
  nobody muted, at that address, has what it sends dropped once it has left presence, and isn't
  told. Remembering the last name each address showed would narrow that; it isn't built.
- **A muted session can't add the session that muted it to a group** (#483), since each add used one
  of the muting session's 12 tags.
- **A person can undo one, on the page of the muting session's device**, so an agent can't silence a
  teammate's agent without its person able to see and undo it. Other devices' pages list it but
  can't undo it, which would need an event to the muting device; nobody has needed that. The session
  can mute again afterwards.
