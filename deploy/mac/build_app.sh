#!/usr/bin/env bash
# Build "ACTIVATE Studio.app" for macOS on Apple silicon from this checkout.
#
# The app carries everything it runs: Node for macOS arm64, the built server
# and web app with production node_modules, GUFI (from GUFI's own macOS
# package, or built for an older macOS by deploy/mac/build_gufi.sh; either
# way loading its libraries from inside the app), a standalone Python for
# the indexer, and the embedding model. It builds on macOS or Linux; the
# .dmg is made only on macOS, where hdiutil exists.
#
# Usage: deploy/mac/build_app.sh
#   OUT_DIR     where the outputs go (default deploy/mac/out)
#   CACHE_DIR   downloads, kept between builds (default deploy/mac/.cache)
#   GUFI_TARBALL  a gufi-macos-arm64.tar.gz from build_gufi.sh, used instead
#               of GUFI's package when set
#   GUFI_PKG    a GUFI macOS .pkg on disk to use instead of the pinned download
#   SKIP_BUILD  1 to use the existing server/dist and web/dist
#   MIN_MACOS   LSMinimumSystemVersion; by default the newest macOS that any
#               bundled binary declares it needs
#
# Outputs in OUT_DIR: ACTIVATE Studio.app; activate-studio-macos-arm64.tar.gz
# with its .sha256 and install.sh (the release assets install.sh fetches);
# activate-studio-macos-arm64.dmg on macOS.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
APP_NAME="ACTIVATE Studio"
APP="$OUT_DIR/$APP_NAME.app"
RES="$APP/Contents/Resources"
ASSET=activate-studio-macos-arm64
BUNDLE_ID=com.parallelworks.activate-studio

# Pinned inputs. Every download is checked against its SHA-256 on each
# build, so a corrupt or replaced cache entry is caught too.
NODE_VERSION=v26.10.0    # as deploy/make_bundle.sh; package.json asks for Node 26
NODE_SHA256=751fdf7439f115d87ee2a8f3f18c065b6151852068e3e666ac60ac2996f75ac9
PY_TAG=20261013          # python-build-standalone release
PY_VERSION=3.12.15
PY_SHA256=9866e11cf2ae9caa1e671383786f248859a52dd8bfb702fb4c0dc2c65f1d36de
GUFI_PKG_URL=https://github.com/mar-file-system/GUFI/releases/download/macos-pkg-test/gufi-0.7.0.macOS.arm64.pkg
GUFI_PKG_SHA256=deb8e254c774dcf3324211b1a368380f17fa23d059883af45004c06470ba4826
GUFI_COMMIT=1713537a4b8fbc188fa75f8e9845edcaf8bf69ba    # that release's tag, for LICENSE.txt
GUFI_LICENSE_SHA256=9e40647012a77d1fdcac90a9b7241843900cb589631b5c7f7cbad793a91828aa
# GUFI's package links libomp and pcre2 from a Homebrew install it does not
# carry; the app takes them from Homebrew's macOS 15 bottles.
LIBOMP_VERSION=23.1.4
LIBOMP_SHA256=af8425bbe39630ec9b2022b67449ac737e0ed6d48e98a6309f841295d3c6f0f2
PCRE2_VERSION=10.49
PCRE2_SHA256=0ec47d18c50f5770db2b3f07b5b85644284cc6ee8e07e23b11d2727e95932b77
MODEL_COMMIT=7a7bac37782986fe1d4f213de771a8a3d9170b35    # as deploy/app.def
MODEL_SHA256=71f1d177171468fb5f186c07019e303015aea17af275a67767760bba7be8d2e6

for tool in python3 curl tar git pnpm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "build_app: $tool is required" >&2; exit 1; }
done

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'
}

# fetch URL FILE SHA256 [curl options]: download into the cache once.
fetch() {
  local url="$1" file="$2" sum="$3"
  shift 3
  if [ ! -f "$file" ] || [ "$(sha256_of "$file")" != "$sum" ]; then
    echo "  fetching $url"
    curl -fsSL "$@" -o "$file.part" "$url"
    mv "$file.part" "$file"
  fi
  local got
  got="$(sha256_of "$file")"
  if [ "$got" != "$sum" ]; then
    echo "build_app: $file has SHA-256 $got, expected $sum" >&2
    exit 1
  fi
}

