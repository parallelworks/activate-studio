#!/bin/bash
# The Studio's server for the Linux app: runs it with the payload's own
# Node, Python, GUFI and tesseract, and keeps it running. The payload is a
# relocatable tree and this script is at its top, so it runs from any path.
# Settings are read from studio.env in the data directory before every
# start. The server starts again at once when it asks for a restart (exit
# 75) and after a short pause when it stops for any other reason; a TERM
# stops it and this loop. Exit 76, which the server uses after an update
# replaced the payload, runs the new copy's own run loop in place of this
# one.
#
# After an update (DATA/updates/pending.json, written by the server when it
# swaps a new payload in; only a per-user install updates itself), a server
# that stops three times within two minutes is taken as broken: the previous
# payload, kept beside this one as .<name>.previous, is put back and started
# instead, and DATA/updates/rolled-back.json says so. A server that stays up
# a minute clears the mark itself.
#
# An update replaces the payload's directory in two moves, so a process cut
# off between them leaves no payload at its path. Before each start, and
# when the server stops, this loop looks for that (studio-recover.sh, read in
# at the start so that it is still here when the directory is not): it puts
# the previous payload back, or finishes the swap when the new one is
# complete, and then runs whichever copy is now at its path.
#
# What the macOS app's run loop does and this one does not, for now: read
# the desktop's proxy settings (the server follows HTTPS_PROXY, HTTP_PROXY
# and NO_PROXY from the environment or studio.env).
#
# With --once the script starts the server one time and exits with its exit
# status, for the desktop window (desktop/wails), which does the restarting
# itself: it starts the server again after 75, and after 76 starts the
# updated payload's copy of this script. STUDIO_RUN_PORT is the port to use
# unless studio.env names one (4180 when neither is set). Without --once
# the PORT default is 4180 as before.
#
# With --rollback the script only does the rollback described above, for a
# window that supervises the server itself and has counted the stops:
# studio-run.sh --rollback DATA_DIR [STOPS].
#
# Usage: studio-run.sh [--once | --rollback] DATA_DIR
set -u

ONCE=0 ROLLBACK_ONLY=0
case "${1:-}" in
  --once) ONCE=1; shift ;;
  --rollback) ROLLBACK_ONLY=1; shift ;;
esac
RES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
DATA="${1:?usage: studio-run.sh [--once | --rollback] DATA_DIR}"
CONFIG="$DATA/studio.env"
LOG="$DATA/logs/studio.log"
DEFAULT_KB="${STUDIO_DEFAULT_KB:-$HOME/Documents/ACTIVATE Studio}"
RESTART=75
RELAUNCH=76
PENDING="$DATA/updates/pending.json"
PREVIOUS="$(dirname "$RES")/.$(basename "$RES").previous"

mkdir -p "$DATA/index/models" "$DATA/logs"

pid=
stopping=0
trap 'stopping=1; [ -n "$pid" ] && kill -TERM "$pid" 2>/dev/null' TERM INT HUP

stamp() { date '+%Y-%m-%d %H:%M:%S'; }

