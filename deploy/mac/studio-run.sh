#!/bin/bash
# The Studio's server, kept running by the LaunchAgent the app installs
# (Contents/Resources/studio-run.sh in the app), or, where no LaunchAgent can
# be installed, started by the app itself (launcher.sh, with
# STUDIO_SUPERVISOR=detached). Settings are read from studio.env before
# every start. The server starts again at once when it asks for a restart
# (exit 75) and after a short pause when it stops for any other reason; a
# TERM stops it and this loop. Exit 76, which a server the app started
# itself uses after an update replaced the app, runs the new copy's own run
# loop in place of this one.
#
# After an update (DATA/updates/pending.json, written by the server when it
# swaps a new copy of the app in), a server that stops three times within
# two minutes is taken as broken: the previous copy, kept beside the app as
# .<name>.previous, is put back and started instead, and
# DATA/updates/rolled-back.json says so. A server that stays up a minute
# clears the mark itself.
#
# Usage: studio-run.sh DATA_DIR
set -u

RES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
DATA="${1:?usage: studio-run.sh DATA_DIR}"
CONFIG="$DATA/studio.env"
LOG="$DATA/logs/studio.log"
DEFAULT_KB="$HOME/Documents/ACTIVATE Studio"
RESTART=75
RELAUNCH=76
# The app this script is in; the agent names it, and an agent written
# before it did leaves it to be worked out here.
APP="${STUDIO_APP_BUNDLE:-$(cd "$RES/../.." && pwd -P)}"
PENDING="$DATA/updates/pending.json"
PREVIOUS="$(dirname "$APP")/.$(basename "$APP").previous"

mkdir -p "$DATA/index/models" "$DATA/logs"

pid=
stopping=0
trap 'stopping=1; [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null' TERM INT HUP

stamp() { date '+%Y-%m-%d %H:%M:%S'; }

# The PATH the person's login shell sets, so programs they run by name in
# Terminal (the pw CLI above all) are found by the server too: launchd
# starts it with a short PATH. Interactive as well as login, since many set
# PATH in .zshrc; given five seconds, since a shell's startup files can wait
# on input or the network; empty when the shell does not answer.
login_path() {
  local sh out n=0 pid
  sh="${SHELL:-}"
  [ -x "$sh" ] || sh="$(dscl . -read "/Users/$(id -un)" UserShell 2>/dev/null | awk '{print $2}')"
  [ -x "$sh" ] || return 0
  out="$(mktemp "${TMPDIR:-/tmp}/studio-path.XXXXXX")" || return 0
  "$sh" -ilc 'printf "\n__STUDIO_PATH__=%s\n" "$PATH"' </dev/null >"$out" 2>/dev/null &
  pid=$!
  while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 50 ]; do sleep 0.1; n=$((n + 1)); done
  kill "$pid" 2>/dev/null
  sed -n 's/^__STUDIO_PATH__=//p' "$out" | tail -n 1
  rm -f "$out"
}
LOGIN_PATH="$(login_path)"

