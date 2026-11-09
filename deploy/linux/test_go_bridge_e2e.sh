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
#    API; nothing is published anywhere) offers a Go payload whose server
#    exits at once, stamped as the next version and signed with that key.
#    The old updater installs it, and the run loop puts OLD back after three
#    failed starts, with the data intact.
# 4. It then offers NEW, the Go payload, stamped as the version after. The
#    old server's own updater checks, downloads, verifies and swaps it in,
#    and the server comes back as the Go server, with the data intact and
#    no Roll back to OLD.
# 5. The copy of OLD goes after the first clean start; then, on Stable, a
#    Go version that never starts is put back by the run loop, and on
#    Preview, Roll back goes to the earlier Go Preview by download.
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
#   CHANNEL=preview  publish the Go build as a Preview release (a GitHub prerelease whose signed
#                    manifest says channel preview) to an install set to take prereleases; the
#                    default, stable, publishes it as Stable and adds a newer prerelease that the
#                    Stable install must not offer, before or after the move
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
next_minor() { "$PY" -I -c 'import sys; m, n = sys.argv[1].lstrip("v").split(".")[:2]; print(f"v{m}.{int(n) + 1}")' "$1"; }
CRASH0="${CRASH_VERSION:-$(next_minor "$OLD_VERSION")}"
NEW_VERSION="${NEW_VERSION:-$(next_minor "$CRASH0")}"
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
serve_feed() { "$NODE" "$MJS" serve "$T/rel" "$FEED_PORT" & FEED_PID=$!; pids+=("$FEED_PID"); sleep 1; }
serve_feed
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

# ---- 3. a Go payload that never starts, through the old updater ----

mkdir -p "$T/stage/x"
tar -xzf "$NEW_ARCHIVE" -C "$T/stage/x"
"$NODE" "$MJS" stamp "$T/stage/x/activate-studio" "$NEW_VERSION" "$T/keys/update.pub"
grep -q '"server": *"go"' "$T/stage/x/activate-studio/manifest.json" || bad "the new payload's manifest does not say server go"
MIN_GLIBC="$("$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["minGlibc"])' "$T/stage/x/activate-studio/manifest.json")"
export MIN_GLIBC
(cd "$T/stage/x" && tar --owner=0 --group=0 --numeric-owner --sort=name -I 'gzip -1' -cf "$T/stage/activate-studio-linux-x86_64.tar.gz" activate-studio)
rm -rf "$T/stage/x"
# go_release VERSION [crash]: the Go payload stamped as VERSION, in
# $T/stage/VERSION/; crash: its server exits at once, every time.
go_release() {
  local d="$T/stage/$1"
  mkdir -p "$d/x"
  tar -xzf "$NEW_ARCHIVE" -C "$d/x"
  "$NODE" "$MJS" stamp "$d/x/activate-studio" "$1" "$T/keys/update.pub"
  if [ "${2:-}" = crash ]; then printf '#!/bin/sh\nexit 1\n' >"$d/x/activate-studio/app/bin/studio-server"; fi
  (cd "$d/x" && tar --owner=0 --group=0 --numeric-owner --sort=name -I 'gzip -1' -cf "$d/activate-studio-linux-x86_64.tar.gz" activate-studio)
  rm -rf "$d/x"
}
bump() { "$PY" -I -c 'import sys; p = sys.argv[1].lstrip("v").split("."); p = p + ["0"] if len(p) == 2 else p; p[2] = str(int(p[2]) + 1); print("v" + ".".join(p))' "$1"; }
UPD="$DATA/updates"
app_version() { "$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$APP/app/build-info.json" 2>/dev/null; }
GGUF="$DATA/index/models/minilm384.gguf"
[ -L "$GGUF" ] && had_gguf=1 || had_gguf=''

CHANNEL="${CHANNEL:-stable}"
if [ "$CHANNEL" = preview ]; then
  # The setting as the old version names it; the Go updater reads it as preview.
  api -m 30 PUT "$U/api/settings" '{"updates": {"channel": "prerelease"}}' >/dev/null || bad "the channel was not saved"
fi

# A Go payload that never starts, on the channel: the old version's updater
# installs it, and the run loop puts the old version back.
echo "A Go payload that never starts ($CRASH0)"
go_release "$CRASH0" crash
if [ "$CHANNEL" = preview ]; then
  PRERELEASE=1 CHANNEL=preview "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$CRASH0" "$T/stage/$CRASH0/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
else
  CHANNEL=stable "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$CRASH0" "$T/stage/$CRASH0/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
