#!/usr/bin/env bash
# End to end check, on a Mac, of the move from a released macOS app (the
# Swift window and the Node server) to the Go version (the Wails window and
# the Go server) through the ordinary update (docs/UPDATES.md, "The Go
# version"), as deploy/linux/test_go_bridge_e2e.sh does for Linux, run as
# the user with a scratch HOME:
#
# 1. The released app (OLD_ARCHIVE, activate-studio-macos-arm64.tar.gz) is
#    put in HOME/Applications, made to verify updates with a throwaway key,
#    and its server started the way START says.
# 2. Settings, a label, a second library and a conversation are made through
#    its API, and the folder is indexed.
# 3. A stand-in release server on 127.0.0.1 offers a Go build whose server
#    exits at once, stamped as the next version and signed with that key.
#    The old updater installs it, and the run loop puts OLD back after three
#    failed starts, with the data intact.
# 4. It then offers NEW_ARCHIVE (the Go version), stamped as the version
#    after. The old server's own updater checks, downloads, verifies and
#    swaps it in, and the server comes back as the Go server, with the data
#    intact, the page asked to offer Reopen, and no Roll back to OLD.
# 5. The copy of OLD kept beside the app goes after the first clean start.
#
# Nothing is signed with a real key, nothing is published, and no network
# beyond 127.0.0.1 is used. The old app's own Node and Python run the
# helpers.
#
# Usage: deploy/mac/test_go_bridge_e2e.sh OLD_ARCHIVE NEW_ARCHIVE
#   START            how the old server starts (default detached):
#                      detached  its run loop, as launcher.sh starts it where no LaunchAgent
#                                can be loaded (STUDIO_SUPERVISOR=detached, v1.170 and later):
#                                an update restarts it through exit 76
#                      launcher  the old app's own launcher.sh, which loads its LaunchAgent
#                                (com.parallelworks.activate-studio, in the gui domain, with
#                                the plist in the scratch HOME; booted out at the end), or
#                                from v1.170 runs detached where it cannot: an update
#                                restarts it through launchctl kickstart
#                      loop      its run loop with no LaunchAgent and no STUDIO_SUPERVISOR,
#                                as a release before v1.170 runs when launchctl kickstart
#                                fails: an update restarts it through exit 75, and the old
#                                loop runs the new copy's stand-in node/bin/node
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
START="${START:-detached}"
LABEL=com.parallelworks.activate-studio
SERVICE="gui/$(id -u)/$LABEL"
cleanup() {
  if [ "$START" = launcher ]; then launchctl bootout "$SERVICE" 2>/dev/null || true; fi
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
next_minor() { "$PY" -I -c 'import sys; m, n = sys.argv[1].lstrip("v").split(".")[:2]; print(f"v{m}.{int(n) + 1}")' "$1"; }
CRASH_VERSION="${CRASH_VERSION:-$(next_minor "$OLD_VERSION")}"
NEW_VERSION="${NEW_VERSION:-$(next_minor "$CRASH_VERSION")}"
echo "Go version bridge on macOS: $OLD_VERSION (Swift and Node) to $NEW_VERSION (Wails and Go), started by $START, in $T"

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
server_is() {   # server_is go|node: what listens on the Studio's port
  local p cmd go=0 node=0 seen=
  for p in $(lsof -nP -a -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null || true); do
    cmd="$(ps -o command= -p "$p" 2>/dev/null || true)"
    seen="$seen
        $p $cmd"
    case "$cmd" in
      *"/app/bin/studio-server"*) go=1 ;;
      *"/node/bin/node"*) node=1 ;;
    esac
  done
  if [ "$1" = go ] && [ "$go" = 1 ]; then return 0; fi
  if [ "$1" = node ] && [ "$node" = 1 ] && [ "$go" = 0 ]; then return 0; fi
  echo "        listening on $PORT:${seen:- nothing}"
  return 1
}

# ---- 1. the released app ----

