#!/usr/bin/env bash
# A built library across a restart that interrupts its rebuild, on a Mac
# where ~/Library/LaunchAgents is read-only (the app runs its server itself).
#
# The app is installed from the disk image into ~/Applications and opened.
# A second library is built of a local folder of about 2,600 files (names,
# a set of files of one size, pictures and scanned PDFs whose text only OCR
# reads) at the Text + OCR depth, and waited for until its words are found.
# Then 600 large pictures are added and the library rebuilt, so OCR runs
# for minutes; while it runs, the server is stopped as a shutdown stops it (TERM to the run
# loop, and the window quit), and the app opened again. Right after it
# answers, before anything asks for GET /api/libraries, the library is
# searched by a word only OCR reads and by a name, and queried across all
# libraries by name, size and words; again once the rebuild has finished.
#
# Asserted at every stage: the library's names and sizes are there. After
# the restart, its words are there too (the old index answers until the
# rebuilt one is swapped in).
#
# Usage: mac-restart-e2e.sh DMG_URL OUT_DIR
set -uo pipefail

URL="$1"
OUT="$2"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
APP_NAME="ACTIVATE Studio"
DEST="$HOME/Applications/$APP_NAME.app"
DATA="$HOME/Library/Application Support/$APP_NAME"
LIBS="$DATA/index/libraries.json"
API=http://127.0.0.1:4180
SRC="$HOME/studio-restart/corpus"
mkdir -p "$OUT/shots"
SUMMARY="$OUT/summary.txt"
T0=$(date +%s)
FAILS=0
log() { printf '%5ss  %s\n' "$(( $(date +%s) - T0 ))" "$*" | tee -a "$SUMMARY"; }
collect() {
  [ -d "$DATA/logs" ] && cp -R "$DATA/logs" "$OUT/logs" 2>/dev/null
  mkdir -p "$OUT/index-logs"
  find "$DATA/index" -maxdepth 4 -name '*.log' -exec cp {} "$OUT/index-logs/" \; 2>/dev/null
  cp "$LIBS" "$OUT/libraries.json" 2>/dev/null
  return 0
}
fatal() { log "FAIL: $*"; screencapture -x "$OUT/shots/zz-failure.png" 2>/dev/null; collect; exit 1; }
check() { if eval "$2"; then log "ok: $1"; else log "FAILED: $1"; FAILS=$((FAILS + 1)); fi; }
api() { curl -fsS --max-time 30 "$API$1"; }
json() { python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {"d": d}))' "$1"; }
lib_state() { python3 - "$LIBS" "$LIB" <<'PY' 2>/dev/null
import json, sys
d = json.load(open(sys.argv[1]))
libs = d.get('libraries', d) if isinstance(d, dict) else d
for l in libs:
    if l.get('id') == sys.argv[2]:
        b = l.get('build') or {}
        print(f"{b.get('state')} files={b.get('files')} builtAt={b.get('builtAt')} error={b.get('error')}")
PY
}
wait_server() { for _ in $(seq 1 150); do api /api/version >/dev/null 2>&1 && return 0; sleep 2; done; return 1; }
# One stage: counts, then screenshots, with the counts kept in $C.
stage() {
  C="$(cd "$P" && node mac-restart-e2e.mjs "$1" "$OUT" "$LIB" 2>>"$OUT/playwright.log" | tail -1)"
  log "counts at $1: $C"
}
cnt() { echo "$C" | json "$1"; }

# The app, from the image, with its own --install.
mkdir -p "$HOME/Library/LaunchAgents" && chmod 555 "$HOME/Library/LaunchAgents"
log "~/Library/LaunchAgents is read-only: $(stat -f '%Sp' "$HOME/Library/LaunchAgents")"
curl -fsSL -o "$OUT/../app.dmg" "$URL" || fatal "could not download $URL"
MNT="$(mktemp -d)"
hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$OUT/../app.dmg" >/dev/null || fatal "could not mount the image"
"$MNT/$APP_NAME.app/Contents/MacOS/$APP_NAME" --install >>"$SUMMARY" 2>&1 || fatal "--install failed"
hdiutil detach "$MNT" >/dev/null 2>&1 || true
log "installed $(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$DEST/Contents/Info.plist") in $DEST"

