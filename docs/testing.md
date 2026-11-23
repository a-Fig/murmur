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

The package runs at most 6 of its parallel tests at once, where `go test` would run one per
processor (`maxParallel` in `deliver_test.go`, #1036). On a 16-processor laptop with 15 GB, 16 at
once started about 1,400 git processes a minute, and a few tests gave up their waits, different
ones each run: main failed all three of three runs of 50 tests that way, and the same tests passed
at 6 (2026-11-22, one run, 429 s on a machine other work was loading). CI's runners have 4
processors or fewer, so it changes nothing there. `-parallel N` on the command line overrides it.

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

## Where the daemon tests' time goes

`scripts/daemon-test-times.sh [run] [slowest]` reads one run of `test.yml` back through `gh`: for
each job that tests `internal/daemon`, the job's and the go test step's time, go's time for the
package, how many tests ran at once on how many processors, the git the tests started by
subcommand, the tests that were retried, and the slowest tests. With no run it takes main's last
push run; a pull request's run has the Windows shards only at the `full` level, and only an rc
tag's run or a run by hand has macOS's (#1179). The
per-test times come from `go test -v`, which those jobs run with since 2026-11-22 (#1008): it costs
log lines, not time. `scripts/retried-tests.sh` counts retries over many runs.

The baseline, to set a later run beside: run 45109949890, at c2e5f654, 2026-11-22
(`scripts/daemon-test-times.sh 45109949890`). One run, so a job's time is good to about a factor of
two, as runners vary; the counts of git and the shares hold better.

| Job | go test step | Package | Tests | Test time | At once, on processors | Git started | A push, mean |
|---|---|---|---|---|---|---|---|
| test-linux (the whole package, beside every other package) | 482 s | 460 s | 665 | 904 s | 4 on 4 | 78,688 | 14 ms |
| test-macos-daemon (0) | 869 s | 850 s | 332 | 1,926 s | 3 on 3 | 55,991 | 210 ms |
| test-macos-daemon (1) | 733 s | 715 s | 333 | 1,605 s | 3 on 3 | 53,931 | 158 ms |
| test-windows-daemon (0) | 844 s | 807 s | 166 | 2,287 s | 4 on 4 | 28,283 | 602 ms |
| test-windows-daemon (1) | 1,000 s | 961 s | 167 | 2,957 s | 4 on 4 | 30,237 | 655 ms |
| test-windows-daemon (2) | 1,069 s | 1,030 s | 166 | 3,293 s | 4 on 4 | 26,453 | 792 ms |
| test-windows-daemon (3) | 1,128 s | 1,087 s | 165 | 3,594 s | 4 on 4 | 26,658 | 857 ms |

Test time is the tests' own times added up, without the time a test waited its turn. No test was
retried in that run.

What it shows:

- **On macOS and Windows the time is git's, and a git costs about 56 times as much on Windows as
  on Linux.** The same tests, but for five that are one OS's alone, took 904 s of test time on
  Linux, 3,531 s on macOS and 12,130 s on Windows. The tests started 78,688 git on Linux, which
  ran for 452 s in all (6 ms each); 109,922 on macOS, for 9,926 s (90 ms each); and 111,631 on
  Windows, for 35,996 s (322 ms each). Git's seconds pass the tests' on macOS and Windows because
  a test's daemons run several git at once; on Linux they are half the tests'. The slower machines
  also start more git for the same tests: on Windows `push`, `commit-tree`, `update-ref`, `fetch`
  and `for-each-ref` are 1.6 to 2 times Linux's, and only `ls-remote` is no more. This run doesn't
  say why. Linux's 452 s is loose, too: its means print in whole milliseconds, of 1 to 4.
- **By subcommand, on Windows:** `push` 12,157 s (16,815 of them), `ls-remote` 5,917 s (18,202),
  `fetch` 5,539 s (9,931), `update-ref` 4,725 s (25,090), `commit-tree` 3,574 s (16,265),
  `for-each-ref` 1,883 s (11,089). Those six are 94% of git's time there. So the helpers to look
  at are the ones behind a write to the bus (`commit-tree`, `push`, `update-ref`) and a poll
  (`ls-remote`, `fetch`, `for-each-ref`), not any one test.
- **No test file has more than 7% of the time.** The largest share of Windows test time is
  `switch_test.go` at 7.0%, with 23 of the 664 tests, so twice its share by count; then
  `files_test.go` 5.2%, `person_test.go` 5.0%, `names_more_test.go` 4.9% and `names_test.go` 4.2%.
  The top sixteen of 139 files are 57%. These shares aren't printed by the script: they are the
  per-job lists, with `slowest` at 1000, added up by the file each test is defined in.
- **The slowest single tests,** all on Windows: `TestDamagedRegistryFreesNoName` 123 s,
  `TestGarbageRegistryTip` 113 s, `TestSendByNameAcrossDevices` 96 s,
  `TestMessageForEveryoneReachesAThreadOnce` 95 s,
  `TestAHandoffThatMayHaveLandedIsNotRepeatedAfterARestart` 94 s. On Linux the slowest is
  `TestASendDoesNotWaitForASyncsSlowFetch`, 43 s, which waits on purpose.

A change meant to make the tests cheaper can cite, from the same script on its own run: the git
started and their time in all per job (the count times the mean, added over the subcommands), the
test time per job, and a named test's time. On a laptop,
`MURMUR_GIT_STATS=<file> go test -v -count=1 -run '<tests>' ./internal/daemon` writes the same
count of git to the file and prints the same per-test times, for a few tests at a time.

Some tests stood up two or three devices and a remote to check a decision that reads only the
daemon's state: what a Stop hook gives, whether a person's message explains them. Those can build
a `Daemon` by hand in an `_internal_test.go` file and call the handler or the `...Locked` method
straight, as `threadsDaemon`, `tagsDaemon` and `stopRig` do, with no git and in milliseconds. On
2026-11-23 (#1010) four slow ones went that way: 268 s of Windows test time, and
about 1,770 git and 59 s on a laptop, became four tests of under 0.1 s with no git. Each says in its
comment what it keeps, why that needs no git, and which test still runs the path end to end. A
test whose point is the bus (ref writes, the registry, presence across devices, a restart against
the remote, a git that is slow or refused) stays on real git.

## Testing `mur join` by hand

A throwaway `HOME` isn't enough. Join also follows the variables that move an agent's settings,
and an agent's shell in T3 has several: on 2026-11-21 a test join in a scratch home followed them
and pointed the real `~/.claude_personal` and T3's Antigravity configurations at a test build that
was deleted afterwards, so every new session failed to join (#995). Alongside `HOME`, set
`USERPROFILE` (on Windows it is the home) and `APPDATA`, and unset `CLAUDE_CONFIG_DIR`,
`CODEX_HOME`, `GEMINI_HOME`, `GROK_HOME`, `KIRO_HOME`, `PI_CODING_AGENT_DIR`, `XDG_CONFIG_HOME` and every
`T3CODE_*` variable (`env | grep ^T3CODE_` lists them).

Since #995, join and uninstall in a home inside the temporary folder leave alone every agent
configuration outside that folder, and name the variable that pointed there
([joining.md](design/joining.md#what-mur-join-writes)). That is a backstop, not the setup: a scratch
home outside the temporary folder isn't recognized. The Go tests are isolated already: `cmd/mur`'s
`box.env` and `internal/join`'s `TestMain` clear these variables, and the stand-in `claude` refuses
to write outside the temporary folder.

## On every pull request

`.github/workflows/test.yml` runs those checks on every pull request, on a push to `main`, on an rc
tag (`v*-rc.*`), and by hand. Its jobs run at once, on GitHub's own runners, which are free for a
public repo and start fresh for every job. A pull request from a fork runs on them the same way.

**macOS runs at an rc tag and by hand, not on a pull request or a push to `main`** (Tyler's rule,
2026-11-23, #1179). GitHub's hosted macOS runners were scarce: through 2026-11-23 macOS jobs waited
in the queue for long stretches, were cancelled as "not acquired by runner", and held up merges and
nightlies behind Linux and Windows jobs that had already passed, until main's runs were cancelled by
hand eight at a time. So `test-macos` and the `test-macos-daemon (N)` shards run only when `changes`
says `macos: true`, on an rc tag's push or a run by hand, both always `full`; the nightly takes a
commit whose Linux and Windows jobs passed. The cost: a change that breaks only on macOS merges
green and is found at the next rc, perhaps days later, beside everything else merged since. A
stable tag waits for a green macOS run on its rc ([releasing.md](releasing.md)). To run macOS on a
branch before merging it: `gh workflow run test.yml --ref <branch>`.

**A pull request runs only the jobs its files need** (2026-11-12, #512). The `changes` job gives
the names of the files changed to `scripts/ci-level.sh`, which answers with one word:

| Level | The change touches only | What runs |
|---|---|---|
| `none` | Markdown and images at the top of the repo, in `docs/` and in `.github/`, other than `README.md` and `docs/agents/` | nothing |
| `site` | `site/`, and the above | `test-linux`, the site's tests alone |
| `page` | `internal/web/static`, and the above | `test-linux`, whole |
| `go` | `internal/`, `cmd/`, `README.md` and `docs/agents/`, and the above | every job but the daemon shards and macOS's |
| `full` | anything else; Go that differs per OS: a file named for one (`_windows.go`, `_darwin_arm64.go`, `_unix.go`, `_other.go`, ...), one with a `//go:build` line before or after the change, a change that adds or removes a `runtime.GOOS` line; `go.mod`, `go.sum`, the workflows and `scripts/` | every job but macOS's |

The page's files are built into the binary, so they need the Go tests, but CSS and JS don't behave
differently per OS.

**A pull request runs `internal/daemon` on Windows only at `full`** (2026-11-19, #879; on macOS
it ran only at `full` too, until macOS left pull requests altogether, above). Those six shards were about 85% of a full run's runner time (about 90 of 106 minutes on run
44223188463), and two of its three macOS jobs. The account gets five macOS jobs at a time, so two
full runs at once queued, and on 2026-11-17 three runs failed with a macOS job that waited up to
40 minutes for a runner and was cancelled. Linux runs the package on every Go change, and Windows
runs every other package, so what a pull request alone misses on Windows is a daemon test that fails
there only, from code that isn't marked as per-OS: an edit inside a `runtime.GOOS` branch that was
already there, say. On macOS a pull request misses every failure, per-OS files included. Counting every file that mentions `runtime.GOOS` would have caught that, but
those include the daemon's most-changed files (`sync.go`, `files.go`, `daemon_test.go`), so most
pull requests would be `full` again. `changes` reads what a change did against the merge's first
parent (`CI_LEVEL_BASE`), so a deleted or renamed file is read as it was. The push to `main` runs every job but macOS's, and the nightly takes only a commit whose push
run passed, so such a failure on Windows still shows before a release, on main rather than on its
pull request; on macOS it shows at the next rc (above). To run every job on a branch before merging
it: `gh workflow run test.yml --ref <branch>`.

Two other ways to cut the Windows time were measured and dropped on 2026-11-19, in runs
44470284567 and 44470384061 on a throwaway branch: turning off Defender's real-time scanning (the
shards start about 27,500 git processes each, and a `git config` takes 140 ms there) and raising
`-parallel`. Neither showed a gain, since the same job's time varied from 37 to 64 minutes between
runners. Run whole on one runner, the package took 37 minutes on Windows and 22 on macOS. A file no rule names is `full`, and so is a list of files GitHub couldn't give,
so a mistake runs too much. `scripts/ci-level-test.sh` checks the rules, and `changes` runs it
before it uses their answer. A push to `main`, an rc tag and a run by hand are always `full`. On a pull
request that isn't `full`, the Windows daemon shards show as one skipped check with its name
unfilled (`test-${{ matrix.os }}-daemon ...`): GitHub doesn't expand a matrix it skips. Every pull
request shows `test-macos` and `test-macos-daemon (${{ matrix.shard }})` skipped the same way.

**A test that fails is run once more** (`scripts/go-test-retry.sh`). Daemon tests give up a wait on
a slow runner, a different one each run: three of the four full runs after #504 lost a Windows
shard that way (#513), each a rerun by hand. The job passes if the retry does, and says what it
retried in an annotation and in the run's summary, which is where a test that needs the retry often
shows. A build failure, a data race, a test binary that panics, exits early or reaches `-timeout`,
a failed example, and more than five failures at once are not retried. What the retry does hide is
a test that fails only beside another: alone, it passes.

One run's page shows only that run's retries. `scripts/retried-tests.sh [runs]` reads the
annotations of main's last runs (20 unless told otherwise) through `gh` and counts them by test, so
one that keeps needing the retry stands out (#1011): on 2026-11-23 it showed
`TestFilesASendRefuses` retried in 6 of main's last 12 runs. Since that day an annotation names
the test's package, the runner's OS and the daemon shard as well as the test. A retry still
never fails a run.

`internal/daemon` is most of the Windows time: on 2026-11-12 it took 2,975 s of a 50-minute budget
(the rest of the suite together about 8 minutes), and runs from #489, #500 and #501 died at the
`-timeout 50m` with no test hung (#496). So `test-windows` runs every other package, and
the `test-daemon` job, whose checks are named `test-windows-daemon (N)`, runs the daemon's tests on four runners at once, each taking the tests whose
position in the sorted `go test -list` output is its shard number modulo 4. A test added later falls
into some shard by itself. macOS does the same on two runners, `test-macos-daemon (N)`, since
2026-11-12: the package was 1,154 s of `test-macos`'s 20 minutes, which made it the longest job.
Its shards are a job of their own, with test-daemon's steps, since 2026-11-23: a matrix can't leave
out some of its entries by a condition, and a skipped macOS shard must not ask for a runner.
Linux runs the package whole, in about 6 minutes.

**A test that hangs fails with its stack** (#727, 2026-11-16). It should end in Go's `-timeout`
panic, which names it and prints every goroutine, not in the job's cancellation, which keeps no log:
the shards' 50-minute `-timeout` and a retry outlasted their hour, and a hang on #723 was a job
cancelled after 65 minutes with nothing to show which test. A hang is never retried, so a job's
limit must fit its longest run that doesn't hang, a retry of what failed (which may hang), and
setup; `-timeout` counts the test binary alone. The daemon shards run with 45 minutes, the retry
with its own 10 (`RETRY_TIMEOUT`, as it runs a few tests alone), within 75: Windows shard 1's binary
takes 1,154 s, the others 500 to 820. The other jobs' tests take 3 to 8 minutes, and run with 25
minutes in 60, and macOS's release-built step, which shares its job, with 20.

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
