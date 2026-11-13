#!/usr/bin/env bash
# Build GUFI from source for Linux x86_64, as a relocatable tree for the
# Linux app (deploy/linux/build_app.sh with GUFI_TARBALL set).
#
# GUFI is built with its AI dependencies (sqlite-vec, and sqlite-lembed with
# llama.cpp; with GUFI_LEMBED=0 sqlite-vec alone, for the Go version, whose
# server embeds with ONNX and loads no GGUF model), together with the two libraries a desktop install may not have,
# PCRE2 (which sqlite3-pcre loads) and GCC's OpenMP runtime (libgomp, which
# llama.cpp and GUFI link). The binaries' run paths are set so that each
# loads its libraries from the tree (patchelf), and the tree is checked with
# elf_check.py: it needs no glibc newer than --max-glibc, and every library
# it loads is in the tree or one every desktop has.
#
# Build it in the oldest glibc the app supports, a Rocky Linux 8 container
# (glibc 2.28), so one tree runs on RHEL 8 and later and on Ubuntu 22.04 and
# later. The build installs its tools with dnf when it finds one and runs as
# root (SKIP_DEPS=1 to skip that).
#
# Usage: deploy/linux/build_gufi.sh
#   GUFI_REF     GUFI branch, tag or commit (default: the commit of GUFI's
#                0.8.1 tag, deploy/common/pins.sh)
#   GUFI_SRC     an existing GUFI checkout to build as it is, instead of
#                cloning GUFI_REF
#   OUT_DIR      where the outputs go (default deploy/linux/out)
#   CACHE_DIR    downloads, kept between builds (default deploy/linux/.cache)
#   JOBS         parallel build jobs (default: the number of CPUs)
#   SKIP_DEPS    1 to skip installing the build tools
#   GUFI_LEMBED  0 for the lean build: sqlite-vec without sqlite-lembed and
#                llama.cpp (deploy/common/gufi-no-lembed.patch, applied to
#                GUFI's source, adds the DEP_LEMBED switch it builds with)
#
# Outputs in OUT_DIR: gufi-linux-x86_64.tar.gz (gufi/ with bin, lib,
# licenses, README.txt and manifest.json) and its .sha256; the lean build is
# gufi-vec-linux-x86_64.tar.gz.
# shellcheck disable=SC2034  # settings and pins that the sourced files and the build read
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAME=build_gufi
# shellcheck source=../common/lib.sh
. "$HERE/../common/lib.sh"
# shellcheck source=../common/pins.sh
. "$HERE/../common/pins.sh"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
ASSET=gufi-linux-x86_64
LEMBED="${GUFI_LEMBED:-1}"
case "$LEMBED" in 1) ;; 0) ASSET=gufi-vec-linux-x86_64 ;; *) die "GUFI_LEMBED is 1 or 0" ;; esac
GUFI_REF="${GUFI_REF:-$GUFI_COMMIT}"   # tag 0.8.1

# The ref can come from a workflow's free-text input and goes to git.
[[ "$GUFI_REF" =~ ^[A-Za-z0-9._][A-Za-z0-9._/-]*$ ]] || die "GUFI_REF is not a branch, tag or commit: '$GUFI_REF'"
[ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ] || die "runs on Linux x86_64"

if [ "${SKIP_DEPS:-}" != 1 ] && [ "$(id -u)" = 0 ] && command -v dnf >/dev/null 2>&1; then
  echo "Build tools from dnf..."
  # GUFI's cmake needs column (util-linux) and setfattr/getfattr (attr) at
  # configure time; its dependencies build with autotools; patchelf is from
  # EPEL. libatomic and libgomp are the GCC runtime libraries the tree
  # carries (libgomp) and the app's Node needs (libatomic).
  dnf install -y -q --setopt=install_weak_deps=False \
    gcc gcc-c++ make cmake git ca-certificates tar xz bzip2 python3 pkgconf-pkg-config \
    util-linux attr libattr-devel diffutils patch gawk which file findutils \
    autoconf automake libtool binutils zlib-devel libgomp libatomic epel-release >/dev/null
  dnf install -y -q patchelf >/dev/null
  el8_tools
