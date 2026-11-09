#!/bin/sh
# Put images in a pull request body: `gh` can't upload them, but this repo is public, so files
# pushed to the `pr-media` branch can be embedded by URL. See docs/pr-media.md.
#
#   scripts/pr-media.sh <pr> <image>...          add files, print Markdown to paste
#   scripts/pr-media.sh --remove <pr> [<name>...]  remove those files (or all of the PR's)
#
# Plumbing only (hash-object, a temporary index, write-tree, commit-tree, push <sha>:<ref>): the
# working tree, the real index and HEAD are never touched, because someone is editing this
# checkout. The URLs are pinned to the commit, so later pushes never change a picture.
set -eu

BRANCH=pr-media
REMOTE=origin
MAX_TRIES=5

die() { echo "pr-media: $*" >&2; exit 1; }
usage() {
  echo "usage: pr-media.sh <pr> <image>...            add files, print Markdown to paste" >&2
  echo "       pr-media.sh --remove <pr> [<name>...]  remove those files, or all of the PR's" >&2
  exit 2
}

mode=add
if [ "${1:-}" = "--remove" ]; then mode=remove; shift; fi
[ $# -ge 1 ] || usage
pr=$1; shift
case $pr in 0*|*[!0-9]*) die "the PR number is digits, got '$pr'" ;; esac
[ "$mode" = remove ] || [ $# -ge 1 ] || usage

git rev-parse --git-dir >/dev/null 2>&1 || die "not inside a git repository"

# https://github.com/OWNER/REPO(.git) or git@github.com:OWNER/REPO(.git)
# The configured URL, not `remote get-url`, which applies url.<base>.insteadOf rewrites (a proxy or
# mirror) that would hide the github.com name.
url=$(git config --get "remote.$REMOTE.url") || die "no remote '$REMOTE'"
slug=$(printf '%s' "$url" | sed -E 's#^.*github\.com[:/]##; s#/$##; s#\.git$##')
case $slug in
  */*/*|*/|/*|'') die "can't read OWNER/REPO from $url" ;;
  */*) ;;
  *) die "can't read OWNER/REPO from $url" ;;
esac

# A name goes into a URL and a Markdown image, so keep it plain.
check_name() {
  case $1 in
    ''|.*|*[!A-Za-z0-9._-]*) die "'$1': use letters, digits, '.', '_' and '-' only (no spaces)" ;;
  esac
}

# Blob hashes first, while relative file arguments still resolve; --no-filters so autocrlf and
# attributes can't alter an image.
entries=
names=
if [ "$mode" = add ]; then
  for f in "$@"; do
    [ -f "$f" ] || die "$f: not a file"
    name=${f##*[/\\]}
    check_name "$name"
    ext=$(printf '%s' "${name##*.}" | tr 'A-Z' 'a-z')
    case $ext in png|jpg|jpeg|gif|webp) ;; *) die "$name: only png, jpg, gif and webp render in a PR body" ;; esac
    case "
$names
" in *"
$name
"*) die "$name: two files with that name" ;; esac
    size=$(wc -c < "$f" | tr -d ' ')
    [ "$size" -le 5242880 ] || echo "pr-media: $name is $((size / 1048576)) MB; the branch keeps it forever, so shrink it if you can" >&2
    blob=$(git hash-object -w --no-filters -- "$f")
    entries="$entries$blob $name
"
    names="$names$name
"
  done
else
  for name in "$@"; do check_name "$name"; names="$names$name
"; done
fi

# Pathspecs and index paths are relative to the working directory; work from the top.
# Absolute GIT_DIR/GIT_WORK_TREE too, in case the caller set them relative to this directory.
top=$(git rev-parse --show-toplevel)
gitdir=$(git rev-parse --absolute-git-dir)
export GIT_DIR="$gitdir" GIT_WORK_TREE="$top"
cd "$top"
idx="$gitdir/pr-media.$$.index"
trap 'rm -f "$idx"' EXIT
trap 'exit 1' INT TERM HUP
export GIT_INDEX_FILE="$idx"
# A split index would leave a sharedindex.* file behind when the temporary index is removed.
gi() { git -c core.splitIndex=false "$@"; }

tries=1
while :; do
  tip=$(git ls-remote "$REMOTE" "refs/heads/$BRANCH") || die "can't reach $REMOTE"
  tip=${tip%%[[:space:]]*}
  parent=
  if [ -n "$tip" ]; then
    # By sha, not by name, so a fetch running in another process can't swap FETCH_HEAD under us.
    git fetch --quiet --no-tags --no-write-fetch-head "$REMOTE" "$tip" || die "can't fetch $BRANCH from $REMOTE"
    parent=$(git rev-parse "$tip^{commit}")
  fi

  rm -f "$idx"
  [ -z "$parent" ] || gi read-tree "$parent"
  if [ "$mode" = add ]; then
    while read -r blob name; do
      [ -n "$name" ] || continue
      gi update-index --add --cacheinfo "100644,$blob,$pr/$name"
    done <<EOF
$entries
EOF
    msg="PR #$pr: add $(printf '%s' "$names" | tr '\n' ' ')"
  else
    if [ -n "$names" ]; then
      printf '%s' "$names" | while read -r name; do printf '%s\n' "$pr/$name"; done | tr '\n' '\0'
    else
      gi ls-files -z -- "$pr/"
    fi | gi update-index --force-remove -z --stdin
    msg="PR #$pr: remove ${names:-everything}"
    msg=$(printf '%s' "$msg" | tr '\n' ' ')
  fi
  tree=$(gi write-tree)

  if [ -n "$parent" ] && [ "$tree" = "$(git rev-parse "$parent^{tree}")" ]; then
    [ "$mode" = add ] || { echo "pr-media: nothing to remove for #$pr" >&2; exit 0; }
    echo "pr-media: $BRANCH already has these files; no new commit" >&2
    sha=$parent
    break
  fi
  if [ -n "$parent" ]; then
    sha=$(git commit-tree "$tree" -p "$parent" -m "$msg")
  else
    sha=$(git commit-tree "$tree" -m "$msg")
  fi

  # A plain push is the lease: it is refused unless $sha descends from the branch's tip.
  if out=$(git push --quiet "$REMOTE" "$sha:refs/heads/$BRANCH" 2>&1); then break; fi
  now=$(git ls-remote "$REMOTE" "refs/heads/$BRANCH" | cut -f1)
  if [ "$now" != "$tip" ] && [ "$tries" -lt "$MAX_TRIES" ]; then
    tries=$((tries + 1))
    echo "pr-media: $BRANCH moved while pushing; trying again ($tries/$MAX_TRIES)" >&2
    continue
  fi
  echo "$out" >&2
  die "push to $BRANCH failed"
done

if [ "$mode" = remove ]; then
  echo "pr-media: removed from $BRANCH at $sha" >&2
  exit 0
fi
printf '%s' "$names" | while read -r name; do
  [ -n "$name" ] || continue
  printf '![%s](https://raw.githubusercontent.com/%s/%s/%s/%s)\n' "${name%.*}" "$slug" "$sha" "$pr" "$name"
done