ditto "$T/old/$NAME.app" "$APP"
"$NODE" "$MJS" keygen "$T/keys"
"$NODE" "$MJS" stamp "$RES" "$OLD_VERSION" "$T/keys/update.pub"
printf "PORT='%s'\nKB_ROOT='%s'\nSTUDIO_UPDATE_URL='http://127.0.0.1:%s'\n" "$PORT" "$KB" "$FEED_PORT" >"$DATA/studio.env"
"$NODE" "$MJS" serve "$T/rel" "$FEED_PORT" &
pids+=($!)
case "$START" in
  detached|loop)
    # As launcher.sh starts it where no LaunchAgent is loaded (detached:
    # STUDIO_APP_BUNDLE and STUDIO_SUPERVISOR=detached, so an update
    # restarts it through exit 76, which runs the new copy's own
    # studio-run.sh); loop: without STUDIO_SUPERVISOR, as a release before
    # v1.170 runs when launchctl kickstart fails, so an update restarts it
    # through exit 75 and the old loop runs the new copy's node/bin/node.
    sup=
    [ "$START" = detached ] && sup=detached
    env -i HOME="$H" PATH=/usr/bin:/bin:/usr/sbin:/sbin STUDIO_APP_BUNDLE="$APP" ${sup:+STUDIO_SUPERVISOR=$sup} \
      /bin/bash "$RES/studio-run.sh" "$DATA" >/dev/null 2>&1 &
    pids+=($!) ;;
  launcher)
    # The old app's own launcher.sh, as its window runs it: the LaunchAgent
    # where it can be loaded, else (from v1.170) the detached run loop.
    launchctl bootout "$SERVICE" 2>/dev/null || true
    if env -i HOME="$H" PATH=/usr/bin:/bin:/usr/sbin:/sbin STUDIO_NO_BROWSER=1 /bin/bash "$RES/launcher.sh" >"$T/launcher.log" 2>&1; then
      ok "$OLD_VERSION's launcher.sh started the server: $(tail -n 1 "$T/launcher.log" | cut -c1-120)"
    else
      bad "$OLD_VERSION's launcher.sh failed:"; tail -n 8 "$T/launcher.log" | sed 's/^/        /'
      tail -n 5 "$DATA/logs/agent.log" 2>/dev/null | sed 's/^/        /' || true
    fi
    if launchctl print "$SERVICE" >/dev/null 2>&1; then
      ok "the LaunchAgent $LABEL is loaded in gui/$(id -u)"
    else
      echo "  note  no LaunchAgent is loaded: $(grep -h 'no LaunchAgent' "$DATA/logs/agent.log" 2>/dev/null | tail -n 1 | cut -c1-200)"
    fi ;;
  *) echo "START is detached, launcher or loop" >&2; exit 2 ;;
esac
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

# ---- 3. a Go build that never starts: the run loop puts OLD back ----

