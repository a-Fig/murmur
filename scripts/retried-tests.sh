#!/usr/bin/env bash
# Which tests needed CI's retry lately: scripts/retried-tests.sh [runs]
#
# go-test-retry.sh lets a test that fails once pass on a second try, and leaves an annotation
# titled "Test retried" on the job. One run's annotations show on its page; this reads them from
# main's last runs of test.yml (20 unless told otherwise, 100 at most) and counts them by test, so
# one that keeps needing the retry stands out (#1011). It only reads, through `gh`, what GitHub
# keeps for a run.
#
# The count is of annotations, by the test's name alone: a test retried in two jobs of one run
# counts twice, and its shard changes as tests are added, so the lines above the count say where
# each was. A job that was run again shows only its last attempt. An annotation from before
# 2026-11-23 names the test alone; later ones add its package, the runner's OS and the shard.
set -euo pipefail
runs=${1:-20}
repo=${GH_REPO:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}
seen=$(mktemp)
trap 'rm -f "$seen"' EXIT

gh api "repos/$repo/actions/workflows/test.yml/runs?branch=main&event=push&status=completed&per_page=$runs" \
  --jq '.workflow_runs[] | "\(.id) \(.head_sha[:8]) \(.created_at[:10])"' |
  while read -r id sha day; do
    # A job's ID is its check run's, which is what holds the annotations.
    # A read that fails is said and passed over, so the rest is still counted.
    { gh api "repos/$repo/actions/runs/$id/jobs?per_page=100" --jq '.jobs[].id' ||
      echo "retried-tests: couldn't list the jobs of run $id ($sha); it isn't counted" >&2; } |
      while read -r job; do
        { gh api "repos/$repo/check-runs/$job/annotations?per_page=100" \
          --jq '.[] | select(.title == "Test retried") | .message' ||
          echo "retried-tests: couldn't read job $job of run $id ($sha); it isn't counted" >&2; } |
          while read -r message; do
            echo "$day $sha  $message"
            # Counted by the test's name, whichever way the retry ended.
            echo "$message" | sed -E 's/^(.* )?(Test[^ ]+) failed, then.*$/\2/' >>"$seen"
          done
      done
  done

echo
if [ -s "$seen" ]; then
  echo "Retried, by test, over main's last $runs runs:"
  sort "$seen" | uniq -c | sort -rn
else
  echo "No test was retried in main's last $runs runs."
fi