mkdir -p "$OUT_DIR" "$CACHE_DIR"
WORK="$(mktemp -d "$OUT_DIR/.work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

DESCRIBE="$(git -C "$PROJECT_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
case "$DESCRIBE" in v*) VERSION="$DESCRIBE" ;; *) VERSION=dev ;; esac
SHORT_VERSION="$(printf '%s' "$VERSION" | sed -n 's/^v\([0-9][0-9.]*[0-9]\).*/\1/p')"
SHORT_VERSION="${SHORT_VERSION:-0.0}"
DISTANCE="$(printf '%s' "$VERSION" | sed -n 's/^v[0-9.]*-\([0-9][0-9]*\)-g.*/\1/p')"
BUNDLE_VERSION="$SHORT_VERSION${DISTANCE:+.$DISTANCE}"
echo "Building $APP_NAME.app $VERSION ($COMMIT)"

if [ "${SKIP_BUILD:-}" != 1 ]; then
  echo "Building web and server..."
  (cd "$PROJECT_ROOT" && pnpm build >/dev/null)
fi
for f in server/dist/main.js web/dist/index.html; do
  [ -f "$PROJECT_ROOT/$f" ] || { echo "build_app: $f is missing; run pnpm build" >&2; exit 1; }
done

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$RES/app/server" "$RES/app/web" "$RES/app/deploy" "$RES/node/bin" "$RES/models"

echo "Staging the server's production dependencies for macOS arm64 (pnpm deploy)..."
# --os and --cpu pick the macOS arm64 builds of platform packages (the
# document converter's native binary) whatever this host is.
(cd "$PROJECT_ROOT" && pnpm --filter @activate-studio/server --prod deploy --legacy \
  --os darwin --cpu arm64 "$WORK/server" >/dev/null)
# The deploy records a production-only install for the workspace, which the
# next pnpm command would act on; record the development one again (as
# deploy/make_bundle.sh does).
(cd "$PROJECT_ROOT" && { pnpm install --frozen-lockfile --offline >/dev/null 2>&1 || pnpm install --frozen-lockfile >/dev/null; })
mv "$WORK/server/node_modules" "$RES/app/server/node_modules"
cp "$WORK/server/package.json" "$RES/app/server/package.json"
cp -R "$PROJECT_ROOT/server/dist" "$RES/app/server/dist"

echo "Staging web, indexer, docs, change log, starters..."
cp -R "$PROJECT_ROOT/web/dist" "$RES/app/web/dist"
cp -R "$PROJECT_ROOT/indexer" "$RES/app/indexer"
cp -R "$PROJECT_ROOT/docs" "$RES/app/docs"
# What's new and the notice after an update read it.
cp "$PROJECT_ROOT/CHANGELOG.md" "$RES/app/CHANGELOG.md"
cp -R "$PROJECT_ROOT/extensions-starter" "$RES/app/extensions-starter"
cp "$PROJECT_ROOT/deploy/gufi-index.yaml" "$RES/app/deploy/"
cp "$PROJECT_ROOT/package.json" "$RES/app/package.json"
find "$RES/app/indexer" -name __pycache__ -type d -prune -exec rm -rf {} +
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '{\n "version": "%s",\n "commit": "%s",\n "builtAt": "%s"\n}\n' \
  "$VERSION" "$COMMIT" "$BUILT_AT" >"$RES/app/build-info.json"

echo "Node $NODE_VERSION for macOS arm64..."
NODE_DIST="node-$NODE_VERSION-darwin-arm64"
SUMS="$CACHE_DIR/node-$NODE_VERSION-SHASUMS256.txt"
[ -f "$SUMS" ] || curl -fsSL -o "$SUMS" "https://nodejs.org/dist/$NODE_VERSION/SHASUMS256.txt"
LISTED="$(awk -v f="$NODE_DIST.tar.gz" '$2 == f {print $1}' "$SUMS")"
if [ "$LISTED" != "$NODE_SHA256" ]; then
  echo "build_app: SHASUMS256.txt lists $LISTED for $NODE_DIST.tar.gz; pinned is $NODE_SHA256" >&2
  exit 1
