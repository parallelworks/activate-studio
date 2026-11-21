package transfers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// Runner is what RemoteFS needs of the ssh pool (remote.Pool).
type Runner interface {
	RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (remote.Result, error)
	Command(site, key string, argv []string) (Cmd, error)
}

// Cmd is a started-later command with streams (exec.Cmd in use).
type Cmd interface {
	StdinPipe() (io.WriteCloser, error)
	StdoutPipe() (io.ReadCloser, error)
	SetStderr(w io.Writer)
	Start() error
	Wait() error
	Kill()
}

// tool is the helper every remote call runs: bash -c tool studio <marker>
// <cmd> <root> [args]. It prints the marker first (read: once its checks
// pass, just before the file), and whatever a login printed ahead of it is
// dropped. Lists arrive on standard input separated by NUL. Names only
// ever arrive as arguments or on standard input, never in the script's
// text. Each command first takes the root's real path and refuses a path
// whose real folder is not inside it. Records go out NUL separated:
// type, size, mtime, mode and path, by tabs, from GNU find's -printf, or
// on systems without it (macOS) from stat -f, where a name with a line
// break is refused since that output is line based.
const tool = `set -f
mark=$1; cmd=$2; root=$3; shift 3
# like: in a private location (PRIV=1, a person's space), what a write
# makes is closed to other accounts, unless the folder it lands in is
# already open to them, when it is readable as they read the folder. The
# nearest folder that exists decides, so a path made in several levels
# takes one answer. A default ACL, where there is one, wins over either.
like() { [ "${PRIV:-}" = 1 ] || return 0; local a=$1; while [ ! -d "$a" ]; do a=$(dirof "$a"); done; if [ -n "$(find -- "$a" -maxdepth 0 -perm -004 2>/dev/null)" ]; then umask 022; else umask 077; fi; }
# closed: in a private location, a folder closed to other accounts with no
# ACL of its own; a file a mover wrote (rsync keeps the source's mode) and
# landed there is closed the same way, as like makes what the helper writes.
closed() { [ "${PRIV:-}" = 1 ] || return 1; [ -z "$(find -- "$1" -maxdepth 0 -perm -004 2>/dev/null)" ] || return 1; case "$(ls -ld -- "$1" 2>/dev/null | cut -d' ' -f1)" in *+) return 1;; esac; return 0; }
[ "$cmd" = read ] || printf '%s\n' "$mark"
fail() { printf '%s\n' "$2" >&2; exit "$1"; }
[ "$cmd" = home ] && { cd -P ~ 2>/dev/null && pwd -P; exit; }
[ "$cmd" = rclone ] && { command -v rclone >/dev/null 2>&1 || exit 9; rclone version 2>/dev/null | head -n 1; exit 0; }
[ "$cmd" = rsync ] && { r=${1:-rsync}; command -v -- "$r" >/dev/null 2>&1 || exit 9; "$r" --version 2>&1 | head -n 20; printf '%s\n' "$mark"; "$r" --help 2>&1 | head -n 400; printf '%s\n' "$mark"; "$r" --trust-sender --version >/dev/null 2>&1 && echo trust-sender; exit 0; }
[ -d "$root" ] || fail 3 "no such folder: $root"
R=$(cd -P -- "$root" 2>/dev/null && pwd -P) || fail 3 "cannot open $root"
cd -- "$R" || fail 3 "cannot open $root"
Rp=${R%/}/
inside() { local d; d=$(cd -P -- "$1" 2>/dev/null && pwd -P) || return 1; case "$d/" in "$Rp"*) return 0;; esac; return 1; }
dirof() { case "$1" in */*) printf '%s' "${1%/*}";; *) printf .;; esac; }
# plain: in a private location, or one written as its owner (LINKS=1), the
# way to a folder a write changes goes through no link. A person a folder is shared with for writing can make
# links in it, and the owner's writes must not follow one to another of
# their folders (one open to more people, say). The nearest part that
# exists is looked at, before anything is made.
plain() {
  [ "${PRIV:-}" = 1 ] || [ "${LINKS:-}" = 1 ] || return 0
  local a=$1
  while [ ! -e "$a" ] && [ ! -L "$a" ]; do a=$(dirof "$a"); done
  [ -L "$a" ] && return 1
  [ -d "$a" ] || a=$(dirof "$a")
  [ "$(cd -P -- "$a" 2>/dev/null && pwd -P)" = "$(cd -L -- "$a" 2>/dev/null && pwd -L)" ]
}
G=0; find . -maxdepth 0 -printf '' >/dev/null 2>&1 && G=1
F='%y\t%s\t%T@\t%m\t%p\0'
SF='%HT%t%z%t%m%t%Lp%t%N'
nl='
'
bsd() { if find -- "$@" -name "*$nl*" 2>/dev/null | grep -q .; then fail 11 "a name holds a line break, which this host's tools cannot list safely"; fi; }
one() { if [ $G = 1 ]; then find -- "$1" -maxdepth 0 -printf "$F"; else stat -f "$SF" -- "$1" | tr '\n' '\0'; fi; }
case "$cmd" in
list)
  p=${1:-.}
  [ -e "$p" ] || [ -L "$p" ] || fail 4 "$p: no such folder"
  [ -d "$p" ] && [ ! -L "$p" ] || fail 5 "$p is not a folder"
  inside "$p" || fail 6 "$p leads outside the location"
  if [ $G = 1 ]; then find -- "$p" -mindepth 1 -maxdepth 1 ! -name '.*' -printf "$F"
  else bsd "$p"; find -- "$p" -mindepth 1 -maxdepth 1 ! -name '.*' -exec stat -f "$SF" {} + | tr '\n' '\0'; fi ;;
list2)
  # A folder and, in the same answer, the folders in it: its entries, then
  # after the marker the folders in it this account cannot list, then the
  # entries of the others, at most $2, then the marker again once all of
  # them were read. Only where find and head know -printf and -z.
  p=${1:-.}
  [ -e "$p" ] || [ -L "$p" ] || fail 4 "$p: no such folder"
  [ -d "$p" ] && [ ! -L "$p" ] || fail 5 "$p is not a folder"
  inside "$p" || fail 6 "$p leads outside the location"
  if [ $G = 1 ]; then find -- "$p" -mindepth 1 -maxdepth 1 ! -name '.*' -printf "$F"
  else bsd "$p"; find -- "$p" -mindepth 1 -maxdepth 1 ! -name '.*' -exec stat -f "$SF" {} + | tr '\n' '\0'; exit 0; fi
  printf '%s\0' "$mark"
  find -- "$p" -mindepth 1 -maxdepth 1 -type d ! -name '.*' \( ! -readable -o ! -executable \) -printf '%p\0' 2>/dev/null
  printf '%s\0' "$mark"
  { find -- "$p" -mindepth 2 -maxdepth 2 ! -path '*/.*' -printf "$F" 2>/dev/null | head -z -n "${2:-2000}" && printf '%s\0' "$mark"; } 2>/dev/null
  exit 0 ;;
walk)
  while IFS= read -r -d '' p; do
    [ -e "$p" ] || [ -L "$p" ] || fail 4 "$p: no such file or folder"
    inside "$(dirof "$p")" || fail 6 "$p leads outside the location"
    [ -L "$p" ] && fail 7 "$p is a link; name what it points to"
    if [ -d "$p" ]; then
      one "$p"
      if [ $G = 1 ]; then find -- "$p" -mindepth 1 \( -name '.?*' -printf "$F" -prune -o -printf "$F" \)
      else bsd "$p"; find -- "$p" -mindepth 1 \( -name '.?*' -prune -print0 -o -print0 \) | xargs -0 stat -f "$SF" | tr '\n' '\0'; fi
    else one "$p"; fi
  done ;;
stat)
  while IFS= read -r -d '' p; do
    [ -e "$p" ] || [ -L "$p" ] || continue
    inside "$(dirof "$p")" || fail 6 "$p leads outside the location"
    one "$p"
  done ;;
free) df -Pk -- "${1:-.}" 2>/dev/null | awk 'NR==2 {print $4}' ;;
sha)
  if command -v sha256sum >/dev/null 2>&1; then H="sha256sum"; elif command -v shasum >/dev/null 2>&1; then H="shasum -a 256"; else H="openssl dgst -sha256 -r"; fi
  while IFS= read -r -d '' p; do
    inside "$(dirof "$p")" || fail 6 "$p leads outside the location"
    [ -f "$p" ] && [ ! -L "$p" ] || fail 5 "$p is not a plain file"
    h=$($H < "$p") || fail 12 "$p could not be read"
    printf '%s\0' "${h%% *}"
  done ;;
read)
  p=$1; d=$(dirof "$p"); n=${p##*/}
  inside "$d" || fail 6 "$p leads outside the location"
  cd -P -- "$d" 2>/dev/null || fail 4 "$p: no such file"
  P=$(pwd -P); case "$P/" in "$Rp"*) ;; *) fail 6 "$p leads outside the location";; esac
  [ -f "./$n" ] && [ ! -L "./$n" ] || fail 5 "$p is not a plain file any more"
  printf '%s\n' "$mark"; exec cat -- "./$n" ;;
write)
  p=$1; d=$(dirof "$p"); n=${p##*/}
  plain "$d" || fail 6 "$p goes through a link"
  like "$p"; mkdir -p -- "$d" 2>/dev/null; inside "$d" || fail 6 "$p leads outside the location"
  cd -P -- "$d" || fail 8 "$p: the folder cannot be opened"
  set -C; cat > "./$n" || fail 8 "$p could not be written"
  [ -n "$2" ] && TZ=UTC touch -t "$2" -- "./$n" 2>/dev/null
  [ "$3" = x ] && chmod u+x -- "./$n" 2>/dev/null
  exit 0 ;;
mkdir) [ -z "$1" ] && exit 0; plain "$1" || fail 6 "$1 goes through a link"; like "$1"; mkdir -p -- "$1" 2>/dev/null; [ -d "$1" ] || fail 8 "$1 could not be made"; inside "$1" || fail 6 "$1 leads outside the location" ;;
land)
  while IFS= read -r -d '' a && IFS= read -r -d '' b && IFS= read -r -d '' f; do
    d=$(dirof "$b"); plain "$d" || fail 6 "$b goes through a link"; plain "$(dirof "$a")" || fail 6 "$a goes through a link"
    like "$b"; mkdir -p -- "$d" 2>/dev/null
    inside "$d" || fail 6 "$b leads outside the location"
    inside "$(dirof "$a")" || fail 6 "$a leads outside the location"
    if [ -e "$b" ] || [ -L "$b" ]; then
      if [ "$f" = r ] && [ -f "$b" ] && [ ! -L "$b" ]; then mv -f -- "$a" "$b" || fail 9 "$b could not be replaced"
      else fail 10 "$b is already there and the plan did not list it"; fi
    elif ln -- "$a" "$b" 2>/dev/null; then rm -f -- "$a"
    else
      [ -e "$b" ] && fail 10 "$b is already there and the plan did not list it"
      mv -n -- "$a" "$b" 2>/dev/null; [ -e "$a" ] && fail 9 "$b could not take its name"
    fi
    closed "$d" && chmod go-rwx -- "$b" 2>/dev/null
    printf '%s\0' "$b"
  done ;;
remove)
  while IFS= read -r -d '' k && IFS= read -r -d '' p; do
    inside "$(dirof "$p")" || fail 6 "$p leads outside the location"
    plain "$(dirof "$p")" || fail 6 "$p goes through a link"
    if [ "$k" = f ]; then [ -f "$p" ] && [ ! -L "$p" ] && { rm -f -- "$p" || fail 9 "$p could not be removed"; printf '%s\0' "$p"; }
    else rmdir -- "$p" 2>/dev/null; fi
  done; exit 0 ;;
rmstage)
  case "${1##*/}" in .studio-transfer-*) ;; *) fail 5 "not a staging folder";; esac
  [ -e "$1" ] || exit 0
  inside "$(dirof "$1")" || fail 6 "$1 leads outside the location"
  [ -L "$1" ] && fail 7 "$1 is a link"
  rm -rf -- "$1" ;;
rmtree)
  while IFS= read -r -d '' p; do
    case "$p" in ''|.|./) fail 6 "refusing the location itself";; esac
    [ -e "$p" ] || [ -L "$p" ] || continue
    inside "$(dirof "$p")" || fail 6 "$p leads outside the location"
    plain "$(dirof "$p")" || fail 6 "$p goes through a link"
    rm -rf -- "$p" || fail 9 "$p could not be removed"
    printf '%s\0' "$p"
  done; exit 0 ;;
*) fail 2 "unknown command" ;;
esac`

