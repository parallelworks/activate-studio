#!/usr/bin/env bash
# End to end check, on a Mac, of the move from a released macOS app (the
# Swift window and the Node server) to the Go version (the Wails window and
# the Go server) through the ordinary update (docs/UPDATES.md, "The Go
# version"), as deploy/linux/test_go_bridge_e2e.sh does for Linux, run as
# the user with a scratch HOME:
#
# 1. The released app (OLD_ARCHIVE, activate-studio-macos-arm64.tar.gz) is
#    put in HOME/Applications, made to verify updates with a throwaway key,
#    and its server started from its own run loop (Resources/studio-run.sh,
#    as its LaunchAgent starts it).
# 2. Settings, a label, a second library and a conversation are made through
#    its API, and the folder is indexed.
# 3. A stand-in release server on 127.0.0.1 offers NEW_ARCHIVE (the Go
#    version), stamped as the next version and signed with that key. The old
#    server's own updater checks, downloads, verifies and swaps it in; its
#    run loop starts the new app's run loop, which starts the Go server.
# 4. Rolling back from the Go version puts OLD back.
#
# Nothing is signed with a real key, nothing is published, no network
# beyond 127.0.0.1 is used, and no LaunchAgent is installed. The old app's
# own Node and Python run the helpers.
#
# Usage: deploy/mac/test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE
#   PORT, FEED_PORT  the Studio's port and the release server's (default 6024, 6025)
#   KEEP=1           leave the scratch folder (its path is printed)
#   SEARCH=0         skip the search checks
set -euo pipefail

OLD_ARCHIVE="$(cd "$(dirname "${1:?usage: test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE}")" && pwd)/$(basename "$1")"
NEW_ARCHIVE="$(cd "$(dirname "${2:?usage: test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE}")" && pwd)/$(basename "$2")"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PORT="${PORT:-6024}"
FEED_PORT="${FEED_PORT:-6025}"
T="$(mktemp -d "${TMPDIR:-/tmp}/go-bridge-e2e.XXXXXX")"
T="$(cd "$T" && pwd -P)"
H="$T/home"
NAME="ACTIVATE Studio"
APP="$H/Applications/$NAME.app"
RES="$APP/Contents/Resources"
DATA="$H/Library/Application Support/$NAME"
KB="$T/kb"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill -TERM "$p" 2>/dev/null || true; done
  # The server the run loop runs, by the port it listens on.
  for p in $(lsof -ti "tcp:$PORT" -sTCP:LISTEN 2>/dev/null || true); do kill -TERM "$p" 2>/dev/null || true; done
  sleep 2
  for p in ${pids[@]+"${pids[@]}"}; do kill -KILL "$p" 2>/dev/null || true; done
  if [ "${KEEP:-}" = 1 ]; then echo "kept $T"; else chmod -R u+w "$T" 2>/dev/null || true; rm -rf "$T"; fi
}
trap cleanup EXIT
mkdir -p "$H/Applications" "$DATA" "$KB/notes" "$T/old" "$T/rel" "$T/keys" "$T/stage"
printf 'a recipe for lemon cake\n' >"$KB/cake.txt"
printf 'the index is a tree of per-directory databases\n' >"$KB/notes/design.md"

tar -xzf "$OLD_ARCHIVE" -C "$T/old"
OLD="$T/old/$NAME.app/Contents/Resources"
NODE="$OLD/node/bin/node"
PY="$OLD/python/bin/python3"
MJS="$HERE/../linux/update_e2e.mjs"
OLD_VERSION="$("$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$OLD/app/build-info.json")"
NEW_VERSION="${NEW_VERSION:-$("$PY" -I -c 'import sys; m, n = sys.argv[1].lstrip("v").split(".")[:2]; print(f"v{m}.{int(n) + 1}")' "$OLD_VERSION")}"
echo "Go version bridge on macOS: $OLD_VERSION (Swift and Node) to $NEW_VERSION (Wails and Go), in $T"