fi
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check-crash.json" || true
json "d['available'] and d['latest']['version'] == '$CRASH0' and d['canInstall']" <"$T/check-crash.json" &&
  ok "$OLD_VERSION's updater offers $CRASH0 and may install it" || { bad "the check:"; cut -c1-400 "$T/check-crash.json" | sed 's/^/        /'; }
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install of $CRASH0 was refused"
"$NODE" "$MJS" remove-release "$T/rel" "$CRASH0"
n=0; until [ -f "$UPD/pending.json" ] || [ "$(app_version)" = "$CRASH0" ] || [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 240 ]; do sleep 1; n=$((n + 1)); done
n=0; until [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 300 ]; do sleep 1; n=$((n + 1)); done
if wait_version "$OLD_VERSION" 300 && [ "$(app_version)" = "$OLD_VERSION" ]; then ok "after three failed starts the run loop put $OLD_VERSION back"; else bad "$OLD_VERSION was not put back (the payload is $(app_version))"; log_tail; fi
grep -q "\"$CRASH0\"" "$UPD/rolled-back.json" 2>/dev/null && ok "rolled-back.json says $CRASH0 did not start" || bad "rolled-back.json: $(cat "$UPD/rolled-back.json" 2>/dev/null)"
sleep 2
server_is node && ok "with the Node server" || bad "the server is not Node's after the run loop's rollback"
[ -x "$APP/node/bin/node" ] && [ ! -e "$APP/node/PLACEHOLDER" ] && ok "a whole Node install, not the stand-ins" || bad "the install after the run loop's rollback is not the Node payload"
data_ok "after the run loop's rollback"
# Read; the checks below wait for the next one.
rm -f "$UPD/rolled-back.json"

# ---- 4. the Go release, through the old updater ----

echo "Update to the Go version ($NEW_VERSION)"
if [ "$CHANNEL" = preview ]; then
  PRERELEASE=1 CHANNEL=preview "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW_VERSION" "$T/stage/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
else
  # A Preview release, which a Stable install never takes: by default one
  # newer than the Stable release below; PRE_VERSION=v1.188.1 is the kind
  # deploy/release_preview.sh publishes, between two Stable releases. It is
  # published first, alone, and the old version's updater must not offer it.
  PRE_VERSION="${PRE_VERSION:-$("$PY" -I -c 'import sys; m, n = sys.argv[1].lstrip("v").split(".")[:2]; print(f"v{m}.{int(n) + 1}")' "$NEW_VERSION")}"
  PRERELEASE=1 CHANNEL=preview "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$PRE_VERSION" "$T/stage/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
  api -m 60 POST "$U/api/updates/check" '{}' >"$T/check0.json" || true
  json "not d['available'] and all(v['version'] != '$PRE_VERSION' for v in d.get('versions') or [])" <"$T/check0.json" &&
    ok "$OLD_VERSION on Stable is not offered the Preview $PRE_VERSION" || bad "$OLD_VERSION on Stable offers the Preview: $(cut -c1-300 "$T/check0.json")"
  CHANNEL=stable "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW_VERSION" "$T/stage/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
fi
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check.json" || true
if json "d['available'] and d['latest']['version'] == '$NEW_VERSION' and d['canInstall']" <"$T/check.json"; then
  ok "$OLD_VERSION's updater offers $NEW_VERSION and may install it"
else bad "the check:"; cut -c1-400 "$T/check.json" | sed 's/^/        /'; fi
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install request was refused"
if wait_version "$NEW_VERSION" 240; then ok "the server answers as $NEW_VERSION"; else bad "the server did not come back as $NEW_VERSION"; log_tail; fi
sleep 2
server_is go && ok "and it is the Go server" || bad "the server is not the Go server"
[ -d "$LIB/.activate-studio.previous" ] && [ -f "$UPD/pending.json" ] && ok "$OLD_VERSION is kept beside it until the first clean start" || bad "no previous payload, or no pending.json, right after the update"
api -m 10 GET "$U/api/updates" | json "d.get('previous') is None and d.get('mode') == 'app'" && ok "the Go updater offers no Roll back to $OLD_VERSION" || bad "the Go updater offers a rollback: $(api -m 10 GET "$U/api/updates" | field 'd.get("previous")')"
code="$(curl -s -o "$T/rollback.json" -w '%{http_code}' -m 30 -X POST -H 'content-type: application/json' -d '{}' "$U/api/updates/rollback" || true)"
[ "$code" = 409 ] && grep -q "runs the Node server" "$T/rollback.json" && ok "and refuses one: $(cut -c1-120 "$T/rollback.json")" || bad "the rollback request: $code $(cut -c1-200 "$T/rollback.json")"
data_ok "on $NEW_VERSION"
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check2.json" || true
if [ "$CHANNEL" = preview ]; then
  json "d['policy']['channel'] == 'preview'" <"$T/check2.json" && ok "the Go updater reads the old prerelease setting as Preview" || bad "the channel after the move: $(cut -c1-300 "$T/check2.json")"
