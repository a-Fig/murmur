# Testing

`go test ./...` runs every Go test, including those that run two devices against a local bare remote.
A change is checked with `gofmt -l .` (empty), `go vet ./...` and `go test -race -count=1 ./...`.
The site has its own tests, of its Worker and of the files machines read on it (sitemap,
robots.txt, llms.txt, the pages' metadata): `node --test --import ./site/test/register.mjs
site/test/*.test.mjs`, with Node 20.6 or later and nothing to install. `test-linux` runs them.
The `daemon` package is most of the time (#39 is about making it faster). Its tests that share
nothing with the others call `t.Parallel()`. Under `-race` on CI that took the package from 464 s to
304 s on Linux, 798 s to 599 s on macOS and 1,500 s to 1,027 s on Windows (2026-11-09: main at
c9376fa, run 42689370600 in a-Fig/murmur, against the first run of the change, 42699090516 in the
contributor's fork, whose macOS job failed one test that has since been fixed). Windows takes
longest, so it decides when a pull request is green.

Go runs a package's serial tests first, one at a time, and the parallel ones after them, so the
two never overlap. A daemon test stays serial if it sets the environment (`t.Setenv` panics in a
parallel test), replaces a package variable, writes into the `HOME` or
`CODEX_HOME` that `TestMain` sets, or counts on timing or a port number that the other tests' load
could upset; those last say why in a comment. The stand-in `t3` is no reason to: `useFakeT3` puts
the test binary in the test's own directory, as a hard link or, on Windows, a copy, and the stand-in
reads its calls file and its fail and sleep settings from files beside it, not from the environment
(#282). That made 53 tests parallel. Three that use the stand-in stay serial:
`TestASessionWhoseMessageDidntFitGoesOnWithTheTurn` replaces a package variable, and
`TestDaemonLeavesOnceTheThreadIsNoLongerKept` and `TestDaemonWithAThreadToWakeStaysUpAndHandsOver`
time the daemon's own exit, which the first of them missed once when run in parallel.
`go test -race -count=1 ./internal/daemon` took 1,354 s and 1,199 s before the change and 1,013 s
and 1,015 s with 55 parallel, on an 8-core Mac that other work was loading heavily throughout
(2026-11-11, main at 3c275e4); with 53, on main at 6c1ca2e and as loaded, it took 1,352 s, so the
gain on a quiet machine is still to be measured, in CI's timings.

A parallel test meets more load than it did alone, so a wait it took for granted may come late. A
call's end, for one, is answered before its after-snapshot is taken: a test that commits right
after an end looks the session up first, since a lookup waits for the ends still being judged. And
a heartbeat can come later than the tests' 3 s stale limit: `who` still lists a stale device, but a
send to its sessions says nobody may read it. `seesSession` and `nameIs` wait for the device to be
fresh as well as listed, and `listsSession` is for waiting until a session is gone (#387).

A package whose tests can reach Murmur's `murmur.*` git config calls `testgit.Isolate` in `TestMain`,
so a developer's own settings don't change what they see (#262). As of 2026-11-09 those are `update`,
`publicrepo`, `setup`, `daemon` and `cmd/mur`; the others that reach it passed with all three keys
set.

## On every pull request

`.github/workflows/test.yml` runs those checks on every pull request, on a push to `main`, and by
hand. Its jobs run at once, on GitHub's own runners, which are free for a public repo and start
fresh for every job. A pull request from a fork runs on them the same way.

**A pull request runs only the jobs its files need** (2026-11-12, #512). The `changes` job gives
the names of the files changed to `scripts/ci-level.sh`, which answers with one word:

| Level | The change touches only | What runs |
|---|---|---|
| `none` | Markdown and images at the top of the repo, in `docs/` and in `.github/`, other than `README.md` and `docs/agents/` | nothing |
| `site` | `site/`, and the above | `test-linux`, the site's tests alone |
| `page` | `internal/web/static`, and the above | `test-linux`, whole |
| `full` | anything else | every job |

The page's files are built into the binary, so they need the Go tests, but CSS and JS don't behave
differently per OS. A file no rule names is `full`, and so is a list of files GitHub couldn't give,
so a mistake runs too much. `scripts/ci-level-test.sh` checks the rules, and `changes` runs it
before it uses their answer. A push to `main` and a run by hand are always `full`. On a pull
request that isn't `full`, the daemon shards show as one skipped check with its name unfilled
(`test-${{ matrix.os }}-daemon ...`): GitHub doesn't expand a matrix it skips.

**A test that fails is run once more** (`scripts/go-test-retry.sh`). Daemon tests give up a wait on
a slow runner, a different one each run: three of the four full runs after #504 lost a Windows
shard that way (#513), each a rerun by hand. The job passes if the retry does, and says what it
retried in an annotation and in the run's summary, which is where a test that needs the retry often
shows. A build failure, a data race, a test binary that panics, exits early or reaches `-timeout`,
a failed example, and more than five failures at once are not retried. What the retry does hide is
a test that fails only beside another: alone, it passes.

`internal/daemon` is most of the Windows time: on 2026-11-12 it took 2,975 s of a 50-minute budget
(the rest of the suite together about 8 minutes), and runs from #489, #500 and #501 died at the
`-timeout 50m` with no test hung (#496). So `test-windows` runs every other package, and
the `test-daemon` job, whose checks are named `test-windows-daemon (N)`, runs the daemon's tests on four runners at once, each taking the tests whose
position in the sorted `go test -list` output is its shard number modulo 4. A test added later falls
into some shard by itself. macOS does the same on two runners, `test-macos-daemon (N)`, since
2026-11-12: the package was 1,154 s of `test-macos`'s 20 minutes, which made it the longest job.
Linux runs the package whole, in about 6 minutes.

Until 2026-11-01 some jobs ran on self-hosted machines, because GitHub's runners didn't start on
this account while the repo was private: the macOS job on a machine of Tyler's that also did other
work (until 2026-10-31), and the Linux job on a Spot VM in Google Cloud (project `t4-ci-3690`) that
stopped itself when idle and was woken by a webhook. They went before the repo went public. A
self-hosted runner runs whatever code a pull request brings and isn't wiped between jobs, and
GitHub advises keeping them off public repositories. The VM's setup and its wake service, `ci/`,
were deleted then (#113) and are in git history.

- **To test a pushed branch without a pull request:** `gh workflow run test.yml --ref <branch>`.
  Then `gh run watch` follows it, and `gh run view --log-failed` shows what failed.
- **gofmt doesn't run on Windows.** A Windows checkout turns line endings into CRLF, which gofmt
  reports as unformatted; the other two jobs check it.
- **A newer push to the same pull request** cancels the run in progress. A push to `main` never
  cancels another's: until 2026-11-12 it did, which left merges made close together with no
  finished run, and the nightly takes the newest commit whose run passed.
- **A daemon test that gives up waiting** prints every goroutine's stack, as the daemons run in the
  test's process; `MURMUR_TEST_LOG=1` prints their logs too. That is how a `git` whose start never
  came back on a macOS runner was found (#217).

## Pull requests from forks

A fork's pull request runs the workflow file as the fork changed it, so its code runs on GitHub's
runners with a read-only token and no secrets. The repository setting "Approval for running fork
pull request workflows from contributors" is set to "Require approval for all external
contributors" (Settings > Actions > General), so a fork's run waits for a maintainer first; read
what it changes under `.github/` before approving.