# go_build VERSION [crash]: NEW_ARCHIVE stamped as VERSION and signed again
# ad hoc (the stamp changed files under the seal, as build_app.sh signs
# it), in $T/stage/VERSION/; crash: its server exits at once, every time.
go_build() {
  local d="$T/stage/$1"
  mkdir -p "$d/x"
  tar -xzf "$NEW_ARCHIVE" -C "$d/x"
  "$NODE" "$MJS" stamp "$d/x/$NAME.app/Contents/Resources" "$1" "$T/keys/update.pub"
  if [ "${2:-}" = crash ]; then printf '#!/bin/sh\nexit 1\n' >"$d/x/$NAME.app/Contents/Resources/app/bin/studio-server"; fi
  codesign --force --deep --sign - "$d/x/$NAME.app" >/dev/null 2>&1 || bad "codesign could not sign the stamped app"
  (cd "$d/x" && tar -czf "$d/activate-studio-macos-arm64.tar.gz" "$NAME.app")
  rm -rf "$d/x"
}
UPD="$DATA/updates"
app_version() { "$PY" -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$RES/app/build-info.json" 2>/dev/null; }

echo "A Go build that never starts ($CRASH_VERSION)"
go_build "$CRASH_VERSION" crash
"$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$CRASH_VERSION" "$T/stage/$CRASH_VERSION/activate-studio-macos-arm64.tar.gz" "$T/keys/update.key"
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check0.json" || true
json "d['available'] and d['latest']['version'] == '$CRASH_VERSION' and d['canInstall']" <"$T/check0.json" &&
  ok "$OLD_VERSION's updater offers $CRASH_VERSION and may install it" || { bad "the check:"; cut -c1-400 "$T/check0.json" | sed 's/^/        /'; }
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install of $CRASH_VERSION was refused"
"$NODE" "$MJS" remove-release "$T/rel" "$CRASH_VERSION"
# The server answers as OLD until the swap: wait for the swap (pending.json,
# or the crashing copy in place), then for the run loop's note.
n=0; until [ -f "$UPD/pending.json" ] || [ "$(app_version)" = "$CRASH_VERSION" ] || [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 240 ]; do sleep 1; n=$((n + 1)); done
n=0; until [ -f "$UPD/rolled-back.json" ] || [ "$n" -ge 300 ]; do sleep 1; n=$((n + 1)); done
if wait_version "$OLD_VERSION" 300 && [ "$(app_version)" = "$OLD_VERSION" ]; then
  ok "after three failed starts the run loop put $OLD_VERSION back"
else bad "$OLD_VERSION was not put back (the app is $(app_version))"; log_tail; fi
grep -q "\"$CRASH_VERSION\"" "$UPD/rolled-back.json" 2>/dev/null && ok "rolled-back.json says $CRASH_VERSION did not start" || bad "rolled-back.json: $(cat "$UPD/rolled-back.json" 2>/dev/null)"
grep -h "keeps stopping\|stopped .* times\|is back" "$DATA/logs/studio.log" 2>/dev/null | tail -n 1 | cut -c1-200 | sed 's/^/  note  /' || true
sleep 2
server_is node && ok "with the Node server" || bad "the server is not Node's after the run loop's rollback"
data_ok "after the run loop's rollback"

# ---- 4. the Go version, through the old updater ----

echo "Update to the Go version ($NEW_VERSION)"
go_build "$NEW_VERSION"
mkdir -p "$T/stage/peek"
tar -xzf "$T/stage/$NEW_VERSION/activate-studio-macos-arm64.tar.gz" -C "$T/stage/peek" "$NAME.app/Contents/Resources/manifest.json"
grep -q '"server": *"go"' "$T/stage/peek/$NAME.app/Contents/Resources/manifest.json" || bad "the new app's manifest does not say server go"
grep -q '"window": *"wails"' "$T/stage/peek/$NAME.app/Contents/Resources/manifest.json" && ok "the new app's window is the Wails one" || bad "the new app's window is not the Wails one"
"$NODE" "$MJS" add-release "$T/rel" "$FEED_PORT" "$NEW_VERSION" "$T/stage/$NEW_VERSION/activate-studio-macos-arm64.tar.gz" "$T/keys/update.key"
api -m 60 POST "$U/api/updates/check" '{}' >"$T/check.json" || true
if json "d['available'] and d['latest']['version'] == '$NEW_VERSION' and d['canInstall']" <"$T/check.json"; then
  ok "$OLD_VERSION's updater offers $NEW_VERSION and may install it"
else bad "the check:"; cut -c1-400 "$T/check.json" | sed 's/^/        /'; fi
api -m 30 POST "$U/api/updates/install" '{}' >/dev/null || bad "the install request was refused"
if wait_version "$NEW_VERSION" 300; then ok "the server answers as $NEW_VERSION"; else bad "the server did not come back as $NEW_VERSION"; log_tail; fi
sleep 2
server_is go && ok "and it is the Go server" || bad "the server is not the Go server"
case "$START" in
  loop) grep -q "studio-run.sh --serve" "$DATA/logs/studio.log" && ok "started by $OLD_VERSION's run loop through the stand-in node/bin/node" || bad "the log does not show the stand-in starting the Go server" ;;
