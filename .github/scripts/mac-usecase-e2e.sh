#!/usr/bin/env bash
# Prepares a runner for the use-case test, then runs it
# (mac-usecase-e2e.mjs): the app is installed from the disk image into
# ~/Applications with its own --install and started; a main folder gets a
# PDF, a .docx and a picture whose text only OCR can read; Remote Login is
# turned on and a key made, so this machine can be reached over ssh as
# another one would be; a second folder is indexed with GUFI tools copied
# out of the app (as that other machine's own GUFI), giving the existing
# index the Studio connects to read only. Playwright drives the Studio's
# pages in Chromium. The server is stopped at the end.
#
# Usage: mac-usecase-e2e.sh DMG_URL OUT_DIR
set -uo pipefail

URL="$1"
OUT="$2"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
APP_NAME="ACTIVATE Studio"
DEST="$HOME/Applications/$APP_NAME.app"
DATA="$HOME/Library/Application Support/$APP_NAME"
W="$HOME/studio-e2e"
mkdir -p "$OUT"
SUMMARY="$OUT/summary.txt"
T0=$(date +%s)
log() { printf '%4ss  %s\n' "$(( $(date +%s) - T0 ))" "$*" | tee -a "$SUMMARY"; }
collect() { [ -d "$DATA/logs" ] && cp -R "$DATA/logs" "$OUT/logs" 2>/dev/null; return 0; }
fail() { log "FAIL: $*"; collect; exit 1; }

# The app, installed by its own --install from the mounted image.
curl -fsSL -o "$OUT/../app.dmg" "$URL" || fail "could not download $URL"
MNT="$(mktemp -d)"
hdiutil attach -nobrowse -readonly -mountpoint "$MNT" "$OUT/../app.dmg" >/dev/null || fail "could not mount the image"
"$MNT/$APP_NAME.app/Contents/MacOS/$APP_NAME" --install >>"$SUMMARY" 2>&1 || fail "--install failed"
hdiutil detach "$MNT" >/dev/null 2>&1 || true
[ -d "$DEST" ] || fail "--install put nothing in $DEST"
log "installed $(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$DEST/Contents/Info.plist") in $DEST"

# The main folder: a PDF, a .docx, and a picture with text.
mkdir -p "$W/main/docs" "$W/main/scans"
cp "$REPO/testdata/corpus/docs/report.pdf" "$REPO/testdata/corpus/docs/handbook.docx" "$W/main/docs/"
cp "$REPO/deploy/mac/testdata/ocr-sample.png" "$W/main/scans/"
log "main folder: $(cd "$W/main" && find . -type f | tr '\n' ' ')"

# This machine over ssh, with a key, as another machine would be.
sudo systemsetup -f -setremotelogin on >>"$SUMMARY" 2>&1 \
  || sudo launchctl load -w /System/Library/LaunchDaemons/ssh.plist >>"$SUMMARY" 2>&1 || true
mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
[ -f "$HOME/.ssh/id_ed25519" ] || ssh-keygen -q -t ed25519 -N "" -f "$HOME/.ssh/id_ed25519"
cat "$HOME/.ssh/id_ed25519.pub" >>"$HOME/.ssh/authorized_keys" && chmod 600 "$HOME/.ssh/authorized_keys"
for _ in $(seq 1 20); do ssh-keyscan -H localhost >>"$HOME/.ssh/known_hosts" 2>/dev/null && break; sleep 1; done
ssh -o BatchMode=yes "$(id -un)@localhost" true || fail "ssh to localhost with the key does not work"
log "ssh $(id -un)@localhost works with a key"

# The other machine's own GUFI, and an existing index there.
ditto "$DEST/Contents/Resources/gufi" "$W/remote-gufi"
mkdir -p "$W/remote-src/projects" "$W/remote-idx"
for f in orbitnotes-2025.txt orbitnotes-2026.txt projects/telemetry.csv; do echo "remote file $f" >"$W/remote-src/$f"; done
"$W/remote-gufi/bin/gufi_dir2index" "$W/remote-src" "$W/remote-idx" >>"$SUMMARY" 2>&1 || fail "gufi_dir2index failed"
IDX="$(dirname "$(find "$W/remote-idx" -maxdepth 2 -name db.db | head -1)")"
[ -f "$IDX/db.db" ] || fail "no index was built in $W/remote-idx"
log "remote index at $IDX, of $W/remote-src, with GUFI in $W/remote-gufi/bin"

# The server, started as the app's window starts it.
STUDIO_NO_BROWSER=1 bash "$DEST/Contents/Resources/launcher.sh" >>"$SUMMARY" 2>&1 || fail "the launcher did not start the server"
curl -fsS http://127.0.0.1:4180/api/version >/dev/null || fail "the server does not answer"
log "server: $(curl -fsS http://127.0.0.1:4180/api/version)"

# Whether Query can use gufi_vt here: the app's gufi_sqlite3 with a remote
# location (the server's own probe, gufiVt.ts), and the two paths the server
# puts in gufi_vt's quotes, which its SAFE_PATH rule refuses with a space.
SQLITE="$DEST/Contents/Resources/gufi/bin/gufi_sqlite3"
probe="CREATE VIRTUAL TABLE temp.p USING gufi_vt(index='/', E=\"SELECT name FROM vrpentries;\", remote_cmd='/bin/true'); SELECT count(*) FROM temp.p;"
log "gufi_vt with remote_cmd in the app's gufi_sqlite3: $("$SQLITE" :memory: "$probe" 2>&1 | tr '\n' ' ')"
for p in "$DEST/Contents/Resources/node/bin/node" "$DEST/Contents/Resources/app/server/dist/gufiVtRemote.js"; do
  if [[ "$p" =~ ^/[^\'\"[:space:]]*$ ]]; then log "path passes SAFE_PATH: $p"; else log "path fails SAFE_PATH (gufi_vt is turned off): $p"; fi
done

# Playwright, with its Chromium.
P="$(mktemp -d)"
(cd "$P" && npm init -y >/dev/null && npm install --no-audit --no-fund playwright@1.64.1 >/dev/null 2>&1 && npx playwright install chromium >/dev/null 2>&1) \
  || fail "Playwright could not be installed"
cp "$HERE/mac-usecase-e2e.mjs" "$P/"
log "Playwright $(cd "$P" && npx playwright --version)"
(cd "$P" && node mac-usecase-e2e.mjs "$OUT" "$W/main" "$(id -un)@localhost" "$IDX" "$W/remote-src" "$W/remote-gufi/bin")
code=$?
collect
"$DEST/Contents/MacOS/$APP_NAME" --stop >>"$SUMMARY" 2>&1 || true
exit "$code"