// RemoteFS is a folder on a host over ssh (site "ssh://...") or pw ssh
// (site a cluster's name, key the viewer's platform key).
type RemoteFS struct {
	R       Runner
	SiteID  string
	KeyText string
	Dir     string // absolute on the host
	Name    string // for messages
	Timeout time.Duration
	// RsyncPath is the rsync program on the host, "" for rsync on its PATH.
	RsyncPath string
	// Private: a person's own space, where what the helper writes is closed
	// to other accounts unless the folder it lands in is open to them.
	Private bool
	// NoLinks: a location written as its owner where, as in a private
	// one, no write goes through a link, but what is written takes the
	// owner's umask and the folder's default ACL.
	NoLinks bool
	// Kept: commands other than reading and writing a file go over the
	// session kept open to the host (Kept), as the Library's searches do,
	// rather than a session started for each. A session to a cluster
	// takes about a second to start; a command over one kept open takes
	// one round trip.
	Kept bool
}

func (r *RemoteFS) Where() string { return r.Name }
func (r *RemoteFS) Root() string  { return r.Dir }
func (r *RemoteFS) Site() string  { return r.SiteID }
func (r *RemoteFS) Key() string   { return r.KeyText }

// toolArgv is the helper's command line: the marker it prints first, so
// what a login prints before it is dropped, then the command.
func toolArgv(mark, cmd, root string, args ...string) []string {
	return toolArgvFor(false, mark, cmd, root, args...)
}

