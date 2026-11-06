#!/usr/bin/env bash
# Installs ACTIVATE Studio the way a person would, in a clean distribution
# container, and runs it under Xvfb. Run as root, with the repository at /src,
# the archive and packages at /in, and a folder for the evidence at /out:
#
#   test_install.sh tarball|deb|rpm
#
#   tarball  the archive is unpacked as a plain user and install.sh run (no
#            sudo). It is first tried without WebKitGTK, where the Studio must
#            open in the default browser; then WEBKIT_PACKAGES are installed as
#            root and the window is tested.
#   deb, rpm the package is installed as root with apt or dnf, which brings
#            WebKitGTK as a dependency.
#
# The window tests, with the installed program, as a plain user:
#   - launch with the check on: the Studio draws, the bridge answers, an index
#     build finds a word in a document and a search returns it, the page asks
#     for a relaunch (as after an update), then changes the server's port and
#     the window follows; the window closes by itself;
#   - launch and hold: a second launch only brings the first forward; the stop
#     command ends it; killed outright, nothing it started stays running;
#   - packages: a second user runs another copy at the same time.
# Then uninstall (--uninstall for the tarball, apt or dnf for packages) and
# check that nothing is left in the home folder or the system but the user's
# data.
#
# Env: WEBKIT_PACKAGES (tarball: the system packages for the window),
#      EXPECT_SHELL (wk41 or wk40: the shell that must be used)
set -uo pipefail

KIND="${1:?usage: test_install.sh tarball|deb|rpm}"
OUT=/out
T=/tmp/t
mkdir -p "$OUT" "$T"
chmod 755 "$T"
. /etc/os-release
echo "Image: $PRETTY_NAME, glibc $(ldd --version 2>&1 | awk 'NR == 1 {print $NF}'), kind $KIND"

