#!/usr/bin/env bash
# Checks scripts/go-test-retry.sh against a stand-in `go`. Each scenario is what the first `go test`
# prints and exits with, in go's own wording; the stand-in's second run always passes.
set -u
repo=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d)
cat >"$T/go" <<'EOF'
#!/usr/bin/env bash
echo x >>"$STUB_LOG"
if [ "$(grep -c . "$STUB_LOG")" -gt 1 ]; then echo "ok  	pkg	0.1s"; exit 0; fi
cat "$STUB_OUT"
exit "$STUB_EXIT"
EOF
chmod +x "$T/go"

fails=0
scenario() { # name, want exit, want calls, first exit; output on stdin
  cat >"$T/out"
  : >"$T/log"
  # No summary file: in CI the job's own would get these scenarios' lines.
  PATH="$T:$PATH" STUB_LOG="$T/log" STUB_OUT="$T/out" STUB_EXIT=$4 GITHUB_STEP_SUMMARY= bash "$repo/scripts/go-test-retry.sh" -race -run '^(TestA)$' ./... >"$T/res" 2>&1
  got=$?
  calls=$(grep -c . "$T/log")
  if [ "$got" = "$2" ] && [ "$calls" = "$3" ]; then echo "ok   $1"; else
    echo "FAIL $1: exit $got (want $2), calls $calls (want $3)"; tail -3 "$T/res"; fails=$((fails + 1))
  fi
}

scenario "passes first time" 0 1 0 <<'EOF'
ok  	github.com/x/a	0.2s
EOF
scenario "a flake is retried and passes" 0 2 1 <<'EOF'
--- FAIL: TestA (1.00s)
    a_test.go:1: timed out waiting for a name
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
ok  	github.com/x/b	0.2s
FAIL
EOF
# go test -v prints every test, passing ones too, and a package's PASS before its verdict (#1008).
scenario "a flake under -v is retried and passes" 0 2 1 <<'EOF'
=== RUN   TestA
=== PAUSE TestA
=== RUN   TestB
--- PASS: TestB (0.10s)
=== CONT  TestA
    a_test.go:1: timed out waiting for a name
--- FAIL: TestA (1.00s)
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
=== RUN   TestC
--- PASS: TestC (0.00s)
PASS
ok  	github.com/x/b	0.2s
FAIL
EOF
scenario "a binary that exits early under -v is not retried" 1 1 1 <<'EOF'
=== RUN   TestA
--- FAIL: TestA (1.00s)
    a_test.go:1: boom
=== RUN   TestB
exit status 3
FAIL	github.com/x/a	1.2s
FAIL
EOF
scenario "a data race is not retried" 1 1 1 <<'EOF'
==================
WARNING: DATA RACE
Read at 0x00c0 by goroutine 8:
==================
--- FAIL: TestA (0.01s)
    testing.go:1490: race detected during execution of test
FAIL
exit status 1
FAIL	github.com/x/a	0.2s
FAIL
EOF
scenario "a binary that exits early is not retried" 1 1 1 <<'EOF'
--- FAIL: TestA (1.00s)
    a_test.go:1: boom
exit status 3
FAIL	github.com/x/a	1.2s
FAIL
EOF
scenario "a build failure beside a flake is not retried" 1 1 1 <<'EOF'
--- FAIL: TestA (1.00s)
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
FAIL	github.com/x/c [build failed]
FAIL
EOF
scenario "a failed example is not retried" 1 1 1 <<'EOF'
--- FAIL: TestA (1.00s)
--- FAIL: ExampleB (0.00s)
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
FAIL
EOF
scenario "a timeout is not retried" 2 1 2 <<'EOF'
--- FAIL: TestA (1.00s)
panic: test timed out after 50m0s
FAIL	github.com/x/a	3000.1s
FAIL
EOF
scenario "six failures are not retried" 1 1 1 <<'EOF'
--- FAIL: Test1 (1s)
--- FAIL: Test2 (1s)
--- FAIL: Test3 (1s)
--- FAIL: Test4 (1s)
--- FAIL: Test5 (1s)
--- FAIL: Test6 (1s)
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
FAIL
EOF

# What a retry leaves to be seen: an annotation and a summary line for each test, with its package,
# the runner's OS and the shard (#1011).
cat >"$T/out" <<'EOF'
--- FAIL: TestA (1.00s)
    a_test.go:1: timed out waiting for a name
FAIL
exit status 1
FAIL	github.com/x/a	1.2s
ok  	github.com/x/b	0.2s
--- FAIL: TestC (1.00s)
FAIL
exit status 1
FAIL	github.com/x/c	1.2s
FAIL
EOF
: >"$T/log"
: >"$T/summary"
PATH="$T:$PATH" STUB_LOG="$T/log" STUB_OUT="$T/out" STUB_EXIT=1 RUNNER_OS=Windows SHARD=2 GITHUB_JOB=test-daemon GITHUB_STEP_SUMMARY="$T/summary" \
  bash "$repo/scripts/go-test-retry.sh" -race ./... >"$T/res" 2>&1
got=$?
want_notes='::warning title=Test retried::github.com/x/a TestA failed, then passed on a second try (Windows, shard 2)
::warning title=Test retried::github.com/x/c TestC failed, then passed on a second try (Windows, shard 2)'
want_summary='### Tests retried (test-daemon, Windows, shard 2)

- `TestA` in `github.com/x/a`: passed on a second try
- `TestC` in `github.com/x/c`: passed on a second try'
if [ "$got" = 0 ] && [ "$(grep '^::warning' "$T/res")" = "$want_notes" ] && [ "$(cat "$T/summary")" = "$want_summary" ]; then
  echo "ok   a retried test is named with its package, OS and shard"
else
  echo "FAIL a retried test is named with its package, OS and shard:"
  grep '^::warning' "$T/res"
  cat "$T/summary"
  fails=$((fails + 1))
fi
rm -rf "$T"
exit "$fails"
