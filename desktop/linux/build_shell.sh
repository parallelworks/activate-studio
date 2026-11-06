#!/usr/bin/env bash
# Builds the Linux window shell (desktop/wails) for one WebKitGTK API, inside
# a container of the distribution that has its development packages, as root
# (the CI workflow runs it so). The binary links to the WebKitGTK, GTK and
# libsoup of the computer it runs on, so it is built on the oldest
# distribution that has the API, which also fixes the oldest glibc it needs:
#
#   wk41  WebKitGTK 4.1 (libsoup 3): Ubuntu 22.04 and later, Debian 12 and
#         later, Fedora. Built on Ubuntu 22.04 (glibc 2.35).
#   wk40  WebKitGTK 4.0 (libsoup 2): RHEL 8 and 9 and their rebuilds,
#         Debian 11. Built on Rocky Linux 8 (glibc 2.28).
#
# Usage: build_shell.sh wk41|wk40
# Env:   SRC (the repository, default: this script's), OUT_DIR (default /out),
#        VERSION (shown by --version), CACHE_DIR (Go's caches, default /cache)
set -euo pipefail

VARIANT="${1:?usage: build_shell.sh wk41|wk40}"
SRC="${SRC:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
OUT_DIR="${OUT_DIR:-/out}"
CACHE_DIR="${CACHE_DIR:-/cache}"
VERSION="${VERSION:-dev}"
GO_VERSION=1.26.0
GO_SHA256=aac1b08a0fb0c4e0a7c1555beb7b59180b05dfc5a3d62e40e9de90cd42f88235

case "$VARIANT" in
  wk41) TAGS=desktop,production,webkit2_41 ;;
  wk40) TAGS=desktop,production ;;
  *) echo "unknown variant $VARIANT" >&2; exit 2 ;;
esac

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq >/dev/null
  case "$VARIANT" in wk41) dev=libwebkit2gtk-4.1-dev ;; *) dev=libwebkit2gtk-4.0-dev ;; esac
  apt-get install -y -qq --no-install-recommends ca-certificates curl gcc pkg-config libgtk-3-dev "$dev" >/dev/null
else
  dnf -y -q install epel-release dnf-plugins-core >/dev/null
  # The repository with the development packages: crb from RHEL 9 on,
  # powertools on RHEL 8.
  dnf config-manager --set-enabled crb 2>/dev/null || dnf config-manager --set-enabled powertools
  dnf -y -q install gcc pkgconf-pkg-config tar gzip webkit2gtk3-devel gtk3-devel >/dev/null
fi

if ! { command -v go >/dev/null 2>&1 && [ "$(go env GOVERSION)" = "go$GO_VERSION" ]; }; then
  curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz"
  echo "$GO_SHA256  /tmp/go.tgz" | sha256sum -c - >/dev/null
  tar -xzf /tmp/go.tgz -C /usr/local
  rm -f /tmp/go.tgz
fi
export PATH=/usr/local/go/bin:$PATH GOTOOLCHAIN=local GOFLAGS=-buildvcs=false \
  GOCACHE="$CACHE_DIR/go-build" GOMODCACHE="$CACHE_DIR/mod"

mkdir -p "$OUT_DIR"
cd "$SRC/desktop/wails"
go vet -tags "$TAGS" ./...
go test -tags "$TAGS" -count=1 ./...
go build -tags "$TAGS" -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT_DIR/activate-studio-$VARIANT" .
echo "Built $OUT_DIR/activate-studio-$VARIANT ($(du -h "$OUT_DIR/activate-studio-$VARIANT" | cut -f1)), $(ldd --version | head -n 1)"
ldd "$OUT_DIR/activate-studio-$VARIANT" | grep -E 'webkit|soup|javascriptcore' || true