fi
for tool in gcc g++ cmake make git curl tar patchelf python3 column setfattr readelf strip; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd -P)"
JOBS="${JOBS:-$(nproc)}"
WORK="$(mktemp -d "$OUT_DIR/.gufi-work.XXXXXX")"
# KEEP_WORK=1 leaves the build directory in OUT_DIR, to look at after a failure.
trap '[ "${KEEP_WORK:-}" = 1 ] || rm -rf "$WORK"' EXIT
DEPS="$WORK/deps"       # PCRE2
BUILD="$WORK/gufi-build"
STAGE="$WORK/stage"
TREE="$WORK/gufi"
LOG="$WORK/build.log"
mkdir -p "$DEPS" "$STAGE" "$TREE/bin" "$TREE/lib" "$TREE/licenses"

COMPILER="$(gcc --version | awk 'NR == 1')"
GLIBC="$(ldd --version 2>&1 | awk 'NR == 1 {print $NF}')"
echo "  $COMPILER, glibc $GLIBC, CMake $(cmake --version | awk 'NR == 1 {print $3}')"

# run CMD...: a build step, its output kept for the checks at the end.
run() { "$@" 2>&1 | tee -a "$LOG"; }

echo "PCRE2 $PCRE2_SRC_VERSION..."
PCRE2_VERSION="$PCRE2_SRC_VERSION"
fetch "https://github.com/PCRE2Project/pcre2/releases/download/pcre2-$PCRE2_VERSION/pcre2-$PCRE2_VERSION.tar.bz2" \
  "$CACHE_DIR/pcre2-$PCRE2_VERSION.tar.bz2" "$PCRE2_SRC_SHA256"
tar -xjf "$CACHE_DIR/pcre2-$PCRE2_VERSION.tar.bz2" -C "$WORK"
# Shared, which libsqlite3-pcre loads, and static (position independent),
# which GUFI 0.8.0 links into its tools and into gufi_vt; JIT on.
(
  cd "$WORK/pcre2-$PCRE2_VERSION"
  run ./configure --prefix="$DEPS" --libdir="$DEPS/lib" --enable-shared --enable-static --with-pic --enable-jit
  run make -j "$JOBS"
  run make install
)

echo "GCC runtime licenses..."
gcc_licenses "$WORK" gcc
echo "GUFI..."
if [ -n "${GUFI_SRC:-}" ]; then
  SRC="$(cd "$GUFI_SRC" && pwd -P)"
  GUFI_REF="(a local checkout)"
  echo "  building $SRC as it is"
else
  SRC="$WORK/gufi-src"
  # A full clone: GUFI's CMake names the version with git describe --tags.
  git clone --quiet "$GUFI_REPO" "$SRC"
  commit="$(git -C "$SRC" rev-parse --verify --quiet "$GUFI_REF^{commit}" ||
    git -C "$SRC" rev-parse --verify --quiet "origin/$GUFI_REF^{commit}")" ||
    die "GUFI has no branch, tag or commit '$GUFI_REF'"
  git -C "$SRC" checkout --quiet --detach "$commit"
fi
GUFI_REV="$(git -C "$SRC" rev-parse HEAD)"
GUFI_DESCRIBE="$(git -C "$SRC" describe --tags --always --dirty)"
echo "  $GUFI_DESCRIBE ($GUFI_REV)"
if [ "$LEMBED" = 0 ]; then
  echo "  without sqlite-lembed and llama.cpp (deploy/common/gufi-no-lembed.patch)"
  git -C "$SRC" apply --check "$HERE/../common/gufi-no-lembed.patch" || die "gufi-no-lembed.patch does not apply to this GUFI"
  git -C "$SRC" apply "$HERE/../common/gufi-no-lembed.patch"
fi
# llama.cpp (vendored by sqlite-lembed) defaults to -march=native, which
# bakes in the build host's instructions: the tree has to run on any x86-64
# CPU the targets run on.
if [ "$LEMBED" = 0 ]; then
  :
