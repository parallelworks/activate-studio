#!/usr/bin/env bash
# Builds the STEP mesher's modules into server-go/occt/ (or the folder given):
# occt-import-js.wasm and occt-import-js.noeh.wasm, with binaryen's wasm-opt
# passes (prepare.mjs).
#
# Both inputs come from the npm registry at exact versions, each checked
# against the npm integrity hash recorded here (the sha512 the registry and
# pnpm-lock.yaml give); a mismatch stops the build. They go into a
# temporary folder; nothing is installed. To move to a new version, change
# the version and its hash together (npm view PACKAGE@VERSION dist.integrity).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
out="${1:-$root/server-go/occt}"

OCCT_VERSION=0.0.23
OCCT_INTEGRITY=sha512-RFfYQXYFX5C1mB1Aywm0ShcUKzXOr/VzTnlzhBSDJOR6YCAPt1HYCzeXWg1vwwjn/cUxwqRNhhtf1dlewoZYCQ==
BINARYEN_VERSION=132.0.0
BINARYEN_INTEGRITY=sha512-4Usg7yEvwO3FMso3Oov6qkOOHSod1IEv+boKkh77kTljM+eNBouqCTbqWswGzbG+vDNO/b/v2C4gfEh1WgB9Lg==
# What these inputs and prepare.mjs made when the hashes above were recorded
# (the build is deterministic): a different result is reported, not fatal,
# since a change to prepare.mjs changes it on purpose.
OUT_SHA256_WASM=78435f4cdec688511f65a300352948d42dcef45b529904095d0a8c40ac9a0d0e
OUT_SHA256_NOEH=13fcfcf5f225b6dc86d9cb3c827dec81a6024f83f8b82cd3ad03639887a0b52f

# timeout SECONDS COMMAND where there is one (macOS has none).
limit() {
  local s="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then timeout "$s" "$@"; else "$@"; fi
}

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# fetch NAME VERSION INTEGRITY: the package's tarball, checked, unpacked in
# $tmp/NAME/package.
fetch() {
  local name="$1" version="$2" want="$3" tgz got
  mkdir -p "$tmp/$name"
  tgz="$(cd "$tmp/$name" && limit 300 npm pack --silent "$name@$version" | tail -n 1)"
  got="sha512-$(node -e 'process.stdout.write(require("crypto").createHash("sha512").update(require("fs").readFileSync(process.argv[1])).digest("base64"))' "$tmp/$name/$tgz")"
  if [ "$got" != "$want" ]; then
    echo "build.sh: $name@$version has integrity $got, not the recorded $want" >&2
    exit 1
  fi
  tar -xzf "$tmp/$name/$tgz" -C "$tmp/$name"
}
fetch occt-import-js "$OCCT_VERSION" "$OCCT_INTEGRITY"
fetch binaryen "$BINARYEN_VERSION" "$BINARYEN_INTEGRITY"

# The same occt-import-js as server/ installs (pnpm-lock.yaml), so the Go
# mesher and Node's model.ts read STEP files with one module.
lock="$root/pnpm-lock.yaml"
if [ -f "$lock" ] && ! grep -qF "occt-import-js@$OCCT_VERSION:" "$lock"; then
  echo "build.sh: pnpm-lock.yaml does not pin occt-import-js@$OCCT_VERSION" >&2
  exit 1
fi

mkdir -p "$out"
limit 600 node "$here/prepare.mjs" "$tmp/binaryen/package/index.js" "$tmp/occt-import-js/package/dist/occt-import-js.wasm" "$out/occt-import-js.wasm"
# The licenses, for a bundle that carries the modules.
cp "$tmp/occt-import-js/package/dist/license.occt-import-js.txt" "$tmp/occt-import-js/package/dist/license.occt.txt" "$out/"
sum() { node -e 'process.stdout.write(require("crypto").createHash("sha256").update(require("fs").readFileSync(process.argv[1])).digest("hex"))' "$1"; }
[ "$(sum "$out/occt-import-js.wasm")" = "$OUT_SHA256_WASM" ] && [ "$(sum "$out/occt-import-js.noeh.wasm")" = "$OUT_SHA256_NOEH" ] ||
  echo "build.sh: note: the modules differ from the ones recorded (OUT_SHA256_*); update the record if prepare.mjs changed" >&2
ls -l "$out"
