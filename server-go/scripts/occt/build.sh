#!/usr/bin/env bash
# Builds the STEP mesher's modules into server-go/occt/ (or the folder given):
# occt-import-js.wasm and occt-import-js.noeh.wasm, from the occt-import-js
# package server/ installs, with binaryen's wasm-opt passes (prepare.mjs).
# binaryen comes from the npm registry at a pinned version into a temporary
# folder; nothing is installed.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
out="${1:-$root/server-go/occt}"
BINARYEN_VERSION="${BINARYEN_VERSION:-132.0.0}"
src="$root/server/node_modules/occt-import-js/dist/occt-import-js.wasm"
[ -f "$src" ] || { echo "no $src: run pnpm install first" >&2; exit 1; }
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
(cd "$tmp" && timeout 300 npm pack --silent "binaryen@$BINARYEN_VERSION" >/dev/null && tar xzf binaryen-*.tgz)
mkdir -p "$out"
timeout 600 node "$here/prepare.mjs" "$tmp/package/index.js" "$src" "$out/occt-import-js.wasm"
ls -l "$out"
