#!/usr/bin/env bash
# Build "ACTIVATE Studio.app" for macOS on Apple silicon from this checkout.
#
# The app carries everything it runs: Node for macOS arm64, the built server
# and web app with production node_modules, GUFI (from GUFI's own macOS
# package, or built for an older macOS by deploy/mac/build_gufi.sh; either
# way loading its libraries from inside the app), tesseract for OCR (built
# by deploy/mac/build_ocr.sh), a standalone Python for the indexer (with
# pypdf, xlrd and olefile for documents, and onnxruntime, tokenizers and
# numpy for the ONNX embedding model), and the embedding models. It builds
# on macOS or Linux; the .dmg is made only on macOS, where hdiutil exists,
# and OCR is built only on a Mac (or given as OCR_TARBALL).
#
# Usage: deploy/mac/build_app.sh
#   OUT_DIR     where the outputs go (default deploy/mac/out)
#   CACHE_DIR   downloads, kept between builds (default deploy/mac/.cache)
#   GUFI_TARBALL  a gufi-macos-arm64.tar.gz from build_gufi.sh, used instead
#               of GUFI's package when set
#   GUFI_PKG    a GUFI macOS .pkg on disk to use instead of the pinned download
#   OCR_TARBALL an ocr-macos-arm64.tar.gz from build_ocr.sh; without it, a
#               Mac runs build_ocr.sh (its archive is kept in CACHE_DIR for
#               the next build), and elsewhere the app has no OCR
#   SKIP_BUILD  1 to use the existing server/dist and web/dist
#   MIN_MACOS   LSMinimumSystemVersion; by default the newest macOS that any
#               bundled binary declares it needs
#   STUDIO_SERVER  which server the app runs (docs/UPDATES.md, "The Go version"):
#               node (default), go (the Go server in Resources/app/bin with Node
#               and Python behind it), or go-only (the Go server alone: no Node,
#               no Python, no node_modules, no GGUF model; the ONNX runtime
#               library in Resources/app/lib; node/bin/node and
#               app/server/dist/main.js are stand-ins that older updaters'
#               checks look for, marked by node/PLACEHOLDER). Needs Go.
#               Both Go builds carry the ONNX runtime library in app/lib
#               (beside app/bin/studio-server, where its embedder looks) and
#               the STEP preview's modules in app/occt, with their licenses
#               in Resources/licenses.
#   OCCT_DIR    a folder from server-go/scripts/occt/build.sh (the two
#               modules and their licenses), used instead of building them
#   MAC_SHELL   swift or wails: the window. Default wails for go-only, else
#               swift. wails is desktop/wails built for macOS (Go with cgo, on a
#               Mac), with the Swift window's features (docs/MACOS.md)
#
# Outputs in OUT_DIR: ACTIVATE Studio.app; activate-studio-macos-arm64.tar.gz
# with its .sha256 and install.sh (the release assets install.sh fetches);
# activate-studio-macos-arm64.dmg on macOS (deploy/mac/make_dmg.sh).
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

# Pinned inputs shared with the other platforms (the versions of Node and
# Python, the model, pypdf) are in deploy/common/pins.sh; every download is
# checked against its SHA-256 on each build, so a corrupt or replaced cache
# entry is caught too.
NAME=build_app
. "$HERE/../common/lib.sh"
. "$HERE/../common/pins.sh"
NODE_SHA256=751fdf7439f115d87ee2a8f3f18c065b6151852068e3e666ac60ac2996f75ac9
PY_SHA256=9866e11cf2ae9caa1e671383786f248859a52dd8bfb702fb4c0dc2c65f1d36de
GUFI_PKG_URL=https://github.com/mar-file-system/GUFI/releases/download/0.8.0/gufi-0.8.0.macOS.arm64.pkg
GUFI_PKG_SHA256=b45cdaf8d7839faf112196555ecfd59c383316985ed2d136563bb29d53bfd75e
GUFI_LICENSE_SHA256=9e40647012a77d1fdcac90a9b7241843900cb589631b5c7f7cbad793a91828aa
# GUFI's package links libomp and pcre2 from a Homebrew install it does not
# carry; the app takes them from Homebrew's macOS 15 bottles.
LIBOMP_VERSION=23.1.4
LIBOMP_SHA256=af8425bbe39630ec9b2022b67449ac737e0ed6d48e98a6309f841295d3c6f0f2
PCRE2_VERSION=10.49
PCRE2_SHA256=0ec47d18c50f5770db2b3f07b5b85644284cc6ee8e07e23b11d2727e95932b77