fi
fetch "https://nodejs.org/dist/$NODE_VERSION/$NODE_DIST.tar.gz" "$CACHE_DIR/$NODE_DIST.tar.gz" "$NODE_SHA256"
tar -xzf "$CACHE_DIR/$NODE_DIST.tar.gz" -C "$WORK" "$NODE_DIST/bin/node" "$NODE_DIST/LICENSE"
mv "$WORK/$NODE_DIST/bin/node" "$RES/node/bin/node"
mv "$WORK/$NODE_DIST/LICENSE" "$RES/node/LICENSE"

echo "Python $PY_VERSION (python-build-standalone $PY_TAG)..."
PY_FILE="cpython-$PY_VERSION+$PY_TAG-aarch64-apple-darwin-install_only_stripped.tar.gz"
fetch "https://github.com/astral-sh/python-build-standalone/releases/download/$PY_TAG/${PY_FILE/+/%2B}" \
  "$CACHE_DIR/$PY_FILE" "$PY_SHA256"
tar -xzf "$CACHE_DIR/$PY_FILE" -C "$RES"
# The indexer uses the standard library only: no Tk, IDLE, pip or headers.
(cd "$RES/python" && rm -rf include share lib/itcl* lib/tcl* lib/tk* lib/thread* lib/libtcl* \
  lib/python3.*/idlelib lib/python3.*/tkinter lib/python3.*/turtledemo lib/python3.*/ensurepip \
  lib/python3.*/lib2to3 lib/python3.*/site-packages/pip* lib/python3.*/lib-dynload/_tkinter* \
  bin/idle3* bin/pip* bin/2to3* bin/pydoc3* bin/python3*-config)

G="$RES/gufi"
if [ -n "${GUFI_TARBALL:-}" ]; then
  # A tree from build_gufi.sh, already relinked, signed and checked against
  # the macOS release it was built for; it carries its own README,
  # licenses and manifest.
  echo "GUFI from $GUFI_TARBALL (deploy/mac/build_gufi.sh)..."
  [ -f "$GUFI_TARBALL" ] || { echo "build_app: GUFI_TARBALL $GUFI_TARBALL does not exist" >&2; exit 1; }
  GUFI_SUM="$(sha256_of "$GUFI_TARBALL")"
  if [ -f "$GUFI_TARBALL.sha256" ] && [ "$(awk '{print $1; exit}' "$GUFI_TARBALL.sha256")" != "$GUFI_SUM" ]; then
    echo "build_app: $GUFI_TARBALL has SHA-256 $GUFI_SUM, not the one its .sha256 names" >&2
    exit 1
  fi
  mkdir -p "$WORK/gufi-tarball"
  tar -xzf "$GUFI_TARBALL" -C "$WORK/gufi-tarball"
  for f in manifest.json bin/gufi_query lib/gufi_vt.dylib licenses/GUFI-LICENSE.txt; do
    [ -e "$WORK/gufi-tarball/gufi/$f" ] || {
      echo "build_app: $GUFI_TARBALL has no gufi/$f; it is not an archive from build_gufi.sh" >&2; exit 1; }
  done
  mv "$WORK/gufi-tarball/gufi" "$G"
  python3 - "$G/manifest.json" "$GUFI_SUM" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
print(f"  GUFI {m['gufi']['version']} ({m['gufi']['commit'][:12]}) for macOS {m['deploymentTarget']}, "
      f"built {m['builtAt']}; SHA-256 {sys.argv[2]}")
