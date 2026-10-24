#!/usr/bin/env bash
# Updates an installed ACTIVATE Studio in place, the way the app does it
# for a person: an older release is installed in ~/Applications and opened,
# then the Studio is asked to check for updates and install the newest one
# (POST /api/updates/check, then /api/updates/install), and followed until
# it has restarted on the new version. Checked after: the server reports the
# newest release, the copy in ~/Applications is that version and its
# signature verifies, the previous copy is kept beside it for a rollback,
# and the server runs as before (the LaunchAgent, or, with
# ~/Library/LaunchAgents read-only, the run loop server.pid names).
#
# Usage: mac-update-e2e.sh FROM_DMG_URL OUT_DIR [readonly-agents]
#
# OUT_DIR gets summary.txt (each step, timed), shots/*.png, updates.txt (the
# update status as it went), and on a failure the Studio's logs and its
# updates folder. A failed check ends the run with "FAIL: why".
set -uo pipefail

URL="$1"
OUT="$2"
READONLY="${3:-}"
APP_NAME="ACTIVATE Studio"
DEST="$HOME/Applications/$APP_NAME.app"
PREV="$HOME/Applications/.$APP_NAME.app.previous"
DATA="$HOME/Library/Application Support/$APP_NAME"
AGENT="$HOME/Library/LaunchAgents/com.parallelworks.activate-studio.plist"
API=http://127.0.0.1:4180
mkdir -p "$OUT/shots"
SUMMARY="$OUT/summary.txt"
T0=$(date +%s)
N=0

