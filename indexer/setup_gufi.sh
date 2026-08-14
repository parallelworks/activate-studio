#!/usr/bin/env bash
# One-time GUFI toolchain build. Installs to /opt/gufi.
set -euo pipefail

SRC="${GUFI_SRC:-$HOME/gufi-src}"
PREFIX="${GUFI_PREFIX:-/opt/gufi}"

sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
  cmake libsqlite3-dev pkg-config zlib1g-dev libpcre2-dev libattr1-dev attr \
  autoconf automake libtool tesseract-ocr poppler-utils

if [ ! -d "$SRC" ]; then
  git clone https://github.com/mar-file-system/GUFI.git "$SRC"
fi
cd "$SRC"
git fetch --tags
mkdir -p build && cd build
cmake .. -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX"
make -j"$(nproc)"
sudo make install

"$PREFIX/bin/gufi_query" -h >/dev/null 2>&1 || true
echo "GUFI installed at $PREFIX"

# Embedding model for the vector layer (384-dim all-MiniLM, per the GUFI master doc).
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODEL_DIR="$PROJECT_ROOT/index/models"
mkdir -p "$MODEL_DIR"
if [ ! -f "$MODEL_DIR/minilm384.gguf" ]; then
  curl -sL -o "$MODEL_DIR/minilm384.gguf" \
    'https://huggingface.co/asg017/sqlite-lembed-model-examples/resolve/main/all-MiniLM-L6-v2/all-MiniLM-L6-v2.e4ce9877.q8_0.gguf'
fi
