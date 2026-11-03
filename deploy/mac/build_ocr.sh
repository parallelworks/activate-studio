#!/usr/bin/env bash
# Build tesseract, the OCR engine, for the macOS app on Apple silicon, as a
# relocatable tree for deploy/mac/build_app.sh (OCR_TARBALL).
#
# The indexer runs tesseract on pictures (indexer/enrich.py), and downmark
# hands it each scanned PDF page as a PNG on stdin, with tesseract's tsv
# config (server/src/preextract.ts), so with this tree in the app OCR needs
# nothing installed, poppler included. Homebrew's bottles target the newest
# macOS releases and load their libraries from Homebrew's prefix, so
# tesseract and leptonica, its image library, are built here from their
# release sources for MACOSX_DEPLOYMENT_TARGET. The image formats leptonica
# reads (PNG, JPEG, TIFF, WebP and GIF) are built as static libraries into
# it; zlib is the system's. tesseract is built without its training tools,
# its legacy engine (the tessdata_fast models use the LSTM engine only),
# OpenMP (the indexer runs one tesseract per core), libarchive and libcurl.
#
# The tree:
#   bin/tesseract               loads lib/ through its run path,
#                               @executable_path/../lib
#   lib/libleptonica.6.dylib    install name @rpath/libleptonica.6.dylib
#   share/tessdata/             eng.traineddata from tessdata_fast, and
#                               tesseract's configs (tsv among them)
#   licenses/, README.txt, manifest.json
# tesseract finds its data through TESSDATA_PREFIX, which studio-run.sh sets
# to share/tessdata here.
#
# Runs on a Mac with Apple silicon and the Command Line Tools, such as
# GitHub's macos-15 runner, with CMake (from Homebrew when it is missing).
# Nothing the tree loads comes from Homebrew.
#
# Usage: deploy/mac/build_ocr.sh
#   MACOSX_DEPLOYMENT_TARGET  the oldest macOS the tree runs on (default 14.0)
#   OUT_DIR    where the outputs go (default deploy/mac/out)
#   CACHE_DIR  downloads, kept between builds (default deploy/mac/.cache)
#   JOBS       parallel build jobs (default: the number of CPUs)
#
# Outputs in OUT_DIR: ocr-macos-arm64.tar.gz (ocr/ as above), its .sha256,
# and ocr-macos-arm64.manifest.json (the manifest with the archive's name,
# size and SHA-256).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
ASSET=ocr-macos-arm64
TARGET="${MACOSX_DEPLOYMENT_TARGET:-14.0}"

# Pinned sources (deploy/common/pins.sh), checked against their SHA-256 on
# every build.
NAME=build_ocr
. "$HERE/../common/lib.sh"
. "$HERE/../common/pins.sh"
. "$HERE/../common/ocr_build.sh"

# The target can come from a workflow's free-text input, and goes into
# compiler flags.
[[ "$TARGET" =~ ^[0-9]{2}(\.[0-9]{1,2})?$ ]] || die "MACOSX_DEPLOYMENT_TARGET must look like 14.0, not '$TARGET'"
if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then die "runs on a Mac with Apple silicon"; fi
for tool in xcrun codesign otool curl tar make python3; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
if ! command -v cmake >/dev/null 2>&1; then
  command -v brew >/dev/null 2>&1 || die "cmake is required (brew install cmake)"
  HOMEBREW_NO_AUTO_UPDATE=1 brew install cmake
fi

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd -P)"
JOBS="${JOBS:-$(sysctl -n hw.ncpu)}"
WORK="$(mktemp -d "$OUT_DIR/.ocr-work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
DEPS="$WORK/deps"      # the static image libraries and leptonica
STAGE="$WORK/stage"    # tesseract's install
TREE="$WORK/ocr"
LOG="$WORK/build.log"
mkdir -p "$DEPS" "$STAGE" "$TREE/bin" "$TREE/lib" "$TREE/share/tessdata" "$TREE/licenses"

ocr_fetch_sources

# Every compile and link targets the deployment target, with Apple's
# compilers; pkg-config, where a project asks it, sees only what is built
# here. CMake 4 refuses a project that declares a minimum before 3.5.
export MACOSX_DEPLOYMENT_TARGET="$TARGET"
SDKROOT="${SDKROOT:-$(xcrun --sdk macosx --show-sdk-path)}"
CC="$(xcrun -f clang)"
CXX="$(xcrun -f clang++)"
export SDKROOT CC CXX PKG_CONFIG_LIBDIR="$DEPS/lib/pkgconfig" CMAKE_POLICY_VERSION_MINIMUM=3.5
COMPILER="$("$CC" --version | head -n 1)"
SDK_VERSION="$(xcrun --sdk macosx --show-sdk-version)"
echo "  $COMPILER, macOS SDK $SDK_VERSION, deployment target $TARGET, CMake $(cmake --version | head -n 1 | awk '{print $3}')"