// privateTool is the helper for a private location (RemoteFS.Private),
// and linksTool for one whose writes only refuse links (RemoteFS.NoLinks).
const (
	privateTool = "PRIV=1\n" + tool
	linksTool   = "LINKS=1\n" + tool
)

func toolArgvFor(private bool, mark, cmd, root string, args ...string) []string {
	script := tool
	if private {
		script = privateTool
	}
	return toolArgvOf(script, mark, cmd, root, args...)
}

func toolArgvOf(script, mark, cmd, root string, args ...string) []string {
	return append([]string{"bash", "-c", script, "studio", mark, cmd, root}, args...)
}

// script is the helper the location's commands run.
func (r *RemoteFS) script() string {
	switch {
	case r.Private:
		return privateTool
	case r.NoLinks:
		return linksTool
	}
	return tool
}

// afterMark is a helper's output after its marker line; ok false when the
// marker never came.
func afterMark(out []byte, mark string) ([]byte, bool) {
	i := bytes.Index(out, []byte(mark+"\n"))
	if i < 0 {
		return nil, false
	}
	return out[i+len(mark)+1:], true
}

// runTool runs a helper command on a site and drops a login's output.
func runTool(ctx context.Context, run Runner, site, key string, timeout time.Duration, input io.Reader, cmd, root string, args ...string) (remote.Result, bool, error) {
	return runToolFor(ctx, false, run, site, key, timeout, input, cmd, root, args...)
}

