#!/bin/bash
# ACTIVATE Studio for macOS (Contents/MacOS/ACTIVATE Studio in the app).
#
#   ACTIVATE Studio                       start the server if needed and open the Studio
#   ACTIVATE Studio --stop                stop the server until the app is next opened
#   ACTIVATE Studio --uninstall [--purge] remove the background server and the app;
#                                         --purge also removes the index (wherever it is kept),
#                                         the indexes of libraries built here, logs, settings
#                                         and the window's own data
#   ACTIVATE Studio --uninstall --dry-run [--purge]
#                                         list what would be removed and kept, one
#                                         "kind<TAB>path" line each; removes nothing
#
# Your documents folder is never removed.
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
  local was_running=1
  if ! loaded; then
    was_running=0
    if ! launchctl bootstrap "$DOMAIN" "$AGENT"; then
      say "ACTIVATE Studio could not start its background server (launchctl bootstrap failed)."
      exit 1
    fi
  fi

  local port
  port="$(setting PORT)"
  port="${port:-4180}"
  if ! version_of "$port" >/dev/null; then
    # A server already running that does not answer on this port started
    # before studio.env named it; a restart reads the settings again.
    [ "$was_running" = 1 ] && launchctl kickstart -k "$SERVICE" 2>/dev/null || true
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

# What the Studio writes in its index folder (INDEX_DIR, server/src/config.ts
# and indexing.ts); a folder the index was moved to keeps anything else.
INDEX_ENTRIES='gufi extract embed-cache.db embed-cache.db-wal embed-cache.db-shm folders indexed-root'

# The Python the app carries, else one on the PATH: it reads the Studio's
# JSON files.
pyrun() {
  local p="$RES/python/bin/python3"
  [ -x "$p" ] || p="$(command -v python3 || true)"
  [ -n "$p" ] && "$p" "$@"
}

