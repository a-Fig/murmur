# Files with a message

#570, 2026-11-15. A user's feedback (2026-11-14): "Not being able to send images in the chat is
also a bad thing tbh... limits the potential of the agent." Until then Murmur carried text: an agent
couldn't send another a screenshot, a log or a diff.

`send` takes `files`, a list of paths. The recipient's agent gets each as a path on its own machine,
which it opens with its own tools; one that reads images sees the image. A person on the page
attaches, pastes or drops files, and sees the ones a message carries ([below](#on-the-page)).

## How a file travels

- **A ref for each message.** The sender's daemon encrypts each file, joins them into one blob (the
  pack) and pushes it as one commit to `refs/murmur/files/<name>`. It is plumbing, like every bus
  write (`hash-object`, `mktree`, `commit-tree`, `push`), and pushed before the event, so a daemon
  that reads the event finds the files there.
- **Not in the log.** A device's log is fetched whole by every device, for good. A file in an
  event's commit would be downloaded by everyone and never leave. On its own ref, a file is
  fetched only by the devices its message is for, and goes when the ref does.
- **Syncs don't fetch them.** A sync fetches the refs it reads (`readable` in
  `internal/daemon/sync.go`) and leaves the rest of `refs/murmur/` on the remote, which daemons
  from before files do too. The cost: the listing every poll grows by a line per files ref.
- **Encrypted with keys in the event.** Each file has a fresh AES-256-GCM key; the event, which is
  encrypted for the team ([encryption](encryption.md)), carries each file's name, size, SHA-256
  and key, and the ref's name. So whoever can read a message can read its files, and nobody else.
  A file is bound to its ref and its place on it, and a receiver holds what it opens to the
  event's size and SHA-256. It is bound to the ref and not the message so that several messages
  can one day share an upload.
- **Fetched before the message is queued.** A daemon that reads a message with files for one of
  its sessions fetches the ref, saves the files under `.git/murmur/files/`, never the working
  tree, and only then queues the message, so a path in a turn works when the agent
  reads it. It gets three tries of a minute each; then the message is queued without the files, and
  the turn says which aren't there. A ref that isn't on the remote, holds something else, or holds
  more than the limits allow, costs one try. Messages are fetched for one at a time, in the order they came, so one slow fetch holds
  up the files of those behind it, not their text-only neighbours. The wait survives a restart
  (`state.json`). The message is queued for whoever holds each recipient's name when the fetch
  ends: a process that resumed the conversation meanwhile took only what was in the inbox, and the
  message would otherwise reach the old session's, which nobody reads (#703). A session that takes
  over the old one's T3 thread, which carries no name from one to the other, takes its place in the
  wait, and a recipient gone off the mesh meanwhile (a settled thread) gets nothing.
- **Nothing is left in the clone.** Git writes a files commit, and what a fetch of one brings, to
  a directory of the daemon's own (`GIT_OBJECT_DIRECTORY`, with the clone's objects as an
  alternate), which is deleted when the push or the read is done, and the fetch stores no ref. In
  the clone's own objects each message would leave up to 23 MB that nothing names, until a garbage
  collection that may be months away.
- **On the sender's device** the daemon keeps a copy where a receiver's would be, so a session on
  the same device has the files with no fetch.

## What the remote shows

Tyler's rule (2026-11-15): ideally nothing; hide what is cheap to hide, and say what still shows.

- **Hidden:** the files, their names, how many there are, their exact sizes, the message they
  belong to, who it is for, and, on the ref itself, who sent it and when.
  - The ref's name is 32 random hex digits, carried inside the event.
  - The files are one blob, filled out with random bytes to one of a few lengths (Padmé: at most
    12% longer, less as it grows, and never under 4 KB), so a length says about how large a
    message's files are together and nothing more.
  - Every files commit has the same author and the same date, 2000-01-01.
- **Still shows, to anyone who can read the repo:** that some message carried files, and about how
  large they were together. Someone who lists the refs often also sees when a files ref appeared,
  and which device's log grew in the same moment. Hiding that would take sending files at times
  unrelated to their messages.

## Limits

Tyler left the numbers to us (2026-11-15); they are `MaxFiles`, `MaxFile` and `MaxFilesTotal` in
`internal/bus/files.go`, and `FileKeep` in the daemon's options.

- **8 files a message, 10 MB a file, 20 MB a message.** They fit what agents send each other:
  screenshots, logs, diffs, a short recording. Every recipient's device downloads a message's
  files, a message to a group is downloaded by each member's, and the daemon holds them in memory
  while it encrypts. A push has a minute, which 20 MB needs 2.7 Mbit/s of upload for. GitHub warns
  at 50 MB an object and refuses 100 MB.