# The PATH the person's login shell sets, so programs they run by name in a
# terminal (the pw CLI above all) are found by the server too: a service or
# a desktop launcher starts it with a short PATH. Interactive as well as
# login, since many set PATH in .bashrc; given five seconds, since a
# shell's startup files can wait on input or the network; empty when the
# shell does not answer. The same shell's KRB5CCNAME, when it sets one,
# comes along, so ssh to a host that takes Kerberos tickets finds the cache
# the person's terminal uses.
login_env() {
  local sh out n=0 pid
  sh="${SHELL:-}"
  [ -x "$sh" ] || sh="$(getent passwd "$(id -un)" 2>/dev/null | cut -d: -f7)"
  [ -x "$sh" ] || return 0
  out="$(mktemp "${TMPDIR:-/tmp}/studio-path.XXXXXX")" || return 0
  "$sh" -ilc 'printf "\n__STUDIO_PATH__=%s\n__STUDIO_KRB5CCNAME__=%s\n" "$PATH" "${KRB5CCNAME:-}"' </dev/null >"$out" 2>/dev/null &
  pid=$!
  while kill -0 "$pid" 2>/dev/null && [ "$n" -lt 50 ]; do sleep 0.1; n=$((n + 1)); done
  kill "$pid" 2>/dev/null
  grep -E '^__STUDIO_(PATH|KRB5CCNAME)__=' "$out" | tail -n 2
  rm -f "$out"
}
LOGIN_ENV="$(login_env)"
LOGIN_PATH="$(printf '%s\n' "$LOGIN_ENV" | sed -n 's/^__STUDIO_PATH__=//p' | tail -n 1)"
LOGIN_KRB5CCNAME="$(printf '%s\n' "$LOGIN_ENV" | sed -n 's/^__STUDIO_KRB5CCNAME__=//p' | tail -n 1)"
[ -n "${KRB5CCNAME:-}" ] || [ -z "$LOGIN_KRB5CCNAME" ] || export KRB5CCNAME="$LOGIN_KRB5CCNAME"

