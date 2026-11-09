#!/bin/bash
# Screenshots of the app's window on this Mac, for a person to look at: the
# window controls over the page's top row (desktop/wails, html.shell-inset)
# in Chat, Library and Settings, light and dark, with the navigation
# expanded and collapsed, then with a classification banner and with the
# setup wizard open. The window runs its self test's hold plan
# (desktop/wails/selftest.go), and each state is set by a script the window
# hands to the page (eval.js in the test folder).
#
# Usage: deploy/mac/window_shots.sh "path/to/ACTIVATE Studio.app" OUT_DIR
# OUT_DIR gets one PNG per state, window.json (the window's own report:
# its number, title bar height and the controls' frames) and window.log.
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

# The setup is done, so no first-run wizard opens over the pages.
page "fetch('/api/setup/complete',{method:'POST'}).then(()=>fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({aiEnabled:true})})).then(()=>location.reload())"
sleep 3
for theme in light dark; do
  for nav in expanded collapsed; do
    c=0; [ "$nav" = collapsed ] && c=1
    for view in chat library settings; do
      state "$theme" "$c" "$view"
      shot "$view-$theme-$nav"
    done
  done
done
state light 0 settings; sleep 4
page "window.dispatchEvent(new Event('ade:setup-open'))"
shot "setup-light-expanded" 2
page "fetch('/api/settings',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({bannerText:'CUI // TEST MARKING',bannerColor:'#502b85'})}).then(()=>location.reload())"
sleep 3
for theme in light dark; do
  state "$theme" 0 library
  shot "banner-library-$theme-expanded"
done
state light 1 chat
shot "banner-chat-light-collapsed"
ls "$OUT"