SERVER="${STUDIO_SERVER:-node}"
case "$SERVER" in node | go | go-only) ;; *) echo "build_app: STUDIO_SERVER is node, go or go-only" >&2; exit 1 ;; esac
MAC_SHELL="${MAC_SHELL:-$([ "$SERVER" = go-only ] && echo wails || echo swift)}"
case "$MAC_SHELL" in swift | wails) ;; *) echo "build_app: MAC_SHELL is swift or wails" >&2; exit 1 ;; esac
# The ONNX runtime's own release for macOS arm64, for the Go server's embedder.
ORT_VERSION=1.31.0
ORT_SHA256=1031b36dd3bdaa8d976a6130d9cf2a05e9be320b93ae804f8427b91dc3432afc
TOOLS="python3 curl tar git pnpm"
[ "$SERVER" = node ] && [ "$MAC_SHELL" = swift ] || TOOLS="$TOOLS go"
for tool in $TOOLS; do
  command -v "$tool" >/dev/null 2>&1 || { echo "build_app: $tool is required" >&2; exit 1; }
done

mkdir -p "$OUT_DIR" "$CACHE_DIR"
WORK="$(mktemp -d "$OUT_DIR/.work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

DESCRIBE="$(git -C "$PROJECT_ROOT" describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
case "$DESCRIBE" in v*) VERSION="$DESCRIBE" ;; *) VERSION=dev ;; esac
SHORT_VERSION="$(printf '%s' "$VERSION" | sed -n 's/^v\([0-9][0-9.]*[0-9]\).*/\1/p')"
SHORT_VERSION="${SHORT_VERSION:-0.0}"
DISTANCE="$(printf '%s' "$VERSION" | sed -n 's/^v[0-9.]*-\([0-9][0-9]*\)-g.*/\1/p')"
BUNDLE_VERSION="$SHORT_VERSION${DISTANCE:+.$DISTANCE}"
echo "Building $APP_NAME.app $VERSION ($COMMIT), server: $SERVER, window: $MAC_SHELL"

if [ "${SKIP_BUILD:-}" != 1 ]; then
  echo "Building web and server..."
  (cd "$PROJECT_ROOT" && pnpm build >/dev/null)
fi
for f in server/dist/main.js web/dist/index.html; do
  [ -f "$PROJECT_ROOT/$f" ] || { echo "build_app: $f is missing; run pnpm build" >&2; exit 1; }
done

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$RES/app/server" "$RES/app/web" "$RES/app/deploy" "$RES/node/bin" "$RES/models"

