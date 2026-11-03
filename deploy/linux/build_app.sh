#!/usr/bin/env bash
# Build the Linux x86_64 payload of ACTIVATE Studio from this checkout: the
# same contents as the macOS app's Resources, as a relocatable directory
# tree and an archive of it. It has no window shell and no packaging, which
# come after it; the tree runs from any path (deploy/linux/studio-run.sh at
# its top starts the server).
#
# The tree carries everything it runs: Node for Linux x64 (with the one
# library Node needs that a desktop may lack, libatomic), the built server
# and web app with production node_modules, GUFI built by
# deploy/linux/build_gufi.sh, tesseract built by deploy/linux/build_ocr.sh,
# a standalone Python for the indexer (with pypdf, xlrd and olefile for
# documents, and onnxruntime, tokenizers and numpy for the ONNX embedding
# model), and the embedding models. Build GUFI and tesseract in a Rocky
# Linux 9 container (glibc 2.34) so the tree runs on Ubuntu 22.04 and later
# and on RHEL 9 and later: this script checks that no file in the tree
# needs a newer glibc (elf_check.py).
#
# Usage: deploy/linux/build_app.sh
#   OUT_DIR       where the outputs go (default deploy/linux/out)
#   CACHE_DIR     downloads, kept between builds (default deploy/linux/.cache)
#   GUFI_TARBALL  gufi-linux-x86_64.tar.gz from build_gufi.sh (required)
#   OCR_TARBALL   ocr-linux-x86_64.tar.gz from build_ocr.sh (required)
#   LIBATOMIC     the libatomic.so.1 to bundle with Node (default: the one
#                 gcc reports)
#   SKIP_BUILD    1 to use the existing server/dist and web/dist
#   SHELL_DIR     a folder with activate-studio-wk41 and activate-studio-wk40,
#                 the window shells (desktop/linux/build_shell.sh); when set the
#                 tree also gets bin/, install.sh, uninstall.sh, the menu entry
#                 and icons (desktop/linux/add_desktop.sh)
#
# Outputs in OUT_DIR: activate-studio/ (the tree);
# activate-studio-linux-x86_64.tar.gz with its .sha256 and
# activate-studio-linux-x86_64.manifest.json (version, asset, SHA-256, size,
# the oldest glibc it needs, build time).
# shellcheck disable=SC2034  # settings and pins that the sourced files and the build read
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$HERE/../.." && pwd)"
NAME=build_app
# shellcheck source=../common/lib.sh
. "$HERE/../common/lib.sh"
# shellcheck source=../common/pins.sh
. "$HERE/../common/pins.sh"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
ASSET=activate-studio-linux-x86_64
TOP=activate-studio

# What only this platform pins (the rest is deploy/common/pins.sh).
NODE_SHA256=cb5c9ce9c80d7b8821e3a258543c71b939138cf17c74d5cc44bbe85d6dbc5ad8
PY_SHA256=178d3d83b6e8e99285a65d3fce9c16b87952cc5efdb4d1a0e3ff3be18ff0fc9c
# The oldest targets' libraries: RHEL 9's glibc, and the libstdc++ GCC 11
# ships there.
MAX_GLIBC=2.34
MAX_GLIBCXX=3.4.29
MAX_CXXABI=1.3.13

[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || die "builds on Linux x86_64"
for tool in python3 curl tar git pnpm patchelf; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
[ -n "${GUFI_TARBALL:-}" ] && [ -f "$GUFI_TARBALL" ] || die "GUFI_TARBALL names the archive from build_gufi.sh"
[ -n "${OCR_TARBALL:-}" ] && [ -f "$OCR_TARBALL" ] || die "OCR_TARBALL names the archive from build_ocr.sh"

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
START="$(date +%s)"
WORK="$(mktemp -d "$OUT_DIR/.work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
APP="$OUT_DIR/$TOP"
rm -rf "$APP"

DESCRIBE="$(git -C "$PROJECT_ROOT" describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "$PROJECT_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"
case "$DESCRIBE" in v*) VERSION="$DESCRIBE" ;; *) VERSION=dev ;; esac
echo "Building the Linux payload $VERSION ($COMMIT)"