elif grep -q -e '-DGGML_CCACHE=Off' "$SRC/contrib/deps/sqlite-lembed.sh"; then
  sed -i 's/-DGGML_CCACHE=Off/-DGGML_CCACHE=Off -DGGML_NATIVE=Off/' "$SRC/contrib/deps/sqlite-lembed.sh"
  grep -q -e '-DGGML_NATIVE=Off' "$SRC/contrib/deps/sqlite-lembed.sh" || die "could not turn off -march=native for llama.cpp"
else
  grep -q -e 'GGML_NATIVE' "$SRC/contrib/deps/sqlite-lembed.sh" || die "contrib/deps/sqlite-lembed.sh has changed; check how llama.cpp is built"
fi
# spread_tree.c opens a new file with O_CREAT and no mode, which Ubuntu's
# default _FORTIFY_SOURCE turns into a compile error where Rocky's does not
# (as indexer/setup_gufi.sh does).
SPREAD="$SRC/src/gufi_query/spread_tree.c"
if grep -q 'open(extdb_path, O_CREAT | O_WRONLY)' "$SPREAD" 2>/dev/null; then
  sed -i 's/open(extdb_path, O_CREAT | O_WRONLY)/open(extdb_path, O_CREAT | O_WRONLY, S_IRUSR | S_IWUSR)/' "$SPREAD"
fi
(
  # PCRE2 built above comes first for every compile, link and CMake lookup,
  # the nested dependency builds' included.
  export PKG_CONFIG_PATH="$DEPS/lib/pkgconfig" CMAKE_PREFIX_PATH="$DEPS"
  export CFLAGS="-I$DEPS/include" CXXFLAGS="-I$DEPS/include" LDFLAGS="-L$DEPS/lib"
  # GUFI links a jemalloc that pkg-config finds instead of building its own;
  # the tree must not depend on one.
  if pkg-config --exists jemalloc 2>/dev/null; then
    die "pkg-config finds a jemalloc, which GUFI would link instead of building its own"
  fi
  run cmake -S "$SRC" -B "$BUILD" \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$STAGE" \
    -DDEP_AI=On -DDEP_LEMBED="$([ "$LEMBED" = 1 ] && echo On || echo Off)" \
    -DDEP_BUILD_THREADS="$JOBS" -DBASH_COMPLETION=Off
  run cmake --build "$BUILD" -j "$JOBS"
  # The Local component is what GUFI's package holds: the tools, and the
  # bundled dependencies' libraries, which a plain install leaves out. It
  # also installs an example config at the absolute /etc/GUFI, so the
  # install goes under a root of its own and only the prefix is used. The
  # install copies the dependencies' licenses from <build>/licenses, which
  # GUFI's own script downloads; they are put there from the sources built
  # here, as deploy/mac/build_gufi.sh does.
  mkdir -p "$BUILD/licenses"
  B0="$BUILD/builds"
  stock() {   # stock SOURCE NAME
    [ -f "$1" ] || die "no license at $1 (has GUFI's dependency layout changed?)"
    cp "$1" "$BUILD/licenses/$2"
  }
  stock "$WORK/pcre2-$PCRE2_VERSION/LICENCE.md" pcre2-LICENSE.md
  stock "$B0/sqlite-vec-main/LICENSE-APACHE" sqlite-vec-LICENSE-APACHE
  stock "$B0/sqlite-vec-main/LICENSE-MIT" sqlite-vec-LICENSE-MIT
  [ "$LEMBED" = 0 ] || stock "$B0/sqlite-lembed/vendor/llama.cpp/LICENSE" llama.cpp-LICENSE
  stock "$B0/jemalloc-dev/COPYING" jemalloc-COPYING
  # GUFI's install names an OpenMP license; the runtime here is GCC's libgomp.
  cat "$CACHE_DIR/gcc-COPYING3" "$CACHE_DIR/gcc-COPYING.RUNTIME" >"$BUILD/licenses/openmp-LICENSE.TXT"
  printf '%s\n' "sqlite-lembed has no license file in its repository; its sqlite-dist.toml lists MIT OR Apache." \
    "See the licenses of sqlite-vec, which are the same, from the same author." >"$BUILD/licenses/sqlite-lembed"
  run env DESTDIR="$WORK/root" cmake --install "$BUILD" --component Local --prefix "$STAGE"
)

