#!/usr/bin/env bash
# Notarize "ACTIVATE Studio.app" through its disk image, and staple the
# tickets. The image (from make_dmg.sh, of the app sign_app.sh signed) is
# signed with the identity that signed the app, submitted to Apple's notary
# service and waited for; a rejection prints the notary log, which names
# each problem. Once accepted, the ticket is stapled to the image and to
# the app, whose archive the release carries, and Gatekeeper's own
# assessment of both is checked.
#
# Usage: deploy/mac/notarize.sh APP DMG
#   SIGN_IDENTITY     the identity for the image, as codesign takes it; by
#                     default the one the app is signed with
#   SIGN_KEYCHAIN     the keychain that holds it, when not in the search list
#   NOTARY_KEY_P8     the App Store Connect API key (the .p8 file's text),
#                     or NOTARY_KEY_FILE, the .p8 file
#   NOTARY_KEY_ID     the key's ID
#   NOTARY_ISSUER_ID  its issuer ID
set -euo pipefail

fail() { echo "notarize: $*" >&2; exit 1; }

[ $# -eq 2 ] || fail "usage: notarize.sh APP DMG"
[ -f "$1/Contents/Info.plist" ] || fail "$1 is not an app bundle"
[ -f "$2" ] || fail "no such disk image: $2"
command -v xcrun >/dev/null 2>&1 || fail "xcrun is needed (macOS, with Xcode or its command line tools)"
[ -n "${NOTARY_KEY_ID:-}" ] && [ -n "${NOTARY_ISSUER_ID:-}" ] || fail "set NOTARY_KEY_ID and NOTARY_ISSUER_ID"
APP="$(cd "$1" && pwd -P)"
DMG="$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/notarize.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# The key goes to notarytool as a file only this user can read, removed on
# exit, and is never printed.
if [ -n "${NOTARY_KEY_P8:-}" ]; then
  KEY="$WORK/AuthKey.p8"
  (umask 077 && printf '%s\n' "$NOTARY_KEY_P8" >"$KEY")
elif [ -n "${NOTARY_KEY_FILE:-}" ]; then
  KEY="$NOTARY_KEY_FILE"
  [ -f "$KEY" ] || fail "no such key file: $KEY"
else
  fail "set NOTARY_KEY_P8 (the API key's .p8 text) or NOTARY_KEY_FILE"
fi
notary() { xcrun notarytool "$@" --key "$KEY" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID"; }

# Only an app signed with a Developer ID certificate can be notarized.
signer="$(codesign -dvv "$APP" 2>&1 | sed -n 's/^Authority=//p' | head -n 1)"
case "$signer" in
  "Developer ID Application: "*) ;;
  *) fail "the app is signed by ${signer:-no certificate (ad hoc)}, not a Developer ID Application certificate; sign it with sign_app.sh" ;;
esac
codesign --verify --deep --strict "$APP" || fail "the app's signature does not verify"
IDENTITY="${SIGN_IDENTITY:-$signer}"

echo "Signing the disk image ($signer)..."
out="$(codesign --force --sign "$IDENTITY" ${SIGN_KEYCHAIN:+--keychain "$SIGN_KEYCHAIN"} --timestamp "$DMG" 2>&1)" ||
  { printf '%s\n' "$out" >&2; fail "codesign could not sign the disk image"; }
codesign --verify --strict "$DMG" || fail "the disk image's signature does not verify"

echo "Submitting $(basename "$DMG") to the notary service and waiting for it..."
result="$(notary submit "$DMG" --wait --timeout 60m --output-format json)" || true
id="$(plutil -extract id raw -o - - <<<"$result" 2>/dev/null || true)"
status="$(plutil -extract status raw -o - - <<<"$result" 2>/dev/null || true)"
echo "  submission ${id:-(none)}: ${status:-no answer}"
if [ "$status" = "In Progress" ]; then
  fail "the notary service had not finished after an hour; xcrun notarytool info $id shows where it is"
fi
if [ "$status" != Accepted ]; then
  printf '%s\n' "$result" >&2
  if [ -n "$id" ]; then
    echo "The notary log:" >&2
    notary log "$id" >&2 || true
  fi
  fail "the notary service did not accept the disk image"
fi

# The ticket for a just-accepted submission can take a moment to be
# served; stapling is tried a few times.
staple() {
  local n out
  for n in 1 2 3 4 5; do
    if out="$(xcrun stapler staple "$1" 2>&1)"; then return 0; fi
    sleep "$((n * 10))"
  done
  printf '%s\n' "$out" >&2
  fail "could not staple the ticket to $1"
}
echo "Stapling..."
staple "$DMG"
staple "$APP"

echo "Checking..."
failures=0
for f in "$DMG" "$APP"; do
  if out="$(xcrun stapler validate "$f" 2>&1)"; then
    echo "  ok    the ticket is stapled to $(basename "$f")"
  else
    echo "  FAIL  stapler validate $(basename "$f"):"; printf '%s\n' "$out" | sed 's/^/        /'
    failures=$((failures + 1))
  fi
done
assess() {
  local what="$1" out
  shift
  out="$(spctl "$@" 2>&1 || true)"
  if grep -q ': accepted$' <<<"$out" && grep -q '^source=Notarized Developer ID$' <<<"$out"; then
    echo "  ok    Gatekeeper accepts $what as notarized Developer ID"
  else
    echo "  FAIL  Gatekeeper's assessment of $what:"; printf '%s\n' "$out" | sed 's/^/        /'
    failures=$((failures + 1))
  fi
}
assess "the app" -a -vvv -t exec "$APP"
assess "the disk image" -a -t open --context context:primary-signature -vv "$DMG"
[ "$failures" = 0 ] || fail "$failures check(s) failed"
echo "Notarized and stapled: $APP, $DMG"