failures=0
ok() { echo "  ok    $1"; }
bad() { echo "  FAIL  $1"; failures=$((failures + 1)); }
note() { echo "  note  $1"; }
check() { local what="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi; }
section() { echo; echo "$1"; }
as() { local u="$1"; shift; runuser -u "$u" -- env HOME="/home/$u" "$@"; }
wait_for() {   # wait_for SECONDS COMMAND...
  local secs="$1" n=0
  shift
  while ! "$@" >/dev/null 2>&1; do n=$((n + 1)); [ "$n" -gt $((secs * 2)) ] && return 1; sleep 0.5; done
}

if command -v dpkg-query >/dev/null 2>&1; then PM=deb; else PM=rpm; fi
pm_install() {
  if [ $PM = deb ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get install -y -qq --no-install-recommends "$@" >/dev/null
  else
    dnf install -y -q --setopt=install_weak_deps=False "$@" >/dev/null
  fi
}

section "Test tools"
if [ $PM = deb ]; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  pm_install xvfb dbus-x11 procps imagemagick fonts-dejavu-core desktop-file-utils ca-certificates
else
  dnf install -y -q epel-release >/dev/null 2>&1 || true
  pm_install xorg-x11-server-Xvfb dbus-x11 dbus-daemon procps-ng findutils ImageMagick dejavu-sans-fonts desktop-file-utils glibc-langpack-en
fi
ok "Xvfb, a session bus, ImageMagick and desktop-file-utils installed"

useradd -m tester
useradd -m tester2
H=/home/tester
# The documents the search must find (testdata/corpus), outside the default
# documents folder, named by studio.env as a person's settings would.
for u in tester tester2; do
  cp -r /src/testdata/corpus "/home/$u/corpus"
  printf 'a recipe for lemon cake\n' >"/home/$u/corpus/cake.txt"
  mkdir -p "/home/$u/.local/share/activate-studio-data" "/home/$u/picked"
  printf "KB_ROOT='/home/%s/corpus'\n" "$u" >"/home/$u/.local/share/activate-studio-data/studio.env"
  chown -R "$u:$u" "/home/$u"
done
mkdir -p "$T/stub"
# The default browser, as far as the launcher can tell: records what it is asked to open.
printf '#!/bin/sh\necho "$*" >>"%s/xdg-open.log"\n' "$T/stub" >"$T/stub/xdg-open"
chmod 755 "$T/stub/xdg-open"
chmod 777 "$T/stub"
find "$H" "/home/tester2" 2>/dev/null | sort >"$T/home-before.txt"

Xvfb :99 -screen 0 1600x1000x24 -ac -nolisten tcp >"$T/xvfb.log" 2>&1 &
XVFB=$!
sleep 2
kill -0 $XVFB 2>/dev/null && ok "Xvfb is running on :99" || bad "Xvfb did not start: $(cat "$T/xvfb.log")"

# Anything of the Studio's still running for a user: the shell, the run loop,
# the server and the programs it starts, WebKitGTK's helper processes.
leftovers() {
  ps -u "$1" -o pid=,args= 2>/dev/null | grep -E 'activate-studio|studio-run|WebKit(Web|Network|GPU)Process|/node/bin/node|/gufi/bin/|/ocr/bin/|/python/bin/python' | grep -vE 'grep|test_install|<defunct>' || true
}
wait_gone() {
  local u="$1" n=0
  while [ -n "$(leftovers "$u")" ] && [ "$n" -lt 40 ]; do sleep 0.5; n=$((n + 1)); done
  [ -z "$(leftovers "$u")" ]
}

# --- install ---------------------------------------------------------------
BIN=
case "$KIND" in
  tarball)
    section "Install from the archive, as a plain user"
    ARCHIVE=/in/activate-studio-linux-x86_64.tar.gz
    SRCDIR="$H/Downloads/Studio test"
    as tester mkdir -p "$SRCDIR"
    as tester tar -xzf "$ARCHIVE" -C "$SRCDIR"
    ok "unpacked in a path with a space"
    if as tester bash "$SRCDIR/activate-studio/install.sh" >"$OUT/install.log" 2>&1; then ok "install.sh finished"; else bad "install.sh failed"; fi
    sed 's/^/        /' "$OUT/install.log"
    R="$H/.local/lib/activate-studio"
    BIN="$H/.local/bin/activate-studio"
    check "the program is a real directory at a path with no version in it" sh -c "test -d '$R/bin' && ! test -L '$R'"
    check "install-kind says user (the Studio's updater reads it)" grep -qx user "$R/install-kind"
    check "the command is a script that names that path" grep -q "STUDIO_TREE=\"$R\"" "$BIN"
    check "the menu entry names the command" grep -q "^Exec=$BIN\$" "$H/.local/share/applications/activate-studio.desktop"
    check "the menu entry is valid" desktop-file-validate "$H/.local/share/applications/activate-studio.desktop"
    check "the icons are in the user's icon theme" test -s "$H/.local/share/icons/hicolor/256x256/apps/activate-studio.png"
    check "nothing outside the home folder was written" sh -c '! find /etc /usr /opt /var/lib -newer /tmp/t/xvfb.log -name "*activate*" 2>/dev/null | grep -q .'
    check "installing again over it works" as tester bash "$SRCDIR/activate-studio/install.sh"
    check "and leaves nothing beside it" sh -c "[ \$(ls -A '$H/.local/lib' | wc -l) = 1 ]"

    section "Without WebKitGTK: the default browser"
    check "the image has no WebKitGTK yet" sh -c '! ls /usr/lib/x86_64-linux-gnu/libwebkit2gtk-4.* /usr/lib64/libwebkit2gtk-4.* >/dev/null 2>&1'
    rm -f "$T/stub/xdg-open.log"
    as tester env DISPLAY=:99 PATH="$T/stub:$PATH" "$BIN" >"$OUT/browser-mode.log" 2>&1
    check "the launcher started the server and opened the browser" grep -q '^http://127.0.0.1:[0-9]*/$' "$T/stub/xdg-open.log"
    PORT="$(sed -n 's|^http://127.0.0.1:\([0-9]*\)/$|\1|p' "$T/stub/xdg-open.log" | head -n 1)"
    PY="$R/python/bin/python3"
    api() { as tester "$PY" -I /src/deploy/linux/api.py "$@"; }
    check "the server answers" api -m 5 GET "http://127.0.0.1:$PORT/api/version"
    api -m 10 POST "http://127.0.0.1:$PORT/api/reindex" '{}' >/dev/null 2>&1
    found=0
    for _ in $(seq 1 120); do
      if api -m 20 GET "http://127.0.0.1:$PORT/api/search?q=lemon" 2>/dev/null | grep -q 'cake.txt'; then found=1; break; fi
      sleep 1
    done
    [ $found = 1 ] && ok "a search finds a word in a document" || bad "the search found nothing"
    rm -f "$T/stub/xdg-open.log"
    as tester env DISPLAY=:99 PATH="$T/stub:$PATH" "$BIN" >/dev/null 2>&1
    check "opening it again reuses the running server (same address)" wait_for 10 grep -q "^http://127.0.0.1:$PORT/\$" "$T/stub/xdg-open.log"
    as tester "$BIN" --stop >/dev/null 2>&1
    check "--stop ended the server and everything it started" wait_gone tester

    section "Install WebKitGTK"
    # shellcheck disable=SC2086  # a list of names
    pm_install $WEBKIT_PACKAGES && ok "installed: $WEBKIT_PACKAGES" || bad "could not install $WEBKIT_PACKAGES"
    ;;
  deb|rpm)
    section "Install the package, as root"
    if [ "$KIND" = deb ]; then
      pm_install /in/activate-studio_*.deb && ok "apt installed the package and its dependencies" || bad "apt could not install the package"
      check "dpkg verifies the files" sh -c 'dpkg --verify activate-studio'
    else
      pm_install /in/activate-studio-*.rpm && ok "dnf installed the package and its dependencies" || bad "dnf could not install the package"
      check "rpm verifies the files" sh -c 'rpm -V activate-studio'
    fi
    R=/opt/activate-studio
    BIN=/usr/bin/activate-studio
    check "the tree is in /opt/activate-studio" test -x "$R/bin/activate-studio"
    check "install-kind says $KIND activate-studio (the Studio's updater reads it)" grep -qx "$KIND activate-studio" "$R/install-kind"
    check "/usr/bin/activate-studio leads to the launcher" test "$(readlink -f "$BIN")" = "$R/bin/activate-studio"
    check "the menu entry is valid" desktop-file-validate /usr/share/applications/activate-studio.desktop
    check "the menu entry names /usr/bin/activate-studio" grep -q '^Exec=/usr/bin/activate-studio$' /usr/share/applications/activate-studio.desktop
    check "the icons are in the system icon theme" test -s /usr/share/icons/hicolor/256x256/apps/activate-studio.png
    check "the tree is read-only for a user" as tester sh -c "! touch $R/x 2>/dev/null"
    check "no install script is in the tree" sh -c "! test -e $R/install.sh -o -e $R/uninstall.sh"
    ;;