echo "Assembling the tree..."
cp -a "$WORK/root$STAGE/bin/." "$TREE/bin/"
cp -a "$WORK/root$STAGE/lib/." "$TREE/lib/"
# GUFI links its AI tooling against libraries under the build tree's
# dependency prefix (llama.cpp, and its own SQLite, which gufi_vt and the
# sqlite3-pcre and dsi plugins load by the unversioned name libsqlite3.so);
# a plain install leaves them out. They are copied in where the install has
# not already put them, as the container recipe (deploy/app.def) does.
find "$TREE/lib" -xtype l -delete
for l in "$BUILD"/deps/*/lib/*.so*; do
  [ -e "$l" ] || continue
  [ -e "$TREE/lib/$(basename "$l")" ] || cp -a "$l" "$TREE/lib/"
done
cp -L "$DEPS"/lib/libpcre2-8.so.0 "$TREE/lib/"
# GCC's OpenMP runtime, which every target may lack (it is not part of a
# desktop install), from the build container's gcc.
cp -L "$(gcc -print-file-name=libgomp.so.1)" "$TREE/lib/libgomp.so.1"
# Libraries nothing here loads (libllama-common links OpenSSL and cJSON,
# which the tree does not carry), build metadata, and static libraries.
rm -rf "$TREE/lib/pkgconfig" "$TREE/lib/cmake" "$TREE"/lib/libllama-common* "$TREE"/lib/libpresidio_plugin* \
  "$TREE"/lib/*.a "$TREE"/lib/*.la
# GUFI 0.8.0 installs some libraries (libvec0, liblembed0, libsqlite3) as
# links to a file by absolute path, a path of its build that is not in the
# tree. In the tree they are links to the file of that name beside them.
for l in "$TREE"/lib/*; do
  [ -L "$l" ] || continue
  case "$(readlink "$l")" in
    /*) own="$(basename "$(readlink "$l")")"
        rm -f "$l"
        if [ "$own" != "$(basename "$l")" ] && [ -f "$TREE/lib/$own" ]; then ln -s "$own" "$l"; fi ;;
  esac
done
for l in "$TREE"/lib/* "$TREE"/bin/*; do
  if [ -L "$l" ] && [ ! -e "$l" ]; then rm -f "$l"; fi
done
chmod 755 "$TREE"/lib/*.so*

# Every file loads its libraries from the tree: a program from lib/ beside
# bin/, a library from its own directory. (A RUNPATH covers a file's direct
# dependencies only, which is why each library gets one.)
ELF_BINS=() ELF_LIBS=()
for f in "$TREE"/bin/*; do
  if [ -f "$f" ] && [ ! -L "$f" ] && head -c 4 "$f" | grep -q ELF; then ELF_BINS+=("$f"); fi
done
for f in "$TREE"/lib/*; do
  if [ -f "$f" ] && [ ! -L "$f" ] && head -c 4 "$f" | grep -q ELF; then ELF_LIBS+=("$f"); fi
done
# The lean build's programs lose their symbols first (strip after patchelf
# leaves a program the loader rejects): about 10 MB each become under 1 MB.
if [ "$LEMBED" = 0 ]; then
  for f in "${ELF_BINS[@]}"; do strip --strip-unneeded "$f"; done
fi
for f in "${ELF_BINS[@]}"; do patchelf --set-rpath '$ORIGIN/../lib' "$f"; done
for f in "${ELF_LIBS[@]}"; do patchelf --set-rpath '$ORIGIN' "$f"; done
echo "  run paths set on ${#ELF_BINS[@]} programs and ${#ELF_LIBS[@]} libraries"

echo "Licenses..."
# license FILE NAME: a license from the sources, into licenses/.
license() {
  [ -f "$1" ] || die "no license at $1 (has GUFI's dependency layout changed?)"
  cp "$1" "$TREE/licenses/$2"
}
B="$BUILD/builds"    # GUFI's DEP_BUILD_PREFIX: its dependencies' sources
license "$SRC/LICENSE.txt" GUFI-LICENSE.txt
license "$B/sqlite-vec-main/LICENSE-MIT" sqlite-vec-LICENSE-MIT.txt
license "$B/sqlite-vec-main/LICENSE-APACHE" sqlite-vec-LICENSE-APACHE.txt
[ "$LEMBED" = 0 ] || license "$B/sqlite-lembed/vendor/llama.cpp/LICENSE" llama.cpp-LICENSE.txt
license "$B/jemalloc-dev/COPYING" jemalloc-COPYING.txt
license "$WORK/pcre2-$PCRE2_VERSION/LICENCE.md" pcre2-LICENCE.md
# libgomp is GCC's runtime library, under the GPL with the runtime library
# exception, which lets programs of any license link and ship it.
gcc_licenses "$TREE/licenses" libgomp
if [ "$LEMBED" = 1 ]; then
[ -f "$B/sqlite-lembed/sqlite-dist.toml" ] || die "no sqlite-dist.toml in sqlite-lembed"
{
  echo "sqlite-lembed (https://github.com/asg017/sqlite-lembed) has no license file."
  echo "Its sqlite-dist.toml states:"
  echo
  sed -n '/^\[package\]/,/^$/p' "$B/sqlite-lembed/sqlite-dist.toml" | grep -E '^(name|license|authors|repo) *='
  echo
  echo "The MIT and Apache 2.0 texts are those of sqlite-vec, by the same author:"
  echo "sqlite-vec-LICENSE-MIT.txt and sqlite-vec-LICENSE-APACHE.txt here."
} >"$TREE/licenses/sqlite-lembed-LICENSE.txt"
fi

# The versions GUFI's bundled dependencies report, or "unknown".
version_in() { if [ -f "$1" ]; then head -n 1 "$1" | tr -d '[:space:]'; else echo unknown; fi; }
pc_version() { sed -n 's/^Version: *//p' "$1" 2>/dev/null | head -n 1 | grep . || echo unknown; }
SQLITE_VERSION="$(pc_version "$BUILD/deps/sqlite3/lib/pkgconfig/sqlite3.pc")"
LLAMA_VERSION="$(pc_version "$BUILD/deps/llama.cpp/lib/pkgconfig/llama.pc")"
VEC_VERSION="$(version_in "$B/sqlite-vec-main/VERSION")"
LEMBED_VERSION="$(version_in "$B/sqlite-lembed/VERSION")"
[ "$LEMBED" = 1 ] || LEMBED_VERSION=none LLAMA_VERSION=none
GOMP_VERSION="$(gcc -dumpfullversion)"