# The folder: 600 named files, 300 of 7777 bytes, 1,300 notes in 40
# folders, 300 pictures and 100 scanned PDFs with text only OCR reads, and
# 20 PDFs with text.
mkdir -p "$SRC"
for i in $(seq -w 1 600); do d="$SRC/ledgers/$(( 10#$i % 20 ))"; mkdir -p "$d"; printf 'ledger %s entry for account %s\n' "$i" "$i" >"$d/ledger-0$i.txt"; done
for i in $(seq -w 1 300); do d="$SRC/blobs/$(( 10#$i % 10 ))"; mkdir -p "$d"; head -c 7777 /dev/zero | tr '\0' 'x' >"$d/size-$i.dat"; done
for i in $(seq -w 1 1300); do d="$SRC/notes/$(( 10#$i % 40 ))"; mkdir -p "$d"; printf '# Note %s\n\nplain text note number %s\n' "$i" "$i" >"$d/note-$i.md"; done
for i in $(seq -w 1 300); do
  d="$SRC/scans/$(( 10#$i % 15 ))"; mkdir -p "$d"
  case $(( 10#$i % 3 )) in 0) cp "$REPO/deploy/mac/testdata/ocr-sample.png" "$d/scan-$i.png" ;; 1) cp "$REPO/deploy/mac/testdata/ocr-sample.jpg" "$d/scan-$i.jpg" ;; *) cp "$REPO/deploy/mac/testdata/ocr-sample.webp" "$d/scan-$i.webp" ;; esac
done
for i in $(seq -w 1 100); do d="$SRC/scanned-pdfs/$(( 10#$i % 5 ))"; mkdir -p "$d"; cp "$REPO/deploy/mac/testdata/ocr-sample.pdf" "$d/scanned-$i.pdf"; done
mkdir -p "$SRC/reports" && for i in $(seq -w 1 20); do cp "$REPO/testdata/corpus/docs/report.pdf" "$SRC/reports/report-$i.pdf"; done
log "the folder holds $(find "$SRC" -type f | wc -l | tr -d ' ') files"

# Playwright, for the pages.
P="$(mktemp -d)"
(cd "$P" && npm init -y >/dev/null && npm install --no-audit --no-fund playwright@1.64.1 >/dev/null 2>&1 && npx playwright install chromium >/dev/null 2>&1) \
  || fatal "Playwright could not be installed"
cp "$HERE/mac-restart-e2e.mjs" "$P/"

# Opened as a person opens it; the server starts without a LaunchAgent.
open "$DEST" &
wait_server || fatal "the server did not answer"
log "server: $(api /api/version)"
[ -s "$DATA/server.pid" ] || fatal "no server.pid: the app did not start the server itself"
log "run loop: server.pid $(head -1 "$DATA/server.pid")"

# The library, at Text + OCR.
r="$(curl -fsS -X POST -H 'Content-Type: application/json' "$API/api/libraries/build" \
  -d "{\"sourceRoot\": \"$SRC\", \"label\": \"Corpus\", \"depth\": \"ocr\"}")" || fatal "the build could not be started: $r"
LIB="$(echo "$r" | json 'd["library"]["id"]')"
JOB="$(echo "$r" | json 'd["job"]["id"]')"
log "library $LIB, build job $JOB"
for _ in $(seq 1 450); do
  j="$(api "/api/index/job/$JOB" 2>/dev/null)" || { sleep 2; continue; }
  s="$(echo "$j" | json 'd["state"]')"
  [ "$s" = done ] && break
  [ "$s" = error ] || [ "$s" = cancelled ] && fatal "the build ended $s: $(echo "$j" | json 'd["error"]')"
  sleep 2
done
log "build: $(api "/api/index/job/$JOB" | json '[(p["id"], p["state"], p["done"], p["total"], p["ms"]) for p in d["phases"]]'), state $(lib_state)"
for _ in $(seq 1 60); do
  n="$(api "/api/search?q=heliotrope&library=all&limit=1000" | json "sum(1 for h in d.get('hits', []) if (h.get('library') or {}).get('id') == '$LIB')")"
  [ "${n:-0}" -gt 0 ] && break; sleep 3
done
log "searchable by words: $n hits for heliotrope from $LIB"

stage 1-after-build
check "after the build: names in search" '[ "$(cnt "d[\"searchName\"][\"lib\"]")" -gt 0 ]'
check "after the build: names in Query (600)" '[ "$(cnt "d[\"query_name\"][\"lib\"]")" -eq 600 ]'
check "after the build: sizes in Query (300)" '[ "$(cnt "d[\"query_size\"][\"lib\"]")" -eq 300 ]'
check "after the build: words in search" '[ "$(cnt "d[\"searchWord\"][\"lib\"]")" -gt 0 ]'
check "after the build: words in Query" '[ "$(cnt "d[\"query_words\"][\"lib\"]")" -gt 0 ]'
WORDS_BEFORE="$(cnt 'd["query_words"]["lib"]')"

# Large pictures, so the rebuild's OCR takes minutes, then the rebuild.
BIG="$HOME/studio-restart/big.png"
sips --resampleWidth 3000 "$REPO/deploy/mac/testdata/ocr-sample.png" --out "$BIG" >/dev/null 2>&1 || cp "$REPO/deploy/mac/testdata/ocr-sample.png" "$BIG"
log "large picture: $(sips -g pixelWidth -g pixelHeight "$BIG" 2>/dev/null | awk '/pixel/ {print $2}' | tr '\n' ' '); the app's tesseract reads: $(TESSDATA_PREFIX="$DEST/Contents/Resources/ocr/share/tessdata" "$DEST/Contents/Resources/ocr/bin/tesseract" "$BIG" stdout 2>/dev/null | grep -io heliotrope | head -1)"
NEW=600
for i in $(seq -w 1 $NEW); do d="$SRC/scans-new/$(( 10#$i % 30 ))"; mkdir -p "$d"; cp "$BIG" "$d/big-$i.png"; done
log "added $NEW large pictures; the folder holds $(find "$SRC" -type f | wc -l | tr -d ' ') files"
r="$(curl -fsS -X POST "$API/api/libraries/$LIB/rebuild")" || fatal "the rebuild could not be started"
JOB="$(echo "$r" | json 'd["job"]["id"]')"
log "rebuild job $JOB; library state $(lib_state)"
# Stopped about 30 s in, while the library's own state still says it is
# building (its job reports no phases).
building=
for _ in $(seq 1 30); do
  case "$(lib_state)" in building*|updating*) building=1 ;; *) building= ;; esac
  [ "$(api "/api/index/job/$JOB" | json 'd["state"]')" = done ] && { building=; break; }
  sleep 1
