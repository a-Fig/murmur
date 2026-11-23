# Updating

Murmur updates itself by default (#192, Tyler, 2026-11-07), so fixes reach an installed daemon
without anyone reinstalling. `internal/update` checks GitHub's `a-Fig/murmur` releases/latest at
daemon start and hourly, with no repo information in the request; a network failure waits quietly
for the next hour. A development build (`-dev`) never updates itself. Versions from before the
updater need one manual install to get it.

- **Download and install.** The daemon downloads its `mur-<os>-<arch>[.exe]` asset and the release's
  `SHA256SUMS`, checks the hash, and stages the file beside the running binary's resolved target.
  The downloads never leave https (2026-11-09), since whoever could serve the binary over plain http
  could serve its checksum too. Hosts aren't limited, because GitHub has moved its asset host before
  and a daemon refusing the next one could never update past it. The new file is renamed over the
  old in one step, so a kill at any moment leaves a whole binary there, old or new (#944). Windows
  allows that rename over a running exe only while the exe has a second name, so the updater first
  hard-links it to a backup (`mur.exe.old-.mur-update-*`), which goes once nothing runs it; where
  links fail (FAT), it doesn't update. Before #944 it renamed the running exe aside first, and a kill
  between the two renames left no `mur.exe`. The next daemon start removes staged files a kill left
  behind. A lock beside the executable serializes clones sharing an install, and a manual reinstall
  during the download is left alone. An install the daemon can't write (a package manager's) is logged once
  and left alone.
- **Restart.** After installing, the daemon waits until no handoff is in flight and no page counts
  as open, or until the page asks for the restart (#240), and until no key to its page that another
  clone's page handed out is still good (#348), since the next daemon's page wouldn't know it. It
  then shuts down normally and starts the installed binary with its own flags, and sessions
  reconnect through the grace. Running MCP servers keep their loaded binary until their sessions
  restart. The protocol number ([daemon](daemon.md)) still handles clients and daemons that can't
  talk; release versions are not a second protocol.
- **The page's socket is handed over** (#255, decided 2026-11-09 from Sol's pre-release review). The
  old daemon passes its listening socket to the new one (descriptor 3 on Unix, the same handle on
  Windows, named by `MURMUR_PAGE_LISTENER`), with the page's address and token beside it in
  `page.json`. The port stays bound throughout, so a tab's requests wait in the socket's queue and
  the new daemon answers them; the tab then reloads onto the new version, unless a person has text
  in a composer or a message that failed, which live only in the page.
  - *Why.* Before, the new daemon bound the old port again and kept the token, and in the gap
    another user's process could take the port and be sent one of the tab's requests with its token.
    Checking that the port is free can't prove it stayed free, so the token now goes only with the
    socket: a daemon that gets `page.json` without the socket (a start through WMI, which can't hand
    handles over) serves the page on a new port with a new token, and the tab needs `mur ui` again.
    Rotating the token on every restart was set aside, since a page served on the port in the gap
    could read whatever the tab holds.
  - On Windows a socket stays tied to the I/O completion port of the process that served it, so the
    new daemon detaches it before taking it up.
- **On the page**, a newer release shows above Settings: "Restart to update" while an installed
  update waits on the open page, "Updating to X" while the daemon will install it, and "X is out"
  when updates are off here or the last check failed, which it says apart. It learns the latest
  release from the updater, else from the `latest.json` a daemon in this clone recorded, and never
  asks GitHub itself; `mur version` hears of newer releases the same way, and prints the install
  command. Settings shows no commands (Tyler, 2026-11-22).
- **Update now** (#1045, Tyler, 2026-11-22). When "X is out" and updates won't install it, because
  they are off here or the last attempt failed, About offers "Update now". It asks the daemon's
  updater for one check that installs even while updates are off (`Options.Install`), through the
  same verified download, rename and restart as an automatic update, and leaves the setting as it
  was. The page sends the version it showed, and the updater installs only that one: if GitHub
  now has another (a newer release, or a stable one where a nightly was shown before the channel
  changed), it installs nothing and the row shows the release it found instead (Opus review). The
  request returns at once; the row says "Installing X", then "X is installed" with Restart now, or
  why it failed.
  - *Not offered* while updates are on and working (they install it within the hour, or at once
    after a change), while an update waits for its restart, once this daemon's updater has
    stopped (a binary it can't replace), since nothing would act on it, or while
    `MURMUR_NO_UPDATE` is set on the daemon, which may mean something else manages the binary.
  - *With updates off*, the updater makes no request, so the page knows of a newer release only
    from a check this clone's daemon made before they were turned off (`latest.json`). A clone that
    has always had them off isn't told of releases, and gets no button.
- **The setting** (#1045, Tyler, 2026-11-22) is one per machine: Off, On (stable releases, the
  default) or On, nightly (prereleases and nightlies too), in Settings' About and as `mur updates
  [off|on|nightly]`. It is the global git config, since one binary serves every clone: Off is
  `murmur.update false`, nightly is `murmur.channel prerelease` (#244, so a release runs on our own
  machines before a stable tag sends it to everyone, `docs/releasing.md`), and On is neither. Both
  are read on each check, so a change needs no restart; the page's change also asks its daemon for a
  check within seconds (10 s apart at most, against GitHub's hourly allowance per address), while
  other clones' daemons and `mur updates` wait for the hourly one.
  - *Overrides.* A repo's own git config, the system's, and `MURMUR_NO_UPDATE=1` on the daemon
    still win, as git's order has them. The page and `mur updates` show the machine's choice and,
    under it, what applies here and why ("Off here: this repo's git config sets murmur.update"),
    so a choice that isn't in effect never shows as if it were. The endpoint (`/api/updates`) takes
    the page's token, as every call does, and only the three choices.
  - *Shared binaries.* A binary shared across clones shares their update settings too, so to keep
    every clone from updating it, use the global setting without local overrides.
  - *From nightly to On* doesn't go back to an older stable release: the updater installs only a
    version newer than the one running, so a nightly stays until a stable release passes it.
- **Going back to stable** (#1045, Tyler, 2026-11-22), for a prerelease that misbehaves. "Go back to
  stable" in Settings, or `mur updates back`, is offered only while a prerelease runs and updates
  are on here. It writes the prerelease's version to the global `murmur.updateSkip`, and the next
  check installs GitHub's latest stable release in its place, older or not, through the same
  verified download, rename and restart; never an older one than that. From then on no prerelease
  installs that isn't newer than the one gone back from, and the page says so ("On stable until the
  next nightly after X"). Stable releases still install as they come out, on either channel, so the
  machine never stays below the latest stable one (Opus review: a hold on every release would have
  kept back a stable fix). The key is taken off once a newer version is in place, or found running.
  It isn't offered while an update waits for its restart, which would start that update instead.
- **What the checksum proves.** It catches corruption or a download inconsistent with the release,
  not who published it: a compromised GitHub account, or a release with a replaced checksum file,
  gets through. Release signing is #193.