func runToolFor(ctx context.Context, private bool, run Runner, site, key string, timeout time.Duration, input io.Reader, cmd, root string, args ...string) (remote.Result, bool, error) {
	script := tool
	if private {
		script = privateTool
	}
	return runToolWith(ctx, marker(), false, script, run, site, key, timeout, input, cmd, root, args...)
}

// runToolWith is runToolFor with the helper's script and marker given,
// over the session kept open to the host when kept is set and the runner
// keeps one, and the input is a list short enough to go as arguments. A
// command may also mark the parts of its answer with the marker.
func runToolWith(ctx context.Context, mark string, kept bool, script string, run Runner, site, key string, timeout time.Duration, input io.Reader, cmd, root string, args ...string) (remote.Result, bool, error) {
	argv := toolArgvOf(script, mark, cmd, root, args...)
	var res remote.Result
	var err error
	k, canKeep := run.(Kept)
	if kept && canKeep && keepable(input) {
		res, err = k.Run(ctx, site, key, keptArgv(argv, input), timeout)
	} else {
		res, err = run.RunOnce(ctx, site, key, argv, timeout, input)
	}
	if err != nil {
		return res, false, err
	}
	i := bytes.Index(res.Raw, []byte(mark+"\n"))
	banner := i > 0
	if out, ok := afterMark(res.Raw, mark); ok {
		res.Raw, res.Stdout = out, string(out)
	} else if res.Code == 0 {
		return res, false, apierr.New(502, "%s: the host's answer could not be read", remote.SiteName(site))
	}
	return res, banner, nil
}

// nulInput is a helper command's list, sent on standard input separated
// by NUL, or as arguments over a kept-open session (RemoteFS.Kept).
type nulInput struct {
	*bytes.Reader
	items []string
	size  int
}

func nulList(items ...[]string) io.Reader {
	var b bytes.Buffer
	var all []string
	for _, list := range items {
		for _, s := range list {
			b.WriteString(s)
			b.WriteByte(0)
			all = append(all, s)
		}
	}
	return &nulInput{Reader: bytes.NewReader(b.Bytes()), items: all, size: b.Len()}
}

// Kept is a Runner that also sends a command over the session it keeps
// open to a host (remote.Pool.Run), where nothing arrives on standard
// input.
type Kept interface {
	Run(ctx context.Context, site, key string, argv []string, timeout time.Duration) (remote.Result, error)
}

