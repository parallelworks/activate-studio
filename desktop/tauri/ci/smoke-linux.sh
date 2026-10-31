#!/bin/bash
# The shell's smoke test on a Linux runner: Xvfb, a stand-in default
# browser (an xdg-open handler that records the URL), the stream stub, a
# screenshot poller, then the shell in STUDIO_SMOKE=1 mode, whose page runs
# the checks. The first copy relaunches itself; the second closes its
# window. Afterwards: are any of the servers, or the children they started,
# still running?
#
# Usage: smoke-linux.sh SHELL_BINARY OUT_DIR REPO_ROOT
set -u
BIN="$1"
OUT="$2"
REPO="$3"
TMP="${RUNNER_TEMP:-/tmp}/studio-smoke"
rm -rf "$TMP" "$OUT"
mkdir -p "$OUT/shots" "$TMP"

export DISPLAY=:99
Xvfb :99 -screen 0 1440x900x24 -nolisten tcp >"$OUT/xvfb.log" 2>&1 &
XVFB=$!
sleep 1

# The default browser: whatever xdg-open runs for https.
mkdir -p "$HOME/.local/share/applications" "$TMP/bin"
cat >"$TMP/bin/smoke-browser" <<EOF
#!/bin/sh
echo "\$@" >>"$OUT/browser-opened.txt"
EOF
chmod +x "$TMP/bin/smoke-browser"
cat >"$HOME/.local/share/applications/smoke-browser.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Smoke Browser
Exec=$TMP/bin/smoke-browser %u
MimeType=x-scheme-handler/http;x-scheme-handler/https;
NoDisplay=true
EOF
xdg-mime default smoke-browser.desktop x-scheme-handler/https x-scheme-handler/http
export BROWSER="$TMP/bin/smoke-browser"
echo "xdg-mime https handler: $(xdg-mime query default x-scheme-handler/https)" >"$OUT/browser-setup.txt"

node "$REPO/desktop/tauri/test/stream-stub.mjs" 47123 >"$OUT/stub.log" 2>&1 &
STUB=$!

cp -r "$REPO/testdata/corpus" "$TMP/kb"
mkdir -p "$TMP/picked"

