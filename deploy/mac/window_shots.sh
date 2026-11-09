#!/bin/bash
# Screenshots of the app's window on this Mac, for a person to look at: the
# window controls over the page's top row (desktop/wails, html.shell-inset)
# in Chat, Library and Settings, light and dark, with the navigation
# expanded and collapsed, without and with a classification banner, the
# banner switched on and off live, longer names, and the setup wizard. The
# window runs its self test's hold plan
# (desktop/wails/selftest.go), and each state is set by a script the window
# hands to the page (eval.js in the test folder).
#
# Usage: deploy/mac/window_shots.sh "path/to/ACTIVATE Studio.app" OUT_DIR
# OUT_DIR gets one PNG per state, window.json and window-*.json (the
# window's own report: its number, title bar height and the controls'
# frames, at launch and in each layout) and window.log.
set -euo pipefail
APP="$(cd "${1:?usage: window_shots.sh APP OUT_DIR}" && pwd -P)"
OUT="${2:?usage: window_shots.sh APP OUT_DIR}"
mkdir -p "$OUT"
T="$(mktemp -d)"
H="$T/home"
D="$H/Library/Application Support/ACTIVATE Studio"
WT="$T/window-test"
mkdir -p "$D" "$WT" "$T/corpus/notes" "$T/corpus/papers"
printf '# Design\n\nThe index is a tree of per-directory databases.\n' >"$T/corpus/notes/design.md"
printf 'A recipe for lemon cake.\n' >"$T/corpus/cake.txt"
printf 'Drag measurements.\n' >"$T/corpus/papers/drag.txt"
# A PDF for the viewer, which this app draws with pdf.js in the window (it
# carries no poppler), and an Apple Mail folder as Mail keeps one, for the
# suggested folders.
HERE="$(cd "$(dirname "$0")" && pwd -P)"
cp "$HERE/testdata/ocr-sample.pdf" "$T/corpus/papers/scan.pdf"
MB="$H/Library/Mail/V10/0000-TEST/INBOX.mbox/Data/Messages"
mkdir -p "$MB"
python3 -I - "$MB/1.emlx" <<'PY'
import sys
msg = ("From: Ada <ada@example.com>\r\nTo: Charles <cb@example.com>\r\n"
       "Subject: Engine notes\r\nDate: Mon, 2 Nov 2026 10:00:00 -0600\r\n\r\n"
       "The analytical engine weaves quillwort patterns.\r\n")
plist = '<?xml version="1.0" encoding="UTF-8"?>\n<plist version="1.0"><dict><key>flags</key><integer>8590195713</integer></dict></plist>\n'
open(sys.argv[1], "w").write("%d\n%s%s" % (len(msg.encode()), msg, plist))
PY
PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
printf "PORT='%s'\nKB_ROOT='%s'\n" "$PORT" "$T/corpus" >"$D/studio.env"
# AI on, with a model address that answers nothing, so Chat is in the
# navigation; no message is sent.
printf "AI_ENABLED='1'\nOPENAI_BASE_URL='http://127.0.0.1:9/v1'\nOPENAI_API_KEY='window-shots'\n" >>"$D/studio.env"

env HOME="$H" STUDIO_SHELL_TEST=1 STUDIO_SHELL_TEST_PLAN=hold STUDIO_SHELL_TEST_DIR="$WT" \
  "$APP/Contents/MacOS/ACTIVATE Studio" >"$OUT/window.log" 2>&1 &
WPID=$!
finish() {
  kill -TERM "$WPID" 2>/dev/null || true
  for _ in $(seq 1 20); do kill -0 "$WPID" 2>/dev/null || break; sleep 0.5; done
  kill -KILL "$WPID" 2>/dev/null || true
  env HOME="$H" "$APP/Contents/Resources/launcher.sh" --stop >/dev/null 2>&1 || true
}
trap finish EXIT
for _ in $(seq 1 180); do [ -s "$WT/ready" ] && break; kill -0 "$WPID" 2>/dev/null || break; sleep 1; done
if [ ! -s "$WT/ready" ]; then
  echo "the window did not open, or the Studio did not draw in it"
  tail -n 30 "$OUT/window.log"
  exit 1
fi
python3 -I -c 'import json, sys; print(json.dumps(json.load(open(sys.argv[1]))["results"].get("window"), indent=1))' \
  "$WT/results.json" >"$OUT/window.json"
cat "$OUT/window.json"
WID="$(python3 -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["windowNumber"])' "$OUT/window.json")"

# Hands a script to the page and waits until the window has run it.
page() {
  rm -f "$WT/evaluated"
  printf '%s' "$1" >"$WT/eval.tmp"
  mv "$WT/eval.tmp" "$WT/eval.js"
  for _ in $(seq 1 50); do [ -e "$WT/evaluated" ] && return 0; sleep 0.2; done
  echo "the window did not run the script"; return 1
}
shot() {
  sleep "${2:-4}"
  screencapture -x -o -l "$WID" "$OUT/$1.png"
  echo "$1.png"
}
state() { # theme collapsed view
  page "localStorage.setItem('ade-theme','$1');localStorage.setItem('ade-nav-collapsed','$2');localStorage.setItem('ade-status-panel','1');location.hash='#view=$3';location.reload()"
}

