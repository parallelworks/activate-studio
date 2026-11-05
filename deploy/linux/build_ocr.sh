#!/usr/bin/env bash
# Build tesseract, the OCR engine, for the Linux app on x86_64, as a
# relocatable tree for deploy/linux/build_app.sh (OCR_TARBALL).
#
# The indexer runs tesseract on pictures (indexer/enrich.py), and downmark
# hands it each scanned PDF page as a PNG on stdin, with tesseract's tsv
# config (server/src/preextract.ts), so with this tree in the app OCR needs
# nothing installed, poppler included. tesseract and leptonica, its image
# library, are built from their release sources, with the image formats
# leptonica reads (PNG, JPEG, TIFF, WebP and GIF) as static libraries in it;
# zlib is the system's. tesseract is built without its training tools, its
# legacy engine, OpenMP (the indexer runs one tesseract per core), libarchive
# and libcurl. The sources and the build are shared with the macOS app
# (deploy/common/ocr_build.sh).
#
# The tree:
#   bin/tesseract               loads lib/ through its run path, $ORIGIN/../lib
#   lib/libleptonica.so.6
#   share/tessdata/             eng.traineddata from tessdata_fast, and
#                               tesseract's configs (tsv among them)
#   licenses/, README.txt, manifest.json
# tesseract finds its data through TESSDATA_PREFIX, which studio-run.sh sets
# to share/tessdata here.
#
# Build it in the oldest glibc the app supports, a Rocky Linux 9 container
# (see build_gufi.sh), which installs the tools with dnf when run as root
# (SKIP_DEPS=1 to skip that).
#
# Usage: deploy/linux/build_ocr.sh
#   OUT_DIR    where the outputs go (default deploy/linux/out)
#   CACHE_DIR  downloads, kept between builds (default deploy/linux/.cache)
#   JOBS       parallel build jobs (default: the number of CPUs)
#
# Outputs in OUT_DIR: ocr-linux-x86_64.tar.gz (ocr/ as above) and its .sha256.
# shellcheck disable=SC2034  # settings and pins that the sourced files and the build read
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAME=build_ocr
# shellcheck source=../common/lib.sh
. "$HERE/../common/lib.sh"
# shellcheck source=../common/pins.sh
. "$HERE/../common/pins.sh"
# shellcheck source=../common/ocr_build.sh
. "$HERE/../common/ocr_build.sh"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
ASSET=ocr-linux-x86_64

[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || die "runs on Linux x86_64"
if [ "${SKIP_DEPS:-}" != 1 ] && [ "$(id -u)" = 0 ] && command -v dnf >/dev/null 2>&1; then
  echo "Build tools from dnf..."
  dnf install -y -q --setopt=install_weak_deps=False \
    gcc gcc-c++ make cmake ca-certificates tar xz python3 pkgconf-pkg-config zlib-devel file binutils >/dev/null
fi
for tool in gcc g++ cmake make curl tar python3 readelf; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd -P)"
JOBS="${JOBS:-$(nproc)}"
WORK="$(mktemp -d "$OUT_DIR/.ocr-work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
DEPS="$WORK/deps"      # the static image libraries and leptonica
STAGE="$WORK/stage"    # tesseract's install
TREE="$WORK/ocr"
LOG="$WORK/build.log"
mkdir -p "$DEPS" "$STAGE" "$TREE/bin" "$TREE/lib" "$TREE/share/tessdata" "$TREE/licenses"

ocr_fetch_sources

# pkg-config, where a project asks it, sees only what is built here. CMake 4
# refuses a project that declares a minimum before 3.5.
export CC=gcc CXX=g++ PKG_CONFIG_LIBDIR="$DEPS/lib/pkgconfig" CMAKE_POLICY_VERSION_MINIMUM=3.5
COMPILER="$("$CC" --version | awk 'NR == 1')"
GLIBC="$(ldd --version 2>&1 | awk 'NR == 1 {print $NF}')"
echo "  $COMPILER, glibc $GLIBC, CMake $(cmake --version | awk 'NR == 1 {print $3}')"
CM=(-DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DEPS" -DCMAKE_PREFIX_PATH="$DEPS"
  -DCMAKE_INSTALL_LIBDIR=lib -DCMAKE_FIND_USE_PACKAGE_REGISTRY=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON)
TESS_RPATH='$ORIGIN/../lib'
OUTSIDE_PREFIXES="/usr/local"
# leptonica's TIFF check links libtiff, whose JPEG codec the static libjpeg
# supplies.
LEPTONICA_EXTRA=(-DCMAKE_C_STANDARD_LIBRARIES="$DEPS/lib/libjpeg.a")
ocr_build

