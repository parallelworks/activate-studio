#!/usr/bin/env bash
# End to end check of the Linux payload's updates, in a clean container (run
# as root, with the repository at /src and the payload archive in /in, as
# test_in_container.sh is):
#
# 1. The payload is installed per user, under a plain user's ~/.local, with an
#    install-kind file of "user", and its server is started from its run loop.
# 2. A stand-in release server on 127.0.0.1 (in the shape of GitHub's releases
#    API; nothing is published anywhere) offers the next version, a copy of
#    the payload stamped as that version and signed with a throwaway key,
#    which the installed payload carries the public half of. The check offers
#    it, "Update now" downloads and verifies it, swaps it in beside the old
#    one, the server exits 76, the run loop runs the new payload's own loop,
#    and the server answers as the new version.
# 3. A release whose archive is corrupt is refused: the payload is untouched
#    and the server keeps running.
# 4. A release that verifies but whose server will not start is installed,
#    and the run loop puts the previous payload back after three stops.
# 4b. A server killed (SIGKILL) between the two directory moves of an update,
#    once with the new payload complete and once with it partial: the run loop
#    finishes the swap in the first case and puts the previous payload back in
#    the second.
# 5. The same payload installed read-only under /opt, as a package leaves it
#    (install-kind deb, rpm, and none), is told the version and the package
#    command, refuses to install, and changes nothing in the tree.
#
# The payload's own Node and Python do the work (the image needs no other
# tool), and every release is signed with a key made here: no real key, no
# release and no network beyond 127.0.0.1 is used.
#
# OVERLAY_SRC=/src runs a server build and run loop from the repository in
# the payload first, to try a change before a payload with it is built (the
# payload built from this commit has them already).
#
# Usage: docker run --rm -v "$PWD:/src:ro" -v "$IN:/in:ro" IMAGE bash /src/deploy/linux/test_update_e2e.sh
set -euo pipefail

# This script runs the payload's Python as root (with -B, since -I ignores this variable), which must not leave bytecode caches
# in a tree that a user or a package owns.
export PYTHONDONTWRITEBYTECODE=1
ARCHIVE="${ARCHIVE:-/in/activate-studio-linux-x86_64.tar.gz}"
SRC="${SRC:-/src}"
HERE="$SRC/deploy/linux"
[ -f "$ARCHIVE" ] || { echo "no $ARCHIVE" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "run as root: it makes a user and installs under /opt" >&2; exit 2; }
id tester >/dev/null 2>&1 || useradd -m tester
H=/home/tester
USERLIB="$H/.local/lib"
APP="$USERLIB/activate-studio"
T="$(mktemp -d /tmp/update-e2e.XXXXXX)"
chmod 755 "$T"
mkdir -p "$T/rel" "$T/keys" "$T/kb" "$T/stage"
chown -R tester "$T/kb"
failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
as_tester() { runuser -u tester -- env HOME="$H" "$@"; }
pids=()
cleanup() {
  for p in ${pids[@]+"${pids[@]}"}; do kill -TERM "$p" 2>/dev/null || true; done
  sleep 1
  for p in ${pids[@]+"${pids[@]}"}; do kill -KILL "$p" 2>/dev/null || true; done
  chmod -R u+w "$T" 2>/dev/null || true
  rm -rf "$T" /opt/activate-studio
}
trap cleanup EXIT

echo "Update end to end on $(. /etc/os-release && echo "$PRETTY_NAME")"

# ---- the payload, installed per user, as version v98.1 ----

as_tester mkdir -p "$USERLIB"
as_tester tar -xzf "$ARCHIVE" -C "$USERLIB"
NODE="$APP/node/bin/node"
PY="$APP/python/bin/python3"
MJS="$HERE/update_e2e.mjs"
if [ -n "${OVERLAY_SRC:-}" ]; then
  echo "  (the server build and run loop in $OVERLAY_SRC replace the payload's)"
  rm -rf "$APP/app/server/dist"
  cp -R "$OVERLAY_SRC/server/dist" "$APP/app/server/dist"
  cp "$OVERLAY_SRC/deploy/linux/studio-run.sh" "$APP/studio-run.sh"
  cp "$OVERLAY_SRC/deploy/linux/studio-recover.sh" "$APP/studio-recover.sh"
  chown -R tester "$APP"
fi
"$NODE" "$MJS" keygen "$T/keys"
chmod 644 "$T/keys/update.pub"
"$NODE" "$MJS" stamp "$APP" v98.1 "$T/keys/update.pub"
printf 'user\n' >"$APP/install-kind"
chown -R tester "$APP"
MIN_GLIBC="$("$PY" -B -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["minGlibc"])' "$APP/manifest.json")"
export MIN_GLIBC

