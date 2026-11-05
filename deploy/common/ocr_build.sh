# The OCR build the macOS and Linux apps share (deploy/mac/build_ocr.sh,
# deploy/linux/build_ocr.sh): tesseract, leptonica and the static image
# libraries leptonica reads, from the sources deploy/common/pins.sh pins.
# Sourced after the script has set CACHE_DIR, WORK, DEPS, STAGE, JOBS and
# NAME, and sourced lib.sh and pins.sh. ocr_fetch_sources fetches and unpacks
# the sources; ocr_build builds them once the script has set its compilers
# and these, which differ by platform:
#   CM                 CMake arguments every project is built with
#   TESS_RPATH         tesseract's run path, to find lib/ beside bin/
#   OUTSIDE_PREFIXES   an egrep alternation of prefixes whose libraries must
#                      not be linked in place of the ones built here
#   LEPTONICA_EXTRA    extra CMake arguments for leptonica (an array)

ocr_fetch_sources() {
echo "Sources..."
fetch "https://github.com/tesseract-ocr/tesseract/archive/refs/tags/$TESSERACT_VERSION.tar.gz" \
  "$CACHE_DIR/tesseract-$TESSERACT_VERSION.tar.gz" "$TESSERACT_SHA256"
fetch "https://github.com/DanBloomberg/leptonica/releases/download/$LEPTONICA_VERSION/leptonica-$LEPTONICA_VERSION.tar.gz" \
  "$CACHE_DIR/leptonica-$LEPTONICA_VERSION.tar.gz" "$LEPTONICA_SHA256"
fetch "https://github.com/pnggroup/libpng/archive/refs/tags/v$LIBPNG_VERSION.tar.gz" \
  "$CACHE_DIR/libpng-$LIBPNG_VERSION.tar.gz" "$LIBPNG_SHA256"
fetch "https://github.com/libjpeg-turbo/libjpeg-turbo/releases/download/$JPEG_TURBO_VERSION/libjpeg-turbo-$JPEG_TURBO_VERSION.tar.gz" \
  "$CACHE_DIR/libjpeg-turbo-$JPEG_TURBO_VERSION.tar.gz" "$JPEG_TURBO_SHA256"
fetch "https://download.osgeo.org/libtiff/tiff-$LIBTIFF_VERSION.tar.gz" \
  "$CACHE_DIR/tiff-$LIBTIFF_VERSION.tar.gz" "$LIBTIFF_SHA256"
fetch "https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-$LIBWEBP_VERSION.tar.gz" \
  "$CACHE_DIR/libwebp-$LIBWEBP_VERSION.tar.gz" "$LIBWEBP_SHA256"
fetch "https://downloads.sourceforge.net/project/giflib/giflib-6.x/giflib-$GIFLIB_VERSION.tar.gz" \
  "$CACHE_DIR/giflib-$GIFLIB_VERSION.tar.gz" "$GIFLIB_SHA256"
fetch "https://github.com/tesseract-ocr/tessdata_fast/raw/$TESSDATA_TAG/eng.traineddata" \
  "$CACHE_DIR/eng-fast-$TESSDATA_TAG.traineddata" "$ENG_SHA256"
fetch "https://github.com/tesseract-ocr/tessdata_fast/raw/$TESSDATA_TAG/LICENSE" \
  "$CACHE_DIR/tessdata_fast-LICENSE-$TESSDATA_TAG" "$TESSDATA_LICENSE_SHA256"
for f in tesseract-$TESSERACT_VERSION leptonica-$LEPTONICA_VERSION libpng-$LIBPNG_VERSION \
  libjpeg-turbo-$JPEG_TURBO_VERSION tiff-$LIBTIFF_VERSION libwebp-$LIBWEBP_VERSION giflib-$GIFLIB_VERSION; do
  tar -xzf "$CACHE_DIR/$f.tar.gz" -C "$WORK"
  [ -d "$WORK/$f" ] || die "$f.tar.gz does not unpack to $f/"
done
}

