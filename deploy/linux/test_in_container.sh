#!/usr/bin/env bash
# Run inside a clean distribution container (as root, with the repository at
# /src and the payload archive in /in) to check the Linux payload on that
# distribution, the way a person who unpacked it would run it: as a plain
# user, in a path with a space in it, on the packages the image has plus
# only the ones named in EXTRA_PACKAGES (installed first, and printed, so a
# run records exactly what the payload needed beyond the base image); then
# the update path (test_update_e2e.sh).
#
# The workflow runs it with:
#   docker run --rm -v "$PWD:/src:ro" -v "$IN:/in:ro" -e EXTRA_PACKAGES=... IMAGE bash /src/deploy/linux/test_in_container.sh
set -euo pipefail

ARCHIVE=/in/activate-studio-linux-x86_64.tar.gz
[ -f "$ARCHIVE" ] || { echo "no $ARCHIVE" >&2; exit 2; }
. /etc/os-release
echo "Image: $PRETTY_NAME, glibc $(ldd --version 2>&1 | awk 'NR == 1 {print $NF}')"

if command -v dpkg-query >/dev/null 2>&1; then PKG=deb; else PKG=rpm; fi
echo "Packages in the image: $(if [ $PKG = deb ]; then dpkg-query -W | wc -l; else rpm -qa | wc -l; fi)"
if [ -n "${EXTRA_PACKAGES:-}" ]; then
  echo "Extra packages installed first: $EXTRA_PACKAGES"
  if [ $PKG = deb ]; then
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq >/dev/null
    # shellcheck disable=SC2086  # a list of names
    apt-get install -y -qq --no-install-recommends $EXTRA_PACKAGES >/dev/null
  else
    # shellcheck disable=SC2086
    dnf install -y -q --setopt=install_weak_deps=False $EXTRA_PACKAGES >/dev/null
  fi
else
  echo "Extra packages installed first: none"
fi

useradd -m tester
# A path with a space: the payload runs from wherever it is unpacked.
runuser -u tester -- bash -c 'mkdir -p "$HOME/My Studio" && tar -xzf /in/activate-studio-linux-x86_64.tar.gz -C "$HOME/My Studio"'
APP="/home/tester/My Studio/activate-studio"

echo
echo "Payload manifest"
sed 's/^/  /' "$APP/manifest.json" | head -n 40

status=0
runuser -u tester -- env HOME=/home/tester bash /src/deploy/linux/check_app.sh "$APP" --run || status=$?

# The update path end to end (a per-user install updating itself, a corrupt
# download, a release that will not start, and the read-only /opt installs a
# package leaves), against a local stand-in for the release server. Set
# UPDATE_E2E=0 to skip it.
# It drives its stand-in release server with the payload's Node, so a payload
# without Node (node/PLACEHOLDER) skips it; bridge-e2e updates that payload
# from the last release instead.
if [ -e "$APP/node/PLACEHOLDER" ]; then
  echo
  echo "Update end to end: skipped, the payload has no Node to serve the stand-in feed (bridge-e2e covers it)"
elif [ "${UPDATE_E2E:-1}" = 1 ]; then
  echo
  bash /src/deploy/linux/test_update_e2e.sh || status=$?
fi

echo
echo "Dependencies"
echo "  the system libraries the payload's ELF files need, and the package that provides each here:"
# Some images (Oracle Linux) ship without the linker cache.
[ -e /etc/ld.so.cache ] || ldconfig 2>/dev/null || true
for lib in $(sed -n 's/^ *"\(lib[^"]*\|ld-linux[^"]*\)",\{0,1\}$/\1/p' "$APP/manifest.json"); do
  path="$(ldconfig -p | awk -v l="$lib" '$1 == l {print $NF; exit}')"
  if [ -z "$path" ]; then echo "  $lib: NOT FOUND"; continue; fi
  owner=
  for p in "$path" "$(readlink -f "$path")"; do
    [ -n "$owner" ] && break
    if [ $PKG = deb ]; then owner="$(dpkg -S "$p" 2>/dev/null | cut -d: -f1 | head -n 1 || true)"; else owner="$(rpm -qf "$p" 2>/dev/null | head -n 1 || true)"; fi
  done
  echo "  $lib: ${owner:-unknown}"
done
exit "$status"
