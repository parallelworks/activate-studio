#!/usr/bin/env bash
# The Studio's helper for library builds on other systems (cmd/studio-remote),
# for each platform it is sent to, as <out>/<platform>/studio-remote, and on
# Linux the one that writes in a library there (cmd/studio-write), beside it
# as studio-write. The packages put them in app/bin/remote, beside the
# server; a checkout's server finds them in server-go/bin/remote (the
# default here).
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$here/bin/remote}"
for p in linux-amd64 linux-arm64 darwin-arm64; do
  mkdir -p "$out/$p"
  (cd "$here" && CGO_ENABLED=0 GOOS="${p%-*}" GOARCH="${p#*-}" go build -trimpath -ldflags "-s -w" \
    -o "$out/$p/studio-remote" ./cmd/studio-remote)
  case "$p" in linux-*)
    (cd "$here" && CGO_ENABLED=0 GOOS=linux GOARCH="${p#*-}" go build -trimpath -ldflags "-s -w" \
      -o "$out/$p/studio-write" ./cmd/studio-write) ;;
  esac
done