if [ "${SKIP_BUILD:-}" != 1 ]; then
  echo "Building web and server..."
  (cd "$PROJECT_ROOT" && pnpm build >/dev/null)
fi
for f in server/dist/main.js web/dist/index.html; do
  [ -f "$PROJECT_ROOT/$f" ] || die "$f is missing; run pnpm build"
done

mkdir -p "$APP/app/server" "$APP/app/web" "$APP/app/deploy" "$APP/node/bin" "$APP/node/lib" "$APP/models" "$APP/share"

echo "Staging the server's production dependencies for Linux x64 (pnpm deploy)..."
# --os and --cpu pick the Linux x64 builds of platform packages (the
# document converter's native binary) whatever this host is.
(cd "$PROJECT_ROOT" && pnpm --filter @activate-studio/server --prod deploy --legacy \
  --os linux --cpu x64 "$WORK/server" >/dev/null)
# The deploy records a production-only install for the workspace, which the
# next pnpm command would act on; record the development one again (as
# deploy/make_bundle.sh does).
(cd "$PROJECT_ROOT" && { pnpm install --frozen-lockfile --offline >/dev/null 2>&1 || pnpm install --frozen-lockfile >/dev/null; })
mv "$WORK/server/node_modules" "$APP/app/server/node_modules"
cp "$WORK/server/package.json" "$APP/app/server/package.json"
cp -R "$PROJECT_ROOT/server/dist" "$APP/app/server/dist"

echo "Staging web, indexer, docs, change log, starters..."
cp -R "$PROJECT_ROOT/web/dist" "$APP/app/web/dist"
cp -R "$PROJECT_ROOT/indexer" "$APP/app/indexer"
cp -R "$PROJECT_ROOT/docs" "$APP/app/docs"
# What's new and the notice after an update read it.
cp "$PROJECT_ROOT/CHANGELOG.md" "$APP/app/CHANGELOG.md"
cp -R "$PROJECT_ROOT/extensions-starter" "$APP/app/extensions-starter"
cp "$PROJECT_ROOT/deploy/gufi-index.yaml" "$APP/app/deploy/"
cp "$PROJECT_ROOT/package.json" "$APP/app/package.json"
find "$APP/app/indexer" -name __pycache__ -type d -prune -exec rm -rf {} +
# A clone's own ONNX packages (fetch_embed.py --deps) are for its platform,
# not the payload's, which carries its own in its Python.
rm -rf "$APP/app/indexer/.deps"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf '{\n "version": "%s",\n "commit": "%s",\n "builtAt": "%s"\n}\n' \
  "$VERSION" "$COMMIT" "$BUILT_AT" >"$APP/app/build-info.json"
cp "$PROJECT_ROOT/LICENSE" "$APP/LICENSE"
cp "$PROJECT_ROOT/NOTICE" "$APP/NOTICE"

echo "Node $NODE_VERSION for Linux x64..."
NODE_DIST="node-$NODE_VERSION-linux-x64"
SUMS="$CACHE_DIR/node-$NODE_VERSION-SHASUMS256.txt"
[ -f "$SUMS" ] || curl -fsSL -o "$SUMS" "https://nodejs.org/dist/$NODE_VERSION/SHASUMS256.txt"
LISTED="$(awk -v f="$NODE_DIST.tar.gz" '$2 == f {print $1}' "$SUMS")"
[ "$LISTED" = "$NODE_SHA256" ] || die "SHASUMS256.txt lists $LISTED for $NODE_DIST.tar.gz; pinned is $NODE_SHA256"
fetch "https://nodejs.org/dist/$NODE_VERSION/$NODE_DIST.tar.gz" "$CACHE_DIR/$NODE_DIST.tar.gz" "$NODE_SHA256"
tar -xzf "$CACHE_DIR/$NODE_DIST.tar.gz" -C "$WORK" "$NODE_DIST/bin/node" "$NODE_DIST/LICENSE"
mv "$WORK/$NODE_DIST/bin/node" "$APP/node/bin/node"
mv "$WORK/$NODE_DIST/LICENSE" "$APP/node/LICENSE"
# Node 26 links libatomic, which a desktop install may not have (Rocky 9
# does not carry it unless something pulled it in). It is bundled beside the
# node binary, which finds it through its run path; no LD_LIBRARY_PATH,
# which every program the server starts would inherit.
LIBATOMIC="${LIBATOMIC:-$(gcc -print-file-name=libatomic.so.1 2>/dev/null || true)}"
[ -f "$LIBATOMIC" ] || die "no libatomic.so.1 to bundle with Node (set LIBATOMIC)"
cp -L "$LIBATOMIC" "$APP/node/lib/libatomic.so.1"
patchelf --set-rpath '$ORIGIN/../lib' "$APP/node/bin/node"
# libatomic is GCC's runtime library: GPL with the runtime library exception.
mkdir -p "$APP/node/licenses"
printf '%s\n' "libatomic.so.1 is GCC's atomic operations runtime library, taken from the build machine's GCC:" \
  "$(gcc --version | awk 'NR == 1')" \
  "It is licensed under the GNU GPL version 3 with the GCC Runtime Library Exception," \
  "which allows programs of any license to link and ship it: https://www.gnu.org/licenses/gcc-exception-3.1.html" \
  "Its source is part of GCC: https://gcc.gnu.org/" >"$APP/node/licenses/libatomic-NOTICE.txt"
