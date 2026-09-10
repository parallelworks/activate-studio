#!/usr/bin/env bash
# One-time GUFI toolchain build. Installs to /opt/gufi.
set -euo pipefail

SRC="${GUFI_SRC:-$HOME/gufi-src}"
PREFIX="${GUFI_PREFIX:-/opt/gufi}"
# GUFI's AI dependencies (sqlite-vec and sqlite-lembed, which carries
# llama.cpp) are what semantic search needs, and they are also the hard part
# of the build. DEP_AI defaults to On upstream, so a plain cmake attempts
# them; GUFI_AI=0 turns them off for a quick build that keeps full-text and
# metadata search and loses only the semantic blend.
AI="${GUFI_AI:-1}"
JOBS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"

case "$(uname -s)" in
  Darwin)
    # Matching GUFI's own macOS CI: Homebrew's LLVM rather than Apple clang
    # (libomp is not wired into the system toolchain), plus the GNU
    # utilities its scripts assume. The GNU tools go first on PATH for this
    # build only, which is why the caller's PATH is left alone.
    command -v brew >/dev/null 2>&1 || { echo "Homebrew is required: https://brew.sh"; exit 1; }
    brew install cmake pkgconf pcre2 sqlite autoconf automake libtool \
      llvm libomp gettext coreutils findutils gnu-sed gpatch grep diffutils \
      tesseract poppler
    for tool in coreutils findutils gnu-sed grep; do
      PATH="$(brew --prefix "$tool")/libexec/gnubin:$PATH"
    done
    PATH="$(brew --prefix diffutils)/bin:$PATH"
    export PATH
    export CC="$(brew --prefix llvm)/bin/clang"
    export CXX="$(brew --prefix llvm)/bin/clang++"
    OMP_PREFIX="$(brew --prefix libomp)"
    EXTRA_CMAKE=(
      -DCMAKE_OSX_SYSROOT=macosx
      -DOpenMP_C_LIB_NAMES=libomp -DOpenMP_C_FLAGS=-fopenmp
      -DOpenMP_CXX_LIB_NAMES=libomp -DOpenMP_CXX_FLAGS=-fopenmp
      -DOpenMP_libomp_LIBRARY="$OMP_PREFIX/lib/libomp.dylib"
      -DCMAKE_C_FLAGS="-I$OMP_PREFIX/include"
      -DCMAKE_CXX_FLAGS="-I$OMP_PREFIX/include"
    )
    ;;
  *)
    sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
      cmake libsqlite3-dev pkg-config zlib1g-dev libpcre2-dev libattr1-dev attr \
      autoconf automake libtool tesseract-ocr poppler-utils
    EXTRA_CMAKE=()
    ;;
esac

if [ ! -d "$SRC" ]; then
  git clone https://github.com/mar-file-system/GUFI.git "$SRC"
fi
cd "$SRC"
git fetch --tags
mkdir -p build && cd build
cmake .. -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX" \
  -DDEP_AI="$([ "$AI" = "1" ] && echo On || echo Off)" \
  ${EXTRA_CMAKE+"${EXTRA_CMAKE[@]}"}
make -j"$JOBS"
# A prefix the user owns needs no sudo, which is the common case on a laptop.
if [ -w "$(dirname "$PREFIX")" ]; then make install; else sudo make install; fi

"$PREFIX/bin/gufi_query" -h >/dev/null 2>&1 || true
echo "GUFI installed at $PREFIX"

# Embedding model for the vector layer (384-dim all-MiniLM, per the GUFI master doc).
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODEL_DIR="${INDEX_BASE:-$PROJECT_ROOT/index}/models"
if [ "$AI" != "1" ]; then
  echo "Built without the AI dependencies: full-text and metadata search work, semantic search does not."
  exit 0
fi
mkdir -p "$MODEL_DIR"
if [ ! -f "$MODEL_DIR/minilm384.gguf" ]; then
  curl -sL -o "$MODEL_DIR/minilm384.gguf" \
    'https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/main/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf'
fi
