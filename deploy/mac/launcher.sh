#!/bin/bash
# ACTIVATE Studio for macOS (Contents/MacOS/ACTIVATE Studio in the app).
#
#   ACTIVATE Studio                       start the server if needed and open the Studio
#   ACTIVATE Studio --install [DIR]       install this copy in DIR (~/Applications by
#                                         default, created if missing), replacing a copy
#                                         there; for a copy opened from the disk image
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
# Opened from its disk image (or from where it was downloaded, which macOS
# runs from a read-only copy), the app is not installed yet: its window
# offers to install it in ~/Applications, which needs no administrator
# rights and where it updates itself, and opens that copy. Run without the
# window, this script offers the same with a macOS dialog, or in a terminal
# says to use --install.
#
# The server runs as a LaunchAgent (com.parallelworks.activate-studio), on
# 127.0.0.1 only, from studio-run.sh in this app. Where no LaunchAgent can be
# installed or loaded, as on a managed Mac where ~/Library/LaunchAgents is
# read-only, the app starts studio-run.sh itself, in a session of its own,
# and records it in server.pid; that server starts when the app is opened,
# not at login. No administrator rights are needed either way. Its data
# lives in ~/Library/Application Support/ACTIVATE Studio: index/, logs/,
# updates/, and studio.env, the settings read before each start (PORT=4180
# by default). STUDIO_NO_BROWSER=1 skips opening a window.
set -euo pipefail

CONTENTS="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
APP="$(dirname "$CONTENTS")"
RES="$CONTENTS/Resources"
LABEL=com.parallelworks.activate-studio
DATA="$HOME/Library/Application Support/ACTIVATE Studio"
CONFIG="$DATA/studio.env"
PIDFILE="$DATA/server.pid"
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

stamp() { date '+%Y-%m-%d %H:%M:%S'; }

# The run loop the app started itself (start_detached), while it runs: its
# PID, checked against its command line so that a stale or reused number is
# never signalled. DETACHED_CMD keeps the command line, when called directly.
DETACHED_CMD=
detached_pid() {
  local p
  DETACHED_CMD=
  p="$(head -n 1 "$PIDFILE" 2>/dev/null || true)"
  case "$p" in ''|*[!0-9]*) return 1 ;; esac
  DETACHED_CMD="$(ps -ww -o command= -p "$p" 2>/dev/null || true)"
  case "$DETACHED_CMD" in *"/studio-run.sh $DATA") printf '%s\n' "$p" ;; *) DETACHED_CMD=; return 1 ;; esac
}

# Starts studio-run.sh as the LaunchAgent runs it, as a process of this
# person's own, where no agent can be installed or loaded. The app's Python
# gives it a session of its own (setsid), so it outlives this script and the
# app's window, and it ignores hangups. Its output goes where the agent's
# goes. STUDIO_SUPERVISOR=detached tells the server that an update restarts
# it through the run loop (studio-run.sh, exit 76) rather than launchd.
start_detached() {
  local py="$RES/python/bin/python3" log="$DATA/logs/agent.log" p
  if [ -x "$py" ]; then
    STUDIO_APP_BUNDLE="$APP" STUDIO_SUPERVISOR=detached nohup "$py" -B -c 'import os, signal, sys
os.setsid()
signal.signal(signal.SIGHUP, signal.SIG_IGN)
os.execv("/bin/bash", ["/bin/bash"] + sys.argv[1:])' "$RES/studio-run.sh" "$DATA" </dev/null >>"$log" 2>&1 &
  else
    STUDIO_APP_BUNDLE="$APP" STUDIO_SUPERVISOR=detached nohup /bin/bash "$RES/studio-run.sh" "$DATA" </dev/null >>"$log" 2>&1 &
  fi
  p=$!
  disown "$p" 2>/dev/null || true
  printf '%s\n' "$p" >"$PIDFILE"
  echo "$(stamp) the app started the server itself, without a LaunchAgent (process $p)" >>"$log"
}

