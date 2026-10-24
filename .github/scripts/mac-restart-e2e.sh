#!/usr/bin/env bash
# A built library across a restart that interrupts its rebuild, on a Mac
# where ~/Library/LaunchAgents is read-only (the app runs its server itself).
#
# The app is installed from the disk image into ~/Applications and opened.
# A second library is built of a local folder of about 2,600 files (names,
# a set of files of one size, pictures and scanned PDFs whose text only OCR
# reads) at the Text + OCR depth, and waited for until its words are found.
# Then 1,200 pictures, each with its own dense text and noise, are added and
# the library rebuilt; once tesseract runs and some are read, the server is
# stopped as a shutdown stops it (TERM to the run loop, and the window quit),
# and the app opened again. Right after it
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
  C="$(cd "$P" && node mac-restart-e2e.mjs "$1" "$OUT" "$LIB" ${2:-} 2>>"$OUT/playwright.log" | tail -1)"
  log "counts at $1: $C"
  log "omitted at $1: $(echo "$C" | json '{k: v["omitted"] for k, v in d.items() if isinstance(v, dict) and v.get("omitted")} or "none"')"
  log "times at $1 (ms): $(echo "$C" | json '{k: v["ms"] for k, v in d.items() if isinstance(v, dict) and "ms" in v}')"
  # Each request finds files in the library: it is in the results, or named
  # in omitted (v1.173, #588); never left out silently.
  check "at $1: the library is never left out without being named in omitted" '[ "$(cnt "len(d[\"silent\"])")" -eq 0 ]'
  # Query across libraries runs on gufi_vt in the app (v1.173, #587).
  check "at $1: Query across all libraries runs on gufi_vt" \
    '[ "$(cnt "\",\".join(sorted({d[k][\"engine\"] or \"none\" for k in d if k.startswith(\"query_\")}))")" = gufi_vt ]'
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

# Pictures each with its own dense text (one line has "heliotrope" and the
# picture's number) and noise, so each needs an OCR pass of its own that
# takes a while; then the rebuild.
NEW=1200
python3 -m venv "$HOME/studio-restart/pv" >/dev/null 2>&1 && "$HOME/studio-restart/pv/bin/pip" -q install pillow >/dev/null 2>&1 \
  || fatal "Pillow could not be installed to draw the pictures"
"$HOME/studio-restart/pv/bin/python" - "$SRC/scans-new" "$NEW" <<'PY' || fatal "the pictures could not be drawn"
import os, random, sys
from PIL import Image, ImageDraw, ImageFont
out, n = sys.argv[1], int(sys.argv[2])
font = None
big = None
for f in ('/System/Library/Fonts/Supplemental/Arial.ttf', '/System/Library/Fonts/Helvetica.ttc', '/Library/Fonts/Arial.ttf',
          '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf'):
    if os.path.exists(f):
        font, big = ImageFont.truetype(f, 34), ImageFont.truetype(f, 56); break
font = font or ImageFont.load_default()
big = big or font
words = 'pump valve station pressure inspection reservoir turbine filter gauge sensor flow intake outlet basin channel'.split()
for i in range(1, n + 1):
    rnd = random.Random(i)
    img = Image.new('L', (2200, 1500), 255)
    d = ImageDraw.Draw(img)
    for row in range(26):
        line = ' '.join(rnd.choice(words) for _ in range(9)) + f' {rnd.randint(100, 999)}'
        if row == 3:
            # The line the counts look for, larger, so the noise does not hide it.
            d.text((60, 40 + row * 55), f'Station Heliotrope plate {i:04d} passed every valve check', fill=0, font=big)
            continue
        if row == 4:
            continue
        d.text((60, 40 + row * 55), line, fill=0, font=font)
    px = img.load()
    for _ in range(5000):
        px[rnd.randrange(2200), rnd.randrange(1500)] = rnd.randrange(80, 200)
    sub = os.path.join(out, str(i % 30))
    os.makedirs(sub, exist_ok=True)
    img.save(os.path.join(sub, f'plate-{i:04d}.png'))
PY
ONE="$(find "$SRC/scans-new" -name 'plate-0001.png' | head -1)"
log "drew $NEW pictures of 2200x1500 with different text and noise; the app's tesseract reads plate 1 in $( { /usr/bin/time -p env TESSDATA_PREFIX="$DEST/Contents/Resources/ocr/share/tessdata" "$DEST/Contents/Resources/ocr/bin/tesseract" "$ONE" stdout --psm 3 >"$OUT/plate-0001.txt" 2>/dev/null; } 2>&1 | awk '/^real/ {print $2}') s: $(grep -io 'heliotrope plate [0-9]*' "$OUT/plate-0001.txt" | head -1)"
log "the folder holds $(find "$SRC" -type f | wc -l | tr -d ' ') files"
r="$(curl -fsS -X POST "$API/api/libraries/$LIB/rebuild")" || fatal "the rebuild could not be started"
JOB="$(echo "$r" | json 'd["job"]["id"]')"
log "rebuild job $JOB; library state $(lib_state)"
# Stopped while OCR runs: the library's job reports no phases, only its
# progress, so the stop waits for tesseract processes and some pictures
# read (extraction cache entries for the new pictures), then comes at once.
CACHE="$DATA/index/libraries/$LIB/extract"
ocr_seen=
for _ in $(seq 1 480); do
  n="$(pgrep -x tesseract | wc -l | tr -d ' ')"
  read_n="$(find "$CACHE" -path '*scans-new*' -type f 2>/dev/null | wc -l | tr -d ' ')"
  if [ "${n:-0}" -gt 0 ] && [ "${read_n:-0}" -gt 0 ]; then ocr_seen="$n"; break; fi
  [ "$(api "/api/index/job/$JOB" | json 'd["state"]')" = done ] && break
  sleep 0.25
