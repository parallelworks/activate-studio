#!/usr/bin/env bash
# Sign "ACTIVATE Studio.app" with a certificate, for notarization. Each
# Mach-O file in the bundle (Node, Python and its libraries, GUFI's tools
# and libraries, tesseract and leptonica, the document converter, and any
# other file that is one)
# is signed first, deepest first, then the bundle itself, which signs its
# executable and seals every other file. Everything is signed with the
# hardened runtime, a secure timestamp, and the entitlements it needs,
# from deploy/mac/entitlements:
#
#   node.plist  Resources/node/bin/node. V8 compiles JavaScript to machine
#               code as it runs: allow-jit and
#               allow-unsigned-executable-memory.
#   app.plist   the app's executable, the window. The chat's voice input
#               records from the microphone, which Info.plist's
#               NSMicrophoneUsageDescription explains: device.audio-input.
#
# Python, GUFI, tesseract and the converter need none. Library validation,
# which the hardened runtime turns on, lets a program load only the
# system's libraries and those signed by its own team, and every file here
# is signed by the same one.
#
# build_app.sh signs the bundle ad hoc; this replaces that signature.
# Links that lead nowhere are removed first, as there. At the end every
# signature is checked, and what failed is printed.
#
# Usage: deploy/mac/sign_app.sh APP IDENTITY [KEYCHAIN]
#   IDENTITY  the certificate to sign with, as codesign takes it: its name,
#             such as "Developer ID Application: Name (TEAMID)", or its
#             SHA-1 hash
#   KEYCHAIN  the keychain that holds it, when it is not in the search list
# IDENTITY - signs ad hoc, without a timestamp, to look at the result.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENT="$HERE/entitlements"

fail() { echo "sign_app: $*" >&2; exit 1; }

[ $# -ge 2 ] && [ $# -le 3 ] || fail "usage: sign_app.sh APP IDENTITY [KEYCHAIN]"
[ -f "$1/Contents/Info.plist" ] || fail "$1 is not an app bundle"
command -v codesign >/dev/null 2>&1 || fail "codesign is needed (macOS)"
APP="$(cd "$1" && pwd -P)"
IDENTITY="$2"
KEYCHAIN="${3:-}"
TS=--timestamp
[ "$IDENTITY" != - ] || TS=--timestamp=none
for f in "$ENT/node.plist" "$ENT/app.plist"; do
  plutil -lint "$f" >/dev/null || fail "$f is not a valid plist"
done

EXE="$APP/Contents/MacOS/$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$APP/Contents/Info.plist")"
NODE="$APP/Contents/Resources/node/bin/node"
# A script as the executable (an app built on Linux) keeps its signature in
# extended attributes, which the release archive drops.
file -b "$EXE" | grep -q Mach-O || fail "the app's executable is not the native window ($(file -b "$EXE")); build the app on a Mac"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/sign-app.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# The entitlements file a file is signed with, if any.
entitlements_for() {
  case "$1" in
    "$APP") printf '%s' "$ENT/app.plist" ;;
    "$NODE") printf '%s' "$ENT/node.plist" ;;
  esac
}

# Tried three times, since the timestamp server fails now and then.
sign() {
  local f="$1" e out n
  e="$(entitlements_for "$f")"
  for n in 1 2 3; do
    if out="$(codesign --force --sign "$IDENTITY" ${KEYCHAIN:+--keychain "$KEYCHAIN"} --options runtime "$TS" \
      ${e:+--entitlements "$e"} "$f" 2>&1)"; then
      return 0
    fi
    sleep "$((n * 5))"
  done
  printf '%s\n' "$out" >&2
  fail "codesign could not sign ${f#"$APP"/}"
}

# Links that lead nowhere fail the bundle's signature check (build_app.sh
# says where they come from), and extended attributes, such as a
# quarantine flag or Finder information, are no part of a signature.
find "$APP" -type l | while IFS= read -r l; do [ -e "$l" ] || rm -f "$l"; done
xattr -cr "$APP"

# Every Mach-O file other than the executable, by the magic numbers file(1)
# reads (a Java class file shares the universal one), deepest first.
python3 - "$APP" "$EXE" >"$WORK/files" <<'PY'
import os, struct, sys
root, exe = sys.argv[1], sys.argv[2]
thin = {b'\xfe\xed\xfa\xce', b'\xce\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xcf\xfa\xed\xfe'}
fat = {b'\xca\xfe\xba\xbe', b'\xca\xfe\xba\xbf'}
found = []
for d, _dirs, files in os.walk(root):
    for name in files:
        p = os.path.join(d, name)
        if p == exe or os.path.islink(p):
            continue
        with open(p, 'rb') as fh:
            head = fh.read(8)
        if head[:4] in thin or (head[:4] in fat and len(head) == 8 and 0 < struct.unpack('>I', head[4:])[0] < 20):
            found.append(p)
found.sort(key=lambda p: (-p.count('/'), p))
sys.stdout.write(''.join(p + '\0' for p in found))
PY

n=0
echo "Signing the programs and libraries in the app ($IDENTITY)..."
while IFS= read -r -d '' f; do
  sign "$f"
  n=$((n + 1))
done <"$WORK/files"
echo "  $n files"
echo "Signing the bundle..."
sign "$APP"

echo "Checking the signatures..."
failures=0
bad() { echo "  FAIL  $1" >&2; failures=$((failures + 1)); }
keys_of_plist() { python3 -c 'import plistlib, sys; print(" ".join(sorted(plistlib.load(open(sys.argv[1], "rb")))))' "$1"; }
keys_signed() {
  codesign -d --entitlements - --xml "$1" 2>/dev/null |
    python3 -c 'import plistlib, sys; d = sys.stdin.buffer.read(); print(" ".join(sorted(plistlib.loads(d))) if d.strip() else "")'
}
team="$(codesign -dvv "$APP" 2>&1 | sed -n 's/^TeamIdentifier=//p')"
printf '%s\0' "$APP" >>"$WORK/files"
while IFS= read -r -d '' f; do
  rel="${f#"$APP"/}"
  [ "$f" = "$APP" ] && rel="the bundle"
  if ! out="$(codesign --verify --strict "$f" 2>&1)"; then
    bad "$rel: $out"
    continue
  fi
  info="$(codesign -dvv "$f" 2>&1)"
  grep -q '^CodeDirectory .*flags=0x[0-9a-f]*([^)]*runtime' <<<"$info" || bad "$rel: no hardened runtime"
  [ "$(sed -n 's/^TeamIdentifier=//p' <<<"$info")" = "$team" ] || bad "$rel: signed for another team than the bundle"
  if [ "$TS" = --timestamp ] && ! grep -q '^Timestamp=' <<<"$info"; then bad "$rel: no secure timestamp"; fi
  e="$(entitlements_for "$f")"
  want=""
  [ -n "$e" ] && want="$(keys_of_plist "$e")"
  got="$(keys_signed "$f")"
  [ "$got" = "$want" ] || bad "$rel: entitlements are '${got}', expected '${want}'"
done <"$WORK/files"
if ! out="$(codesign --verify --deep --strict --verbose=2 "$APP" 2>&1)"; then
  bad "codesign --verify --deep --strict:"
  printf '%s\n' "$out" | sed 's/^/        /' >&2
fi

if [ "$failures" -gt 0 ]; then
  fail "$failures signature check(s) failed"
fi
echo "  every signature verifies, with the hardened runtime$([ "$TS" = --timestamp ] && echo ' and a timestamp'), team ${team:-none}"
echo "  node: $(keys_signed "$NODE")"
echo "  the executable: $(keys_signed "$APP")"
