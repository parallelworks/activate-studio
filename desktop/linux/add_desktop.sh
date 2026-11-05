#!/usr/bin/env bash
# Adds the desktop pieces to a payload tree (the activate-studio/ folder
# build_app.sh makes): the two window shells, the launcher, the install and
# uninstall scripts, the menu entry template and the icons in the sizes a
# desktop looks for. Run by build_app.sh when SHELL_DIR is set.
#
# Usage: add_desktop.sh TREE SHELL_DIR   (SHELL_DIR holds activate-studio-wk41
#                                         and activate-studio-wk40)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP="$(cd "${1:?usage: add_desktop.sh TREE SHELL_DIR}" && pwd)"
SHELLS="$(cd "${2:?usage: add_desktop.sh TREE SHELL_DIR}" && pwd)"

mkdir -p "$APP/bin" "$APP/share/applications"
for v in wk41 wk40; do
  [ -f "$SHELLS/activate-studio-$v" ] || { echo "no $SHELLS/activate-studio-$v" >&2; exit 1; }
  install -m 755 "$SHELLS/activate-studio-$v" "$APP/bin/activate-studio-$v"
done
install -m 755 "$HERE/activate-studio" "$APP/bin/activate-studio"
install -m 755 "$HERE/install.sh" "$HERE/uninstall.sh" "$APP/"
install -m 644 "$HERE/activate-studio.desktop" "$APP/share/applications/activate-studio.desktop"

# Icons from the 1024-pixel PNG build_app.sh put in share/.
if command -v magick >/dev/null 2>&1; then IM=(magick); elif command -v convert >/dev/null 2>&1; then IM=(convert); else
  echo "ImageMagick (magick or convert) is needed to make the icon sizes" >&2; exit 1
fi
for s in 48 64 128 256 512; do
  d="$APP/share/icons/hicolor/${s}x${s}/apps"
  mkdir -p "$d"
  "${IM[@]}" "$APP/share/activate-studio.png" -filter Lanczos -resize "${s}x${s}" "$d/activate-studio.png"
done
echo "Desktop pieces added to $APP"