# One string from pending.json.
field() { sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" "$PENDING" 2>/dev/null | head -n 1; }

# The recovery function, read in while the payload is here.
# shellcheck source=studio-recover.sh
[ -f "$RES/studio-recover.sh" ] && . "$RES/studio-recover.sh"

# Finish or undo a cut-off update (see the top). Cheap when nothing is wrong:
# two tests. Runs the loop of the payload it put back or completed in place
# of this one.
recover() {
  type studio_recover >/dev/null 2>&1 || return 0
  [ -f "$RES/studio-run.sh" ] && [ ! -f "$DATA/updates/swap.json" ] && return 0
  studio_recover "$RES" "$DATA" >/dev/null 2>&1
  if [ $? = 10 ] && [ -f "$RES/studio-run.sh" ]; then
    if [ "$ONCE" = 1 ]; then exec /bin/bash "$RES/studio-run.sh" --once "$DATA"; fi
    exec /bin/bash "$RES/studio-run.sh" "$DATA"
  fi
  return 0
}

# Put the previous payload back and run its own loop in place of this one.
roll_back() {
  local from to failed
  from="$(field to)"
  to="$(field from)"
  if [ ! -d "$PREVIOUS" ]; then
    echo "$(stamp) ${from:-the new version} keeps stopping, and there is no previous copy to go back to" >>"$LOG"
    rm -f "$PENDING"
    return
  fi
  failed="$(dirname "$RES")/.$(basename "$RES").failed"
  # The payload has read-only directories (Python's), which rm cannot empty.
  chmod -R u+w "$failed" 2>/dev/null
  rm -rf "$failed"
  if ! mv "$RES" "$failed"; then
    echo "$(stamp) could not move $RES aside to roll back" >>"$LOG"
    return
  fi
  if ! mv "$PREVIOUS" "$RES"; then
    mv "$failed" "$RES"
    echo "$(stamp) could not put $PREVIOUS back" >>"$LOG"
    return
  fi
  printf '{\n "from": "%s",\n "to": "%s",\n "at": "%s",\n "reason": "it stopped %s times within two minutes of starting"\n}\n' \
    "${from:-unknown}" "${to:-unknown}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" >"$DATA/updates/rolled-back.json"
  rm -f "$PENDING"
  chmod -R u+w "$failed" 2>/dev/null
  rm -rf "$failed"
  echo "$(stamp) ${from:-the new version} stopped $1 times within two minutes; ${to:-the previous version} is back" >>"$LOG"
  [ "$ROLLBACK_ONLY" = 1 ] && return 0
  exec /bin/bash "$RES/studio-run.sh" "$DATA"
}

if [ "$ROLLBACK_ONLY" = 1 ]; then roll_back "${2:-3}"; exit 0; fi

backoff=2
failures=
while [ "$stopping" = 0 ]; do
  recover
  # An update that replaces the whole app (requiresRelaunch, docs/UPDATES.md)
  # may hold no Node: this loop, the old copy's, stops, and the reopened app
  # starts its own server.
  if [ ! -x "$RES/node/bin/node" ] && [ -f "$DATA/updates/relaunch.json" ]; then
    echo "$(stamp) the app was replaced by a version that runs once the app is reopened; this run loop stops" >>"$LOG"
    exit 0
  fi
  # One previous log is kept.
  if [ -f "$LOG" ] && [ $(( $(wc -c <"$LOG") )) -gt 10485760 ]; then mv -f "$LOG" "$LOG.1"; fi
  (
    unset KB_ROOT
    # Defaults that studio.env may change; everything it sets is exported.
    export PORT="${STUDIO_RUN_PORT:-4180}" INDEX_BASE="$DATA/index" PYTHON_BIN="$RES/python/bin/python3" \
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
    # Not settings: loopback only, the restart and settings file, and where
    # the data is. Python writes no bytecode caches, which would land
    # inside the payload (read-only when a package installed it). The
    # payload's own path is not given as STUDIO_APP_BUNDLE, which is the
    # macOS app's updater: the server finds the payload it runs from (see
    # docs/LINUX-APP.md, Updates).
    export HOST=127.0.0.1 STUDIO_CONFIG_FILE="$CONFIG" STUDIO_RESTARTABLE=1 \
      STUDIO_DATA_DIR="$DATA" PYTHONDONTWRITEBYTECODE=1
    # The payload's Node and tesseract come ahead of any other, with its
    # English model and configs, unless studio.env names another
    # TESSDATA_PREFIX.
    export PATH="$RES/node/bin:$RES/ocr/bin:${LOGIN_PATH:+$LOGIN_PATH:}${PATH:-/usr/bin:/bin}:/usr/local/bin:$HOME/.local/bin"
    export TESSDATA_PREFIX="${TESSDATA_PREFIX:-$RES/ocr/share/tessdata}"
    # The embedding models ship with the payload; the index refers to them.
    model="$INDEX_BASE/models/minilm384.gguf"
    if [ -L "$model" ] || [ ! -e "$model" ]; then
      mkdir -p "$INDEX_BASE/models" && ln -sfn "$RES/models/minilm384.gguf" "$model"
    fi
    onnx="$INDEX_BASE/models/minilm-l6"
    if [ -d "$RES/models/minilm-l6" ] && { [ -L "$onnx" ] || [ ! -e "$onnx" ]; }; then
      ln -sfn "$RES/models/minilm-l6" "$onnx"
    fi
    # The window's opt-in to the Go front server (STUDIO_GO_SERVER=1 in the
    # shell, which names the binary): it listens on PORT and starts this
    # Node server behind it on a loopback port, passing on Node's exit
    # status (docs/GO-PORT.md).
    if [ -n "${STUDIO_GO_SERVER_BIN:-}" ] && [ -x "$STUDIO_GO_SERVER_BIN" ]; then
      echo "$(stamp) starting the Go server on http://127.0.0.1:$PORT/ with $KB_ROOT, Node behind it"
      export STUDIO_NODE_SPAWN=1 STUDIO_NODE_BIN="$RES/node/bin/node" \
        STUDIO_NODE_MAIN="$RES/app/server/dist/main.js" STUDIO_PROJECT_ROOT="$RES/app"
      exec "$STUDIO_GO_SERVER_BIN"
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
  [ "$ONCE" = 1 ] && exit "$code"
  if [ "$code" = "$RELAUNCH" ] && [ -f "$RES/studio-run.sh" ]; then
    # An update replaced the payload: its own run loop takes over this
    # process, reading its own script rather than this one.
    echo "$(stamp) the payload was updated; its own run loop takes over" >>"$LOG"
    exec /bin/bash "$RES/studio-run.sh" "$DATA"
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
