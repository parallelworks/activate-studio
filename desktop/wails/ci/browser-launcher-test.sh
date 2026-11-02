#!/bin/bash
# The Firefox launcher (packaging/linux/activate-studio-browser) under Xvfb
# against the real Studio: starts, shows the Studio, restarts the server after
# exit 76, ends the server when Firefox is closed, ends it on TERM, and what
# KILL of the launcher leaves behind.
#
# Usage: browser-launcher-test.sh LAUNCHER OUT_DIR
# Env:   DISPLAY, STUDIO_APP_DIR, STUDIO_NODE
set -u
LAUNCHER=$1 OUT=$2
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)
R="$OUT/results.txt"; : >"$R"
res() { echo "$*" | tee -a "$R"; }

# A preload the server reads (passed as an argument to the server only: through
# NODE_OPTIONS every node the server starts would run it, and the helper it
# starts would start helpers of its own without end), to make it exit with a chosen code on request.
cat >"$OUT/preload.mjs" <<'JS'
import fs from 'node:fs'
import path from 'node:path'
import { spawn } from 'node:child_process'
const dir = process.env.TEST_DIR
const helper = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1e6)'], { stdio: 'ignore' })
helper.unref()
fs.appendFileSync(path.join(dir, 'pids.log'), JSON.stringify({ server: process.pid, helper: helper.pid }) + '\n')
const trigger = path.join(dir, 'trigger')
setInterval(() => {
  let code
  try { code = Number(fs.readFileSync(trigger, 'utf8').trim()); fs.rmSync(trigger) } catch { return }
  if (Number.isInteger(code)) process.exit(code)
}, 100).unref()
JS

alive() { [ -r "/proc/$1/stat" ] && [ "$(awk '{print $3}' "/proc/$1/stat")" != Z ]; }
start_launcher() {   # $1 = label; sets LPID DATA
  DATA=$(mktemp -d); rm -f "$OUT/pids.log" "$OUT/trigger"
  mkdir -p "$DATA/kb"
  env HOME="$DATA" STUDIO_SHELL_DATA_DIR="$DATA" STUDIO_KB_ROOT="$DATA/kb" TEST_DIR="$OUT" \
    STUDIO_NODE_ARGS="--import file://$OUT/preload.mjs" MOZ_DISABLE_CONTENT_SANDBOX=1 MOZ_DISABLE_GMP_SANDBOX=1 \
    "$LAUNCHER" >"$OUT/$1-launcher.out" 2>&1 &
  LPID=$!
}
wait_log() {   # $1 = pattern, $2 = seconds
  for _ in $(seq 1 $(( $2 * 2 ))); do grep -q "$1" "$DATA/logs/launcher.log" 2>/dev/null && return 0; sleep 0.5; done; return 1
}
server_pids() { jq -r '.server' "$OUT/pids.log" 2>/dev/null; }
helper_pids() { jq -r '.helper' "$OUT/pids.log" 2>/dev/null; }
firefox_version=$(firefox --version 2>&1); res "firefox: $firefox_version"

echo "=== 1 start, show the Studio"
start_launcher t1
if wait_log "firefox started" 90; then res "start: ok (server answered, firefox started)"; else res "start: FAIL"; fi
sleep 20
import -window root "$OUT/studio-in-firefox.png" 2>>"$OUT/import.log"
PORT1=$(sed -n 's/.*server answers on port \([0-9]*\).*/\1/p' "$DATA/logs/launcher.log" | head -n 1)
res "port $PORT1; launcher pid $LPID alive: $(alive $LPID && echo yes || echo no)"
res "firefox processes: $(ps -eo pid=,comm= | awk '$2 ~ /^firefox/ {n++} END {print n+0}')"
xdotool search --name "Mozilla Firefox" 2>/dev/null | head -n 3 | while read w; do res "window $w: $(xdotool getwindowname $w)"; done

echo "=== 2 server exit 76 restarts the server on the same port"
S1=$(server_pids | tail -n 1)
echo 76 >"$OUT/trigger"
if wait_log "answers again" 60; then
  S2=$(server_pids | tail -n 1)
  PORT2=$(sed -n 's/.*answers again on port \([0-9]*\).*/\1/p' "$DATA/logs/launcher.log" | tail -n 1)
  res "restart after 76: ok (server $S1 -> $S2, port $PORT1 -> $PORT2, launcher alive: $(alive $LPID && echo yes || echo no), old server alive: $(alive $S1 && echo yes || echo no))"
else res "restart after 76: FAIL"; fi
sleep 5
import -window root "$OUT/after-restart.png" 2>>"$OUT/import.log"

echo "=== 3 close the Firefox window: the launcher and the server end"
SRV=$(server_pids | tail -n 1); HLP=$(helper_pids | tail -n 1)
W=$(xdotool search --onlyvisible --class firefox 2>/dev/null | head -n 1)
if [ -n "$W" ]; then xdotool windowfocus "$W"; sleep 0.5; xdotool key --clearmodifiers ctrl+q; res "sent ctrl+q to window $W"; else res "no firefox window found to close"; fi
for _ in $(seq 1 40); do alive $LPID || break; sleep 0.5; done
res "after closing firefox: launcher alive: $(alive $LPID && echo yes || echo no); server $SRV alive: $(alive $SRV && echo yes || echo no); helper $HLP alive: $(alive $HLP && echo yes || echo no)"
tail -n 8 "$DATA/logs/launcher.log" | tee -a "$OUT/t1-launcher-tail.txt"
alive $LPID && { kill -TERM $LPID 2>/dev/null; sleep 2; }

echo "=== 4 TERM to the launcher"
start_launcher t4
wait_log "firefox started" 90 || res "t4 start: FAIL"
sleep 5
SRV=$(server_pids | tail -n 1); HLP=$(helper_pids | tail -n 1)
kill -TERM $LPID
sleep 6
res "after TERM: launcher alive: $(alive $LPID && echo yes || echo no); server $SRV alive: $(alive $SRV && echo yes || echo no); helper $HLP alive: $(alive $HLP && echo yes || echo no); firefox processes: $(ps -eo pid=,comm= | awk '$2 ~ /^firefox/ {n++} END {print n+0}')"

echo "=== 5 KILL of the launcher"
start_launcher t5
wait_log "firefox started" 90 || res "t5 start: FAIL"
sleep 5
SRV=$(server_pids | tail -n 1); HLP=$(helper_pids | tail -n 1)
kill -KILL $LPID
sleep 6
res "after KILL: server $SRV alive: $(alive $SRV && echo yes || echo no); helper $HLP alive: $(alive $HLP && echo yes || echo no); firefox processes: $(ps -eo pid=,comm= | awk '$2 ~ /^firefox/ {n++} END {print n+0}')"
# Leave the runner clean: end what is left, by PID.
for p in $SRV $HLP; do alive "$p" && kill -KILL "$p" 2>/dev/null; done
for p in $(ps -eo pid=,comm= | awk '$2 ~ /^firefox/ {print $1}'); do kill -TERM "$p" 2>/dev/null; done
exit 0
