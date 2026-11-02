#!/usr/bin/env bash
# One-time GUFI toolchain build. Installs to /opt/gufi.
set -euo pipefail

# Resolved before any cd, so running this by a relative path works.
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${GUFI_SRC:-$HOME/gufi-src}"
PREFIX="${GUFI_PREFIX:-/opt/gufi}"
# GUFI's AI dependencies (sqlite-vec and sqlite-lembed, which carries
# llama.cpp) are what semantic search needs, and they are also the hard part
# of the build. DEP_AI defaults to On upstream, so a plain cmake attempts
# them; GUFI_AI=0 turns them off for a quick build that keeps full-text and
# metadata search and loses only the semantic blend.
AI="${GUFI_AI:-1}"
# The GUFI release the Studio is tested with (tag 0.8.0): a commit, a tag or a
# branch. A clone's checkout is moved to it, so an older build is replaced.
GUFI_REF="${GUFI_REF:-021a717a6089b9e28b068695dc7c7f3732fd1a56}"
JOBS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"

case "$(uname -s)" in
  Darwin)
    # Matching GUFI's own macOS CI: Homebrew's LLVM rather than Apple clang
    # (libomp is not wired into the system toolchain), plus the GNU
    # utilities its scripts assume. The GNU tools go first on PATH for this
    # build only, which is why the caller's PATH is left alone.
    command -v brew >/dev/null 2>&1 || { echo "Homebrew is required: https://brew.sh"; exit 1; }
    brew install cmake pkgconf pcre2 sqlite autoconf automake libtool \
      llvm libomp gettext coreutils findutils gnu-sed gpatch grep diffutils flock \
      tesseract poppler
    for tool in coreutils findutils gnu-sed grep; do
      PATH="$(brew --prefix "$tool")/libexec/gnubin:$PATH"
    done
    # GUFI 0.8.0's configure looks for flock, which Homebrew installs apart from util-linux.
    PATH="$(brew --prefix diffutils)/bin:$(brew --prefix flock)/bin:$PATH"
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
    # A root shell (a container) has no sudo and needs none.
    SUDO=sudo
    [ "$(id -u)" = 0 ] && SUDO=
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update
    # build-essential, git, curl and python3 are used below. bsdextrautils
    # provides column, which GUFI's cmake requires at configure time.
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y \
      build-essential git curl ca-certificates python3 bsdextrautils \
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
git checkout --quiet --force "$GUFI_REF"
echo "GUFI $(git describe --tags --always) ($(git rev-parse HEAD))"
# spread_tree.c opens a new file with O_CREAT and no mode argument. Ubuntu's
# default _FORTIFY_SOURCE turns that into a compile error (Debian's does not).
# Give the call the mode it needs; a source that already has one is left alone.
SPREAD=src/gufi_query/spread_tree.c
if grep -q 'open(extdb_path, O_CREAT | O_WRONLY)' "$SPREAD" 2>/dev/null; then
  sed -i.orig 's/open(extdb_path, O_CREAT | O_WRONLY)/open(extdb_path, O_CREAT | O_WRONLY, S_IRUSR | S_IWUSR)/' "$SPREAD"
  rm -f "$SPREAD.orig"
fi
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
# The ONNX embedding backend, faster and better at search by meaning: the
# int8 model beside the GGUF one, and onnxruntime, tokenizers and numpy for
# the Python the Studio runs the indexer with, in indexer/.deps. Without
# them (no network, or a Python with no pinned wheels) the GGUF model is used.
PY="${PYTHON_BIN:-}"
if [ -z "$PY" ]; then
  for cand in python3.13 python3.12 python3.11 python3.10 python3; do
    if command -v "$cand" >/dev/null 2>&1; then PY="$cand"; break; fi
  done
fi
"$PY" "$PROJECT_ROOT/indexer/fetch_embed.py" --deps "$PROJECT_ROOT/indexer/.deps" --model "$MODEL_DIR/minilm-l6" \
  --cache "$PROJECT_ROOT/deploy/.cache/embed" ||
  echo "The ONNX embedding backend was not installed; search by meaning uses the GGUF model."
