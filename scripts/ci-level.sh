#!/bin/sh
# How much of .github/workflows/test.yml a pull request needs, from the files it touches, one per
# line on stdin, named from the top of the repo. Prints one word:
#
#   none  docs, templates and images: no check reads them
#   site  site/ only: the site's own tests, which take seconds
#   page  the page's static files (internal/web/static): they are built into the binary, so the Go
#         tests run, on Linux alone, since CSS and JS don't behave differently per OS
#   go    Go and the files its tests read: every package on Linux, and on macOS and Windows every
#         package but internal/daemon, whose tests there are most of CI's time; a push to main runs
#         them (#879)
#   full  anything else, and code that differs per OS (a file named for one, or with a build
#         constraint), go.mod, the workflows and these scripts: every job
#
# The widest need of any file wins, a file no rule names is `full`, and so is an empty list, so a
# mistake here runs too much rather than too little. scripts/ci-level-test.sh checks the rules.
set -eu
cd "$(dirname "$0")/.."

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

# full for Go that is built differently per OS, which the daemon's tests on that OS exercise: a
# file named for an OS, or one with a //go:build line (a deleted file has neither to read, and its
# name is the only clue left).
perOS() {
  case "$1" in
    *_windows.go | *_windows_test.go | *_darwin.go | *_darwin_test.go | *_linux.go | *_linux_test.go | \
      *_unix.go | *_unix_test.go | *_other.go | *_other_test.go) echo full ;;
    *.go) if [ -f "$1" ] && grep -q '^//go:build' "$1"; then echo full; else echo go; fi ;;
    *) echo go ;;
  esac
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
    # Tested Markdown: the skills, the README a setup test reads, and the agent pages that test
    # reads too (internal/setup/setup_test.go).
    internal/* | cmd/*) l=$(perOS "$f") ;;
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
