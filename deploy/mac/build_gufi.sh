#!/usr/bin/env bash
# Build GUFI from source for macOS on Apple silicon, as a relocatable tree
# for the app (deploy/mac/build_app.sh with GUFI_TARBALL set).
#
# GUFI's own macOS package is built without a deployment target, so every
# binary in it declares the macOS release that built it as its minimum.
# This builds GUFI with its AI dependencies (sqlite-vec, and sqlite-lembed
# with llama.cpp) for MACOSX_DEPLOYMENT_TARGET, together with the two
# libraries it otherwise takes from Homebrew, LLVM's OpenMP runtime and
# PCRE2, whose bottles target the newest macOS releases only. The tree's
# files are then relinked to load their libraries from the tree
# (macho_relink.py), signed ad hoc, and checked against the deployment
# target (check_gufi.sh --static). check_gufi.sh without --static also runs
# what was built.
#
# Runs on a Mac with Apple silicon, Homebrew and the Command Line Tools,
# such as GitHub's macos-15 runner (.github/workflows/gufi-macos.yml). The
# build tools come from Homebrew, as GUFI's macOS guide asks (its LLVM and
# the GNU utilities); nothing the tree loads does.
#
# Usage: deploy/mac/build_gufi.sh
#   GUFI_REF     GUFI branch, tag or commit (default: the commit of GUFI's
#                macos-pkg-test release, whose package build_app.sh uses)
#   GUFI_SRC     an existing GUFI checkout to build as it is, instead of
#                cloning GUFI_REF
#   MACOSX_DEPLOYMENT_TARGET  the oldest macOS the tree runs on (default 14.0)
#   OUT_DIR      where the outputs go (default deploy/mac/out)
#   CACHE_DIR    downloads, kept between builds (default deploy/mac/.cache)
#   JOBS         parallel build jobs (default: the number of CPUs)
#   SKIP_BREW    1 to skip installing the Homebrew build tools
#
# Outputs in OUT_DIR: gufi-macos-arm64.tar.gz (gufi/ with bin, lib,
# licenses, README.txt and manifest.json), its .sha256, and
# gufi-macos-arm64.manifest.json (the manifest with the archive's name,
# size and SHA-256).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT_DIR="${OUT_DIR:-$HERE/out}"
CACHE_DIR="${CACHE_DIR:-$HERE/.cache}"
ASSET=gufi-macos-arm64
TARGET="${MACOSX_DEPLOYMENT_TARGET:-14.0}"
GUFI_REPO=https://github.com/mar-file-system/GUFI
GUFI_REF="${GUFI_REF:-1713537a4b8fbc188fa75f8e9845edcaf8bf69ba}"

# Pinned sources, checked against their SHA-256 on every build. libomp is
# the release build_app.sh takes as a Homebrew bottle, built here from
# LLVM's source the way Homebrew's formula builds it.
LIBOMP_VERSION=23.1.4
LLVM_SRC_SHA256=819ecf7351f1a66f76b2d701d8942faaf5faeb324cb90c9d5b090de142434bb2
PCRE2_VERSION=10.49
PCRE2_SHA256=53c156e1ba416a20da8e65395daa132da0d80e76910424caca3fcdae7831d384

die() { echo "build_gufi: $*" >&2; exit 1; }

# Both can come from a workflow's free-text inputs: the target goes into
# compiler flags and the ref to git, so each must be what it claims to be.
[[ "$TARGET" =~ ^[0-9]{2}(\.[0-9]{1,2})?$ ]] || die "MACOSX_DEPLOYMENT_TARGET must look like 14.0, not '$TARGET'"
[[ "$GUFI_REF" =~ ^[A-Za-z0-9._][A-Za-z0-9._/-]*$ ]] || die "GUFI_REF is not a branch, tag or commit: '$GUFI_REF'"
if [ "$(uname -s)" != Darwin ] || [ "$(uname -m)" != arm64 ]; then die "runs on a Mac with Apple silicon"; fi
for tool in brew xcrun codesign otool git curl tar python3; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

sha256_of() { shasum -a 256 "$1" | awk '{print $1}'; }