if [ "$SERVER" != node ]; then
  echo "The Go server (studio-server, semantic-map) for macOS arm64..."
  mkdir -p "$RES/app/bin"
  (cd "$PROJECT_ROOT/server-go" && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" \
    -o "$RES/app/bin/" ./cmd/studio-server ./cmd/semantic-map)
  # The helper a library's build on another system runs, for each system
  # it is sent to: Linux x86_64 and aarch64, macOS arm64 (internal/userlibs);
  # about 25 MB each.
  "$PROJECT_ROOT/server-go/scripts/build_remote.sh" "$RES/app/bin/remote"
  # The STEP preview's modules (occt-import-js, Open CASCADE, LGPL 2.1):
  # separate files beside the server, not built into it, so they stay
  # replaceable. OCCT_DIR names a folder with them already built
  # (server-go/scripts/occt/build.sh); else they are built here.
  echo "The STEP preview modules (occt-import-js)..."
  mkdir -p "$RES/app/occt" "$RES/licenses"
  # build.sh fetches occt-import-js and binaryen at the versions and npm
  # integrity hashes it records, and stops on a mismatch.
  occt="${OCCT_DIR:-$WORK/occt}"
  [ -n "${OCCT_DIR:-}" ] || "$PROJECT_ROOT/server-go/scripts/occt/build.sh" "$occt" >/dev/null
  cp "$occt/occt-import-js.wasm" "$occt/occt-import-js.noeh.wasm" "$RES/app/occt/"
  cp "$occt/license.occt-import-js.txt" "$RES/licenses/occt-import-js-LICENSE.txt"
  cp "$occt/license.occt.txt" "$RES/licenses/occt-LICENSE.txt"
fi
if [ "$SERVER" = go-only ]; then
  # What the updaters of v1.180 to v1.188 check in a bundle before they
  # install it (Resources/node/bin/node and app/server/dist/main.js are
  # files): stand-ins, so that those installs take the Go version as an
  # ordinary update. studio-run.sh reads node/PLACEHOLDER and runs the Go
  # server alone.
  mkdir -p "$RES/app/server/dist"
  printf '%s\n' "This app runs the Go server alone (app/bin/studio-server). node/bin/node and" \
    "app/server/dist/main.js are stand-ins for the checks of older updaters (docs/UPDATES.md)." >"$RES/node/PLACEHOLDER"
  printf '%s\n' '#!/bin/sh' 'echo "ACTIVATE Studio runs without Node; this file is a stand-in for older updaters" >&2' 'exit 1' >"$RES/node/bin/node"
  chmod 755 "$RES/node/bin/node"
  printf '%s\n' '// A stand-in for the checks of older updaters: this app runs the Go server' \
    '// (app/bin/studio-server) and carries no Node server.' \
    "throw new Error('ACTIVATE Studio runs the Go server; there is no Node server here')" >"$RES/app/server/dist/main.js"
else
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
fi

echo "Staging web, indexer, docs, change log, starters..."
cp -R "$PROJECT_ROOT/web/dist" "$RES/app/web/dist"
if [ "$SERVER" = go-only ]; then
  # The pins of what a build on another system is sent (internal/userlibs).
  mkdir -p "$RES/app/indexer" && cp "$PROJECT_ROOT/indexer/embed_deps.json" "$RES/app/indexer/"
else
  cp -R "$PROJECT_ROOT/indexer" "$RES/app/indexer"
fi
cp -R "$PROJECT_ROOT/docs" "$RES/app/docs"
# What's new and the notice after an update read it.
cp "$PROJECT_ROOT/CHANGELOG.md" "$RES/app/CHANGELOG.md"
cp -R "$PROJECT_ROOT/extensions-starter" "$RES/app/extensions-starter"
cp "$PROJECT_ROOT/deploy/gufi-index.yaml" "$RES/app/deploy/"
cp "$PROJECT_ROOT/package.json" "$RES/app/package.json"
if [ -d "$RES/app/indexer" ]; then
  find "$RES/app/indexer" -name __pycache__ -type d -prune -exec rm -rf {} +
  # A clone's own ONNX packages (fetch_embed.py --deps) are for its platform,
  # not the app's, which carries its own in its Python.
  rm -rf "$RES/app/indexer/.deps"
fi
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '{\n "version": "%s",\n "commit": "%s",\n "builtAt": "%s"\n}\n' \
  "$VERSION" "$COMMIT" "$BUILT_AT" >"$RES/app/build-info.json"

if [ "$SERVER" != go-only ]; then
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
# The indexer uses the standard library and pypdf: no Tk, IDLE, pip or headers.
(cd "$RES/python" && rm -rf include share lib/itcl* lib/tcl* lib/tk* lib/thread* lib/libtcl* \
  lib/python3.*/idlelib lib/python3.*/tkinter lib/python3.*/turtledemo lib/python3.*/ensurepip \
  lib/python3.*/lib2to3 lib/python3.*/site-packages/pip* lib/python3.*/lib-dynload/_tkinter* \
  bin/idle3* bin/pip* bin/2to3* bin/pydoc3* bin/python3*-config)
echo "pypdf $PYPDF_VERSION into the app's Python..."
fetch "$PYPDF_URL" "$CACHE_DIR/pypdf-$PYPDF_VERSION-py3-none-any.whl" "$PYPDF_SHA256"
# A wheel is a zip laid out as site-packages wants it; its license is in
# the .dist-info it carries.
python3 - "$CACHE_DIR/pypdf-$PYPDF_VERSION-py3-none-any.whl" "$RES"/python/lib/python3.*/site-packages <<'PY'
import sys, zipfile
zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])
PY
# The ONNX embedding backend (indexer/embed_onnx.py): its packages, as the
# macOS arm64 wheels for this Python pinned in indexer/embed_deps.json, and
# the int8 all-MiniLM-L6-v2 model beside the GGUF one; with them xlrd and
# olefile, which read .xls and .ppt files. Each is checked against its
# SHA-256.
PY_CP="cp$(echo "$PY_VERSION" | cut -d. -f1,2 | tr -d .)"
echo "onnxruntime, tokenizers and numpy ($PY_CP), xlrd and olefile, and the ONNX embedding model..."
python3 "$PROJECT_ROOT/indexer/fetch_embed.py" --platform "macos-arm64-$PY_CP" --cache "$CACHE_DIR/embed" \
  --deps "$(echo "$RES"/python/lib/python3.*/site-packages)" --model "$RES/models/minilm-l6"