# Whether a path is inside another (or the same).
inside() { case "$1/" in "$2"/*) return 0 ;; esac; return 1; }

# Whether a folder holds an index the Studio wrote there.
studio_index() { [ -d "$1/gufi" ] || [ -e "$1/indexed-root" ]; }

# What an uninstall removes and keeps, as "kind<TAB>path" lines. The app's
# Uninstall dialog shows this list and the removal follows it, so the two
# never differ. Kinds: server (the LaunchAgent), app, update-copy, data (the
# Studio's data folder), index (an index moved elsewhere: only the Studio's
# entries in it), library-index (the index of a library built here, in a
# folder chosen for it: only the index and its manifest), window-data, and
# keep-documents and keep-data for what stays.
plan() {
  local purge="$1" kb base moved side d f libs
  kb="$(setting KB_ROOT)"
  printf 'server	%s
' "$AGENT"
  if [ -f "$CONTENTS/Info.plist" ] && grep -q "<string>$LABEL</string>" "$CONTENTS/Info.plist"; then
    printf 'app	%s
' "$APP"
    # The copies updates keep beside it (.<name>.previous and others).
    for side in previous new unpack failed; do
      d="$(dirname "$APP")/.$(basename "$APP").$side"
      [ -e "$d" ] && printf 'update-copy	%s
' "$d"
    done
  fi
  if [ "$purge" = 1 ]; then
    [ -d "$DATA" ] && printf 'data	%s
' "$DATA"
    base="$(setting INDEX_BASE)"
    base="${base:-$DATA/index}"
    # INDEX_BASE set by hand outside the data folder, when it is the Studio's.
    if [ -d "$base" ] && ! inside "$base" "$DATA" && { [ -f "$base/libraries.json" ] || [ -f "$base/indexing.json" ] || studio_index "$base"; }; then
      printf 'data	%s
' "$base"
    fi
    moved="$(setting STUDIO_INDEX_DIR)"
    if [ -n "$moved" ] && [ -d "$moved" ] && ! inside "$moved" "$DATA" && ! inside "$moved" "$base" && studio_index "$moved"; then
      printf 'index	%s
' "$moved"
    fi
    # Libraries built of folders here, with their index in a folder chosen
    # for it: those holding the manifest the Studio writes beside the index.
    libs="$(pyrun -c 'import json, sys
try: defs = json.load(open(sys.argv[1]))
except Exception: defs = []
for d in defs if isinstance(defs, list) else []:
    b = d.get("build") if isinstance(d, dict) else None
    if isinstance(b, dict) and isinstance(b.get("indexDir"), str) and b["indexDir"].startswith("/"): print(b["indexDir"])' "$base/libraries.json" 2>/dev/null || true)"
    while IFS= read -r d; do
      [ -n "$d" ] && [ -f "$d/.studio-index.json" ] || continue
      inside "$d" "$DATA" || inside "$d" "$base" || inside "$kb" "$d" || printf 'library-index	%s
' "$d"
    done <<<"$libs"
    for f in "$HOME/Library/WebKit/$LABEL" "$HOME/Library/Caches/$LABEL" "$HOME/Library/HTTPStorages/$LABEL" \
      "$HOME/Library/HTTPStorages/$LABEL.binarycookies" "$HOME/Library/Saved Application State/$LABEL.savedState" \
      "$HOME/Library/Preferences/$LABEL.plist"; do
      [ -e "$f" ] && printf 'window-data	%s
' "$f"
    done
  else
    [ -d "$DATA" ] && printf 'keep-data	%s
' "$DATA"
  fi
  printf 'keep-documents	%s
' "${kb:-$HOME/Documents/ACTIVATE Studio}"
}

# The index a library built here keeps in a folder chosen for it: the tree
# its manifest names, the manifest, and the build's staging folders; the
# folder itself only when nothing else is left in it.
remove_library_index() {
  local d="$1" tree s
  tree="$(pyrun -c 'import json, sys; print(json.load(open(sys.argv[1])).get("index", ""))' "$d/.studio-index.json" 2>/dev/null || true)"
  if [ -n "$tree" ] && inside "$tree" "$d" && [ "$tree" != "$d" ]; then rm -rf "$tree"; fi
  for s in "$d"/.build "$d"/.old "$d"/.inc-*; do [ -e "$s" ] && rm -rf "$s"; done
  rm -f "$d/.studio-index.json"
  rmdir "$d" 2>/dev/null || true
}

# The Studio's entries in a folder the index was moved to; the folder only
# when nothing else is left in it.
remove_moved_index() {
  local d="$1" e
  for e in $INDEX_ENTRIES; do rm -rf "${d:?}/$e"; done
  for e in "$d"/gufi.staging-* "$d"/inc-*; do [ -e "$e" ] && rm -rf "$e"; done
  rmdir "$d" 2>/dev/null || true
}

uninstall() {
  local purge=0 dry=0 a lines kind path
  for a in "$@"; do
    case "$a" in
      --purge) purge=1 ;;
      --dry-run) dry=1 ;;
    esac
  done
  # Worked out before anything is removed: the data folder holds the list
  # of libraries.
  lines="$(plan "$purge")"
  if [ "$dry" = 1 ]; then
    printf '%s\n' "$lines"
    return
  fi
  stop
  rm -f "$AGENT"
  launchctl enable "$SERVICE" 2>/dev/null || true
  while IFS=$'\t' read -r kind path; do
    [ -n "$path" ] || continue
    case "$kind" in
      data) rm -rf "$path"; echo "Removed the index, logs and settings ($path)." ;;
      index) remove_moved_index "$path"; echo "Removed the index kept in $path." ;;
      library-index) remove_library_index "$path"; echo "Removed the library index in $path." ;;
      window-data) rm -rf "$path"; echo "Removed $path." ;;
      update-copy) rm -rf "$path" ;;
      keep-data) echo "Kept the index, logs and settings in $path (--purge removes them)." ;;
      keep-documents) echo "Your documents are untouched, in $path." ;;
    esac
  done <<<"$lines"
  # The app last, since this script is inside it.
  while IFS=$'\t' read -r kind path; do
    if [ "$kind" = app ]; then rm -rf "$path"; echo "Removed $path."; fi
  done <<<"$lines"
}

case "${1:-}" in
  --stop) stop; echo "ACTIVATE Studio is stopped; opening the app starts it again." ;;
  --uninstall) shift; uninstall "$@" ;;
  --help|-h) sed -n '2,20p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
  *) start ;;    # Finder may pass -psn_... on older systems
esac
