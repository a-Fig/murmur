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
  the sender is told the message is queued. A message from the device's own person, sent from its
  page, isn't held, whether to one session, a group or everyone (#777, Tyler, 2026-11-16: "I don't see when I would want
  DND to block my own messages"); nothing ties a person's devices together, so one they send from
  another device is held as anyone's is.
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
- **A clone that loses its keys** becomes a new device, and its sessions get new names
  ([encryption](encryption.md)). Tags, mutes and the explained list are kept by name, so until
  2026-11-16 (#827) a session that came back had none: one in `group:ui` on do not disturb was out
  of the group and given turns again. Now the state file keeps which name each session key had
  (`namesLeft`), and a session that comes back with that key takes what the old name kept: the
  tags unless it has some already, the mutes added to its own. While its new name is pending it
  goes by the old name's, so it is never off do not disturb in between, and a session here that
  muted it still doesn't hear from it. They are found by the session's key, not its ID: one that
  moves to another conversation before its name lands leaves them with the conversation they
  were for. When the name lands they move to it, and mutes other sessions on the clone made *of*
  the old name become mutes of the new one.
  - *The page's wake and busy choices for one session* are kept by name too, in
    `settings.json`, and follow it (#836): until then a session set never to be woken came back
    with the device's choice. The name lands under the daemon's lock, and the settings' lock
    is never taken inside that one. So the pair of names waits in the state file
    (`settingsMoves`) for the next read of the settings, the page's included, and the session is
    looked up by its old name meanwhile. The page sends back the whole of what it read with
    every save, so one that read before the move would undo it: the moves made are kept
    (`settingsMoved`) and a save's old names are read as the new ones.
- **A session with no name yet** keeps its tags and mutes by its ID. One that stops before its name
  lands still has them while its inbox is kept, and they go with the inbox (#503). One whose daemon
  stopped or was killed with it counts as stopped from about then (#736, #762), so they are kept as long
  too. Until 2026-11-16 they went two minutes after the restart, and a session resumed later than
  that had lost its mutes, so the sessions it muted reached it again. After a restart, a
  person's change to the tags of a session the
  daemon knows nothing of, or an agent's add of it to a group, read in the first 30 s, waits in
  memory for a session of that ID to reconnect in that time, as a message does (#524): one whose
  name was pending and that kept nothing here would otherwise lose it. It is applied before the
  messages that waited, so the group's message read with an add reaches the session, and a held add
  is checked again as one read in a catch-up. A daemon back after more than 10 minutes refuses such
  an add when it reads it, as it does any add from history it passes over that it can't show was
  sent since the session started; a person's change still waits, unless the daemon has nothing the
  log could be for (no session, kept inbox, person's account, name or tags) and passes it over
  unread. Nothing is written to disk for it, as the daemon can't tell the ID from a made-up one. The
  cost: what waited is lost by a session that reconnects after the 30 s, as the messages are, and by
  one whose daemon restarts again within them.
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
  one read late could put back a member who has left. The session's device leaves out a group a
  person took the session out of, and logs it: the person's mark stands over the add, and kept as
  the session's own the tag would only take one of its 12 places.
- **The adder refuses what it can tell will fail** (#524, #543, 2026-11-15). For a session on its
  own device it applies the same rule to the same state, so it refuses every add the device would
  refuse or leave a group out of: past 12 tags of the session's own (those a person took off still
  count), or a group a person took the session out of. An add of several groups is refused whole
  then, though the device would give the rest: an add is all or nothing, and the reply names the
  group. For a session on another device it has only presence, which shows the tags a session set
  itself but not those a person took off, nor a person's marks on groups: it refuses an add only
  past 12 tags presence shows as the session's own, and the reply says that device can still refuse
  it, and that `who` with the group shows who is in. That count can be one too many in a narrow
  case: a session a person took out of a group, which then dropped that tag itself, keeps the
  person's mark but no longer holds the tag, so an add of that group with others counts it, where
  the device would leave it out and might have room for the rest. It also refuses an add to a
  device whose clock it can tell runs over an hour ahead, as that device would take the add as too
  old. It measures the earlier of the presence's own time and its commit's (git's, from the
  writer's clock as it wrote) against when the presence was read here: the presence time alone can
  be pinned ahead after a clock went back, and the gap can only understate the difference.
  Publishing a person's marks in presence would let the adder see more, but it would put the names
  of tags a person removed back on the bus, and change its format.
- **Each receiving device decides who is in.** A group message goes on the bus addressed to the tag,
  and each daemon gives it to those of its sessions that have the tag when it reads it. A device is
  the only authority on its sessions' tags, so no sender's view of the members can be out of date,
  and an add followed at once by a message, both on the adder's log, reaches the session added. The
  sender is told who it reaches as far as presence shows.
- **How a member learns it is in a group.** Murmur has no notice of its own, only messages, so the
  group's message says it: its first line is "Message sent in group:ui from quiet-otter" (Tyler,
  2026-11-12), and its last, the first time, says the session is in the group and how to answer or
  leave, as a person's explanation does (#345): "You are in group:ui. Reply with Murmur's `send` to
  group:ui, or to quiet-otter alone, if a reply is needed. Remove the tag group:ui from yourself to
  leave the group." (Tyler, 2026-11-13). Later ones end "Reply with Murmur's `send` to group:ui or
  quiet-otter, if a reply is needed." A person writing in a group gets the group's line too
  (#484): theirs said to answer them alone, and the rest of the group never saw the answer.
- **No loop guard**, as for any message (Tyler, 2026-10-29), though an answer to a group gives every
  member a turn. If groups prove noisy, the next step is a group message that never extends a
  running turn.
- **Older daemons.** Presence says whether a device's daemon knows groups. One that doesn't passes
  over group messages and `group.add`, so a sender is told which members won't get the message, and
  an add to such a device is refused.
- **On the page** a group is a conversation a person can write to. A mute covers a group (#27;
  [mutes](mutes.md)).
- **A device is a group** (#463, Tyler, 2026-11-12; built 2026-11-15): every agent's session on a
  device has `group:device-<its ID>`, so there is one way to name several sessions. An ID that
  starts with `device-` already, as those made since #330 do, gives `group:<its ID>`, which starts
  the same way, so two devices can share one (`laptop-1a2b` and `device-laptop-1a2b`); a send to
  such a tag is refused, and the sender is told to use the ID. `who` lists it
  and searches by it, `lookup` shows it, and `send` to it is a send to the device. Some choices:
  - *Nobody sets it, or takes it off.* Each reader adds it to a session from the device the session
    runs on, as it shows the session. It is never kept, and never carried in presence: a presence
    that claims one has it dropped on reading, so no device can put its sessions in another's group.
    It isn't counted toward a session's 12, and can be longer than 40 (a device ID can be 40).
  - *A group a team made under the prefix before is gone* (#705, 2026-11-16). A `group:device-lab`
    loses its members as their devices update, as the prefix is the devices'. Letting such a group
    on would let a session put itself in another device's group, so it is refused, and both the
    refusal of the tag and of a send to it say what to use if it was a team's group (`group:lab`):
    only "if", as the name may be a device's ID, mistyped or of a device that has gone. One a session
    or a person had set is dropped from what is kept, as an old `human` is, so it takes no place.
  - *On the bus it is still the device.* A message to the group is written to the device's ID, so
    every daemon delivers it as before, older ones too, and only to that device's sessions. The tag
    is how a device is named, not a new way of addressing it.
  - *A person's account isn't in it*, as a device's messages don't reach a person.
  - *Not done yet:* the page still shows a device's conversation as it did and doesn't list the tag.

## Sending to a list

#520, built 2026-11-13. An agent can send one message to several sessions without making a group:
`send`'s `to` takes names or addresses separated by commas.

- **A list is not a group.** Each recipient gets an ordinary one-to-one message, which doesn't say
  who else got it, and a reply goes to the sender alone (Tyler, 2026-11-13). So a group or `*` can't
  be in a list: a message there is one conversation that all its members see.
- **The MCP server expands it**, sending to each recipient as a single `send` would, so the daemon
  needs nothing new for it and an older daemon takes one too. The cost is a push per recipient, one
  after another, so a list holds at most 10: twenty pushes could outlast a client's wait for the
  tool call, which would leave the sender with no result and some recipients with the message.
- **Each recipient once.** Names are sent before addresses, and the daemon's answer gives the
  address a name reached, so the same session named again by address is skipped. Two names for one
  session, and a device beside one of its sessions, still each send. A device's ID looks like a
  person's name, so a list can't tell one apart to refuse it.
- **One recipient's refusal doesn't stop the rest; a refusal of the message does** (#541, 2026-11-15).
  The daemon's reply says which a refusal is (`Refused`): one for the recipient alone (a name that
  isn't one or that nobody has, a group that isn't one, offline, muted the sender, a device with no
  key to encrypt to), or one for the message or the sender (no subject, too long, a key, not
  registered, a delegate, no team key yet, a push that failed), which would be the same for the
  rest, so they aren't tried. A call no daemon answered is the second kind too. Before, the server
  took any refusal that didn't quote the recipient for the second kind, so a name with a capital in
  it stopped the whole list: the daemon's refusal of it quoted nobody.
  - *Mixed versions.* mur updates the daemon while running MCP servers keep their binary until their
    sessions end, so an older server beside a newer daemon is the common pairing. Such a server
    still goes by whether the refusal quotes the recipient, and the daemon's refusals for a
    recipient now all quote it as it was given, but for a mute or an offline session at an address
    whose messages go on to another session, which they name by that one, and a device with no key
    to encrypt to, which they name by its device. A newer server beside an older daemon goes by the
    words too: a refusal that quotes the recipient, and the few of that daemon's refusals for a
    recipient that don't. Neither side needs the other's version, so there is no new protocol
    number.