# A release of a copy of this payload: stamped, with the given change made to it.
# release TAG [corrupt|crash]
release() {
  local tag="$1" how="${2:-}" tree="$T/stage/$1"
  mkdir -p "$tree"
  cp -a "$APP" "$tree/activate-studio"
  "$NODE" "$MJS" stamp "$tree/activate-studio" "$tag" "$T/keys/update.pub"
  # The archive carries no install-kind file: the updater writes it.
  rm -f "$tree/activate-studio/install-kind"
  # Never started, so no run state: the data directory is outside the payload.
  if [ "$how" = crash ]; then printf '#!/bin/sh\nexit 1\n' >"$tree/activate-studio/node/bin/node"; chmod 755 "$tree/activate-studio/node/bin/node"; fi
  (cd "$tree" && tar --owner=0 --group=0 --numeric-owner --sort=name -I 'gzip -1' -cf "$T/stage/activate-studio-linux-x86_64.tar.gz" activate-studio)
  rm -rf "$tree"
  "$NODE" "$MJS" add-release "$T/rel" "$FAKE_PORT" "$tag" "$T/stage/activate-studio-linux-x86_64.tar.gz" "$T/keys/update.key" "$how"
  rm -f "$T/stage/activate-studio-linux-x86_64.tar.gz"
}

