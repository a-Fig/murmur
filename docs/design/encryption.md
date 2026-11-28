# Bus encryption

Every mesh starts in Team mode, on public and private repos (#156). Teammates can join and read
shared Team history without exchanging keys by hand. The mesh owner can change the mode (#1264).

## Repo encryption modes

- **Team.** Messages use the shared team key. Devices that receive grants can read Team history,
  including earlier conversations between other teammates.
- **Private.** Each message and its attachments are readable by the sender's device and the
  recipient devices selected when the message is sent. Group messages and broadcasts use the
  devices addressed at that moment. A later join gives a device no extra access to earlier Private
  messages. Receipts are encrypted for the original sender and the device writing the receipt.
  Presence, session tags and group membership remain readable by teammates so they can discover
  and address each other.
- **Off.** Daemons write plaintext events and signed plaintext presence without a team-key grant.
  Anyone who can read the remote can read this traffic, including anyone on the internet for a
  public repo.

`mur owner mode` shows this clone's verified setting. The owner can change it with
`mur owner mode team`, `mur owner mode private`, or `mur owner --allow-plaintext mode off`.
Active peers must support encryption modes before the owner can switch. A daemon applies a mode
after it syncs the signed policy. Upgrade offline daemons before they reconnect, since older
versions continue using Team. Existing history keeps the protection it had when written.
Returning to Team creates a fresh team key for new traffic.

## Team keys and records

Tyler's decision (2026-11-10): **every device that can push, and has published its key, reads
everything ever sent on the bus**, direct messages and history included. #257's first design
encrypted each record for the devices seen in the last 6 minutes, so a laptop closed overnight came
back to a gap it could never fill, and a new device could read nothing from before it joined. A team
key replaced it.

- **Device keys.** Each daemon makes an Ed25519 signing key and an X25519 box key, in
  `.git/murmur/keys` (0600). It publishes the box key in its presence and seals the presence: a
  signature over the record's JSON, bound to the ref (`internal/bus/seal.go`, from #159).
- **Push access is the key directory.** Only what can write `refs/murmur/*` can publish a key and be
  granted the team key, so GitHub's access control decides who is on the team. **That is every way
  to push, not only collaborators** (#397, 2026-11-10): a deploy key with write access, an org team
  or default permission, a GitHub App that can write contents, and a workflow that runs untrusted
  code with a write token (a `pull_request_target` workflow that runs a pull request's code). Git
  records what a ref points at, not who pushed it. An outsider who gets one push through such a
  workflow is granted every team key, history included, and a rotation changes nothing while the
  workflow is there; such a workflow can already push code. Opening a pull request can't do it: that
  writes only `refs/pull/*`. Checking who pushed, through GitHub's activity API, is open in #397.
- **Pinning.** A reader pins a device's signing key in `state.json` the first time it reads a sealed
  presence from it, and from then on takes a presence as that device's only if it is sealed with
  that key (`internal/daemon/keys.go`); one that fails is refused and logged, and nobody is asked
  anything. A real device never changes keys under its ID (see key loss), so a mismatch means a
  forgery or a copied `.git`. Pinning can't stop someone with push access who seals a presence for a
  device before a new reader has seen the real one, but the trust model already trusts push access
  with code. A reader takes a presence as new only if it was signed later than every other it has
  taken from that device, so a presence put back on its ref is never new (Sol's reviews of #257). A
  device rolled back to a binary from before encryption writes unsealed presence, which pinned
  teammates refuse until the updater brings it forward.
- **The team key.** One shared ref, `refs/murmur/team`, holds a chain of sealed records, pushed with
  a lease as the name registry is, so two devices can't both make the first key
  (`internal/daemon/team.go`).
  - An *epoch* publishes a team X25519 public key in the clear, its private half wrapped for its
    writer. On a new mesh, `mur init` writes the first epoch. In older meshes, the first daemon
    to find the team ref empty makes it ([the bus refs](joining.md#the-bus-refs-set-a-repo-up)).
  - A *grant* wraps every team private key its writer holds for one device. Each device's encrypted
    presence names the newest epoch it has read and the team keys it holds, and a holder of the
    current key grants to each pinned device whose presence names the current epoch and lists fewer
    keys than it could give (Tyler, 2026-11-10).
    - **Naming the epoch is the proof of push access.** An epoch's ID is the hash of a random key,
      so a presence naming it was signed after the epoch was made, and it sits on the device's own
      ref, so it was pushed after too. A collaborator removed before a rotation can read the new ID
      on a public repo but can't push a presence naming it. No time is trusted.
    - **Nobody has to be online together.** A device that joins while nobody is around pushes its
      presence and can go; the first key holder to sync grants it. An earlier rule granted only on a
      presence a daemon watched arrive, and such a device got nothing.
    - **By what a device holds, not by what the ref shows**, so a grant that doesn't open is made
      again. A device away isn't granted over and over: it is granted again only on a presence
      signed after the one it had when the grant was read (`GrantMark`).
  - A *roster* lists the people who can push, encrypted for one epoch's team key; `mur rotate`
    writes it.
  - Granted keys are kept in `.git/murmur/team-keys` (0600), which only grows, so a rewritten team
    ref takes nothing away. If that file is lost and `state.json` stays, the daemon reads the team
    ref again and gets its keys back with no teammate around.
- **Records.** Each event and presence is encrypted once, with a fresh content key wrapped the way
  age does it (an ephemeral X25519 key, HKDF-SHA256 and AES-256-GCM, all Go standard library;
  `internal/bus/box.go`). A presence is encrypted for the current team key; an event also for its
  writer's device, which reads its own log to queue a message between two of its sessions and would
  otherwise stop there until granted. The team public key is readable by anyone, so a device can
  write before it holds any private half. An event's whole content goes inside the box. A presence
  keeps its device ID, time and box key readable. Commit subjects are `event` and `presence`; they
  used to repeat the content.
- **A device waiting for a grant** can send at once. Reading a log stops at the first record for an
  epoch it hasn't been granted, and goes on from there once it is, so nothing is missed. A box for a
  key the team ref doesn't show waits one full read of the team ref, since a rotation's first
  records can be fetched before its epoch; after that it is junk, so junk holds a log up for one
  sync. While it waits, teammates show with no sessions. **The grant comes when a key holder next
  syncs**, the floor for any scheme where only devices hold keys.
- **Rotation.** A new epoch is a new team key, which every other device gets by a grant, so a
  collaborator removed from the repo never gets it, **unless something else of theirs can still
  push**: a deploy key they added, an org team, an App (#397). Removing someone means removing each.
  A device that joins later is still granted every old key, so history stays readable. Only a device
  holding every team key may rotate. `mur rotate` lists who can push with `gh` and makes a new epoch
  if anyone on the current roster is gone (read access counts as gone) or it can read no roster;
  `--force` always does. When someone has only joined, it rewrites the roster with no new key.
  **Someone who joined and left between two runs was never on a roster, so their leaving isn't
  seen**; only `--force` covers that. When rotation should run by itself is open (Tyler, 2026-11-10:
  the structure first).
- **Missing keys.** In Team and Private a daemon refuses a message that needs an unavailable
  recipient key. A device can publish plaintext when the owner has chosen Off.
- **Older daemons.** A daemon from before encryption passes over `box` events and sees an updated
  device with no sessions. An updated daemon reads plaintext events from any device, since events
  carry no signature, and plaintext presence only from a device it hasn't pinned. So for a while
  older devices can't hear updated ones; the updater ([updating](updating.md)) closes that gap
  within an hour of a release. **Until it updates, an older daemon writes in the clear, and nothing
  can stop it.**
- **A collaborator removed from the repo** keeps every team key they were granted. On a public repo
  they can still fetch the bus, so **they read everything written until someone rotates**, and
  everything before. On a private repo, removal takes their read access. #257's first design dropped
  them within 6 minutes with nothing to run; the team key trades that for history and for devices
  that come back.
- **No forward secrecy.** A leaked device's keys read everything up to the last epoch it was
  granted.
- **Key loss.** A deleted clone comes back as a new device and is granted like any. If only the keys
  file is gone and `state.json` (or, failing that, the local copy of its presence ref) shows the
  device had keys, the daemon takes a new device ID, because teammates would refuse a new key under
  the old one; the old ID goes stale and its names stay taken, and the new ID's sessions get new
  names: the registry never moves a name between devices (#617). That local copy is read only for
  its seal, so the daemon writes it with the first presence of a run, and again after a fetch has
  brought the ref or a broken copy was removed, not with every heartbeat (2026-11-22): any
  presence of the device's answers the question. A session that comes back with
  its session key takes its tags, mutes and explained list to its new name ([tags](tags.md), #827). The
  person's name stays with the old device, and the page asks them for another: nothing shows the
  new device is the same person's (#834).
- **The name registry stays in the clear, and unsealed.** It holds random names, device IDs and
  random session IDs, and a device must read it before it can name a session, which may be before it
  holds the team key. For the same reason an encrypted presence carries its sessions' names and IDs
  beside the box, which exposes nothing the registry doesn't.
- **What anyone who can read a public repo still sees:** device IDs (since #330 a new one says
  nothing of the machine, but one made before begins with a slug of its hostname); when each device
  writes, how many records and how large; the team key IDs each record is encrypted for; which
  devices each team key was granted to, and when; each device's public keys; the name registry,
  with each clone's commit fingerprint ([counting](counting.md), #1244), which shows which devices
  commit as one email;
  `refs/murmur/reported`; and that some message carried files, with about how large they were
  together ([files](files.md)). Not messages, subjects, who sent what to whom, statuses, branches,
  tags, hosts, owners, agents, rosters, or a file's name, size or contents.
- **Open, for Tyler** (#156): whether the census (#199) should keep anything readable, such as
  `owner` or an event's kind (since #550 it estimates messages and sessions instead; [counting
  use](counting.md)); and when a rotation should happen without anyone running `mur rotate`.

## Mesh owner

For a new mesh (#1263), the clone that runs `mur init` signs the first epoch and its repo policy.
That clone's worktrees share `.git/murmur/keys` and have the same authority. The owner must join
and come online to grant the first key. After any teammate has the current key, that teammate
can grant it too.

A daemon pins the owner when it reads the root of the team ref for the first time. Until then, it
trusts the remote to show the original root. Afterward, it keeps its verified history if the ref
is deleted or rewound. The owner's epochs, rosters and policy records share a signed sequence.
Teammates accept those records only from the pinned signing key and in sequence. Grants still
come from devices that hold the current key. Older daemons can read an owned mesh's team key.
They need the owner-aware update to verify who can rotate.

`mur owner` shows the owner this clone has verified. `mur rotate` requires that owner's signing
key as well as the team's key history. To hand over ownership, run `mur owner transfer DEVICE`
with the new owner's device ID from `mur who`. That device must have published its keys.
The current owner signs the new device's key and the next admin sequence into the team ref.
The new owner can rotate once its clone has all the team keys.
Before transferring, update Murmur on every device. The command checks the recipient and
other devices it sees online, and the recipient must also follow the automatic rotation setting
(below). A device that was offline needs the update before returning
to the mesh so it can follow the new owner's rotations.

For a mesh created before ownership, the device that signed the first team epoch becomes owner.
Git authors and presence labels were recorded independently, so a name match can't prove which
device ran init. Existing epochs and rosters stay readable. The owner writes a signed policy
checkpoint on its next sync, after which daemons require its signature and sequence on new
admin records. A daemon that makes a mesh's first epoch itself, with no `mur init`, pushes the
checkpoint with the epoch. On its own it was a second push at startup, which could reach the
remote ahead of a starting session's presence. Ownership stays with the current owner's device ID and signing key. If that clone
loses either before a transfer, it loses the ability to administer the mesh.

Nothing stops someone deleting that clone by hand, so Murmur says so where people look (#1287).
`mur owner` in the owning clone and the page's repo settings say what deleting it costs.
`mur join --leave` there warns, since a leave keeps the keys and a join again restores
everything. `mur uninstall` refuses while a clone on the machine's record of clones owns its mesh
(a leave keeps an owning clone on the record, so leaving first doesn't get around it, #1291),
unless `--owner-ok` is passed: its report says the daemon's folder is no longer read, and it removes the
command a transfer needs. The check reads the clone's saved state and keys, so it works with the
daemon stopped.

### Automatic rotation

Automatic rotation is off unless the mesh owner turns it on, under **Rotate the team key
automatically** in the page's repo settings (#1285, Tyler, 2026-11-27). It is a field of the signed
repo policy, `autoRotate`, so only the owner can change it and every device shows the same value.
A policy written before the field existed reads as off, so existing meshes stay off after an
update. Off, the owner's daemon makes no collaborator lookups and no rotations of its own; `mur
rotate` and `mur rotate --force` work as before.

It was on by default in nightlies (#412, #1271), and was turned off before a release because each
rotation pauses reading for every other device until a key holder grants the new key ("A device
waiting for a grant" above). On by default, every existing Team mesh would have rotated once, all at once,
when its owner updated, because none had a roster yet. It also needs `gh` credentials that can
list collaborators, and what it guards against is narrow: someone who lost push access reading
new traffic.

On, the owner's daemon checks the GitHub collaborator roster at startup, about once a day, and
right after the setting is turned on. If the current key has no roster, the check records who
can push and keeps the key: nobody is known to have left, and a rotation would pause the whole
team for nothing. Someone who lost access before that first check keeps reading until the next
rotation; `mur rotate --force` covers that case. On later checks, a collaborator who is missing
or now has read-only access triggers a new key. Successful checks also record new collaborators
so their later departure can be detected. A check that fails leaves the recorded roster in place
and is retried after an hour while the daemon is running.

The check runs only while the owner's daemon is online and the repo uses team encryption. A
collaborator who joins and leaves between checks won't appear in the roster. The owner can still
run `mur rotate --force`.

A check acts only on what allowed it to start (Sol's review of #1289, 2026-11-27). It carries the
owner, a count of changes to the owner, the mode and the setting, and once it has decided, the key
and roster it decided on. Every write it makes is checked against them under `teamMu`, so the lease
on the team ref refuses whatever lands after. If the owner turns the setting off, leaves Team mode
or hands the mesh to another device during the lookup, the lookup is stopped and nothing is
written. If the key changed, as a switch out of Team mode and back or a `mur rotate` changes it,
the check starts again with a new lookup. The sample taken for the old key says nothing about the
new one, so the check notes the key before it lists collaborators. Comparing an older sample with
the roster of a key made by hand during the lookup rotated a second time (Sol's re-review).

A new owner can be a grant behind: handed the mesh just after a key it hasn't been granted yet, it
can't read that key's roster. Its check would rotate, which needs every key, so it waits for the
grant and runs when the grant comes rather than at the hour's retry.

Off holds only on an owner whose Murmur knows the setting. `v0.5.6-nightly.20261127` has #1271
and not this setting, so it checks whenever the owner is in Team mode, and any older owner that
signs a policy drops `autoRotate`. So ownership goes only to a device whose presence says it follows
the setting (`teamAdmin` 4), and an owner running that nightly has to update for Off to take
effect. A daemon that saves `state.json` without knowing the field drops it from its cached policy.
The cache records which policy fields it was written with (`teamPolicyV`), and a daemon that finds
fewer reads the team history again from its root, where the signed value is. The read goes at least
to the cached tip, from the clone's own objects, so it finishes even while the remote has no team
ref; writes, presence included, wait for it. It reads every record, grants included, and saves
`state.json` after each page of 500 records (`bus.LogPage`), so an interrupted read resumes. That
cost comes back each time a downgrade and upgrade drops the field; it hasn't been measured on a long
history (2026-11-27).

A transfer also goes by the recipient's latest listed presence. If a newer one is listed and not yet
read, after a failed fetch say, the transfer is refused until it syncs: the newer one may come from
a downgrade that no longer follows the setting.
