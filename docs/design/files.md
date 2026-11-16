# Files with a message

#570, 2026-11-15. A user's feedback (2026-11-14): "Not being able to send images in the chat is
also a bad thing tbh... limits the potential of the agent." Until then Murmur carried text: an agent
couldn't send another a screenshot, a log or a diff.

`send` takes `files`, a list of paths. The recipient's agent gets each as a path on its own machine,
which it opens with its own tools; one that reads images sees the image. The page doesn't show or
send files yet (#570's second part).

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
  file names, and a turn made from it later says the files aren't there. A sender that never runs
  again leaves its refs.
- **A send to several recipients uploads the files once for each**, since each gets a message of
  its own (#520).
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
- **Secrets.** A file that is text goes through the check a body does (`internal/scrub`): UTF-16
  with its mark, as Windows PowerShell writes a redirected file, and anything with no zero byte. An
  image or other binary can't be checked.
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
  The fetch is on protocol 2: stopped on 0 or 1 over HTTPS, git-remote-https's own fetch-pack goes
  on downloading. Where there is no scratch directory, only a pack still being written counts and
  nothing is removed, as another fetch or a gc may be writing there too.

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

## Older daemons, and people

Tyler's decision (2026-11-15): sent with a note, not refused. A daemon from before files ignores
an event's `files` and gives its sessions the text, with no sign there was more. A device's
presence says whether its daemon takes files (`files`, beside `groups` and `receipts`), and a
sender is told which recipients' devices don't. Refusing would hold back the text for a gap the
updater closes within an hour ([updating](updating.md)). A sender to a person is told the same
until the page shows files.

A client from before files talking to a newer daemon sends none. A newer client finds an older
daemon by its protocol number (23) and replaces it, as for any request an older daemon can't serve.
