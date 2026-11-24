#!/usr/bin/env bash
# The identity of the OCR tree deploy/mac/build_ocr.sh makes: a short hash of
# that script and the OCR sources it pins in deploy/common/pins.sh. An ocr-*
# release is named ocr-KEY-macos14 (.github/workflows/ocr-macos.yml), and
# mac-app.yml takes the archive from OCR_RELEASE only when the name carries
# the key of the checkout it builds, so a change to the script or the pins
# builds OCR from source until a new release is made.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
{
  cat "$HERE/build_ocr.sh"
  grep -E '^(TESSERACT|LEPTONICA|LIBPNG|JPEG_TURBO|LIBTIFF|LIBWEBP|GIFLIB|TESSDATA|ENG)_' "$HERE/../common/pins.sh"
} | shasum -a 256 | cut -c1-12