# fetch URL FILE SHA256: download into the cache once.
fetch() {
  local url="$1" file="$2" sum="$3"
  if [ ! -f "$file" ] || [ "$(sha256_of "$file")" != "$sum" ]; then
    echo "  fetching $url"
    curl -fsSL -o "$file.part" "$url"
    mv "$file.part" "$file"
  fi
  [ "$(sha256_of "$file")" = "$sum" ] || die "$file has SHA-256 $(sha256_of "$file"), expected $sum"
}

mkdir -p "$OUT_DIR" "$CACHE_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd -P)"
CACHE_DIR="$(cd "$CACHE_DIR" && pwd -P)"
JOBS="${JOBS:-$(sysctl -n hw.ncpu)}"
# The working path is long on purpose: macho_relink.py rewrites library
# paths in place, so each build-time path must be at least as long as the
# @loader_path one that replaces it.
WORK="$(mktemp -d "$OUT_DIR/.gufi-work.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
DEPS="$WORK/deps"       # libomp and PCRE2
BUILD="$WORK/gufi-build"
STAGE="$WORK/stage"
TREE="$WORK/gufi"
LOG="$WORK/build.log"
mkdir -p "$DEPS" "$STAGE" "$TREE/bin" "$TREE/lib" "$TREE/licenses"

# Every compile and link sees the target through the environment, which
# clang and CMake both read: GUFI builds its bundled dependencies with
# nested configure and CMake runs that a flag given to GUFI's own CMake
# would not reach.
export MACOSX_DEPLOYMENT_TARGET="$TARGET"
SDKROOT="${SDKROOT:-$(xcrun --sdk macosx --show-sdk-path)}"
export SDKROOT HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_INSTALL_CLEANUP=1

echo "Build tools from Homebrew..."
# GUFI's contrib/CI/macos.sh list, less pcre2 and libomp, which are built
# here, and sqlite, which GUFI bundles.
if [ "${SKIP_BREW:-}" != 1 ]; then
  brew install cmake pkgconf llvm autoconf automake libtool gettext \
    coreutils findutils gnu-sed gpatch grep diffutils flock
fi
BREW_PREFIX="$(brew --prefix)"
LLVM_PREFIX="$(brew --prefix llvm)"
# The llama.cpp that GUFI's sqlite-lembed vendors leans on headers that
# older C++ libraries pulled in through others (errno, EXIT_FAILURE and
# std::fill without <cerrno>, <cstdlib> and <algorithm>), which the libc++
# of newer LLVM with this SDK no longer does. GUFI's dependency build sets
# its own flags for that project, so CXXFLAGS does not reach it; the C++
# compiler itself is a wrapper that includes the common standard headers in
# every compile.
mkdir -p "$WORK/cxx"
INCLUDES=""
for h in cerrno cstdlib cstring cstdint climits cmath new algorithm functional memory string vector; do
  INCLUDES="$INCLUDES -include $h"
done
cat >"$WORK/cxx/clang++" <<EOF
#!/bin/sh
case " \$* " in
  *" -c "*) exec "$LLVM_PREFIX/bin/clang++" $INCLUDES "\$@" ;;
esac
exec "$LLVM_PREFIX/bin/clang++" "\$@"
EOF
chmod 755 "$WORK/cxx/clang++"
export CC="$LLVM_PREFIX/bin/clang" CXX="$WORK/cxx/clang++"
COMPILER="$("$CC" --version | head -n 1)"
SDK_VERSION="$(xcrun --sdk macosx --show-sdk-version)"
echo "  $COMPILER, macOS SDK $SDK_VERSION, deployment target $TARGET"

# run CMD...: a build step, its output kept for the checks at the end.
run() { "$@" 2>&1 | tee -a "$LOG"; }

echo "LLVM OpenMP $LIBOMP_VERSION..."
LLVM_SRC="llvm-project-$LIBOMP_VERSION.src"
fetch "https://github.com/llvm/llvm-project/releases/download/llvmorg-$LIBOMP_VERSION/$LLVM_SRC.tar.xz" \
  "$CACHE_DIR/$LLVM_SRC.tar.xz" "$LLVM_SRC_SHA256"
# The runtimes build needs these parts of the monorepo, not all of it.
tar -xJf "$CACHE_DIR/$LLVM_SRC.tar.xz" -C "$WORK" "$LLVM_SRC/runtimes" "$LLVM_SRC/openmp" \
  "$LLVM_SRC/cmake" "$LLVM_SRC/llvm/cmake" "$LLVM_SRC/llvm/utils/llvm-lit" "$LLVM_SRC/llvm/utils/lit" \
  "$LLVM_SRC/third-party/unittest"
