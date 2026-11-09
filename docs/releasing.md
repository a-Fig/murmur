# Releasing

A release is a pushed tag. `.github/workflows/release.yml` builds `mur` for macOS, Linux and Windows
(amd64 and arm64) and publishes the binaries as a GitHub release, and `install.sh` and
`install.ps1` download from it.

## Cutting one

From a commit on `main` whose checks passed, and which a Codex (Sol) agent has reviewed whole for
bugs, security holes and anything else that shouldn't ship. That review is Tyler's rule
(2026-11-08): a release reaches every installed daemon within the hour (self-update), so a PR's
review isn't enough on its own; it never sees how the merged PRs fit together. Each finding is
fixed before the tag, filed as an issue if it can wait, or dropped if it isn't worth acting on.

```sh
git tag v0.1.0
git push origin v0.1.0
```

`gh run watch` follows the run; the release appears when it finishes.

- **The version** is the tag without its `v`, built into the binary with `-X main.version=...`, so
  `mur version` prints it. A plain `go build` shows the `0.1.0-dev` in `cmd/mur/main.go` instead.
- **A tag with a `-` in it** (`v0.2.0-pre.1`) is published as a prerelease. GitHub's "latest
  release" skips prereleases, so the installers' default finds only stable releases: while only
  prereleases exist, `install.sh` fails with "not found", and `MURMUR_VERSION=v0.2.0-pre.1` installs
  one.
- **A tag that isn't `vN.N.N` or `vN.N.N-suffix`** fails the run before anything is built.
- **What is published:** `mur-<os>-<arch>` (`.exe` on Windows) for darwin, linux and windows on
  amd64 and arm64, and `SHA256SUMS` listing them. The installers rely on those names.
- **To undo a release** (a bad build, or a test tag): `gh release delete <tag> --cleanup-tag --yes`.
  Re-running a tag's workflow needs its release deleted first, since the run creates the release.
  Installed daemons update hourly and never go back, so deleting stops further updates but doesn't
  undo the ones taken.

## Channels

A stable tag reaches every installed daemon within the hour, so a release runs on the prerelease
channel first (#244).

- **Stable**, the default: a daemon takes GitHub's latest release, which is never a prerelease.
- **Prerelease**: `git config --global murmur.channel prerelease` (or a repo's own config) makes a
  daemon take the highest version among all published releases, prerelease or stable, compared as
  versions rather than by date. Tyler's machines run it. Unsetting it doesn't go back: a daemon on
  a nightly stays there until a stable release is higher.

Daemons from before the channel (v0.2.3 and earlier) don't read `murmur.channel` and keep taking
stable releases, so a machine joins the channel at the first stable release that has it. The key is
separate from `murmur.update` because those daemons read that one as a bool, and an unknown value
there reads as "updates on".

## Nightlies

`.github/workflows/nightly.yml` runs at 07:23 UTC. It takes the newest commit on `main` whose `test`
run passed and, if `main` has changed since the last nightly and the latest stable release, tags it
`vX.Y.Z-nightly.YYYYMMDD`, where X.Y.Z is the latest stable release with its patch raised by one: it
sorts above that release and below the next. It publishes the tag by calling `release.yml`, since a
tag created with `GITHUB_TOKEN` starts no workflow, and then deletes all but the newest seven
nightly releases and tags. A nightly tag with no published release (a failed build) doesn't count
as out, so the next night tags its commit again; pruning removes the dead tag later. A rerun the
same day finds the date's tag taken: delete it by hand first. Running the workflow with `dry_run`
prints what it would tag and delete. Nightlies get no pre-release review (Cutting one, above): they
reach only daemons on the prerelease channel.

## Cutting a release through the channel

1. Tag a release candidate from `main`: `git tag v0.3.0-rc.1 && git push origin v0.3.0-rc.1`. The
   prerelease channel takes it within the hour.
2. Fix what turns up on `main` and tag `-rc.2`, and so on. Nightlies are still numbered from the
   last stable release (`v0.2.4-nightly.*` after `v0.2.3`) and sort below the candidate, so the
   channel stays on the candidate until the next one.
3. Once a candidate has run for a while without trouble, tag the same commit `v0.3.0`.

## Installing from a release

The README has the one-line installs, and each script's header lists its options. This is what
those don't say:

- **What the checksum proves.** The binaries and `SHA256SUMS` come from the same release, so the
  check catches a damaged or truncated download, not a release someone with write access tampered
  with.
- **Unsigned binaries.** A binary downloaded by a browser is blocked by Gatekeeper on macOS and
  warned about by SmartScreen on Windows, because it isn't signed; a download from the command
  line avoids both (2026-10-29). That is why the installers download from the command line, and why
  the release page isn't the way to install.
- **Dock apps.** On macOS, an app started from the Dock doesn't get the Terminal's PATH, so
  `install.sh`'s PATH edit doesn't reach it; anything that must find mur from there uses the
  absolute path the installer prints.

## Building from source

With Go 1.26 or later:

```sh
git clone https://github.com/a-Fig/murmur && cd murmur
go build -o ~/bin/mur ./cmd/mur      # anywhere on PATH; mur.exe on Windows
```

Such a build says `0.1.0-dev` in `mur version`, which is also why it never updates itself.