# A fresh report of the window's own state, saved as OUT/window-NAME.json.
report() {
  rm -f "$WT/window-report.json"
  : >"$WT/window-report"
  for _ in $(seq 1 50); do [ -s "$WT/window-report.json" ] && break; sleep 0.2; done
  cp "$WT/window-report.json" "$OUT/window-$1.json" 2>/dev/null || echo "no window report for $1"
  python3 -I -c 'import json, sys; r = json.load(open(sys.argv[1])); print(sys.argv[2], "title bar", r.get("titlebarHeight"), "controls", [(c["x"], c["y"]) for c in r.get("controls", [])])' "$OUT/window-$1.json" "$1" 2>/dev/null || true
}
# Settings saved as Settings does it: the page picks them up without a reload.
settings() {
  page "fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify($1)}).then(()=>window.dispatchEvent(new Event('ade:config-changed')))"
}
BANNER="{bannerText:'CUI // TEST MARKING',bannerColor:'#502b85'}"
NOBANNER="{bannerText:''}"
pages() { # prefix
  for theme in light dark; do
    for nav in expanded collapsed; do
      c=0; [ "$nav" = collapsed ] && c=1
      for view in chat library settings; do
        state "$theme" "$c" "$view"
        shot "$1$view-$theme-$nav"
      done
    done
  done
}

# The setup is done, so no first-run wizard opens over the pages.
page "fetch('/api/setup/complete',{method:'POST'}).then(()=>fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({aiEnabled:true})})).then(()=>location.reload())"
sleep 3
# No banner: the controls share the page's 52px top row.
pages ""
report row
state light 0 settings; sleep 4
page "window.dispatchEvent(new Event('ade:setup-open'))"
shot "setup-light-expanded" 2
# Longer names in the sidebar's brand row.
for name in "Matthew's Studio" "Materials Engineering Knowledge Studio"; do
  page "fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({appName:\"$name\"})}).then(()=>location.reload())"
  sleep 4
  shot "name-$(printf '%s' "$name" | tr -cd 'A-Za-z' | head -c 12)-light-expanded" 1
done
page "fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({appName:''})}).then(()=>location.reload())"
sleep 3
# The banner switched on live, from the library, without a reload.
state light 0 library; sleep 4
settings "$BANNER"
shot "switch-on-library-light-expanded"
report switched-on
# The banner on: the compact layout.
pages "banner-"
report banner
# And off again, live.
state light 0 library; sleep 4
settings "$NOBANNER"
shot "switch-off-library-light-expanded"
report switched-off

# The page's own answers, through the self test's log (selftest.log).
said() { { grep -o "page: $1 .*" "$WT/selftest.log" 2>/dev/null || true; } | tail -n 1 | sed "s/^page: $1 //"; }
ask() { # name script-returning-a-promise-of-a-string
  page "Promise.resolve().then(()=>$2).then(v=>window.__studioShell.call('TestLog',['$1 '+v]),e=>window.__studioShell.call('TestLog',['$1 error '+e]))"
  for _ in $(seq 1 ${3:-60}); do [ -n "$(said "$1")" ] && break; sleep 1; done
  said "$1"
}
FAIL=0

# A PDF opened from the library: its pages drawn by pdf.js in this window.
state light 0 library; sleep 4
page "location.hash='#open=file:papers/scan.pdf'"
sleep 8
shot "pdf-light-expanded" 1
PDF="$(ask pdf "new Promise(r=>setTimeout(r,1000)).then(()=>{const c=[...document.querySelectorAll('canvas.pdf-page-img')];const inked=c.filter(x=>{if(!x.width||!x.height)return false;const d=x.getContext('2d').getImageData(0,0,x.width,x.height).data;let n=0;for(let i=0;i<d.length;i+=16)if(d[i]<128)n++;return n>50});return JSON.stringify({canvases:c.length,inked:inked.length,serverPages:document.querySelectorAll('img.pdf-page-img').length,text:!!document.querySelector('.viewer-body pre, .md-body')})})")"
echo "pdf: $PDF"
case "$PDF" in *'"inked":0'*|'' |*error*) echo "FAIL pdf.js did not draw the PDF in the window"; FAIL=1 ;; *'"inked":'*) echo "ok pdf.js drew the PDF's page in the window" ;; esac

# The suggested folders: Apple Mail offered, built as a library, its
# message found by search.
SUG="$(ask sug "fetch('/api/libraries/suggestions').then(r=>r.text())")"
echo "suggestions: $SUG"
case "$SUG" in *'"id":"mail"'*) echo "ok Apple Mail is offered" ;; *) echo "FAIL Apple Mail is not offered"; FAIL=1 ;; esac
state light 0 settings; sleep 4
page "window.dispatchEvent(new Event('ade:setup-open'))"
shot "suggestions-setup-light-expanded" 3
MAILDIR="$H/Library/Mail"
BUILT="$(ask build "fetch('/api/libraries/build/check',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sourceRoot:'$MAILDIR',label:'Apple Mail'})}).then(r=>r.json()).then(c=>fetch('/api/libraries/build',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({sourceRoot:'$MAILDIR',label:'Apple Mail',create:!!(c.create&&c.create.length)})})).then(r=>r.text().then(t=>r.status+' '+t.slice(0,300)))")"
echo "build: $BUILT"
found=0
for i in $(seq 1 30); do
  HIT="$(ask "find$i" "fetch('/api/search?q=quillwort&library=all').then(r=>r.text())" 20)"
  case "$HIT" in *1.emlx*) found=1; break ;; esac
  sleep 4
done
if [ "$found" = 1 ]; then echo "ok the Apple Mail message is found by search"; else echo "FAIL the Apple Mail message was not found: $HIT"; FAIL=1; fi
page "location.hash='#view=search?q=quillwort'"
shot "mail-search-light-expanded" 4
OPENED="$(ask fda "fetch('/api/libraries/suggestions/full-disk-access',{method:'POST'}).then(r=>r.status+' '+r.headers.get('content-type'))")"
echo "full disk access setting: $OPENED"

ls "$OUT"
exit "$FAIL"
