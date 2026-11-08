package userlibs

import (
	"embed"
	"os"
	"path/filepath"
)

// The scripts a build on another system runs (remoteBuild.ts's, with the
// Studio's helper in place of the indexer's Python): build_status.sh is
// still Node's text byte for byte, which TestGoldenIsCurrent checks; the
// others start the helper's passes and are Go's own.
//
//go:embed scripts/*.sh
var scriptFiles embed.FS

func script(name string) string {
	b, err := scriptFiles.ReadFile("scripts/" + name + ".sh")
	if err != nil {
		panic(err)
	}
	return string(b)
}

var (
	buildCheck    = script("build_check")
	buildRun      = script("build_run")
	buildStart    = script("build_start")
	buildStatusSh = script("build_status")
	embedProbe    = script("embed_probe")
)

// The scripts userLibraries.ts and remoteEmbed.ts keep to themselves.

// indexCheck: the index root is a directory with a db.db, and that file's
// modified time. Without one, the directories below that have one, since
// gufi_dir2index writes the index one level down, named after the indexed
// directory.
const indexCheck = `[ -d "$1" ] || exit 3
if [ ! -f "$1/db.db" ]; then for f in "$1"/*/db.db; do [ -f "$f" ] && printf '%s\n' "${f%/db.db}"; done | head -n 3; exit 4; fi
[ -r "$1/db.db" ] || exit 5
if stat -c %Y / >/dev/null 2>&1; then stat -c %Y -- "$1/db.db"; else stat -f %m -- "$1/db.db"; fi`

// findTools: the first directory among those given that holds every tool in $1.
const findTools = `need=$1; shift; for d in "$@"; do ok=1; for t in $need; do { [ -f "$d/$t" ] && [ -x "$d/$t" ]; } || ok=0; done; [ "$ok" = 1 ] && { printf "%s\n" "$d"; exit 0; }; done; exit 1`

// searchTools: findTools after gufi_query on the login's PATH and the
// Studio's installs there, newest first.
const searchTools = `need=$1; shift; c=$(command -v gufi_query 2>/dev/null); s=$(ls -dt "$HOME"/.activate-studio/gufi/*/bin 2>/dev/null); set -- $s "$@"; case "$c" in /*) set -- "${c%/*}" "$@" ;; esac; set -- "$need" "$@"; ` + findTools

const hello = `uname -s; printf "%s\n" "$HOME"`

const sourceCheck = `[ -d "$1" ] || exit 3; [ -r "$1" ] && [ -x "$1" ] || exit 4`

// workflowHome: the remote home, and the bin directory of the GUFI install
// an index run used.
const workflowHome = `cd "$HOME" && echo "$HOME"; ls -d "$HOME"/.activate-studio/gufi/"$1"*/bin 2>/dev/null | head -1`

// The embedding files there (remoteEmbed.ts). $1 the folder under
// ~/.activate-studio (embed or readers) and the version; PUT's $2 the
// file's name.
const (
	embedList  = `d="$HOME/.activate-studio/$1.files"; [ -d "$d" ] || exit 0` + "\n" + `for f in "$d"/*; do [ -f "$f" ] && printf '%s %s\n' "$(wc -c < "$f" | tr -d ' ')" "${f##*/}"; done; exit 0`
	embedPut   = `d="$HOME/.activate-studio/$1.files"; mkdir -p "$d" && cat > "$d/.$2.part" && mv -f "$d/.$2.part" "$d/$2" && touch "$HOME/.activate-studio/$1.sending"`
	embedTouch = `touch "$HOME/.activate-studio/$1.sending"`
	embedDone  = `rm -f "$HOME/.activate-studio/$1.sending"`
)

// indexerScript is one of the Studio's indexer files, copied to the remote
// system for a build; an error where it is not there, as readFileSync throws.
func (m *Module) indexerScript(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(m.ProjectRoot, "indexer", name))
	return string(b), err
}
