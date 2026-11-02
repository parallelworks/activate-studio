#!/usr/bin/env bash
# Make the disk image for "ACTIVATE Studio.app": the app beside a short note
# on installing it, on a volume named after the app that shows the app's
# icon, compressed (UDZO). There is no link to /Applications to drag it to:
# the app is per user (its data, server and LaunchAgent are the user's), and
# double-clicking it on the image installs it in ~/Applications, which needs
# no administrator rights and where it updates itself (launcher.sh
# --install). Needs macOS (hdiutil). build_app.sh
# makes one of the ad hoc signed app; the release's signing job makes one
# of the app it signed, and notarize.sh signs and notarizes it.
#
# Usage: deploy/mac/make_dmg.sh APP OUT.dmg
set -euo pipefail

fail() { echo "make_dmg: $*" >&2; exit 1; }

[ $# -eq 2 ] || fail "usage: make_dmg.sh APP OUT.dmg"
[ -f "$1/Contents/Info.plist" ] || fail "$1 is not an app bundle"
command -v hdiutil >/dev/null 2>&1 || fail "hdiutil is needed (macOS)"
APP="$(cd "$1" && pwd -P)"
NAME="$(basename "$APP" .app)"
mkdir -p "$(dirname "$2")"
OUT="$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/make-dmg.XXXXXX")"
cleanup() {
  if [ -d "$WORK/mnt" ]; then hdiutil detach -force "$WORK/mnt" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# Whether a volume is mounted at a path, by its real path as mount(8) prints
# it (/private/var/..., where mktemp gave /var/...).
mounted() {
  local p
  p="$(cd "$1" 2>/dev/null && pwd -P)" || return 1
  # All of mount's output is read, so pipefail never sees a SIGPIPE.
  mount | grep -F " on $p (" >/dev/null
}

# hdiutil fails now and then on CI runners ("Resource busy", mostly on
# detach while the system still holds the new volume): each call is tried
# a few times, a detach is forced last, and the message shows if it gives up.
hdi() {
  local n out
  for n in 1 2 3 4 5; do
    if out="$(hdiutil "$@" 2>&1)"; then return 0; fi
    # A detach can report failure and still go through, and the next try
    # then finds nothing there ("No such file or directory"): it is done
    # once nothing is mounted at the mount point.
    if [ "$1" = detach ] && ! mounted "${@: -1}"; then return 0; fi
    sleep "$((n * 2))"
  done
  if [ "$1" = detach ] && out="$(hdiutil detach -force "${@: -1}" 2>&1)"; then return 0; fi
  echo "make_dmg: hdiutil $1 failed: $out" >&2
  return 1
}

ICON="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIconFile' "$APP/Contents/Info.plist" 2>/dev/null || true)"
ICNS=
[ -n "$ICON" ] && ICNS="$APP/Contents/Resources/${ICON%.icns}.icns"
[ -z "$ICNS" ] || [ -f "$ICNS" ] || fail "the app names the icon $ICON, which it does not have"

rm -f "$OUT"
mkdir -p "$WORK/dmg"
ditto "$APP" "$WORK/dmg/$NAME.app"
cat >"$WORK/dmg/Installing $NAME.txt" <<NOTE
Installing $NAME

Double-click $NAME and choose Install. macOS may first ask whether to open an app downloaded from the internet; choose Open.

Install copies it to the Applications folder in your home folder and opens it from there. No administrator rights are needed, and it updates itself there. Spotlight finds it there, and you can keep it in the Dock.

Eject this disk image afterwards.

An administrator who prefers /Applications can drag the app there instead. That copy updates itself only for users who can write to /Applications.
NOTE
if [ -n "$ICNS" ]; then
  # The volume's own icon, which Finder shows in the window's title bar
  # and the sidebar: .VolumeIcon.icns at its root, used once the root has
  # the custom-icon Finder flag. The flag is set on the mounted volume, so
  # the image is made writable first and compressed after.
  cp "$ICNS" "$WORK/dmg/.VolumeIcon.icns"
  hdi create -volname "$NAME" -srcfolder "$WORK/dmg" -ov -format UDRW "$WORK/rw.dmg"
  mkdir -p "$WORK/mnt"
  hdi attach -nobrowse -noautoopen -mountpoint "$WORK/mnt" "$WORK/rw.dmg"
  # Finder info: 32 bytes, with kHasCustomIcon (0x0400) in the flags at byte 8.
  xattr -wx com.apple.FinderInfo 0000000000000000040000000000000000000000000000000000000000000000 "$WORK/mnt"
  hdi detach "$WORK/mnt"
  hdi convert "$WORK/rw.dmg" -format UDZO -ov -o "$OUT"
  rm -f "$WORK/rw.dmg"
  # Checked on the finished image, read only.
  hdi attach -nobrowse -noautoopen -readonly -mountpoint "$WORK/mnt" "$OUT"
  flags="$(xattr -px com.apple.FinderInfo "$WORK/mnt" 2>/dev/null | tr -d ' \n' | cut -c17-20 || true)"
  have_icon=0
  [ -f "$WORK/mnt/.VolumeIcon.icns" ] && have_icon=1
  hdi detach "$WORK/mnt"
  rmdir "$WORK/mnt"
  if [ "$have_icon" != 1 ] || [ "$((0x${flags:-0} & 0x0400))" = 0 ]; then
    fail "the disk image's volume icon did not take (icon file $have_icon, Finder flags ${flags:-none})."
  fi
else
  hdi create -volname "$NAME" -srcfolder "$WORK/dmg" -ov -format UDZO "$OUT"
fi
echo "  $OUT ($(du -h "$OUT" | cut -f1))"
