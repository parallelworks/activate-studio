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
# What the macOS app's run loop does and this one does not, for now: roll
# an update back when the new version keeps stopping (there is no update
# channel for Linux yet), and read the desktop's proxy settings (the
# server follows HTTPS_PROXY, HTTP_PROXY and NO_PROXY from the environment
# or studio.env).
#
# Usage: studio-run.sh DATA_DIR
set -u

RES="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
DATA="${1:?usage: studio-run.sh DATA_DIR}"
CONFIG="$DATA/studio.env"
LOG="$DATA/logs/studio.log"
DEFAULT_KB="${STUDIO_DEFAULT_KB:-$HOME/Documents/ACTIVATE Studio}"
RESTART=75
RELAUNCH=76

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

backoff=2
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
    # Not settings: loopback only, the restart and settings file, and where
    # the data is. Python writes no bytecode caches, which would land
    # inside the payload (read-only when a package installed it). The
    # payload's own path is not given as STUDIO_APP_BUNDLE, which is the
    # macOS app's updater: see docs/LINUX-APP.md.
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
  if [ "$code" = "$RELAUNCH" ] && [ -f "$RES/studio-run.sh" ]; then
    # An update replaced the payload: its own run loop takes over this
    # process, reading its own script rather than this one.
    echo "$(stamp) the payload was updated; its own run loop takes over" >>"$LOG"
    exec /bin/bash "$RES/studio-run.sh" "$DATA"
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
