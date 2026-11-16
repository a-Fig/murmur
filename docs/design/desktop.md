# The desktop app

Added 2026-11-15 (#23). Tyler: "murmur, desktop app. I want it. I want it to be blazingly fast &
performant & light weight." `mur app` shows [the page](page.md) in a window of its own: the same
page, from the same daemon, so the app and the browser can't drift apart. The code is
`internal/desktop` and `cmd/mur/app.go`.

## What the window adds

Only what a browser tab can't do:

- **Its own window and icon**, in the taskbar or the Dock, with a dark title bar when the page's
  theme is dark.
- **The unread count**: a dot on the taskbar button and the count in the notification-area icon's
  tooltip on Windows, a badge on the Dock icon on macOS, and "(3) Murmur" in the title everywhere.
- **A notification for each message to the person** that arrives while the window isn't in front,
  three at most at once. Windows shows a balloon from the notification-area icon, which Windows 10
  and 11 turn into a toast (none shows under Do not disturb), and clicking it brings the window up.
  macOS uses `osascript`, so the banner is Script Editor's: a binary outside an app bundle has no
  bundle identifier, which `UNUserNotificationCenter` needs. Linux uses `notify-send` where it is
  installed.
- **Links to other sites open in the browser**, never in the window. A window that ends up on
  another page anyway (a file dropped on it) goes back to the clone's page within 10 seconds.

## How the page and the window talk

The window gives the page three functions. `murmurApp` takes what the page shows (its host, the
unread count, the theme) and the messages to the person as they arrive; the page calls it only when
one of those changes (`tellApp` in `app.js`). A script the window runs in every document sends
`murmurOpen` every link to another origin, which opens it in the browser, and `murmurHere` the
document's host. Any page in the window could call them, so none does anything a page shouldn't: they
set a title and a count, show a notification, open an http or https address in the browser, and
record a host, which can take the window only to a joined clone's page.

The page decides what is news, as it does for its screen reader announcements. The window decides
whether the person is looking, from the system: whether it is the window in front on Windows, the
active app on macOS. The page's own guess (`document.hasFocus()`) is taken only on Linux. One run on
2026-11-15 saw it stay true in a minimized WebView2 and another saw it go false, so it isn't relied
on where the system can say.

## The web view: glaze

The window is the system's web view: WebView2 on Windows, WKWebView on macOS, WebKitGTK on Linux.
It comes through [glaze](https://github.com/crgimenes/glaze), which loads it at run time with
purego, so there is no cgo.

- *Why not Electron:* it ships a browser engine of its own, about 100 MB, with each app.
- *Why not Wails v2:* it needs cgo on macOS and Linux, its own build tool, and a project laid out
  around assets it bundles, when the page here is served by the daemon.
- *Why not webview/webview_go:* cgo on every platform, and a C++ compiler on Windows.
- *What no cgo keeps:* the release job still cross-compiles every target from one Linux runner with
  `CGO_ENABLED=0` (`docs/releasing.md`), and no build needs a C toolchain.
- *What glaze costs:* it is young, with one maintainer (v0.0.83, November 2026). On Windows it calls
  `CreateWebViewEnvironmentWithOptionsInternal`, an undocumented export of the WebView2 runtime, so
  it ships no `WebView2Loader.dll`; a runtime that drops it makes `mur app` fail with an error that
  names it. It needs Go 1.27.1, so `go.mod` went from 1.26.0 to 1.27.1, with go1.27.3 as the
  toolchain releases are built with.
- *Tray and notifications aren't glaze's:* the author's `native` module has both, but its tray runs
  an event loop of its own, which can't share a process with the window's. Each platform's file here
  does them with a few system calls instead (`native_*.go`).

## In `mur`, except on Linux

The app is a command of `mur`, not a second binary, so the installers, the self-update and the
release stay as they are, and the app is always the daemon's version. What that costs each other
command differs by platform:

- **Windows:** nothing. purego makes plain system calls there, and glaze does nothing until a window
  is made.
- **macOS:** without cgo, as releases are built, purego puts a shim of its own (`fakecgo`) under the
  Go runtime, which then starts threads through the system's C library, for every command: the hook,
  the daemon and the MCP server too. A Go binary links that library on macOS anyway, and `mur help`
  took 20 to 26 ms on the macOS server before and after (2026-11-15), but the shim is code no release
  ran before, and `-race`, which every macOS CI test used, turns cgo on and leaves it out. So the
  macOS job also runs the commands' packages without cgo, on Apple silicon, and starts a
  release-built `mur` and its hook (Opus review, 2026-11-15). Only `mur app` keeps its main goroutine
  on the process's first thread, which AppKit needs.
- **Linux:** left out unless `mur` is built with `-tags desktop`, which a CI job vets. There the shim
  makes the Go runtime link glibc dynamically: a `mur` built with it is no longer static, and would
  fail to start where glibc isn't at the usual path (Alpine, NixOS, a distroless container), for
  every command, the hook included.
- **Elsewhere** (FreeBSD and the like): left out, and `mur app` says so.

## Which repo

A daemon serves one clone's page, so the app picks one:

1. the clone it was started in, as `mur ui` does, if that clone joined;
2. else the clone it showed last (`last.json` in `murmur/app` in the user's config directory);
3. else a joined clone whose daemon is running, else any joined clone (`internal/clones`).

The page's repo menu moves the window to another clone's page, as it moves a tab. The page tells
the window its host, the window finds the clone whose `daemon.json` names that host, and that clone
is the one remembered.

## Keeping its daemon

The app is someone waiting for messages, so its page marks every call as the app's (`X-Page-App`),
and the daemon doesn't exit while the window is open, minimized or not. That is all the mark does: a
minimized app's calls are still marked hidden, as a background tab's are, so the person isn't shown
as active, a daemon still restarts onto an update, and it polls the remote at the quiet rate, as it
would for a hidden tab. A notification then takes up to about 45 seconds: the daemon's 30-second
quiet poll, then the hidden page's 15-second one. A window under another window counts as hidden too (#638): its page's
document stays visible there, so the window checks each second whether it is in front, as it does for
notifications, and tells the page when that changes; back in front, the page polls at once. Linux has
no such check, so there an app under another window still counts as looked at. A first version left the hidden mark off instead, which did all three (Opus
review, 2026-11-15).

A daemon can still go: a crash, `mur stop`. Every 10 seconds the window asks for its clone's page,
starting the daemon if it isn't running, and goes to the new address when the daemon came back on
another port. So `mur stop` with the app open is undone within 10 seconds; leaving the repo isn't,
as a clone that left is never started again. A daemon that keeps failing to start is logged once,
not every 10 seconds.

## Starting it

`mur app` starts the window as a process of its own, watches it for 2 seconds and prints
"Opened.", so the terminal can be closed. A window that can't be made (no WebView2 runtime, no screen
to draw on) ends in that time, and `mur app` prints the reason instead. The window's output goes to
`app.log` beside `last.json`, which moves to `app.log.old` past 4 MB. `--wait` keeps it in the
foreground, `--devtools` lets the web view open its developer tools, and `--page ADDR` shows that
address and no clone's, for working on the app against the devserver
(`go run ./internal/web/devserver`).

## The launcher

Added 2026-11-15 (#616). The app starts as any other app does, from a launcher `mur app` puts in
place: each time it runs from a terminal and finds a repo to show, it adds the launcher, or moves
it onto this `mur` if `mur` has moved (a record in `launcher.json`, beside `last.json`, saves
looking each time). `mur uninstall` takes it out. `mur join` doesn't add it: an agent runs join,
and a person's Start menu is theirs to change; the person who runs `mur app` wants the app.

It touches only what is Murmur's (Opus review): a launcher of the same name that isn't (a bundle
whose Info.plist names another identifier, a `.desktop` file for another icon, any `Murmur.lnk` no
record of ours names) is neither replaced nor taken out. A launcher the person deleted stays
deleted until `mur` moves. A `mur` in a temporary directory, as `go run` and test builds are, puts
none in place, so working on the app never points the real launcher at a binary that goes.

- **Windows:** `Murmur.lnk` in the user's Start menu (found through `APPDATA`), with the app's icon
  as an `.ico` beside `last.json`. It runs `conhost.exe --headless mur.exe app`: `mur` is a console
  program, and a shortcut straight to it opens a console window for as long as `mur app` takes to
  start the window, which `--headless` doesn't (checked 2026-11-15). The shortcut carries the app's
  AppUserModelID (`brand.AppID`), and the window's process takes the same one, so its taskbar button
  is the shortcut's, and pinning it pins the app. It is written through the shell's ShellLink object
  with plain system calls, as the rest of the window's Windows code is.
- **macOS:** `Murmur.app` in `~/Applications`, where Launchpad and Spotlight find it. Its executable
  is a symbolic link to the installed `mur`, so `mur`'s self-update, which replaces the binary where
  it is, updates the app too, as a copy wouldn't. LaunchServices starts the link with the bundle's
  path as `argv[0]` and no arguments (checked on macOS 12), which is how `mur` knows to be the
  window (`FromBundle`): it runs the window in that process, so the Dock's icon is the bundle's.
- **Linux:** `dev.afig.murmur.desktop` in the user's applications and the icon in their hicolor
  theme, under `XDG_DATA_HOME`, in a build with the app (`-tags desktop`).

**One window.** A launcher clicked twice, or `mur app` typed while the app is open, brings the open
window forward rather than opening a second. The window's process listens on a loopback port and
records it, with its process and a random key, in `window.json`; a later `mur app` that reaches it
asks it to come forward and stops. A `mur app` typed in a joined repo, or given one with `-C`, also
moves the window to that repo, as it would have opened there. The process claims the port as it
starts, before the seconds a daemon can take to start, so a second click in that time finds it;
two clicks at once look for a window and claim under a lock file (`window.json.lock`), so the
second finds the first. On
Windows the later `mur app` first lets the window's process take the foreground, which Windows
otherwise gives only to the process the person just started. A window that ends without closing (a
crash) leaves its record, which the next launch can't reach, and it opens a new window.

**What it leaves for later:** the notifications are still a balloon on Windows and Script Editor's
on macOS. The AppUserModelID is what a toast of its own needs, and the bundle what macOS's own
notifications need; both are code still to write. A `mur app` typed in a terminal on macOS runs the
plain binary, not the bundle, so its Dock icon is `mur`'s until the window is started from the
bundle. A launcher started where no repo has joined fails without saying so, as there is no
terminal to say it in. A taskbar pin of the shortcut outlives `mur uninstall`, and stops working.
Not yet seen on macOS: the window opening from the bundle (the test machine has no joined repo),
and what privacy prompts a bundle that starts git and a daemon on repos in `~/Documents` or
`~/Desktop` brings, which macOS asks of the bundle, not of Terminal, and may ask again after an
update, as each build is signed anew.

## What it costs (2026-11-15)

Measured on the Windows 11 laptop (15 GB of RAM, other agents keeping the CPU between 40 and 100%)
and on the macOS server (macOS 12, Intel).

- **Size.** `mur` grows by 0.7 MB on windows/amd64 (27.9 to 28.6 MB) and by 0.5 MB on darwin/arm64.
  Linux's is unchanged, since the app isn't in it.
- **Start to first paint.** 0.9 to 1.3 s from launching `mur app --wait` to the page's first paint
  on Windows; about 0.5 s of it is WebView2 making its window. A tab in a browser that is already
  running paints about 0.2 s after the address reaches it (184 ms from navigation to first paint).
  A browser started cold couldn't be timed cleanly: Windows opened its window behind the one in
  front, and Chromium doesn't paint a window nothing can see.
- **Memory, idle.** On Windows the app is 7 processes, about 400 MB of working set and 190 MB
  private. A Chromium started for the page alone was 9 processes, 500 MB and 240 MB; one more tab in
  a browser already open costs its renderer, about 100 MB and 53 MB. WebView2 is Chromium, so on
  Windows the app is lighter than a browser started for it and heavier than a tab. On macOS the app
  was about 120 MB in all (mur 39 MB, WebKit's content process 69 MB, its network process 13 MB).
- **One flag.** The app runs WebView2's GPU work in its browser process (`--in-process-gpu`, unless
  `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS` is already set). It took the app from 470 MB of working set
  and 310 MB private to 400 MB and 190 MB; a page of text needs no GPU process of its own. The
  variable is set only while the web view is made, so the daemons and browsers the app starts don't
  inherit it.

## Unsigned

The binaries aren't signed, as `mur`'s never were. The installers download from the command line,
which macOS's Gatekeeper and Windows' SmartScreen don't check (`docs/releasing.md`), and `mur app`
is that same binary. A downloaded app bundle or installer would be blocked or warned about. Signing
would take:

- **macOS:** the Apple Developer Program ($99 a year), a Developer ID Application certificate,
  `codesign` with the hardened runtime, and `notarytool` to notarize each release, from a macOS
  runner or with the key on the Linux one. A bare binary can't have its ticket stapled, so Gatekeeper
  checks it online; an app bundle can.
- **Windows:** a code-signing certificate (an OV or EV one, a few hundred dollars a year, or Azure
  Trusted Signing at about $10 a month) and `signtool` or `osslsigncode` in the release job.
  SmartScreen still warns until a signature has built a reputation; EV and Trusted Signing build it
  faster.

## What it doesn't do

- A notification brings the window up, on Windows, but doesn't open its conversation.
- WebView2 keeps its data in `%APPDATA%\mur.exe`, which `mur uninstall` doesn't remove.