done
[ -n "$ocr_seen" ] || fatal "no tesseract process with pictures read was seen while the rebuild ran (it finished, or read no pictures)"
log "rebuild at the stop: $ocr_seen tesseract processes, $read_n of $NEW new pictures read; job $(api "/api/index/job/$JOB" | json '{k: d.get(k) for k in ("state", "label", "progress", "phases")}'), library state $(lib_state)"
case "$(lib_state)" in building*|updating*) ;; *) fatal "the rebuild had finished before the stop" ;; esac
stage 2-during-rebuild counts-only
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
# The stop ends the build's programs too (v1.173, #588); v1.172 left
# enrich.py and its workers running.
sleep 2
left="$(pgrep -fl "$DEST/Contents/Resources" || true)"
[ -n "$left" ] && log "still running after the stop: $left"
check "the stop leaves none of the app's programs running" '[ -z "$left" ]'
# The pictures read before the stop, with their cache files' times, to show
# that the resumed build reads only the rest.
find "$CACHE" -path '*scans-new*' -type f -exec stat -f '%m %N' {} + 2>/dev/null | sort -k2 >"$OUT/cache-at-stop.txt"
log "pictures read before the stop: $(wc -l <"$OUT/cache-at-stop.txt" | tr -d ' ') of $NEW"
api /api/version >/dev/null 2>&1 && fatal "the server still answers after the stop"
log "stopped; library state $(lib_state)"
cp "$DATA/logs/studio.log" "$OUT/studio-before-restart.log" 2>/dev/null
sleep 3

# Opened again; the first requests are the counts.
open "$DEST" &
wait_server || fatal "the server did not answer after the app was opened again"
T_BACK=$(date +%s)
log "server again: $(api /api/version); run loop $(head -1 "$DATA/server.pid"); library state $(lib_state)"
STATE_BACK="$(lib_state)"
check "after the restart: the interrupted build is still marked updating" 'case "$STATE_BACK" in updating*) true ;; *) false ;; esac'
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
  case "$st" in building*|updating*) ;; *) break ;; esac
  [ $((i % 20)) = 0 ] && log "rebuild still running: $st; $(find "$CACHE" -path '*scans-new*' -type f | wc -l | tr -d ' ') of $NEW pictures read"
  sleep 3
done
log "after the rebuild: library state $(lib_state); it resumed and finished $(( $(date +%s) - T_BACK )) s after the server answered again"
check "after the rebuild: the library is ready" 'case "$(lib_state)" in ready*) true ;; *) false ;; esac'
# Resumed, not started over: the pictures read before the stop were not read again.
find "$CACHE" -path '*scans-new*' -type f -exec stat -f '%m %N' {} + 2>/dev/null | sort -k2 >"$OUT/cache-after.txt"
reread="$(join -1 2 -2 2 <(sort -k2 "$OUT/cache-at-stop.txt") <(sort -k2 "$OUT/cache-after.txt") | awk '$2 != $3' | wc -l | tr -d ' ')"
log "pictures read before the stop and read again after it: $reread of $(wc -l <"$OUT/cache-at-stop.txt" | tr -d ' ')"
check "after the rebuild: the build resumed from the extraction cache (nothing read before the stop was read again)" '[ "$reread" -eq 0 ]'
stage 4-after-rebuild
check "after the rebuild: names in search" '[ "$(cnt "d[\"searchName\"][\"lib\"]")" -gt 0 ]'
check "after the rebuild: names in Query (600)" '[ "$(cnt "d[\"query_name\"][\"lib\"]")" -eq 600 ]'
check "after the rebuild: sizes in Query (300)" '[ "$(cnt "d[\"query_size\"][\"lib\"]")" -eq 300 ]'
check "after the rebuild: words in search" '[ "$(cnt "d[\"searchWord\"][\"lib\"]")" -gt 0 ]'
check "after the rebuild: words in Query, the new pictures too (at least $WORDS_BEFORE + 95% of $NEW)" '[ "$(cnt "d[\"query_words\"][\"lib\"]")" -ge $(( WORDS_BEFORE + NEW * 95 / 100 )) ]'

collect
"$DEST/Contents/MacOS/$APP_NAME" --stop >>"$SUMMARY" 2>&1 || true
if [ "$FAILS" -gt 0 ]; then log "$FAILS check(s) failed"; exit 1; fi
log "PASS"
