#!/usr/bin/env bash
# Updates an installed ACTIVATE Studio to a build that has a new GUFI, with
# real indexes in place, and checks that every library answers as it did.
#
# An older release is installed in ~/Applications and opened. It builds a
# main-folder-free set of libraries of a small folder (by name, by text, and
# by meaning), and a battery of searches, queries, statistics and library
# states is recorded. The app is quit and replaced by the build under test,
# opened on the same data, and the battery runs again: nothing may have been
# rebuilt (the indexes keep their modification times), and every answer must
# match. Then each library is rebuilt by the new GUFI and the battery runs a
# third time.
#
# Usage: mac-upgrade-e2e.sh FROM_DMG_URL TO_APP OUT_DIR
#   FROM_DMG_URL  a published disk image of the older release
#   TO_APP        the "ACTIVATE Studio.app" to update to
# OUT_DIR gets summary.txt, before.json, after.json, rebuilt.json and the
# Studio's logs. A failed check ends the run with "FAIL: why".
set -uo pipefail

URL="$1"
TO_APP="$2"
OUT="$3"
APP_NAME="ACTIVATE Studio"
DEST="$HOME/Applications/$APP_NAME.app"
DATA="$HOME/Library/Application Support/$APP_NAME"
API=http://127.0.0.1:4180
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$OUT"
SUMMARY="$OUT/summary.txt"
T0=$(date +%s)
log() { printf '%4ss  %s\n' "$(( $(date +%s) - T0 ))" "$*" | tee -a "$SUMMARY"; }
collect() { [ -d "$DATA/logs" ] && cp -R "$DATA/logs" "$OUT/logs" 2>/dev/null; return 0; }
fail() { log "FAIL: $*"; collect; exit 1; }
version() { curl -fsS --max-time 3 "$API/api/version" 2>/dev/null; }
bundle_version() { /usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$1/Contents/Info.plist" 2>/dev/null; }
gufi_of() { DYLD_LIBRARY_PATH= "$1/Contents/Resources/gufi/bin/gufi_query" --version 2>&1 | head -n 1; }
wait_server() {
  local v=
  for _ in $(seq 1 120); do v="$(version)" && break; sleep 2; done
  [ -n "$v" ] || return 1
  echo "$v"
}
stop_app() {
  osascript -e "tell application \"$APP_NAME\" to quit" >/dev/null 2>&1 || true
  sleep 3
  "$DEST/Contents/MacOS/$APP_NAME" --stop >>"$SUMMARY" 2>&1 || true
  for _ in $(seq 1 30); do version >/dev/null || return 0; sleep 1; done
  return 1
}

log "runner: $(sw_vers -productName) $(sw_vers -productVersion) ($(uname -m))"

# 1. The older release, installed as the update e2e does: copied from its
# disk image and opened without the quarantine mark.
DMG="$OUT/../from.dmg"
curl -fsSL -o "$DMG" "$URL" || fail "could not download $URL"
MNT="$(mktemp -d)"
hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$DMG" >/dev/null || fail "could not mount $DMG"
mkdir -p "$HOME/Applications"
ditto "$MNT/$APP_NAME.app" "$DEST" || fail "could not copy the app to $DEST"
hdiutil detach "$MNT" >/dev/null 2>&1 || true
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
FROM="$(bundle_version "$DEST")"
FROM_GUFI="$(gufi_of "$DEST")"
log "installed $FROM, GUFI: $FROM_GUFI"

open "$DEST" &
v="$(wait_server)" || fail "the server of $FROM did not answer"
log "the server answers: $v"

# 2. A folder with the shapes the Studio meets, and a library of it at each depth.
SRC="$HOME/e2e-docs"
python3 "$HERE/mac-upgrade-e2e.py" corpus "$SRC" || fail "could not make the corpus"
curl -fsS -X POST "$API/api/setup/complete" -H 'content-type: application/json' -d '{}' >/dev/null || true
for spec in "names:names" "text:text" "meaning:meaning-documents"; do
  id="${spec%%:*}"; depth="${spec##*:}"
  out="$(curl -sS -X POST "$API/api/libraries/build" -H 'content-type: application/json' \
    -d "{\"sourceRoot\":\"$SRC\",\"label\":\"$id\",\"id\":\"e2e-$id\",\"depth\":\"$depth\"}")"
  case "$out" in *'"library"'*) log "building e2e-$id at depth $depth" ;; *) fail "the build of e2e-$id did not start: $out" ;; esac