else
  json "d['policy']['channel'] == 'stable' and not d['available'] and all(v['version'] != '$PRE_VERSION' for v in d.get('versions') or [])" <"$T/check2.json" &&
    ok "the Stable install does not take the newer Preview $PRE_VERSION" || bad "the Stable install offers a Preview release: $(cut -c1-300 "$T/check2.json")"
fi
if [ "${WINDOW:-}" = 1 ]; then
  # The old window is still the old program; the update changed it, so the
  # page offers Reopen now (and reopens by itself when the person started the
  # install there). The stand-in for that click: the old window ends and the
  # install's launcher starts the new one, as reopen.go does.
  api -m 10 GET "$U/api/updates" | json 'd.get("reopen") is True' && ok "the Go updater asks for the window to be reopened" || bad "no reopen asked for"
  [ -n "${SHOT:-}" ] && "${SHOT}" "$T" go-before-reopen || true
  # Every window process from before the reopen: the old program, whose
  # file is now under .activate-studio.previous.
  old_windows="$(pgrep -u "$(id -u)" -f "activate-studio-wk4" || true)"
  [ -n "$old_windows" ] || bad "no window was running before the reopen"
  left=
  for p in $old_windows; do kill -TERM "$p" 2>/dev/null || true; done
  for _ in $(seq 1 50); do
    left=; for p in $old_windows; do kill -0 "$p" 2>/dev/null && left=1; done
    [ -z "$left" ] && break; sleep 0.2
  done
  [ -z "$left" ] && ok "the old window ended" || bad "the old window is still running"
  setsid env -i HOME="$H" PATH=/usr/bin:/bin DISPLAY="$DISPLAY" XAUTHORITY="${XAUTHORITY:-}" \
    XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-}" DBUS_SESSION_BUS_ADDRESS="${DBUS_SESSION_BUS_ADDRESS:-}" \
    STUDIO_DATA_DIR="$DATA" "$APP/bin/activate-studio" >>"$T/window.log" 2>&1 &
  pids+=($!)
  if wait_version "$NEW_VERSION" 120; then ok "the reopened window starts $NEW_VERSION"; else bad "the reopened window did not start the server"; log_tail; fi
  # The window now running is a new process whose program is the very file
  # in the new payload (same device and inode), not the old one renamed.
  w=
  for _ in $(seq 1 30); do
    for p in $(pgrep -u "$(id -u)" -f "activate-studio-wk4" || true); do
      case " $old_windows " in *" $p "*) continue ;; esac
      exe="$(readlink "/proc/$p/exe" || true)"
      [ -n "$exe" ] && [ "${exe% (deleted)}" = "$exe" ] || continue
      want="$APP/bin/$(basename "$exe")"
      [ -f "$want" ] && [ "$(stat -L -c %d:%i "/proc/$p/exe" 2>/dev/null)" = "$(stat -c %d:%i "$want")" ] && w="$p"
    done
    [ -n "$w" ] && break
    sleep 1
  done
  if [ -n "$w" ]; then ok "the window is a new process running the new payload's shell ($(readlink "/proc/$w/exe"))"; else bad "no window running the new payload's shell:"; for p in $(pgrep -u "$(id -u)" -f "activate-studio-wk4" || true); do echo "        $p $(readlink "/proc/$p/exe")"; done; fi
  api -m 10 GET "$U/api/updates" | json 'd.get("reopen") is False' && ok "and Reopen now is no longer offered" || bad "the reopened window still offers Reopen now"
  sleep 2
  server_is go && ok "under it, the Go server" || bad "the reopened window's server is not the Go server"
  data_ok "in the reopened window"
  [ -n "${SHOT:-}" ] && "${SHOT}" "$T" go-after-reopen || true
fi

# ---- 5. the copy kept beside it goes after the first clean start ----

echo "After the first clean start"
# pending.json is cleared a minute after the start; the copy goes once no
# process runs from it (with WINDOW=1 the old window did until the reopen).
n=0
while [ -d "$LIB/.activate-studio.previous" ] && [ "$n" -lt 240 ]; do sleep 1; n=$((n + 1)); done
if [ ! -d "$LIB/.activate-studio.previous" ]; then
  ok "the copy of $OLD_VERSION is removed after the clean start (${n}s)"
  [ ! -e "$UPD/pending.json" ] && ok "and only once pending.json was cleared" || bad "the copy was removed while pending.json was there"