if [ "$LEMBED" = 1 ]; then AI_DEPS="sqlite-vec $VEC_VERSION,
sqlite-lembed $LEMBED_VERSION, llama.cpp $LLAMA_VERSION"
else AI_DEPS="sqlite-vec $VEC_VERSION
(built without sqlite-lembed and llama.cpp, deploy/common/gufi-no-lembed.patch)"
fi
cat >"$TREE/README.txt" <<README
GUFI, the Grand Unified File Index (Los Alamos National Laboratory)
https://github.com/mar-file-system/GUFI

Built from source by the ACTIVATE Studio build (deploy/linux/build_gufi.sh)
for Linux x86_64 on glibc $GLIBC and later: GUFI $GUFI_DESCRIBE
($GUFI_REV), with the dependencies GUFI bundles (SQLite $SQLITE_VERSION,
sqlite3-pcre, $AI_DEPS, and jemalloc, linked into the tools), PCRE2
$PCRE2_VERSION (lib/libpcre2-8.so.0) built from its release source, and GCC's
OpenMP runtime from GCC $GOMP_VERSION (lib/libgomp.so.1).

The files are modified from what GUFI's install produces: their run paths
were set (patchelf) so that the tools load their libraries from lib/ here
wherever the tree is. libllama-common and libpresidio_plugin, which nothing
here loads, are left out. manifest.json records the build.

The licenses are in licenses/. SQLite and sqlite3-pcre are in the public
domain.
README

