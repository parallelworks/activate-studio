#!/bin/bash
# One self-test run of the shell on Linux (under Xvfb; DISPLAY set).
#
# Usage: smoke-linux.sh BINARY MODE OUT_DIR
# Env:   STUDIO_APP_DIR, STUDIO_KB_ROOT, STUDIO_SHELL_TEST_FOLDER (set by the workflow)
set -u
BIN=$1 MODE=$2 OUT=$3
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
DATA="$(mktemp -d)"

# The default browser, as far as the shell can tell: a stand-in xdg-open
# that records what it was asked to open.
FAKEBIN="$(mktemp -d)"
printf '#!/bin/sh\necho "$(date +%%s%%3N) $*" >>"%s/xdg-open.log"\n' "$OUT" >"$FAKEBIN/xdg-open"
chmod +x "$FAKEBIN/xdg-open"

run() {
  env PATH="$FAKEBIN:$PATH" \
    STUDIO_NODE="$(command -v node)" \
    STUDIO_SHELL_MODE="$MODE" \
    STUDIO_SHELL_DATA_DIR="$DATA" \
    STUDIO_SHELL_TEST=1 \
    STUDIO_SHELL_TEST_DIR="$OUT" \
    STUDIO_SHELL_SCREENSHOT="import -window root" \
    STUDIO_SHELL_TEST_DIALOG_CLOSE="$HERE/close-dialog-linux.sh" \
    STUDIO_SHELL_TEST_OPEN_REAL=1 \
    NO_AT_BRIDGE=1 \
    STUDIO_SHELL_LAUNCH_T0="$(date +%s%3N)" \
    "$@" timeout 400 dbus-run-session -- "$BIN" >"$OUT/shell.log" 2>&1
}

run
code=$?
echo "$code" >"$OUT/exit-code.txt"
# WebKitGTK's web process sandbox (bubblewrap) needs unprivileged user
# namespaces, which Ubuntu 24.04's AppArmor restricts. If that is why it
# failed, run again without the sandbox and say so.
if [ "$code" != 0 ] && grep -q "bwrap" "$OUT/shell.log"; then
  mv "$OUT/shell.log" "$OUT/shell-sandboxed.log"
  echo "sandbox: bwrap failed, rerun with WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1" >"$OUT/sandbox.txt"
  run WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1
  code=$?
  echo "$code" >"$OUT/exit-code.txt"
fi

# After the shell exited: is anything it started still running?
sleep 1
alive=()
if [ -f "$OUT/pids.log" ]; then
  for p in $(jq -r '.server, .helper' "$OUT/pids.log"); do
    if [ -r "/proc/$p/stat" ] && [ "$(awk '{print $3}' "/proc/$p/stat")" != Z ]; then alive+=("$p"); fi
  done
fi
# Any node still running the Studio's server, whoever started it (a listing,
# nothing is killed here).
ps -eo pid,ppid,pgid,args | grep -F "$STUDIO_APP_DIR/server/dist/main.js" | grep -v grep >"$OUT/node-after-exit.txt" || true
printf '{"exitCode": %s, "aliveAfterExit": [%s]}\n' "$code" "$(IFS=,; echo "${alive[*]:-}")" >"$OUT/after-exit.json"
cp "$DATA/logs/studio.log" "$OUT/studio.log" 2>/dev/null || true
cat "$OUT/after-exit.json"
rm -rf "$DATA" "$FAKEBIN"
exit 0