esac
if [ "$START" = launcher ] && launchctl print "$SERVICE" >/dev/null 2>&1; then
  launchctl print "$SERVICE" 2>/dev/null | grep -q "$RES/studio-run.sh" && ok "under the LaunchAgent, which runs the new copy's studio-run.sh" || bad "the LaunchAgent does not run the app's studio-run.sh"
fi
check_exe() { "$PY" -I -c 'import plistlib, sys; print(plistlib.load(open(sys.argv[1], "rb"))["CFBundleExecutable"])' "$APP/Contents/Info.plist"; }
file -b "$APP/Contents/MacOS/$(check_exe)" | grep -q 'Mach-O' && ok "the installed app's executable is a program (the Wails window)" || bad "the installed app's executable is not a program"
codesign --verify --deep --strict "$APP" >/dev/null 2>&1 && ok "the installed app's signature verifies" || bad "the installed app's signature does not verify"
# The old updater marks the window changed (updates/reopen): the Go page
# offers Reopen now in Settings, Updates, and in the Updated notice.
[ -f "$UPD/reopen" ] && api -m 10 GET "$U/api/updates" | json 'd.get("reopen") is True' &&
  ok "the page is asked to offer Reopen (updates/reopen names $(cat "$UPD/reopen"))" || bad "no reopen: $(api -m 10 GET "$U/api/updates" | field 'd.get("reopen")')"
# The old Swift window keeps showing the Studio until it is reopened: the
# page it loads is the Go server's.
curl -fsS -m 10 "$U/" | grep -qi '<html' && ok "the Go server serves the page the old window shows" || bad "the Go server does not serve the page"
api -m 10 GET "$U/api/updates" | json "d.get('previous') is None" && ok "the Go updater offers no Roll back to $OLD_VERSION" || bad "the Go updater offers a rollback: $(api -m 10 GET "$U/api/updates" | field 'd.get("previous")')"
code="$(curl -s -o "$T/rollback.json" -w '%{http_code}' -m 30 -X POST -H 'content-type: application/json' -d '{}' "$U/api/updates/rollback" || true)"
[ "$code" = 409 ] && grep -q "runs the Node server" "$T/rollback.json" && ok "and refuses one: $(cut -c1-120 "$T/rollback.json")" || bad "the rollback request: $code $(cut -c1-200 "$T/rollback.json")"
data_ok "on $NEW_VERSION"

# ---- 5. the copy kept beside it goes after the first clean start ----

echo "After the first clean start"
PREV="$H/Applications/.$NAME.app.previous"
n=0
while [ -d "$PREV" ] && [ "$n" -lt 240 ]; do sleep 1; n=$((n + 1)); done
if [ ! -d "$PREV" ]; then
  ok "the copy of $OLD_VERSION is removed after the clean start (${n}s)"
  [ ! -e "$UPD/pending.json" ] && ok "and only once pending.json was cleared" || bad "the copy was removed while pending.json was there"
else
  bad "the copy of $OLD_VERSION is still there after ${n}s"; log_tail
fi
grep -q "\"$OLD_VERSION\"" "$UPD/previous.json" 2>/dev/null && ok "updates/previous.json names $OLD_VERSION" || bad "previous.json: $(cat "$UPD/previous.json" 2>/dev/null)"
api -m 10 GET "$U/api/updates" | json "d.get('previous') is None" && ok "still no Roll back to $OLD_VERSION" || bad "a rollback is offered after the clean start"
data_ok "after the copy was removed"

echo
if [ "$failures" -gt 0 ]; then echo "$failures check(s) failed"; exit 1; fi
echo "All checks passed"