# Nothing from Homebrew or /usr/local: a library found there would be
# linked in place of the one built here.
CM=(-DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DEPS" -DCMAKE_PREFIX_PATH="$DEPS"
  -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_OSX_ARCHITECTURES=arm64 -DCMAKE_OSX_DEPLOYMENT_TARGET="$TARGET"
  -DCMAKE_OSX_SYSROOT="$SDKROOT" -DCMAKE_IGNORE_PREFIX_PATH="/opt/homebrew;/usr/local"
  -DCMAKE_FIND_USE_PACKAGE_REGISTRY=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON)

TESS_RPATH="@executable_path/../lib"
OUTSIDE_PREFIXES="/opt/homebrew|/usr/local"
LEPTONICA_EXTRA=()
ocr_build

echo "Assembling the tree..."
cp "$STAGE/bin/tesseract" "$TREE/bin/"
LEPT="libleptonica.6.dylib"
cp -L "$DEPS/lib/$LEPT" "$TREE/lib/$LEPT" || die "leptonica installed no $LEPT"
cp -R "$STAGE/share/tessdata/configs" "$STAGE/share/tessdata/tessconfigs" "$TREE/share/tessdata/"
cp "$CACHE_DIR/eng-fast-$TESSDATA_TAG.traineddata" "$TREE/share/tessdata/eng.traineddata"
chmod 755 "$TREE/bin/tesseract" "$TREE/lib/$LEPT"
[ -f "$TREE/share/tessdata/configs/tsv" ] || die "tesseract installed no configs/tsv, which downmark asks for"

# Every reference is the tree's library or the system's, and the program
# finds the library through its run path.
problems=
for f in "$TREE/bin/tesseract" "$TREE/lib/$LEPT"; do
  refs="$(otool -L "$f" | tail -n +2 | awk '{print $1}' | grep -v -E "^(/usr/lib/|/System/|@rpath/$LEPT\$)" || true)"
  [ -z "$refs" ] || problems="$problems$(basename "$f"): $refs"$'\n'
done
[ "$(otool -D "$TREE/lib/$LEPT" | tail -n 1)" = "@rpath/$LEPT" ] || problems="${problems}$LEPT: install name $(otool -D "$TREE/lib/$LEPT" | tail -n 1)"$'\n'
rpaths="$(otool -l "$TREE/bin/tesseract" | awk '/cmd LC_RPATH/ {getline; getline; print $2}')"
[ "$rpaths" = "@executable_path/../lib" ] || problems="${problems}tesseract: run paths '$rpaths'"$'\n'
[ -z "$problems" ] || die "the tree does not load only its own library and the system's:"$'\n'"$problems"
# The linker warns when an object or library targets a newer macOS than the
# file it links into: that code may call what the deployment target lacks.
if grep -E "built for newer '?macOS'? version" "$LOG" | sort -u | head -n 20 | grep .; then
  die "parts of the build target a newer macOS than $TARGET (above)"
fi

echo "Signing..."
for f in "$TREE/bin/tesseract" "$TREE/lib/$LEPT"; do
  out="$(codesign --force --sign - "$f" 2>&1)" || die "codesign could not sign $f: $out"
  codesign --verify "$f" || die "codesign rejects $f after signing"
done
python3 "$HERE/macho_check.py" --max-minos "$TARGET" "$TREE/bin/tesseract" "$TREE/lib/$LEPT" ||
  die "the Mach-O check failed (above)"

echo "Licenses..."
license() {
  [ -f "$1" ] || die "no license at $1"
  cp "$1" "$TREE/licenses/$2"
}
license "$WORK/tesseract-$TESSERACT_VERSION/LICENSE" tesseract-LICENSE.txt
license "$CACHE_DIR/tessdata_fast-LICENSE-$TESSDATA_TAG" tessdata_fast-LICENSE.txt
license "$WORK/leptonica-$LEPTONICA_VERSION/leptonica-license.txt" leptonica-LICENSE.txt
license "$WORK/libpng-$LIBPNG_VERSION/LICENSE" libpng-LICENSE.txt
license "$WORK/libjpeg-turbo-$JPEG_TURBO_VERSION/LICENSE.md" libjpeg-turbo-LICENSE.md
license "$WORK/libjpeg-turbo-$JPEG_TURBO_VERSION/README.ijg" libjpeg-turbo-README.ijg
license "$WORK/tiff-$LIBTIFF_VERSION/LICENSE.md" libtiff-LICENSE.md
license "$WORK/libwebp-$LIBWEBP_VERSION/COPYING" libwebp-COPYING.txt
license "$WORK/libwebp-$LIBWEBP_VERSION/PATENTS" libwebp-PATENTS.txt
license "$WORK/giflib-$GIFLIB_VERSION/COPYING" giflib-COPYING.txt

