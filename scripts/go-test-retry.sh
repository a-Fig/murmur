#!/usr/bin/env bash
# go test, and once more for the tests that failed: scripts/go-test-retry.sh <go test arguments>
#
# Some daemon tests give up a wait on a slow CI runner, a different one each run (#513), and a run
# lost to one costs a rerun by hand. So a test that fails is run again, alone, and the job passes if
# it passes then. What was retried is printed, annotated on the run and written to its summary, so a
# test that needs the retry often is seen.
#
# Only a test that failed by name is retried, and only when its binary ran to its end. A build that
# fails, a data race, a test binary that hangs, panics or exits early, a failed example or fuzz
# target, and more than MAX_RETRIED failures at once are real, and fail the job as they did before.
#
# A test that passes alone on the retry may still have failed because of another test running
# beside it. The retry hides that too; the names it prints are the only trace.
set -uo pipefail

MAX_RETRIED=5
out=$(mktemp)
trap 'rm -f "$out"' EXIT

go test "$@" 2>&1 | tee "$out"
status=${PIPESTATUS[0]}
if [ "$status" = 0 ]; then exit 0; fi

# The race detector fails the test it fires in by name, and a race rarely fires twice running.
if grep -q '^WARNING: DATA RACE' "$out"; then
  echo "go-test-retry: the race detector found a data race; not retrying"
  exit "$status"
fi
# An example or a fuzz target that failed is not run by the retry, which names tests.
if grep -E '^--- FAIL: ' "$out" | grep -qvE '^--- FAIL: Test'; then
  echo "go-test-retry: something other than a test failed; not retrying"
  exit "$status"
fi

# A top-level test's line starts the line; a subtest's is indented, and its parent fails with it.
failed=$(grep -E '^--- FAIL: Test' "$out" | awk '{print $3}' | sort -u)
count=$(printf '%s' "$failed" | grep -c . || true)
if [ "$count" = 0 ]; then
  echo "go-test-retry: go test failed with no failed test named (a build failure, a panic or a timeout); not retrying"
  exit "$status"
fi
if [ "$count" -gt "$MAX_RETRIED" ]; then
  echo "go-test-retry: $count tests failed, more than $MAX_RETRIED; not retrying"
  exit "$status"
fi
# A package that failed without naming a test (it didn't build), or whose binary stopped before
# its end (os.Exit, a crash, a kill), would pass a retry that runs only the tests named so far. Each
# package's lines come together, its verdict last; a binary that ran every test prints a bare FAIL,
# and then go prints "exit status 1" and the verdict.
if ! awk '
  /^--- FAIL: Test/ { n++ }
  /^(FAIL|ok)[ \t]+[^ \t]+/ { if ($1 == "FAIL" && (n == 0 || last != "FAIL")) bad = 1; n = 0 }
  !/^exit status [0-9]+$/ { last = $0 }
  END { exit bad }' "$out"; then
  echo "go-test-retry: a package failed without naming a test, or its test binary stopped early; not retrying"
  exit "$status"
fi
# A panic or the -timeout kills the binary mid-run: the tests named before it are not all of it.
if grep -qE '^(panic: |fatal error: )' "$out"; then
  echo "go-test-retry: the test binary died; not retrying"
  exit "$status"
fi

names=$(printf '%s' "$failed" | paste -sd'|' -)
echo
echo "go-test-retry: retrying once: $(printf '%s' "$failed" | paste -sd' ' -)"

# The retry replaces any -run of the first try, and keeps every other argument.
args=()
skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -run | -test.run) skip=1 ;;
    -run=* | -test.run=*) ;;
    *) args+=("$a") ;;
  esac
done

go test -run "^($names)\$" "${args[@]}"
status=$?

verdict="passed on a second try"
if [ "$status" != 0 ]; then verdict="was retried, and the retry of these tests failed"; fi
for t in $failed; do
  echo "::warning title=Test retried::$t failed, then $verdict"
done
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Tests retried (${GITHUB_JOB:-job})"
    echo
    for t in $failed; do echo "- \`$t\`: $verdict"; done
  } >>"$GITHUB_STEP_SUMMARY"
fi
exit "$status"
