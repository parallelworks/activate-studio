#!/usr/bin/env bash
# Builds the .deb and the .rpm from the payload archive (the tarball
# build_app.sh makes, window shells included) with nfpm. Both install the tree
# at /opt/activate-studio, with /usr/bin/activate-studio linking to its
# launcher, a menu entry and icons under /usr/share, and install-kind in the
# tree ("deb activate-studio" or "rpm activate-studio"), which tells the
# Studio's updater that apt or dnf updates it. No
# install scripts run: nothing is written into the tree after it is installed,
# and nothing outside it but the link, the entry and the icons.
#
#   .deb  the WebKitGTK 4.1 shell (Ubuntu 22.04 and later, Debian 12 and later)
#   .rpm  the WebKitGTK 4.0 shell (RHEL 8 and 9 and their rebuilds)
#
# Usage: build_packages.sh ARCHIVE OUT_DIR
# Env:   NFPM (the nfpm binary, default: nfpm on PATH)
#        SIGNING_KEY_FILE (and NFPM_PASSPHRASE): a GPG secret key; when set the
#        packages are signed with it (nfpm's own signature)
set -euo pipefail

ARCHIVE="$(readlink -f "${1:?usage: build_packages.sh ARCHIVE OUT_DIR}")"
OUT_DIR="$(mkdir -p "${2:?usage: build_packages.sh ARCHIVE OUT_DIR}" && cd "$2" && pwd)"
NFPM="${NFPM:-nfpm}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/studio-pkg.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

echo "Unpacking $ARCHIVE ..."
tar -xzf "$ARCHIVE" -C "$WORK"
TREE="$WORK/activate-studio"
VERSION="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$TREE/manifest.json" | head -n 1)"
# The build's name (git describe: v1.185, or v1.185-5-g1079dc6 after the tag)
# as a package version: 1.185, or 1.185+5.g1079dc6, which sorts after 1.185.
# Both formats allow these characters; neither allows a hyphen in the rpm.
case "$VERSION" in
  v[0-9]*-[0-9]*-g*) v="${VERSION#v}"; PKG_VERSION="${v%%-*}+$(printf '%s' "${v#*-}" | tr '-' '.')" ;;
  v[0-9]*) PKG_VERSION="${VERSION#v}" ;;
  *) PKG_VERSION="0.0.0+${VERSION:-dev}" ;;
esac
# Three numbers, as nfpm makes them (1.185 would become 1.185.0 anyway).
PKG_VERSION="$(printf '%s' "$PKG_VERSION" | sed -E 's/^([0-9]+\.[0-9]+)(\+.*)?$/\1.0\2/')"
echo "Package version $PKG_VERSION (build $VERSION)"

sign() {   # sign FORMAT: the nfpm signature block, when a key is given
  [ -n "${SIGNING_KEY_FILE:-}" ] || return 0
  printf '  signature:\n    key_file: %s\n' "$SIGNING_KEY_FILE"
}

# One package: the format, the shell it keeps, and the one it drops.
build() {
  local fmt="$1" keep="$2" drop="$3" stage="$WORK/stage-$1"
  # Hard links: no second copy of the tree on the disk.
  cp -al "$TREE" "$stage"
  rm -f "$stage/install.sh" "$stage/uninstall.sh" "$stage/bin/activate-studio-$drop"
  rm -rf "$stage/share/applications"
  # The marker the Studio's updater reads: this copy is the package's.
  printf '%s activate-studio\n' "$fmt" >"$stage/install-kind"
  chmod 644 "$stage/install-kind"
  mkdir -p "$WORK/share-$fmt"
  sed 's|@BIN@|/usr/bin/activate-studio|g' "$HERE/activate-studio.desktop" >"$WORK/share-$fmt/activate-studio.desktop"

  local icons=""
  for s in 48 64 128 256 512; do
    icons+="  - src: $stage/share/icons/hicolor/${s}x${s}/apps/activate-studio.png
    dst: /usr/share/icons/hicolor/${s}x${s}/apps/activate-studio.png
    file_info: {mode: 0644}
"
  done
  local lib_deps
  if [ "$fmt" = deb ]; then
    # libc6 2.35 and libstdc++ 11: the 4.1 shell is built on Ubuntu 22.04.
    # The rest of the tree needs less (glibc 2.28, as on RHEL 8).
    lib_deps="    depends:
      - libwebkit2gtk-4.1-0
      - libgtk-3-0t64 | libgtk-3-0
      - libsoup-3.0-0
      - libc6 (>= 2.35)
      - libstdc++6 (>= 11)
      - libgcc-s1
      - zlib1g
    recommends:
      - xdg-utils"
  else
    lib_deps="    depends:
      - webkit2gtk3
      - gtk3
      - libc.so.6(GLIBC_2.28)(64bit)
      - libstdc++.so.6(GLIBCXX_3.4.25)(64bit)
      - libgcc_s.so.1()(64bit)
      - libz.so.1()(64bit)
    recommends:
      - xdg-utils"
  fi
  cat >"$WORK/nfpm-$fmt.yaml" <<YAML
name: activate-studio
arch: amd64
platform: linux
version: "$PKG_VERSION"
release: "1"
section: utils
priority: optional
maintainer: Parallel Works <noreply@parallelworks.com>
vendor: Parallel Works
homepage: https://github.com/parallelworks/activate-studio
license: Apache-2.0
description: |
  ACTIVATE Studio: search, query and chat over your documents.
  A window around a server that runs on this computer only, with its own
  GUFI and OCR in /opt/activate-studio. Each user has their own index
  and settings in their home folder. Updates come through the package
  manager.
contents:
  - src: $stage
    dst: /opt/activate-studio
    type: tree
  - src: /opt/activate-studio/bin/activate-studio
    dst: /usr/bin/activate-studio
    type: symlink
  - src: $WORK/share-$fmt/activate-studio.desktop
    dst: /usr/share/applications/activate-studio.desktop
    file_info: {mode: 0644}
$icons
$fmt:
  compression: zstd
$(sign "$fmt")
overrides:
  $fmt:
$lib_deps
YAML
  # nfpm reads overrides by packager; its keys are the packager's.
  "$NFPM" package --config "$WORK/nfpm-$fmt.yaml" --packager "$fmt" --target "$OUT_DIR/"
  rm -rf "$stage"
  echo "  kept the $keep shell"
}

build deb wk41 wk40
build rpm wk40 wk41
ls -l "$OUT_DIR"