# An absolute install name, which macho_relink.py rewrites like the others.
run cmake -S "$WORK/$LLVM_SRC/runtimes" -B "$WORK/libomp-build" \
  -DLLVM_ENABLE_RUNTIMES=openmp -DLIBOMP_INSTALL_ALIASES=OFF -DOPENMP_ENABLE_OMPT_TOOLS=OFF \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$DEPS" -DCMAKE_INSTALL_NAME_DIR="$DEPS/lib" \
  -DCMAKE_OSX_DEPLOYMENT_TARGET="$TARGET" -DCMAKE_OSX_SYSROOT=macosx
run cmake --build "$WORK/libomp-build" -j "$JOBS"
run cmake --install "$WORK/libomp-build"

echo "PCRE2 $PCRE2_VERSION..."
fetch "https://github.com/PCRE2Project/pcre2/releases/download/pcre2-$PCRE2_VERSION/pcre2-$PCRE2_VERSION.tar.bz2" \
  "$CACHE_DIR/pcre2-$PCRE2_VERSION.tar.bz2" "$PCRE2_SHA256"
tar -xjf "$CACHE_DIR/pcre2-$PCRE2_VERSION.tar.bz2" -C "$WORK"
# Shared only, so that every GUFI file links the one library the tree
# carries; JIT on, as in the Homebrew build the package path uses.
(
  cd "$WORK/pcre2-$PCRE2_VERSION"
  run ./configure --prefix="$DEPS" --disable-static --enable-jit
  run make -j "$JOBS"
  run make install
)

echo "GUFI..."
if [ -n "${GUFI_SRC:-}" ]; then
  SRC="$(cd "$GUFI_SRC" && pwd -P)"
  GUFI_REF="(a local checkout)"
  echo "  building $SRC as it is"
else
  # Not "$WORK/GUFI": macOS file systems ignore case, and that is $TREE.
  SRC="$WORK/gufi-src"
  # A full clone: GUFI's CMake names the version with git describe --tags.
  git clone --quiet "$GUFI_REPO" "$SRC"
  commit="$(git -C "$SRC" rev-parse --verify --quiet "$GUFI_REF^{commit}" ||
    git -C "$SRC" rev-parse --verify --quiet "origin/$GUFI_REF^{commit}")" ||
    die "GUFI has no branch, tag or commit '$GUFI_REF'"
  git -C "$SRC" checkout --quiet --detach "$commit"
fi
GUFI_COMMIT="$(git -C "$SRC" rev-parse HEAD)"
GUFI_DESCRIBE="$(git -C "$SRC" describe --tags --always --dirty)"
echo "  $GUFI_DESCRIBE ($GUFI_COMMIT)"
(
  # GUFI's scripts and dependency builds expect the GNU utilities (sed -i,
  # truncate, flock), first on PATH for this build only, as in GUFI's
  # contrib/build_on_mac.sh.
  for tool in coreutils findutils gnu-sed gpatch grep; do
    PATH="$(brew --prefix "$tool")/libexec/gnubin:$PATH"
  done
  PATH="$(brew --prefix diffutils)/bin:$(brew --prefix flock)/bin:$PATH"
  # The libomp and PCRE2 built above come first for every compile, link,
  # pkg-config and CMake lookup, the nested dependency builds' included
  # (llama.cpp looks for OpenMP on its own), ahead of anything Homebrew has.
  export PATH PKG_CONFIG_PATH="$DEPS/lib/pkgconfig" CMAKE_PREFIX_PATH="$DEPS"
  export CFLAGS="-I$DEPS/include" CXXFLAGS="-I$DEPS/include" LDFLAGS="-L$DEPS/lib"
  # GUFI links a jemalloc that pkg-config finds instead of building its
  # bundled one, and Homebrew's targets a newer macOS.
  if pkg-config --exists jemalloc 2>/dev/null; then
    die "pkg-config finds a jemalloc ($(pkg-config --variable=prefix jemalloc)), which GUFI would link instead of building its own for macOS $TARGET; uninstall it (brew uninstall jemalloc) and run again"
  fi
  run cmake -S "$SRC" -B "$BUILD" \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$STAGE" \
    -DCMAKE_OSX_SYSROOT=macosx -DCMAKE_OSX_DEPLOYMENT_TARGET="$TARGET" \
    -DDEP_AI=On -DDEP_BUILD_THREADS="$JOBS" -DOpenMP_ROOT="$DEPS" \
    -DOpenMP_C_FLAGS=-fopenmp -DOpenMP_C_LIB_NAMES=libomp \
    -DOpenMP_CXX_FLAGS=-fopenmp -DOpenMP_CXX_LIB_NAMES=libomp \
    -DOpenMP_libomp_LIBRARY="$DEPS/lib/libomp.dylib"
  run cmake --build "$BUILD" -j "$JOBS"
  # The Local component is what GUFI's package holds: the tools, and the
  # bundled dependencies' libraries, which a plain install leaves out.
  run cmake --install "$BUILD" --component Local --prefix "$STAGE"
)

