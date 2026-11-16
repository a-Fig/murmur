# Incognito

Added 2026-11-15 (#681), to Tyler's design: a setting in `mur ui`'s Settings that takes this device
off Murmur in one repo for a while, as if it were offline. Turning it off brings the device back as
it was. Its names, keys and inbox are kept, and nothing needs joining again. The code is
`internal/daemon/incognito.go`.

## What it does

- **Teammates** see the device go. Turning incognito on deletes the device's presence, as a daemon's
  goodbye does, so other devices drop it at their next sync rather than show it frozen at its last
  sighting. After that the daemon writes nothing to the bus: no presence, events, receipts, names or
  team-ref changes. Starts and stops of its sessions aren't kept to be written later either, which
  would show afterwards when they worked.
- **Its agents** can't use Murmur. Every Murmur tool (`who`, `lookup`, `send`, `status`, `tags`,
  `mute`, `inbox`) says Murmur is off for this repo and how to turn it back on. The hook gives no
  notices, a turn's end gives nothing waiting, and no message is handed over as a turn.
- **Messages** to its sessions wait in the inbox until incognito is off, then arrive as usual. That
  covers messages sent just before the switch, and messages sent while it's on to a name someone
  remembers. Nothing is lost.
- **The person's page** keeps working: the daemon keeps fetching the bus. It shows the team without
  this device's agents, with a line in the sidebar saying why. Sending is off, and under a
  conversation the page offers to turn incognito off.

The setting is kept in the clone's `settings.json` beside the page's other settings, so it holds
across daemon restarts.

## What it doesn't hide

- The device still has a team-key grant, and its key data is on the team ref. They show that the
  device exists, not that it is working now.
- Commits and branches pushed to the repository itself are outside Murmur, and still show work.
- Messages already on the bus stay there, with their receipts as far as they got.