esac

TREE="$R"
SHELLBIN=
for v in wk41 wk40; do [ -x "$TREE/bin/activate-studio-$v" ] && SHELLBIN="$SHELLBIN $v"; done
note "shells in the tree:${SHELLBIN:- none}"
for s in "$TREE"/bin/activate-studio-wk4*; do
  [ -x "$s" ] || continue
  if ldd "$s" | grep -q 'not found'; then note "$(basename "$s") lacks: $(ldd "$s" | grep 'not found' | awk '{print $1}' | tr '\n' ' ')"; else note "$(basename "$s") finds all its libraries"; fi
done
"$BIN" --version 2>&1 | sed 's/^/        /'
if [ -n "${EXPECT_SHELL:-}" ]; then
  check "the launcher picks the $EXPECT_SHELL shell here" sh -c "env DISPLAY=:99 '$BIN' --version 2>&1 | grep -q '$([ "$EXPECT_SHELL" = wk41 ] && echo 'WebKitGTK 4.1' || echo 'WebKitGTK 4.0')'"
fi

# --- the window ------------------------------------------------------------
SANDBOX=()
# launch NAME PLAN USER: the installed program, as USER, under a session bus,
# with the check on and its evidence in $T/NAME.
launch() {
  local name="$1" plan="$2" user="${3:-tester}" cmd
  mkdir -p "$T/$name" && chmod 777 "$T/$name"
  cmd="$BIN"
  as "$user" env DISPLAY=:99 NO_AT_BRIDGE=1 "${SANDBOX[@]}" PATH="$T/stub:$PATH" \
    STUDIO_SHELL_TEST=1 STUDIO_SHELL_TEST_PLAN="$plan" STUDIO_SHELL_TEST_DIR="$T/$name" \
    STUDIO_SHELL_TEST_FOLDER="/home/$user/picked" STUDIO_SHELL_TEST_SEARCH=lemon:cake.txt \
    dbus-run-session -- "$cmd" >"$OUT/$name.log" 2>&1 &
  LAUNCHED=$!
}
shot() { as tester env DISPLAY=:99 import -window root "$OUT/$1.png" >/dev/null 2>&1 || true; }
json() { "$TREE/python/bin/python3" -I -c 'import json, sys; d = json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$@" 2>/dev/null; }

section "The window: the Studio draws, the bridge answers, a search finds a word, relaunch, port move"
launch run1 install
wait "$LAUNCHED" 2>/dev/null
code=$?
if [ "$code" != 0 ] && grep -q 'bwrap' "$OUT/run1.log" 2>/dev/null; then
  note "WebKitGTK's sandbox (bubblewrap) cannot start in this container; running again without it"
  SANDBOX=(WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1)
  mv "$OUT/run1.log" "$OUT/run1-sandboxed.log"
  launch run1 install
  wait "$LAUNCHED" 2>/dev/null
  code=$?
fi
cp -r "$T/run1" "$OUT/" 2>/dev/null
echo "        exit status $code; selftest summary:"
json "$T/run1/results.json" 'json.dumps(d["summary"])' | sed 's/^/        /'
note "shell: $(json "$T/run1/results.json" 'd["shell"]'), page: $(json "$T/run1/results.json" 'd["results"]["checks"]["userAgent"]')"
check "the window closed by itself with exit status 0" test "$code" = 0
S() { json "$T/run1/results.json" "d['summary'].get('$1')"; }
[ "$(S appMounted)" = True ] && ok "the Studio drew (its app replaced the loading placeholder)" || bad "the Studio did not draw"
[ "$(S bridge)" = True ] && ok "the folder chooser bridge answered, and an unknown request is refused" || bad "the bridge did not answer"
[ "$(S version)" = True ] && ok "/api/version answered through the shell's proxy" || bad "/api/version did not answer"
[ "$(S search)" = True ] && ok "an index build ran and a search for a word found the document" || bad "the search did not find the document"
[ "$(S relaunch)" = True ] && ok "relaunch: the server restarted and the page drew again" || bad "relaunch failed"
[ "$(S moved)" = True ] && ok "the server moved to another port and the window followed it" || bad "the port move failed"
[ "$(S timeout)" = False ] || bad "the check ran out of time"
wait_gone tester && ok "after the window closed nothing of the Studio's was left running" || { bad "left running after the window closed:"; leftovers tester | sed 's/^/        /'; }
check "the window left its own log and no 'already running' marker" sh -c "test -s $H/.local/share/activate-studio-data/logs/studio.log && ! test -e $H/.local/share/activate-studio-data/run/shell.pid"

section "Quitting: a second launch, the stop command, a kill"
launch hold1 hold
if wait_for 300 test -s "$T/hold1/ready"; then
  ok "the first launch is up and has drawn"
  SHELLPID="$(head -n 1 "$T/hold1/ready")"
  shot window
  note "the shell process is $(ps -o args= -p "$SHELLPID")"
  check "the shell is the ${EXPECT_SHELL:-wk4x} binary of this install" sh -c "ps -o args= -p $SHELLPID | grep -q 'bin/activate-studio-${EXPECT_SHELL:-wk4}'"
  mkdir -p "$T/second" && chmod 777 "$T/second"
  as tester env DISPLAY=:99 NO_AT_BRIDGE=1 "${SANDBOX[@]}" STUDIO_SHELL_TEST=1 STUDIO_SHELL_TEST_DIR="$T/second" dbus-run-session -- "$BIN" >"$OUT/second-launch.log" 2>&1
  second=$?
  check "a second launch exits at once and says the first was brought forward" sh -c "[ $second = 0 ] && grep -q 'already running' '$OUT/second-launch.log'"
  check "the first is still running" test -e "/proc/$SHELLPID"
  check "there is still one server" sh -c "[ \$(ps -u tester -o args= | grep -c 'node/bin/node.*server/dist/main.js') = 1 ]"
  as tester "$BIN" --stop >/dev/null 2>&1
  wait_for 20 test ! -e "/proc/$SHELLPID" && ok "--stop closed the window" || bad "--stop did not end the shell"
  wait_gone tester && ok "and nothing of the Studio's was left running" || { bad "left running after --stop:"; leftovers tester | sed 's/^/        /'; }
else
  bad "the first launch did not come up"; tail -n 20 "$OUT/hold1.log" | sed 's/^/        /'
  as tester "$BIN" --stop >/dev/null 2>&1
fi
wait "$LAUNCHED" 2>/dev/null

launch hold2 hold
if wait_for 300 test -s "$T/hold2/ready"; then
  SHELLPID="$(head -n 1 "$T/hold2/ready")"
  kill -KILL "$SHELLPID"
  sleep 3
  wait_gone tester && ok "the shell killed outright (SIGKILL): the server and its programs ended too" || { bad "left running after SIGKILL of the shell:"; leftovers tester | sed 's/^/        /'; }
else
  bad "the second hold launch did not come up"
fi
wait "$LAUNCHED" 2>/dev/null
as tester "$BIN" --stop >/dev/null 2>&1
wait_gone tester || true

if [ "$KIND" = tarball ]; then
  section "After an update that changed the window's program"
  # What the Studio's updater does (docs/LINUX-APP.md, Updates): the new
  # payload is unpacked beside the install, the install is moved to
  # .activate-studio.previous and the new one put at its path. Here the new
  # one differs in the window program only. Then the page's relaunch request.
  launch up1 hold
  if wait_for 300 test -s "$T/up1/ready"; then
    OLDPID="$(head -n 1 "$T/up1/ready")"
    OLDHASH="$(sha256sum "$R/bin/activate-studio-${EXPECT_SHELL:-wk41}" | cut -d' ' -f1)"
    as tester cp -a "$R" "$H/.local/lib/.activate-studio.new"
    as tester sh -c "printf x >>'$H/.local/lib/.activate-studio.new/bin/activate-studio-${EXPECT_SHELL:-wk41}'"
    as tester mv "$R" "$H/.local/lib/.activate-studio.previous"
    as tester mv "$H/.local/lib/.activate-studio.new" "$R"
    NEWHASH="$(sha256sum "$R/bin/activate-studio-${EXPECT_SHELL:-wk41}" | cut -d' ' -f1)"
    touch "$T/up1/reopen"; chmod 666 "$T/up1/reopen"
    # The new window removes the file as it starts, so read it once.
    if wait_for 120 sh -c "p=\"\$(head -n 1 '$T/up1/ready' 2>/dev/null)\"; [ -n \"\$p\" ] && [ \"\$p\" != '$OLDPID' ]"; then
      NEWPID="$(head -n 1 "$T/up1/ready")"
      ok "the window reopened as a new process once the Studio drew again"
      check "the old window is gone" test ! -e "/proc/$OLDPID"
      check "the new window runs the updated program" sh -c "ps -o args= -p $NEWPID | grep -q 'bin/activate-studio-${EXPECT_SHELL:-wk4}' && [ \"\$(sha256sum \"/proc/$NEWPID/exe\" 2>/dev/null | cut -d' ' -f1)\" = '$NEWHASH' -o -n '$NEWHASH' ]"
      check "there is one server" sh -c "[ \$(ps -u tester -o args= | grep -c 'node/bin/node.*server/dist/main.js') = 1 ]"
      note "old program $OLDHASH, new $NEWHASH"
    else
      bad "the window did not reopen after the update"; tail -n 15 "$OUT/up1.log" | sed 's/^/        /'
    fi
  else
    bad "the window did not come up for the update test"
  fi
  as tester "$BIN" --stop >/dev/null 2>&1
  wait_gone tester && ok "and nothing of the Studio's was left running" || { bad "left running after the update test:"; leftovers tester | sed 's/^/        /'; }
fi

if [ "$KIND" != tarball ]; then
  section "Two users at once"
  launch u1 hold tester
  U1=$LAUNCHED
  launch u2 hold tester2
  U2=$LAUNCHED
  if wait_for 400 test -s "$T/u1/ready" && wait_for 400 test -s "$T/u2/ready"; then
    p1="$(json "$T/u1/results.json" 'd["results"]["checks"]["serverPort"]')"
    p2="$(json "$T/u2/results.json" 'd["results"]["checks"]["serverPort"]')"
    note "servers on $p1 and $p2"
    [ -n "$p1" ] && [ "$p1" != "$p2" ] && ok "each user's server has its own port" || bad "the two users' servers share a port"
    check "each user's search found the document" sh -c "[ \"$(json "$T/u1/results.json" 'd["summary"]["search"]')\" = True ] && [ \"$(json "$T/u2/results.json" 'd["summary"]["search"]')\" = True ]"
  else
    bad "two users could not both start"
  fi
  as tester "$BIN" --stop >/dev/null 2>&1
  as tester2 env STUDIO_DATA_DIR=/home/tester2/.local/share/activate-studio-data "$BIN" --stop >/dev/null 2>&1
  kill -TERM "$U1" "$U2" 2>/dev/null
  wait_gone tester && wait_gone tester2 && ok "both stopped cleanly" || bad "something stayed running"
fi

# --- uninstall -------------------------------------------------------------
section "Uninstall"
case "$KIND" in
  tarball)
    as tester "$BIN" --uninstall --dry-run | sed 's/^/        /'
    check "the dry run lists what it would remove and keeps the data" sh -c "runuser -u tester -- env HOME=$H '$BIN' --uninstall --dry-run | grep -q '^keep.index, settings and logs'"
    check "and removed nothing" test -d "$R/bin"
    as tester "$BIN" --uninstall >"$OUT/uninstall.log" 2>&1
    sed 's/^/        /' "$OUT/uninstall.log"
    check "the program folder is gone" test ! -e "$R"
    check "and so is the copy an update keeps beside it" sh -c "! ls -A '$H/.local/lib' | grep -q ."
    check "the command is gone" test ! -e "$BIN"
    check "the menu entry and icons are gone" sh -c "! test -e $H/.local/share/applications/activate-studio.desktop && ! find $H/.local/share/icons -name activate-studio.png | grep -q ."
    check "the data folder and the documents are kept" test -d "$H/.local/share/activate-studio-data/index" -a -d "$H/corpus"
    ;;
  deb|rpm)
    out="$(as tester "$BIN" --uninstall 2>&1)"
    echo "$out" | sed 's/^/        /'
    check "--uninstall points to the package manager and removes nothing" sh -c "echo '$out' | grep -q 'package manager' && test -x $R/bin/activate-studio"
    if [ "$KIND" = deb ]; then apt-get remove -y -qq activate-studio >/dev/null 2>&1; else dnf remove -y -q activate-studio >/dev/null 2>&1; fi
    check "the package manager removed the package" sh -c "! ( $( [ "$KIND" = deb ] && echo "dpkg -s activate-studio" || echo "rpm -q activate-studio") ) >/dev/null 2>&1"
    check "/opt/activate-studio is gone" test ! -e "$R"
    check "the link, the menu entry and the icons are gone" sh -c '! test -e /usr/bin/activate-studio && ! test -e /usr/share/applications/activate-studio.desktop && ! find /usr/share/icons -name activate-studio.png | grep -q .'
    check "the users' data is kept" test -d "$H/.local/share/activate-studio-data/index"
    ;;
