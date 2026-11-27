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
#     is published without becoming latest.
set -euo pipefail

: "${GITHUB_REPOSITORY:?}" "${GITHUB_REF:?}" "${GITHUB_REF_NAME:?}" "${GITHUB_SHA:?}" "${GITHUB_RUN_NUMBER:?}"
repo="$GITHUB_REPOSITORY"

fail() { echo "::error::$*" >&2; exit 1; }

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

# clear_draft: a published release stops here; a draft from an earlier
# attempt is removed, keeping its tag.
clear_draft() {
  local draft
  draft=$(gh release view "$1" --repo "$repo" --json isDraft -q .isDraft 2>/dev/null) || return 0
  [[ "$draft" == true ]] || fail "$1 is already published and is never replaced; start a new workflow run for a new number"
  gh release delete "$1" --repo "$repo" --yes
}

# check_tag: an existing tag must peel to the commit being released.
check_tag() {
  local at
  at=$(gh api "repos/$repo/commits/refs/tags/$1" -q .sha 2>/dev/null) || return 0
  [[ "$at" == "$GITHUB_SHA" ]] || fail "tag $1 points at $at, not the commit being released ($GITHUB_SHA)"
}

# latest_flag is --latest when tag is at least the current latest release.
latest_flag() {
  local current mine theirs
  current=$(gh api "repos/$repo/releases/latest" -q .tag_name 2>/dev/null) || { echo "--latest"; return; }
  mine=$(build_of "$1"); theirs=$(build_of "$current")
  if [[ -z "$mine" || -z "$theirs" ]]; then echo "--latest"; return; fi
  read -r mn mp <<<"$mine"; read -r tn tp <<<"$theirs"
  if (( mn > tn || (mn == tn && mp >= tp) )); then echo "--latest"; else echo "--latest=false"; fi
}

tag=$(release_tag)
clear_draft "$tag"
check_tag "$tag"
# The bundles live in their bundles-<digest> releases, named by each bundle manifest.
rm -f release/collab-ai-bundle-*.tar.gz release/collab-ai-bundle-inputs-linux-*.json
gh release create "$tag" release/* --repo "$repo" --target "$GITHUB_SHA" --draft --title "$tag" --generate-notes
# Keep incomplete uploads out of latest. Published assets are never overwritten.
gh release edit "$tag" --repo "$repo" --draft=false "$(latest_flag "$tag")"
echo "published $tag"
