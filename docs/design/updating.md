# Updating

Murmur updates itself by default (#192, Tyler, 2026-11-07), so fixes reach an installed daemon
without anyone reinstalling. `internal/update` checks GitHub's `a-Fig/murmur` releases/latest at
daemon start and hourly, with updates off too (it then installs nothing), and with no repo
information in the request; a network failure waits quietly for the next hour. A development build
(`-dev`) never updates itself. Versions from before the updater need one manual install to get it.

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
  (2026-11-22); it installs the old way only where a running `mur.exe` can't be linked, or the
  PowerShell can't make the call, and if the new file then can't be moved in it puts the old one
  back (#1171). Every check, hourly and with updates off, removes staged files a
  kill left behind and the backups nothing runs any more, the updater's and `install.ps1`'s
  (`mur.exe.old-1a2b3c4d`, on Windows only). One a process still runs, as an MCP server may for
  days, stays until the check after that process ends (#1171, 2026-11-23). One of the installer's
  that is still a second name of the binary in place stays too, and all of them while no binary
  is in place: that is how an install stopped half-way looks from outside. The next `install.ps1`
  puts the newest such backup back as `mur.exe` before anything else, and removes none until the
  new `mur.exe` is in place (#1198). A lock beside the executable (`mur.exe.update.lock`) is held
  for a whole update, downloads included, and serializes clones sharing an install.
  `install.ps1` takes it too, for its whole swap: it waits ten seconds, then says so and changes
  nothing (#1198). An `install.ps1` from before that takes no lock, so a reinstall during the
  daemon's download is left alone. The setting and the channel are read again just before the
  swap (#1197): updates turned off during the download install nothing (unless a person asked for
  that install, *Update now* below), and a prerelease isn't installed after a move to stable, nor
  one a return to stable has held back since the check picked it, Update now included (#1212). An
  install the daemon can't write (a package manager's) is logged once and left alone.
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
- **About's version row** (#1114, Tyler, 2026-11-23, from the v3 design of #1018) says where this
  stands: `mur <version>` with the daemon's protocol and when it last learned of a release ("Checked
  5 minutes ago") under it, and on the right a dot with "Up to date", or a warning dot with "X
  available" and, when it applies, Update now or Restart now. The protocol and the time are the
  daemon's `GET /api/health` (`Daemon.Protocol`, `UpdateChecked`), read when Settings opens and every
  half minute; the time is that of the `latest.json` above, so no time is shown until a check has
  found a release. The row says nothing of the choice, which the Updates row below it shows, and
  only what is happening to the newer release.
- **Update now** (#1045, Tyler, 2026-11-22). When "X is out" and updates won't install it, because
  they are off here or the last attempt failed, About offers "Update now". It asks the daemon's
  updater for one check that installs even while updates are off (`Options.Install`), through the
  same verified download, rename and restart as an automatic update, and leaves the setting as it
  was. The page sends the version it showed, and the updater installs only that one: if GitHub
  now has another (a newer release, or a stable one where a nightly was shown before the channel
  changed), it installs nothing and the row shows the release it found instead, or no "X is out" if
  that one isn't newer than the one running (Opus review). With updates on, a newer one installs
  on its own at the next check, 10 s later. The request returns at once; the row says "Installing
  it", then "Installed" with Restart now, or why it failed.
  - *Not offered* while updates are on and working (they install it within the hour, or at once
    after a change), while an update waits for its restart, once this daemon's updater has
    stopped (a binary it can't replace), since nothing would act on it, or while
    `MURMUR_NO_UPDATE` is set on the daemon, which may mean something else manages the binary.
    In those last two About gives the installer's one line, with a copy button, instead
    (`Version.Install`, #1217, 2026-11-28), and the sidebar's "X is out" says Settings has the
    command. Without it a person learned the last update failed and found nothing to press.
  - *With updates off* (#1099, 2026-11-22), the updater still checks on the same schedule: at
    start, hourly, and when the page asks, at least 10 s apart. An Off check reads which release a
    check with updates on would install, records it (`latest.json`) and reports it, and stops
    there: it downloads nothing, takes no lock and writes no config (`look`), so even a clone that
    has always had updates off shows "X is out" and the button. Off means "don't install on your
    own", not "no network"; before #1099 it meant both, and the button the setting was built for
    never appeared on such a clone. `TestOffChecksButNeverInstalls` fails if an Off check downloads
    a release or replaces the binary. `MURMUR_NO_UPDATE` still means no checks at all.
- **The setting** (#1045, Tyler, 2026-11-22) is one per machine: Off, On (stable releases, the
  default) or On, nightly (prereleases and nightlies too), in Settings' About and as `mur updates
  [off|on|nightly]`. Off still checks, to know what is out (*With updates off*, above). It is the
  global git config, since one binary serves every clone: Off is `murmur.update false`, nightly is
  `murmur.channel prerelease` (#244, so a release runs on our own machines before a stable tag
  sends it to everyone, `docs/releasing.md`), and On is neither. Both
  are read on each check, so a change needs no restart; the page's change also asks its daemon for a
  check within seconds (at least 10 s apart, against GitHub's hourly allowance per address), while
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
  - *Back to a release from before the hold* (#1197, Sol's pre-release review of v0.5.4). v0.5.3
    and older don't know `murmur.updateSkip`, and take a prerelease whenever `murmur.channel` is
    `prerelease`: gone back to, v0.5.3 installed the same nightly again, which went back again. So
    on the nightly channel Back also makes the global `murmur.channel` `prerelease-held`, which
    those releases read as stable and later ones as nightly, and the updater holds it again just
    before it installs the release gone back to, in case it reads `prerelease` by then. Once the
    hold is off, it writes `prerelease` again. While such a release runs, the machine waits for the
    next stable release, not the next nightly.
  - *A channel Back can't hold* (#1212, Sol's re-check). v0.5.3 reads `murmur.channel` in every
    scope, so a repo's own `prerelease` (how `git config murmur.channel prerelease` writes it,
    v0.5.3's way to nightlies), the system's or an include's still sends it to the nightly. Going
    back from a prerelease of v0.5.4 or earlier may land on v0.5.3, so Back looks, in this clone
    and every clone on the machine's record (`internal/clones`, one binary serving them all), for
    a channel v0.5.3 would still read as `prerelease` with the global one held. If it finds one,
    it writes nothing and names it, to be taken out. The updater looks again before it goes back
    to a release from before v0.5.4, for an override made since, and the page then says why it
    can't go back. From a later prerelease Back doesn't look: the stable release gone back to
    knows the hold, wherever the channel is set. Back judges by the version running, not knowing
    the release it will land on, so from a 0.5.4 prerelease after v0.5.4 is out it refuses all the
    same; the updater, which knows, doesn't. It looks before it writes anything, since a hold
    written during the look would leave the channel held with nothing held back.
  - *At most once per install.* What the look can't see (a clone not on the record, a person on
    v0.5.3 setting `prerelease` again) still sends v0.5.3 to the nightly. So the updater records
    each install that goes back from the prerelease in the hold (`murmur.updateBack`, the version
    and the executable's path), before the swap, so that a kill after it loses nothing; a swap that
    fails takes the record out again. An install found running that prerelease again takes the
    hold off and stays on it: whatever the older release read chose it (Opus's second review of
    #1197). Each Back is then one trip at most per install, never a loop. A kill between the record
    and the swap reads the same way: the nightly stays, and Back is offered again. The hold comes
    off for the whole machine. Once v0.5.4 is the latest stable release, every release gone back
    to knows the hold and none of this arises.
- **What the checksum proves.** It catches corruption or a download inconsistent with the release,
  not who published it: a compromised GitHub account, or a release with a replaced checksum file,
  gets through. Release signing is #193.
