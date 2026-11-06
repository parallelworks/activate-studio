#!/bin/bash
# Removes a per-user install of ACTIVATE Studio (install.sh): the program, the
# command, the menu entry and the icons. Also at bin/activate-studio
# --uninstall.
#
#   uninstall.sh [--purge] [--dry-run]
#
# --purge also removes what the Studio kept for you: the index, settings and
# logs in ~/.local/share/activate-studio-data (or STUDIO_DATA_DIR) and the
# window's storage. --dry-run lists what would be removed and kept, one
# "kind<TAB>path" line each, and removes nothing. Your documents folder is
# never removed. A package install is removed with apt or dnf.
set -u

# This script is inside the folder it removes, so it runs from a copy.
if [ -z "${STUDIO_UNINSTALL_COPY:-}" ]; then
  tmp="$(mktemp "${TMPDIR:-/tmp}/studio-uninstall.XXXXXX")" || exit 1
  cp "${BASH_SOURCE[0]}" "$tmp"
  STUDIO_UNINSTALL_COPY=1 STUDIO_UNINSTALL_TREE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" exec /bin/bash "$tmp" "$@"
fi
trap 'rm -f "$0"' EXIT

PURGE=0 DRY=0
for a in "$@"; do
  case "$a" in
    --purge) PURGE=1 ;;
    --dry-run) DRY=1 ;;
    -h|--help) echo "usage: uninstall.sh [--purge] [--dry-run]"; exit 0 ;;
    *) echo "unknown option $a" >&2; exit 2 ;;
  esac
done

TREE="$STUDIO_UNINSTALL_TREE"
if [ "$(cut -d' ' -f1 "$TREE/install-kind" 2>/dev/null || true)" != user ]; then
  echo "$TREE is not a per-user install (its install-kind is not 'user'); nothing removed." >&2
  exit 1
fi
PARENT="$(dirname "$TREE")"
NAME="$(basename "$TREE")"
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
DATA="${STUDIO_DATA_DIR:-$DATA_HOME/activate-studio-data}"
BIN="$HOME/.local/bin/activate-studio"
ENTRY="$DATA_HOME/applications/activate-studio.desktop"
ICON_DIR="$DATA_HOME/icons/hicolor"

remove=() keep=()
add() { remove+=("$1	$2"); }
add program "$TREE"
# What an update leaves beside the program: the copy it replaced, for a
# rollback, and its working and failed copies.
for suffix in previous new failed old; do
  [ -e "$PARENT/.$NAME.$suffix" ] && add "previous copy" "$PARENT/.$NAME.$suffix"
done
[ -f "$BIN" ] && grep -q 'ACTIVATE Studio command' "$BIN" && add command "$BIN"
[ -f "$ENTRY" ] && add "menu entry" "$ENTRY"
for f in "$ICON_DIR"/*/apps/activate-studio.png; do
  if [ -e "$f" ]; then add icon "$f"; fi
done
# The window's web view keeps a cache and its storage (what the page saved
# in the browser's storage: theme, recent searches) in folders named for the
# program the shell gives itself (titlebar_linux.go), the same for both
# shells. The cache goes with the program, the storage with --purge.
for d in "$HOME/.cache/activate-studio" "$DATA/webview"; do
  [ -d "$d" ] && add "window cache" "$d"
done
if [ -d "$DATA_HOME/activate-studio" ]; then
  if [ "$PURGE" = 1 ]; then add "window storage" "$DATA_HOME/activate-studio"; else keep+=("window storage	$DATA_HOME/activate-studio"); fi
fi
if [ -d "$DATA" ]; then
  if [ "$PURGE" = 1 ]; then add "index, settings and logs" "$DATA"; else keep+=("index, settings and logs	$DATA"); fi
fi
keep+=("documents	(your documents folder, never removed)")

if [ "$DRY" = 1 ]; then
  for l in "${remove[@]}"; do printf 'remove\t%s\n' "$l"; done
  for l in "${keep[@]}"; do printf 'keep\t%s\n' "$l"; done
  exit 0
fi

# Stop what runs from it first.
STUDIO_TREE="$TREE" "$TREE/bin/activate-studio" --stop >/dev/null 2>&1 || true
for l in "${remove[@]}"; do
  path="${l#*	}"
  # The payload has read-only directories (Python's), which rm cannot empty.
  chmod -R u+w -- "$path" 2>/dev/null || true
  rm -rf -- "$path"
done
rmdir "$PARENT" 2>/dev/null || true
update-desktop-database "$DATA_HOME/applications" >/dev/null 2>&1 || true
gtk-update-icon-cache -q -t -f "$ICON_DIR" >/dev/null 2>&1 || true
echo "ACTIVATE Studio was removed."
if [ "$PURGE" = 0 ] && [ -d "$DATA" ]; then echo "Your index, settings and logs are still in $DATA (run again with --purge to remove them)."; fi
