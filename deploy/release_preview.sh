#!/usr/bin/env bash
# Publish a commit as a Preview release (docs/UPDATES.md, "Publishing
# a Preview release"): a prerelease that installs on the Preview channel
# take and installs on Stable never see, signed, notarized, and never marked
# "latest".
#
# The version is vMAJOR.MINOR.N after the newest Stable release vMAJOR.MINOR
# (v1.188.1 after v1.188), which every updater orders after that release and
# before the next Stable one (v1.189): the Node updaters of v1.180 to v1.188
# (server/src/updates.ts, parseVersion and compareVersions) and the Go one
# (server-go/internal/updates/versions.go) both compare MAJOR, MINOR and
# PATCH as numbers, with a missing PATCH as 0. A Preview install therefore
# moves on to the next Stable release when it comes out.
#
# Steps: checks (the commit is on the branch and builds the Go version, its
# required CI checks passed, the version is free and in that range, the
# signing and notary secrets are set); an annotated tag pushed to origin; the GitHub release created as a
# prerelease with --latest=false (and "latest" checked to be unchanged
# after); linux-app and mac-app dispatched with release=VERSION and
# channel=preview, which sign the manifests with
# STUDIO_RELEASE_CHANNEL=preview, sign and notarize the macOS app, and leave
# the fixed linux and macos releases alone. With WAIT=1 it waits for both
# runs and then checks the release with deploy/mark_preview.sh --check.
#
# Usage: deploy/release_preview.sh [COMMIT] [VERSION]
#   COMMIT   default the tip of the branch (origin/main)
#   VERSION  default the next vMAJOR.MINOR.N after the newest Stable release
# Environment:
#   PREVIEW_BRANCH  the branch the commit must be on (default main)
#   WORKFLOW_REF    the branch whose workflow files are dispatched (default
#                   PREVIEW_BRANCH; it must have the channel input)
#   WAIT=1          wait for the two runs, then run mark_preview.sh --check
#   PREVIEW_NOTES   text put before the standard release notes
#   DRY_RUN=1       print what it would do after the checks, change nothing
#   CI_WAIT=0       fail at once when the commit's required checks are still
#                   running, rather than waiting for them (up to 45 minutes)
#   CI_GATE=0       skip the CI check below (a commit whose CI cannot pass
#                   for a reason outside it; say why in the release notes)
#   ALLOW_UNSIGNED=1  go on without the macOS signing secrets (the release
#                   then carries the ad hoc signed app)
# Needs git and gh, signed in with rights to push tags, create releases and
# run workflows.
set -euo pipefail

REPO="${STUDIO_RELEASE_REPO:-parallelworks/activate-studio}"
BRANCH="${PREVIEW_BRANCH:-main}"
WORKFLOW_REF="${WORKFLOW_REF:-$BRANCH}"
fail() { echo "release_preview: $*" >&2; exit 1; }
say() { echo "== $*"; }
run() { if [ "${DRY_RUN:-0}" = 1 ]; then echo "   would run: $*"; else "$@"; fi; }

