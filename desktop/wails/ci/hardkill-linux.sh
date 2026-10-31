#!/bin/bash
# The shell killed outright (SIGKILL, no shutdown): does the server, and
# what the server started, end too?
#
# Usage: hardkill-linux.sh BINARY OUT_DIR
set -u
BIN=$1 OUT=$2
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
DATA="$(mktemp -d)"
env STUDIO_NODE="$(command -v node)" STUDIO_SHELL_MODE=direct STUDIO_SHELL_DATA_DIR="$DATA" \
  STUDIO_SHELL_TEST=1 STUDIO_SHELL_TEST_PLAN=hold STUDIO_SHELL_TEST_DIR="$OUT" NO_AT_BRIDGE=1 \
  ${WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS:+WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1} \
  "$BIN" >"$OUT/shell.log" 2>&1 &
shell=$!
for _ in $(seq 1 120); do [ -f "$OUT/pids.json" ] && break; sleep 0.5; done
if [ ! -f "$OUT/pids.json" ]; then
  echo '{"error": "pids.json never appeared"}' >"$OUT/hardkill.json"
  kill -KILL "$shell" 2>/dev/null
  exit 0
fi
kill -KILL "$shell"
wait "$shell" 2>/dev/null
sleep 3
alive=()
for p in $(jq -r '.servers[] | .server, .helper' "$OUT/pids.json"); do
  if [ -r "/proc/$p/stat" ] && [ "$(awk '{print $3}' "/proc/$p/stat")" != Z ]; then
    alive+=("$p")
    ps -o pid,ppid,pgid,args -p "$p" >>"$OUT/alive.txt"
  fi
done
printf '{"shell": %s, "aliveAfterKill": [%s]}\n' "$shell" "$(IFS=,; echo "${alive[*]:-}")" >"$OUT/hardkill.json"
cat "$OUT/hardkill.json"
# Leave the runner clean: end what is left, by PID.
for p in "${alive[@]:-}"; do [ -n "$p" ] && kill -KILL "$p" 2>/dev/null; done
rm -rf "$DATA"
exit 0