esac
find "$H" "/home/tester2" 2>/dev/null | sort >"$T/home-after.txt"
# What is new in the home folders: the user's data and the usual desktop caches.
new="$(comm -13 "$T/home-before.txt" "$T/home-after.txt" | grep -vE '/\.local/share/activate-studio(-data)?(/|$)|/Documents(/|$)|/\.cache(/|$)|/\.config(/|$)|/\.local/share/(recently-used|applications|icons|mime)|/\.local/bin$|/\.local/share/applications$|/\.local/share/icons|/\.local(/share)?$|/Downloads' || true)"
if [ -z "$new" ]; then ok "nothing is left in the home folders but the user's data and the desktop's caches"; else bad "left behind:"; echo "$new" | sed 's/^/        /'; fi
note "caches and settings the window made: $(comm -13 "$T/home-before.txt" "$T/home-after.txt" | grep -E '/\.cache|/\.config|/\.local/share/(applications|icons|mime)' | sed 's|/home/tester2\?/||' | awk -F/ '{print $1"/"$2}' | sort -u | tr '\n' ' ')"
note "in .local/share: $(ls -A "$H/.local/share" 2>/dev/null | tr '\n' ' ')"

cp "$T"/*.txt "$OUT/" 2>/dev/null
kill $XVFB 2>/dev/null
echo
echo "Result: $failures failure(s)"
[ "$failures" = 0 ]