[ $# -le 2 ] || fail "usage: release_preview.sh [COMMIT] [VERSION]"
command -v git >/dev/null || fail "git is needed"
command -v gh >/dev/null || fail "gh is needed"
cd "$(git rev-parse --show-toplevel)"

git fetch -q --tags origin "$BRANCH" || fail "could not fetch $BRANCH and the tags from origin"
COMMIT="$(git rev-parse --verify "${1:-origin/$BRANCH}^{commit}")" || fail "no commit ${1:-origin/$BRANCH}"
git merge-base --is-ancestor "$COMMIT" "origin/$BRANCH" || fail "$COMMIT is not on origin/$BRANCH"
# grep -c reads to the end: grep -q would quit early and, under pipefail, git show's SIGPIPE would fail the check.
# The build at that commit must be the Go version by default.
git show "$COMMIT:deploy/linux/build_app.sh" | grep -c 'STUDIO_SERVER:-go-only' >/dev/null ||
  fail "deploy/linux/build_app.sh at $COMMIT does not build the Go version by default"
git show "$COMMIT:deploy/mac/build_app.sh" | grep -c 'STUDIO_SERVER:-go-only' >/dev/null ||
  fail "deploy/mac/build_app.sh at $COMMIT does not build the Go version by default"
# The workflows dispatched must sign for a channel.
for w in linux-app mac-app; do
  git show "origin/$WORKFLOW_REF:.github/workflows/$w.yml" 2>/dev/null | grep -c '^      channel:' >/dev/null ||
    fail ".github/workflows/$w.yml on $WORKFLOW_REF has no channel input"
done

# The commit's CI: each required check (the ruleset's, below) passed on the
# commit, or on the head of the pull request it was merged from when that
# head has the same tree, which is the case when the branch was up to date
# with main at the squash: main's own run of those checks then repeats one
# already done on the same files. Otherwise this waits for main's run.
REQUIRED_CHECKS="${REQUIRED_CHECKS:-build-and-test macos build-test zizmor}"
# ok, fail, cancelled or wait: the newest run of each required check on a
# commit. A push to main cancels the runs still queued for the push before
# it, so a commit in the middle of a series of merges is often cancelled.
ci_state() {
  local runs name state=ok line
  runs="$(gh api --paginate "repos/$REPO/commits/$1/check-runs?per_page=100" \
    -q '.check_runs[] | [.name, (.id | tostring), .status, (.conclusion // "")] | @tsv' 2>/dev/null)" || { echo wait; return; }
  for name in $REQUIRED_CHECKS; do
    line="$(awk -F'\t' -v n="$name" '$1 == n' <<<"$runs" | sort -t$'\t' -k2,2n | tail -n 1)"
    case "$(cut -f3 <<<"$line")/$(cut -f4 <<<"$line")" in
      completed/success|completed/skipped|completed/neutral) ;;
      completed/cancelled) echo cancelled; return ;;
      completed/*) echo fail; return ;;
      *) state=wait ;;
    esac
  done
  echo "$state"
}
if [ "${CI_GATE:-1}" = 1 ]; then
  TREE="$(git rev-parse "$COMMIT^{tree}")"
  HEADS="$(gh api "repos/$REPO/commits/$COMMIT/pulls" -q '.[].head.sha' 2>/dev/null || true)"
  deadline=$(( $(date +%s) + 45 * 60 ))
  while :; do
    via=; state="$(ci_state "$COMMIT")"
    if [ "$state" != ok ]; then
      for h in $HEADS; do
        [ "$(gh api "repos/$REPO/git/commits/$h" -q .tree.sha 2>/dev/null)" = "$TREE" ] || continue
        [ "$(ci_state "$h")" = ok ] && { state=ok; via=" (on the pull request's head ${h:0:7}, the same tree)"; break; }
      done
    fi
    case "$state" in
      ok) say "required checks passed for ${COMMIT:0:7}$via"; break ;;
      fail) fail "a required check ($REQUIRED_CHECKS) failed on ${COMMIT:0:7}; see gh run list --commit $COMMIT" ;;
      cancelled) fail "a required check on ${COMMIT:0:7} was cancelled, usually by a newer push to $BRANCH: release the newer commit, or rerun the cancelled run" ;;
    esac
    [ "${CI_WAIT:-1}" = 1 ] || fail "the required checks on ${COMMIT:0:7} are still running (CI_WAIT=1 waits for them)"
    [ "$(date +%s)" -lt "$deadline" ] || fail "the required checks on ${COMMIT:0:7} did not finish within 45 minutes"
    echo "   waiting for the required checks on ${COMMIT:0:7}..."
    sleep 30
  done
fi

# The newest Stable release: not a prerelease, not a draft, a version tag.
STABLE="$(gh release list -R "$REPO" --exclude-pre-releases --exclude-drafts -L 50 --json tagName -q '.[].tagName' |
  grep -E '^v[0-9]+\.[0-9]+(\.[0-9]+)?$' | sort -V | tail -n 1)"
[ -n "$STABLE" ] || fail "found no Stable release"
BASE="$(printf '%s' "$STABLE" | sed -E 's/^(v[0-9]+\.[0-9]+).*/\1/')"
NEXT_STABLE="$(printf '%s' "$BASE" | awk -F. '{ printf "%s.%d", $1, $2 + 1 }')"
if [ -n "${2:-}" ]; then
  VERSION="$2"
else
  # One past the highest vMAJOR.MINOR.N tag or release there is.
  n="$( { git tag -l "$BASE.*"; gh release list -R "$REPO" -L 100 --json tagName -q '.[].tagName'; } |
    sed -n "s/^${BASE//./\\.}\.\([0-9][0-9]*\)$/\1/p" | sort -n | tail -n 1)"
  VERSION="$BASE.$(( ${n:-0} + 1 ))"
fi
printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || fail "$VERSION is not vMAJOR.MINOR.PATCH"
# Newer than the newest Stable, older than the next one (same MAJOR.MINOR).
[ "$(printf '%s\n' "$VERSION" "$STABLE" | sort -V | tail -n 1)" = "$VERSION" ] && [ "$VERSION" != "$STABLE" ] ||
  fail "$VERSION is not newer than the newest Stable release $STABLE"