done
python3 "$HERE/mac-upgrade-e2e.py" wait "$API" 900 || fail "the libraries did not finish building under $FROM"
log "libraries ready under $FROM"
python3 "$HERE/mac-upgrade-e2e.py" battery "$API" "$OUT/before.json" || fail "the battery failed under $FROM"
python3 "$HERE/mac-upgrade-e2e.py" stamps "$DATA" "$OUT/stamps-before.json" || fail "could not read the index files"
python3 "$HERE/mac-upgrade-e2e.py" format "$DATA" | tee -a "$SUMMARY"

# 3. Update: the app quit, replaced by the build under test, opened on the same data.
stop_app || fail "the server of $FROM did not stop"
log "stopped $FROM"
rm -rf "$DEST"
ditto "$TO_APP" "$DEST" || fail "could not copy $TO_APP to $DEST"
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
TO="$(bundle_version "$DEST")"
TO_GUFI="$(gufi_of "$DEST")"
log "replaced with $TO, GUFI: $TO_GUFI"
[ "$FROM_GUFI" != "$TO_GUFI" ] || log "note: both builds report the same GUFI version"
case "$TO_GUFI" in *0.8.1*) ;; *) fail "the new build's GUFI is '$TO_GUFI', not 0.8.1" ;; esac
open "$DEST" &
v="$(wait_server)" || fail "the server of $TO did not answer"
log "the server answers: $v"
sleep 20

python3 "$HERE/mac-upgrade-e2e.py" wait "$API" 600 || fail "a library was still building after the update to $TO"
python3 "$HERE/mac-upgrade-e2e.py" stamps "$DATA" "$OUT/stamps-after.json" || fail "could not read the index files"
python3 "$HERE/mac-upgrade-e2e.py" same-stamps "$OUT/stamps-before.json" "$OUT/stamps-after.json" || fail "the update touched an index"
log "no index was touched by the update"
python3 "$HERE/mac-upgrade-e2e.py" battery "$API" "$OUT/after.json" || fail "the battery failed under $TO"
python3 "$HERE/mac-upgrade-e2e.py" compare "$OUT/before.json" "$OUT/after.json" | tee -a "$SUMMARY"
[ "${PIPESTATUS[0]}" = 0 ] || fail "answers changed with the update (above)"
log "every answer under $TO matches the one under $FROM"

# 4. Each library rebuilt by the new GUFI.
for id in names text meaning; do
  curl -fsS -X POST "$API/api/libraries/e2e-$id/rebuild" -H 'content-type: application/json' -d '{}' >/dev/null || fail "could not rebuild e2e-$id"
done
sleep 5
python3 "$HERE/mac-upgrade-e2e.py" wait "$API" 900 || fail "the rebuilds did not finish"
python3 "$HERE/mac-upgrade-e2e.py" battery "$API" "$OUT/rebuilt.json" || fail "the battery failed after the rebuild"
python3 "$HERE/mac-upgrade-e2e.py" compare "$OUT/before.json" "$OUT/rebuilt.json" | tee -a "$SUMMARY"
[ "${PIPESTATUS[0]}" = 0 ] || fail "answers changed with the rebuild (above)"
log "every answer after the rebuild matches the one before the update"
python3 "$HERE/mac-upgrade-e2e.py" format "$DATA" | tee -a "$SUMMARY"

stop_app || log "the server of $TO did not stop"
collect
log "PASS"