log() { printf '%4ss  %s\n' "$(( $(date +%s) - T0 ))" "$*" | tee -a "$SUMMARY"; }
shot() {
  N=$((N + 1))
  local f
  f="$(printf '%02d' "$N")-$1.png"
  if screencapture -x "$OUT/shots/$f" 2>/dev/null; then log "screenshot $f"; else log "screenshot $f could not be taken"; fi
}
collect() {
  [ -d "$DATA/logs" ] && cp -R "$DATA/logs" "$OUT/logs" 2>/dev/null
  [ -d "$DATA/updates" ] && cp -R "$DATA/updates" "$OUT/updates-folder" 2>/dev/null
  ls -la "$HOME/Applications" >"$OUT/applications.txt" 2>&1
  return 0
}
fail() { log "FAIL: $*"; shot failure; collect; exit 1; }
version() { curl -fsS --max-time 3 "$API/api/version" 2>/dev/null; }
field() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {"d": d}))' "$1" 2>/dev/null; }
bundle_version() { /usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$1/Contents/Info.plist" 2>/dev/null; }

log "runner: $(sw_vers -productName) $(sw_vers -productVersion), user $(id -un)"
if [ -n "$READONLY" ]; then
  mkdir -p "$HOME/Library/LaunchAgents"
  chmod 555 "$HOME/Library/LaunchAgents"
  log "~/Library/LaunchAgents is now read-only: $(stat -f '%Sp' "$HOME/Library/LaunchAgents")"
fi

# 1. The older release, installed in ~/Applications as a person would:
# copied from its disk image, and opened without the quarantine mark (as
# Install leaves it, and as the copy's own updates do).
DMG="$OUT/../from.dmg"
curl -fsSL -o "$DMG" "$URL" || fail "could not download $URL"
MNT="$(mktemp -d)"
hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$DMG" >/dev/null || fail "could not mount $DMG"
mkdir -p "$HOME/Applications"
ditto "$MNT/$APP_NAME.app" "$DEST" || fail "could not copy the app to $DEST"
hdiutil detach "$MNT" >/dev/null 2>&1 || true
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true
FROM="$(bundle_version "$DEST")"
log "installed $FROM in $DEST"

# GitHub answers unauthenticated API requests from a runner's shared address
# with HTTP 403 once its hourly limit is spent, which a person's own Mac,
# checking once an hour, does not meet. With GH_TOKEN set, the Studio's
# release check goes through a local proxy that adds the job's token
# (STUDIO_UPDATE_URL, read from studio.env); the request is otherwise the
# app's own, and the downloads go to GitHub directly.
if [ -n "${GH_TOKEN:-}" ]; then
  # The app's own Node (signed, so the macOS firewall lets it listen).
  "$DEST/Contents/Resources/node/bin/node" -e '
const http = require("node:http")
http.createServer(async (req, res) => {
  const h = { Authorization: "Bearer " + process.env.GH_TOKEN }
  for (const k of ["accept", "user-agent", "x-github-api-version", "if-none-match"]) if (req.headers[k]) h[k] = req.headers[k]
  try {
    const r = await fetch("https://api.github.com" + req.url, { headers: h })
    const body = Buffer.from(await r.arrayBuffer())
    const out = { "Content-Length": body.length }
    for (const k of ["content-type", "etag"]) if (r.headers.get(k)) out[k] = r.headers.get(k)
    res.writeHead(r.status, out); res.end(body)
    console.log(new Date().toISOString(), req.url, r.status)
  } catch (e) { res.writeHead(502); res.end(String(e)); console.log(new Date().toISOString(), req.url, "error", String(e)) }
}).listen(4199, "127.0.0.1", () => console.log("listening on 127.0.0.1:4199"))
' >"$OUT/api-proxy.log" 2>&1 &
  for _ in $(seq 1 20); do curl -fsS --max-time 10 -o /dev/null "http://127.0.0.1:4199/repos/parallelworks/activate-studio/releases?per_page=1" && break; sleep 1; done
  curl -fsS --max-time 10 -o /dev/null "http://127.0.0.1:4199/repos/parallelworks/activate-studio/releases?per_page=1" \
    || fail "the local API proxy does not answer: $(tr '\n' ' ' <"$OUT/api-proxy.log")"
  mkdir -p "$DATA"
  printf "PORT='4180'\nSTUDIO_UPDATE_URL='http://127.0.0.1:4199'\n" >"$DATA/studio.env"
  log "the release check goes through a local proxy with the job's token (GitHub's unauthenticated limit on shared runners); it answers"
fi

open "$DEST" &
v=
for _ in $(seq 1 120); do v="$(version)" && break; sleep 2; done
[ -n "$v" ] || fail "the server of $FROM did not answer"
log "the server answers: $v"
sleep 6
shot before-update
if [ -n "$READONLY" ]; then
  [ -e "$AGENT" ] && fail "a LaunchAgent was written although the folder is read-only"
  [ -s "$DATA/server.pid" ] || fail "no server.pid before the update"
  OLD_PID="$(head -1 "$DATA/server.pid")"
  log "runs without a LaunchAgent; server.pid $OLD_PID: $(ps -ww -o command= -p "$OLD_PID")"
else
  launchctl print "gui/$(id -u)/com.parallelworks.activate-studio" >/dev/null 2>&1 || fail "the LaunchAgent is not loaded"
  log "runs under the LaunchAgent"
fi

# 2. The update, as Update now asks for it.
s="$(curl -fsS -X POST "$API/api/updates/check")" || fail "the update check failed"
# A check the window's own status request started answers "checking" here
# without waiting; it is followed until it ends.
for _ in $(seq 1 45); do
  [ "$(echo "$s" | field 'd["state"]')" = checking ] || break
  sleep 2
  s="$(curl -fsS "$API/api/updates")" || fail "the update status could not be read"
done
echo "$s" >"$OUT/updates.txt"
[ "$(echo "$s" | field 'd["state"]')" = error ] && log "check error: $(echo "$s" | field 'd["error"]')"
LATEST="$(echo "$s" | field 'd["latest"]["version"] if d.get("latest") else ""')"
log "check: mode $(echo "$s" | field 'd["mode"]'), latest $LATEST, available $(echo "$s" | field 'd["available"]'), canInstall $(echo "$s" | field 'd["canInstall"]'), reason $(echo "$s" | field 'd["reason"]')"
[ "$(echo "$s" | field 'd["canInstall"]')" = True ] || fail "the update cannot be installed: $(echo "$s" | field 'd["reason"]')"
s="$(curl -fsS -X POST "$API/api/updates/install")" || fail "the install request failed"
echo "$s" >>"$OUT/updates.txt"
log "install requested: state $(echo "$s" | field 'd["state"]')"

# 3. Followed until the server answers with the new version.
last=
new=
for _ in $(seq 1 240); do
  sleep 2
  if s="$(curl -fsS --max-time 3 "$API/api/updates" 2>/dev/null)"; then
    st="$(echo "$s" | field 'd["state"]')"
    cur="$(echo "$s" | field 'd["current"]["version"]')"
    p="$(echo "$s" | field '(d.get("progress") or {}).get("bytes", "")')"
    line="state $st, running $cur${p:+, $p bytes}"
    [ "$line" != "$last" ] && { log "$line"; echo "$s" >>"$OUT/updates.txt"; last="$line"; }
    [ "$st" = error ] && fail "the update failed: $(echo "$s" | field 'd["error"]')"
    if [ "$cur" = "$LATEST" ]; then new="$cur"; break; fi
  else
    [ "$last" != down ] && { log "the server is restarting (no answer)"; last=down; }
  fi
done
[ -n "$new" ] || fail "the Studio did not come back on $LATEST"
log "the server runs $new: $(version)"
sleep 8
shot after-update

# 4. The copy in ~/Applications, the previous copy, and how it runs.
TO="$(bundle_version "$DEST")"
log "the copy in ~/Applications is version $TO (was $FROM)"
[ "$TO" != "$FROM" ] || fail "the copy in ~/Applications is still $FROM"
codesign --verify --deep --strict "$DEST" 2>>"$SUMMARY" || fail "the updated copy's signature does not verify"
log "the updated copy's signature verifies"
[ -d "$PREV" ] || fail "no previous copy at $PREV for a rollback"
log "the previous copy is kept for a rollback: version $(bundle_version "$PREV")"
if [ -n "$READONLY" ]; then
  [ -e "$AGENT" ] && fail "a LaunchAgent appeared after the update"
  NEW_PID="$(head -1 "$DATA/server.pid" 2>/dev/null)"
  cmd="$(ps -ww -o command= -p "$NEW_PID" 2>/dev/null)"
  log "server.pid $NEW_PID: $cmd"
  case "$cmd" in *"$DEST/Contents/Resources/studio-run.sh $DATA") ;; *) fail "server.pid does not name the updated copy's run loop" ;; esac
  node_parent="$(pgrep -f "$DEST/Contents/Resources/node/bin/node" | head -1 | xargs -I{} ps -o ppid= -p {} | tr -d ' ')"
  log "the server's parent process: $node_parent"
  [ "$node_parent" = "$NEW_PID" ] || fail "the server is not a child of the run loop server.pid names"