# Stops that run loop: TERM, which it passes to the server and waits out (as
# launchd allows 30 seconds), then KILL to its process group, the one setsid
# gave it, if it is still the same process.
stop_detached() {
  local p n=0
  if p="$(detached_pid)"; then
    kill -TERM "$p" 2>/dev/null || true
    while [ "$n" -lt 70 ] && detached_pid >/dev/null; do sleep 0.5; n=$((n + 1)); done
    if p="$(detached_pid)"; then kill -KILL -- "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null || true; fi
  fi
  rm -f "$PIDFILE"
}

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
  stop_detached
}

# Installs and loads the LaunchAgent for this copy of the app: rewritten when
# the app moved, loaded when it is not running. Fails, with AGENT_WHY saying
# what failed, where the agent cannot be written or loaded; WAS_RUNNING says
# whether it was running already.
AGENT_WHY=
WAS_RUNNING=1
agent_start() {
  local want dir
  dir="$(dirname "$AGENT")"
  want="$(agent_plist)"
  if [ ! -f "$AGENT" ] || [ "$(cat "$AGENT" 2>/dev/null)" != "$want" ]; then
    # Checked before a running agent is stopped to be rewritten.
    if ! mkdir -p "$dir" 2>/dev/null || [ ! -w "$dir" ] || { [ -e "$AGENT" ] && [ ! -w "$AGENT" ]; }; then
      AGENT_WHY="$dir is not writable"
      return 1
    fi
    if loaded; then
      launchctl bootout "$SERVICE" 2>/dev/null || true
      for _ in 1 2 3 4 5 6 7 8 9 10; do loaded || break; sleep 0.5; done
    fi
    if ! { printf '%s\n' "$want" >"$AGENT"; } 2>/dev/null; then
      AGENT_WHY="$AGENT could not be written"
      return 1
    fi
  fi
  launchctl enable "$SERVICE" 2>/dev/null || true
  WAS_RUNNING=1
  if ! loaded; then
    WAS_RUNNING=0
    if ! launchctl bootstrap "$DOMAIN" "$AGENT" 2>>"$DATA/logs/agent.log" && ! loaded; then
      AGENT_WHY="launchctl bootstrap did not load $AGENT"
      return 1
    fi
  fi
}

# Starts the server again, the way it runs.
restart_server() {
  if [ "$MODE" = agent ]; then
    launchctl kickstart -k "$SERVICE" 2>/dev/null || true
  else
    stop_detached
    start_detached
  fi
}

wait_ready() {
  local n=0
  until version_of "$1" >/dev/null; do
    n=$((n + 1))
    if [ "$n" -gt 120 ]; then
      say "ACTIVATE Studio did not start. Its logs are in $DATA/logs (studio.log, and agent.log for the background server)."
      if printing; then
        tail -n 20 "$DATA/logs/studio.log" >&2 || true
        [ "$MODE" = detached ] && { tail -n 5 "$DATA/logs/agent.log" >&2 || true; }
      else
        open "$DATA/logs" || true
      fi
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

# Whether the background server runs from the copy of the app at $1: the
# run loop the app started itself, or the LaunchAgent naming that copy.
running_from() {
  local run="$1/Contents/Resources/studio-run.sh"
  if detached_pid >/dev/null; then
    case "$DETACHED_CMD" in *"$run $DATA") return 0 ;; esac
  fi
  [ -f "$AGENT" ] && grep -qF "<string>$(xml "$run")</string>" "$AGENT" && loaded
}