PY
else
  echo "GUFI from its macOS package..."
  if [ -n "${GUFI_PKG:-}" ]; then
    PKG="$GUFI_PKG"
    echo "  using $PKG (SHA-256 $(sha256_of "$PKG"))"
  else
    PKG="$CACHE_DIR/$(basename "$GUFI_PKG_URL")"
    fetch "$GUFI_PKG_URL" "$PKG" "$GUFI_PKG_SHA256"
  fi
  python3 "$HERE/pkg_extract.py" "$PKG" "$G" --prefix usr/local --only bin lib
  # Libraries nothing here loads, whose own dependencies (OpenSSL, cJSON) the
  # package does not carry; and links whose targets it does not ship.
  rm -rf "$G/lib/pkgconfig" "$G"/lib/libllama-common* "$G/lib/libpresidio_plugin.dylib"
  for l in "$G"/lib/*; do
    if [ -L "$l" ] && [ ! -e "$l" ]; then rm -f "$l"; fi
  done
  bottle() {
    local name="$1" version="$2" sum="$3"
    fetch "https://ghcr.io/v2/homebrew/core/$name/blobs/sha256:$sum" \
      "$CACHE_DIR/$name-$version.arm64_sequoia.bottle.tar.gz" "$sum" -H "Authorization: Bearer QQ=="
    tar -xzf "$CACHE_DIR/$name-$version.arm64_sequoia.bottle.tar.gz" -C "$WORK"
  }
  bottle libomp "$LIBOMP_VERSION" "$LIBOMP_SHA256"
  bottle pcre2 "$PCRE2_VERSION" "$PCRE2_SHA256"
  mkdir -p "$G/licenses"
  cp "$WORK/libomp/$LIBOMP_VERSION/lib/libomp.dylib" "$WORK/pcre2/$PCRE2_VERSION/lib/libpcre2-8.0.dylib" "$G/lib/"
  chmod 755 "$G/lib/libomp.dylib" "$G/lib/libpcre2-8.0.dylib"
  cp "$WORK/libomp/$LIBOMP_VERSION/LICENSE.TXT" "$G/licenses/libomp-LICENSE.txt"
  cp "$WORK/pcre2/$PCRE2_VERSION/LICENCE.md" "$G/licenses/pcre2-LICENCE.md"
  fetch "https://raw.githubusercontent.com/mar-file-system/GUFI/$GUFI_COMMIT/LICENSE.txt" \
    "$CACHE_DIR/GUFI-LICENSE-$GUFI_COMMIT.txt" "$GUFI_LICENSE_SHA256"
  cp "$CACHE_DIR/GUFI-LICENSE-$GUFI_COMMIT.txt" "$G/licenses/GUFI-LICENSE.txt"
  CHANGED="$(python3 "$HERE/macho_relink.py" "$G" | wc -l)"
  echo "  relinked $((CHANGED)) files to load their libraries from the app (macho_relink.py)"
  cat >"$G/README.txt" <<README
GUFI, the Grand Unified File Index (Los Alamos National Laboratory)
https://github.com/mar-file-system/GUFI

These files come from GUFI's macOS package,
$GUFI_PKG_URL
(SHA-256 $GUFI_PKG_SHA256), and are modified: the ACTIVATE Studio build
(deploy/mac/macho_relink.py) rewrote the library references in bin/ and
lib/ so that the tools load their libraries from lib/ here, and recomputed
their ad hoc code signatures. libllama-common and libpresidio_plugin, which
nothing here loads, are left out. lib/libomp.dylib (LLVM OpenMP
$LIBOMP_VERSION) and lib/libpcre2-8.0.dylib (PCRE2 $PCRE2_VERSION) come from
Homebrew's bottles, since GUFI's package links them from a Homebrew
install. The licenses are in licenses/.
README
fi
if command -v codesign >/dev/null 2>&1; then
  for f in "$G"/bin/* "$G"/lib/*.dylib; do
    if [ -f "$f" ] && [ ! -L "$f" ] && file -b "$f" | grep -q Mach-O; then codesign --verify "$f"; fi
  done
fi

echo "Embedding model..."
if [ -f "$PROJECT_ROOT/index/models/minilm384.gguf" ]; then
  cp "$PROJECT_ROOT/index/models/minilm384.gguf" "$RES/models/minilm384.gguf"
  [ "$(sha256_of "$RES/models/minilm384.gguf")" = "$MODEL_SHA256" ] || \
    echo "  note: index/models/minilm384.gguf differs from the pinned model; using it as is"
else
  fetch "https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/$MODEL_COMMIT/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf" \
    "$CACHE_DIR/minilm384-$MODEL_COMMIT.gguf" "$MODEL_SHA256"
  cp "$CACHE_DIR/minilm384-$MODEL_COMMIT.gguf" "$RES/models/minilm384.gguf"
fi

echo "Launcher, icon, Info.plist..."
cp "$HERE/launcher.sh" "$RES/launcher.sh"
cp "$HERE/studio-run.sh" "$RES/studio-run.sh"
# The public key updates are verified with; sign_release.sh signs them.
cp "$HERE/update-signing.pub" "$RES/update-signing.pub"
# On a Mac the app's executable is its own window (shell/StudioShell.swift),
# which runs the launcher and shows the Studio. Built elsewhere, it is the
# launcher itself, which opens the Studio in a browser's app window.
if [ "$(uname -s)" = Darwin ] && command -v swiftc >/dev/null 2>&1 && [ "${NATIVE_WINDOW:-1}" = 1 ]; then
  swiftc -O -target arm64-apple-macos13 -o "$APP/Contents/MacOS/$APP_NAME" "$HERE/shell/StudioShell.swift"
  echo "  native window"
else
  cp "$HERE/launcher.sh" "$APP/Contents/MacOS/$APP_NAME"
  echo "  browser window (the native one is compiled only on a Mac)"
fi
chmod 755 "$APP/Contents/MacOS/$APP_NAME" "$RES/launcher.sh" "$RES/studio-run.sh"
# The app's icon, from the 1024-pixel image on Apple's icon grid
# (AppIcon.svg is its source). On a Mac, every size the .icns holds is made
# with sips and iconutil; elsewhere the one 1024-pixel PNG goes in as is,
# which macOS scales (it reads PNG entries).
ICON=
ICON_PNG="$HERE/AppIcon-1024.png"
if command -v iconutil >/dev/null 2>&1 && command -v sips >/dev/null 2>&1; then
  mkdir -p "$WORK/AppIcon.iconset"
  for s in 16 32 128 256 512; do
    sips -z "$s" "$s" "$ICON_PNG" --out "$WORK/AppIcon.iconset/icon_${s}x${s}.png" >/dev/null
    sips -z "$((s * 2))" "$((s * 2))" "$ICON_PNG" --out "$WORK/AppIcon.iconset/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$WORK/AppIcon.iconset" -o "$RES/AppIcon.icns" && ICON=AppIcon
elif python3 - "$ICON_PNG" "$RES/AppIcon.icns" <<'PY'
import struct, sys
png = open(sys.argv[1], 'rb').read()
if png[:8] != b'\x89PNG\r\n\x1a\n' or struct.unpack('>II', png[16:24]) != (1024, 1024):
    sys.exit(1)
entry = b'ic10' + struct.pack('>I', 8 + len(png)) + png
open(sys.argv[2], 'wb').write(b'icns' + struct.pack('>I', 8 + len(entry)) + entry)
PY
then ICON=AppIcon; fi
[ -n "$ICON" ] || echo "  no app icon could be made; the app uses the generic icon"

NEEDS="$(python3 "$HERE/macho_check.py" --print-minos "$RES/node/bin/node" "$RES"/python/bin/python3.* \
  "$RES"/python/lib/*.dylib "$RES"/python/lib/python3.*/lib-dynload/*.so "$G"/bin/* "$G"/lib/*.dylib)"
MIN_MACOS="${MIN_MACOS:-$NEEDS}"
APP_NAME="$APP_NAME" BUNDLE_ID="$BUNDLE_ID" SHORT_VERSION="$SHORT_VERSION" BUNDLE_VERSION="$BUNDLE_VERSION" \
  MIN_MACOS="$MIN_MACOS" BUILD="$VERSION ($COMMIT, $BUILT_AT)" ICON="$ICON" \
  python3 - "$APP/Contents/Info.plist" <<'PY'
import os, plistlib, sys
e = os.environ
info = {
    'CFBundleDevelopmentRegion': 'en',
    'CFBundleDisplayName': e['APP_NAME'],
    'CFBundleExecutable': e['APP_NAME'],
    'CFBundleIdentifier': e['BUNDLE_ID'],
    'CFBundleInfoDictionaryVersion': '6.0',
    'CFBundleName': e['APP_NAME'],
    'CFBundlePackageType': 'APPL',
    'CFBundleShortVersionString': e['SHORT_VERSION'],
    'CFBundleVersion': e['BUNDLE_VERSION'],
    'LSApplicationCategoryType': 'public.app-category.productivity',
    'LSArchitecturePriority': ['arm64'],
    'LSMinimumSystemVersion': e['MIN_MACOS'],
    'NSHighResolutionCapable': True,
    # The window loads the Studio from its own server on 127.0.0.1.
    'NSAppTransportSecurity': {'NSAllowsLocalNetworking': True},
    'NSMicrophoneUsageDescription': 'Voice input in the Studio chat.',
    'ActivateStudioBuild': e['BUILD'],
}
if e['ICON']:
    info['CFBundleIconFile'] = e['ICON']
with open(sys.argv[1], 'wb') as fh:
    plistlib.dump(info, fh)
PY
printf 'APPL????' >"$APP/Contents/PkgInfo"
echo "  LSMinimumSystemVersion $MIN_MACOS (bundled binaries need $NEEDS)"

echo "Checking the app..."
"$HERE/check_app.sh" "$APP"

echo "Packing..."
cd "$OUT_DIR"
rm -f "$ASSET.tar.gz" "$ASSET.tar.gz.sha256"
if [ "$(uname -s)" = Darwin ]; then
  COPYFILE_DISABLE=1 tar --no-mac-metadata -czf "$ASSET.tar.gz" "$APP_NAME.app"
else
  tar -czf "$ASSET.tar.gz" "$APP_NAME.app"
fi
echo "$(sha256_of "$ASSET.tar.gz")  $ASSET.tar.gz" >"$ASSET.tar.gz.sha256"
cp "$HERE/install.sh" "$OUT_DIR/install.sh"
chmod 755 "$OUT_DIR/install.sh"

if command -v hdiutil >/dev/null 2>&1; then
  # hdiutil fails now and then on CI runners ("Resource busy", mostly on
  # detach while the system still holds the new volume): each call is tried
  # a few times, a detach is forced last, and the message shows if it gives up.
  hdi() {
    local n out
    for n in 1 2 3 4 5; do
      if out="$(hdiutil "$@" 2>&1)"; then return 0; fi
      sleep "$((n * 2))"
    done
    if [ "$1" = detach ] && out="$(hdiutil detach -force "${@: -1}" 2>&1)"; then return 0; fi
    echo "build_app: hdiutil $1 failed: $out" >&2
    return 1
  }
  echo "Disk image..."
  rm -rf "$WORK/dmg" "$ASSET.dmg"
  mkdir -p "$WORK/dmg"
  ditto "$APP" "$WORK/dmg/$APP_NAME.app"
  ln -s /Applications "$WORK/dmg/Applications"
  if [ -n "$ICON" ]; then
    # The volume's own icon, which Finder shows in the window's title bar
    # and the sidebar: .VolumeIcon.icns at its root, used once the root has
    # the custom-icon Finder flag. The flag is set on the mounted volume, so
    # the image is made writable first and compressed after.
    cp "$RES/$ICON.icns" "$WORK/dmg/.VolumeIcon.icns"
    hdi create -volname "$APP_NAME" -srcfolder "$WORK/dmg" -ov -format UDRW "$WORK/rw.dmg"
    mkdir -p "$WORK/mnt"
    hdi attach -nobrowse -noautoopen -mountpoint "$WORK/mnt" "$WORK/rw.dmg"
    # Finder info: 32 bytes, with kHasCustomIcon (0x0400) in the flags at byte 8.
    xattr -wx com.apple.FinderInfo 0000000000000000040000000000000000000000000000000000000000000000 "$WORK/mnt"
    hdi detach "$WORK/mnt"
    hdi convert "$WORK/rw.dmg" -format UDZO -ov -o "$ASSET.dmg"
    rm -f "$WORK/rw.dmg"
    # Checked on the finished image, read only.
    hdi attach -nobrowse -noautoopen -readonly -mountpoint "$WORK/mnt" "$ASSET.dmg"
    flags="$(xattr -px com.apple.FinderInfo "$WORK/mnt" 2>/dev/null | tr -d ' \n' | cut -c17-20 || true)"
    have_icon=0; [ -f "$WORK/mnt/.VolumeIcon.icns" ] && have_icon=1
    hdi detach "$WORK/mnt"
    if [ "$have_icon" != 1 ] || [ "$((0x${flags:-0} & 0x0400))" = 0 ]; then
      echo "build_app: the disk image's volume icon did not take (icon file $have_icon, Finder flags ${flags:-none})." >&2
      exit 1
    fi
  else
    hdi create -volname "$APP_NAME" -srcfolder "$WORK/dmg" -ov -format UDZO "$ASSET.dmg"
  fi
fi

echo
echo "App:     $APP ($(du -sh "$APP" | cut -f1))"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$ASSET.tar.gz" | cut -f1)), with .sha256 and install.sh"
[ -f "$ASSET.dmg" ] && echo "Image:   $OUT_DIR/$ASSET.dmg ($(du -h "$ASSET.dmg" | cut -f1))"
exit 0