echo "Assembling the tree..."
cp -R "$STAGE/bin/." "$TREE/bin/"
cp -R "$STAGE/lib/." "$TREE/lib/"
cp "$DEPS/lib/libomp.dylib" "$TREE/lib/"
cp -L "$DEPS/lib/libpcre2-8.0.dylib" "$TREE/lib/"
# What build_app.sh leaves out of GUFI's package: libraries nothing here
# loads (libllama-common links OpenSSL and cJSON, which the tree does not
# carry), build metadata, and links to files that are not installed.
rm -rf "$TREE/lib/pkgconfig" "$TREE/lib/cmake" "$TREE"/lib/libllama-common* "$TREE"/lib/libpresidio_plugin* \
  "$TREE"/lib/*.a "$TREE"/lib/*.la
for l in "$TREE"/lib/* "$TREE"/bin/*; do
  if [ -L "$l" ] && [ ! -e "$l" ]; then rm -f "$l"; fi
done
chmod 755 "$TREE"/lib/*.dylib
MACHO=()
for f in "$TREE"/bin/* "$TREE"/lib/*; do
  if [ -f "$f" ] && [ ! -L "$f" ] && file -b "$f" | grep -q Mach-O; then MACHO+=("$f"); fi
done

# Everything the tree loads has to come from this build. A library linked
# from Homebrew would be relinked below to the tree's library of the same
# name, which would hide that a build step picked up the wrong one.
outside=
for f in "${MACHO[@]}"; do
  refs="$(otool -L "$f" | tail -n +2 | awk '{print $1}' | grep -E "^($BREW_PREFIX|/usr/local)/" || true)"
  [ -z "$refs" ] || outside="$outside$(basename "$f"): $refs"$'\n'
done
[ -z "$outside" ] || die "linked against libraries outside the build:"$'\n'"$outside"
# The linker warns when an object or library targets a newer macOS than the
# file it links into: that code may call what the deployment target lacks.
if grep -E "built for newer '?macOS'? version" "$LOG" | sort -u | head -n 20 | grep .; then
  die "parts of the build target a newer macOS than $TARGET (above)"
fi

echo "Relinking and signing..."
CHANGED="$(python3 "$HERE/macho_relink.py" "$TREE" | wc -l)"
echo "  relinked $((CHANGED)) files to load their libraries from the tree (macho_relink.py)"
for f in "${MACHO[@]}"; do
  out="$(codesign --force --sign - "$f" 2>&1)" || die "codesign could not sign $f: $out"
  codesign --verify "$f" || die "codesign rejects $f after signing"
done

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
license "$B/sqlite-lembed/vendor/llama.cpp/LICENSE" llama.cpp-LICENSE.txt
license "$B/jemalloc-dev/COPYING" jemalloc-COPYING.txt
license "$WORK/$LLVM_SRC/openmp/LICENSE.TXT" libomp-LICENSE.txt
license "$WORK/pcre2-$PCRE2_VERSION/LICENCE.md" pcre2-LICENCE.md
# sqlite-lembed has no license file; its sqlite-dist.toml states the license.
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

# The versions GUFI's bundled dependencies report, or "unknown".
version_in() { if [ -f "$1" ]; then head -n 1 "$1" | tr -d '[:space:]'; else echo unknown; fi; }
pc_version() { sed -n 's/^Version: *//p' "$1" 2>/dev/null | head -n 1 | grep . || echo unknown; }
SQLITE_VERSION="$(pc_version "$BUILD/deps/sqlite3/lib/pkgconfig/sqlite3.pc")"
LLAMA_VERSION="$(pc_version "$BUILD/deps/llama.cpp/lib/pkgconfig/llama.pc")"
VEC_VERSION="$(version_in "$B/sqlite-vec-main/VERSION")"
LEMBED_VERSION="$(version_in "$B/sqlite-lembed/VERSION")"
MINOS="$(python3 "$HERE/macho_check.py" --print-minos "${MACHO[@]}")"

