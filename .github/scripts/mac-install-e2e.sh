#!/usr/bin/env bash
# Installs ACTIVATE Studio from a published disk image the way a person
# does, on a macOS runner's desktop, and records each step: the image is
# downloaded and marked as downloaded by a browser (quarantine), opened in
# Finder, and the app on it opened through LaunchServices; macOS's prompt
# for an app from the internet and the app's own Install dialog are clicked
# through System Events; the installed copy in ~/Applications is checked
# (signature, no quarantine), its server waited for, and stopped.
#
# Usage: mac-install-e2e.sh DMG_URL OUT_DIR [readonly-agents] [replace]
#   readonly-agents  make ~/Library/LaunchAgents read-only first (a managed
#                    Mac), so the app must start its server without one
#   replace          afterwards, open the image's app again and check that
#                    it asks to replace the installed copy; Cancel is chosen
#
# OUT_DIR gets summary.txt (each step, timed), ui.txt (the windows and
# buttons seen at each look), shots/*.png, and logs/ (the Studio's own).
# A failed check ends the run with "FAIL: why" as the last summary line.
set -uo pipefail

URL="$1"
OUT="$2"
READONLY="${3:-}"
REPLACE="${4:-}"
APP_NAME="ACTIVATE Studio"
VOL="/Volumes/$APP_NAME"
DEST="$HOME/Applications/$APP_NAME.app"
DATA="$HOME/Library/Application Support/$APP_NAME"
AGENT="$HOME/Library/LaunchAgents/com.parallelworks.activate-studio.plist"
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
fail() { log "FAIL: $*"; shot failure; ui_state "at the failure"; collect; exit 1; }
collect() {
  [ -d "$DATA/logs" ] && cp -R "$DATA/logs" "$OUT/logs" 2>/dev/null
  return 0
}

# The visible processes, and for the ones that matter, each window's name
# and buttons, as System Events sees them.
ui_state() {
  {
    echo "== $(( $(date +%s) - T0 ))s: $1"
    osascript <<'OSA' 2>&1
set out to ""
tell application "System Events"
  set out to out & "visible processes: " & (name of every process whose visible is true) as text
  repeat with pname in {"ACTIVATE Studio", "CoreServicesUIAgent", "UserNotificationCenter", "Finder"}
    if exists process pname then
      tell process pname
        set out to out & linefeed & pname & ": " & (count of windows) & " window(s)"
        repeat with w in windows
          set wn to ""
          try
            set wn to name of w as text
          end try
          set bs to ""
          try
            set bs to (name of every button of w) as text
          end try
          set tx to ""
          try
            set tx to (value of every static text of w) as text
          end try
          set out to out & linefeed & "  window [" & wn & "] buttons [" & bs & "] text [" & tx & "]"
        end repeat
      end tell
    end if
  end repeat
end tell
return out
OSA
  } >>"$OUT/ui.txt"
}

# Clicks the named button in any window of the process; prints what it did.
click() {
  osascript - "$1" "$2" <<'OSA' 2>&1
on run argv
  set pname to item 1 of argv
  set bname to item 2 of argv
  tell application "System Events"
    if not (exists process pname) then return "no process " & pname
    tell process pname
      repeat with w in windows
        if exists button bname of w then
          click button bname of w
          return "clicked " & bname & " in " & pname
        end if
      end repeat
    end tell
  end tell
  return "no button " & bname & " in " & pname
end run
OSA
}

has_button() { click_probe "$1" "$2" | grep -q '^yes$'; }
click_probe() {
  osascript - "$1" "$2" <<'OSA' 2>/dev/null
on run argv
  tell application "System Events"
    if not (exists process (item 1 of argv)) then return "no"
    tell process (item 1 of argv)
      repeat with w in windows
        if exists button (item 2 of argv) of w then return "yes"
      end repeat
    end tell
  end tell
  return "no"
end run
OSA
}

server_version() { curl -fsS --max-time 2 http://127.0.0.1:4180/api/version 2>/dev/null; }

log "runner: $(sw_vers -productName) $(sw_vers -productVersion) ($(uname -m)), user $(id -un)"
log "Gatekeeper: $(spctl --status 2>&1)"
log "System Events can see processes: $(osascript -e 'tell application "System Events" to count processes' 2>&1)"

if [ -n "$READONLY" ]; then
  mkdir -p "$HOME/Library/LaunchAgents"
  chmod 555 "$HOME/Library/LaunchAgents"
  log "~/Library/LaunchAgents is now read-only: $(stat -f '%Sp' "$HOME/Library/LaunchAgents")"
fi
[ -e "$DEST" ] && fail "$DEST exists before the install"

# 1. The image, marked as a browser marks a download.
DMG="$OUT/../activate-studio.dmg"
curl -fsSL -o "$DMG" "$URL" || fail "the disk image could not be downloaded from $URL"
xattr -w com.apple.quarantine "0083;$(printf '%x' "$(date +%s)");Safari;$(uuidgen)" "$DMG"
log "downloaded $(du -h "$DMG" | cut -f1), quarantine: $(xattr -p com.apple.quarantine "$DMG")"
log "Gatekeeper on the image: $(spctl -a -t open --context context:primary-signature -v "$DMG" 2>&1 | tr '\n' ' ')"
shot desktop

# 2. Opened as a double-click in Finder does: mounted and shown.
open "$DMG"
for _ in $(seq 1 60); do [ -d "$VOL/$APP_NAME.app" ] && break; sleep 1; done
[ -d "$VOL/$APP_NAME.app" ] || fail "the image did not mount at $VOL"
sleep 3
log "mounted at $VOL; it holds: $(ls -1A "$VOL" | tr '\n' '|')"
[ -f "$VOL/Installing $APP_NAME.txt" ] || fail "the install note is missing"
[ -L "$VOL/Applications" ] && fail "the image still has an Applications link"
log "the app on the image: quarantine $(xattr -p com.apple.quarantine "$VOL/$APP_NAME.app" 2>&1 | head -1)"
log "version on the image: $(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$VOL/$APP_NAME.app/Contents/Info.plist")"
shot image-window
ui_state "image open"