gcc_licenses "$APP/node/licenses" libatomic

echo "Python $PY_VERSION (python-build-standalone $PY_TAG)..."
PY_FILE="cpython-$PY_VERSION+$PY_TAG-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
fetch "https://github.com/astral-sh/python-build-standalone/releases/download/$PY_TAG/${PY_FILE/+/%2B}" \
  "$CACHE_DIR/$PY_FILE" "$PY_SHA256"
tar -xzf "$CACHE_DIR/$PY_FILE" -C "$APP"
# The indexer uses the standard library and pypdf: no Tk, IDLE, pip or headers,
# and no _crypt (the deprecated crypt module, which needs libcrypt.so.1, a
# compatibility package on RHEL 9).
(cd "$APP/python" && rm -rf include share lib/itcl* lib/tcl* lib/tk* lib/thread* lib/libtcl* lib/libtk* \
  lib/python3.*/idlelib lib/python3.*/tkinter lib/python3.*/turtledemo lib/python3.*/ensurepip \
  lib/python3.*/lib2to3 lib/python3.*/site-packages/pip* lib/python3.*/lib-dynload/_tkinter* lib/python3.*/lib-dynload/_crypt* \
  bin/idle3* bin/pip* bin/2to3* bin/pydoc3* bin/python3*-config)
echo "pypdf $PYPDF_VERSION into the payload's Python..."
fetch "$PYPDF_URL" "$CACHE_DIR/pypdf-$PYPDF_VERSION-py3-none-any.whl" "$PYPDF_SHA256"
# A wheel is a zip laid out as site-packages wants it; its license is in the
# .dist-info it carries.
python3 - "$CACHE_DIR/pypdf-$PYPDF_VERSION-py3-none-any.whl" "$APP"/python/lib/python3.*/site-packages <<'PY'
import sys, zipfile
zipfile.ZipFile(sys.argv[1]).extractall(sys.argv[2])
PY
# The ONNX embedding backend (indexer/embed_onnx.py): its packages, as the
# Linux x86_64 wheels for this Python pinned in indexer/embed_deps.json, and
# the int8 all-MiniLM-L6-v2 model beside the GGUF one; with them xlrd and
# olefile, which read .xls and .ppt files. Each is checked against its
# SHA-256.
PY_CP="cp$(echo "$PY_VERSION" | cut -d. -f1,2 | tr -d .)"
echo "onnxruntime, tokenizers and numpy ($PY_CP), xlrd and olefile, and the ONNX embedding model..."
python3 "$PROJECT_ROOT/indexer/fetch_embed.py" --platform "linux-x86_64-$PY_CP" --cache "$CACHE_DIR/embed" \
  --deps "$(echo "$APP"/python/lib/python3.*/site-packages)" --model "$APP/models/minilm-l6"

echo "GUFI from $GUFI_TARBALL (deploy/linux/build_gufi.sh)..."
GUFI_SUM="$(sha256_of "$GUFI_TARBALL")"
if [ -f "$GUFI_TARBALL.sha256" ] && [ "$(awk '{print $1; exit}' "$GUFI_TARBALL.sha256")" != "$GUFI_SUM" ]; then
  die "$GUFI_TARBALL has SHA-256 $GUFI_SUM, not the one its .sha256 names"
