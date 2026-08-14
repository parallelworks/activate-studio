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
python3 "$PROJECT_ROOT/indexer/enrich.py" --kb-root "$KB_ROOT" \
  --index "$STAGING/$(basename "$KB_ROOT")" --extract-cache "$INDEX_BASE/extract"

# Vector embeddings (skippable: SKIP_EMBED=1). Model fetched by setup_gufi.sh.
MODEL="$INDEX_BASE/models/minilm384.gguf"
if [ -z "${SKIP_EMBED:-}" ] && [ -f "$MODEL" ]; then
  python3 "$PROJECT_ROOT/indexer/embed.py" --index "$STAGING/$(basename "$KB_ROOT")" --model "$MODEL"
fi

# Atomic-ish swap so the server never sees a half-built tree.
if [ -d "$GUFI_DEST" ]; then
  rm -rf "$INDEX_BASE/gufi.old"
  mv "$GUFI_DEST" "$INDEX_BASE/gufi.old"
fi
mv "$STAGING" "$GUFI_DEST"
rm -rf "$INDEX_BASE/gufi.old"

echo "index rebuilt at $GUFI_DEST/$(basename "$KB_ROOT")"