# The document readers again as wheels, which the app sends to a remote
# system for a build there (server/src/remoteEmbed.ts).
python3 "$PROJECT_ROOT/indexer/fetch_embed.py" --download-readers --cache "$RES/app/indexer/wheels"
else
  echo "The ONNX embedding model..."
  python3 "$PROJECT_ROOT/indexer/fetch_embed.py" --cache "$CACHE_DIR/embed" --model "$RES/models/minilm-l6"
fi
if [ "$SERVER" != node ]; then
  echo "The ONNX runtime $ORT_VERSION library, for the Go server's embedder..."
  ORT_DIST="onnxruntime-osx-arm64-$ORT_VERSION"
  fetch "https://github.com/microsoft/onnxruntime/releases/download/v$ORT_VERSION/$ORT_DIST.tgz" "$CACHE_DIR/$ORT_DIST.tgz" "$ORT_SHA256"
  # The macOS archive's paths start with ./ (the Linux one's do not).
  tar -xzf "$CACHE_DIR/$ORT_DIST.tgz" -C "$WORK" "./$ORT_DIST/lib/libonnxruntime.$ORT_VERSION.dylib" "./$ORT_DIST/LICENSE" "./$ORT_DIST/ThirdPartyNotices.txt"
  # In app/lib: the embedder looks beside studio-server and in ../lib.
  mkdir -p "$RES/app/lib" "$RES/licenses"
  cp "$WORK/$ORT_DIST/lib/libonnxruntime.$ORT_VERSION.dylib" "$RES/app/lib/libonnxruntime.dylib"
  cp "$WORK/$ORT_DIST/LICENSE" "$RES/licenses/onnxruntime-LICENSE.txt"
  cp "$WORK/$ORT_DIST/ThirdPartyNotices.txt" "$RES/licenses/onnxruntime-ThirdPartyNotices.txt"
fi

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
  python3 "$HERE/pkg_extract.py" "$PKG" "$G" --prefix Applications --only bin lib --drop-absolute-links
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