done
log "rebuild before the stop: job $(api "/api/index/job/$JOB" | json '{k: d.get(k) for k in ("state", "label", "progress")}'), library state $(lib_state)"
[ -n "$building" ] || fatal "the rebuild was not running 30 s after it started (it finished too soon to be interrupted)"
stage 2-during-rebuild
log "library state after the counts: $(lib_state)"
check "during the rebuild: names in Query (600)" '[ "$(cnt "d[\"query_name\"][\"lib\"]")" -eq 600 ]'
check "during the rebuild: sizes in Query (300)" '[ "$(cnt "d[\"query_size\"][\"lib\"]")" -eq 300 ]'
check "during the rebuild: words in Query (at least the $WORDS_BEFORE before)" '[ "$(cnt "d[\"query_words\"][\"lib\"]")" -ge "$WORDS_BEFORE" ]'

# Stopped as a shutdown stops it: TERM to the run loop, and the window quit.
RL="$(head -1 "$DATA/server.pid")"
WIN="$(pgrep -f "$DEST/Contents/MacOS/$APP_NAME" | head -1)"
log "stopping: TERM to the run loop $RL, and the window $WIN"
kill -TERM "$RL" 2>/dev/null
[ -n "$WIN" ] && kill -TERM "$WIN" 2>/dev/null
t_stop=$(date +%s)
for _ in $(seq 1 30); do kill -0 "$RL" 2>/dev/null || break; sleep 1; done
if kill -0 "$RL" 2>/dev/null; then
  # A shutdown does not wait longer: the rest of the process group is killed.
  log "the run loop still runs 30 s after TERM; KILL to its process group, as a shutdown does"
  kill -KILL -- "-$RL" 2>/dev/null || kill -KILL "$RL" 2>/dev/null
  sleep 2