# One string from pending.json.
field() { sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" "$PENDING" 2>/dev/null | head -n 1; }

# Put the previous copy of the app back and run its own loop in place of
# this one (the same process, so launchd sees no exit).
roll_back() {
  local from to failed
  from="$(field to)"
  to="$(field from)"
  if [ ! -d "$PREVIOUS" ]; then
    echo "$(stamp) ${from:-the new version} keeps stopping, and there is no previous copy to go back to" >>"$LOG"
    rm -f "$PENDING"
    return
  fi
  failed="$(dirname "$APP")/.$(basename "$APP").failed"
  rm -rf "$failed"
  if ! mv "$APP" "$failed"; then
    echo "$(stamp) could not move $APP aside to roll back" >>"$LOG"
    return
  fi
  if ! mv "$PREVIOUS" "$APP"; then
    mv "$failed" "$APP"
    echo "$(stamp) could not put $PREVIOUS back" >>"$LOG"
    return
  fi
  printf '{\n "from": "%s",\n "to": "%s",\n "at": "%s",\n "reason": "it stopped %s times within two minutes of starting"\n}\n' \
    "${from:-unknown}" "${to:-unknown}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >"$DATA/updates/rolled-back.json"
  rm -f "$PENDING"
  rm -rf "$failed"
  echo "$(stamp) ${from:-the new version} stopped $1 times within two minutes; ${to:-the previous version} is back" >>"$LOG"
  exec /bin/bash "$APP/Contents/Resources/studio-run.sh" "$DATA"
}

backoff=2
failures=
while [ "$stopping" = 0 ]; do
  # One previous log is kept.
  if [ -f "$LOG" ] && [ $(( $(wc -c <"$LOG") )) -gt 10485760 ]; then mv -f "$LOG" "$LOG.1"; fi
  (
    unset KB_ROOT
    # Defaults that studio.env may change; everything it sets is exported.
    export PORT=4180 INDEX_BASE="$DATA/index" PYTHON_BIN="$RES/python/bin/python3" \
      STUDIO_GUFI_PREFIX="$RES/gufi" NODE_ENV=production
    if [ -f "$CONFIG" ]; then
      set -a
      # shellcheck source=/dev/null
      . "$CONFIG"
      set +a
    fi
    if [ -n "${KB_ROOT:-}" ]; then
      # A folder the person chose holds their own material: no starter
      # folders are written into it.
      [ -n "${KB_STARTER_DIRS+set}" ] || export KB_STARTER_DIRS=
    else
      # The default folder; on first start the server writes a README and
      # a few starter folders into it.
      export KB_ROOT="$DEFAULT_KB"
      mkdir -p "$KB_ROOT"
    fi
    # Not settings: loopback only, the launcher's restart and settings file,
    # and where the app and its data are, for updates. Python writes no
    # bytecode caches, which would land inside the app and break its
    # signature's seal.
    export HOST=127.0.0.1 STUDIO_CONFIG_FILE="$CONFIG" STUDIO_RESTARTABLE=1 \
      STUDIO_APP_BUNDLE="$APP" STUDIO_DATA_DIR="$DATA" PYTHONDONTWRITEBYTECODE=1
    # The app's tesseract comes ahead of any other, with its English model
    # and configs, unless studio.env names another TESSDATA_PREFIX.
    export PATH="$RES/node/bin:$RES/ocr/bin:${LOGIN_PATH:+$LOGIN_PATH:}${PATH:-/usr/bin:/bin:/usr/sbin:/sbin}:/usr/local/bin:/opt/homebrew/bin:$HOME/.local/bin"
    export TESSDATA_PREFIX="${TESSDATA_PREFIX:-$RES/ocr/share/tessdata}"
    # The embedding models ship with the app; the index refers to them.
    model="$INDEX_BASE/models/minilm384.gguf"
    if [ -L "$model" ] || [ ! -e "$model" ]; then
      mkdir -p "$INDEX_BASE/models" && ln -sfn "$RES/models/minilm384.gguf" "$model"
    fi
    onnx="$INDEX_BASE/models/minilm-l6"
    if [ -d "$RES/models/minilm-l6" ] && { [ -L "$onnx" ] || [ ! -e "$onnx" ]; }; then
      ln -sfn "$RES/models/minilm-l6" "$onnx"
    fi
    echo "$(stamp) starting on http://127.0.0.1:$PORT/ with $KB_ROOT"
    exec "$RES/node/bin/node" "$RES/app/server/dist/main.js"
  ) >>"$LOG" 2>&1 &
  pid=$!
  started=$(date +%s)
  wait "$pid"
  code=$?
  # A signal ends the wait early; the server still finishes its own stop.
  while kill -0 "$pid" 2>/dev/null; do wait "$pid"; code=$?; done
  pid=
  [ "$stopping" = 1 ] && break
  if [ "$code" = "$RELAUNCH" ] && [ -f "$APP/Contents/Resources/studio-run.sh" ]; then
    # An update put a new copy of the app in place: its run loop takes over
    # this process (so the PID the app recorded stays right), reading its
    # own script rather than this one, and watches the update's mark.
    echo "$(stamp) the app was updated; its own run loop takes over" >>"$LOG"
    exec /bin/bash "$APP/Contents/Resources/studio-run.sh" "$DATA"
  fi
  if [ -f "$PENDING" ]; then
    now=$(date +%s)
    if [ $(( now - started )) -gt 60 ]; then
      rm -f "$PENDING"
      failures=
    elif [ "$code" != "$RESTART" ]; then
      # Stops in the last two minutes, this one included.
      recent='' n=0
      for t in $failures $now; do
        if [ $(( now - t )) -le 120 ]; then recent="$recent $t"; n=$((n + 1)); fi
      done
      failures="$recent"
      [ "$n" -ge 3 ] && roll_back "$n"
    fi
  fi
  if [ "$code" = "$RESTART" ]; then
    backoff=2
    continue
  fi
  [ $(( $(date +%s) - started )) -gt 60 ] && backoff=2
  echo "$(stamp) the server stopped with status $code; starting it again in ${backoff}s" >>"$LOG"
  sleep "$backoff" &
  wait $!
  backoff=$(( backoff * 2 > 60 ? 60 : backoff * 2 ))
done
exit 0