# OCR: tesseract and leptonica with the English model, loading their
# library from the tree and signed ad hoc, as build_ocr.sh made and checked
# them on the Mac it ran on.
O="$RES/ocr"
if [ -z "${OCR_TARBALL:-}" ] && [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; then
  # Built once per version of build_ocr.sh and deployment target, and kept
  # in the cache for the builds after.
  OCR_CACHE="$CACHE_DIR/ocr-$(sha256_of "$HERE/build_ocr.sh" | cut -c1-16)-${MACOSX_DEPLOYMENT_TARGET:-14.0}"
  if [ ! -f "$OCR_CACHE/ocr-macos-arm64.tar.gz" ]; then
    echo "OCR: building tesseract (deploy/mac/build_ocr.sh)..."
    OUT_DIR="$OCR_CACHE" CACHE_DIR="$CACHE_DIR" "$HERE/build_ocr.sh"
  fi
  OCR_TARBALL="$OCR_CACHE/ocr-macos-arm64.tar.gz"
fi
if [ -n "${OCR_TARBALL:-}" ]; then
  echo "OCR from $OCR_TARBALL (deploy/mac/build_ocr.sh)..."
  [ -f "$OCR_TARBALL" ] || { echo "build_app: OCR_TARBALL $OCR_TARBALL does not exist" >&2; exit 1; }
  OCR_SUM="$(sha256_of "$OCR_TARBALL")"
  if [ -f "$OCR_TARBALL.sha256" ] && [ "$(awk '{print $1; exit}' "$OCR_TARBALL.sha256")" != "$OCR_SUM" ]; then
    echo "build_app: $OCR_TARBALL has SHA-256 $OCR_SUM, not the one its .sha256 names" >&2
    exit 1
  fi
  mkdir -p "$WORK/ocr-tarball"
  tar -xzf "$OCR_TARBALL" -C "$WORK/ocr-tarball"
  for f in manifest.json bin/tesseract lib/libleptonica.6.dylib share/tessdata/eng.traineddata \
    share/tessdata/configs/tsv licenses/tesseract-LICENSE.txt; do
    [ -e "$WORK/ocr-tarball/ocr/$f" ] || {
      echo "build_app: $OCR_TARBALL has no ocr/$f; it is not an archive from build_ocr.sh" >&2; exit 1; }
  done
  mv "$WORK/ocr-tarball/ocr" "$O"
  python3 - "$O/manifest.json" "$OCR_SUM" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
print(f"  tesseract {m['tesseract']} with leptonica {m['components']['leptonica']} for macOS "
      f"{m['deploymentTarget']}, built {m['builtAt']}; SHA-256 {sys.argv[2]}")
PY
  if command -v codesign >/dev/null 2>&1; then
    for f in "$O"/bin/* "$O"/lib/*.dylib; do codesign --verify "$f"; done
  fi
else
  echo "OCR: tesseract is built on a Mac (build_ocr.sh); without OCR_TARBALL this app has none"
fi

if [ "$SERVER" = go-only ]; then
  :
elif [ -f "$PROJECT_ROOT/index/models/minilm384.gguf" ]; then
  echo "GGUF embedding model..."
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
if [ "$(uname -s)" = Darwin ] && [ "$MAC_SHELL" = wails ] && [ "${NATIVE_WINDOW:-1}" = 1 ]; then
  # The Go window (desktop/wails, launcher_darwin.go): it runs the launcher
  # as the Swift one does.
  (cd "$PROJECT_ROOT/desktop/wails" && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 MACOSX_DEPLOYMENT_TARGET=14.0 \
    CGO_LDFLAGS="-mmacosx-version-min=14.0" go build -trimpath -tags desktop,production \
    -ldflags "-s -w -X main.version=$VERSION" -o "$APP/Contents/MacOS/$APP_NAME" .)
  echo "  native window (Wails)"
elif [ "$(uname -s)" = Darwin ] && command -v swiftc >/dev/null 2>&1 && [ "${NATIVE_WINDOW:-1}" = 1 ]; then
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

# The copyright line the About window and Finder's Get Info show, as the
# NOTICE file states it.
COPYRIGHT="$(sed -n 's/^\(Copyright .*\)$/\1/p' "$PROJECT_ROOT/NOTICE" | head -n 1)"

# The packages' native libraries, read NUL-separated: the app's path has a
# space in it, and an unquoted list would split each path into pieces that
# macho_check.py then skips as missing.
SITE_NATIVE=()
[ "$SERVER" = go-only ] || while IFS= read -r -d '' f; do SITE_NATIVE+=("$f"); done < <(
  find "$RES"/python/lib/python3.*/site-packages -type f \( -name '*.so' -o -name '*.dylib' \) -print0)
