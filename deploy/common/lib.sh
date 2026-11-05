# Shell helpers the desktop apps' build scripts share; source it after
# setting NAME, the script's name for messages (build_app, build_ocr, ...).

die() { echo "${NAME:-build}: $*" >&2; exit 1; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{print $1}'
}

# fetch URL FILE SHA256 [curl options]: download into the cache once, and
# check the file against its checksum on every run, so a corrupt or
# replaced cache entry is caught too.
fetch() {
  local url="$1" file="$2" sum="$3"
  shift 3
  if [ ! -f "$file" ] || [ "$(sha256_of "$file")" != "$sum" ]; then
    echo "  fetching $url"
    curl -fsSL --retry 3 "$@" -o "$file.part" "$url"
    mv "$file.part" "$file"
  fi
  local got
  got="$(sha256_of "$file")"
  [ "$got" = "$sum" ] || die "$file has SHA-256 $got, expected $sum"
}

# gcc_licenses DIR PREFIX: the GPL 3 and the GCC Runtime Library Exception
# into DIR, as PREFIX-COPYING3.txt and PREFIX-COPYING.RUNTIME.txt (needs
# CACHE_DIR and the pins).
gcc_licenses() {
  local dir="$1" prefix="$2" base="https://raw.githubusercontent.com/gcc-mirror/gcc/$GCC_LICENSE_TAG"
  fetch "$base/COPYING3" "$CACHE_DIR/gcc-COPYING3" "$GCC_COPYING3_SHA256"
  fetch "$base/COPYING.RUNTIME" "$CACHE_DIR/gcc-COPYING.RUNTIME" "$GCC_RUNTIME_EXCEPTION_SHA256"
  cp "$CACHE_DIR/gcc-COPYING3" "$dir/$prefix-COPYING3.txt"
  cp "$CACHE_DIR/gcc-COPYING.RUNTIME" "$dir/$prefix-COPYING.RUNTIME.txt"
}