cat >"$TREE/README.txt" <<README
GUFI, the Grand Unified File Index (Los Alamos National Laboratory)
https://github.com/mar-file-system/GUFI

Built from source by the ACTIVATE Studio build (deploy/mac/build_gufi.sh)
for macOS $TARGET and later on Apple silicon: GUFI $GUFI_DESCRIBE
($GUFI_COMMIT), with the dependencies GUFI bundles (SQLite $SQLITE_VERSION,
sqlite3-pcre, sqlite-vec $VEC_VERSION, sqlite-lembed $LEMBED_VERSION,
llama.cpp $LLAMA_VERSION, and jemalloc, linked into the tools), and LLVM
OpenMP $LIBOMP_VERSION (lib/libomp.dylib) and PCRE2 $PCRE2_VERSION
(lib/libpcre2-8.0.dylib), built from their release sources.

The files are modified from what GUFI's install produces: their library
references were rewritten (deploy/mac/macho_relink.py) so that the tools
load their libraries from lib/ here wherever the tree is, and they carry
ad hoc code signatures. libllama-common and libpresidio_plugin, which
nothing here loads, are left out. manifest.json records the build.

The licenses are in licenses/. SQLite and sqlite3-pcre are in the public
domain.
README

GUFI_REF="$GUFI_REF" GUFI_COMMIT="$GUFI_COMMIT" GUFI_DESCRIBE="$GUFI_DESCRIBE" TARGET="$TARGET" \
  MINOS="$MINOS" SDK="$SDK_VERSION" BUILD_MACOS="$(sw_vers -productVersion)" COMPILER="$COMPILER" \
  LIBOMP="$LIBOMP_VERSION" PCRE2="$PCRE2_VERSION" SQLITE="$SQLITE_VERSION" VEC="$VEC_VERSION" \
  LEMBED="$LEMBED_VERSION" LLAMA="$LLAMA_VERSION" python3 - "$TREE/manifest.json" <<'PY'
import datetime, json, os, sys
e = os.environ
manifest = {
    'gufi': {'ref': e['GUFI_REF'], 'commit': e['GUFI_COMMIT'], 'version': e['GUFI_DESCRIBE']},
    'deploymentTarget': e['TARGET'],
    'minos': e['MINOS'],
    'arch': 'arm64',
    'components': {
        'libomp': e['LIBOMP'], 'pcre2': e['PCRE2'], 'sqlite': e['SQLITE'],
        'sqlite-vec': e['VEC'], 'sqlite-lembed': e['LEMBED'], 'llama.cpp': e['LLAMA'],
    },
    'builtWith': {'macos': e['BUILD_MACOS'], 'sdk': e['SDK'], 'compiler': e['COMPILER']},
    'builtAt': datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ'),
}
with open(sys.argv[1], 'w') as fh:
    json.dump(manifest, fh, indent=1)
    fh.write('\n')
PY

echo "Checking the tree..."
"$HERE/check_gufi.sh" --static "$TREE"

echo "Packing..."
rm -f "$OUT_DIR/$ASSET.tar.gz" "$OUT_DIR/$ASSET.tar.gz.sha256" "$OUT_DIR/$ASSET.manifest.json"
COPYFILE_DISABLE=1 tar --no-mac-metadata -czf "$OUT_DIR/$ASSET.tar.gz" -C "$WORK" gufi
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
echo "GUFI $GUFI_DESCRIBE for macOS $TARGET and later (its files need macOS $MINOS)"
echo "Archive: $OUT_DIR/$ASSET.tar.gz ($(du -h "$OUT_DIR/$ASSET.tar.gz" | cut -f1)), with .sha256 and $ASSET.manifest.json"
echo "Run it:  deploy/mac/check_gufi.sh $OUT_DIR/$ASSET.tar.gz"
echo "Use it:  GUFI_TARBALL=$OUT_DIR/$ASSET.tar.gz deploy/mac/build_app.sh"