// keptMax is the most a list sent as arguments may hold, and smallWrite
// the largest file written that way; more goes over a session of its own,
// on standard input. The session reads each command as one line, a byte
// at a time, so what it carries stays small.
const (
	keptMax    = 64 << 10
	smallWrite = 48 << 10
)

// smallFile is a file's bytes for the write command, sent as an argument
// in base64 over a kept-open session.
type smallFile struct {
	*bytes.Reader
	data []byte
}

func keepable(input io.Reader) bool {
	switch in := input.(type) {
	case nil:
		return true
	case *nulInput:
		return in.size <= keptMax
	case *smallFile:
		return len(in.data) <= smallWrite
	}
	return false
}

// feedSh runs the helper with its list from arguments: $1 is how many
// arguments are the helper's own, $2 its script; the rest are the list,
// written to its standard input separated by NUL.
const feedSh = `n=$1; s=$2; shift 2
a=(); while [ "$n" -gt 0 ]; do a+=("$1"); shift; n=$((n-1)); done
{ [ $# -eq 0 ] || printf '%s\0' "$@"; } | bash -c "$s" "${a[@]}"`

// feedFileSh runs the helper ($1) with the file in $2, in base64, on its
// standard input.
const feedFileSh = `s=$1; b=$2; shift 2
d=-d; [ "$(printf eA== | base64 -d 2>/dev/null)" = x ] || d=-D
printf '%s' "$b" | base64 $d | bash -c "$s" "$@"`

// keptArgv is argv (a helper command line) with input fed to it.
func keptArgv(argv []string, input io.Reader) []string {
	switch in := input.(type) {
	case *nulInput:
		if len(in.items) == 0 {
			return argv
		}
		out := append([]string{"bash", "-c", feedSh, "feed", strconv.Itoa(len(argv) - 3), argv[2]}, argv[3:]...)
		return append(out, in.items...)
	case *smallFile:
		return append([]string{"bash", "-c", feedFileSh, "feed", argv[2], base64.StdEncoding.EncodeToString(in.data)}, argv[3:]...)
	}
	return argv
}

// run runs a helper command and maps its exit status to an error.
func (r *RemoteFS) run(ctx context.Context, input io.Reader, cmd string, args ...string) (remote.Result, error) {
	return r.runWith(ctx, marker(), input, cmd, args...)
}

// runMarked is run with the marker given (runToolWith).
func (r *RemoteFS) runMarked(ctx context.Context, mark, cmd string, args ...string) (remote.Result, error) {
	return r.runWith(ctx, mark, nil, cmd, args...)
}

func (r *RemoteFS) runWith(ctx context.Context, mark string, input io.Reader, cmd string, args ...string) (remote.Result, error) {
	t := r.Timeout
	if t <= 0 {
		t = 10 * time.Minute
	}
	res, _, err := runToolWith(ctx, mark, r.Kept, r.script(), r.R, r.SiteID, r.KeyText, t, input, cmd, r.Dir, args...)
	if err != nil {
		return res, r.named(502, messageOf(err))
	}
	if res.Code == 0 {
		return res, nil
	}
	if sftpAnswer(res) {
		// The account changed to SFTP only since it was last looked at.
		if _, ok := r.R.(Subsystemer); ok && remote.IsSSH(r.SiteID) {
			markSFTP(r.SiteID, r.KeyText, true)
		}
		return res, r.named(502, "the account takes SFTP only; try again, and Studio will use SFTP")
	}
	msg := lastLine(remote.QuietStderr(res.Stderr))
	if msg == "" {
		msg = fmt.Sprintf("the command ended with status %d", res.Code)
	}
	status := 502
	switch res.Code {
	case 4:
		status = 404
	case 5, 6, 7, 11:
		status = 400
	case 10:
		status = 409
	}
	return res, r.named(status, msg)
}

