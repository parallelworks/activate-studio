package transfers

import (
	"regexp"
	"strings"
)

// LooseSh defines loose, which says how another account could change a
// folder $1 on the way to a write, as the account running it: owner (it
// belongs to another account), other (every account can write in it), group
// (its group can), acl (its ACL lets another account or group), or unknown;
// nothing where none can. root owning a folder above is no finding, and
// neither is a sticky one, whose entries only their owners can move; with
// $2 self, for the folder written in itself, both are. The accounts and
// groups in LOOSE_OK, as ACL entries name them (user:bob group:team), are
// ones its owner shared it with for writing.
const LooseSh = `lz_uid=$(id -u); lz_user=$(id -un)
if stat -c %u / >/dev/null 2>&1; then lz_stat() { stat -c "$1" -- "$3" 2>/dev/null; }; else lz_stat() { stat -f "$2" -- "$3" 2>/dev/null; }; fi
loose() {
  local o m g mm a
  o=$(lz_stat %u %u "$1"); m=$(lz_stat %a %Mp%Lp "$1"); g=$(lz_stat %G %Sg "$1")
  if [ -z "$o" ] || [ -z "$m" ]; then echo unknown; return 0; fi
  if [ "$o" != "$lz_uid" ] && { [ "$o" != 0 ] || [ "${2:-}" = self ]; }; then echo owner; return 0; fi
  mm=$((8#$m))
  [ "${2:-}" != self ] && [ $((mm & 512)) -ne 0 ] && return 0
  if [ $((mm & 2)) -ne 0 ]; then echo other; return 0; fi
  [ $((mm & 16)) -ne 0 ] || return 0
  # With an ACL the group bits are its mask, not the group's own.
  if [ "$(ls -ld -- "$1" 2>/dev/null | cut -c11)" = + ] && a=$(getfacl -cpE -- "$1" 2>/dev/null) && [ -n "$a" ]; then
    printf '%s\n' "$a" | while IFS= read -r e; do
      case "$e" in *:?w?) ;; *) continue ;; esac
      e=${e%:*}
      case "$e" in
      default:*|user:|mask:|other:|"user:$lz_user") ;;
      group:) [ "$g" = "$lz_user" ] || { echo group; break; } ;;
      *) case " ${LOOSE_OK:-} " in *" $e "*) ;; *) echo acl; break ;; esac ;;
      esac
    done
    return 0
  fi
  [ "$g" = "$lz_user" ] || echo group
  return 0
}
# looseway: the first of $1 and the folders above it that another account
# could change, as "<how> <folder>"; nothing where there is none.
looseway() {
  local d=$1 w
  w=$(loose "$d" self)
  while [ -z "$w" ] && [ "$d" != / ]; do d=${d%/*}; [ -n "$d" ] || d=/; w=$(loose "$d"); done
  [ -n "$w" ] && printf '%s %s\n' "$w" "$d"
  return 0
}
`

// LooseSaid is what loose found, in words that follow the folder's name.
func LooseSaid(how string) string {
	switch how {
	case "owner":
		return "belongs to another account"
	case "other":
		return "can be written by every account"
	case "group":
		return "can be written by its group"
	case "acl":
		return "can be written by another account or group, by its ACL"
	}
	return "could not be read"
}

var grantRe = regexp.MustCompile(`^(user|group):[A-Za-z0-9_][A-Za-z0-9_.@-]{0,63}$`)

// LooseOK is LOOSE_OK's line for the grants given, as ACL entries name
// them; one that is not an account or group name is left out.
func LooseOK(grants []string) string {
	var ok []string
	for _, g := range grants {
		if grantRe.MatchString(g) {
			ok = append(ok, g)
		}
	}
	return "LOOSE_OK='" + strings.Join(ok, " ") + "'\n"
}

// guardSh refuses a write where another account could change the folder it
// goes to or one above it (RemoteFS.Guard).
const guardSh = LooseSh + `guard() {
  local w how
  w=$(looseway "$R"); [ -n "$w" ] || return 0
  how=${w%% *}
  case "$how" in
  owner) how="belongs to another account" ;;
  other) how="can be written by every account" ;;
  group) how="can be written by its group" ;;
  acl) how="can be written by another account or group, by its ACL" ;;
  *) how="could not be read" ;;
  esac
  fail 13 "${w#* } $how, so another account could send this elsewhere; nothing was changed. Refresh the library to see how to make it writable."
}
`