# 3. The app on the image, opened through LaunchServices.
# open waits while macOS asks about an app from the internet, so it runs
# in the background and the prompt is answered below.
open "$VOL/$APP_NAME.app" &
log "opened the app on the image"
installed_at=
prompt_seen=
install_seen=
for i in $(seq 1 60); do
  sleep 2
  if has_button CoreServicesUIAgent Open; then
    prompt_seen=1
    log "macOS asks about an app downloaded from the internet"
    ui_state "the downloaded-app prompt"
    shot downloaded-app-prompt
    log "$(click CoreServicesUIAgent Open)"
    continue
  fi
  if has_button "$APP_NAME" Install; then
    install_seen=1
    if [ -n "$prompt_seen" ]; then log "the app shows its Install dialog, after the macOS prompt"; else log "the app shows its Install dialog"; fi
    ui_state "the Install dialog"
    shot install-dialog
    log "$(click "$APP_NAME" Install)"
    continue
  fi
  if [ -d "$DEST" ]; then installed_at=$(( $(date +%s) - T0 )); break; fi
  [ $((i % 5)) = 0 ] && { shot waiting; ui_state "waiting"; }
done
[ -n "$install_seen" ] || fail "no Install dialog appeared (see ui.txt and the screenshots)"
[ -n "$installed_at" ] || fail "nothing was installed in $DEST"
[ -n "$prompt_seen" ] || log "macOS showed no prompt about an app downloaded from the internet"
log "installed in $DEST at ${installed_at}s"

# 4. The installed copy.
sleep 2
codesign --verify --deep --strict "$DEST" 2>>"$SUMMARY" || fail "the installed copy's signature does not verify"
log "the installed copy's signature verifies"
if xattr -p com.apple.quarantine "$DEST" >/dev/null 2>&1; then fail "the installed copy still has the quarantine mark"; fi
log "the installed copy has no quarantine mark"
log "Gatekeeper on the installed copy: $(spctl -a -vv "$DEST" 2>&1 | tr '\n' ' ')"

# 5. Its server, and its window.
v=
for _ in $(seq 1 120); do v="$(server_version)" && break; sleep 2; done
[ -n "$v" ] || fail "the installed copy's server did not answer on 127.0.0.1:4180"
log "the server answers: $v"
sleep 8
ui_state "the Studio running"
shot studio-window
log "processes: $(pgrep -fl 'ACTIVATE Studio' | sed 's/^/[/;s/$/]/' | tr '\n' ' ')"

if [ -n "$READONLY" ]; then
  [ -e "$AGENT" ] && fail "a LaunchAgent was written although the folder is read-only"
  [ -s "$DATA/server.pid" ] || fail "no server.pid, so the app did not start the server itself"
  log "no LaunchAgent; the app started the server itself (server.pid $(head -1 "$DATA/server.pid"))"
else
  [ -f "$AGENT" ] || fail "no LaunchAgent was installed"
  launchctl print "gui/$(id -u)/com.parallelworks.activate-studio" >/dev/null 2>&1 || fail "the LaunchAgent is not loaded"
  log "the LaunchAgent is installed and loaded"
fi

# 6. Asked again from the image, with a copy installed: Replace, then Cancel.
if [ -n "$REPLACE" ]; then
  before="$(stat -f '%m' "$DEST")"
  osascript -e "tell application \"$APP_NAME\" to quit" >/dev/null 2>&1 || true
  sleep 3
  open -n "$VOL/$APP_NAME.app" &
  log "opened the app on the image again"
  replace_seen=
  for _ in $(seq 1 45); do
    sleep 2
    if has_button CoreServicesUIAgent Open; then shot downloaded-app-prompt-again; log "$(click CoreServicesUIAgent Open)"; continue; fi
    if has_button "$APP_NAME" Install; then shot install-dialog-again; log "$(click "$APP_NAME" Install)"; continue; fi
    if has_button "$APP_NAME" Replace; then
      replace_seen=1
      ui_state "the Replace dialog"
      shot replace-dialog
      log "$(click "$APP_NAME" Cancel)"
      break
    fi
  done
  [ -n "$replace_seen" ] || fail "no Replace dialog appeared with a copy installed"
  sleep 3
  [ "$(stat -f '%m' "$DEST")" = "$before" ] || fail "Cancel changed the installed copy"
  log "Cancel left the installed copy as it was"
  shot after-cancel
fi

# 7. Stopped through the installed app.
osascript -e "tell application \"$APP_NAME\" to quit" >/dev/null 2>&1 || true
"$DEST/Contents/MacOS/$APP_NAME" --stop >>"$SUMMARY" 2>&1 || fail "--stop failed"
for _ in $(seq 1 30); do server_version >/dev/null || break; sleep 1; done
server_version >/dev/null && fail "the server still answers after --stop"
log "--stop stopped the server"
collect
hdiutil detach "$VOL" >/dev/null 2>&1 || true
log "PASS"
