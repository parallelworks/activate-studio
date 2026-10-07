#!/usr/bin/env bash
# Build a self-contained deployment bundle for the ACTIVATE workflow
# (deploy/workflow.yaml). The bundle carries everything the target resource
# cannot be assumed to have: the production server with its node_modules
# (pnpm deploy), the built web app, the indexer, a Node runtime, the GUFI
# source tree, and the embedding model. Compilers (gcc, cmake) are needed on
# the resource only if GUFI is built there.
#
# Usage: deploy/make_bundle.sh [output.tar.gz]
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$PROJECT_ROOT/studio-bundle.tar.gz}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

NODE_VERSION="v26.10.0"
# From nodejs.org/dist/<version>/SHASUMS256.txt; change both together.
NODE_SHA256="ca70e9e349de048b9522abb3adc05b3bd6f43c5ffd3ec57916c7da292f59f022"
NODE_DIST="node-${NODE_VERSION}-linux-x64"
CACHE="$PROJECT_ROOT/deploy/.cache"
mkdir -p "$CACHE"

echo "Building web and server..."
(cd "$PROJECT_ROOT" && pnpm build >/dev/null)

echo "Staging production server (pnpm deploy)..."
mkdir -p "$STAGE/app"
(cd "$PROJECT_ROOT" && pnpm --filter @activate-studio/server --prod deploy --legacy "$STAGE/app/server" >/dev/null)
# The deploy records a prod-only install; restore the dev one.
(cd "$PROJECT_ROOT" && pnpm install --frozen-lockfile >/dev/null)

echo "Staging web, indexer, docs, starters..."
# Stamp the build so a running deployment can say which one it is. Without
# this a refresh that silently failed is indistinguishable from one that
# worked, since PROJECT_ROOT in a deployment is app/ and there is no git
# checkout there to ask.
VERSION="$(cd "$PROJECT_ROOT" && git describe --tags --always --dirty 2>/dev/null || echo dev)"
case "$VERSION" in v*) ;; *) VERSION="dev" ;; esac
COMMIT="$(cd "$PROJECT_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
printf '{\n "version": "%s",\n "commit": "%s",\n "builtAt": "%s"\n}\n' \
  "$VERSION" "$COMMIT" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$STAGE/app/build-info.json"
echo "Build stamp: $VERSION ($COMMIT)"

mkdir -p "$STAGE/app/web"
cp -r "$PROJECT_ROOT/web/dist" "$STAGE/app/web/dist"
cp -r "$PROJECT_ROOT/indexer" "$STAGE/app/indexer"
cp -r "$PROJECT_ROOT/docs" "$STAGE/app/docs"
cp -r "$PROJECT_ROOT/extensions-starter" "$STAGE/app/extensions-starter"

echo "Staging embedding model..."
mkdir -p "$STAGE/app/index/models"
if [ -f "$PROJECT_ROOT/index/models/minilm384.gguf" ]; then
  cp "$PROJECT_ROOT/index/models/minilm384.gguf" "$STAGE/app/index/models/"
else
  echo "  (model not present locally; vector search disabled unless added)"
fi

echo "Fetching Node runtime ${NODE_VERSION}..."
if [ ! -f "$CACHE/${NODE_DIST}.tar.xz" ]; then
  curl -fsSL -o "$CACHE/${NODE_DIST}.tar.xz" \
    "https://nodejs.org/dist/${NODE_VERSION}/${NODE_DIST}.tar.xz"
fi
# Checked on every run, so a corrupt or tampered cache is caught too.
# shasum covers macOS, where sha256sum may be missing.
if command -v sha256sum >/dev/null 2>&1; then
  echo "${NODE_SHA256}  $CACHE/${NODE_DIST}.tar.xz" | sha256sum -c -
else
  echo "${NODE_SHA256}  $CACHE/${NODE_DIST}.tar.xz" | shasum -a 256 -c -
fi
cp "$CACHE/${NODE_DIST}.tar.xz" "$STAGE/"

echo "Fetching GUFI source..."
if [ ! -d "$CACHE/GUFI" ]; then
  git clone --depth 1 --recurse-submodules --shallow-submodules \
    https://github.com/mar-file-system/GUFI "$CACHE/GUFI"
fi
tar -C "$CACHE" --exclude='GUFI/.git' -czf "$STAGE/gufi-src.tar.gz" GUFI

echo "Packing $OUT ..."
tar -C "$STAGE" -czf "$OUT" .
ls -lh "$OUT"
echo "Upload with: pw buckets cp $OUT <bucket-uri>/studio-bundle.tar.gz"
