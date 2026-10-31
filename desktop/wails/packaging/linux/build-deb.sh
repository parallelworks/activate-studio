#!/bin/bash
# Builds a .deb of the shell: /opt/activate-studio/activate-studio, a
# desktop entry and an icon. The Studio itself (server/dist, web/dist and
# their production node_modules) and a Node runtime are not in it yet; a
# release would put them in /opt/activate-studio/app and point
# STUDIO_APP_DIR and STUDIO_NODE there (or the shell would find them beside
# itself).
#
# Usage: build-deb.sh BINARY ICON_PNG VERSION OUT_DIR
set -eu
BIN=$1 ICON=$2 VERSION=$3 OUT=$4
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
install -Dm755 "$BIN" "$ROOT/opt/activate-studio/activate-studio"
install -Dm644 "$HERE/activate-studio.desktop" "$ROOT/usr/share/applications/activate-studio.desktop"
install -Dm644 "$ICON" "$ROOT/usr/share/icons/hicolor/512x512/apps/activate-studio.png"
mkdir -p "$ROOT/DEBIAN"
SIZE=$(du -sk "$ROOT/opt" "$ROOT/usr" | awk '{s+=$1} END {print s}')
# WebKitGTK 4.1 (libsoup 3): Ubuntu 22.04 and later, Debian 12 and later.
cat >"$ROOT/DEBIAN/control" <<CONTROL
Package: activate-studio
Version: $VERSION
Section: utils
Priority: optional
Architecture: amd64
Installed-Size: $SIZE
Depends: libgtk-3-0t64 | libgtk-3-0, libwebkit2gtk-4.1-0, libsoup-3.0-0
Maintainer: Parallel Works <noreply@parallelworks.com>
Homepage: https://github.com/parallelworks/activate-studio
Description: ACTIVATE Studio desktop shell (Wails v2 spike)
 A native window around the ACTIVATE Studio server.
CONTROL
mkdir -p "$OUT"
dpkg-deb --root-owner-group --build "$ROOT" "$OUT/activate-studio_${VERSION}_amd64.deb"
