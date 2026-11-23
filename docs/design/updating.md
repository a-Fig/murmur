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
  between the two renames left no `mur.exe`. `install.ps1` swaps the same way since #959
  (2026-11-22), and falls back to the two renames where it can't. The next daemon start removes
  staged files a kill left behind. A lock beside the executable serializes clones sharing an
  install, and a manual reinstall during the download is left alone. An install the daemon can't
  write (a package manager's) is logged once and left alone.
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
  with the install command when updates are off or the last check failed. It learns the latest
  release from the updater, else from the `latest.json` a daemon in this clone recorded, and never
  asks GitHub itself; `mur version` hears of newer releases the same way.
- **Opting out.** `git config --global murmur.update false`; a repo's own git config overrides it,
  and it is read on each check. `MURMUR_NO_UPDATE=1` opts out a daemon started with it. Agents
  explain updates and the opt-out before joining (`docs/agents/join.md`). `git config murmur.channel
  prerelease` opts into prereleases and nightlies (#244, Tyler, 2026-11-08), so a release runs on
  our own machines before a stable tag sends it to everyone (`docs/releasing.md`). A binary shared
  across clones shares their update settings too, so to keep every clone from updating it, use the
  global setting without local overrides.
- **What the checksum proves.** It catches corruption or a download inconsistent with the release,
  not who published it: a compromised GitHub account, or a release with a replaced checksum file,
  gets through. Release signing is #193.
