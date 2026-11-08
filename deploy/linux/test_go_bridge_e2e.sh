#!/usr/bin/env bash
# End to end check of the move from a released Node payload to the Go
# version through the ordinary update (docs/UPDATES.md, "The Go version"),
# run as a plain user with a scratch HOME:
#
# 1. The released payload (OLD, such as v1.188) is installed per user with its
#    own install.sh, verifying updates with a throwaway key made here, and
#    started: by its run loop, or with WINDOW=1 by its own window shell on
#    DISPLAY.
# 2. Settings, a label, a second library and a conversation are made through
#    its API, and the folder is indexed.
# 3. A stand-in release server on 127.0.0.1 (the shape of GitHub's releases
#    API; nothing is published anywhere) offers NEW, the Go payload, stamped
#    as the next version and signed with that key. The old server's own
#    updater checks, downloads, verifies and swaps it in, and the server
#    comes back as the Go server, with the data intact.
# 4. Rolling back from the Go version puts OLD back, with the data intact.
#
# No real key, no release and no network beyond 127.0.0.1 is used. The old
# payload's own Node and Python run the helpers.
#
# Usage: deploy/linux/test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE
#   PORT, FEED_PORT  the Studio's port and the release server's (default 6024, 6025)
#   WINDOW=1         start the old window shell (bin/activate-studio) on DISPLAY
#   KEEP=1           leave the scratch folder (its path is printed)
#   SEARCH=0         skip the search checks (a Go payload whose index is not ported yet)
#   SHOT=PROGRAM     with WINDOW=1, run PROGRAM SCRATCH NAME for a screenshot before and after the reopen
set -euo pipefail

OLD_ARCHIVE="$(cd "$(dirname "${1:?usage: test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE}")" && pwd)/$(basename "$1")"
NEW_ARCHIVE="$(cd "$(dirname "${2:?usage: test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE}")" && pwd)/$(basename "$2")"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PORT="${PORT:-6024}"
FEED_PORT="${FEED_PORT:-6025}"
T="$(mktemp -d "${TMPDIR:-/tmp}/go-bridge-e2e.XXXXXX")"
H="$T/home"
LIB="$H/.local/lib"
APP="$LIB/activate-studio"
DATA="$H/.local/share/activate-studio-data"
KB="$T/kb"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null || true; done
  sleep 2
  for p in ${pids[@]+"${pids[@]}"}; do kill -KILL -- "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null || true; done
  if [ "${KEEP:-}" = 1 ]; then echo "kept $T"; else chmod -R u+w "$T" 2>/dev/null || true; rm -rf "$T"; fi
}
trap cleanup EXIT
mkdir -p "$H" "$KB/notes" "$T/old" "$T/rel" "$T/keys" "$T/stage"
printf 'a recipe for lemon cake\n' >"$KB/cake.txt"
printf 'the index is a tree of per-directory databases\n' >"$KB/notes/design.md"

# The old payload, unpacked once for its helpers (Node and Python).
tar -xzf "$OLD_ARCHIVE" -C "$T/old"
OLD="$T/old/activate-studio"
NODE="$OLD/node/bin/node"
PY="$OLD/python/bin/python3"
MJS="$HERE/update_e2e.mjs"
OLD_VERSION="$("$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$OLD/app/build-info.json")"
NEW_VERSION="${NEW_VERSION:-$("$PY" -I -c 'import sys; m, n = sys.argv[1].lstrip("v").split(".")[:2]; print(f"v{m}.{int(n) + 1}")' "$OLD_VERSION")}"
echo "Go version bridge: $OLD_VERSION (Node) to $NEW_VERSION (Go), in $T"