fi
mkdir -p "$WORK/gufi-tarball"
tar -xzf "$GUFI_TARBALL" -C "$WORK/gufi-tarball"
for f in manifest.json bin/gufi_query bin/gufi_dir2index bin/gufi_sqlite3 lib/gufi_vt.so lib/vec0.so lib/lembed0.so \
  lib/libgomp.so.1 lib/libpcre2-8.so.0 licenses/GUFI-LICENSE.txt; do
  [ -e "$WORK/gufi-tarball/gufi/$f" ] || die "$GUFI_TARBALL has no gufi/$f; it is not an archive from build_gufi.sh"
done
mv "$WORK/gufi-tarball/gufi" "$APP/gufi"

echo "OCR from $OCR_TARBALL (deploy/linux/build_ocr.sh)..."
OCR_SUM="$(sha256_of "$OCR_TARBALL")"
if [ -f "$OCR_TARBALL.sha256" ] && [ "$(awk '{print $1; exit}' "$OCR_TARBALL.sha256")" != "$OCR_SUM" ]; then
  die "$OCR_TARBALL has SHA-256 $OCR_SUM, not the one its .sha256 names"
fi
mkdir -p "$WORK/ocr-tarball"
tar -xzf "$OCR_TARBALL" -C "$WORK/ocr-tarball"
for f in manifest.json bin/tesseract lib/libleptonica.so.6 share/tessdata/eng.traineddata \
  share/tessdata/configs/tsv licenses/tesseract-LICENSE.txt; do
  [ -e "$WORK/ocr-tarball/ocr/$f" ] || die "$OCR_TARBALL has no ocr/$f; it is not an archive from build_ocr.sh"
done
mv "$WORK/ocr-tarball/ocr" "$APP/ocr"

echo "Embedding model..."
if [ -f "$PROJECT_ROOT/index/models/minilm384.gguf" ]; then
  cp "$PROJECT_ROOT/index/models/minilm384.gguf" "$APP/models/minilm384.gguf"
  [ "$(sha256_of "$APP/models/minilm384.gguf")" = "$MODEL_SHA256" ] || \
    echo "  note: index/models/minilm384.gguf differs from the pinned model; using it as is"
else
  fetch "https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/$MODEL_COMMIT/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf" \
    "$CACHE_DIR/minilm384-$MODEL_COMMIT.gguf" "$MODEL_SHA256"
  cp "$CACHE_DIR/minilm384-$MODEL_COMMIT.gguf" "$APP/models/minilm384.gguf"
fi

echo "Run loop, update key, icon..."
cp "$HERE/studio-run.sh" "$APP/studio-run.sh"
# The public key updates are verified with (deploy/linux/sign_release.sh signs
# them with the macOS app's key); a per-user install's server reads it from
# here before it installs an update.
cp "$HERE/../mac/update-signing.pub" "$APP/update-signing.pub"
# The icon a launcher entry or a package names, a 1024-pixel PNG.
cp "$HERE/../mac/AppIcon-1024.png" "$APP/share/activate-studio.png"
chmod 755 "$APP/studio-run.sh"
if [ -n "${SHELL_DIR:-}" ]; then
  echo "Window shells, launcher, installer..."
  "$PROJECT_ROOT/desktop/linux/add_desktop.sh" "$APP" "$SHELL_DIR"
fi

# Links that lead nowhere, such as the one pnpm leaves in .pnpm/node_modules
# for the workspace's own server package (it points at the source checkout
# the payload was built from).
find "$APP" -type l | while IFS= read -r l; do [ -e "$l" ] || rm -f "$l"; done

# The tree's own manifest: what it was built from, and what the checks above
# found about the libraries it needs.
echo "Manifest..."
ELF_REPORT="$(python3 -I "$HERE/elf_check.py" --root "$APP" --skip bin/activate-studio-wk --max-glibc "$MAX_GLIBC" --max-glibcxx "$MAX_GLIBCXX" --max-cxxabi "$MAX_CXXABI" "$APP")"
VERSION="$VERSION" COMMIT="$COMMIT" BUILT_AT="$BUILT_AT" NODE_VERSION="$NODE_VERSION" PY_VERSION="$PY_VERSION" PY_TAG="$PY_TAG" \
  GUFI_SUM="$GUFI_SUM" OCR_SUM="$OCR_SUM" ELF_REPORT="$ELF_REPORT" MAX_GLIBC="$MAX_GLIBC" ASSET="$ASSET" \
  python3 - "$APP" <<'PY'
