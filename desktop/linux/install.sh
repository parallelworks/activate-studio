#!/bin/bash
# Installs ACTIVATE Studio for the current user, with no administrator rights:
# from the unpacked tarball (this script is at its top),
#
#   tar -xzf activate-studio-linux-x86_64.tar.gz
#   activate-studio/install.sh
#
# The program goes to ~/.local/share/activate-studio/<version>, with a link
# named current to it, a command at ~/.local/bin/activate-studio, and a menu
# entry and icons under ~/.local/share. The Studio updates itself there (a
# new version goes beside the old one and current moves to it). Your
# documents, index and settings are not touched; they stay in
# ~/.local/share/activate-studio-data and your documents folder.
#
#   install.sh            copy this tree, and leave it where it is
#   install.sh --move     move it instead of copying (no second copy on the disk)
#
# Needs WebKitGTK for the Studio's own window (libwebkit2gtk-4.1-0 on Ubuntu 22.04 and
# later, webkit2gtk3 on RHEL 9 and its rebuilds, libwebkit2gtk-4.0-37 on Debian 11); without it the
# Studio opens in the default browser. A system package manager installs that;
# this script installs nothing outside your home folder.
#
# STUDIO_INSTALL_ROOT overrides the install folder (a test setting).
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
MOVE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --move) MOVE=1 ;;
    -h|--help) sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
[ -f "$SRC/studio-run.sh" ] && [ -f "$SRC/manifest.json" ] || { echo "run this from the unpacked activate-studio folder" >&2; exit 1; }
if [ "$(id -u)" = 0 ]; then
  echo "This installs for one user, in their home folder. Run it as that user; a system-wide install is the .deb or .rpm package." >&2
  exit 1
fi
[ -f "$SRC/PACKAGE-MANAGED" ] && { echo "this tree belongs to a package; nothing to install" >&2; exit 1; }

VERSION="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$SRC/manifest.json" | head -n 1)"
[ -n "$VERSION" ] || { echo "no version in manifest.json" >&2; exit 1; }
# A folder name: the version as the manifest has it, made safe.
DIR="$(printf '%s' "$VERSION" | tr -c 'A-Za-z0-9._+~-' '_')"
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
ROOT="${STUDIO_INSTALL_ROOT:-$DATA_HOME/activate-studio}"
BIN_DIR="$HOME/.local/bin"
APPS_DIR="$DATA_HOME/applications"
ICON_DIR="$DATA_HOME/icons/hicolor"

mkdir -p "$ROOT" "$BIN_DIR" "$APPS_DIR"
# Something running from the folder about to be replaced is stopped first.
if [ -x "$ROOT/current/bin/activate-studio" ]; then "$ROOT/current/bin/activate-studio" --stop >/dev/null 2>&1 || true; fi

echo "Installing ACTIVATE Studio $VERSION in $ROOT/$DIR ..."
NEW="$ROOT/.$DIR.new"
rm -rf "$NEW"
if [ "$MOVE" = 1 ] && mv "$SRC" "$NEW" 2>/dev/null; then :; else cp -a "$SRC" "$NEW"; fi
# Installing the same version again replaces it; the old copy is kept until
# the link has moved.
OLD=
if [ -e "$ROOT/$DIR" ]; then OLD="$ROOT/.$DIR.old"; rm -rf "$OLD"; mv "$ROOT/$DIR" "$OLD"; fi
mv "$NEW" "$ROOT/$DIR"
ln -sfn "$DIR" "$ROOT/.current.new"
mv -T "$ROOT/.current.new" "$ROOT/current"
[ -z "$OLD" ] || rm -rf "$OLD"

# The marker the updater and the launcher read (docs/LINUX-APP.md, "Install
# layout").
cat >"$ROOT/install.json.new" <<JSON
{
 "layout": 1,
 "kind": "user",
 "installer": "install.sh",
 "current": "current",
 "version": "$VERSION",
 "installedAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
JSON
mv -f "$ROOT/install.json.new" "$ROOT/install.json"

# The command: a small script, so the tree it runs is named by the link and
# an update is picked up without touching this file.
cat >"$BIN_DIR/activate-studio" <<WRAPPER
#!/bin/sh
# ACTIVATE Studio command (written by install.sh; uninstall removes it).
STUDIO_TREE="$ROOT/current"
export STUDIO_TREE
exec "\$STUDIO_TREE/bin/activate-studio" "\$@"
WRAPPER
chmod 755 "$BIN_DIR/activate-studio"

# The menu entry and icons.
exec_path="$BIN_DIR/activate-studio"
case "$exec_path" in *" "*) exec_path="\"$exec_path\"" ;; esac
sed "s|@BIN@|$exec_path|g" "$ROOT/current/share/applications/activate-studio.desktop" >"$APPS_DIR/activate-studio.desktop"
if [ -d "$ROOT/current/share/icons/hicolor" ]; then
  (cd "$ROOT/current/share/icons/hicolor" && find . -name activate-studio.png) | while IFS= read -r f; do
    mkdir -p "$ICON_DIR/$(dirname "$f")"
    cp "$ROOT/current/share/icons/hicolor/$f" "$ICON_DIR/$f"
  done
fi
update-desktop-database "$APPS_DIR" >/dev/null 2>&1 || true
gtk-update-icon-cache -q -t -f "$ICON_DIR" >/dev/null 2>&1 || true

echo
echo "Installed. Open ACTIVATE Studio from your applications menu, or run:"
case ":$PATH:" in
  *":$BIN_DIR:"*) echo "  activate-studio" ;;
  *) echo "  $BIN_DIR/activate-studio     ($BIN_DIR is not on your PATH yet)" ;;
esac
have=0
for l in libwebkit2gtk-4.1.so.0 libwebkit2gtk-4.0.so.37; do
  for d in /usr/lib/x86_64-linux-gnu /usr/lib64 /usr/lib /usr/local/lib; do [ -e "$d/$l" ] && have=1; done
done
if [ "$have" = 0 ]; then
  echo
  echo "WebKitGTK is not installed, so the Studio will open in your default browser. For its own window, ask your administrator to install"
  echo "libwebkit2gtk-4.1-0 (Ubuntu, Debian) or webkit2gtk3 (RHEL, Rocky, Alma)."
fi
if [ "$MOVE" = 0 ]; then echo; echo "The unpacked folder $SRC is no longer needed; you can delete it."; fi
