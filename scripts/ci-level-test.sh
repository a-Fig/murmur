#!/bin/sh
# Checks scripts/ci-level.sh: each case is the level expected, then the files changed. They run in
# a scratch repo whose one commit is the base (CI_LEVEL_BASE), with the change in its working tree.
set -eu
unset CDPATH
script="$(cd "$(dirname "$0")" && pwd)/ci-level.sh"
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
cd "$repo"
g() { git -c user.name=t -c user.email=t@t -c commit.gpgsign=false "$@"; }
g init -q
g config core.autocrlf false
mkdir -p internal/a internal/b internal/c internal/d internal/e internal/f
printf 'package a\n' >internal/a/plain.go
printf '//go:build windows\n\npackage b\n' >internal/b/tagged.go
printf '//go:build windows\n\npackage c\n' >internal/c/untag.go
printf 'package d\n\nvar x = 1\n' >internal/d/goos.go
printf 'package e\n\nfunc f() bool { return runtime.GOOS == "windows" }\n' >internal/e/inbranch.go
printf 'package e\n' >internal/e/stays.go
printf '// +build windows\n\npackage f\n' >internal/f/legacy.go
printf '  //go:build windows\n\npackage f\n\nvar a = 1\n' >internal/f/indented.go
g add -A
g commit -qm base
export CI_LEVEL_BASE=HEAD
# The change: an untagged edit, a deletion of a tagged file, a tag taken out, a runtime.GOOS
# branch added, an edit beside one that was already there, a tagged file added, a file with an
# old-style constraint deleted, and an edit to one whose constraint is indented.
printf 'package a\n\nvar y = 2\n' >internal/a/plain.go
rm internal/b/tagged.go
printf 'package c\n' >internal/c/untag.go
printf 'package d\n\nvar x = runtime.GOOS == "windows"\n' >internal/d/goos.go
printf 'package e\n\nfunc f() bool { return runtime.GOOS == "windows" }\n\nvar z = 3\n' >internal/e/inbranch.go
printf '//go:build !windows\n\npackage e\n' >internal/e/added.go
rm internal/f/legacy.go
printf '  //go:build windows\n\npackage f\n\nvar a = 2\n' >internal/f/indented.go

fails=0
want() {
  expect=$1
  shift
  got=$(printf '%s\n' "$@" | sh "$script")
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
want full internal/gitx/files_windows_amd64.go
want full internal/daemon/files_darwin_arm64_test.go
want full internal/gone/deleted_linux.go
want go internal/gone/deleted.go
want go internal/a/plain.go internal/e/stays.go
want full internal/b/tagged.go
want full internal/c/untag.go
want full internal/d/goos.go
want go internal/e/inbranch.go
want full internal/e/added.go
want full internal/f/legacy.go
want full internal/f/indented.go
# A rename comes as both names: the old one, deleted here, is read as it was.
want full internal/a/plain.go internal/b/tagged.go
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

got=$(sh "$script" </dev/null)
if [ "$got" != full ]; then
  echo "FAIL: want full for no input, got $got"
  fails=$((fails + 1))
fi
# Without a base it can read, what a Go file's change did can't be read, so it is full.
for b in "" nosuchcommit; do
  got=$(echo internal/a/plain.go | CI_LEVEL_BASE=$b sh "$script")
  if [ "$got" != full ]; then
    echo "FAIL: want full with CI_LEVEL_BASE='$b', got $got"
    fails=$((fails + 1))
  fi
done

if [ "$fails" != 0 ]; then exit 1; fi
echo ok