api() { "$PY" -I "$HERE/api.py" "$@"; }
json() { "$PY" -I -c 'import json, sys; d = json.load(sys.stdin); sys.exit(0 if eval(sys.argv[1]) else 1)' "$1"; }
field() { "$PY" -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
U="http://127.0.0.1:$PORT"
# The version running: /api/version, or where that is not served (a Go
# server whose version route is not ported yet), the updater's own report.
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
log_tail() { grep -v -e 'incoming request' -e 'request completed' "$DATA/logs/studio.log" 2>/dev/null | tail -n 15 | cut -c1-240 | sed 's/^/        /'; }
# Which server answers: the Go one runs app/bin/studio-server.
server_is() {   # server_is go|node
  local p found=
  for p in /proc/[0-9]*; do
    [ "$(stat -c %u "$p" 2>/dev/null)" = "$(id -u)" ] || continue
    tr '\0' ' ' <"$p/cmdline" 2>/dev/null | grep -q "^$APP/app/bin/studio-server\|^$APP/node/bin/node $APP/app/server/dist/main.js" || continue
    if tr '\0' ' ' <"$p/cmdline" | grep -q 'studio-server'; then found=go; elif [ -z "$found" ]; then found=node; fi
  done
  [ "$found" = "$1" ]
}

# ---- 1. the released payload, installed per user ----

"$NODE" "$MJS" keygen "$T/keys"
"$NODE" "$MJS" stamp "$OLD" "$OLD_VERSION" "$T/keys/update.pub"
env -i HOME="$H" PATH=/usr/bin:/bin /bin/bash "$OLD/install.sh" >"$T/install.log" 2>&1 || { cat "$T/install.log"; exit 1; }
[ "$(cut -d' ' -f1 "$APP/install-kind")" = user ] && ok "$OLD_VERSION installed per user with its own install.sh" || bad "install-kind is not user"
mkdir -p "$DATA"
printf "PORT='%s'\nKB_ROOT='%s'\nSTUDIO_UPDATE_URL='http://127.0.0.1:%s'\n" "$PORT" "$KB" "$FEED_PORT" >"$DATA/studio.env"
"$NODE" "$MJS" serve "$T/rel" "$FEED_PORT" &
pids+=($!)
if [ "${WINDOW:-}" = 1 ]; then
  [ -n "${DISPLAY:-}" ] || { echo "WINDOW=1 needs DISPLAY" >&2; exit 2; }
  setsid env -i HOME="$H" PATH=/usr/bin:/bin DISPLAY="$DISPLAY" XAUTHORITY="${XAUTHORITY:-}" \
    XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
    STUDIO_DATA_DIR="$DATA" "$APP/bin/activate-studio" >"$T/window.log" 2>&1 &
else
  setsid env -i HOME="$H" PATH=/usr/bin:/bin /bin/bash "$APP/studio-run.sh" "$DATA" >/dev/null 2>&1 &
fi
pids+=($!)
if wait_version "$OLD_VERSION" 120; then ok "$OLD_VERSION runs"; else bad "$OLD_VERSION did not start"; log_tail; exit 1; fi
server_is node && ok "with the Node server" || bad "the server is not Node's"

# ---- 2. data ----

api -m 30 PUT "$U/api/settings" '{"appName": "Bridge Check", "theme": "dark"}' >/dev/null || bad "settings were not saved"
api -m 30 POST "$U/api/kb/tags" '{"paths": ["notes/design.md"], "add": ["keep-me"]}' >/dev/null || bad "the label was not applied"
mkdir -p "$T/second"
env -i PATH=/usr/bin:/bin "$APP/gufi/bin/gufi_dir2index" -x "$KB" "$T/second" >/dev/null 2>&1 || true
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

# ---- 3. the Go release, through the old updater ----

echo "Update to the Go version"
mkdir -p "$T/stage/x"
tar -xzf "$NEW_ARCHIVE" -C "$T/stage/x"
"$NODE" "$MJS" stamp "$T/stage/x/activate-studio" "$NEW_VERSION" "$T/keys/update.pub"
grep -q '"server": *"go"' "$T/stage/x/activate-studio/manifest.json" || bad "the new payload's manifest does not say server go"
MIN_GLIBC="$("$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["minGlibc"])' "$T/stage/x/activate-studio/manifest.json")"
export MIN_GLIBC
(cd "$T/stage/x" && tar --owner=0 --group=0 --numeric-owner --sort=name -I 'gzip -1' -cf "$T/stage/activate-studio-linux-x86_64.tar.gz" activate-studio)
rm -rf "$T/stage/x"
"$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW_VERSION" "$T/stage/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check.json" || true
if json "d['available'] and d['latest']['version'] == '$NEW_VERSION' and d['canInstall']" <"$T/check.json"; then
  ok "$OLD_VERSION's updater offers $NEW_VERSION and may install it"
