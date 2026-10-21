#!/bin/bash
# ACTIVATE Studio for macOS (Contents/MacOS/ACTIVATE Studio in the app).
#
#   ACTIVATE Studio                       start the server if needed and open the Studio
#   ACTIVATE Studio --stop                stop the server until the app is next opened
#   ACTIVATE Studio --uninstall [--purge] remove the background server and the app;
#                                         --purge also removes the index, logs and settings
#
# The server runs as a LaunchAgent (com.parallelworks.activate-studio), on
# 127.0.0.1 only, from studio-run.sh in this app. Its data lives in
# ~/Library/Application Support/ACTIVATE Studio: index/, logs/, updates/,
# and studio.env, the settings read before each start (PORT=4180 by default).
# STUDIO_NO_BROWSER=1 skips opening a window.
set -euo pipefail

CONTENTS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
APP="$(dirname "$CONTENTS")"
RES="$CONTENTS/Resources"
LABEL=com.parallelworks.activate-studio
DATA="$HOME/Library/Application Support/ACTIVATE Studio"
CONFIG="$DATA/studio.env"
AGENT="$HOME/Library/LaunchAgents/$LABEL.plist"
DOMAIN="gui/$(id -u)"
SERVICE="$DOMAIN/$LABEL"

# In a terminal, or under the app's own window (which shows what it is
# told), print; opened from Finder as a plain launcher, use macOS dialogs.
printing() { [ -t 2 ] || [ "${STUDIO_NO_BROWSER:-}" = 1 ]; }

say() {
  if printing; then
    echo "$1" >&2
  else
    osascript -e 'on run argv' -e 'display alert "ACTIVATE Studio" message (item 1 of argv)' -e 'end run' "$1" >/dev/null 2>&1 || true
  fi
}

setting() {
  # One value from studio.env, as the run loop will see it.
  # shellcheck source=/dev/null
  ( set +eu; unset "$1"; [ -f "$CONFIG" ] && . "$CONFIG" >/dev/null 2>&1; printf '%s' "${!1:-}" )
}

version_of() { curl -fsS -m 2 "http://127.0.0.1:$1/api/version" 2>/dev/null; }

built_at() { sed -n 's/.*"builtAt": *"\([^"]*\)".*/\1/p' | head -n 1; }

loaded() { launchctl print "$SERVICE" >/dev/null 2>&1; }

xml() { local s="${1//&/&amp;}"; s="${s//</&lt;}"; printf '%s' "${s//>/&gt;}"; }

agent_plist() {
  cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>/bin/bash</string>
    <string>$(xml "$RES/studio-run.sh")</string>
    <string>$(xml "$DATA")</string>
  </array>
  <key>AssociatedBundleIdentifiers</key>
  <array>
    <string>$LABEL</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>STUDIO_APP_BUNDLE</key>
    <string>$(xml "$APP")</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ExitTimeOut</key>
  <integer>30</integer>
  <key>WorkingDirectory</key>
  <string>$(xml "$DATA")</string>
  <key>StandardOutPath</key>
  <string>$(xml "$DATA/logs/agent.log")</string>
  <key>StandardErrorPath</key>
  <string>$(xml "$DATA/logs/agent.log")</string>
</dict>
</plist>
PLIST
}

stop() {
  launchctl bootout "$SERVICE" 2>/dev/null || true
  # Disabled, so it does not start again at login until the app is opened.
  launchctl disable "$SERVICE" 2>/dev/null || true
}

wait_ready() {
  local n=0
  until version_of "$1" >/dev/null; do
    n=$((n + 1))
    if [ "$n" -gt 120 ]; then
      say "ACTIVATE Studio did not start. Its log is $DATA/logs/studio.log."
      if printing; then tail -n 20 "$DATA/logs/studio.log" >&2 || true; else open "$DATA/logs" || true; fi
      exit 1
    fi
    sleep 1
  done
}