# Screenshots on request (.req -> .png, then .done). For the folder
# dialog, Escape afterwards, which cancels it.
poll() {
  while :; do
    for req in "$OUT"/shots/*.req; do
      [ -e "$req" ] || continue
      name="$(basename "$req" .req)"
      import -window root "$OUT/shots/$name.png" 2>>"$OUT/poller.log"
      if [ "${name%-*}" = dialog ]; then
        w="$(xdotool search --name 'Smoke test folder dialog' 2>/dev/null | head -n 1)"
        echo "$(date +%T) dialog window: ${w:-none}" >>"$OUT/poller.log"
        if [ -n "$w" ]; then
          xdotool windowfocus "$w" 2>>"$OUT/poller.log"
          xdotool mousemove --window "$w" 60 60 2>>"$OUT/poller.log"
        fi
        sleep 0.3
        xdotool key Escape 2>>"$OUT/poller.log"
      fi
      mv "$req" "$OUT/shots/$name.taken"
      touch "$OUT/shots/$name.done"
      echo "$(date +%T) took $name" >>"$OUT/poller.log"
    done
    sleep 0.2
  done
}
poll &
POLL=$!

export STUDIO_SMOKE=1 \
  STUDIO_SMOKE_OUT="$OUT" \
  STUDIO_SMOKE_PRELOAD="$REPO/desktop/tauri/test/smoke-preload.mjs" \
  STUDIO_SMOKE_STUB=http://127.0.0.1:47123 \
  STUDIO_SMOKE_FOLDER="$TMP/picked" \
  STUDIO_SMOKE_REAL_DIALOG=1 \
  STUDIO_APP_DIR="$REPO" \
  STUDIO_DATA_DIR="$TMP/data" \
  KB_ROOT="$TMP/kb" \
  AI_ENABLED=0

t0=$(date +%s%3N)
timeout 300 "$BIN" >"$OUT/shell.log" 2>&1
code1=$?
t1=$(date +%s%3N)
echo "first copy exited with $code1 after $((t1 - t0)) ms" | tee "$OUT/run.txt"

# The relaunched copy closes its window when done.
for _ in $(seq 1 360); do
  [ -f "$OUT/results-2.json" ] && grep -q '"stop"' "$OUT/results-2.json" && break
  sleep 0.5
done
pid2="$(node -e 'try{console.log(require(process.argv[1]).shellPid)}catch{}' "$OUT/results-2.json" 2>/dev/null)"
if [ -n "$pid2" ]; then
  for _ in $(seq 1 60); do kill -0 "$pid2" 2>/dev/null || break; sleep 0.5; done
  if kill -0 "$pid2" 2>/dev/null; then echo "second copy $pid2 still running" | tee -a "$OUT/run.txt"; else echo "second copy $pid2 exited" | tee -a "$OUT/run.txt"; fi
fi
sleep 1

# Orphans: every server and child PID recorded, and any node left besides the stub.
node -e '
const fs = require("fs")
const [pidsFile, stub, out] = process.argv.slice(1)
const alive = p => { try { process.kill(p, 0); return true } catch { return false } }
const rows = fs.existsSync(pidsFile) ? fs.readFileSync(pidsFile, "utf8").trim().split("\n").filter(Boolean).map(l => JSON.parse(l)) : []
const left = rows.flatMap(r => [r.server, r.child]).filter(alive)
let nodes = []
for (const d of fs.readdirSync("/proc")) {
  if (!/^\d+$/.test(d) || Number(d) === Number(stub) || Number(d) === process.pid) continue
  try {
    if (fs.readFileSync(`/proc/${d}/comm`, "utf8").trim() !== "node") continue
    nodes.push({ pid: Number(d), cmd: fs.readFileSync(`/proc/${d}/cmdline`, "utf8").replace(/\0/g, " ").trim() })
  } catch {}
}
const r = { serversRecorded: rows.length, pidsChecked: rows.length * 2, stillAlive: left, otherNodeProcesses: nodes, ok: left.length === 0 }
fs.writeFileSync(out, JSON.stringify(r, null, 2))
console.log(JSON.stringify(r))
' "$OUT/server-pids.jsonl" "$STUB" "$OUT/orphans.json"

# What outlives the shell when it is ended from outside: TERM (handled:
# the server's process group is stopped) and KILL (not catchable: only the
# server hears of it, through PR_SET_PDEATHSIG).
kill_test() {
  local phase="$1" sig="$2" inst="$3" before spid exited
  before=$(wc -l <"$OUT/server-pids.jsonl" 2>/dev/null || echo 0)
  STUDIO_SMOKE_PHASE="$phase" "$BIN" >>"$OUT/shell-$phase.log" 2>&1 &
  spid=$!
  for _ in $(seq 1 120); do
    [ -f "$OUT/results-$inst.json" ] && grep -q '"coldStartMs"' "$OUT/results-$inst.json" && break
    sleep 0.5
  done
  sleep 2
  kill -"$sig" "$spid"
  exited=false
  for _ in $(seq 1 40); do kill -0 "$spid" 2>/dev/null || { exited=true; break; }; sleep 0.5; done
  wait "$spid" 2>/dev/null
  sleep 2
  node -e '
const fs = require("fs")
const [file, before, sig, exited, out] = process.argv.slice(1)
const alive = p => { try { process.kill(p, 0); return true } catch { return false } }
const rows = fs.readFileSync(file, "utf8").trim().split("\n").filter(Boolean).slice(Number(before)).map(l => JSON.parse(l))
const r = { signal: sig, shellExited: exited === "true", servers: rows, alive: rows.flatMap(x => [x.server, x.child]).filter(alive) }
fs.writeFileSync(out, JSON.stringify(r, null, 2))
console.log(JSON.stringify(r))
for (const p of r.alive) { try { process.kill(p, "SIGKILL") } catch {} }
' "$OUT/server-pids.jsonl" "$before" "$sig" "$exited" "$OUT/kill-$phase.json"
}
kill_test T TERM 3
kill_test K KILL 4

cp "$TMP/data/logs/"*.log "$OUT/" 2>/dev/null
ls -la "$HOME/Downloads" >"$OUT/downloads-dir.txt" 2>&1
kill "$POLL" "$STUB" 2>/dev/null
kill "$XVFB" 2>/dev/null
exit 0
