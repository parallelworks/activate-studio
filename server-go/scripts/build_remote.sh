#!/usr/bin/env bash
# The Studio's helper for library builds on other systems (cmd/studio-remote),
# for each platform it is sent to, as <out>/<platform>/studio-remote. The
# packages put it in app/bin/remote, beside the server; a checkout's server
# finds it in server-go/bin/remote (the default here).
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$here/bin/remote}"
for p in linux-amd64 linux-arm64; do
  mkdir -p "$out/$p"
  (cd "$here" && CGO_ENABLED=0 GOOS="${p%-*}" GOARCH="${p#*-}" go build -trimpath -ldflags "-s -w" \
    -o "$out/$p/studio-remote" ./cmd/studio-remote)
done