open_window() {
  local url="http://127.0.0.1:$1/" app dir
  if [ "${STUDIO_NO_BROWSER:-}" = 1 ]; then
    echo "ACTIVATE Studio is running at $url"
    return
  fi
  # An app window of its own where Chrome or Edge is installed; otherwise
  # the default browser (where Safari's File, Add to Dock makes one).
  for app in "Google Chrome" "Microsoft Edge"; do
    for dir in /Applications "$HOME/Applications"; do
      if [ -d "$dir/$app.app" ]; then
        open -na "$dir/$app.app" --args --app="$url" && return
      fi
    done
  done
  open "$url"
}

start() {
  case "$APP" in
    */AppTranslocation/*|/Volumes/*)
      say "Move ACTIVATE Studio to your Applications folder, then open it from there."
      exit 1 ;;
  esac
  mkdir -p "$DATA/index" "$DATA/logs" "$(dirname "$AGENT")"
  [ -f "$CONFIG" ] || printf "PORT='4180'\n" >"$CONFIG"
  # This is the app's current executable, so an update that asked for the
  # app to be reopened has had it.
  rm -f "$DATA/updates/reopen"

  # The agent names this copy of the app: rewrite it when the app moved,
  # and load it when it is not running.
  local want
  want="$(agent_plist)"
  if [ ! -f "$AGENT" ] || [ "$(cat "$AGENT")" != "$want" ]; then
    if loaded; then
      launchctl bootout "$SERVICE" 2>/dev/null || true
      for _ in 1 2 3 4 5 6 7 8 9 10; do loaded || break; sleep 0.5; done
    fi
    printf '%s\n' "$want" >"$AGENT"
  fi
  launchctl enable "$SERVICE" 2>/dev/null || true
  if ! loaded && ! launchctl bootstrap "$DOMAIN" "$AGENT"; then
    say "ACTIVATE Studio could not start its background server (launchctl bootstrap failed)."
    exit 1
  fi

  local port
  port="$(setting PORT)"
  port="${port:-4180}"
  if ! version_of "$port" >/dev/null; then
    printing || osascript -e 'display notification "Starting..." with title "ACTIVATE Studio"' >/dev/null 2>&1 || true
    wait_ready "$port"
  fi
  # A server still running an earlier copy of the app (replaced in place)
  # is restarted once, so the window shows this one.
  local mine running
  mine="$(built_at <"$RES/app/build-info.json" 2>/dev/null || true)"
  running="$(version_of "$port" | built_at || true)"
  if [ -n "$mine" ] && [ -n "$running" ] && [ "$running" != "$mine" ]; then
    launchctl kickstart -k "$SERVICE" 2>/dev/null || true
    sleep 2
    wait_ready "$port"
  fi
  open_window "$port"
}

uninstall() {
  stop
  rm -f "$AGENT"
  launchctl enable "$SERVICE" 2>/dev/null || true
  local kb
  kb="$(setting KB_ROOT)"
  if [ "${1:-}" = --purge ]; then
    rm -rf "$DATA"
    echo "Removed the index, logs and settings ($DATA)."
  else
    echo "Kept the index, logs and settings in $DATA (--purge removes them)."
  fi
  echo "Your documents are untouched, in ${kb:-$HOME/Documents/ACTIVATE Studio}."
  if [ -f "$CONTENTS/Info.plist" ] && grep -q "<string>$LABEL</string>" "$CONTENTS/Info.plist"; then
    # With the copies updates keep beside it (.<name>.previous and others).
    local side
    for side in previous new unpack failed; do rm -rf "$(dirname "$APP")/.$(basename "$APP").$side"; done
    rm -rf "$APP"
    echo "Removed $APP."
  fi
}

case "${1:-}" in
  --stop) stop; echo "ACTIVATE Studio is stopped; opening the app starts it again." ;;
  --uninstall) uninstall "${2:-}" ;;
  --help|-h) sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
  *) start ;;    # Finder may pass -psn_... on older systems
esac
