# How Murmur fits together

Murmur is one binary, `mur`, playing several parts. Each topic has a file of its own in this
folder; this page says which part does what, which state each owns, and what must stay true when
you change one. The rest is in the files it links to.

## The parts

- **The daemon** (`internal/daemon`), one per clone. It holds every session's state.
  [daemon](daemon.md)
- **The MCP server** (`internal/mcpserver`), one per agent session, over stdio. It is thin: it
  turns tool calls into requests to the daemon, and starts the daemon where Murmur is on
  ([joining](joining.md) says where).
- **The hooks** (`internal/hook`), run by an agent after a tool call or a turn. They give the
  session its notices and never start a daemon. [other agents](clients.md)
- **The commands** (`cmd/mur`): `init`, `join`, `ui` and the rest, for people and for agents with
  no tools. [joining](joining.md)
- **The page** (`internal/web`), served by the daemon on 127.0.0.1 for people. [page](page.md)
- **The bus** (`internal/bus`): events as commits on `refs/murmur/*` on the clone's remote, read
  and written with git plumbing. [transport](transport.md)

## Who owns which state

- **Messages:** the sender's own log, `refs/murmur/mesh/<deviceId>`, encrypted.
  [delivery](delivery.md), [encryption](encryption.md)
- **Who is on, and what they're doing:** each device's `refs/murmur/presence/<deviceId>`.
  [presence](presence.md)
- **Session names:** `refs/murmur/names`, shared by every device. [session names](session-names.md)
- **The team key:** `refs/murmur/team`, shared, and the keys this device holds in `.git/murmur/`.
  [encryption](encryption.md)
- **A session's inbox and everything else the daemon keeps:** `.git/murmur/state.json`, written
  only by the daemon.
- **The user's yes to joining:** `.git/murmur/joined.json`. [joining](joining.md)
- **The page's settings:** `.git/murmur/settings.json`. [page](page.md)

## What must stay true

- **Never touch the working tree or the index.** People and agents are editing that checkout at
  the same moment. Bus writes use `commit-tree` and `push <sha>:<ref>`. [transport](transport.md)
- **One writer per ref.** A device writes only its own mesh and presence refs, so its pushes never
  race. A shared ref (names, team) is written with a lease, as a compare-and-swap.
  [transport](transport.md)
- **Only the daemon polls the remote and writes the device's state.** Sessions and hooks ask it;
  the commands that set things up (`init`, `join`, `rotate`) run their own git, once.
- **The daemon proves itself before it gets a request.** A client sends its token only after the
  daemon answers a challenge, so a program that took the port learns nothing. [daemon](daemon.md)
- **Requests and replies are versioned** (`proto.Protocol`). A change an older daemon or client
  can't follow raises it. [daemon](daemon.md)
- **Most of the daemon's state sits behind one lock**, `mu`; a few narrower ones serialize syncs,
  writes to the device's log and the team key. A function whose name ends in `Locked` expects its
  caller to hold `mu`. Git calls and process starts happen outside it, so a slow one can't stall
  every session.
- **What goes on the bus is encrypted** ([encryption](encryption.md)), and every text is checked
  for secrets before it is sent, since the bus keeps everything ([VISION](../VISION.md), "Leaked
  secrets").

## Which test to write

- **A rule you can state without git**, such as a schedule, a parse or a message's wording: a unit
  test in the package, with controlled time and fakes. They run in milliseconds.
- **Anything that reads or writes refs:** a test against a local bare remote, in `internal/bus` or
  with `newDevice` in `internal/daemon`.
- **Anything between devices**, such as a message, presence or a name: two `newDevice`s on one
  remote. These are most of CI's time, so keep them to what needs two devices.
- **A command people or agents run:** the `cmd/mur` harness (`newBox`), which gives each test a
  scratch home. Never run `mur join`, `init` or `uninstall` against your own home.

How the tests run, and what they cost, is in [testing](../testing.md).
