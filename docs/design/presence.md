# Who is on, and what they have done

## Who is on, and what they are doing

Tyler's decisions, 2026-10-29, built 2026-10-30 (#66). With many agents on, `who` had a line for
every session ever seen, the stale ones above the live ones, and a context only where an agent had
called `status`: in a live test it listed three devices left from earlier tests above the two agents
that mattered.

- **A cap.** `who` lists at most 20 sessions, most recently active first (`view.WhoLimit`), then
  says how many more there are and that `lookup` shows any one. Sessions are ranked across devices
  and shown under their devices. The caller's own session and device are always shown. A stale
  device is one line (its name, owner, when it was last seen, how many sessions). The cap is in the
  text `who` renders; the daemon's reply holds every session.
- **Held to what a device can say** (2026-11-09). Anyone who can push can make any number of device
  refs and date a presence as they like. So `who` lists at most 5 devices with no session listed
  (`view.WhoQuiet`) and counts the rest in one line, stale devices that still have sessions first,
  since that is the line an agent acts on. Another device's session start is dated on the reader's
  clock, no later than the presence that lists it: a start dated ahead had ranked its sessions above
  every other in every agent's `who`.
- **Last active on every line**, as `active 2m ago`.
- **The conversation's title is the default status line.** An agent that has set no status shows its
  conversation's title in `who`, `lookup` and the page; a `status` it sets always wins, and setting
  it to nothing brings the title back. The title is read from the client's own state, which the
  client owns, versions and writes, so every reader opens it read-only, waits no more than a moment
  for a lock, and treats anything it doesn't expect as a failed read that keeps the title the
  session had. Where each client keeps it (seen 2026-10-30):

  | Session | Title | Not used |
  |---|---|---|
  | In T3 | `orchestration_v2_projection_threads.title` for the thread, in T3's `statev2.sqlite` | The client's own name for the conversation: T3 doesn't copy its title there, and the two would take turns |
  | Codex outside T3 | The thread's explicit name: `threads.name` in `state_N.sqlite` (the highest N) of the `CODEX_HOME` the session's MCP server names (#88), else `thread_name` in `session_index.jsonl` | `threads.title` and `threads.preview`, which hold the opening message |
  | Claude Code outside T3 | The last `custom-title` line in the session's transcript (the name the user gave it), else the last `ai-title` line (the one Claude Code made up) | The first prompt: a session with neither has no title |

  The transcript is the hook's `transcript_path`. Claude Code rewrites the title lines near the end
  of the file, so the last 128 KB is read (in the 300 largest transcripts on the laptop, 2026-10-30,
  the last `ai-title` was within 31 KB of the end). A new path, as after `/clear`, is a new
  conversation and drops the old title.
- **It goes out as presence**, in the session's `doing` field, where a status goes, so the bus
  format is unchanged and an older daemon shows the title as a status. **A conversation's title is
  therefore shown to every teammate who can read the bus, unless the agent sets a status**; it is
  cut to the status line's 120 characters, and one that `scrub` would stop in a status is not shown.
- **When it is read.** A daemon goes through its sessions every 5 seconds (`Options.TitleEvery`) and
  reads each source once, outside its lock. A session with a status is not read. A changed title
  writes presence, so it shows on other devices within seconds.

## What a session has done

Added 2026-10-29 (#21). `lookup` gives an agent what `who` doesn't: the branches another session has
committed to, when it was last active, whether a message would reach it, and its latest messages. A
session on this device also gives its worktree folder, the commits it made and the client's session
ID, which stay on the device. Git doesn't record which session made a commit, so the daemon watches
tool calls for them.

- **Bracketing a call.** On `PreToolUse` the daemon snapshots the worktree where the tool call
  will run (`attrib.Head`:
  HEAD, branch, every ref tip and every worktree's detached HEAD); at the call's end it snapshots
  again and credits the commits between the two to the sessions the hook speaks for. A commit made
  in that worktree during the call by a person or another session is credited too: git can't say who
  made it, and this was accepted. A tip that isn't an object in the clone, such as a branch or a
  worktree HEAD file naming a lost commit, is skipped; until #540 it failed the snapshot or the end's
  `git log` in every worktree.
- **Pairing.** The two ends of a call share an ID. Claude Code reports a failed call in
  `PostToolUseFailure` (seen on 2.1.312, 2026-10-29). Antigravity includes an error in
  `PostToolUse`. A commit made before a failed push still counts. A snapshot expires after an
  hour, and the oldest is dropped when more than 128 are held.
- **Which worktree.** The one the call started in, and the one it ended in if Claude Code itself
  moved (#60). A `cd` inside a Bash call leaves the hook's working directory where it was (seen on
  2.1.312, 2026-10-29), so what `cd ../other && git commit` or `git -C ../other commit` makes in
  another worktree isn't credited. A subagent's hooks carry the parent's session ID, so its commits
  are the parent's.
- **What it costs.** A call that cannot run a command (`Read`, `Edit` and the like, named in
  `internal/hook`) isn't bracketed. The start is one `git` process, and the call must wait for it,
  or a commit made before the snapshot would count as old history; the end is answered at once.
  Measured 2026-10-29 on the idle Windows laptop, where one `git` process took 24 ms: `PreToolUse`
  for a bracketed call took a median of 79 ms and `PostToolUse` 19 ms, against 15 and 19 ms for a
  skipped tool. As first built, with four and five `git` processes, they took 241 and 335 ms, and
  under several test runs that start took a median of 1.7 s; the start as it is now should take
  about a quarter of that (inferred, not measured).
- **It never stops a call.** `PreToolUse` waits at most 5 s for the daemon. Claude Code's hook
  prints nothing and Antigravity's returns `{"decision":"allow"}`.
- **Where it is kept.** Each session's latest 100 credited commits are in `state.json` and nowhere
  else, for 24 hours after it stops. Presence carries only the branches it committed to, most recent
  first, at most 5.
- **Last active** is the time of the session's last request to the daemon: any hook event or MCP
  call. It goes out with the next presence write and never causes one, so another device sees it up
  to two minutes late, and a reader works out the time on its own clock from how long before the
  presence was written it was. An agent without the hook moves it by MCP calls only. Claude Code
  and Antigravity's hooks bracket tool calls for commit credit.
- **Messages in `lookup`** are the page's: the last 500, read back from the bus past a restart
  (#138). A group's messages count for the sessions in it now, by their tags (#485, #445), so one a
  session got before it left the group isn't listed. That is the page's filter for a running
  session. For a stopped one the page counts none, and lookup on its own device counts them by the
  tags kept for its name.

## Device nicknames

A device's ID (`device-3f2a9c1e`; `tyler-laptop-3f2a` for a clone made before #330) says nothing
of whose it is. Since 2026-11-11 (#190) a device
can be called something else, such as its owner's name.

- **A label, not a new ID, and not an address.** The ID names the device's refs and is in every
  address on the bus, so changing it would orphan the device's log. `who`, `lookup` and the page
  show the nickname beside the ID. A first version let `send` and `lookup` take `tyler/bd0102`, and
  in Sol's review it sent a message to the wrong device, by a nickname that was the ID of a device
  whose presence had gone. Agents address each other by session name anyway, so the address was cut
  (Tyler, 2026-11-11) rather than guarded.
- **Shared, and set by the device's own user**, with `mur nickname <name>` or on the page's Settings
  (#533). It is kept in `state.json` and travels in presence, inside the box, so every teammate sees
  the same name. Labels of one's own for other people's devices were left out: nobody has asked for
  two names for one device.
- **The default is the person's name**, the one chosen on the page ([a person on the
  page](people.md)), worked out by readers from presence. The git `user.name` wasn't used: it is the
  same on all of one person's clones.
- **Not unique.** One person may want both their machines called `tyler`, and the ID sits beside it.
  A nickname is refused only when it is a device's ID or another device's person's name, which would
  read as someone else. Any device can still say any nickname in its presence, so a nickname is no
  proof of whose a device is.
- **Its form is a person's name's** (`bus.ValidNick`), so it never reads as a session's name.
- **Shown after the ID, in words**: `who` and `lookup` print `tyler-laptop-3f2a, called tyler`.
  Until 2026-11-15 (#542) the nickname came first, where a session's name goes, and an agent read
  it as an address and sent to it. A send or a lookup to a name that is only a nickname is refused
  with the device's ID. A person's name stays the person's address, and a device's ID stands before
  any nickname, also the ID of a device whose presence has gone but whose log is on the bus.
- Not built: the nickname in a message's header, which names its session and "on <owner>'s machine"
  the first time.

## Device colours

The page fills a device's faces with its colour (#560), one of 12. Since 2026-11-14 (#573) its
person can pick one on Settings.

- **Shared and settled the same everywhere.** The pick and when it was made (`Color`, `ColorAt`)
  travel in presence, inside the box. Every daemon decides who has which colour by one rule,
  `bus.DeviceColors`, and the page shows what its daemon decided.
- **One device per picked colour.** A colour another device holds is refused. In a race, the
  earlier pick by its own clock wins, and a tie goes to the lower ID. The loser goes back to Auto,
  and its Settings says who has it.
- **Coming back.** An idle daemon takes its presence off the remote, so its colour looks free, and
  so does a device that goes invisible (#1084). A returning daemon, or a device back online,
  leaves its pick out of presence until it has synced with every live presence fetched and open, then gives
  the pick up if a peer took it. Keeping a lost pick to reclaim later made two devices swap
  colours each time one went idle (found in review, #577).
- **Stale devices keep their colours**, since staleness is each reader's own clock, and two pages
  minutes apart would otherwise colour a sleeping device differently. A device whose presence has
  not changed for a week lets its colour go (#581, 2026-11-22): a daemon that was killed, or a clone
  that was abandoned, leaves its presence behind for good, and its colour could never be picked. It
  is then shown as though it had picked none, and anyone can pick the colour. If it comes back, it
  gives the pick up to whoever took it, as any returning device does; a daemon that never stopped,
  and slept through the week or couldn't push for it, holds its pick out of presence until it has
  synced. The week is by each reader's wall clock, so pages can disagree about a gone device's
  colour for as long as their clocks differ. A presence a reader fetches only after sleeping
  through the week is judged by its commit's time too, so the reader doesn't date it from its own
  waking and hold the colour for another week (#1165).
- **Auto** is the colour the ID hashes to, or the next one nobody picked.
- **No protocol bump.** An older device ignores the fields and shows in its automatic colour, and
  the pick is a page call to the page's own daemon.
