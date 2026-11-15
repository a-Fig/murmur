#!/bin/sh
# How much of .github/workflows/test.yml a change needs, from the files it touches, one per line on
# stdin. Prints one word:
#
#   none  docs, templates and images: no check reads them
#   site  site/ only: the site's own tests, which take seconds
#   page  the page's static files (internal/web/static): they are built into the binary, so the Go
#         tests run, on Linux alone, since CSS and JS don't behave differently per OS
#   full  anything else: every job
#
# The widest need of any file wins, a file no rule names is `full`, and so is an empty list, so a
# mistake here runs too much rather than too little. scripts/ci-level-test.sh checks the rules.
set -eu

rank() {
  case "$1" in none) echo 0 ;; site) echo 1 ;; page) echo 2 ;; *) echo 3 ;; esac
}

# none for Markdown and images, full for anything else.
quiet() {
  case "$1" in
    *.md | *.png | *.jpg | *.jpeg | *.gif | *.webp | *.svg) echo none ;;
    *) echo full ;;
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
    # Tested Markdown: the skills, the README a setup test reads, and the agent pages that test
    # reads too (internal/setup/setup_test.go).
    internal/* | README.md | docs/agents/* | .github/workflows/*) l=full ;;
    # Docs and images, at the top of the repo, in docs/ and in .github/. The same kinds of file
    # anywhere else (a test's data under cmd/, say) are `full`: `*` in a pattern crosses a `/`.
    docs/* | .github/*) l=$(quiet "$f") ;;
    */*) l=full ;;
    *) l=$(quiet "$f") ;;
  esac
  if [ "$(rank "$l")" -gt "$(rank "$level")" ]; then level=$l; fi
done

if [ "$seen" = 0 ]; then level=full; fi
echo "$level"
