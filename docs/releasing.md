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
  amd64 and arm64, `SHA256SUMS` listing them, and, once the signing secret is set,
  `release.json` and `release.json.sig` (Signing, below). The installers and the updater rely on
  those names.
- **The Go it's built with** is `go.mod`'s `toolchain` line, which `setup-go` installs ahead of
  the `go` line (the oldest Go that builds the source). Until that line (2026-11-09), releases
  were built with go1.26.0, and the rc.1 binary could reach 36 standard-library vulnerabilities
  fixed in go1.26.1 to go1.26.10. The run checks every binary with `govulncheck` before publishing
  and fails on a finding, so a newly announced vulnerability stops releases and nightlies until the
  line is raised to the Go that fixes it. The line is raised by hand. A laptop's
  `govulncheck ./...` judges the standard library by the laptop's Go, not the release's.
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

## Signing

release.yml's "Sign the release" step runs `go run ./internal/update/signrelease`, which writes
`release.json` (the tag, its channel, `stable` or `prerelease` for a tag with a `-`, and every
published file's SHA-256) and `release.json.sig`, an SSH signature of it in the namespace
`murmur-release`, with the ed25519 key in the `RELEASE_SIGNING_KEY` secret (#193). A mur with keys
in `internal/update/keys.go` installs only a release whose signature verifies against one of them,
whose `release.json` names the tag it was published under, whose channel the daemon takes, and that
is newer than itself; the daemon log says why it refused one. With no key built in, a daemon checks
SHA256SUMS only, and a mur without a key ignores the signature.

As of 2026-11-09 there is no key: the secret isn't set, `keys.go` is empty, and the step signs
nothing. Once `keys.go` has a key, a run without the secret, or with a key `keys.go` doesn't list,
fails before anything is published.

- **What it doesn't stop:** someone with write access can still hold updates back, by publishing
  nothing or an unsigned release that daemons refuse; signing only keeps them from shipping their
  own. On the prerelease channel, a refused release that is the highest version holds the channel
  where it is until that release is deleted, as a daemon takes only the highest.
- **Forks** build without the secret; a fork that publishes releases replaces `keys.go`'s keys
  with its own, or empties it.

### Switching it on

1. Make two keys, on a machine you trust, outside any clone's working tree. Each command prints
   the key's public line:

   ```sh
   go run ./internal/update/signrelease -generate ~/murmur-release-1.key
   go run ./internal/update/signrelease -generate ~/murmur-release-2.key
   ```

   The first signs releases. The second is a spare, built in too but kept offline, so a lost or
   leaked first key can be replaced without stranding every installed daemon.
2. `gh secret set RELEASE_SIGNING_KEY -R a-Fig/murmur < ~/murmur-release-1.key`. Put both files in
   a password manager and delete them from the disk. From the next release on, releases are signed;
   daemons don't check yet.
3. Check that release by hand (below).
4. In a PR, put both public lines in `keys.go`. The releases built from it check signatures from
   then on.

### Rotating

- **Planned:** add the new key's line beside the others and release; after that, set the secret to
  the new key. Remove the old line once daemons have had time to update. A daemon that was off for
  the whole of that time refuses releases signed by the new key and needs one reinstall.
- **A leaked or lost key:** set the secret to the spare, take the leaked key's line out, and
  release at once. Every daemon already trusts the spare. Until it updates, a daemon still trusts
  the leaked key, so whoever holds it and can publish a release here could reach it. Then make a
  new spare and add its line.

### Where the secret can be read

A repository secret reaches any workflow run in this repo, including one that someone with write
access pushes on a branch. Protecting `main` and `v*` tags, then moving the secret into a GitHub
environment that only those refs may use (with `environment:` on the release job), narrows it to
the release workflow. Neither is set up as of 2026-11-09.

### Checking a release by hand

`ssh-keygen -Y verify` needs OpenSSH 8.1 or later; `ssh -V` prints the version. Some Windows and
older Linux installs have an older one; on Windows, Git for Windows brings a current one
(`C:\Program Files\Git\usr\bin\ssh-keygen.exe`). In a POSIX shell:

```sh
tag=v0.3.0
gh release download "$tag" -R a-Fig/murmur -p release.json -p release.json.sig -p mur-linux-amd64
curl -fsSL https://raw.githubusercontent.com/a-Fig/murmur/main/internal/update/keys.go |
  grep -o 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5[A-Za-z0-9+/=]*' |
  sed 's/^/murmur namespaces="murmur-release" /' > allowed_signers
ssh-keygen -Y verify -f allowed_signers -I murmur -n murmur-release -s release.json.sig < release.json
grep -E '"(tag|channel)"' release.json   # the tag asked for, and its channel
shasum -a 256 mur-linux-amd64            # the sha256 release.json lists for it
```

In PowerShell, where `cmd /c` feeds `release.json` to ssh-keygen as raw bytes (a PowerShell pipe
would re-encode it, and the signature would fail) and `-Encoding ascii` keeps a byte-order mark out
of `allowed_signers`:

```powershell
$tag = 'v0.3.0'
gh release download $tag -R a-Fig/murmur -p release.json -p release.json.sig -p mur-windows-amd64.exe
(Invoke-RestMethod https://raw.githubusercontent.com/a-Fig/murmur/main/internal/update/keys.go |
  Select-String -AllMatches 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5[A-Za-z0-9+/=]*').Matches.Value |
  ForEach-Object { "murmur namespaces=`"murmur-release`" $_" } | Set-Content -Encoding ascii allowed_signers
cmd /c 'ssh-keygen -Y verify -f allowed_signers -I murmur -n murmur-release -s release.json.sig < release.json'
Select-String '"(tag|channel)"' release.json
(Get-FileHash mur-windows-amd64.exe).Hash.ToLower()
```

`ssh-keygen` prints `Good "murmur-release" signature` for a release signed by a listed key.

## Installing from a release

The README has the one-line installs, and each script's header lists its options. This is what
those don't say:

- **What the checksum proves.** The binaries and `SHA256SUMS` come from the same release, so the
  check catches a damaged or truncated download, not a release someone with write access tampered
  with. The installers don't check the signature: POSIX sh and Windows PowerShell 5.1 have no
  ed25519, and the installers are served from `main`'s tip, so until `main` is protected whoever
  could replace a release could replace the installer too. A mur with a key checks every update
  after the first install, and Signing, above, says how to check a release by hand.
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