else bad "the check:"; cut -c1-400 "$T/check.json" | sed 's/^/        /'; fi
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install request was refused"
if wait_version "$NEW_VERSION" 240; then ok "the server answers as $NEW_VERSION"; else bad "the server did not come back as $NEW_VERSION"; log_tail; fi
sleep 2
server_is go && ok "and it is the Go server" || bad "the server is not the Go server"
[ -d "$LIB/.activate-studio.previous" ] && ok "$OLD_VERSION is kept beside it" || bad "no previous payload"
api -m 10 GET "$U/api/updates" | json "d.get('previous') == {'version': '$OLD_VERSION'} and d.get('mode') == 'app'" && ok "the Go updater offers the rollback" || bad "the Go updater offers no rollback"
data_ok "on $NEW_VERSION"
if [ "${WINDOW:-}" = 1 ]; then
  # The old window is still the old program; the update changed it, so the
  # page offers Reopen now (and reopens by itself when the person started the
  # install there). The stand-in for that click: the old window ends and the
  # install's launcher starts the new one, as reopen.go does.
  api -m 10 GET "$U/api/updates" | json 'd.get("reopen") is True' && ok "the Go updater asks for the window to be reopened" || bad "no reopen asked for"
  [ -n "${SHOT:-}" ] && "${SHOT}" "$T" go-before-reopen || true
  old_shell="$(pgrep -u "$(id -u)" -f "^$APP/bin/activate-studio" | head -n 1 || true)"
  [ -n "$old_shell" ] && kill -TERM "$old_shell" 2>/dev/null
  for _ in $(seq 1 50); do [ -n "$old_shell" ] && kill -0 "$old_shell" 2>/dev/null || break; sleep 0.2; done
  setsid env -i HOME="$H" PATH=/usr/bin:/bin DISPLAY="$DISPLAY" XAUTHORITY="${XAUTHORITY:-}" \
    XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
    STUDIO_DATA_DIR="$DATA" "$APP/bin/activate-studio" >>"$T/window.log" 2>&1 &
  pids+=($!)
  if wait_version "$NEW_VERSION" 120; then ok "the reopened window starts $NEW_VERSION"; else bad "the reopened window did not start the server"; log_tail; fi
  w=
  for _ in $(seq 1 30); do
    for p in $(pgrep -u "$(id -u)" -f "activate-studio-wk4" || true); do
      case "$(readlink "/proc/$p/exe")" in "$APP/bin/"*) w="$p" ;; esac
    done
    [ -n "$w" ] && break
    sleep 1
  done
  if [ -n "$w" ]; then ok "the window is the new payload's shell ($(readlink "/proc/$w/exe"))"; else bad "no window from the new payload"; fi
  sleep 2
  server_is go && ok "under it, the Go server" || bad "the reopened window's server is not the Go server"
  data_ok "in the reopened window"
  [ -n "${SHOT:-}" ] && "${SHOT}" "$T" go-after-reopen || true
fi

# ---- 4. back to the Node version ----

echo "Roll back"
api -m 60 POST "$U/api/updates/rollback" '{}' >/dev/null || bad "the rollback was refused"
if wait_version "$OLD_VERSION" 240; then ok "the server answers as $OLD_VERSION again"; else bad "the server did not come back as $OLD_VERSION"; log_tail; fi
sleep 2
server_is node && ok "with the Node server" || bad "the server is not Node's after the rollback"
data_ok "after the rollback"

echo
if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed"; exit 1; fi
echo "All checks passed"
