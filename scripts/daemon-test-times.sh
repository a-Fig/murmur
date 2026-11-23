#!/usr/bin/env bash
# Where the daemon package's test time goes, in one run of test.yml:
#
#   scripts/daemon-test-times.sh [run] [slowest]
#
# run is a run's ID, or nothing for main's last completed push run; slowest is how many of the
# slowest tests to list for each job and over the run (15 unless told otherwise). It reads, through
# `gh`, only what GitHub keeps for a run: the jobs' step times, their logs and their annotations.
# docs/testing.md has a dated baseline made with it, to set a later run beside (#1008).
#
# For each job that runs internal/daemon (test-linux, and the macOS and Windows daemon shards on a
# run at the `full` level) it prints:
#   - how long the job and its go test step took;
#   - go's own time for the package, and whether it passed;
#   - how many tests ran at once on how many processors, and how many git the tests started, by
#     subcommand, as the tests' own count has it (MURMUR_GIT_STATS, #513);
#   - the tests that were retried;
#   - the slowest tests. Those come from go test -v, which the jobs have run with since
#     2026-11-22; a run from before then lists none.
# Then the slowest tests over every job. A test's time is its own, without the time it waited for
# its turn to run in parallel. A test that was retried is listed by its first run.
#
# test-linux's figures don't sit beside the shards': it runs every package at once (./...), so
# other packages' tests share its processors with the daemon's, and it runs the whole package where
# a shard runs a part.
set -euo pipefail
export LC_ALL=C # a point for the decimal, whatever the machine's locale
run=${1:-}
top=${2:-15}
repo=${GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}
if [ -z "$run" ]; then
  run=$(gh api "repos/$repo/actions/workflows/test.yml/runs?branch=main&event=push&status=completed&per_page=1" --jq '.workflow_runs[0].id')
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

gh api "repos/$repo/actions/runs/$run" --jq '"run \(.id), \(.head_sha[:8]) on \(.head_branch), \(.event), \(.created_at), \(.conclusion // .status)"'

# One line per job that tests the daemon: its ID, its seconds, its go test step's seconds, its name.
gh api "repos/$repo/actions/runs/$run/jobs?per_page=100" --jq '
  def secs(a; b): if a and b then ((b | fromdate) - (a | fromdate)) else 0 end;
  .jobs[]
  | select(.name == "test-linux" or (.name | test("-daemon ")))
  | select(.conclusion != "skipped")
  | [.id, secs(.started_at; .completed_at),
     ([.steps[] | select(.name | test("go test|go-test-retry")) | secs(.started_at; .completed_at)] | max // 0),
     .name] | @tsv' >"$work/jobs"

if [ ! -s "$work/jobs" ]; then
  echo "No job of this run tested the daemon package."
  exit 0
fi

while IFS=$'\t' read -r job secs step name; do
  echo
  echo "== $name: the job took ${secs}s, its go test step ${step}s"
  # A log line is the job, the step, and a time stamp before the text.
  if ! gh run view --repo "$repo" --job "$job" --log >"$work/raw" 2>/dev/null; then
    echo "   (its log couldn't be read)"
    continue
  fi
  cut -f3- "$work/raw" | sed -E 's/^[0-9T:.-]+Z //' | tr -d '\r' >"$work/log"

  # The daemon package's own lines: go's verdict for it, and the tests' count of git.
  awk '
    /^(ok|FAIL)[ \t]+[^ \t]+\/internal\/daemon[ \t]/ { print "   go: " $0 }
    /^git in a test run of / { print "   " $0; n = 30; next }
    n > 0 && /^[ \t]+[a-z-]+[ \t]/ { print "   " $0; n--; next }
    { n = 0 }' "$work/log"

  gh api "repos/$repo/check-runs/$job/annotations?per_page=100" \
    --jq '.[] | select(.title == "Test retried") | "   retried: " + .message' || true

  # Top-level tests of the daemon package, with their own time: those before its verdict line. Its
  # first verdict only: a second is the retry of the tests that failed, which would count twice.
  awk -v name="$name" '
    /^--- (PASS|FAIL): Test/ { t = $4; gsub(/[()s]/, "", t); held[$3] = t; next }
    /^(ok|FAIL)[ \t]+[^ \t]+/ {
      if ($2 ~ /\/internal\/daemon$/ && !seen++) for (k in held) printf "%s\t%s\t%s\n", held[k], k, name
      delete held
    }' "$work/log" | sort -rn >"$work/tests"
  if [ -s "$work/tests" ]; then
    awk -F'\t' -v n="$top" '
      { s += $1; if (NR <= n) line[NR] = sprintf("   %8.2fs  %s", $1, $2) }
      END {
        printf "   %d tests, %.0fs of test time; the slowest:\n", NR, s
        for (i = 1; i <= n && i <= NR; i++) print line[i]
      }' "$work/tests"
    cat "$work/tests" >>"$work/all"
  else
    echo "   (no per-test times in its log)"
  fi
done <"$work/jobs"

if [ -s "$work/all" ]; then
  echo
  echo "== The slowest tests over the run"
  # awk reads to the end: head would close the pipe on sort, which fails the script under pipefail.
  sort -rn "$work/all" | awk -F'\t' -v n="$top" 'NR <= n { printf "   %8.2fs  %s  (%s)\n", $1, $2, $3 }'
fi
