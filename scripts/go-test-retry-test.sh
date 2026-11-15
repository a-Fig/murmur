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
  PATH="$T:$PATH" STUB_LOG="$T/log" STUB_OUT="$T/out" STUB_EXIT=$4 bash "$repo/scripts/go-test-retry.sh" -race -run '^(TestA)$' ./... >"$T/res" 2>&1
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
rm -rf "$T"
exit "$fails"