api() { "$PY" -I "$HERE/../linux/api.py" "$@"; }
json() { "$PY" -I -c 'import json, sys; d = json.load(sys.stdin); sys.exit(0 if eval(sys.argv[1]) else 1)' "$1"; }
field() { "$PY" -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
U="http://127.0.0.1:$PORT"
running_version() {
  api -m 2 GET "$U/api/version" 2>/dev/null | field 'd["version"]' 2>/dev/null ||
    api -m 5 GET "$U/api/updates" 2>/dev/null | field 'd["current"]["version"]' 2>/dev/null || true
}
wait_version() {   # wait_version VERSION SECONDS
  local n=0
  while [ "$n" -lt "$2" ]; do
    if [ "$(running_version)" = "$1" ]; then return 0; fi
    sleep 1; n=$((n + 1))
  done
  return 1
}
poll() {   # poll SECONDS PATH PYTHON-EXPRESSION-ON-d
  local n=0
  while [ "$n" -lt "$1" ]; do
    if api -m 30 GET "$U$2" 2>/dev/null | json "$3" 2>/dev/null; then return 0; fi
    sleep 1; n=$((n + 1))
  done
  return 1
}
log_tail() { tail -n 15 "$DATA/logs/studio.log" 2>/dev/null | cut -c1-240 | sed 's/^/        /'; }
# Which server listens on the port: the Go one is app/bin/studio-server.
server_is() {   # server_is go|node
  local p cmd found=
  for p in $(lsof -ti "tcp:$PORT" -sTCP:LISTEN 2>/dev/null || true); do
    cmd="$(ps -o command= -p "$p" 2>/dev/null || true)"
    case "$cmd" in
      "$RES/app/bin/studio-server"*) found=go ;;
      "$RES/node/bin/node"*) found=node ;;
    esac
  done
  [ "$found" = "$1" ]
}

# ---- 1. the released app ----

ditto "$T/old/$NAME.app" "$APP"
"$NODE" "$MJS" keygen "$T/keys"
"$NODE" "$MJS" stamp "$RES" "$OLD_VERSION" "$T/keys/update.pub"
printf "PORT='%s'\nKB_ROOT='%s'\nSTUDIO_UPDATE_URL='http://127.0.0.1:%s'\n" "$PORT" "$KB" "$FEED_PORT" >"$DATA/studio.env"
"$NODE" "$MJS" serve "$T/rel" "$FEED_PORT" &
pids+=($!)
env -i HOME="$H" PATH=/usr/bin:/bin:/usr/sbin:/sbin /bin/bash "$RES/studio-run.sh" "$DATA" >/dev/null 2>&1 &
pids+=($!)
if wait_version "$OLD_VERSION" 120; then ok "$OLD_VERSION runs"; else bad "$OLD_VERSION did not start"; log_tail; exit 1; fi
server_is node && ok "with the Node server" || bad "the server is not Node's"

# ---- 2. data ----

api -m 30 PUT "$U/api/settings" '{"appName": "Bridge Check", "theme": "dark"}' >/dev/null || bad "settings were not saved"
api -m 30 POST "$U/api/kb/tags" '{"paths": ["notes/design.md"], "add": ["keep-me"]}' >/dev/null || bad "the label was not applied"
mkdir -p "$T/second"
env -i PATH=/usr/bin:/bin "$RES/gufi/bin/gufi_dir2index" -x "$KB" "$T/second" >/dev/null 2>&1 || true
api -m 30 POST "$U/api/libraries" "{\"id\": \"second\", \"label\": \"Second\", \"indexRoot\": \"$T/second/$(basename "$KB")\"}" >/dev/null || bad "the second library was not added"
CONV="$(api -m 30 POST "$U/api/chat/conversations" '{"title": "kept across the update"}' | field 'd.get("id") or d.get("conversation", {}).get("id")' || true)"
[ -n "$CONV" ] && [ "$CONV" != None ] || bad "no conversation was made"
api -m 10 POST "$U/api/reindex" '{}' >/dev/null || bad "the index did not start"
search_ok() { poll "$1" "/api/search?q=lemon" 'any(x.get("name") == "cake.txt" for x in (d.get("results") or d.get("hits") or []))'; }
if [ "${SEARCH:-1}" = 1 ]; then search_ok 180 && ok "the folder is indexed and searchable" || bad "search found nothing"; fi
data_ok() {   # data_ok WHEN
  api -m 10 GET "$U/api/settings" | json 'd["saved"].get("appName") == "Bridge Check" and d["saved"].get("theme") == "dark"' && ok "$1: settings kept" || bad "$1: settings lost"
  api -m 10 GET "$U/api/kb/tags?path=notes/design.md" | json '"keep-me" in str(d)' && ok "$1: the label kept" || bad "$1: the label lost"
  api -m 10 GET "$U/api/libraries" | json '"second" in str(d)' && ok "$1: the second library kept" || bad "$1: the second library lost"
  api -m 10 GET "$U/api/chat/conversations" | json '"kept across the update" in str(d)' && ok "$1: the conversation kept" || bad "$1: the conversation lost"
  if [ "${SEARCH:-1}" = 1 ]; then search_ok 120 && ok "$1: search still finds the file" || bad "$1: search finds nothing"; fi
}
data_ok "on $OLD_VERSION"

