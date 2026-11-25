package folders

import "strings"

// Access is a folder as one account finds it on a machine.
type Access struct {
	// File: what was asked for is a file, and the folder is the one it is in.
	File  bool
	Write bool
	// Ino tells the folder from another at the same path on another machine.
	Ino string
}

// AccessScript says what the account it runs as may do in a folder of a
// library: its arguments are the library's folder and a path in it ("" for
// the folder itself). It says nothing of one that is not there, that the
// account cannot open, or that a link takes out of the library's folder.
const AccessScript = `r=$(CDPATH= cd -P -- "$1" 2>/dev/null && pwd -P) || exit 0
p=$r${2:+/$2}
f=
if [ ! -d "$p" ]; then
  [ -f "$p" ] && [ -r "$p" ] || exit 0
  f=1
  p=${p%/*}
fi
CDPATH= cd -P -- "${p:-/}" 2>/dev/null || exit 0
case "$(pwd -P)/" in "${r%/}"/*) ;; *) exit 0 ;; esac
[ -r . ] && [ -x . ] || exit 0
w=
[ -w . ] && w=1
set -- $(ls -di . 2>/dev/null)
printf 'studio-access file=%s write=%s ino=%s\n' "$f" "$w" "$1"
`

// ParseAccess is AccessScript's answer; ok false when it said nothing.
func ParseAccess(out string) (a Access, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), " ")
		if len(f) != 4 || f[0] != "studio-access" {
			continue
		}
		return Access{File: f[1] == "file=1", Write: f[2] == "write=1", Ino: strings.TrimPrefix(f[3], "ino=")}, true
	}
	return Access{}, false
}