echo "  minimum macOS: ${#SITE_NATIVE[@]} native libraries in the Python packages"
MACHO_ALL=("$G"/bin/* "$G"/lib/*.dylib "$O"/bin/* "$O"/lib/*.dylib)
if [ "$SERVER" != go-only ]; then
  MACHO_ALL+=("$RES/node/bin/node" "$RES"/python/bin/python3.* "$RES"/python/lib/*.dylib "$RES"/python/lib/python3.*/lib-dynload/*.so)
fi
[ "$SERVER" = node ] || MACHO_ALL+=("$RES"/app/bin/* "$RES"/app/lib/*.dylib)
NEEDS="$(python3 "$HERE/macho_check.py" --print-minos "${MACHO_ALL[@]}" ${SITE_NATIVE[@]+"${SITE_NATIVE[@]}"})"
MIN_MACOS="${MIN_MACOS:-$NEEDS}"
APP_NAME="$APP_NAME" BUNDLE_ID="$BUNDLE_ID" SHORT_VERSION="$SHORT_VERSION" BUNDLE_VERSION="$BUNDLE_VERSION" \
  MIN_MACOS="$MIN_MACOS" BUILD="$VERSION ($COMMIT, $BUILT_AT)" ICON="$ICON" COPYRIGHT="$COPYRIGHT" \
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
if e['COPYRIGHT']:
    info['NSHumanReadableCopyright'] = e['COPYRIGHT']
with open(sys.argv[1], 'wb') as fh:
    plistlib.dump(info, fh)
PY
printf 'APPL????' >"$APP/Contents/PkgInfo"
# Which server the app runs: studio-run.sh reads "server" (docs/UPDATES.md).
if [ "$SERVER" != node ]; then
  printf '{\n "version": "%s",\n "server": "go",\n "window": "%s",\n "onnxruntime": "%s"\n}\n' \
    "$VERSION" "$MAC_SHELL" "$ORT_VERSION" >"$RES/manifest.json"
fi
echo "  LSMinimumSystemVersion $MIN_MACOS (bundled binaries need $NEEDS)"

# Links that lead nowhere, such as the one pnpm leaves in .pnpm/node_modules
# for the workspace's own server package (it points at the source checkout
# the app was built from): nothing follows them, and a signature check of
# the bundle fails on them.
find "$APP" -type l | while IFS= read -r l; do [ -e "$l" ] || rm -f "$l"; done

# The bundle's own signature, ad hoc: it seals the Info.plist and every file
# in the bundle. A downloaded app whose bundle has none is reported by
# Gatekeeper as damaged, with no way to open it. Last, since any change to
# the bundle after it breaks the seal. A release with the Developer ID
# certificate replaces it (sign_app.sh, then notarize.sh).
if command -v codesign >/dev/null 2>&1; then
  echo "Signing the app (ad hoc)..."
  codesign --force --sign - "$APP"
fi

echo "Checking the app..."
STUDIO_SERVER="$SERVER" "$HERE/check_app.sh" "$APP"

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
  echo "Disk image..."
  "$HERE/make_dmg.sh" "$APP" "$OUT_DIR/$ASSET.dmg"
fi

echo
echo "App:     $APP ($(du -sh "$APP" | cut -f1))"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$ASSET.tar.gz" | cut -f1)), with .sha256 and install.sh"
[ -f "$ASSET.dmg" ] && echo "Image:   $OUT_DIR/$ASSET.dmg ($(du -h "$ASSET.dmg" | cut -f1))"
exit 0