ocr_build() {
# run CMD...: a build step, its output kept for the checks at the end.
run() { "$@" 2>&1 | tee -a "$LOG"; }
# cmake_build NAME SOURCE ARGS...: configure, build and install one project.
cmake_build() {
  local name="$1" src="$2"
  shift 2
  echo "$name..."
  run cmake -S "$src" -B "$WORK/build-$name" "${CM[@]}" "$@" || die "$name: configure failed (above)"
  run cmake --build "$WORK/build-$name" -j "$JOBS" || die "$name: build failed (above)"
  run cmake --install "$WORK/build-$name" || die "$name: install failed (above)"
  # A library found outside this build would be linked into the tree.
  local outside
  outside="$(grep -E '^[A-Za-z0-9_]+(_LIBRARY|_LIBRARIES|_INCLUDE_DIR|_DIR|_LIBRARY_RELEASE)[A-Za-z0-9_]*:(FILEPATH|PATH|STRING)=('"$OUTSIDE_PREFIXES"')' \
    "$WORK/build-$name/CMakeCache.txt" || true)"
  [ -z "$outside" ] || die "$name found libraries outside the build:"$'\n'"$outside"
}

cmake_build libpng "$WORK/libpng-$LIBPNG_VERSION" \
  -DPNG_SHARED=OFF -DPNG_STATIC=ON -DPNG_FRAMEWORK=OFF -DPNG_TESTS=OFF -DPNG_TOOLS=OFF
cmake_build libjpeg-turbo "$WORK/libjpeg-turbo-$JPEG_TURBO_VERSION" \
  -DENABLE_SHARED=OFF -DENABLE_STATIC=ON -DWITH_TURBOJPEG=OFF -DWITH_TOOLS=OFF -DWITH_TESTS=OFF
# JPEG and zlib only, as leptonica reads TIFF; every other codec off.
cmake_build libtiff "$WORK/tiff-$LIBTIFF_VERSION" \
  -DBUILD_SHARED_LIBS=OFF -Dtiff-tools=OFF -Dtiff-tests=OFF -Dtiff-contrib=OFF -Dtiff-docs=OFF \
  -Dtiff-opengl=OFF -Dtiff-cxx=OFF -Dzlib=ON -Djpeg=ON -Dold-jpeg=OFF -Djpeg12=OFF -Dlibdeflate=OFF \
  -Dlzma=OFF -Dzstd=OFF -Dwebp=OFF -Djbig=OFF -Dlerc=OFF \
  -DJPEG_INCLUDE_DIR="$DEPS/include" -DJPEG_LIBRARY="$DEPS/lib/libjpeg.a"
cmake_build libwebp "$WORK/libwebp-$LIBWEBP_VERSION" \
  -DBUILD_SHARED_LIBS=OFF -DWEBP_BUILD_ANIM_UTILS=OFF -DWEBP_BUILD_CWEBP=OFF -DWEBP_BUILD_DWEBP=OFF \
  -DWEBP_BUILD_GIF2WEBP=OFF -DWEBP_BUILD_IMG2WEBP=OFF -DWEBP_BUILD_VWEBP=OFF -DWEBP_BUILD_WEBPINFO=OFF \
  -DWEBP_BUILD_WEBPMUX=OFF -DWEBP_BUILD_EXTRAS=OFF -DWEBP_BUILD_LIBWEBPMUX=ON

echo "giflib..."
# Its Makefile, for the static library only.
run make -C "$WORK/giflib-$GIFLIB_VERSION" -j "$JOBS" libgif.a CC="$CC" || die "giflib: build failed (above)"
cp "$WORK/giflib-$GIFLIB_VERSION/libgif.a" "$DEPS/lib/"
cp "$WORK/giflib-$GIFLIB_VERSION/gif_lib.h" "$DEPS/include/"

# Shared, so that it links the static image libraries in itself; every
# format is required (STRICT_CONF), so a missing one stops the build. TIFF
# by its library, not libtiff's CMake package.
cmake_build leptonica "$WORK/leptonica-$LEPTONICA_VERSION" \
  -DBUILD_SHARED_LIBS=ON -DBUILD_PROG=OFF -DSW_BUILD=OFF -DSTRICT_CONF=ON \
  -DENABLE_ZLIB=ON -DENABLE_PNG=ON -DENABLE_JPEG=ON -DENABLE_TIFF=ON -DENABLE_WEBP=ON -DENABLE_GIF=ON \
  -DENABLE_OPENJPEG=OFF -DCMAKE_DISABLE_FIND_PACKAGE_tiff=ON \
  -DPNG_PNG_INCLUDE_DIR="$DEPS/include" -DPNG_LIBRARY="$DEPS/lib/libpng16.a" \
  -DJPEG_INCLUDE_DIR="$DEPS/include" -DJPEG_LIBRARY="$DEPS/lib/libjpeg.a" \
  -DTIFF_INCLUDE_DIR="$DEPS/include" -DTIFF_LIBRARY="$DEPS/lib/libtiff.a" \
  -DGIF_INCLUDE_DIR="$DEPS/include" -DGIF_LIBRARY="$DEPS/lib/libgif.a" \
  -DWebP_DIR="$DEPS/share/WebP/cmake" ${LEPTONICA_EXTRA[@]+"${LEPTONICA_EXTRA[@]}"}

# TIFF through leptonica, which reads every page of one; tesseract's own
# use of libtiff only quiets its warnings.
cmake_build tesseract "$WORK/tesseract-$TESSERACT_VERSION" \
  -DCMAKE_INSTALL_PREFIX="$STAGE" -DCMAKE_INSTALL_RPATH="$TESS_RPATH" \
  -DBUILD_SHARED_LIBS=OFF -DBUILD_TRAINING_TOOLS=OFF -DBUILD_TESTS=OFF -DGRAPHICS_DISABLED=ON \
  -DDISABLED_LEGACY_ENGINE=ON -DDISABLE_ARCHIVE=ON -DDISABLE_CURL=ON -DDISABLE_TIFF=ON \
  -DOPENMP_BUILD=OFF -DENABLE_LTO=OFF -DENABLE_NATIVE=OFF -DENABLE_CCACHE=OFF -DINSTALL_CONFIGS=ON \
  -DLeptonica_DIR="$DEPS/lib/cmake/leptonica"
}
