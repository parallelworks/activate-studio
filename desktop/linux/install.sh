#!/bin/bash
# Installs ACTIVATE Studio for the current user, with no administrator rights:
# from the unpacked tarball (this script is at its top),
#
#   tar -xzf activate-studio-linux-x86_64.tar.gz
#   activate-studio/install.sh
#
# The program goes to ~/.local/lib/activate-studio (a real directory, the
# same path for every version: the Studio's updater replaces it in place and
# keeps the old copy beside it as .activate-studio.previous), a command at
# ~/.local/bin/activate-studio, and a menu entry and icons under
# ~/.local/share. The file install-kind in that directory says "user", which
# is what lets the Studio update itself there. Your documents, index and
# settings are not touched; they stay in ~/.local/share/activate-studio-data
# and your documents folder.
#
#   install.sh            copy this tree, and leave it where it is
#   install.sh --move     move it instead of copying (no second copy on the disk)
#
# Needs WebKitGTK for the Studio's own window (libwebkit2gtk-4.1-0 on Ubuntu 22.04 and
# later, webkit2gtk3 on RHEL 8 and 9 and their rebuilds, libwebkit2gtk-4.0-37 on Debian 11); without it the
# Studio opens in the default browser. A system package manager installs that;
# this script installs nothing outside your home folder.
#
# STUDIO_INSTALL_DIR overrides the install folder (a test setting).
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
case "$(cut -d' ' -f1 "$SRC/install-kind" 2>/dev/null || true)" in deb|rpm) echo "this tree belongs to a package; nothing to install" >&2; exit 1 ;; esac

VERSION="$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$SRC/manifest.json" | head -n 1)"
[ -n "$VERSION" ] || { echo "no version in manifest.json" >&2; exit 1; }
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
TREE="${STUDIO_INSTALL_DIR:-$HOME/.local/lib/activate-studio}"
PARENT="$(dirname "$TREE")"
NAME="$(basename "$TREE")"
BIN_DIR="$HOME/.local/bin"
APPS_DIR="$DATA_HOME/applications"
ICON_DIR="$DATA_HOME/icons/hicolor"
[ "$SRC" != "$TREE" ] || { echo "this is the installed copy already" >&2; exit 1; }

mkdir -p "$PARENT" "$BIN_DIR" "$APPS_DIR"
# Something running from the copy about to be replaced is stopped first.
if [ -x "$TREE/bin/activate-studio" ]; then STUDIO_TREE="$TREE" "$TREE/bin/activate-studio" --stop >/dev/null 2>&1 || true; fi

echo "Installing ACTIVATE Studio $VERSION in $TREE ..."
NEW="$PARENT/.$NAME.new"
chmod -R u+w "$NEW" 2>/dev/null || true
rm -rf "$NEW"
if [ "$MOVE" = 1 ] && mv "$SRC" "$NEW" 2>/dev/null; then :; else cp -a "$SRC" "$NEW"; fi
# The marker the Studio's updater reads (docs/LINUX-APP.md, Updates).
printf 'user\n' >"$NEW/install-kind"
# An earlier install is replaced; its copy is kept until the new one is in
# place, then removed (an update, not this script, keeps a .previous).
OLD=
if [ -e "$TREE" ]; then OLD="$PARENT/.$NAME.old"; chmod -R u+w "$OLD" 2>/dev/null || true; rm -rf "$OLD"; mv "$TREE" "$OLD"; fi
mv "$NEW" "$TREE"
if [ -n "$OLD" ]; then chmod -R u+w "$OLD" 2>/dev/null || true; rm -rf "$OLD"; fi

# The command: a small script that names the install's path, which an update
# keeps (it replaces the directory, not this file).
cat >"$BIN_DIR/activate-studio" <<WRAPPER
#!/bin/sh
# ACTIVATE Studio command (written by install.sh; uninstall removes it).
STUDIO_TREE="$TREE"
export STUDIO_TREE
# An update that was cut off between its two directory moves leaves no
# payload at its path; this is the one entry point outside it, so it asks the
# recovery script of the staged or the previous copy beside it to finish the
# update or put the previous payload back (docs/LINUX-APP.md, Updates).
if [ ! -f "\$STUDIO_TREE/studio-run.sh" ]; then
  beside="\$(dirname "\$STUDIO_TREE")/.\$(basename "\$STUDIO_TREE")"
  for copy in "\$beside.new" "\$beside.previous"; do
    if [ -f "\$copy/studio-recover.sh" ]; then
      /bin/bash "\$copy/studio-recover.sh" "\$STUDIO_TREE" "\${STUDIO_DATA_DIR:-\${XDG_DATA_HOME:-\$HOME/.local/share}/activate-studio-data}" >/dev/null 2>&1
      break
    fi
  done
  # A payload that predates the script: put the previous one back.
  [ -f "\$STUDIO_TREE/studio-run.sh" ] || [ ! -d "\$beside.previous" ] || mv "\$beside.previous" "\$STUDIO_TREE" 2>/dev/null
fi
exec "\$STUDIO_TREE/bin/activate-studio" "\$@"
WRAPPER
chmod 755 "$BIN_DIR/activate-studio"

# The menu entry and icons.
exec_path="$BIN_DIR/activate-studio"
case "$exec_path" in *" "*) exec_path="\"$exec_path\"" ;; esac
sed "s|@BIN@|$exec_path|g" "$TREE/share/applications/activate-studio.desktop" >"$APPS_DIR/activate-studio.desktop"
if [ -d "$TREE/share/icons/hicolor" ]; then
  (cd "$TREE/share/icons/hicolor" && find . -name activate-studio.png) | while IFS= read -r f; do
    mkdir -p "$ICON_DIR/$(dirname "$f")"
    cp "$TREE/share/icons/hicolor/$f" "$ICON_DIR/$f"
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