// named puts the location's name before a message once. The remote
// package names the host in its own errors already.
func (r *RemoteFS) named(status int, msg string) error {
	if r.Name != "" && !strings.HasPrefix(msg, r.Name+": ") {
		msg = r.Name + ": " + msg
	}
	return apierr.New(status, "%s", msg)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// parseRecords reads the helper's records. Paths come back as find gives
// them: "./x" for the root's own entries, which is cut to "x".
func parseRecords(raw []byte) ([]FileInfo, error) {
	var out []FileInfo
	for _, rec := range bytes.Split(raw, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\t", 5)
		if len(f) != 5 {
			return nil, apierr.New(502, "the host's listing could not be read")
		}
		size, _ := strconv.ParseInt(f[1], 10, 64)
		mt, _ := strconv.ParseFloat(f[2], 64)
		mode, _ := strconv.ParseUint(f[3], 8, 32)
		p := f[4]
		if p == "." {
			p = ""
		}
		p = strings.TrimPrefix(p, "./")
		t := typeOf(f[0])
		if t == 'd' {
			size = 0
		}
		out = append(out, FileInfo{Rel: p, Type: t, Size: size, Mtime: int64(mt), Mode: uint32(mode)})
	}
	return out, nil
}

func dotted(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

func (r *RemoteFS) List(ctx context.Context, rel string, max int) ([]Entry, bool, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpList(ctx, rel, max)
	}
	res, err := r.run(ctx, nil, "list", dotted(rel))
	if err != nil {
		return nil, false, err
	}
	recs, err := parseRecords(res.Raw)
	if err != nil {
		return nil, false, err
	}
	var out []Entry
	for _, e := range recs {
		out = append(out, entryOf(e, baseName(e.Rel)))
	}
	es, more := sortEntries(out, max)
	return es, more, nil
}

// TwoLevels is a folder's entries and, where the host could say, those of
// the folders in it, by their paths under the location.
type TwoLevels struct {
	Entries  []Entry
	Children map[string][]Entry
}

// ListTwo is List with the entries of the folders in rel too, in one
// command, up to max of them. A folder in rel is in Children only when all
// of its entries were read: not one this account cannot list, nor one
// past where max cut the answer off. Hosts without GNU find answer rel
// alone.
func (r *RemoteFS) ListTwo(ctx context.Context, rel string, max int) (TwoLevels, error) {
	if r.SFTPOnly(ctx) {
		es, _, err := r.sftpList(ctx, rel, 0)
		return TwoLevels{Entries: es}, err
	}
	if max <= 0 {
		max = 2000
	}
	mark := marker()
	res, err := r.runMarked(ctx, mark, "list2", dotted(rel), strconv.Itoa(max))
	if err != nil {
		return TwoLevels{}, err
	}
	parts := bytes.SplitN(res.Raw, []byte(mark+"\x00"), 4)
	recs, err := parseRecords(parts[0])
	if err != nil {
		return TwoLevels{}, err
	}
	var out TwoLevels
	var dirs []string
	for _, e := range recs {
		out.Entries = append(out.Entries, entryOf(e, baseName(e.Rel)))
		if e.Type == 'd' {
			dirs = append(dirs, e.Rel)
		}
	}
	out.Entries, _ = sortEntries(out.Entries, 0)
	if len(parts) < 3 {
		return out, nil
	}
	closed := map[string]bool{}
	for _, p := range splitNul(string(parts[1])) {
		closed[strings.TrimPrefix(p, "./")] = true
	}
	deeper, err := parseRecords(parts[2])
	if err != nil {
		return out, nil
	}
	whole := len(parts) == 4
	// find reads the folders in the order it listed them, each whole
	// before the next: cut off, the folder it stopped in and those after
	// it are not known.
	known := map[string]bool{}
	if whole && len(deeper) < max {
		for _, d := range dirs {
			known[d] = true
		}
	} else if len(deeper) > 0 {
		last := dirOf(deeper[len(deeper)-1].Rel)
		for _, d := range dirs {
			if d == last {
				break
			}
			known[d] = true
		}
	}
	out.Children = map[string][]Entry{}
	for _, d := range dirs {
		if known[d] && !closed[d] {
			out.Children[d] = []Entry{}
		}
	}
	for _, e := range deeper {
		d := dirOf(e.Rel)
		if es, ok := out.Children[d]; ok {
			out.Children[d] = append(es, entryOf(e, baseName(e.Rel)))
		}
	}
	for d, es := range out.Children {
		out.Children[d], _ = sortEntries(es, 0)
	}
	return out, nil
}

func (r *RemoteFS) Stat(ctx context.Context, rels []string) (map[string]FileInfo, error) {
	out := map[string]FileInfo{}
	if len(rels) == 0 {
		return out, nil
	}
	if r.SFTPOnly(ctx) {
		return r.sftpStat(ctx, rels)
	}
	res, err := r.run(ctx, nulList(rels), "stat")
	if err != nil {
		return nil, err
	}
	recs, err := parseRecords(res.Raw)
	if err != nil {
		return nil, err
	}
	for _, e := range recs {
		out[e.Rel] = e
	}
	return out, nil
}

func (r *RemoteFS) Walk(ctx context.Context, rels []string, limit int) (Walked, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpWalk(ctx, rels, limit)
	}
	in := make([]string, len(rels))
	for i, p := range rels {
		in[i] = dotted(p)
	}
	res, err := r.run(ctx, nulList(in), "walk")
	if err != nil {
		return Walked{}, err
	}
	recs, err := parseRecords(res.Raw)
	if err != nil {
		return Walked{}, err
	}
	return collect(rels, recs, limit)
}

