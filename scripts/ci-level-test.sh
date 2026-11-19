#!/bin/sh
# Checks scripts/ci-level.sh: each case is the level expected, then the files changed.
set -eu
cd "$(dirname "$0")"

fails=0
want() {
  expect=$1
  shift
  got=$(printf '%s\n' "$@" | sh ./ci-level.sh)
  if [ "$got" != "$expect" ]; then
    echo "FAIL: want $expect, got $got, for: $*"
    fails=$((fails + 1))
  fi
}

want none docs/VISION.md
want none CLAUDE.md AGENTS.md docs/assets/brand/logo.png
want none .github/pull_request_template.md
want site site/src/worker.js
want site site/README.md docs/names.md
want page internal/web/static/assets/style.css
want page internal/web/static/assets/app.js site/index.html docs/brand.md
want go internal/web/web.go
want go internal/web/static/assets/style.css internal/web/web.go
want go internal/daemon/daemon.go cmd/mur/main.go
want go README.md
want go docs/agents/use.md
want go internal/skills/murmur/SKILL.md
want go cmd/mur/testdata/page.md
want go docs/agents/pictures/turn.png
want full internal/gitx/proc_windows.go
want full internal/daemon/sendpaths_unix_test.go
want full internal/proc/ancestors_darwin.go internal/web/web.go
want full internal/clientcli/proc_other.go
want full internal/desktop/off.go
want go internal/gone/deleted.go
want full internal/gone/deleted_linux.go
want full go.sum
want full .github/workflows/test.yml
want full go.mod
want full install.sh
want full scripts/ci-level.sh
want full LICENSE
want full scripts/notes.md
want full docs/build.sh
want full .github/dependabot.yml
want none .github/ISSUE_TEMPLATE/bug.md docs/assets/brand/mark.svg
want go site/src/worker.js cmd/mur/main.go
want full site/src/worker.js go.mod
want full ""

got=$(sh ./ci-level.sh </dev/null)
if [ "$got" != full ]; then
  echo "FAIL: want full for no input, got $got"
  fails=$((fails + 1))
fi

if [ "$fails" != 0 ]; then exit 1; fi
echo ok
