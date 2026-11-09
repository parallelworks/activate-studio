#!/usr/bin/env bash
# The deployment bundle for the ACTIVATE workflow (deploy/workflow.yaml,
# Bundle mode): the Studio's Linux payload, activate-studio-linux-x86_64.tar.gz,
# as the Linux release ships it. It carries the Go server, the built web app,
# GUFI, tesseract, and the ONNX runtime and model, with no Node and no
# Python, so the resource needs no outbound network and builds nothing.
#
# Usage: deploy/make_bundle.sh [--release TAG | --from FILE] [output.tar.gz]
#   --release TAG  that release's payload, downloaded from GitHub and checked
#                  against its .sha256 (default: the checkout's release tag,
#                  git describe --tags --match 'v*' --abbrev=0)
#   --from FILE    a payload already here, such as deploy/linux/build_app.sh's
#                  deploy/linux/out/activate-studio-linux-x86_64.tar.gz
# The output defaults to studio-bundle.tar.gz at the repository root.
set -euo pipefail
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO=parallelworks/activate-studio
ASSET=activate-studio-linux-x86_64.tar.gz
TAG=""
FROM=""
while [ $# -gt 0 ]; do
  case "$1" in
    --release) TAG="${2:?--release needs a tag}"; shift 2 ;;
    --from) FROM="${2:?--from needs a file}"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) break ;;
  esac
done
OUT="${1:-$PROJECT_ROOT/studio-bundle.tar.gz}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ -z "$FROM" ]; then
  [ -n "$TAG" ] || TAG="$(cd "$PROJECT_ROOT" && git describe --tags --match 'v*' --abbrev=0)"
  URL="https://github.com/$REPO/releases/download/$TAG/$ASSET"
  echo "Downloading $URL ..."
  curl -fsSL --retry 3 -o "$STAGE/$ASSET" "$URL"
  curl -fsSL --retry 3 -o "$STAGE/$ASSET.sha256" "$URL.sha256"
  (cd "$STAGE" && sha256sum -c "$ASSET.sha256")
  FROM="$STAGE/$ASSET"
fi
[ -f "$FROM" ] || { echo "no such file: $FROM" >&2; exit 1; }
# The workflow takes only the Go version's payload.
tar -tzf "$FROM" activate-studio/app/bin/studio-server >/dev/null 2>&1 ||
  { echo "$FROM is not the Studio's Linux payload with the Go server (activate-studio/app/bin/studio-server)" >&2; exit 1; }
tar -xzOf "$FROM" activate-studio/app/build-info.json 2>/dev/null | tr -d '\n' && echo
cp "$FROM" "$OUT.tmp"
mv "$OUT.tmp" "$OUT"
ls -lh "$OUT"
echo "Upload with: pw buckets cp $OUT <bucket-uri>/studio-bundle.tar.gz"