case "$VERSION" in "$BASE".*) ;; *) fail "$VERSION is not between $STABLE and the next Stable $NEXT_STABLE" ;; esac
git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null && fail "the tag $VERSION exists"
git ls-remote --exit-code --tags origin "refs/tags/$VERSION" >/dev/null 2>&1 && fail "the tag $VERSION exists on origin"
gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1 && fail "the release $VERSION exists"

# What signing needs: names only, never values.
secrets="$(gh secret list -R "$REPO" --json name -q '.[].name')"
missing=
for s in STUDIO_UPDATE_SIGNING_KEY MACOS_SIGNING_P12 MACOS_SIGNING_P12_PASSWORD NOTARY_KEY_P8 NOTARY_KEY_ID NOTARY_ISSUER_ID; do
  grep -qx "$s" <<<"$secrets" || missing="$missing $s"
done
gh variable get APPLE_TEAM_ID -R "$REPO" >/dev/null 2>&1 || missing="$missing APPLE_TEAM_ID(variable)"
case "$missing" in
  *STUDIO_UPDATE_SIGNING_KEY*) fail "the repository has no STUDIO_UPDATE_SIGNING_KEY secret" ;;
esac
if [ -n "$missing" ]; then
  [ "${ALLOW_UNSIGNED:-0}" = 1 ] || fail "the macOS app would not be signed and notarized; missing:$missing (ALLOW_UNSIGNED=1 goes on)"
  echo "warning: missing$missing; the macOS app will be signed ad hoc only" >&2
fi

LATEST="$(gh api "repos/$REPO/releases/latest" -q .tag_name 2>/dev/null || true)"
say "Preview $VERSION from $(git log -1 --format='%h %s' "$COMMIT")"
say "newest Stable $STABLE, next Stable $NEXT_STABLE, latest $LATEST, workflows from $WORKFLOW_REF"

run git tag -a "$VERSION" "$COMMIT" -m "ACTIVATE Studio $VERSION (Preview)"
run git push -q origin "refs/tags/$VERSION"
NOTES="ACTIVATE Studio $VERSION is a Preview release. Only installs set to the Preview channel (Settings, Updates) take it; Stable installs stay on $STABLE and move on at $NEXT_STABLE. See docs/UPDATES.md, \"Channels\"."
[ -z "${PREVIEW_NOTES:-}" ] || NOTES="$PREVIEW_NOTES"$'\n\n'"$NOTES"
run gh release create "$VERSION" -R "$REPO" --verify-tag --prerelease --latest=false --title "$VERSION (Preview)" --notes "$NOTES"
if [ "${DRY_RUN:-0}" != 1 ]; then
  now="$(gh api "repos/$REPO/releases/latest" -q .tag_name 2>/dev/null || true)"
  [ "$now" = "$LATEST" ] || fail "\"latest\" moved from $LATEST to $now; set it back with: gh release edit $LATEST -R $REPO --latest"
  [ "$(gh release view "$VERSION" -R "$REPO" --json isPrerelease -q .isPrerelease)" = true ] || fail "$VERSION is not marked a prerelease"
fi

since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
for w in linux-app mac-app; do
  run gh workflow run "$w.yml" -R "$REPO" --ref "$WORKFLOW_REF" -f release="$VERSION" -f channel=preview
done
say "dispatched linux-app and mac-app for $VERSION (channel preview)"
[ "${DRY_RUN:-0}" = 1 ] && exit 0

if [ "${WAIT:-0}" = 1 ]; then
  sleep 15
  for w in linux-app mac-app; do
    id="$(gh run list -R "$REPO" --workflow "$w.yml" --event workflow_dispatch -L 10 --json databaseId,createdAt \
      -q "[.[] | select(.createdAt >= \"$since\")] | last | .databaseId")"
    [ -n "$id" ] && [ "$id" != null ] || fail "could not find the $w run"
    say "$w: https://github.com/$REPO/actions/runs/$id"
    gh run watch "$id" -R "$REPO" --exit-status >/dev/null || fail "the $w run $id failed"
  done
  deploy/mark_preview.sh "$VERSION" --check
  now="$(gh api "repos/$REPO/releases/latest" -q .tag_name 2>/dev/null || true)"
  [ "$now" = "$LATEST" ] || fail "\"latest\" moved from $LATEST to $now"
  say "$VERSION is published for Preview; latest is still $LATEST"
else
  echo "When both runs pass: deploy/mark_preview.sh $VERSION --check"
fi