free_port() { "$PY" -B -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'; }
FAKE_PORT="$(free_port)"
"$NODE" "$MJS" serve "$T/rel" "$FAKE_PORT" &
pids+=($!)

api() { "$PY" -B -I "$HERE/api.py" "$@"; }
# json PYTHON-EXPRESSION-ON-d < json
json() { "$PY" -B -I -c 'import json, sys; d = json.load(sys.stdin); sys.exit(0 if eval(sys.argv[1]) else 1)' "$1"; }
field() { "$PY" -B -I -c 'import json, sys; d = json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }
wait_version() {   # wait_version PORT VERSION SECONDS
  local n=0
  while [ "$n" -lt "$3" ]; do
    if [ "$(api -m 2 GET "http://127.0.0.1:$1/api/version" 2>/dev/null | field 'd["version"]' 2>/dev/null || true)" = "$2" ]; then return 0; fi
    sleep 1; n=$((n + 1))
  done
  return 1
}
wait_status() {   # wait_status PORT PYTHON-EXPRESSION SECONDS: until /api/updates satisfies it
  local n=0
  while [ "$n" -lt "$3" ]; do
    if api -m 5 GET "http://127.0.0.1:$1/api/updates" 2>/dev/null | json "$2" 2>/dev/null; then return 0; fi
    sleep 1; n=$((n + 1))
  done
  return 1
}
# start_loop ROOT DATA PORT: the run loop as the plain user; its process id is LOOP_PID
start_loop() {
  local root="$1" data="$2" port="$3"
  as_tester mkdir -p "$data"
  printf "PORT='%s'\nKB_ROOT='%s'\nSTUDIO_UPDATE_URL='http://127.0.0.1:%s'\n" "$port" "$T/kb" "$FAKE_PORT" | as_tester tee "$data/studio.env" >/dev/null
  rm -f "$data/loop.pid"
  as_tester env -i HOME="$H" PATH=/usr/bin:/bin /bin/bash -c 'echo $$ >"$1/loop.pid"; exec /bin/bash "$2/studio-run.sh" "$1"' sh "$data" "$root" >/dev/null 2>&1 &
  local n=0
  while [ ! -s "$data/loop.pid" ] && [ "$n" -lt 50 ]; do sleep 0.1; n=$((n + 1)); done
  LOOP_PID="$(cat "$data/loop.pid")"
  pids+=("$LOOP_PID")
}
# server_pid LOOP: the server the run loop started (a child whose command line names the server)
server_pid() {
  local s p
  for s in /proc/[0-9]*/stat; do
    p="${s#/proc/}"; p="${p%/stat}"
    [ "$(sed 's/^[0-9]* (.*) //' "$s" 2>/dev/null | awk '{print $2}')" = "$1" ] || continue
    # Node's server, or the Go server (with Node behind it, its child).
    if tr '\0' ' ' <"/proc/$p/cmdline" 2>/dev/null | grep -q -e 'app/server/dist/main.js' -e 'app/bin/studio-server'; then echo "$p"; return; fi
  done
}
stop_loop() { kill -TERM "$1" 2>/dev/null || true; local n=0; while kill -0 "$1" 2>/dev/null && [ "$n" -lt 100 ]; do sleep 0.1; n=$((n + 1)); done; }
# The last lines of a server log, without the request lines.
log_tail() { grep -v -e '"msg":"incoming request"' -e '"msg":"request completed"' "$1/logs/studio.log" 2>/dev/null | tail -n 20 | cut -c1-300 | sed 's/^/        /'; }
# A listing of every path in a tree with its mode, size, owner and modification time.
tree_state() { find "$1" -printf '%P %m %s %u %T@\n' | sort; }
# tree_same BEFORE-LISTING DIR: whether the tree is as listed; what differs, if not
tree_same() {
  local now; now="$(tree_state "$2")"
  if [ "$now" = "$1" ]; then return 0; fi
  diff <(printf '%s\n' "$1") <(printf '%s\n' "$now") | head -n 8 | sed 's/^/        /'
  return 1
}

# ---- 2. the update ----

echo "Per-user install: update to the next version"
release v98.2
DATA="$H/.local/share/activate-studio"
PORT="$(free_port)"
start_loop "$APP" "$DATA" "$PORT"
LOOP="$LOOP_PID"
if wait_version "$PORT" v98.1 90; then ok "the installed payload runs as v98.1"; else bad "the server did not start as v98.1:"; log_tail "$DATA"; fi
OLDPID="$(server_pid "$LOOP")"
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/check" '{}' >"$T/check.json" || true
if json 'd["mode"] == "app" and d["available"] and d["latest"]["version"] == "v98.2" and d["canInstall"] and d["reason"] is None' <"$T/check.json"; then
  ok "the check finds v98.2 and the install is allowed"
else bad "the check:"; sed 's/^/        /' "$T/check.json"; fi
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/install" '{}' >/dev/null || bad "the install request was refused"
if wait_version "$PORT" v98.2 120; then ok "the server answers as v98.2 after the update"; else bad "the server did not come back as v98.2:"; log_tail "$DATA"; fi
if kill -0 "$LOOP" 2>/dev/null; then ok "the same run loop process is still the supervisor"; else bad "the run loop process ended"; fi
NEWPID="$(server_pid "$LOOP")"
if [ -n "$NEWPID" ] && [ "$NEWPID" != "$OLDPID" ]; then ok "the server is a new process"; else bad "the server process did not change ($OLDPID, $NEWPID)"; fi
grep -q 'the payload was updated; its own run loop takes over' "$DATA/logs/studio.log" && ok "the log says the new payload's run loop took over" || bad "no hand over in the log"
[ "$(ls -A "$USERLIB" | sort | tr '\n' ' ')" = ".activate-studio.previous activate-studio " ] && ok "beside the payload: the previous one, and nothing else" || bad "unexpected files in $USERLIB: $(ls -A "$USERLIB" | tr '\n' ' ')"
[ "$("$PY" -B -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$USERLIB/.activate-studio.previous/app/build-info.json")" = v98.1 ] && ok "the previous payload is v98.1" || bad "the previous payload is not v98.1"
[ "$(cat "$APP/install-kind")" = user ] && ok "the new payload says it is a per-user install" || bad "install-kind is $(cat "$APP/install-kind" 2>/dev/null)"
[ "$(stat -c %U "$APP/studio-run.sh")" = tester ] && ok "the new payload's files belong to the user" || bad "the new payload's owner is $(stat -c %U "$APP/studio-run.sh")"
api -m 5 GET "http://127.0.0.1:$PORT/api/updates" | json 'd["previous"] == {"version": "v98.1"}' && ok "Settings can roll back to v98.1" || bad "no rollback offered"

# ---- 3. a corrupt download ----

echo "A corrupt release asset"
release v98.3 corrupt
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/check" '{}' >/dev/null || true
BEFORE="$(tree_state "$APP")"
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/install" '{}' >/dev/null || true
if wait_status "$PORT" 'd["state"] == "error" and "SHA-256" in d["error"]' 120; then ok "the update is refused: the archive's SHA-256 is not the manifest's"; else bad "no refusal:"; api GET "http://127.0.0.1:$PORT/api/updates" | sed 's/^/        /'; fi
tree_same "$BEFORE" "$APP" && ok "the running payload is untouched" || bad "the running payload changed"
[ "$(ls -A "$USERLIB" | sort | tr '\n' ' ')" = ".activate-studio.previous activate-studio " ] && ok "nothing was left beside it" || bad "left in $USERLIB: $(ls -A "$USERLIB" | tr '\n' ' ')"
wait_version "$PORT" v98.2 10 && ok "the server is still up, still v98.2" || bad "the server stopped answering as v98.2"

# ---- 4. a release that verifies but does not start ----

echo "A release whose server will not start"
release v98.4 crash
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/check" '{}' >/dev/null || true
api -m 30 POST "http://127.0.0.1:$PORT/api/updates/install" '{}' >/dev/null || true
# The old server answers until the swap, so wait for the run loop's note first.
n=0; while [ ! -f "$DATA/updates/rolled-back.json" ] && [ "$n" -lt 120 ]; do sleep 1; n=$((n + 1)); done
if [ -f "$DATA/updates/rolled-back.json" ] && wait_version "$PORT" v98.2 60; then
  ok "after three stops the run loop put v98.2 back, and the server answers as v98.2"
else bad "no rollback:"; log_tail "$DATA"; fi
grep -q 'v98.4 stopped 3 times within two minutes; v98.2 is back' "$DATA/logs/studio.log" && ok "the log says so" || bad "no rollback line in the log"
[ "$("$PY" -B -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print(d["from"], d["to"])' "$DATA/updates/rolled-back.json")" = "v98.4 v98.2" ] && ok "rolled-back.json names v98.4 and v98.2" || bad "rolled-back.json is wrong"
[ ! -e "$DATA/updates/pending.json" ] && ok "the update mark is cleared" || bad "pending.json is still there"
[ "$(ls -A "$USERLIB" | tr '\n' ' ')" = "activate-studio " ] && ok "the failed payload is gone and no copy is left" || bad "left in $USERLIB: $(ls -A "$USERLIB" | tr '\n' ' ')"
kill -0 "$LOOP" 2>/dev/null && ok "the same run loop process, through both hand overs" || bad "the run loop process ended"
# ---- 4b. a server killed between the two moves of an update ----

echo "A server killed between the two moves of an update"
LIBNAME=activate-studio
BESIDE="$USERLIB/.$LIBNAME"
# What the server has done when it is killed after the first move: the swap file written, the
# running payload set aside as .previous, the new one staged as .new. The kill is real.
# cut_off TAG [partial]
cut_off() {
  local tag="$1" how="${2:-}" pid
  as_tester sh -c "chmod -R u+w '$BESIDE.previous' '$BESIDE.new' 2>/dev/null; rm -rf '$BESIDE.previous' '$BESIDE.new'"
  as_tester cp -a "$APP" "$BESIDE.new"
  "$NODE" "$MJS" stamp "$BESIDE.new" "$tag" "$T/keys/update.pub"
  [ "$how" = partial ] && rm -f "$BESIDE.new/node/bin/node"
  chown -R tester "$BESIDE.new"
  as_tester mkdir -p "$DATA/updates"
  printf '{\n "from": "%s",\n "to": "%s",\n "at": "2026-11-04T00:00:00Z",\n "reopen": "no"\n}\n' "$CUR" "$tag" | as_tester tee "$DATA/updates/swap.json" >/dev/null
  pid="$(server_pid "$LOOP")"
  as_tester mv "$APP" "$BESIDE.previous"
  kill -KILL "$pid"
}
CUR=v98.2
cut_off v98.5
if wait_version "$PORT" v98.5 90; then ok "the run loop finished the swap: the server answers as v98.5"; else bad "no v98.5 after the kill:"; log_tail "$DATA"; fi
kill -0 "$LOOP" 2>/dev/null && ok "the same run loop process is the supervisor" || bad "the run loop process ended"
[ ! -e "$DATA/updates/swap.json" ] && ok "the swap file is gone" || bad "swap.json is still there"
[ "$(field 'd["to"]' <"$DATA/updates/pending.json" 2>/dev/null)" = v98.5 ] && ok "the update is recorded as pending, so a new version that keeps stopping is rolled back" || bad "no pending mark for v98.5"
[ "$("$PY" -B -I -c 'import json, sys; print(json.load(open(sys.argv[1]))["version"])' "$BESIDE.previous/app/build-info.json")" = v98.2 ] && ok "the previous payload is v98.2" || bad "the previous payload is not v98.2"
[ "$(stat -c %U "$APP/studio-run.sh")" = tester ] && ok "the payload belongs to the user" || bad "the payload's owner is $(stat -c %U "$APP/studio-run.sh")"
rm -f "$DATA/updates/pending.json"
CUR=v98.5
cut_off v98.6 partial
if wait_version "$PORT" v98.5 90; then ok "with the new payload partial, the previous one came back: v98.5 answers"; else bad "no v98.5 after the partial kill:"; log_tail "$DATA"; fi
kill -0 "$LOOP" 2>/dev/null && ok "the same run loop process, again" || bad "the run loop process ended"
[ "$("$PY" -B -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print(d["from"], d["to"])' "$DATA/updates/rolled-back.json" 2>/dev/null)" = "v98.6 v98.5" ] && ok "rolled-back.json says the update to v98.6 was interrupted" || bad "rolled-back.json is wrong"
[ ! -e "$DATA/updates/pending.json" ] && [ ! -e "$DATA/updates/swap.json" ] && ok "no pending mark and no swap file" || bad "pending.json or swap.json is left"

stop_loop "$LOOP"
as_tester sh -c "chmod -R u+w '$BESIDE.previous' '$BESIDE.new' 2>/dev/null; rm -rf '$BESIDE.previous' '$BESIDE.new'"
rm -rf "$USERLIB/activate-studio"

# ---- 5. installs a package manager owns ----

echo "Read-only under /opt, as a package leaves it"
mkdir -p /opt
tar -xzf "$ARCHIVE" -C /opt
OPT=/opt/activate-studio
# The user's copy is gone; the tools are this one's.
NODE="$OPT/node/bin/node"
PY="$OPT/python/bin/python3"
if [ -n "${OVERLAY_SRC:-}" ]; then
  rm -rf "$OPT/app/server/dist"
  cp -R "$OVERLAY_SRC/server/dist" "$OPT/app/server/dist"
  cp "$OVERLAY_SRC/deploy/linux/studio-run.sh" "$OPT/studio-run.sh"
  cp "$OVERLAY_SRC/deploy/linux/studio-recover.sh" "$OPT/studio-recover.sh"
fi
"$NODE" "$MJS" stamp "$OPT" v98.1 "$T/keys/update.pub"
chmod -R go-w,a+rX "$OPT"
chmod 755 /opt
[ "$(stat -c %U "$OPT")" = root ] && ok "the tree is root-owned" || bad "the tree is owned by $(stat -c %U "$OPT")"
as_tester sh -c "touch '$OPT/x' 2>/dev/null" && bad "the user can write to /opt/activate-studio" || ok "the user cannot write to it"
OPTDATA="$H/.local/share/activate-studio-opt"
for kind in deb rpm none; do
  if [ "$kind" = none ]; then rm -f "$OPT/install-kind"; else printf '%s\n' "$kind" >"$OPT/install-kind"; fi
  BEFORE="$(tree_state "$OPT")"
  OPTPORT="$(free_port)"
  start_loop "$OPT" "$OPTDATA" "$OPTPORT"
  OLOOP="$LOOP_PID"
  if wait_version "$OPTPORT" v98.1 90; then
    api -m 30 POST "http://127.0.0.1:$OPTPORT/api/updates/check" '{}' >"$T/opt.json" || true
    case "$kind" in
      deb) want='d["package"] == {"kind": "deb", "manager": "apt", "name": "activate-studio"} and d["command"] == "sudo apt update\nsudo apt install --only-upgrade activate-studio" and ".deb package" in d["reason"] and "apt" in d["reason"]' ;;
      rpm) want='d["package"] == {"kind": "rpm", "manager": "dnf", "name": "activate-studio"} and d["command"] == "sudo dnf upgrade --refresh activate-studio" and ".rpm package" in d["reason"] and "dnf" in d["reason"]' ;;
      none) want='("cannot write to" in d["reason"] or "package" in d["reason"]) and "git pull" not in d["reason"]' ;;
    esac
    if json 'd["mode"] == "package" and d["available"] and not d["canInstall"] and d["latest"]["version"] == "v98.4" and '"$want" <"$T/opt.json"; then
      ok "$kind: the version is shown with the package manager's wording, and no install is offered"
    else bad "$kind: the status:"; sed 's/^/        /' "$T/opt.json"; fi
    if api -m 10 POST "http://127.0.0.1:$OPTPORT/api/updates/install" '{}' >"$T/opt-install.json" 2>/dev/null; then bad "$kind: the install request was accepted"; else ok "$kind: the install request is refused (409)"; fi
    grep -q 'git pull' "$T/opt-install.json" && bad "$kind: the refusal gives the clone's advice" || true
  else
    bad "$kind: the server did not start from /opt:"; log_tail "$OPTDATA"
  fi
  stop_loop "$OLOOP"
  tree_same "$BEFORE" "$OPT" && ok "$kind: nothing in /opt/activate-studio changed" || bad "$kind: the tree changed"
  [ -z "$(ls -A /opt | grep -v '^activate-studio$')" ] && ok "$kind: nothing was written beside it" || bad "$kind: /opt holds $(ls -A /opt | tr '\n' ' ')"
done

echo
if [ "$failures" -eq 0 ]; then echo "Update end to end: all checks passed"; else echo "Update end to end: $failures check(s) FAILED"; fi
exit $(( failures > 0 ))