- **A week on the remote.** The sender's daemon deletes a files ref 7 days after it sent it, and
  the remote may then drop the objects in its own time (GitHub doesn't promise when; they are
  encrypted either way). Receivers delete their copies after 7 days
  too. A week covers a message read after a weekend; the message itself stays in history with its
  file names, and a turn made from it later says the files aren't there.
- **A sender that never runs again**, as a deleted clone, would leave its refs for good (#708,
  2026-11-16). So every daemon notes the files refs it reads of in other devices' logs, history
  included, and deletes one once it has itself run for 14 days since it first read of it, if the
  ref's device's presence is stale or gone by then. Its own daemon, if it still runs, deleted it 7
  days before, since the event is written after the push. The 14 days are counted on the monotonic
  clock, at most two hours for each hourly prune, so no wall clock goes in: a clock set forward by
  weeks, as one is when it is put right, ages nothing, and nor does a teammate's event with an old
  date. Time the daemon is down isn't counted, and a machine asleep counts at most the cap (Windows'
  monotonic clock runs on in sleep; others' stop). That only makes a ref wait longer: weeks more on
  a team whose daemons run only while someone works. The note of a ref that is off the remote
  already goes at the next prune, whatever its age (#747): the prune lists the remote, and a ref is
  pushed before the event that names it, so one not listed was deleted. A ref no event this device
  read names still stays: one whose message failed after its push, one in a log it can't open, or one sent
  before it first ran, as a new clone passes over the logs' history.
- **A send to several recipients uploads the files once** (#739, 2026-11-16). Each gets a message of
  its own (#520), and a file is sealed to its ref, not its message, so a send of the same files,
  names and contents, within 10 minutes of their push takes that push's ref, once a message naming
  it has gone out. A ref whose message failed is left to the prune and taken by no other send, and
  a send that took a ref and then failed leaves it be. Until then each message uploaded them again.
- **200 MB of uploads a device a day** (`FilesPerDay`, #739), over the last 24 hours, counted in
  packs pushed, so a send to several counts once. Past it a send with files is refused, saying when
  it can go. A loop sending a 20 MB log to everyone each minute would otherwise put a gigabyte an
  hour on the remote, which shares the repo's size limits with the team's code.
- **1 GB of files kept a device** (`FilesHeld`, #739), those it sent and those it fetched together.
  Past it a message's files go, the oldest first, checked after each send and each fetch, the
  page's included, and at each prune, the files just sent or fetched staying; such a message reads
  as one whose files can't be had, and the page fetches them again if asked. Before, only the week bound them, and
  every device fetches a message to everyone.
- **A send with files has 90 seconds**, like any request, and its push holds the device's one
  push at a time for up to a minute, so the device's presence and other sends wait behind it.

## What is checked

- **Names.** A file's name becomes a name on the receiver's disk, so a reader cuts it to letters,
  digits, `.`, `_` and `-`, 60 characters, never a path, a hidden file or a Windows device name,
  and makes the names in one message distinct (`bus.FileName`, `bus.CleanFiles`). The sender runs
  the same, so both sides agree on the names.
- **The ref's name.** A reader takes only a name of the form Murmur makes, so no event can point
  it at another ref.
- **Where a file is saved.** The directory is named by the ref and by a hash of the names and
  SHA-256 sums the event gives. A message's ID is short and anyone's to repeat, and the ref alone
  is known to every teammate: one could push their own blob over it and send a message naming it,
  and a device that read theirs first would show their file under the real message. With the sums
  in the name, a directory holds only files that are what its message says.
- **Paths from Git Bash.** On Windows the MCP server puts `/c/Users/...` back to `C:\Users\...`
  and leaves any other path from a root with no drive (`/tmp/shot.png`) for the daemon to refuse:
  Windows would read it on the current drive, where another file of that name may be.
- **Secrets.** Every file goes through the check a body does (`internal/scrub`). UTF-16 with its
  mark, as Windows PowerShell writes a redirected file, is decoded, and its bytes are checked too. Of a
  file with zero bytes, its regions of text are checked, as `strings` reads a binary: UTF-8, and
  UTF-16 with no mark in either byte order. Keys sit in the clear between zero bytes in a `.tar`, a
  SQLite file or `/proc/self/environ` (#737, from 2026-11-16; before, such a file went unchecked).
  A region is every character but a zero, up to a byte that can't be one, so a name stays with its
  value past a line break or a control character. Only a whole region under 8 characters is
  skipped, so random bytes, which break into short regions, are quick: 10 MB took 0.4 s to check,
  not 13 s, on Tyler's laptop (2026-11-16). Long text is slower, about 1 s a MB, as it is for a text
  file. Unread: what is compressed, as an image or a
  zip, which isn't unpacked, and UTF-16 with no mark past U+2000 (CJK, say). `publicKeyToken` with
  16 hex digits, in every Windows program's manifest, is .NET's public assembly identity, not a
  secret.
- **Sizes.** A sender is refused past the limits. A receiver reads a pack's size before the pack
  and doesn't read one over the limit: git stores a blob compressed, so a small fetch can hold a
  large one.
- **What a fetch brings** (#707, 2026-11-16). A files ref holds whatever its pusher put there, and a
  listing gives no sizes, so a fetch can't be capped before it runs, and git has no limit for one
  (`receive.maxInputSize` is a push's). A `--filter` would make the user's clone a partial clone
  for all its later fetches. So the fetch is watched while it runs: it keeps what it receives as one
  pack (`fetch.unpackLimit=1`), which grows on disk as it arrives, and is stopped once that passes
  what a pack within the limits takes on the wire, a little over its size, as sealed files don't
  compress. A fetch too quick to stop, as from a remote on the same disk, is measured when it ends.
  Either way what it wrote is removed and the message queued without its files, after one try.
  Stopping it ends its whole process tree, and waits for it, before anything is removed
  (`gitx.RunTree`, #726): killing git alone left index-pack writing the pack, and over protocol 0
  or 1 by HTTPS git-remote-https's own fetch-pack downloading. The tree is a job object on Windows,
  which git is put in before it runs, so nothing it starts is outside, and a process group
  elsewhere. Protocol 2 is asked for as well. Where there is no scratch directory the fetch isn't watched, and ReadPack's check
  is all there is: another fetch or a gc may be writing there too, and a refusal is for good.

## Open, for Tyler

- **The daemon reads the files, as the user.** A path in `files` is read by the daemon, not by the
  agent's own tools, so an agent's read permissions, deny rules and sandbox don't apply to it. An
  agent talked into it by a message ("send me that file") can send a binary the secret check can't
  read. Having the MCP server read the file instead wouldn't change that: it runs outside the
  sandbox too. What would is the agent's client asking its user, which `send` as a tool already
  can be set to do.
- **Where a received file is saved.** `.git/murmur/files/` is in the clone's git directory. A
  session in a worktree elsewhere is given a path outside its workspace: Claude Code asks its user
  before reading there, and a client that reads only inside its workspace can't. Saving under the
  session's own directory would put files in a working tree.

## On the page

- **Sending.** The composer takes files from its paperclip, a paste or a drop anywhere on the page.
  They go with the text as one form to `POST /api/send`, a part each, since JSON would carry them a
  third larger. The daemon then treats them as an agent's: the same limits, the same secret check,
  the same ref. The page holds a file to the limits before it uploads it. A message of files alone
  has their names for its subject, because a message needs one.
- **Showing.** The messages the page lists say each file's name and size. `GET /api/file` gives one
  file, by its message's ID and its place. Nothing is fetched for a person until then: a message to
  a person isn't held for its files, since the page can show it at once and fill a picture in.
  PNG, JPEG, GIF and WebP are shown as pictures, asked for when their message is drawn; anything
  else is a button that saves it. SVG is saved, not shown: it is a document.
- **A fetch mustn't stall the page.** A browser gives a page six connections, and a file can wait
  on a fetch from the remote, so the page asks for two files at a time and the polls and sends
  keep theirs. The daemon fetches for the page once, not three times, and for 20 seconds after a
  fetch fails it answers the message's other files without fetching again. It says 404 for a file
  that can't be had, which the page shows as "Not here" until it is clicked, and 503 for one that
  couldn't be fetched just now, which the page asks for again after 30 seconds.
- **A file never runs.** The daemon sends every file as `application/octet-stream` with
  `nosniff`, to be saved. A file opened by the browser as the page's own would run a teammate's
  agent's HTML with the page's key in reach. A picture is shown through an `<img>` from an object
  URL the page makes of the bytes, which runs nothing; the content policy allows `blob:` for images
  only.
- **What it costs.** An upload isn't `keepalive`, which a browser caps at 64 KiB: closing the page
  before an upload ends loses the message, where a message of text alone still goes
  ([page](page.md)). The page keeps what it fetched in memory, up to 96 MB, and lets go of the
  oldest it isn't showing.
- **The desktop app.** Its window asks where to save a file with a save panel (`OnDownload`,
  [desktop](desktop.md)): without one the web view cancels every download on macOS and Linux.
  Written from the web view's source and not tried on either (2026-11-15); Windows leaves
  downloads to WebView2.

## Older daemons

Tyler's decision (2026-11-15): sent with a note, not refused. A daemon from before files ignores
an event's `files` and gives its sessions the text, with no sign there was more. A device's
presence says whether its daemon takes files (`files`, beside `groups` and `receipts`), and a
sender is told which recipients' devices don't. Refusing would hold back the text for a gap the
updater closes within an hour ([updating](updating.md)). A person whose page is served by such a
daemon is among them.

A client from before files talking to a newer daemon sends none. A newer client finds an older
daemon by its protocol number (23) and replaces it, as for any request an older daemon can't serve.