# ---- 3. the Go version, through the old updater ----

echo "Update to the Go version"
mkdir -p "$T/stage/x"
tar -xzf "$NEW_ARCHIVE" -C "$T/stage/x"
NEWRES="$T/stage/x/$NAME.app/Contents/Resources"
"$NODE" "$MJS" stamp "$NEWRES" "$NEW_VERSION" "$T/keys/update.pub"
grep -q '"server": *"go"' "$NEWRES/manifest.json" || bad "the new app's manifest does not say server go"
grep -q '"window": *"wails"' "$NEWRES/manifest.json" && ok "the new app's window is the Wails one" || bad "the new app's window is not the Wails one"
# The stamp changed files under the seal; sign it again ad hoc, as
# build_app.sh signs it.
codesign --force --deep --sign - "$T/stage/x/$NAME.app" >/dev/null 2>&1 || bad "codesign could not sign the stamped app"
(cd "$T/stage/x" && tar -czf "$T/stage/activate-studio-macos-arm64.tar.gz" "$NAME.app")
rm -rf "$T/stage/x"
"$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW_VERSION" "$T/stage/activate-studio-macos-arm64.tar.gz" "$T/keys/update.key"
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check.json" || true
if json "d['available'] and d['latest']['version'] == '$NEW_VERSION' and d['canInstall']" <"$T/check.json"; then
  ok "$OLD_VERSION's updater offers $NEW_VERSION and may install it"
else bad "the check:"; cut -c1-400 "$T/check.json" | sed 's/^/        /'; fi
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install request was refused"
if wait_version "$NEW_VERSION" 300; then ok "the server answers as $NEW_VERSION"; else bad "the server did not come back as $NEW_VERSION"; log_tail; fi
sleep 2
server_is go && ok "and it is the Go server" || bad "the server is not the Go server"
check_exe() { "$PY" -I -c 'import plistlib, sys; print(plistlib.load(open(sys.argv[1], "rb"))["CFBundleExecutable"])' "$APP/Contents/Info.plist"; }
file -b "$APP/Contents/MacOS/$(check_exe)" | grep -q 'Mach-O' && ok "the installed app's executable is a program (the Wails window)" || bad "the installed app's executable is not a program"
codesign --verify --deep --strict "$APP" >/dev/null 2>&1 && ok "the installed app's signature verifies" || bad "the installed app's signature does not verify"
api -m 10 GET "$U/api/updates" | json "d.get('previous') == {'version': '$OLD_VERSION'}" && ok "the Go updater offers the rollback" || bad "the Go updater offers no rollback"
data_ok "on $NEW_VERSION"

# ---- 4. back to the Node version ----

echo "Roll back"
api -m 60 POST "$U/api/updates/rollback" '{}' >/dev/null || bad "the rollback was refused"
if wait_version "$OLD_VERSION" 300; then ok "the server answers as $OLD_VERSION again"; else bad "the server did not come back as $OLD_VERSION"; log_tail; fi
sleep 2
server_is node && ok "with the Node server" || bad "the server is not Node's after the rollback"
data_ok "after the rollback"

echo
if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed"; exit 1; fi
echo "All checks passed"