func (r *RemoteFS) Free(ctx context.Context, rel string) (int64, bool) {
	if r.SFTPOnly(ctx) {
		return r.sftpFree(ctx, rel)
	}
	res, err := r.run(ctx, nil, "free", dotted(rel))
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
	if err != nil {
		return 0, false
	}
	return n * 1024, true
}

func (r *RemoteFS) Sha256(ctx context.Context, rels []string) (map[string]string, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpSha256(ctx, rels)
	}
	out := map[string]string{}
	for start := 0; start < len(rels); start += 2000 {
		batch := rels[start:min(start+2000, len(rels))]
		res, err := r.run(ctx, nulList(batch), "sha")
		if err != nil {
			return nil, err
		}
		hs := strings.Split(strings.TrimRight(res.Stdout, "\x00"), "\x00")
		if len(hs) != len(batch) {
			return nil, apierr.New(502, "%s: the checksums could not be read", r.Name)
		}
		for i, h := range hs {
			out[batch[i]] = strings.TrimPrefix(strings.ToLower(h), "\\")
		}
	}
	return out, nil
}

func (r *RemoteFS) command(args []string) (Cmd, error) {
	return r.R.Command(r.SiteID, r.KeyText, args)
}

// marker is a line the read command prints before the file, so whatever
// a login prints on standard output first is dropped.
func marker() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "STUDIO-" + hex.EncodeToString(b)
}

const maxPreamble = 1 << 20

// readCloser is a remote cat's output after the marker; Close waits for
// the command and reports how it ended.
type readCloser struct {
	io.Reader
	cmd    Cmd
	stderr *bytes.Buffer
	name   string
	once   sync.Once
	err    error
	out    io.ReadCloser
}

func (rc *readCloser) Close() error {
	rc.once.Do(func() {
		// Draining what is left lets cat end on its own when the caller
		// read it all; otherwise it is stopped.
		rc.cmd.Kill()
		rc.out.Close()
		if err := rc.cmd.Wait(); err != nil {
			msg := lastLine(remote.QuietStderr(rc.stderr.String()))
			if msg != "" {
				rc.err = errors.New(msg)
			}
		}
	})
	return rc.err
}

func (r *RemoteFS) Read(ctx context.Context, rel string) (io.ReadCloser, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpRead(ctx, rel)
	}
	mark := marker()
	cmd, err := r.command(toolArgv(mark, "read", r.Dir, rel))
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)
	if err := cmd.Start(); err != nil {
		return nil, r.named(502, err.Error())
	}
	// Up to the marker.
	var head []byte
	buf := make([]byte, 32<<10)
	want := []byte(mark + "\n")
	for {
		n, rerr := out.Read(buf)
		head = append(head, buf[:n]...)
		if i := bytes.Index(head, want); i >= 0 {
			rest := head[i+len(want):]
			return &readCloser{Reader: io.MultiReader(bytes.NewReader(rest), out), cmd: cmd, stderr: &stderr, name: rel, out: out}, nil
		}
		if rerr != nil || len(head) > maxPreamble {
			cmd.Kill()
			cmd.Wait()
			msg := lastLine(remote.QuietStderr(stderr.String()))
			if msg == "" {
				msg = "the host did not start sending the file"
			}
			return nil, r.named(502, msg)
		}
	}
}

// touchTime is mtime as touch -t takes it, in UTC.
func touchTime(mtime int64) string {
	if mtime <= 0 {
		return ""
	}
	return unixTime(mtime).UTC().Format("200601021504.05")
}

func (r *RemoteFS) Write(ctx context.Context, rel string, src io.Reader, mtime int64, mode uint32) (int64, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpWrite(ctx, rel, src, mtime, mode)
	}
	x := ""
	if mode&0o100 != 0 {
		x = "x"
	}
	if _, ok := r.R.(Kept); ok && r.Kept {
		// A small file goes over the kept-open session in one command.
		head, err := io.ReadAll(io.LimitReader(src, smallWrite+1))
		if err != nil {
			return 0, err
		}
		if len(head) <= smallWrite {
			if _, err := r.run(ctx, &smallFile{Reader: bytes.NewReader(head), data: head}, "write", rel, touchTime(mtime), x); err != nil {
				return 0, err
			}
			return int64(len(head)), nil
		}
		src = io.MultiReader(bytes.NewReader(head), src)
	}
	cmd, err := r.command(toolArgvOf(r.script(), marker(), "write", r.Dir, rel, touchTime(mtime), x))
	if err != nil {
		return 0, err
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return 0, err
	}
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)
	if err := cmd.Start(); err != nil {
		return 0, r.named(502, err.Error())
	}
	n, cerr := io.Copy(in, src)
	in.Close()
	if cerr != nil {
		cmd.Kill()
		cmd.Wait()
		return n, cerr
	}
	if err := cmd.Wait(); err != nil {
		msg := lastLine(remote.QuietStderr(stderr.String()))
		if msg == "" {
			msg = err.Error()
		}
		return n, r.named(502, msg)
	}
	return n, nil
}

