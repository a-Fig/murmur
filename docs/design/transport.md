# Git as the transport

Every teammate already has credentials and push access to the repo, so the repo carries the
messages: no server, no accounts, no extra auth. Whoever can push is in. Outside the repo, Murmur
checks GitHub for updates ([updating](updating.md)) and, for a public repo only, sends us the repo's
address once ([counting use](counting.md)).

## Refs

- **One ref per device for events**, `refs/murmur/mesh/<deviceId>`: one empty commit per event, the
  payload in the commit message (a readable subject, a JSON body). One writer per ref means pushes
  never race. A message to another session sits in the sender's log, addressed to it.
- **One shared ref where a race must be settled.** Per-device logs can't settle one: two devices
  could each take the same session name (#20) within one sync, each seeing no rival. A push to a ref
  is atomic, and with a lease (`--force-with-lease`) it is refused unless the ref still holds what
  the pusher last fetched, which makes the ref a compare-and-swap; a refused pusher fetches and
  decides again. In V1's test through GitHub (2026-10-26), two machines pushed at the same instant
  three times, and each time exactly one push landed. Session names use this on `refs/murmur/names`
  ([session names](session-names.md)), and the team key on `refs/murmur/team`
  ([encryption](encryption.md)).
- **Presence is separate**: `refs/murmur/presence/<deviceId>`, force-pushed with the device's
  current state when it changes and on a heartbeat about every 2 minutes, which would otherwise
  flood the event log. A presence unchanged for 6 minutes is stale: its device's sessions may not
  get what is sent to them, and `who` and `send` say so.
- **Staleness is measured on the reader's clock**, from when it saw a presence change, and only up
  to its own last successful sync, so a reader that was itself asleep or offline doesn't take
  everyone else for gone. Only a presence it has never seen change is dated by the writer's clock
  (the tip commit's time, never later than now). Dating new refs from now showed devices dead for
  hours as alive for a new daemon's first 6 minutes, which is when a new session is most likely to
  ask `who` (seen 2026-10-30). A writer whose clock runs more than 6 minutes slow looks stale until
  its next heartbeat, at most 2.4 minutes.
- **Custom refs, not branches**, so CI and preview deploys don't fire on every message, and `git
  clone` and `git pull` never fetch them. git-bug uses `refs/bugs/*` on GitHub the same way. Tested
  2026-10-26: a push to `refs/t4/*` produced no `push` webhook delivery and no Actions run, even
  when the pushed commit held a workflow that runs on every push; the same commit pushed to a branch
  produced both. Vercel and Netlify deploy from push webhooks, so they have nothing to react to
  (inferred, not tested).
- **Plumbing only.** Writes use `commit-tree` and `push <sha>:<ref>` and never touch the working
  tree or index, where people and agents are working at the same moment. Bus commits carry git's
  empty tree, so fetching them brings nothing else. They are pushed with `--no-verify` and unsigned:
  a `pre-push` hook that runs the tests, or a signing prompt, on every message would make the bus
  unusable. They only ever go to `refs/murmur/*`.
- **No signature per event** (#155), so a device can write events that claim to come from another.
  Only accounts with push access can write `refs/murmur/*` (a fork can't), and any of them could
  already push code; a signature would stop one teammate posing as another's device, and little
  else, since a message gets no authority from its sender. A full version (Ed25519 keys per device,
  pinned on first sight, with replay checks, about 860 lines; branch `bus-signing`, PR #159) was set
  aside on 2026-11-03 (Tyler's decision). Since #156 presence is sealed with that branch's seal,
  because it carries the key teammates encrypt to.
- **beads** (task tracking synced through `refs/dolt/data` on GitHub) shows custom refs hold up at
  scale, and two of its bugs are warnings: `git push --mirror` from an ordinary clone deletes custom
  refs on the remote (beads #5266), and checking an internal branch out in the user's repo wiped
  tracked files (beads #6484).
- **What a reader reads.** Of the per-device refs, Murmur reads only `mesh/<deviceId>` and
  `presence/<deviceId>`, beside the shared `names` and `team`; anything else is left unfetched.
  Anyone who can push can write these refs, so a reader holds what it reads to what Murmur writes
  and builds over junk rather than on it. The remote refuses a ref whose name clashes with
  another's, so a daemon deletes refs that would block its own, such as `presence/<deviceId>/...`
  (found by the Opus reviews of V1, #8, 2026-10-28).
- **A tip over a device's own log that can't be fetched is replaced** (#222, 2026-11-16). A
  write the remote refuses looks at the log's tip to tell junk from a tip of its own to build on. A
  tip that is a commit here already, as this clone's own from before a restart always is, isn't
  fetched. One that isn't is fetched for 20 s at most. If that runs out, a large object pushed over
  the log, the write replaces it; a fetch that fails otherwise fails the write, as before. Fetching
  for the send's whole 90 s failed that send, and every one after it, as git keeps nothing of a
  fetch that doesn't finish. The cost: a log written from a copy of its clone's `.git`, by another
  clone, whose tip can't be fetched in time, is replaced as junk would be, and what that clone wrote
  since is lost to the log.
- **Sizes are capped** (2026-11-09). Git keeps commits compressed, so a quick fetch can bring a
  message of any size: one of 64 MB packed to 64 KB, and reading it whole took 448 MB. A commit over
  1 MB on a log or a presence, or 32 MB on the names registry, is passed over unread, as junk is. A
  log is read 500 commits at a time, with the daemon's lock let go between pages, so a device back
  after days away doesn't stall its sessions while it catches up, and along first parents, so a
  merge someone wrote by hand can't have its messages delivered twice.
- **A device is one clone's daemon.** Worktrees of a clone share its `.git`, so they share its
  daemon; a second clone on the same machine is a second device. The device ID is `device-` and
  eight random hex digits (`device-3f2a9c1e`), kept in `.git/murmur/`, which no commit or checkout
  touches. Anyone who can read the repo sees it, so it says nothing of the machine (#330,
  2026-11-15); a clone made before keeps its ID, which began with its hostname. Teammates tell
  devices apart by the owner and host the encrypted presence carries, and by nickname. What people
  call a device is under [device nicknames](presence.md#device-nicknames).

## Pushes

- **A device pushes one ref at a time** (#141, 2026-11-01). A session starting used to set off three
  pushes in the same second (presence, log, names), and GitHub now and then refused one with
  `[remote rejected] (failed)`, which is no lease refusal. In one laptop's log of 2026-11-01 all 9
  refusals came within seconds of a session starting or stopping, and none while presence was pushed
  alone, over several hours (inferred from the timing; a bare repo on disk takes concurrent pushes).
  The cost: a `send` made while presence is being pushed waits about a second more.
- **Pushes from different devices still meet on GitHub.** Two devices whose heartbeats fell together
  logged 18 refused presence pushes in 45 minutes (2026-11-09), and two clients pushing at the same
  moment were refused 2 times in 20, and 4 s apart, never. Since 2026-11-10 `bus.Push` retries those
  refusals a few times, checking the remote ref first so a try that landed anyway isn't written
  twice, and heartbeats are spread by 20% either way so devices that started together drift apart.
- **Presence goes first when a session starts** (2026-11-10), since only presence shows the session
  in other devices' `who`. Behind the start event and the name assignment, it had begun 4.05 s after
  the start over GitHub (median, 2026-11-09). A presence can carry a pool name before its assignment
  lands, since the name is already set aside for that device. Against a local remote that holds each
  push for 1.3 s, the presence landed 1.86 s after the start instead of 4.71 s (median of 10), and
  the start event 3.45 s instead of 1.53 s.

## Reading, and how fast

- **Polling.** A daemon polls `git ls-remote origin 'refs/murmur/*'`, which is cheap, and fetches
  only when a SHA has changed. V1, polling every 10 s, took about 12 s from a Windows laptop to a
  macOS machine (2026-10-26), 2 s of it the send.
- **Speed.** Around a conversation the daemon polls every 3 s (10 s before 2026-10-29) and hands a
  message over the moment it is fetched. Measured between two T3 threads on one laptop through
  GitHub over HTTPS: about 6–8 s from an agent's `send` to the other thread's turn starting (a push,
  1.2 s; up to one poll; `ls-remote`, 1.2 s; a fetch; `t3 thread send`, 1.1 s), and a question
  answered by another agent's reply in about 19 s. GitHub documents no limit on git reads, and 63
  `ls-remote` requests about 2 s apart drew none, which is a short test, not evidence about hours of
  them from a team's devices. A poll that fails waits twice as long each time, up to a minute, and
  goes back to its rate when it works.
- **How often it polls** (2026-10-30, #93). Tyler: agents don't need real-time communication, and
  4–7 s a hop is fine. So a daemon polls fast only when a reply is likely, and a session left open
  for hours costs 240 `ls-remote` calls an hour instead of 1,200.
  - *Every 3 s (`Poll`) for 3 minutes (`FastFor`)* after this device sends a message, queues or
    hands one over to a session here, or a session starts here. A reply usually comes within a
    minute or two, and each of these restarts the 3 minutes.
  - *Otherwise, with a session live or a page open,* the wait doubles after each poll: 3 s, 6, 12,
    up to 15 s (`PollCap`). A conversation's first message waits at most 15 s, plus the handoff.
  - *Quiet:* with no session live and no page open, every 30 s (`QuietPoll`, #87). A session
    registering or a page opening makes it look at once.
  - Presence heartbeats, status changes and messages for sessions elsewhere don't speed it up: they
    arrive all the time from active teammates and don't predict a reply.
- **A fetch offers only bus commits** (`--negotiation-tip=refs/murmur/*`, 2026-11-10). By default
  git offers every local ref's commits, and for a parentless presence commit the server is never
  ready, so a clone with 350 commits of its own (300 unpushed) took 6 requests offering 377 commits,
  against 2 offering 18. Over GitHub before the change, a fetch with a presence ref took 0.93 s and
  one without 0.45 s (2026-11-09). The cost is junk: a commit built on a branch and pushed onto a
  bus ref brings the branch's whole history (540 KB against 12 KB for a 200-commit branch). Offering
  the remote's `HEAD` too would avoid that, at a request more per presence fetch, so it was left
  out.
- **Commits are read through one git** (2026-11-15, #623). Reading a commit took two gits, a
  `cat-file` for its size and a `log` for its message, and asking whether one is here took a third.
  Each does a millisecond of work, and on Windows costs 110 to 240 ms to start. The daemon now puts
  these questions to one `git cat-file --batch-command` (`gitx.Reader`), started by the first read
  and ended 5 s after the last (`bus.ReaderLinger`), so the reads around a conversation share it and
  an idle daemon keeps none. Three two-device tests on a Windows laptop went from 2,576 gits to
  1,726, and from 69 s to 56 s. The size is still asked before the commit, so one over the limit is
  never read. `--batch-command` came in git 2.36: on an older git the first try fails, and reads go
  on with a git each. A reader that has run a while is not believed when it says an object is
  missing: a new git is asked, as a wrong "missing" would have a daemon build over history that is
  there. It isn't kept for the daemon's whole life because a Git for Windows that can't delete a
  pack another process has open asks its user whether to try again; git 2.53 deleted packs under a
  live reader, and older ones weren't tried.
- **A ref too slow to fetch** (2026-11-08, #15). Anyone with push access can put a large object on a
  bus ref, and a fetch that used up a sync's minute failed every sync while it stayed. Each fetch in
  a sync now has 15 s (`FetchLimit`). A ref that runs past it is fetched outside the syncs, one at a
  time, for up to 10 minutes, backing off after failures, and the next sync reads what it brought
  rather than fetching again, which on a slow enough link would mean never reading it. An object
  that does finish downloading stays until a gc: `git fetch --filter` would cap it, but makes the
  user's clone a partial clone, for their branches too. On Windows a fetch cut off leaves a
  `tmp_pack_*` behind (seen 2026-11-08), so the daemon removes old ones.
