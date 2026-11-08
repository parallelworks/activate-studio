#!/bin/bash
# Finishes or undoes an update of the payload that was cut off. An update
# replaces the payload's directory in two moves (the running payload to
# .<name>.previous, then the new one, staged as .<name>.new, into its place),
# so a process that dies between them leaves no payload at its path. The same
# holds for a rollback, which also takes two moves. This puts the install back
# the way the cut-off step was heading, and is safe to run when nothing is
# wrong (it then does nothing).
#
#   studio-recover.sh TREE DATA_DIR
#
# TREE is the payload's path (for a per-user install, ~/.local/lib/activate-studio),
# DATA_DIR the Studio's data directory. The script does not use its own
# location, so it can be run from .new or .previous when the payload at TREE
# is gone; studio-run.sh reads it in as the function studio_recover before
# it needs it, so that its running loop can recover after the directory
# holding this file has moved.
#
# The server writes DATA/updates/swap.json before the first move and removes
# it after the last. With the payload gone:
#   - the swap file names a version and .new holds that version, complete
#     (the files the server checks before it swaps are all there, and the
#     version is the one named): .new is moved into place and the update is
#     recorded as pending, as if the server had finished, so the run loop's
#     rollback after three quick stops still applies to it;
#   - otherwise .previous is moved back, and updates/rolled-back.json says
#     the update was interrupted. A .new without a swap file is never used:
#     the server only swaps what it has verified, and a staged copy that has
#     not been is not promoted.
# With the payload there and a swap file left over, the swap file is removed,
# after recording the update as pending when the payload is the version it
# names.
#
# Exit status: 0 nothing needed doing, 10 the payload at TREE was put back or
# completed (a caller that is the old payload's run loop should start the one
# now at TREE), 1 the payload is gone and nothing could restore it.

_rec_stamp() { date '+%Y-%m-%d %H:%M:%S'; }

_rec_version() { sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$1/app/build-info.json" 2>/dev/null | head -n 1; }

_rec_field() { sed -n "s/.*\"$1\": *\"\([^\"]*\)\".*/\1/p" "$2" 2>/dev/null | head -n 1; }

# The files the server checks in a new payload before it swaps it in, and the
# version it was unpacked for.
_rec_complete() {
  local d="$1" want="$2" have
  [ -x "$d/studio-run.sh" ] || return 1
  # A server to run: Go's, or Node's (a Go payload's Node files may be
  # placeholders for older updaters).
  if [ ! -x "$d/app/bin/studio-server" ]; then
    [ -x "$d/node/bin/node" ] && [ -f "$d/app/server/dist/main.js" ] || return 1
  fi
  local f
  for f in update-signing.pub manifest.json; do [ -f "$d/$f" ] || return 1; done
  have="$(_rec_version "$d")"
  [ -n "$have" ] && [ "${have#v}" = "${want#v}" ]
}

_rec_json() {
  # _rec_json FILE key value ...
  local file="$1" tmp sep="" k v
  shift
  tmp="$file.$$.tmp"
  mkdir -p "$(dirname "$file")" 2>/dev/null
  {
    printf '{\n'
    while [ $# -ge 2 ]; do
      k="$1" v="$2"
      shift 2
      printf '%s "%s": "%s"' "$sep" "$k" "$v"
      sep=$',\n'
    done
    printf '\n}\n'
  } >"$tmp" && mv -f "$tmp" "$file"
}

studio_recover() {
  local tree="${1:?tree}" data="${2:?data dir}"
  local parent name new prev failed upd journal log to from now have
  parent="$(dirname "$tree")"
  name="$(basename "$tree")"
  new="$parent/.$name.new"
  prev="$parent/.$name.previous"
  failed="$parent/.$name.failed"
  upd="$data/updates"
  journal="$upd/swap.json"
  log="$data/logs/studio.log"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  to="$(_rec_field to "$journal")"
  from="$(_rec_field from "$journal")"
  _rec_say() { mkdir -p "$data/logs" 2>/dev/null; echo "$(_rec_stamp) $*" >>"$log"; echo "$*"; }

  if [ ! -e "$tree" ]; then
    # A server that is swapping right now has the payload at no path for a
    # moment; give it a couple of seconds to put the new one in place.
    local n=0
    while [ ! -e "$tree" ] && [ "$n" -lt "${STUDIO_RECOVER_WAIT:-10}" ]; do sleep 0.2; n=$((n + 1)); done
    [ -e "$tree" ] && { [ -f "$journal" ] || return 0; }
  fi

  if [ ! -e "$tree" ]; then
    if [ -f "$journal" ] && [ -n "$to" ] && _rec_complete "$new" "$to" && mv "$new" "$tree" 2>/dev/null; then
      _rec_json "$upd/pending.json" from "${from:-unknown}" to "$to" at "$now"
      rm -f "$upd/rolled-back.json"
      [ "$(_rec_field reopen "$journal")" = yes ] && echo "$to" >"$upd/reopen"
      rm -f "$journal"
      _rec_say "the update to $to was cut off between its two moves; it is in place now"
      return 10
    fi
    if [ -d "$prev" ] && mv "$prev" "$tree" 2>/dev/null; then
      local lost="${to:-$(_rec_version "$failed")}"
      local back
      back="$(_rec_version "$tree")"
      _rec_json "$upd/rolled-back.json" from "${lost:-unknown}" to "${back:-unknown}" at "$now" \
        reason "the update was interrupted before it finished"
      rm -f "$journal" "$upd/pending.json"
      _rec_say "the payload was missing after an interrupted update; ${back:-the previous version} is back"
      return 10
    fi
    _rec_say "the payload at $tree is missing and there is nothing to restore it from"
    return 1
  fi

  if [ -f "$journal" ]; then
    # The payload is there. If it is the new version, the last step before
    # the swap file's removal was cut off; if it is the old one, the swap
    # never happened or was undone.
    have="$(_rec_version "$tree")"
    if [ -n "$to" ] && [ -n "$have" ] && [ "${have#v}" = "${to#v}" ]; then
      if [ ! -f "$upd/pending.json" ]; then
        _rec_json "$upd/pending.json" from "${from:-unknown}" to "$to" at "$now"
        rm -f "$upd/rolled-back.json"
        [ "$(_rec_field reopen "$journal")" = yes ] && echo "$to" >"$upd/reopen"
        _rec_say "the update to $to was cut off before it was recorded; it is recorded now"
      fi
    fi
    rm -f "$journal"
  fi
  return 0
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -u
  [ $# -eq 2 ] || { echo "usage: studio-recover.sh TREE DATA_DIR" >&2; exit 2; }
  studio_recover "$1" "$2"
  exit $?
fi
