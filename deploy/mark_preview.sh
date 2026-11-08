#!/usr/bin/env bash
# Mark a published release as a Preview release (docs/UPDATES.md, "Publishing
# a Preview release"): checks that every platform manifest on it was signed
# with "channel": "preview", then sets the GitHub prerelease flag, which is
# what Studios from before channels go by, and takes it off "latest".
#
# Usage: deploy/mark_preview.sh TAG [--check]
#   TAG       the release, such as v1.200
#   --check   only check the manifests and the flag; change nothing
#
# Needs gh, signed in with rights to edit releases. The manifests are signed
# by deploy/mac/sign_release.sh and deploy/linux/sign_release.sh with
# STUDIO_RELEASE_CHANNEL=preview; this script cannot add the field, since the
# signature covers the manifest's bytes.
set -euo pipefail

REPO="${STUDIO_RELEASE_REPO:-parallelworks/activate-studio}"
fail() { echo "mark_preview: $*" >&2; exit 1; }

[ $# -ge 1 ] && [ $# -le 2 ] || fail "usage: mark_preview.sh TAG [--check]"
TAG="$1"
CHECK="${2:-}"
[ -z "$CHECK" ] || [ "$CHECK" = --check ] || fail "usage: mark_preview.sh TAG [--check]"
printf '%s' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+(\.[0-9]+)?$' || fail "$TAG is not a release version (vMAJOR.MINOR[.PATCH])"
command -v gh >/dev/null || fail "gh is needed"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/mark-preview.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

gh release download "$TAG" --repo "$REPO" --pattern '*.manifest.json' --dir "$WORK" >/dev/null 2>&1 || true
shopt -s nullglob
manifests=("$WORK"/*.manifest.json)
[ ${#manifests[@]} -gt 0 ] || fail "$TAG has no signed manifests yet; attach the platform builds first"
bad=0
for m in "${manifests[@]}"; do
  if grep -Eq '"channel": *"preview"' "$m"; then
    echo "  $(basename "$m"): channel preview"
  else
    echo "  $(basename "$m"): not signed as preview" >&2
    bad=1
  fi
done
[ "$bad" = 0 ] || fail "sign every platform's manifest with STUDIO_RELEASE_CHANNEL=preview and attach it again"

flag="$(gh release view "$TAG" --repo "$REPO" --json isPrerelease --jq .isPrerelease)"
if [ "$CHECK" = --check ]; then
  [ "$flag" = true ] || fail "$TAG is not marked as a prerelease on GitHub"
  echo "$TAG is a Preview release."
  exit 0
fi
[ "$flag" = true ] || gh release edit "$TAG" --repo "$REPO" --prerelease --latest=false >/dev/null
echo "$TAG is a Preview release: Stable Studios, old and new, do not see it."