else
  bad "the copy of $OLD_VERSION is still there after ${n}s"; log_tail
fi
grep -q "\"$OLD_VERSION\"" "$UPD/previous.json" 2>/dev/null && ok "updates/previous.json names $OLD_VERSION" || bad "previous.json: $(cat "$UPD/previous.json" 2>/dev/null)"
api -m 10 GET "$U/api/updates" | json "d.get('previous') is None" && ok "still no Roll back to $OLD_VERSION" || bad "a rollback is offered after the clean start"
if [ -n "$had_gguf" ]; then
  n=0; while [ -L "$GGUF" ] && [ "$n" -lt 30 ]; do sleep 1; n=$((n + 1)); done
  [ ! -e "$GGUF" ] && [ ! -L "$GGUF" ] && ok "the Node version's model link in the data folder went with it" || bad "the dangling model link is still there"
fi
data_ok "after the copy was removed"

if [ "$CHANNEL" = preview ]; then
  # ---- 6. Go to an earlier Go Preview, by download ----
  NEW2="$(bump "$NEW_VERSION")"
  echo "Roll back from $NEW2 to the earlier Preview $NEW_VERSION"
  go_release "$NEW2"
  PRERELEASE=1 CHANNEL=preview "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW2" "$T/stage/$NEW2/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
  api -m 60 POST "$U/api/updates/check" '{}' >/dev/null || true
  api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install of $NEW2 was refused"
  wait_version "$NEW2" 240 && ok "$NEW2 runs" || { bad "$NEW2 did not start"; log_tail; }
  n=0; while [ -d "$LIB/.activate-studio.previous" ] && [ "$n" -lt 240 ]; do sleep 1; n=$((n + 1)); done
  [ ! -d "$LIB/.activate-studio.previous" ] && ok "the copy of $NEW_VERSION is removed after the clean start" || bad "the copy of $NEW_VERSION is still there"
  api -m 10 GET "$U/api/updates" | json "(d.get('previous') or {}).get('version') == '$NEW_VERSION' and (d['previous'].get('size') or 0) > 0" &&
    ok "Roll back offers $NEW_VERSION, a download of its size" || bad "previous: $(api -m 10 GET "$U/api/updates" | field 'd.get("previous")')"
  api -m 60 POST "$U/api/updates/rollback" '{}' >/dev/null || bad "the rollback was refused"
  wait_version "$NEW_VERSION" 240 && ok "the server answers as $NEW_VERSION again, downloaded" || { bad "the server did not come back as $NEW_VERSION"; log_tail; }
  sleep 2
  server_is go && ok "with the Go server" || bad "the server is not the Go server after the rollback"
  data_ok "after the rollback to $NEW_VERSION"
else
  # ---- 6. a Go version that never starts: the Go run loop puts the copy back ----
  CRASH="$(bump "$NEW_VERSION")"
  echo "A version that never starts ($CRASH)"
  go_release "$CRASH" crash
  CHANNEL=stable "$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$CRASH" "$T/stage/$CRASH/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key"
  api -m 60 POST "$U/api/updates/check" '{}' >/dev/null || true
  api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install of $CRASH was refused"
  "$NODE" "$MJS" remove-release "$T/rel" "$CRASH"
  # The swap keeps $NEW_VERSION beside it and writes pending.json; the run
  # loop then puts it back, which only that copy makes possible.
  # The server answers as $NEW_VERSION until the swap, so wait for the swap
  # first (pending.json, or $CRASH on disk), then for the run loop's note.
  n=0; until [ -f "$UPD/pending.json" ] || [ "$(app_version)" = "$CRASH" ] || [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 240 ]; do sleep 1; n=$((n + 1)); done
  n=0; until [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 300 ]; do sleep 1; n=$((n + 1)); done
  if wait_version "$NEW_VERSION" 300 && [ "$(app_version)" = "$NEW_VERSION" ]; then ok "after three failed starts the run loop put $NEW_VERSION back"; else bad "$NEW_VERSION was not put back"; log_tail; fi
  grep -q "\"$CRASH\"" "$UPD/rolled-back.json" 2>/dev/null && ok "rolled-back.json says $CRASH did not start" || bad "rolled-back.json: $(cat "$UPD/rolled-back.json" 2>/dev/null)"
  sleep 2
  server_is go && ok "with the Go server" || bad "the server is not the Go server after the run loop's rollback"
  data_ok "after the run loop's rollback"
fi

echo
if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed"; exit 1; fi
echo "All checks passed"
