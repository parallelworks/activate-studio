#!/bin/sh
# Every package must compile and vet for each platform the desktop app
# targets, whatever the host: go vet type-checks the tests too.
set -eu
cd "$(dirname "$0")/.."
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	echo "go vet $target"
	GOOS=${target%/*} GOARCH=${target#*/} go vet ./...
done