func (r *RemoteFS) Mkdir(ctx context.Context, rel string) error {
	if r.SFTPOnly(ctx) {
		return r.sftpMkdir(ctx, rel)
	}
	_, err := r.run(ctx, nil, "mkdir", rel)
	return err
}

func (r *RemoteFS) Land(ctx context.Context, pairs []LandPair) ([]string, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpLand(ctx, pairs)
	}
	var in []string
	for _, p := range pairs {
		f := "n"
		if p.Replace {
			f = "r"
		}
		in = append(in, p.Staged, p.Final, f)
	}
	res, err := r.run(ctx, nulList(in), "land")
	done := splitNul(res.Stdout)
	return done, err
}

func splitNul(s string) []string {
	s = strings.TrimRight(s, "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

func (r *RemoteFS) Remove(ctx context.Context, files, dirs []string) ([]string, error) {
	if r.SFTPOnly(ctx) {
		return r.sftpRemove(ctx, files, dirs)
	}
	var in []string
	for _, f := range files {
		in = append(in, "f", f)
	}
	for _, d := range deepestFirst(dirs) {
		if d != "" {
			in = append(in, "d", d)
		}
	}
	res, err := r.run(ctx, nulList(in), "remove")
	return splitNul(res.Stdout), err
}

// RemoveTrees removes files and folders, folders with everything in them;
// a link is removed itself, never what it points to. It answers what it
// removed.
func (r *RemoteFS) RemoveTrees(ctx context.Context, rels []string) ([]string, error) {
	res, err := r.run(ctx, nulList(rels), "rmtree")
	return splitNul(res.Stdout), err
}

func (r *RemoteFS) RemoveStaging(ctx context.Context, rel string) error {
	if r.SFTPOnly(ctx) {
		return r.sftpRemoveStaging(ctx, rel)
	}
	_, err := r.run(ctx, nil, "rmstage", rel)
	return err
}

// Home is the account's home folder on a host: for an account that takes
// SFTP only, the folder its SFTP session starts in.
func Home(ctx context.Context, run Runner, site, key string) (string, error) {
	if sftpOnlySite(ctx, run, site, key) {
		return sftpHome(ctx, run, site, key)
	}
	res, _, err := runTool(ctx, run, site, key, 60*time.Second, nil, "home", "/")
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(lastLine(res.Stdout))
	if res.Code != 0 || !strings.HasPrefix(h, "/") {
		return "", apierr.New(502, "%s: the home folder could not be found", remote.SiteName(site))
	}
	return h, nil
}

// RsyncProbe is a host's rsync: its --version and --help output (each
// cut short) and whether it takes --trust-sender. found is false without
// rsync; clean is false when the login printed anything before the answer
// on standard output, which breaks rsync's protocol over that connection.
func RsyncProbe(ctx context.Context, run Runner, site, key, program string) (p rsyncProbe, found, clean bool) {
	if program == "" {
		program = "rsync"
	}
	if sftpOnlySite(ctx, run, site, key) {
		return rsyncProbe{}, false, true
	}
	mark := marker()
	res, err := run.RunOnce(ctx, site, key, toolArgv(mark, "rsync", "/", program), 60*time.Second, nil)
	if err != nil || res.Code != 0 {
		return rsyncProbe{}, false, true
	}
	i := bytes.Index(res.Raw, []byte(mark+"\n"))
	out, ok := afterMark(res.Raw, mark)
	if !ok {
		return rsyncProbe{}, false, true
	}
	parts := strings.SplitN(string(out), mark+"\n", 3)
	p.version = parts[0]
	if len(parts) > 1 {
		p.help = parts[1]
	}
	if len(parts) > 2 {
		p.trust = strings.TrimSpace(parts[2]) == "trust-sender"
	}
	return p, strings.TrimSpace(p.version) != "", i <= 0
}