else
  log "the run loop stopped $(( $(date +%s) - t_stop )) s after TERM"
fi
left="$(pgrep -fl "$DEST/Contents/Resources" || true)"
[ -n "$left" ] && log "still running after the stop: $left"
api /api/version >/dev/null 2>&1 && fatal "the server still answers after the stop"
log "stopped; library state $(lib_state)"
cp "$DATA/logs/studio.log" "$OUT/studio-before-restart.log" 2>/dev/null
sleep 3

# Opened again; the first requests are the counts.
open "$DEST" &
wait_server || fatal "the server did not answer after the app was opened again"
log "server again: $(api /api/version); run loop $(head -1 "$DATA/server.pid"); library state $(lib_state)"
stage 3-after-restart
log "library state after the counts: $(lib_state)"
check "after the restart: names in search" '[ "$(cnt "d[\"searchName\"][\"lib\"]")" -gt 0 ]'
check "after the restart: names in Query (600)" '[ "$(cnt "d[\"query_name\"][\"lib\"]")" -eq 600 ]'
check "after the restart: sizes in Query (300)" '[ "$(cnt "d[\"query_size\"][\"lib\"]")" -eq 300 ]'
check "after the restart: words in search" '[ "$(cnt "d[\"searchWord\"][\"lib\"]")" -gt 0 ]'
check "after the restart: words in Query (at least the $WORDS_BEFORE before)" '[ "$(cnt "d[\"query_words\"][\"lib\"]")" -ge "$WORDS_BEFORE" ]'
log "the index status after the restart: $(api /api/index/status | json '{"running": d.get("running"), "job": (d.get("job") or {}).get("label"), "queued": [q.get("label") for q in d.get("queued") or []]}')"

# The interrupted rebuild finishes.
for i in $(seq 1 800); do
  st="$(lib_state)"
  case "$st" in building*|updating*) ;; *) [ "$(api /api/index/status | json 'd.get("running")')" = False ] && break ;; esac
  [ $((i % 20)) = 0 ] && log "rebuild still running: $st"
  sleep 3
done
log "after the rebuild: library state $(lib_state)"
stage 4-after-rebuild
check "after the rebuild: names in search" '[ "$(cnt "d[\"searchName\"][\"lib\"]")" -gt 0 ]'
check "after the rebuild: names in Query (600)" '[ "$(cnt "d[\"query_name\"][\"lib\"]")" -eq 600 ]'
check "after the rebuild: sizes in Query (300)" '[ "$(cnt "d[\"query_size\"][\"lib\"]")" -eq 300 ]'
check "after the rebuild: words in search" '[ "$(cnt "d[\"searchWord\"][\"lib\"]")" -gt 0 ]'
check "after the rebuild: words in Query, the large pictures too ($WORDS_BEFORE + $NEW)" '[ "$(cnt "d[\"query_words\"][\"lib\"]")" -eq $(( WORDS_BEFORE + NEW )) ]'

collect
"$DEST/Contents/MacOS/$APP_NAME" --stop >>"$SUMMARY" 2>&1 || true
if [ "$FAILS" -gt 0 ]; then log "$FAILS check(s) failed"; exit 1; fi
log "PASS"
