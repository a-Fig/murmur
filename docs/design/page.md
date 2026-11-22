# The page for humans

Added 2026-10-29 (#22). A person sees what the agents are doing, and the messages between them, in a
browser. The page is `internal/web`: static files embedded in the binary and a small JSON API, which
the daemon answers from what it holds in memory, so a poll makes no git call. How a person writes
from it is in [a person on the page](people.md).

- **Served by the daemon** on 127.0.0.1, on a random port of its own, with a token of its own
  (`pageToken`, #42), both in `daemon.json` as `web`: the sessions' token lets its holder send as
  this device, and the page's address, which carries its token, reaches the browser's history and
  extensions.
- **Opened by `mur ui`**, which starts the daemon if it isn't running, opens the browser and prints
  the address. The token is in the fragment (`#token=`), which a browser keeps from the server.
- **Switching repos** (2026-11-09, #348). The sidebar's header opens this machine's other joined
  clones. Each clone's daemon serves its own page, with its own port and token, and knows no others,
  so a per-user record lists them: `clones` in the user's config directory, one file per clone so
  writers never need a lock (`internal/clones`). Daemons finding each other was set aside: the
  picker has to offer a clone whose daemon isn't running, and start it.
  - *The token doesn't travel with the tab*, since the other page is another origin and a token
    mustn't go in an address. The page asks its daemon to open the clone; that daemon starts the
    other's if needed and asks it for a key (`OpPageKey`, protocol 10: random, good once, for a
    minute), and the tab trades the key at the other page's `/api/key` for that page's token.
    `/api/key` takes no token, and only JSON, which another site can't send without a preflight
    nothing grants.
  - *The cost:* a page's token now reaches every joined clone's page on the machine, though never a
    sessions' port, and a draft in a composer has to be sent or cleared first, as it lives only in
    the page. #303 (a private file that opens the page) could carry such a key in place of the
    token, so the file would hold nothing that lasts.
- **What it shows.** Every device the daemon knows, this one first, with each session's name, agent,
  branch, what it is doing, and when it was last active (#21); whether a device is reachable comes
  from presence going stale. Messages are every one this daemon reads, whoever it is for, since
  every record is encrypted for the whole team ([encryption](encryption.md)). The daemon keeps the
  last 500 in memory.
  - *Which message is which* (#603, 2026-11-16) goes by its ID, its sender and when it was sent.
    By the ID alone, a device that wrote an ID again hid its own later message from every page,
    and after a restart could put one of its own in another's place. The page still keeps
    messages by one ID each, and passes over an ID it has. So the first message the daemon kept
    with an ID has it plain, for as long as a page may still hold it: through a re-sort of the
    list, and for 4,000 dropped messages after the daemon's own 500 have let it go, the page
    keeping 2,000. Any other message with that ID is given it with a mark added, 64 bits of a
    hash, the same each time. The unread list follows the marked ID.
    - *What is left.* Receipts go by the bus's ID alone, so the page shows them on the message
      that has it plain, whichever of the two they were for. And a daemon that restarts picks
      owners afresh, by time sent where history is read back: a page handed over by an update,
      which keeps what it had, can then show one of the two twice and the other not at all until
      it is loaded again.
- **Messages from before the daemon started are read back from the bus** (#138, 2026-11-12). The
  daemon exits when idle, so the page opened on conversations with their beginnings gone, and one
  away over 10 minutes left a hole in the middle. They are read back from the logs this clone has
  already fetched: the newest commits across every device's log, opened with the keys the daemon
  holds (`internal/daemon/history.go`).
  - *Why not a file.* A daemon never reads what it passes over, so a file would keep the hole, and
    it would be a second copy of every message, in the clear, to keep in step with the bus. A
    person's own messages stay in their file, which goes back further than the page's 500.
  - *What it costs.* Four git commands for up to 4,000 commits, about 0.2 s each on the Windows
    laptop (2026-11-12), and no request to the remote. It runs in the background, first when a page
    or `lookup` asks, since most daemons are started by a session and never show the page, and again
    when a backlog or a slow fetch lands or the device is given a team key. The page's first call
    waits for it, 3 s at most.
  - *What changes.* The page shows messages from before this device joined; no session is given
    them. Once anything is read back, messages are in the order of their senders' clocks, so a
    device whose clock runs ahead takes more of the 500.
  - *What it doesn't do.* It can't reach past the newest 500. A record this device can't open is
    left out until a key opens it, and a device whose backlog fetch was given up on is off the page
    until the fetch lands.
- **Laid out as a sibling of T3 Code** (#179, 2026-11-08; redrawn 2026-11-09), so a person who works
  in T3 Code reads it without learning a second app: sessions down the left under their devices,
  then a column of conversations (direct messages and group chats in one list, newest first), then
  the open conversation as T3 Code's timeline, with the composer under it. On a phone the sidebar is
  a drawer, and the list and a conversation take turns. A conversation is two sessions' messages to
  each other, a device's, a group's or everyone's: the bus has no threads. Each session's dot is
  green when active in the last five minutes, yellow idle, red do not disturb (the tag, or wake-ups
  set to Never), and grey when its device is unreachable. The only other colors are the accent and a
  pull request's state.
- **Quiet sessions fold** under their device. A settled T3 thread (#189) is out of presence but
  still in the messages. Sessions in Claude Code, Codex, Pi or OpenCode never settle, and one left
  open in a forgotten terminal stays in presence for good, so a session idle for a day, or one a
  person hid (the `hidden` tag), folds as quiet too. An idle one comes back when it does anything; a
  hidden one stays hidden until someone shows it, because a tag a person sets stays until a person
  takes it off.
- **Tags organize the lists** (#270, 2026-11-09). Each row shows its session's tags, the sidebar's
  Tags filter every list to one tag, and a tag menu puts a tag on one session or several, as one
  `/api/tags` change per session; a chip shows as waiting until the session's device has applied it,
  two minutes at most. Tags with a meaning show as that meaning: `model:` beside the agent,
  `do-not-disturb` as the red dot, `hidden` as the fold, `human` on a person. There is no grouping
  by tag: a session can carry several, so groups would repeat it.
- **A profile card** (2026-11-15, #689, Tyler's request). Clicking a face, or a sender's name in a
  conversation, opens a card beside it, as a chat app does: the name and address, the state, agent,
  model and tags, the status and branch, what you have in common (the groups you have posted in that
  it is in, and your device), for a person the sessions they have messaged, and a box that sends a
  direct message from the card. It uses only presence and the messages the page has. A face that is
  part of another control (the account chip, a group's pile) doesn't open it. Since 2026-11-16
  (#778, Tyler's request) a name opens it wherever the page shows one: the sidebar, Sessions, Recent
  (activity and messages), a group's members, "Talked with", and a name the page knows written in a
  message (`quiet-otter`). Names in a picker, in sessions being picked to tag, and in a
  conversation's row keep that control's action. Read receipts list names in a tooltip, which
  holds no controls, so they don't open it. Tyler chose the banner
  look over a compact one. It has no way to a fuller view yet (#716): the session's conversations,
  which #656 headed with its details, hold little more than the card does.
- **Themes** are palettes over the same design. Dim is the default (#939). The daemon includes
  its saved theme in the page's HTML so it can paint in that palette before JavaScript runs.
- **Page preferences** (#941). Theme, Only mine, and folded Tags live in
  `.git/murmur/page-preferences.json` for this clone, shared by its browser tabs and desktop app
  on the next load. The daemon writes that file atomically through the authenticated
  `/api/preferences` endpoint. The first old browser or webview to supply a missing choice
  fills it in. A saved choice takes precedence over any old copy. Sidebar width stays with the
  browser or webview because their windows can be different sizes. The light-mode surprise also
  keeps its own history in each. The page token stays in session storage.
- **Recent is a feed of what agents did** (2026-11-12, #448, Tyler's request), not only of what they
  said: sessions joining or leaving, status and tag changes, commits, and pull requests and issues
  opened, merged or closed.
  - *Where it comes from.* Nothing new goes on the bus. Starts and stops are events every device
    already writes, and a status or tag change is the difference between two presences. Pull
    requests and issues are asked of GitHub with `gh` when the page is viewed (the thirty most
    recently updated of each, kept two minutes): only people read them, so they cost the bus
    nothing. Commits come from the hook's credit on the session's own device; from another device
    only the branch shows, as presence carries branches and not commits.
  - *Noise.* A feed, not a log: a session's status changes less than 30 minutes apart show as the
    last one with a count, and its commits to one branch in that time are added up. Heartbeats,
    `lastActive` and the current branch are left out.
  - *Who did it.* Agents on one machine share their user's GitHub login, so a pull request is named
    for the session on its branch, and for the login only when no session is. GitHub's list says who
    opened one, not who merged or closed it.
  - *An issue's session* (2026-11-15, #510, Tyler's request). An issue has no branch, so the hook
    looks at each tool call for one it filed or closed: a shell command that runs `gh issue create`
    or `gh issue close` where a command starts, outside quotes and comments, read from what gh
    printed, or GitHub's MCP server creating or closing one, read from the address in its result. A
    command whose output holds more filed or closed issues than it ran creates or closes reports
    none, as something else printed them (a `gh issue list` beside it, a grep of this code). The
    daemon keeps the issue's repository and number and which it was as the session's activity, and
    the page names that session on GitHub's event of the same number in the same repository, read
    from the event's address: `quiet-otter filed issue #37`, `quiet-otter closed issue #37`, a close
    only if it was about when GitHub's was (an issue reopened and closed again was closed last by
    someone else). The page's listing is of the repository gh picks in the clone, which can be an
    `upstream` remote and not the bus's, so the daemon keeps an issue of any repository on GitHub
    and the page matches. One GitHub hasn't listed yet shows on its own, if it is in the listing's
    repository. Matching by time and login was set aside as a guess that goes wrong with two
    sessions at work, and a marker in the issue as something every agent would have to write. What
    it costs: a hook decodes the call's output only for those calls, and nothing of it but the
    issue's repository and number, and whether it was closed, leaves the hook. It is known only on
    the session's own device, like a commit's count. Not covered, so the login stays: Pi and
    OpenCode, whose plugins pass the hook no tool output; an issue filed another way (`gh api`, the
    website); a loop that runs one `gh issue create` or `gh issue close` for several issues; a call
    that fails after its create, which Claude Code ends as `PostToolUseFailure`, whose output the
    hook doesn't read; and a Codex call still running when it yields, which has no output yet.
  - *What it doesn't have.* The newest 600 entries are kept in memory. After a restart, starts and
    stops come back from the bus with the messages and pull requests from GitHub, but a status, tag
    or commit from before is gone: only a file could keep them, which isn't built. Two status
    changes within one presence write show as one. Without `gh`, or logged out, no pull requests or
    issues show.
- **Settings** (when a session is woken, and what a busy one is given by the hook) are saved in
  `.git/murmur/settings.json`; [delivery](delivery.md) says what they do. Settings also lists every
  mute ([mutes](mutes.md)), with a button to undo those of this device's sessions.
- **Settings is one screen per repo, grouped by topic** (2026-11-16, #775, Tyler's choice of a
  proposal). It grew a section per feature, with rows named by scope ("Device default" twice). A
  device is one clone, so the settings are the open repo's, and the page says which repo
  and machine at its top. The groups are You (name, nickname, colour), Messages to your sessions
  (availability, waking, what a working session is shown, and each session's own, folded until
  opened), Files, Mutes and Appearance. About follows under "For every repo". Every setting is a
  segmented control, on/off ones included (Tyler, 2026-11-17, #862).
  The page says "session", as the rest of it does (Tyler). Grouping by scope was turned down: device
  and repo are nearly the same thing here, and people come with a question, not a scope. A page
  that changes every repo at once is #816.
- **An open page keeps the daemon up.** The page polls every 3 s, 15 s in a hidden tab. A call with
  the token from a page someone can see counts as an open page for 2 minutes and stops the 10-minute
  idle countdown, so a daemon with nothing else to do exits about 12 minutes after its page is
  closed or hidden. A hidden page marks its calls, so a tab forgotten in the background doesn't keep
  the daemon, and its polling of GitHub, up for good (Opus review, 2026-10-29). The desktop app's
  page also says it is the app's, which keeps the daemon running, minimized too, without counting as
  someone looking ([desktop](desktop.md)).
- **This device's health** (`GET /api/health`, 2026-11-10; where the page shows it is Tyler's call).
  Peers are judged stale against this daemon's last successful sync, so a device that has lost the
  remote shows every peer as it was, and nothing said so: #141's refused pushes, a 52-minute DNS
  outage, and #87 were found by reading `daemon.log` by hand. The snapshot says how the syncs,
  pushes, fetches and handoffs have gone since the daemon started, and what became of the public
  repo's report (`web.Health`). A line that may hold a credential is not shown.
