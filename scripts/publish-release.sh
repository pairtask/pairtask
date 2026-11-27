#!/usr/bin/env bash
# publish-release.sh: publish the files in release/ as one workspace release.
#
# Which release (from GitHub Actions' environment):
#   - a push to main publishes release N as vN, N the workflow run number;
#   - a pushed tag vN.P publishes that hotfix; a bare vN tag is refused,
#     since vN belongs to main's run N;
#   - a pushed legacy tag workspace-vX.Y.Z publishes under that tag.
# Rules:
#   - a published release is never replaced: a rerun of the same run may
#     finish its release only while that release is still a draft;
#   - the triggering tag is never deleted, and an existing tag must point
#     at the commit being released;
#   - "latest" only moves forward: a release older than the current latest
#     (a hotfix of an earlier line, or a main run finishing out of order)
#     is published without becoming latest, and a legacy tag that can't be
#     ordered never becomes latest;
#   - nothing is written until every check passed, and a lookup that fails
#     for any reason but "not found" stops the release.
set -euo pipefail

: "${GITHUB_REPOSITORY:?}" "${GITHUB_REF:?}" "${GITHUB_REF_NAME:?}" "${GITHUB_SHA:?}" "${GITHUB_RUN_NUMBER:?}"
repo="$GITHUB_REPOSITORY"

fail() { echo "::error::$*" >&2; exit 1; }

# lookup runs gh and prints its answer: status 0 with an answer, 1 when
# GitHub says the thing doesn't exist, 2 for any other failure.
lookup() {
  local out err
  err=$(mktemp)
  if out=$(gh "$@" 2>"$err"); then
    rm -f "$err"
    printf '%s\n' "$out"
    return 0
  fi
  if grep -qiE 'not found|HTTP 404' "$err"; then
    rm -f "$err"
    return 1
  fi
  cat "$err" >&2
  rm -f "$err"
  return 2
}

# found runs a lookup: prints the answer and is true when it exists, false
# when GitHub confirms it doesn't, and stops the release otherwise.
found() {
  local out rc
  out=$(lookup "$@") && rc=0 || rc=$?
  case $rc in
    0) printf '%s\n' "$out" ;;
    1) return 1 ;;
    *) echo "::error::GitHub lookup failed: gh $*" >&2; exit 2 ;;
  esac
}

# build_of TAG prints "N P" for vN / vN.P, "N A-1" for the legacy form.
build_of() {
  if [[ "$1" =~ ^v([1-9][0-9]{0,8})(\.([1-9][0-9]{0,3}))?$ ]]; then
    echo "${BASH_REMATCH[1]} ${BASH_REMATCH[3]:-0}"
  elif [[ "$1" =~ ^workspace-v0\.0\.0-build\.([1-9][0-9]{0,8})\.([1-9][0-9]{0,3})$ ]]; then
    echo "${BASH_REMATCH[1]} $((BASH_REMATCH[2] - 1))"
  fi
}

release_tag() {
  if [[ "$GITHUB_REF" == refs/heads/main ]]; then
    echo "v${GITHUB_RUN_NUMBER}"
  elif [[ "$GITHUB_REF" == refs/tags/* ]]; then
    case "$GITHUB_REF_NAME" in
      workspace-v*) [[ "$GITHUB_REF_NAME" =~ ^workspace-v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || fail "bad tag $GITHUB_REF_NAME" ;;
      *) [[ "$GITHUB_REF_NAME" =~ ^v[1-9][0-9]{0,8}\.[1-9][0-9]{0,3}$ ]] || fail "only hotfix tags vN.P may be pushed; vN is published by main's run N" ;;
    esac
    echo "$GITHUB_REF_NAME"
  else
    fail "nothing to publish from $GITHUB_REF"
  fi
}

# existing_draft says whether the release is an unpublished draft to redo
# (yes), absent (no), and stops at a published one.
existing_draft() {
  local draft
  draft=$(found release view "$1" --repo "$repo" --json isDraft -q .isDraft) || { status=$?; [[ $status == 1 ]] && { echo no; return; }; exit "$status"; }
  [[ "$draft" == true ]] || fail "$1 is already published and is never replaced; start a new workflow run for a new number"
  echo yes
}

# check_tag: an existing tag must peel to the commit being released.
check_tag() {
  local at
  at=$(found api "repos/$repo/commits/refs/tags/$1" -q .sha) || { status=$?; [[ $status == 1 ]] && return 0; exit "$status"; }
  [[ "$at" == "$GITHUB_SHA" ]] || fail "tag $1 points at $at, not the commit being released ($GITHUB_SHA)"
}

# latest_flag is --latest when tag is at least the current latest release;
# a tag that can't be ordered is never made latest.
latest_flag() {
  local current mine theirs
  mine=$(build_of "$1")
  [[ -n "$mine" ]] || { echo "--latest=false"; return; }
  current=$(found api "repos/$repo/releases/latest" -q .tag_name) || { status=$?; [[ $status == 1 ]] && { echo "--latest"; return; }; exit "$status"; }
  theirs=$(build_of "$current")
  [[ -n "$theirs" ]] || { echo "--latest"; return; }
  read -r mn mp <<<"$mine"; read -r tn tp <<<"$theirs"
  if (( mn > tn || (mn == tn && mp >= tp) )); then echo "--latest"; else echo "--latest=false"; fi
}

# Every check first; only then any write.
tag=$(release_tag)
check_tag "$tag"
redo=$(existing_draft "$tag")
latest=$(latest_flag "$tag")
if [[ "$redo" == yes ]]; then
  gh release delete "$tag" --repo "$repo" --yes
fi
# The bundles live in their bundles-<digest> releases, named by each bundle manifest.
rm -f release/collab-ai-bundle-*.tar.gz release/collab-ai-bundle-inputs-linux-*.json
gh release create "$tag" release/* --repo "$repo" --target "$GITHUB_SHA" --draft --title "$tag" --generate-notes
# Keep incomplete uploads out of latest. Published assets are never overwritten.
gh release edit "$tag" --repo "$repo" --draft=false "$latest"
echo "published $tag"