MINOS="$(python3 "$HERE/macho_check.py" --print-minos "$TREE/bin/tesseract" "$TREE/lib/$LEPT")"
cat >"$TREE/README.txt" <<README
tesseract, the OCR engine (https://github.com/tesseract-ocr/tesseract)

Built from source by the ACTIVATE Studio build (deploy/mac/build_ocr.sh)
for macOS $TARGET and later on Apple silicon: tesseract $TESSERACT_VERSION, without
its training tools or legacy engine, and leptonica $LEPTONICA_VERSION
(lib/$LEPT), which reads PNG, JPEG, TIFF, WebP and GIF through libpng
$LIBPNG_VERSION, libjpeg-turbo $JPEG_TURBO_VERSION, libtiff $LIBTIFF_VERSION, libwebp $LIBWEBP_VERSION and giflib
$GIFLIB_VERSION, built from their release sources and linked into it. zlib is
the system's.

share/tessdata holds eng.traineddata from tessdata_fast $TESSDATA_TAG
(https://github.com/tesseract-ocr/tessdata_fast) and the configs from
tesseract's source. Set TESSDATA_PREFIX to that folder to run bin/tesseract.

The files carry ad hoc code signatures, which the app's own signing
replaces. manifest.json records the build. The licenses are in licenses/.
README

TARGET="$TARGET" MINOS="$MINOS" SDK="$SDK_VERSION" BUILD_MACOS="$(sw_vers -productVersion)" COMPILER="$COMPILER" \
  TESSERACT="$TESSERACT_VERSION" LEPTONICA="$LEPTONICA_VERSION" LIBPNG="$LIBPNG_VERSION" JPEG="$JPEG_TURBO_VERSION" \
  TIFF="$LIBTIFF_VERSION" WEBP="$LIBWEBP_VERSION" GIF="$GIFLIB_VERSION" TESSDATA="$TESSDATA_TAG" \
  python3 - "$TREE/manifest.json" <<'PY'
import datetime, json, os, sys
e = os.environ
manifest = {
    'tesseract': e['TESSERACT'],
    'deploymentTarget': e['TARGET'],
    'minos': e['MINOS'],
    'arch': 'arm64',
    'components': {
        'leptonica': e['LEPTONICA'], 'libpng': e['LIBPNG'], 'libjpeg-turbo': e['JPEG'],
        'libtiff': e['TIFF'], 'libwebp': e['WEBP'], 'giflib': e['GIF'],
    },
    'tessdata': {'source': 'tessdata_fast', 'tag': e['TESSDATA'], 'languages': ['eng']},
    'builtWith': {'macos': e['BUILD_MACOS'], 'sdk': e['SDK'], 'compiler': e['COMPILER']},
    'builtAt': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
}
with open(sys.argv[1], 'w') as fh:
    json.dump(manifest, fh, indent=1)
    fh.write('\n')
PY

echo "Checking it runs, from a clean environment..."
check_ocr() {
  env -i PATH=/usr/bin:/bin TESSDATA_PREFIX="$TREE/share/tessdata" "$TREE/bin/tesseract" "$@"
}
check_ocr --version 2>&1 | head -n 1 | grep -q "tesseract $TESSERACT_VERSION" || die "tesseract --version does not say $TESSERACT_VERSION"
check_ocr --list-langs 2>&1 | grep -qx eng || die "tesseract does not list eng"
for img in "$HERE"/testdata/ocr-sample.*; do
  case "$img" in *.pdf) continue ;; esac
  check_ocr "$img" stdout 2>/dev/null | grep -qi heliotrope || die "tesseract does not read $(basename "$img")"
  echo "  reads $(basename "$img")"
done

echo "Packing..."
rm -f "$OUT_DIR/$ASSET.tar.gz" "$OUT_DIR/$ASSET.tar.gz.sha256" "$OUT_DIR/$ASSET.manifest.json"
COPYFILE_DISABLE=1 tar --no-mac-metadata -czf "$OUT_DIR/$ASSET.tar.gz" -C "$WORK" ocr
SUM="$(sha256_of "$OUT_DIR/$ASSET.tar.gz")"
echo "$SUM  $ASSET.tar.gz" >"$OUT_DIR/$ASSET.tar.gz.sha256"
NAME="$ASSET.tar.gz" SUM="$SUM" SIZE="$(wc -c <"$OUT_DIR/$ASSET.tar.gz" | tr -d ' ')" \
  python3 - "$TREE/manifest.json" "$OUT_DIR/$ASSET.manifest.json" <<'PY'
import json, os, sys
m = json.load(open(sys.argv[1]))
m.update(asset=os.environ['NAME'], sha256=os.environ['SUM'], size=int(os.environ['SIZE']))
with open(sys.argv[2], 'w') as fh:
    json.dump(m, fh, indent=1)
    fh.write('\n')
PY

echo
echo "tesseract $TESSERACT_VERSION for macOS $TARGET and later (its files need macOS $MINOS): $(du -sh "$TREE" | cut -f1) unpacked"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$OUT_DIR/$ASSET.tar.gz" | cut -f1)), with .sha256 and $ASSET.manifest.json"
echo "Use it:  OCR_TARBALL=$OUT_DIR/$ASSET.tar.gz deploy/mac/build_app.sh"
