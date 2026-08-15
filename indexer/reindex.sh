#!/usr/bin/env bash
# Rebuild the GUFI index of the knowledge base, then run text enrichment.
# Full rebuild each time; the corpus is small (thousands of real files).
set -euo pipefail

KB_ROOT="${KB_ROOT:?KB_ROOT must be set (see .env)}"
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INDEX_BASE="${INDEX_BASE:-$PROJECT_ROOT/index}"
GUFI_BIN="${GUFI_BIN:-/opt/gufi/bin}"
GUFI_DEST="$INDEX_BASE/gufi"
STAGING="$INDEX_BASE/gufi.new"

mkdir -p "$INDEX_BASE"
rm -rf "$STAGING"
mkdir -p "$STAGING"

# gufi_dir2index creates $STAGING/<basename of KB_ROOT>.
# -x records extended attributes; thread count sized to the host.
SKIP_FILE="$INDEX_BASE/skip-dirs.txt"
printf '%s\n' .git node_modules .venv venv __pycache__ dist build .cache .pytest_cache screenshots .ipynb_checkpoints .claude .github > "$SKIP_FILE"
"$GUFI_BIN/gufi_dir2index" -x -n "$(nproc)" --skip-file "$SKIP_FILE" "$KB_ROOT" "$STAGING"

# Enrichment: extract text into fts5 tables inside the staged index and
# refresh the on-disk extract cache used for previews.
# Indexer scripts need Python 3.10+; some clusters default python3 to 3.9.
PYTHON_BIN="${PYTHON_BIN:-}"
if [ -z "$PYTHON_BIN" ]; then
  for cand in python3.13 python3.12 python3.11 python3.10 python3; do
    if command -v "$cand" >/dev/null 2>&1 && "$cand" -c 'import sys; sys.exit(0 if sys.version_info >= (3, 10) else 1)' 2>/dev/null; then
      PYTHON_BIN="$cand"; break
    fi
  done
fi
PYTHON_BIN="${PYTHON_BIN:-python3}"
echo "python: $PYTHON_BIN ($($PYTHON_BIN --version 2>&1))"

"$PYTHON_BIN" "$PROJECT_ROOT/indexer/enrich.py" --kb-root "$KB_ROOT" \
  --index "$STAGING/$(basename "$KB_ROOT")" --extract-cache "$INDEX_BASE/extract"

# Vector embeddings (skippable: SKIP_EMBED=1). Model fetched by setup_gufi.sh.
MODEL="$INDEX_BASE/models/minilm384.gguf"
if [ -z "${SKIP_EMBED:-}" ] && [ -f "$MODEL" ]; then
  "$PYTHON_BIN" "$PROJECT_ROOT/indexer/embed.py" --index "$STAGING/$(basename "$KB_ROOT")" --model "$MODEL"
fi

# Atomic-ish swap so the server never sees a half-built tree.
#
# gufi_dir2index nests its output under the source basename, and the server
# expects $INDEX_BASE/gufi to mirror the corpus directly (its incremental
# passes write $INDEX_BASE/gufi/<subdir>). Promoting the nested directory
# rather than the staging root is what keeps the two agreeing: with the
# extra level in place, enrichment resolved every subdirectory to
# $KB_ROOT/<kb-basename>/<subdir>, which does not exist, so nothing below
# the root was ever indexed.
if [ -d "$GUFI_DEST" ]; then
  rm -rf "$INDEX_BASE/gufi.old"
  mv "$GUFI_DEST" "$INDEX_BASE/gufi.old"
fi
mv "$STAGING/$(basename "$KB_ROOT")" "$GUFI_DEST"
rm -rf "$INDEX_BASE/gufi.old" "$STAGING"

echo "index rebuilt at $GUFI_DEST"