# Installs this copy of the app in a folder: copied beside its place there
# under a temporary name, then moved into place, so a copy already there is
# replaced whole or not at all. A server running from that copy is stopped
# first; opening the new copy starts it again (start(), below). INSTALLED
# names the installed copy; on failure the reason is on stderr.
INSTALLED=
install_into() {
  local dir="$1" name dest tmp old err copied=1
  name="$(basename "$APP")"
  if ! mkdir -p "$dir" 2>/dev/null || ! dir="$(cd "$dir" 2>/dev/null && pwd -P)"; then
    echo "Could not create the folder $1." >&2
    return 1
  fi
  dest="$dir/$name"
  if [ "$dest" = "$APP" ]; then
    INSTALLED="$dest"
    echo "This copy of ACTIVATE Studio is the one in $dir already."
    return 0
  fi
  case "$dir/" in "$APP"/*) echo "ACTIVATE Studio cannot be installed inside itself ($dir)." >&2; return 1 ;; esac
  if [ ! -w "$dir" ]; then
    echo "$dir is not writable for you. The Applications folder in your home folder (~/Applications) is." >&2
    return 1
  fi
  tmp="$dir/.$name.install"
  old="$dir/.$name.replaced"
  rm -rf "$tmp" "$old"
  # ditto on a Mac keeps the bundle as signed; cp where there is none (tests).
  if command -v ditto >/dev/null 2>&1; then
    err="$(ditto "$APP" "$tmp" 2>&1)" || copied=0
  else
    err="$(cp -pPR "$APP" "$tmp" 2>&1)" || copied=0
  fi
  if [ "$copied" = 0 ] || [ ! -x "$tmp/Contents/MacOS/$(basename "$name" .app)" ]; then
    rm -rf "$tmp"
    err="${err##*$'\n'}"
    echo "Copying ACTIVATE Studio to $dir failed: ${err:-the copy is incomplete}." >&2
    return 1
  fi
  # A download carries the quarantine mark, and macOS opens a marked app that
  # Finder did not move from a read-only copy elsewhere (App Translocation),
  # where it could not update itself. The mark is removed from this copy
  # only: it is the same notarized bundle Gatekeeper assessed when the copy
  # being installed from opened, and the updater and install.sh put their
  # copies in place without it too.
  if command -v xattr >/dev/null 2>&1; then xattr -dr com.apple.quarantine "$tmp" 2>/dev/null || true; fi
  if [ -e "$dest" ]; then
    if running_from "$dest"; then stop; fi
    if ! mv "$dest" "$old" 2>/dev/null; then
      rm -rf "$tmp"
      echo "Could not replace $dest: it could not be moved aside." >&2
      return 1
    fi
  fi
  if ! mv "$tmp" "$dest" 2>/dev/null; then
    if [ -e "$old" ]; then mv "$old" "$dest" 2>/dev/null || true; fi
    rm -rf "$tmp"
    echo "Could not move the new copy into $dest." >&2
    return 1
  fi
  rm -rf "$old"
  INSTALLED="$dest"
  echo "Installed ACTIVATE Studio in $dest."
}

# The copy macOS runs from a disk image, or from a read-only copy of where it
# was downloaded (App Translocation): not installed yet.
not_installed() {
  case "$APP" in */AppTranslocation/*|/Volumes/*) return 0 ;; esac
  return 1
}

# Opened before it is installed. The app's window asks itself and runs
# --install; without the window, a macOS dialog offers the same, and in a
# terminal the way to install is printed.
offer_install() {
  local dir="$HOME/Applications" dest answer
  dest="$dir/$(basename "$APP")"
  if printing; then
    echo "ACTIVATE Studio is not installed yet: it runs from its disk image or the folder it was downloaded to. Install it in ~/Applications (no administrator rights needed) with:" >&2
    echo "  \"$CONTENTS/MacOS/$(basename "$APP" .app)\" --install" >&2
    echo "or double-click it in Finder and choose Install." >&2
    exit 1
  fi
  answer="$(osascript -e 'on run argv' -e 'display dialog (item 1 of argv) with title "ACTIVATE Studio" buttons {"Cancel", "Install"} default button "Install" cancel button "Cancel"' -e 'end run' \
    "ACTIVATE Studio is not installed yet: it runs from its disk image or the folder it was downloaded to. Install puts it in the Applications folder in your home folder ($dir), with no administrator rights needed, and opens it from there. It updates itself there." 2>/dev/null || true)"
  case "$answer" in *Install*) ;; *) exit 0 ;; esac
  if [ -e "$dest" ]; then
    answer="$(osascript -e 'on run argv' -e 'display dialog (item 1 of argv) with title "ACTIVATE Studio" buttons {"Cancel", "Replace"} default button "Replace" cancel button "Cancel"' -e 'end run' \
      "ACTIVATE Studio is already in $dir. Replace that copy with this one? Your index, settings and libraries stay as they are, in ~/Library/Application Support/ACTIVATE Studio." 2>/dev/null || true)"
    case "$answer" in *Replace*) ;; *) exit 0 ;; esac
  fi
  local out
  if ! out="$(install_into "$dir" 2>&1)"; then
    say "${out##*$'\n'}"
    exit 1
  fi
  open -n "$dest"
  exit 0
}

MODE=agent
start() {
  if not_installed; then offer_install; fi
  if ! mkdir -p "$DATA/index" "$DATA/logs" 2>/dev/null; then
    say "ACTIVATE Studio could not create its data folder, $DATA."
    exit 1
  fi
  [ -f "$CONFIG" ] || printf "PORT='4180'\n" >"$CONFIG"
  # This is the app's current executable, so an update that asked for the
  # app to be reopened has had it.
  rm -f "$DATA/updates/reopen"

  # A server the app started itself at an earlier opening is used as it is
  # (restarted from this copy when another copy started it). Otherwise the
  # LaunchAgent, and where none can be installed or loaded, a server the
  # app starts itself.
  if detached_pid >/dev/null; then
    MODE=detached WAS_RUNNING=1
    case "$DETACHED_CMD" in
      *"$RES/studio-run.sh $DATA") ;;
      *) restart_server; WAS_RUNNING=0 ;;
    esac
  elif ! agent_start; then
    MODE=detached WAS_RUNNING=0
    echo "$(stamp) no LaunchAgent ($AGENT_WHY); the app starts the server itself, when it is opened" >>"$DATA/logs/agent.log"
    start_detached
  fi

  local port
  port="$(setting PORT)"
  port="${port:-4180}"
  if ! version_of "$port" >/dev/null; then
    # A server already running that does not answer on this port started
    # before studio.env named it; a restart reads the settings again.
    [ "$WAS_RUNNING" = 1 ] && restart_server
    printing || osascript -e 'display notification "Starting..." with title "ACTIVATE Studio"' >/dev/null 2>&1 || true
    wait_ready "$port"
  fi
  # A server still running an earlier copy of the app (replaced in place)
  # is restarted once, so the window shows this one.
  local mine running
  mine="$(built_at <"$RES/app/build-info.json" 2>/dev/null || true)"
  running="$(version_of "$port" | built_at || true)"
  if [ -n "$mine" ] && [ -n "$running" ] && [ "$running" != "$mine" ]; then
    restart_server
    sleep 2
    wait_ready "$port"
  fi
  open_window "$port"
}

# What the Studio writes in its index folder (INDEX_DIR, server/src/config.ts
# and indexing.ts); a folder the index was moved to keeps anything else.
INDEX_ENTRIES='gufi extract embed-cache.db embed-cache.db-wal embed-cache.db-shm folders indexed-root'

# The Python the app carries, else one on the PATH: it reads the Studio's
# JSON files. It writes no bytecode caches, which would land inside the app
# and break its signature's seal.
pyrun() {
  local p="$RES/python/bin/python3"
  [ -x "$p" ] || p="$(command -v python3 || true)"
  [ -n "$p" ] && PYTHONDONTWRITEBYTECODE=1 "$p" -B "$@"
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
  # The background server: the LaunchAgent where one is installed, and the
  # run loop the app started itself where none could be.
  if [ -e "$AGENT" ] || loaded; then printf 'server	%s
' "$AGENT"; fi
  if detached_pid >/dev/null; then printf 'server	%s
' "$PIDFILE"; fi
  if [ -f "$CONTENTS/Info.plist" ] && grep -q "<string>$LABEL</string>" "$CONTENTS/Info.plist"; then
    printf 'app	%s
' "$APP"
    # The copies updates keep beside it (.<name>.previous and others).
    for side in previous new unpack failed install replaced; do
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
  if [ -e "$AGENT" ] && ! rm -f "$AGENT" 2>/dev/null; then
    # Left disabled by stop, so it does not start.
    echo "Could not remove $AGENT (its folder is read-only); it is disabled and does not start."
  else
    launchctl enable "$SERVICE" 2>/dev/null || true
  fi
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
  --install) install_into "${2:-$HOME/Applications}" ;;
  --stop) stop; echo "ACTIVATE Studio is stopped; opening the app starts it again." ;;
  --uninstall) shift; uninstall "$@" ;;
  --help|-h) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "${BASH_SOURCE[0]}" ;;
  *) start ;;    # Finder may pass -psn_... on older systems
esac