import json, os, re, sys
root = sys.argv[1]
e = os.environ
def load(p):
    return json.load(open(os.path.join(root, p)))
rep = e['ELF_REPORT']
newest = {k: re.search(rf'newest {k}: {k}_([0-9.]+)', rep).group(1) for k in ('GLIBC', 'GLIBCXX', 'CXXABI')}
needs = re.search(r'system libraries needed: (.*)', rep).group(1).split(', ')
gufi, ocr = load('gufi/manifest.json'), load('ocr/manifest.json')
manifest = {
    'name': 'ACTIVATE Studio',
    'platform': 'linux-x86_64',
    'version': e['VERSION'],
    'commit': e['COMMIT'],
    'builtAt': e['BUILT_AT'],
    'minGlibc': newest['GLIBC'],
    'newestSymbolVersions': {'glibc': newest['GLIBC'], 'glibcxx': newest['GLIBCXX'], 'cxxabi': newest['CXXABI']},
    'systemLibraries': needs,
    'components': {
        'node': e['NODE_VERSION'],
        'python': {'version': e['PY_VERSION'], 'source': f"python-build-standalone {e['PY_TAG']}"},
        'gufi': {'version': gufi['gufi']['version'], 'commit': gufi['gufi']['commit'], 'archiveSha256': e['GUFI_SUM'],
                 'components': gufi['components']},
        'tesseract': {'version': ocr['tesseract'], 'archiveSha256': e['OCR_SUM'], 'components': ocr['components']},
        'libatomic': 'bundled with node',
    },
    'entry': 'studio-run.sh',
}
# The window program, when the tree has one: the Studio's updater compares
# this file in the old and the new payload to tell the person to reopen the
# window (the shells are built together, so either one changes with the other).
if os.path.exists(os.path.join(root, 'bin', 'activate-studio-wk41')):
    manifest['shell'] = 'bin/activate-studio-wk41'
    manifest['shells'] = {'wk41': 'bin/activate-studio-wk41', 'wk40': 'bin/activate-studio-wk40'}
with open(os.path.join(root, 'manifest.json'), 'w') as fh:
    json.dump(manifest, fh, indent=1)
    fh.write('\n')
PY

echo "Checking the tree..."
"$HERE/check_app.sh" "$APP" --static

echo "Packing..."
cd "$OUT_DIR"
rm -f "$ASSET.tar.gz" "$ASSET.tar.gz.sha256" "$ASSET.manifest.json"
tar --owner=0 --group=0 --numeric-owner --sort=name -czf "$ASSET.tar.gz" "$TOP"
SUM="$(sha256_of "$ASSET.tar.gz")"
echo "$SUM  $ASSET.tar.gz" >"$ASSET.tar.gz.sha256"
# The archive's manifest, as the macOS release's: version, asset, checksum,
# size, and the oldest system it runs on. Unsigned here: a release signs a
# manifest of its own with deploy/linux/sign_release.sh, which the updater
# verifies.
ASSET_NAME="$ASSET.tar.gz" SUM="$SUM" SIZE="$(wc -c <"$ASSET.tar.gz" | tr -d ' ')" python3 - "$APP/manifest.json" "$ASSET.manifest.json" <<'PY'
import json, os, sys
m = json.load(open(sys.argv[1]))
out = {
    'version': m['version'], 'asset': os.environ['ASSET_NAME'], 'sha256': os.environ['SUM'],
    'size': int(os.environ['SIZE']), 'minGlibc': m['minGlibc'], 'builtAt': m['builtAt'],
}
with open(sys.argv[2], 'w') as fh:
    json.dump(out, fh, indent=1)
    fh.write('\n')
PY

echo
echo "Tree:    $APP ($(du -sh "$APP" | cut -f1))"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$ASSET.tar.gz" | cut -f1)), with .sha256 and $ASSET.manifest.json"
echo "Built in $(( $(date +%s) - START )) s"