echo "Assembling the tree..."
cp "$STAGE/bin/tesseract" "$TREE/bin/"
LEPT="libleptonica.so.6"
cp -L "$DEPS/lib/$LEPT" "$TREE/lib/$LEPT" || die "leptonica installed no $LEPT"
cp -R "$STAGE/share/tessdata/configs" "$STAGE/share/tessdata/tessconfigs" "$TREE/share/tessdata/"
cp "$CACHE_DIR/eng-fast-$TESSDATA_TAG.traineddata" "$TREE/share/tessdata/eng.traineddata"
chmod 755 "$TREE/bin/tesseract" "$TREE/lib/$LEPT"
[ -f "$TREE/share/tessdata/configs/tsv" ] || die "tesseract installed no configs/tsv, which downmark asks for"
python3 -I "$HERE/elf_check.py" --root "$TREE" "$TREE" || die "the ELF check failed (above)"

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

cat >"$TREE/README.txt" <<README
tesseract, the OCR engine (https://github.com/tesseract-ocr/tesseract)

Built from source by the ACTIVATE Studio build (deploy/linux/build_ocr.sh)
for Linux x86_64 on glibc $GLIBC and later: tesseract $TESSERACT_VERSION, without
its training tools or legacy engine, and leptonica $LEPTONICA_VERSION
(lib/$LEPT), which reads PNG, JPEG, TIFF, WebP and GIF through libpng
$LIBPNG_VERSION, libjpeg-turbo $JPEG_TURBO_VERSION, libtiff $LIBTIFF_VERSION, libwebp $LIBWEBP_VERSION and giflib
$GIFLIB_VERSION, built from their release sources and linked into it. zlib is
the system's.

share/tessdata holds eng.traineddata from tessdata_fast $TESSDATA_TAG
(https://github.com/tesseract-ocr/tessdata_fast) and the configs from
tesseract's source. Set TESSDATA_PREFIX to that folder to run bin/tesseract.

manifest.json records the build. The licenses are in licenses/.
README

GLIBC="$GLIBC" COMPILER="$COMPILER" BUILD_OS="$(. /etc/os-release && echo "$PRETTY_NAME")" \
  TESSERACT="$TESSERACT_VERSION" LEPTONICA="$LEPTONICA_VERSION" LIBPNG="$LIBPNG_VERSION" JPEG="$JPEG_TURBO_VERSION" \
  TIFF="$LIBTIFF_VERSION" WEBP="$LIBWEBP_VERSION" GIF="$GIFLIB_VERSION" TESSDATA="$TESSDATA_TAG" \
  python3 - "$TREE/manifest.json" <<'PY'
import datetime, json, os, sys
e = os.environ
manifest = {
    'tesseract': e['TESSERACT'],
    'arch': 'x86_64',
    'glibc': e['GLIBC'],
    'components': {
        'leptonica': e['LEPTONICA'], 'libpng': e['LIBPNG'], 'libjpeg-turbo': e['JPEG'],
        'libtiff': e['TIFF'], 'libwebp': e['WEBP'], 'giflib': e['GIF'],
    },
    'tessdata': {'source': 'tessdata_fast', 'tag': e['TESSDATA'], 'languages': ['eng']},
    'builtWith': {'os': e['BUILD_OS'], 'compiler': e['COMPILER']},
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
VERSION_OUT="$(check_ocr --version 2>&1 || true)"
printf '%s\n' "$VERSION_OUT" | head -n 1 | grep -q "tesseract $TESSERACT_VERSION" || die "tesseract --version does not say $TESSERACT_VERSION, but: $VERSION_OUT"
LANGS_OUT="$(check_ocr --list-langs 2>&1 || true)"
printf '%s\n' "$LANGS_OUT" | grep -qx eng || die "tesseract does not list eng"
for img in "$HERE"/../mac/testdata/ocr-sample.*; do
  case "$img" in *.pdf) continue ;; esac
  TEXT_OUT="$(check_ocr "$img" stdout 2>/dev/null || true)"
  printf '%s\n' "$TEXT_OUT" | grep -qi heliotrope || die "tesseract does not read $(basename "$img")"
  echo "  reads $(basename "$img")"
done

echo "Packing..."
rm -f "$OUT_DIR/$ASSET.tar.gz" "$OUT_DIR/$ASSET.tar.gz.sha256"
tar -czf "$OUT_DIR/$ASSET.tar.gz" -C "$WORK" ocr
echo "$(sha256_of "$OUT_DIR/$ASSET.tar.gz")  $ASSET.tar.gz" >"$OUT_DIR/$ASSET.tar.gz.sha256"
echo
echo "tesseract $TESSERACT_VERSION for Linux x86_64 on glibc $GLIBC and later: $(du -sh "$TREE" | cut -f1) unpacked"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$OUT_DIR/$ASSET.tar.gz" | cut -f1)), with .sha256"
echo "Use it:  OCR_TARBALL=$OUT_DIR/$ASSET.tar.gz deploy/linux/build_app.sh"