else
  launchctl print "gui/$(id -u)/com.parallelworks.activate-studio" >/dev/null 2>&1 || fail "the LaunchAgent is not loaded after the update"
  log "the LaunchAgent is loaded after the update"
fi
log "pending mark: $( [ -f "$DATA/updates/pending.json" ] && cat "$DATA/updates/pending.json" || echo none)"
log "reopen mark: $( [ -f "$DATA/updates/reopen" ] && cat "$DATA/updates/reopen" || echo none)"
tail -n 15 "$DATA/logs/agent.log" >"$OUT/agent-tail.txt" 2>/dev/null || true
collect

# 5. Uninstalled through the updated app, keeping the data: no server, run
# loop or LaunchAgent is left, and the data folder stays.
osascript -e "tell application \"$APP_NAME\" to quit" >/dev/null 2>&1 || true
sleep 2
"$DEST/Contents/MacOS/$APP_NAME" --uninstall --dry-run >"$OUT/uninstall-plan.txt" 2>&1
log "uninstall plan: $(tr '\t\n' ' |' <"$OUT/uninstall-plan.txt")"
"$DEST/Contents/MacOS/$APP_NAME" --uninstall >>"$SUMMARY" 2>&1 || fail "--uninstall failed"
sleep 3
[ -e "$DEST" ] && fail "the app is still in $DEST after --uninstall"
version >/dev/null && fail "the server still answers after --uninstall"
left="$(pgrep -fl 'studio-run.sh|ACTIVATE Studio.app/Contents/Resources/node' || true)"
[ -n "$left" ] && fail "processes left after --uninstall: $left"
[ -e "$AGENT" ] && fail "the LaunchAgent is left after --uninstall"
launchctl print "gui/$(id -u)/com.parallelworks.activate-studio" >/dev/null 2>&1 && fail "a LaunchAgent is still loaded after --uninstall"
[ -e "$DATA/server.pid" ] && fail "server.pid is left after --uninstall"
[ -d "$DATA/index" ] || fail "--uninstall removed the data folder, which it should keep"
log "uninstalled: no app, server, run loop or LaunchAgent left; the data folder is kept"
log "left in ~/Applications: $(ls -1A "$HOME/Applications" | tr '\n' '|')"
log "PASS"
