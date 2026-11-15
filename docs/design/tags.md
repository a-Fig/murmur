# Tags, groups and lists

## Session tags

#198, first part built 2026-11-08: tags set by hand, the model, and search by tags. Groups followed
(#184, below). Rules in a tag file and subscriptions are put off, perhaps for good (#441), and the
lifecycle state was dropped (#185). The first version also had an availability state (#186), with
`unavailable` making `send` refuse a session; Tyler had it taken out before merging (2026-11-08), so
no tag carries behavior but `do-not-disturb`, which he asked for the same day.

- **What a tag is.** Up to 40 lowercase letters, digits and `. _ : / -`, at most 12 on a session
  (`internal/tags`). A tag under `model:` names the session's model, and a session has one at most.
- **`do-not-disturb`** keeps a session's messages queued in its inbox: none is handed over as a
  turn, and the hook gives no notice or whole message (#204), holds none for the turn's end (#202),
  and doesn't keep the turn going with them (#213). `inbox` gives them as usual. When the tag comes
  off, each goes out once, by whichever route applies then, and counts as arriving at that moment,
  so one older than the 15-minute limit on turns is still handed over as one. `send` isn't refused;
  the sender is told the message is queued.
- **Where a tag comes from**, highest first: a person on the page, the session itself (the `tags`
  tool), or the daemon, which derives a T3 session's model from the thread's `modelSelection.model`
  in T3's database (`claude-opus-5-5`, shown as `model:opus-5.5`); a session outside T3 sets its
  own, as the MCP instructions ask. A person adding a tag pins it on and removing one pins it off,
  and the session can't undo either, except that a session can always leave a group (#539).
- **The session's own device decides its tags** and publishes them in presence, each with its source
  (Tyler, 2026-11-08, over #198's first design of one shared ref with a lease). One device is the
  only writer of its sessions' tags, so nothing needs ordering across devices, and a person's choice
  beats the session's whichever arrives first. A person tagging a session on another device sends
  that device a `tags` event on their own log, which it applies when it reads it. The cost: a change
  for a device that is down waits until it is back, when its sessions can't be reached anyway.
- **Kept by the session's name**, in `state.json`, across restarts, resumes and the session finding
  its T3 thread, until someone takes it off; nothing expires. A skipped backlog still reads its
  `tags` events, so a change waits for a session however long its daemon was down (#230, #237).
  Since a change can be read long after it was made, each tag follows a person's latest choice about
  it, and a change more than a minute older does nothing (#251); by the senders' clocks, so a clock
  over a minute behind can lose a change, which beat an old change undoing a new one. At the cap of
  12, a person's tags are kept first, then the session's, then derived ones.
- **Search.** `who` takes tags and lists only the sessions with all of them; `-tag` leaves out those
  with it, and a `*` at the end matches any tag that starts so (`model:opus*`).
- **`human`** marks a person's account (#267, Tyler, 2026-11-09), so an agent reading `who`,
  `lookup` or a message's header, and a person on the page, can tell a person from an agent at a
  glance. Murmur alone gives it: every daemon works it out from the address, since only
  `device/human` is a person, and drops it from anything else's tags. A session can't set it, and a
  person can't add or remove it. `who human` lists the people and `who -human` the agents.

## Groups

#184, built 2026-11-11. #198 first had a "group" type that a repo declared in a tag file; with tags
free-form, Tyler chose the smaller thing: any tag that starts with `group:` is a group.

- **A group is a tag**, such as `group:ui`, with no slash in it. Its members are the sessions that
  have the tag, and it exists while it has members. Nothing declares it.
- **The tag is the address.** `send` to `group:ui` reaches every member but the sender. No session
  name, device ID or `*` has a colon, so a group shadows none of them. Only `group:` tags are
  addresses: a chat has members who know they are in it, which `reviewing` or `model:opus-5.5`
  doesn't promise (Tyler, 2026-11-11). `send` takes one tag, not `who`'s filter, because a filter
  has no name to answer to.
- **A session joins by setting the tag and leaves by removing it**, and can be in several groups.
  That holds for a group a person put it in too (#539): every group message tells the member that
  removing the tag leaves, and a member that couldn't leave would get a turn for every message. The
  removal takes the person's pin off and counts as a choice made then, so a person's add made more
  than a minute before it, read late, doesn't put the session back. A session whose daemon is from
  before #539 still can't leave such a group. A group can stand for a topic: sessions that want to
  hear of changes to `main` join `group:main-changes`, and whoever lands one sends to it (Tyler,
  2026-11-11), which is #178's ping done by hand.
- **An agent can add other sessions to a group**, with `tags` and their names in `for`. It is the
  only tag one agent can give another: without that limit any agent could put `do-not-disturb` on
  another or change its `model:`. It can't take a session out. The add is a `group.add` event on the
  adder's log, and the session's device makes the tag the session's own, so the session can remove
  it; a `tags` event wouldn't do, as every daemon applies one as a person's choice. An add over an
  hour old when read isn't applied, nor one from history a daemon passes over unless it was sent
  since the session started: an add carries no order against the session's own changes, so an old
  one read late could put back a member who has left. An add that would take a session past its 12
  tags is refused to the adder.
- **Each receiving device decides who is in.** A group message goes on the bus addressed to the tag,
  and each daemon gives it to those of its sessions that have the tag when it reads it. A device is
  the only authority on its sessions' tags, so no sender's view of the members can be out of date,
  and an add followed at once by a message, both on the adder's log, reaches the session added. The
  sender is told who it reaches as far as presence shows.
- **How a member learns it is in a group.** Murmur has no notice of its own, only messages, so the
  group's message says it: its first line is "Message sent in group:ui from quiet-otter" (Tyler,
  2026-11-12), and its last, the first time, says the session is in the group and how to answer or
  leave, as a person's explanation does (#345). Later ones end "Reply with Murmur's `send` to
  group:ui, or to quiet-otter alone, if a reply is needed. Remove the tag group:ui from yourself to
  leave the group." (Tyler, 2026-11-13). A person writing in a group gets the group's line too
  (#484): theirs said to answer them alone, and the rest of the group never saw the answer.
- **No loop guard**, as for any message (Tyler, 2026-10-29), though an answer to a group gives every
  member a turn. If groups prove noisy, the next step is a group message that never extends a
  running turn.
- **Older daemons.** Presence says whether a device's daemon knows groups. One that doesn't passes
  over group messages and `group.add`, so a sender is told which members won't get the message, and
  an add to such a device is refused.
- **On the page** a group is a conversation a person can write to. A mute covers a group (#27;
  [mutes](mutes.md)).

## Sending to a list

#520, built 2026-11-13. An agent can send one message to several sessions without making a group:
`send`'s `to` takes names or addresses separated by commas.

- **A list is not a group.** Each recipient gets an ordinary one-to-one message, which doesn't say
  who else got it, and a reply goes to the sender alone (Tyler, 2026-11-13). So a group or `*` can't
  be in a list: a message there is one conversation that all its members see.
- **The MCP server expands it**, sending to each recipient as a single `send` would, so the daemon
  and the wire format are unchanged and an older daemon takes one too. The cost is a push per
  recipient, one after another, so a list holds at most 10: twenty pushes could outlast a client's
  wait for the tool call, which would leave the sender with no result and some recipients with the
  message.
- **Each recipient once.** Names are sent before addresses, and the daemon's answer gives the
  address a name reached, so the same session named again by address is skipped. Two names for one
  session, and a device beside one of its sessions, still each send. A device's ID looks like a
  person's name, so a list can't tell one apart to refuse it.
