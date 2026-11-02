#!/bin/bash
# Run as root in a clean rockylinux:9 container: install the shell's rpm with
# dnf, check its dependencies resolved, then start the installed shell against
# a stub server under Xvfb and take a screenshot.
#
# Usage: rpm-install-check.sh RPM_DIR OUT_DIR
set -u
RPMDIR=$1 OUT=$2
mkdir -p "$OUT"
BIN=/opt/activate-studio/activate-studio
cat /etc/os-release >"$OUT/os-release.txt"
dnf -y install "$RPMDIR"/*.rpm >"$OUT/dnf-install.log" 2>&1
echo $? >"$OUT/dnf-install.exit"
rpm -qi activate-studio >"$OUT/rpm-qi.txt" 2>&1
rpm -qR activate-studio >"$OUT/rpm-requires.txt" 2>&1
rpm -ql activate-studio >"$OUT/rpm-files.txt" 2>&1
rpm -V activate-studio >"$OUT/rpm-verify.txt" 2>&1; echo "rpm -V exit $?" >>"$OUT/rpm-verify.txt"
ldd "$BIN" >"$OUT/ldd-installed.txt" 2>&1
echo "ldd not found lines: $(grep -c 'not found' "$OUT/ldd-installed.txt")" | tee "$OUT/ldd-missing.txt"
rpm -qa --qf '%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\n' | sort >"$OUT/packages-after-rpm.txt"
rpm -q webkit2gtk3 gtk3 libsoup glibc >"$OUT/versions.txt" 2>&1
cat "$OUT/versions.txt"

# Test tools (these are not part of what the rpm requires).
dnf -y install epel-release >"$OUT/dnf-epel.log" 2>&1
dnf -y install xorg-x11-server-Xvfb ImageMagick nodejs dbus-daemon dejavu-sans-fonts procps-ng >"$OUT/dnf-tools.log" 2>&1
dbus-uuidgen --ensure 2>/dev/null
STUB=$(mktemp -d)
mkdir -p "$STUB/server/dist"
cat >"$STUB/server/dist/main.js" <<'JS'
const http = require('node:http')
const fs = require('node:fs')
const log = process.env.STUB_LOG
http.createServer((q, r) => {
  fs.appendFileSync(log, `${Date.now()} ${q.method} ${q.url} host=${q.headers.host}\n`)
  if (q.url === '/api/version') { r.setHeader('content-type', 'application/json'); r.end('{"version":"stub"}'); return }
  r.setHeader('content-type', 'text/html')
  r.end('<!doctype html><html><head><title>Stub Studio</title></head><body style="font:32px sans-serif"><div id="root">Stub Studio page, served by node and shown by the installed shell.</div></body></html>')
}).listen(Number(process.env.PORT), '127.0.0.1')
JS
Xvfb :99 -screen 0 1400x900x24 -nolisten tcp >"$OUT/xvfb.log" 2>&1 &
XPID=$!
sleep 2
run() {
  rm -f "$OUT/stub-requests.log"
  env DISPLAY=:99 NO_AT_BRIDGE=1 STUB_LOG="$OUT/stub-requests.log" STUDIO_APP_DIR="$STUB" STUDIO_NODE="$(command -v node)" \
    STUDIO_SHELL_MODE=localproxy STUDIO_SHELL_DATA_DIR="$(mktemp -d)" "$@" \
    dbus-run-session -- "$BIN" >"$OUT/shell.log" 2>&1 &
  SPID=$!
  sleep 15
  DISPLAY=:99 import -window root "$OUT/installed-shell.png" 2>"$OUT/import.log"
  if kill -0 "$SPID" 2>/dev/null; then echo alive >"$OUT/shell-state.txt"; else echo "exited" >"$OUT/shell-state.txt"; fi
  # End the shell itself with TERM (by PID, found by exact command name; the
  # wrapper's PID is not the shell's), as a logout would.
  for p in $(ps -eo pid=,comm= | awk '$2 == "activate-studio" {print $1}'); do kill -TERM "$p" 2>/dev/null; done
  sleep 4
  kill "$SPID" 2>/dev/null
}
run
if ! grep -q "GET / " "$OUT/stub-requests.log" 2>/dev/null && grep -q bwrap "$OUT/shell.log"; then
  echo "sandbox: bwrap failed, rerun without it" >"$OUT/sandbox.txt"
  cp "$OUT/shell.log" "$OUT/shell-sandboxed.log"
  run WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1
fi
cat "$OUT/stub-requests.log" 2>/dev/null
# After the shell was ended: is the stub server gone?
ps -eo pid,ppid,args | grep -F "$STUB/server/dist/main.js" | grep -v grep >"$OUT/stub-after.txt"
echo "stub servers left: $(wc -l <"$OUT/stub-after.txt")" | tee "$OUT/stub-left.txt"
kill "$XPID" 2>/dev/null
exit 0
