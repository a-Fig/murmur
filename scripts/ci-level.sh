#!/bin/sh
# How much of .github/workflows/test.yml a pull request needs, from the files it touches, one per
# line on stdin, named from the top of the repo (run it there). Prints one word:
#
#   none  docs, templates and images: no check reads them
#   site  site/ only: the site's own tests, which take seconds
#   page  the page's static files (internal/web/static): they are built into the binary, so the Go
#         tests run, on Linux alone, since CSS and JS don't behave differently per OS
#   go    Go and the files its tests read: every package on Linux, and on Windows every package
#         but internal/daemon, whose tests there are most of CI's time; a push to main runs them
#         (#879)
#   full  anything else, Go that is built or behaves differently per OS (see perOS), go.mod,
#         go.sum, the workflows and these scripts: every job but macOS's
#
# No level runs macOS: a pull request never does, and an rc tag or a run by hand runs it, whatever
# the files (#1179).
#
# CI_LEVEL_BASE names the commit the change is made against (the merge commit's first parent, in
# the workflow). Without it, or when git can't read it, every Go file is `full`, since what it
# changed can't be read.
#
# The widest need of any file wins, a file no rule names is `full`, and so is an empty list, so a
# mistake here runs too much rather than too little. scripts/ci-level-test.sh checks the rules.
set -eu

rank() {
  case "$1" in none) echo 0 ;; site) echo 1 ;; page) echo 2 ;; go) echo 3 ;; *) echo 4 ;; esac
}

# none for Markdown and images, full for anything else.
quiet() {
  case "$1" in
    *.md | *.png | *.jpg | *.jpeg | *.gif | *.webp | *.svg) echo none ;;
    *) echo full ;;
  esac
}

base=${CI_LEVEL_BASE:-}
if [ -n "$base" ] && ! git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1; then base=; fi

# A build constraint, new style or old, indented or not.
tag='^[[:space:]]*//[[:space:]]*(go:build|\+build)'

# full for Go the daemon's tests run differently per OS: a file named for an OS (with or without an
# architecture), one with a build constraint before or after the change (a deleted or renamed file is
# read as it was), and a change that adds or removes a line naming runtime.GOOS. An edit inside an
# existing runtime.GOOS branch is `go`: whole files that test runtime.GOOS include the daemon's
# most-changed ones (sync.go, files.go), and main's run catches the rest on Windows, and the next
# rc's on macOS (docs/testing.md).
perOS() {
  case "$1" in
    *_windows.go | *_windows_*.go | *_darwin.go | *_darwin_*.go | *_linux.go | *_linux_*.go | \
      *_unix.go | *_unix_*.go | *_other.go | *_other_*.go) echo full; return ;;
    *.go) ;;
    *) echo go; return ;;
  esac
  if [ -z "$base" ]; then echo full; return; fi
  if [ -f "$1" ] && grep -qE "$tag" "$1"; then echo full; return; fi
  if git show "$base:$1" 2>/dev/null | grep -qE "$tag"; then echo full; return; fi
  if git diff "$base" -- "$1" | grep -qE '^[-+].*(go:build|\+build|runtime\.GOOS)'; then echo full; return; fi
  echo go
}

level=none
seen=0
while IFS= read -r f || [ -n "$f" ]; do
  [ -n "$f" ] || continue
  seen=1
  case "$f" in
    site/*) l=site ;;
    internal/web/static/*) l=page ;;
    go.mod | go.sum | .github/workflows/* | scripts/*) l=full ;;
    internal/* | cmd/*) l=$(perOS "$f") ;;
    # The README a setup test reads, and the agent pages that test reads too
    # (internal/setup/setup_test.go), which mur also builds in for `mur docs` (docs/agents/pages.go,
    # #831). The skills are under internal/.
    README.md | docs/agents/*) l=go ;;
    # Docs and images, at the top of the repo, in docs/ and in .github/. The same kinds of file
    # anywhere else (a test's data under cmd/, say) are not quiet: `*` in a pattern crosses a `/`.
    docs/* | .github/*) l=$(quiet "$f") ;;
    */*) l=full ;;
    *) l=$(quiet "$f") ;;
  esac
  if [ "$(rank "$l")" -gt "$(rank "$level")" ]; then level=$l; fi
done

if [ "$seen" = 0 ]; then level=full; fi
echo "$level"