GUFI_REF="$GUFI_REF" GUFI_REV="$GUFI_REV" GUFI_DESCRIBE="$GUFI_DESCRIBE" GLIBC="$GLIBC" \
  BUILD_OS="$(. /etc/os-release && echo "$PRETTY_NAME")" COMPILER="$COMPILER" \
  PCRE2="$PCRE2_VERSION" SQLITE="$SQLITE_VERSION" VEC="$VEC_VERSION" \
  LEMBED="$LEMBED_VERSION" LLAMA="$LLAMA_VERSION" GOMP="$GOMP_VERSION" python3 - "$TREE/manifest.json" <<'PY'
import datetime, json, os, sys
e = os.environ
manifest = {
    'gufi': {'ref': e['GUFI_REF'], 'commit': e['GUFI_REV'], 'version': e['GUFI_DESCRIBE']},
    'arch': 'x86_64',
    'glibc': e['GLIBC'],
    'components': {
        'pcre2': e['PCRE2'], 'sqlite': e['SQLITE'], 'sqlite-vec': e['VEC'],
        'sqlite-lembed': e['LEMBED'], 'llama.cpp': e['LLAMA'], 'libgomp': e['GOMP'],
    },
    'builtWith': {'os': e['BUILD_OS'], 'compiler': e['COMPILER']},
    'builtAt': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
}
with open(sys.argv[1], 'w') as fh:
    json.dump(manifest, fh, indent=1)
    fh.write('\n')
PY

echo "Checking the tree..."
NEED_LEMBED=lib/lembed0.so
[ "$LEMBED" = 1 ] || NEED_LEMBED=
for f in bin/gufi_query bin/gufi_dir2index bin/gufi_sqlite3 lib/gufi_vt.so lib/vec0.so $NEED_LEMBED \
  lib/libgomp.so.1 lib/libpcre2-8.so.0 licenses/GUFI-LICENSE.txt manifest.json; do
  [ -s "$TREE/$f" ] || die "the tree has no $f"
done
python3 -I "$HERE/elf_check.py" --root "$TREE" --list "$TREE" || die "the ELF check failed (above)"
# Run what was built, from a clean environment, with nothing of the build's
# (the build tree and PCRE2's prefix are still on disk here, so a library
# found there would hide a missing one: the loader is told to use only the
# tree's run paths by checking that no file needs a library elsewhere).
mkdir -p "$WORK/t/corpus"
printf 'a recipe for lemon cake\n' >"$WORK/t/corpus/cake.txt"
env -i PATH="$TREE/bin:/usr/bin:/bin" "$TREE/bin/gufi_dir2index" -x "$WORK/t/corpus" "$WORK/t/idx"
env -i PATH="$TREE/bin:/usr/bin:/bin" "$TREE/bin/gufi_query" -d ' ' -E 'SELECT name FROM vrpentries;' "$WORK/t/idx/corpus" | grep -q cake.txt \
  || die "gufi_query does not read an index"
echo "  gufi_dir2index and gufi_query work"
env -i PATH="$TREE/bin:/usr/bin:/bin" "$TREE/bin/gufi_query" -d ' ' -E "SELECT vec_length('[1,2,3]');" "$WORK/t/idx/corpus" | grep -q 3 \
  || die "gufi_query has no sqlite-vec"
if [ "$LEMBED" = 0 ] && find "$TREE" -name 'libllama*' -o -name 'lembed0*' | grep -q .; then
  die "the lean tree has llama.cpp or sqlite-lembed in it"
fi

echo "Packing..."
rm -f "$OUT_DIR/$ASSET.tar.gz" "$OUT_DIR/$ASSET.tar.gz.sha256"
tar -czf "$OUT_DIR/$ASSET.tar.gz" -C "$WORK" gufi
echo "$(sha256_of "$OUT_DIR/$ASSET.tar.gz")  $ASSET.tar.gz" >"$OUT_DIR/$ASSET.tar.gz.sha256"
echo
echo "GUFI $GUFI_DESCRIBE for Linux x86_64 on glibc $GLIBC and later: $(du -sh "$TREE" | cut -f1) unpacked"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$OUT_DIR/$ASSET.tar.gz" | cut -f1)), with .sha256"
echo "Use it:  GUFI_TARBALL=$OUT_DIR/$ASSET.tar.gz deploy/linux/build_app.sh"
