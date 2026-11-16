# A device's modes

Added 2026-11-15 (#681), to Tyler's design. In `mur ui`'s Settings, a device's person sets its mode
in one repo: **Online**, **Do not disturb** or **Invisible**. The mode is kept in the clone's
`settings.json` beside the page's other settings, so it holds across daemon restarts. Going back
online brings the device back as it was: its names, keys and inbox are kept, and nothing needs
joining again. The code is `internal/daemon/mode.go`.

## Do not disturb

The device and its agents stay in sight, in `who` and on the page, but no message reaches the
agents. While it lasts, every session on the device carries the `do-not-disturb` tag, so the
session tag's rules apply to each of them:
- Messages wait in the inbox, with no turn and no notice.
- An agent can still read its inbox and use every tool.
- Senders see the tag.

Its own person's messages are the exception (#777, Tyler, 2026-11-16): one sent from the device's
page, to one session, a group or everyone, goes as usual, a turn or a notice, through the mode and a session's own
tag alike, and the page doesn't say it waits. Some people talk to their own agents through Murmur,
and do not disturb is for everyone else.

The device's presence also says it is on do not disturb, so `who` and the page say why on the
device's own line, and senders on other devices hold to it even if a session's tags don't show it.
Neither a person taking the tag off a session nor the limit on a session's tags lifts it: the
daemon checks the mode itself, and shows the tag in `who` and `lookup` all the same. A session that has the tag itself keeps it afterwards.

## Going back online

Going back online, from either mode, lets through what arrived since the device left online, as
taking the tag off does: each message counts as arriving now, so an idle session gets it as a turn.
So does what a session's own tag held, if the session or a person took that tag off while the
device was away: the device's mode kept the tag on, so nothing was let go then. Anything else that
waited from before keeps the fate it had. When the device left online is kept in
`settings.json` beside the mode, so a restart in between doesn't change this.

## Invisible

Invisible takes the device off Murmur in the repo, as if it were offline.

- **Teammates** see the device go. Going invisible deletes the device's presence, as a daemon's
  goodbye does, so other devices drop it at their next sync rather than show it frozen at its last
  sighting. After that the daemon writes nothing to the bus: no presence, events, receipts, names or
  team-ref changes. Starts and stops of its sessions aren't kept to be written later either, which
  would show afterwards when they worked.
- **Its agents** can't use Murmur. Every Murmur tool (`who`, `lookup`, `send`, `status`, `tags`,
  `mute`, `inbox`) says Murmur is off for this repo and how to bring it back. The hook gives no
  notices, a turn's end gives nothing waiting, and no message is handed over as a turn.
- **Messages** to its sessions wait in the inbox until the device is back online, then arrive as
  usual. That covers messages sent just before the switch, and messages sent meanwhile to a name
  someone remembers. A session that stops in the meantime keeps its inbox for a day, as any stopped
  session does.
- **The person's page** keeps working: the daemon keeps fetching the bus. It shows the team without
  this device's agents, with a line in the sidebar saying why. Sending is off, and under a
  conversation the page offers to go back online.

### What invisible doesn't hide

- The device still has a team-key grant, and its key data is on the team ref. They show that the
  device exists, not that it is working now.
- Commits and branches pushed to the repository itself are outside Murmur, and still show work.
- Messages already on the bus stay there, with their receipts as far as they got.
- Once the device is back online, some of what happened while it was invisible shows. Receipts for
  messages read meanwhile carry when they were read, and sessions that started meanwhile show when
  they started and were last active, with the commits credited to them.
