#!/bin/bash
# Install ACTIVATE Studio on a Mac with Apple silicon, for the current user
# (no administrator rights):
#
#   curl -fsSL https://github.com/parallelworks/activate-studio/releases/latest/download/install.sh | bash
#
# or from an archive already on disk, with its .sha256 beside it:
#
#   bash install.sh /path/to/activate-studio-macos-arm64.tar.gz
#
# It downloads the app (STUDIO_URL, or the archive given), checks it against
# its published SHA-256, puts it in ~/Applications (STUDIO_INSTALL_DIR),
# and opens it once. A download made by curl carries no quarantine flag, so
# macOS opens the unsigned app without the blocked-app prompt; any flag that
# is there is removed. STUDIO_NO_OPEN=1 installs without opening it.
set -euo pipefail

STUDIO_URL="${STUDIO_URL:-https://github.com/parallelworks/activate-studio/releases/latest/download/activate-studio-macos-arm64.tar.gz}"
DEST_DIR="${STUDIO_INSTALL_DIR:-$HOME/Applications}"
APP_NAME="ACTIVATE Studio.app"
LABEL=com.parallelworks.activate-studio
SOURCE="${1:-$STUDIO_URL}"

fail() { echo "install: $*" >&2; exit 1; }

if [ "${STUDIO_INSTALL_ANY_HOST:-}" != 1 ]; then
  [ "$(uname -s)" = Darwin ] || fail "ACTIVATE Studio.app is for macOS."
  [ "$(uname -m)" = arm64 ] || fail "this build is for Apple silicon (arm64); this Mac is $(uname -m)."
fi

sha256() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1"; else sha256sum "$1"; fi | awk '{print $1}'
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/activate-studio.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

archive="$WORK/studio.tar.gz"
case "$SOURCE" in
  http://*|https://*)
    echo "Downloading $SOURCE"
    curl -fL --progress-bar -o "$archive" "$SOURCE" || fail "download failed: $SOURCE"
    curl -fsSL -o "$archive.sha256" "$SOURCE.sha256" || fail "no checksum at $SOURCE.sha256"
    ;;
  *)
    [ -f "$SOURCE" ] || fail "no such archive: $SOURCE"
    [ -f "$SOURCE.sha256" ] || fail "no checksum beside the archive ($SOURCE.sha256)"
    cp "$SOURCE" "$archive"
    cp "$SOURCE.sha256" "$archive.sha256"
    ;;
esac

want="$(awk '{print $1; exit}' "$archive.sha256")"
got="$(sha256 "$archive")"
[ -n "$want" ] && [ "$want" = "$got" ] || fail "checksum mismatch (expected $want, got $got)"
echo "Checksum verified."

mkdir -p "$WORK/unpacked" "$DEST_DIR"
tar -xzf "$archive" -C "$WORK/unpacked"
[ -x "$WORK/unpacked/$APP_NAME/Contents/MacOS/ACTIVATE Studio" ] || fail "the archive does not hold $APP_NAME"

# Stop a running copy before replacing it; the app loads it again.
if [ "$(uname -s)" = Darwin ]; then
  launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
fi
target="$DEST_DIR/$APP_NAME"
staged="$DEST_DIR/.$APP_NAME.new"
rm -rf "$staged"
mv "$WORK/unpacked/$APP_NAME" "$staged"
if [ -e "$target" ]; then
  rm -rf "$target.old"
  mv "$target" "$target.old"
fi
mv "$staged" "$target"
rm -rf "$target.old"
if command -v xattr >/dev/null 2>&1; then
  xattr -dr com.apple.quarantine "$target" 2>/dev/null || true
fi
echo "Installed $target"

if [ "${STUDIO_NO_OPEN:-}" = 1 ]; then
  echo "Open it from $DEST_DIR, or run: open \"$target\""
  exit 0
fi
# On a Mac, open it as an app (its own window); elsewhere, as in tests, run it.
if [ "$(uname -s)" = Darwin ] && command -v open >/dev/null 2>&1; then
  open "$target"
else
  "$target/Contents/MacOS/ACTIVATE Studio"
fi
