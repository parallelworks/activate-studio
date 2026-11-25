package userlibs

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// A person's own space (docs/MULTI-USER.md, Personal spaces): a folder they
// own on the cluster the Studio runs beside, indexed, read and written as
// them over their own session. The index goes in their ~/.activate-studio,
// which only they can open, and GUFI runs from their own copy there, so no
// other account can change what runs as them.

// SuggestedSpace is the folder offered first.
const SuggestedSpace = "~/studio"

// HomeCluster is the cluster spaces are made on (libraries.HomeCluster).
func HomeCluster() string { return libraries.HomeCluster() }

// spaceCheckSh reads, as the person, what a folder to index at $1 is: the
// account, the folder (made, closed to every other account, when $3 is 1),
// how another account could write in it or move it (transfers.LooseSh,
// with $4 the grants its owner made from the Studio; with $5 1 the way its
// path is written as well), whether its file system takes ACLs, and where
// GUFI ($2 the Studio's copy) can run from.
const spaceCheckSh = `umask 077
LOOSE_OK=$4
` + transfers.LooseSh + `
home=$(cd ~ 2>/dev/null && pwd -P)
printf 'uid=%s\nuser=%s\nhome=%s\nplatform=%s\n' "$lz_uid" "$lz_user" "$home" "$(uname -sm)"
p=$1
case "$p" in "~") p=$home;; "~/"*) p="$home/${p#"~/"}";; esac
case "$p" in /*) ;; *) echo path=relative; exit 0;; esac
echo "target=$p"
if [ "$3" = 1 ] && [ ! -e "$p" ] && [ ! -L "$p" ]; then mkdir -p -m 700 -- "$p" 2>/dev/null && echo made=1; fi
if [ -e "$p" ] || [ -L "$p" ]; then
  echo exists=1
  [ -d "$p" ] || { echo kind=notdir; exit 0; }
  r=$(cd -P -- "$p" 2>/dev/null && pwd -P) || { echo kind=unreadable; exit 0; }
else
  echo exists=0
  r=$p
  while [ ! -e "$r" ]; do r=${r%/*}; [ -n "$r" ] || r=/; done
  r=$(cd -P -- "$r" 2>/dev/null && pwd -P) || { echo kind=unreadable; exit 0; }
fi
echo "real=$r"
[ -r "$r" ] && [ -x "$r" ] && echo readable=1
[ -w "$r" ] && echo writable=1
echo "owner=$(lz_stat %u %u "$r")"
echo "mode=$(lz_stat %a %Lp "$r")"
echo "fgroup=$(lz_stat %G %Sg "$r")"
echo "pin=$(lz_stat %i %i "$r"):$(lz_stat %u %u "$r")"
case ",$(findmnt -n -o OPTIONS -T "$home" 2>/dev/null | head -n 1)," in *,noexec,*) echo noexec=1 ;; esac
# The mount's own type: stat -f reports ext4 as ext2/ext3.
fs=$(findmnt -n -o FSTYPE -T "$r" 2>/dev/null | head -n 1)
[ -n "$fs" ] || fs=$(stat -f -c %T -- "$r" 2>/dev/null)
echo "fs=$fs"
# A folder to be made goes under $r, the nearest one there is.
w=''; d=$r
if [ -e "$p" ] || [ -L "$p" ]; then echo "self=$(loose "$r" self)"; else w=$(loose "$r"); fi
while [ -z "$w" ] && [ "$d" != / ]; do
  d=${d%/*}; [ -n "$d" ] || d=/
  w=$(loose "$d")
done
# A library's folder is reached by its path again each time.
if [ -z "$w" ] && [ "$5" = 1 ]; then w=$(looseroute "$p"); d=${w#* }; w=${w%% *}; fi
if [ -n "$w" ]; then echo "unsafe=$w $d"; fi
if [ "$(lz_stat %u %u "$r")" = "$lz_uid" ] && [ -w "$r" ]; then
  # A new file, held open, so that the ACL goes to it and to no other.
  t="$r/.studio-acl-$$-$RANDOM$RANDOM"
  set -C
  if { exec 9> "$t"; } 2>/dev/null; then
    if command -v setfacl >/dev/null 2>&1 && setfacl -m "u:$lz_uid:r" -- /proc/self/fd/9 2>/dev/null; then echo acl=1; else echo acl=0; fi
    exec 9>&-
    rm -f -- "$t"
  fi
  set +C
fi
a="$home/.activate-studio"
if [ -e "$a" ] || [ -L "$a" ]; then echo "studio=$(lz_stat %u %u "$a")"; fi
# Where the write helper runs from (writeProbeSh).
LOOSE_OK=
if r=$(cd -P -- "$a" 2>/dev/null && pwd -P); then echo "helperhome=$(looseway "$r")"; else echo "helperhome=$(looseway "$home")"; fi
[ -x "$a/gufi/$2/bin/gufi_query" ] && echo gufi=ok
for b in /usr/local/bin /usr/bin /opt/gufi/bin; do
  [ -x "$b/gufi_query" ] && [ -x "$b/gufi_dir2index" ] || continue
  [ "$(lz_stat %u %u "$b")" = 0 ] || continue
  if [ -z "$(loose "$b")" ] && [ -z "$(loose "$b/gufi_query")" ] && [ -z "$(loose "$b/gufi_dir2index")" ]; then echo "sysgufi=$b"; break; fi
done
exit 0`

// looseBinSh says how another account could change GUFI's tools in $1.
const looseBinSh = transfers.LooseSh + `for p in "$1" "$1/gufi_query" "$1/gufi_dir2index"; do w=$(loose "$p"); [ -n "$w" ] && { printf '%s\n' "$w"; break; }; done; exit 0`

// helperHomeWhy is why the write helper does not run from a person's
// ~/.activate-studio on name: another account could change what is run,
// as looseway says it.
func helperHomeWhy(name, loose string) string {
	how, dir, _ := strings.Cut(loose, " ")
	return "The Studio's write helper runs from your ~/.activate-studio on " + name + ", and " + dir + " " + transfers.LooseSaid(how) + "."
}

// spacePrepareSh keeps ~/.activate-studio to the person alone.
const spacePrepareSh = `umask 077; a="$HOME/.activate-studio"; mkdir -p "$a" && chmod 700 "$a" && echo ready`

// gufiUnpackSh takes the Studio's GUFI, a gzipped tar on standard input,
// into ~/.activate-studio/gufi/$1, whole or not at all, once it runs there.
const gufiUnpackSh = `set -e
umask 077
v=$1
a="$HOME/.activate-studio"; mkdir -p "$a"; chmod 700 "$a"
d="$a/gufi"; mkdir -p "$d"
if [ -x "$d/$v/bin/gufi_query" ]; then cat > /dev/null; echo present; exit 0; fi
t=$(mktemp -d "$d/.part-XXXXXX")
trap 'rm -rf "$t"' EXIT
tar -xzf - -C "$t"
if ! "$t/bin/gufi_query" -h > /dev/null 2> "$t/.err"; then
  echo "GUFI does not run there: $(head -n 2 "$t/.err" | tr '\n' ' ')" >&2; exit 3
fi
rm -f "$t/.err"
rm -rf "$d/$v"
mv "$t" "$d/$v"
trap - EXIT
echo installed`

// spaceFound is what the check found.
type spaceFound struct {
	OK      bool
	Steps   []step
	Target  string // the folder asked for, ~ expanded
	Real    string // its real path, once it exists
	Missing bool
	Home    string
	Info    libraries.SpaceInfo
	GufiBin string
	// SendGufi: the Studio's copy goes to GufiBin first.
	SendGufi bool
	// readable and writable: the account can open the folder, and write in it.
	readable, writable bool
	// Owned: the account owns the folder, or will once it is made.
	Owned bool
	// GufiLoose: another account could change the GUFI found there.
	GufiLoose bool
	// Made: this check made the folder.
	Made bool
	// Closed: no other account can open the folder.
	Closed bool
	// Loose is who else can change what is in the folder, or put another
	// in its place, "" where nobody can.
	Loose string
	// ReadOnly is why the Library does not write in the folder: Loose, and
	// the write helper, which nobody can send elsewhere, cannot run there.
	ReadOnly string
	// Pin is the folder as it is now (libraries.UserDef.Pin), and steady
	// whether no other account could have put it there.
	Pin    string
	steady bool
}

// shareWhy is why the folder cannot be shared from here, "" when it can.
func (f *spaceFound) shareWhy(t *spaceTarget) string {
	switch {
	case t.transport == "ssh":
		return "It is reached over ssh on a Studio only you use, so there is nobody here to share it with."
	case !f.Owned:
		return "Another account owns the folder, and only its owner can share it."
	case f.ReadOnly != "":
		return "Another account could change what a share opens, so it is not shared until the warning above is settled."
	}
	return ""
}

func (f *spaceFound) json() *jsonv.Object {
	create := []any{}
	if f.Missing {
		create = append(create, f.Target)
	}
	return jsonv.NewObject("ok", f.OK, "steps", stepsJSON(f.Steps), "sourceRoot", f.Target, "create", create,
		"account", f.Info.Account, "acl", f.Info.ACL, "fs", f.Info.FS, "owned", f.Owned, "othersWrite", orNil(f.Loose), "readOnly", orNil(f.ReadOnly))
}

func orNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// gufiBundle is the GUFI the Studio carries (the folder above GUFI_BIN,
// when it has a manifest.json), and a version naming its contents; "" for
// none.
func (m *Module) gufiBundle() (dir, version string) {
	m.bundleOnce.Do(func() {
		if m.GufiBin == "" {
			return
		}
		d := filepath.Dir(m.GufiBin)
		for _, f := range []string{"manifest.json", "bin/gufi_query", "bin/gufi_dir2index"} {
			if _, err := os.Stat(filepath.Join(d, f)); err != nil {
				return
			}
		}
		h := sha256.New()
		for _, sub := range []string{"bin", "lib"} {
			var names []string
			filepath.WalkDir(filepath.Join(d, sub), func(p string, e fs.DirEntry, err error) error {
				if err == nil && !e.IsDir() {
					names = append(names, p)
				}
				return nil
			})
			sort.Strings(names)
			for _, p := range names {
				rel, _ := filepath.Rel(d, p)
				fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
				if t, err := os.Readlink(p); err == nil {
					fmt.Fprintf(h, "->%s\x00", t)
					continue
				}
				if fh, err := os.Open(p); err == nil {
					io.Copy(h, fh)
					fh.Close()
				}
			}
		}
		m.bundleDir, m.bundleVer = d, hex.EncodeToString(h.Sum(nil))[:12]
	})
	return m.bundleDir, m.bundleVer
}

// bundleRuns is whether the Studio's GUFI runs on a system uname -sm names.
func bundleRuns(platform string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	switch runtime.GOARCH {
	case "amd64":
		return platform == "Linux x86_64"
	case "arm64":
		return platform == "Linux aarch64"
	}
	return false
}

// spaceAsk is a folder to check: the folder as it was given, whether to
// make it, the GUFI to use when the person named one, and the grants a
// library of it already has.
type spaceAsk struct {
	folder string
	create bool
	gufi   *string
	grants []string
	// folderOnly: what the folder is, without GUFI.
	folderOnly bool
}

// grantsLine is the grants as LOOSE_OK takes them.
func grantsLine(grants []string) string { return strings.Join(grants, " ") }

// checkSpace runs the check over the person's session and reads it. What it
// refuses is what makes a library unusable: a folder that cannot be opened,
// made or is not one, the home folder or one that holds it, the Studio's
// own, and no GUFI. A folder another account can write in or move is
// indexed, read and written as the person, with a warning that says who
// (spaceFound.Loose); where the write helper cannot run, the Library does
// not write in it (spaceFound.ReadOnly).
func (m *Module) checkSpace(ctx context.Context, h probeHost, q spaceAsk) (*spaceFound, error) {
	bundle, version := m.gufiBundle()
	v := version
	if v == "" {
		v = "none"
	}
	flag := "0"
	if q.create {
		flag = "1"
	}
	route := ""
	if q.folderOnly {
		route = "1"
	}
	res, err := m.Pool.Run(ctx, h.Site, h.Key, []string{"bash", "-c", spaceCheckSh, "_", q.folder, v, flag, grantsLine(q.grants), route}, 90*time.Second)
	if err != nil {
		return nil, m.sessionErr(remote.ShortName(h.Site), h.Key, err)
	}
	kv := keyValues(res.Stdout)
	uid, err := strconv.Atoi(kv["uid"])
	if err != nil {
		return nil, apierr.New(502, "%s did not say which account your session reaches: %s", h.Name, lastOrExit(res.Stderr, res.Code))
	}
	user, home := kv["user"], kv["home"]
	f := &spaceFound{Home: home, Info: libraries.SpaceInfo{Account: user, UID: uid, FS: fsName(kv["fs"]), ACL: kv["acl"] == "1"}}
	add := func(s step) { f.Steps = append(f.Steps, s) }
	done := func() *spaceFound { f.OK = allOK(f.Steps); return f }
	add(step{Name: "account", OK: true, Detail: fmt.Sprintf("Your session reaches %s as %s (uid %d). The folder is indexed and read as that account.", h.Name, user, uid)})
	if kv["path"] == "relative" {
		add(step{Name: "folder", Detail: "Give an absolute folder, or one under ~."})
		return done(), nil
	}
	f.Target, f.Real = kv["target"], kv["real"]
	f.readable, f.writable = kv["readable"] == "1", kv["writable"] == "1"
	switch kv["kind"] {
	case "notdir":
		add(step{Name: "folder", Detail: f.Target + " is a file, not a folder."})
		return done(), nil
	case "unreadable":
		add(step{Name: "folder", Detail: "Your account cannot open " + f.Target + "."})
		return done(), nil
	}
	f.Missing, f.Made = kv["exists"] != "1", kv["made"] == "1"
	where := f.Real
	if f.Missing {
		where = posixNormalize(f.Target)
	}
	studio := strings.TrimRight(home, "/") + "/.activate-studio"
	switch {
	case home == "" || !strings.HasPrefix(home, "/"):
		add(step{Name: "folder", Detail: "Your home folder there could not be read, so the Studio has nowhere to keep your index."})
		return done(), nil
	case where == home:
		add(step{Name: "folder", Detail: "That is your home folder, which holds the Studio's own files (~/.activate-studio). Choose a folder inside it, such as " + SuggestedSpace + "."})
		return done(), nil
	case inside(where, studio):
		add(step{Name: "folder", Detail: "That folder is the Studio's own. Choose another."})
		return done(), nil
	case inside(home, where):
		add(step{Name: "folder", Detail: where + " holds your home folder. Choose a folder inside your home folder, such as " + SuggestedSpace + "."})
		return done(), nil
	case !f.Missing && !f.readable:
		add(step{Name: "folder", Detail: "Your account cannot open " + f.Real + "."})
		return done(), nil
	case f.Missing && !f.writable:
		add(step{Name: "folder", Detail: fmt.Sprintf("%s does not exist, or your account cannot open it, and it cannot be made: your account cannot write in %s.", f.Target, f.Real)})
		return done(), nil
	}
	mode, _ := strconv.ParseUint(kv["mode"], 8, 32)
	if f.Missing && kv["acl"] == "" {
		// Whether a folder made there takes ACLs is known once it is made.
		f.Info.ACL = true
	}
	f.Closed = f.Missing || mode&0o077 == 0
	f.Owned = f.Missing || kv["owner"] == strconv.Itoa(uid)
	if q.create && !f.Missing && !f.Made && !f.Owned {
		add(step{Name: "folder", Detail: f.Real + " was made by another account meanwhile. Choose another folder."})
		return done(), nil
	}
	if f.Pin, f.steady = kv["pin"], kv["unsafe"] == ""; f.Missing {
		f.Pin = ""
	}
	// others is a finding about who else can change the folder: what they
	// can do, and what to change so that they cannot. The write helper
	// cannot be sent elsewhere by it; without one, the folder is read only.
	noHelper := m.canWrite(kv["platform"])
	if noHelper == "" && kv["noexec"] == "1" {
		noHelper = "The Studio's write helper cannot run from your home folder there, which is mounted noexec."
	}
	if u := kv["helperhome"]; noHelper == "" && u != "" {
		noHelper = helperHomeWhy(h.Name, u)
	}
	lib := "The Library writes in it as you, and a write cannot be sent outside it."
	if !f.writable && !f.Missing {
		lib = "Your account cannot write in the folder itself, so the Library writes only in what it holds that your account can change."
	}
	others := func(name, found, can, fix string) {
		if f.Loose != "" {
			f.Loose += " "
		}
		f.Loose += found + "."
		detail := found + ". " + can + " " + lib + " " + fix
		if noHelper != "" {
			if f.ReadOnly == "" {
				f.ReadOnly = found + ", so it is read only here: another account could send what the Studio writes there elsewhere. " + noHelper + " " + fix
			}
			detail = found + ". It is indexed and read as you, and the Library does not write in it, since another account could send what the Studio writes there elsewhere. " + noHelper + " " + fix
		}
		add(step{Name: name, OK: true, Warn: true, Detail: strings.TrimSpace(detail)})
	}
	const (
		they  = "They can add, change and remove what is in it."
		again = " and refresh the library, or check again before adding it."
	)
	self := ""
	if !f.Missing {
		self = kv["self"]
	}
	switch {
	case f.Missing:
		add(step{Name: "folder", OK: true, Detail: f.Target + " does not exist yet. It is made, closed to other accounts, when you confirm."})
	case !f.Owned:
		found, can := fmt.Sprintf("%s belongs to another account (uid %s)", f.Real, kv["owner"]), "That account can add, change and remove what is in it."
		switch {
		case mode&0o002 != 0:
			found += fmt.Sprintf(", and every account there can write in it (mode %s)", kv["mode"])
			can = they
		case mode&0o020 != 0:
			found += fmt.Sprintf(", and members of the group %s can write in it (mode %s)", kv["fgroup"], kv["mode"])
			can = they
		}
		others("folder", found, can, "")
	case self == "other":
		others("folder", fmt.Sprintf("Every account there can write in %s (mode %s)", f.Real, kv["mode"]), they, "To keep it to yourself, run chmod go-w on it"+again)
	case self == "group":
		others("folder", fmt.Sprintf("Members of the group %s can write in %s (mode %s)", kv["fgroup"], f.Real, kv["mode"]), they, "To keep it to yourself, run chmod g-w on it"+again)
	case self == "acl":
		others("folder", fmt.Sprintf("The ACL on %s lets another account or group write in it", f.Real), they, "To keep it to yourself, take that entry off with setfacl"+again)
	case self != "":
		others("folder", "Who can write in "+f.Real+" could not be read", "Another account may be able to.", "Check it there, then"+strings.TrimPrefix(again, " and"))
	case mode&0o055 != 0:
		add(step{Name: "folder", OK: true, Warn: true, Detail: fmt.Sprintf("Other accounts can open %s now (mode %s). The index stays in your ~/.activate-studio, which only you can open; to keep the files to yourself too, run chmod 700 on the folder.", f.Real, kv["mode"])})
	default:
		add(step{Name: "folder", OK: true, Detail: "Only you can open " + f.Real + "."})
	}
	if u := kv["unsafe"]; u != "" {
		why, dir, _ := strings.Cut(u, " ")
		others("path", fmt.Sprintf("%s %s, so another account could move or replace a folder under it", dir, transfers.LooseSaid(why)),
			"If this folder is replaced, the Library stops writing until you add it again.", "To avoid that, use a folder under your home folder.")
	} else {
		add(step{Name: "path", OK: true, Detail: "No other account can move or replace the folders on the way to it."})
	}
	if s := kv["studio"]; s != "" && s != strconv.Itoa(uid) {
		add(step{Name: "index", Detail: "~/.activate-studio belongs to another account, so the Studio cannot keep your index there."})
	}
	switch {
	case !f.Owned || f.ReadOnly != "" || remote.IsSSH(h.Site):
	case kv["acl"] == "1":
		add(step{Name: "sharing", OK: true, Detail: fmt.Sprintf("Its file system (%s) takes POSIX ACLs, so it can be shared with named people and groups.", f.Info.FS)})
	case kv["acl"] == "0":
		add(step{Name: "sharing", OK: true, Warn: true, Detail: fmt.Sprintf("Its file system (%s) does not take POSIX ACLs here, so it can be shared with every account for reading only.", f.Info.FS)})
	}
	own := studio + "/gufi/" + version + "/bin"
	switch {
	case q.folderOnly:
	case deref(q.gufi) != "":
		m.foundGufi(ctx, h, f, q.gufi, home, strings.HasPrefix(kv["platform"], "Darwin"))
	case version != "" && kv["gufi"] == "ok":
		f.GufiBin = own
		add(step{Name: "gufi", OK: true, Detail: "GUFI runs from your own copy in ~/.activate-studio."})
	case version != "" && bundleRuns(kv["platform"]):
		f.GufiBin, f.SendGufi = own, true
		add(step{Name: "gufi", OK: true, Detail: "The Studio copies its GUFI (about 9 MB, once) to your ~/.activate-studio, where only you can change it."})
	case kv["sysgufi"] != "":
		f.GufiBin = kv["sysgufi"]
		add(step{Name: "gufi", OK: true, Detail: "GUFI runs from " + f.GufiBin + ", installed for every account."})
	default:
		if m.foundGufi(ctx, h, f, nil, home, strings.HasPrefix(kv["platform"], "Darwin")); f.GufiBin == "" && bundle != "" {
			last := &f.Steps[len(f.Steps)-1]
			last.Detail += " The Studio's copy of GUFI is not built for " + strings.TrimSpace(kv["platform"]) + "."
		}
	}
	return done(), nil
}

// foundGufi is the GUFI installed there: in the folder given, else where
// the test of a build looks. One another account could change is used, as
// the person who named or installed it knows it, with a warning.
func (m *Module) foundGufi(ctx context.Context, h probeHost, f *spaceFound, given *string, home string, mac bool) {
	c := connected{Home: home, Mac: mac}
	g := m.gufiStep(h, m.runner(ctx, h), &f.Steps, c.expand(given), home, mac, true)
	if !g.Ran {
		return
	}
	f.GufiBin = g.Bin
	if r, err := m.Pool.Run(ctx, h.Site, h.Key, []string{"bash", "-c", looseBinSh, "_", g.Bin}, 60*time.Second); err == nil {
		if how := lastLine(r.Stdout); how != "" {
			last := &f.Steps[len(f.Steps)-1]
			last.Warn, f.GufiLoose = true, true
			last.Detail += " It " + transfers.LooseSaid(how) + ", and it runs as you."
		}
	}
}

// sendGufi puts the Studio's GUFI in the person's ~/.activate-studio.
func (m *Module) sendGufi(ctx context.Context, site, key string) error {
	dir, version := m.gufiBundle()
	if dir == "" {
		return apierr.New(409, "The Studio has no copy of GUFI to send.")
	}
	var err error
	m.queued(keyID(key)+"|"+site+"|gufi/"+version, func() {
		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(writeTarGz(pw, dir, []string{"bin", "lib", "licenses", "manifest.json", "README.txt"}))
		}()
		var r remote.Result
		r, err = m.Pool.RunOnce(ctx, site, key, []string{"bash", "-c", gufiUnpackSh, "_", version}, 15*time.Minute, pr)
		pr.Close()
		if err != nil {
			err = apierr.New(502, "GUFI could not be copied to %s: %s", remote.ShortName(site), err.Error())
			return
		}
		if r.Code != 0 || !(strings.Contains(r.Stdout, "installed") || strings.Contains(r.Stdout, "present")) {
			err = apierr.New(502, "GUFI could not be copied to your ~/.activate-studio on %s: %s", remote.ShortName(site), lastOrExit(r.Stderr, r.Code))
		}
	})
	return err
}

// writeTarGz writes the named entries of dir, files, folders and links,
// as a gzipped tar.
func writeTarGz(w io.Writer, dir string, names []string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		root := filepath.Join(dir, n)
		if _, err := os.Lstat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			fi, err := os.Lstat(p)
			if err != nil {
				return err
			}
			link := ""
			switch {
			case fi.Mode()&fs.ModeSymlink != 0:
				if link, err = os.Readlink(p); err != nil {
					return err
				}
				if filepath.IsAbs(link) || strings.HasPrefix(filepath.Clean(link), "..") {
					return nil
				}
			case fi.IsDir(), fi.Mode().IsRegular():
			default:
				return nil
			}
			h, err := tar.FileInfoHeader(fi, link)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			h.Name = filepath.ToSlash(rel)
			if fi.IsDir() {
				h.Name += "/"
			}
			h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			if fi.Mode().IsRegular() {
				fh, err := os.Open(p)
				if err != nil {
					return err
				}
				_, err = io.Copy(tw, fh)
				fh.Close()
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// spaceTarget is the system a space is made on and how it is reached.
type spaceTarget struct {
	owner, site, key string
	// transport is pw (the person's own key) or ssh (the keys of the
	// account the Studio runs as, on a Studio one person uses).
	transport string
	name      string
	host      probeHost
	conn      buildConn
}

// spaceSession is the system a space request names, with the person's own
// credential for it, or why there is none: one of their clusters over pw
// ssh (the Studio's own cluster when none is named), or, on a Studio one
// person uses with sign-in off, a host over plain ssh with their keys.
func (m *Module) spaceSession(w who, b *jsonv.Object) (*spaceTarget, error) {
	owner := w.owner()
	if owner == "" {
		return nil, apierr.New(401, "Sign in to the Studio to make your library.")
	}
	if b != nil && str(field(b, "destination")) != "" {
		if !w.sshAllowed() {
			return nil, errSSHSingle()
		}
		d, err := sshDest(field(b, "destination"), field(b, "port"), field(b, "sshOptions"))
		if err != nil {
			return nil, err
		}
		site, err := remote.SSHSite(d.Destination, d.Port, d.SSHOptions)
		if err != nil {
			return nil, err
		}
		via := ""
		if j := remote.JumpOf(d.SSHOptions); j != "" {
			via = " through " + j
		}
		return &spaceTarget{owner: owner, site: site, transport: "ssh", name: d.Destination,
			host: probeHost{Site: site, Name: d.Destination, Reached: d.Destination + portText(d.Port) + via,
				Advice: func(msg string) string { return sshAdvice(msg, d.Destination, d.Port) }},
			conn: buildConn{Destination: ptr(d.Destination), Port: d.Port, SSHOptions: d.SSHOptions, Site: d.Destination}}, nil
	}
	site := ""
	if b != nil {
		site = str(field(b, "cluster"))
	}
	if site != "" {
		if !clusterRe.MatchString(site) {
			return nil, apierr.New(400, "system: the name of one of your clusters")
		}
	} else if site = HomeCluster(); site == "" {
		return nil, apierr.New(409, "Choose one of your remote computers for your library. This Studio does not know the cluster it runs beside; the operator can set STUDIO_HOME_CLUSTER to offer it first.")
	}
	return m.pwSpaceTarget(w, owner, site)
}

// pwSpaceTarget is site over pw ssh with the person's own key.
func (m *Module) pwSpaceTarget(w who, owner, site string) (*spaceTarget, error) {
	key := m.keyOf(w, owner)
	if key == "" {
		if owner == single {
			return nil, apierr.New(409, "The pw CLI on this computer is not signed in. Run pw auth, then try again.")
		}
		return nil, apierr.New(409, "A folder of your own on a remote computer is reached with your ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then come back here.")
	}
	name := remote.ShortName(site)
	return &spaceTarget{owner: owner, site: site, key: key, transport: "pw", name: name,
		host: probeHost{Site: site, Key: key, Name: name, Reached: name, Advice: func(msg string) string { return pwAdvice(msg, site) }},
		conn: buildConn{SSHOptions: []string{}, Site: site}}, nil
}

// spaceStatus is what the Add library dialog shows for a space.
func (m *Module) spaceStatus(w who) *jsonv.Object {
	why := ""
	site := HomeCluster()
	if _, err := m.spaceSession(w, nil); err != nil {
		why = err.Error()
	}
	spaces := []any{}
	if owner := w.owner(); owner != "" {
		for _, d := range m.load(owner) {
			if d.Space != nil {
				spaces = append(spaces, d.ID)
			}
		}
	}
	cluster := ""
	if site != "" {
		cluster = remote.ShortName(site)
	}
	// Which credential reaches the cluster for the person: their ACTIVATE
	// sign-in, a key they pasted, or none yet.
	credential := ""
	if owner := w.owner(); owner != "" && owner != single {
		if m.SignedIn != nil && m.SignedIn(owner) {
			credential = "sign-in"
		} else if m.keyOf(w, owner) != "" {
			credential = "key"
		}
	}
	// Without a cluster of its own, a Studio still offers a space on any
	// system the person reaches: with pw ssh, or plain ssh where allowed.
	if why != "" && site == "" && w.owner() != "" && (m.keyOf(w, w.owner()) != "" || w.sshAllowed()) {
		why = ""
	}
	return jsonv.NewObject("available", why == "", "why", why, "cluster", cluster, "suggested", SuggestedSpace, "spaces", spaces,
		"credential", credential, "signInOffered", w.Auth && os.Getenv("DISABLE_PERSONAL_KEYS") != "1",
		"pw", m.keyOf(w, w.owner()) != "", "ssh", w.sshAllowed())
}

func spaceFolder(b *jsonv.Object) (string, error) {
	v := field(b, "sourceRoot")
	if v == nil || str(v) == "" {
		return SuggestedSpace, nil
	}
	p, err := remotePath(v, "folder")
	if err != nil {
		return "", err
	}
	return *p, nil
}

// spaceRequest is the folder a request asks to index, and the parts of a
// build it may name: the index folder and GUFI's.
func spaceRequest(b *jsonv.Object) (spaceAsk, *string, error) {
	folder, err := spaceFolder(b)
	if err != nil {
		return spaceAsk{}, nil, err
	}
	idx, err := remotePath(field(b, "indexRoot"), "index folder")
	if err != nil {
		return spaceAsk{}, nil, err
	}
	bin, err := remotePath(field(b, "gufiBin"), "GUFI bin directory")
	if err != nil {
		return spaceAsk{}, nil, err
	}
	return spaceAsk{folder: folder, gufi: bin}, idx, nil
}

// myLibraryWhy is why the folder cannot be the person's My library, where
// their chats are saved: it must take writes, and what indexes it must be
// nobody else's to change, and its index stays where only they can open
// it. "" when it can be.
func myLibraryWhy(f *spaceFound, idx *string) string {
	switch {
	case f.Loose != "":
		return "My library is where your chats are saved, so it has to be a folder no other account can change."
	case f.GufiLoose:
		return "My library is indexed only with a GUFI no other account can change, which runs as you."
	case deref(idx) != "":
		return "My library keeps its index in ~/.activate-studio, which only you can open. Leave the index folder empty."
	}
	return ""
}

// testSpace is the check alone, with what the build would find: where the
// index goes and what reads the files there.
func (m *Module) testSpace(ctx context.Context, w who, b *jsonv.Object) (*jsonv.Object, error) {
	t, err := m.spaceSession(w, b)
	if err != nil {
		return nil, err
	}
	q, idx, err := spaceRequest(b)
	if err != nil {
		return nil, err
	}
	f, err := m.checkSpace(ctx, t.host, q)
	if err != nil {
		return nil, err
	}
	o := f.json()
	if f.OK {
		src := f.Real
		if f.Missing {
			src = f.Target
		}
		p := m.probeFolders(ctx, t.host, buildSpec{SourceRoot: src, IndexRoot: idx}, gufiFound{Bin: f.GufiBin, Ran: true})
		for _, s := range p.Steps {
			if !s.OK || s.Name == "index" || s.Name == "extract" {
				f.Steps = append(f.Steps, s)
			}
		}
		f.OK = allOK(f.Steps)
		o = f.json()
		create := []any{}
		for _, c := range p.Create {
			create = append(create, c)
		}
		if f.OK {
			o.Set("create", create)
		}
	}
	why := f.shareWhy(t)
	return o.Set("system", t.name).Set("canShare", f.OK && why == "").Set("shareWhy", orNil(why)).Set("myLibraryWhy", orNil(myLibraryWhy(f, idx))), nil
}

// wantsMyLibrary is whether a folder becomes a space. On a Studio people
// sign in to that is their My library, which they ask for (myLibrary; a
// request that does not say is one, as before); any other folder of theirs
// is a library named by its folder.
func (m *Module) wantsMyLibrary(b *jsonv.Object, f *spaceFound, idx *string) (bool, error) {
	v, given := field(b, "myLibrary").(bool)
	if !m.Users.AuthEnabled {
		// On a Studio one person uses, the main library is theirs. A folder
		// closed to every other account is written so that it stays closed.
		return f.Loose == "" && f.Owned && f.Closed, nil
	}
	if given && !v {
		return false, nil
	}
	if why := myLibraryWhy(f, idx); why != "" {
		if given {
			return false, apierr.New(409, "%s", why)
		}
		return false, nil
	}
	return true, nil
}

// addSpace makes a library of a folder on one of the person's systems: the
// check, the folder when confirmed, GUFI, the build of its index as them,
// and the share asked for with it.
func (m *Module) addSpace(ctx context.Context, w who, b *jsonv.Object) (*libraries.UserDef, int, string, error) {
	t, err := m.spaceSession(w, b)
	if err != nil {
		return nil, 0, "", err
	}
	owner, site, key := t.owner, t.site, t.key
	q, idx, err := spaceRequest(b)
	if err != nil {
		return nil, 0, "", err
	}
	f, err := m.checkSpace(ctx, t.host, q)
	if err != nil {
		return nil, 0, "", err
	}
	if !f.OK {
		return nil, 0, "", spaceNotPassed(f)
	}
	var share *shareAsk
	if v, ok := field(b, "share").(*jsonv.Object); ok {
		if share, err = shareRequest(v, f.Info); err != nil {
			return nil, 0, "", err
		}
		if why := f.shareWhy(t); why != "" {
			return nil, 0, "", apierr.New(409, "%s", why)
		}
	}
	mine, err := m.wantsMyLibrary(b, f, idx)
	if err != nil {
		return nil, 0, "", err
	}
	if f.Missing {
		if field(b, "create") != true {
			return nil, 0, "", apierr.New(409, "%s does not exist yet. Confirm to create it.", f.Target)
		}
		q.create = true
		if f, err = m.checkSpace(ctx, t.host, q); err != nil {
			return nil, 0, "", err
		}
		if !f.OK {
			return nil, 0, "", spaceNotPassed(f)
		}
		if f.Missing {
			return nil, 0, "", apierr.New(502, "%s could not be made.", f.Target)
		}
		if share != nil && f.shareWhy(t) != "" {
			return nil, 0, "", apierr.New(409, "%s", f.shareWhy(t))
		}
		if mine, err = m.wantsMyLibrary(b, f, idx); err != nil {
			return nil, 0, "", err
		}
	}
	// A folder made My library is the person's alone.
	if share != nil {
		mine = mine && !m.Users.AuthEnabled
	}
	for _, d := range m.load(owner) {
		if d.Build != nil && d.Shared == nil && d.Transport == t.transport && d.Site == t.conn.Site && intOf(d.Port) == t.conn.Port && d.Build.SourceRoot == f.Real {
			return nil, 0, "", apierr.New(409, "%s is already your library %s.", f.Real, d.ShownLabel(m.Users.AuthEnabled))
		}
	}
	if r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", spacePrepareSh}, 60*time.Second); err != nil || !strings.Contains(r.Stdout, "ready") {
		return nil, 0, "", apierr.New(502, "Your ~/.activate-studio could not be kept to you alone on %s.", t.name)
	}
	if f.SendGufi {
		if err := m.sendGufi(ctx, site, key); err != nil {
			return nil, 0, "", err
		}
	}
	bin := f.GufiBin
	r := &buildReq{owner: owner, host: t.host, conn: t.conn, spec: buildSpec{SourceRoot: f.Real, IndexRoot: idx, GufiBin: &bin}}
	info := f.Info
	o := &addOpts{found: f}
	if mine {
		o.Space = &info
	}
	d, job, err := m.addBuildFor(ctx, w, t.transport, r, b, o)
	if err != nil || share == nil {
		return d, job, "", err
	}
	// The library is there whether or not the share could be set.
	if _, err := m.addShare(ctx, w, d.ID, share.object()); err != nil {
		return d, job, err.Error(), nil
	}
	return d, job, "", nil
}

// foundAt is what the check finds of a library's folder now; nil where it
// could not be read.
func (m *Module) foundAt(ctx context.Context, h probeHost, folder string, grants []string) *spaceFound {
	f, err := m.checkSpace(ctx, h, spaceAsk{folder: folder, grants: grants, folderOnly: true})
	if err != nil || f.Real == "" || f.Missing {
		return nil
	}
	return f
}

// keep records on a library what the check found of its folder: whether
// its owner can share it, who else can change it, whether the Library
// writes in it, and which folder it is. Where the write helper cannot run,
// each write checks again, so a folder that could not be read is left as it
// was recorded.
func (f *spaceFound) keep(d *libraries.UserDef) {
	if f != nil {
		// What another account's session finds says nothing of this one's.
		if _, uid, ok := d.Account(); ok && uid != f.Info.UID {
			return
		}
		// A folder another account may have put in its place is not the
		// library's: what was found of it says nothing.
		if d.Pin != "" && f.Pin != "" && d.Pin != f.Pin {
			if !f.steady || d.Pin[strings.Index(d.Pin, ":"):] != f.Pin[strings.Index(f.Pin, ":"):] {
				return
			}
			d.Pin = f.Pin
		}
	}
	d.Guard = true
	if f == nil {
		return
	}
	d.ReadOnly, d.Loose = nil, nil
	if f.ReadOnly != "" {
		d.ReadOnly = ptr(f.ReadOnly)
	}
	if f.Loose != "" {
		d.Loose = ptr(f.Loose)
	}
	if d.Pin == "" {
		d.Pin = f.Pin
	}
	if d.Space != nil {
		return
	}
	d.Own = nil
	if f.Owned {
		info := f.Info
		d.Own = &info
	}
}

func spaceNotPassed(f *spaceFound) error {
	for _, s := range f.Steps {
		if !s.OK {
			return apierr.New(400, "%s", s.Detail)
		}
	}
	return apierr.New(400, "The check did not pass.")
}

// spaceState is kept per module for the space's GUFI copy.
type spaceState struct {
	bundleOnce sync.Once
	bundleDir  string
	bundleVer  string
}

// fsName is a file system's name as the folder check shows it. Where
// findmnt is missing, stat -f names one magic number for ext2, ext3 and
// ext4 alike, which read as wrong on ext4, the usual one.
func fsName(fs string) string {
	if fs == "ext2/ext3" {
		return "ext2, ext3 or ext4"
	}
	return fs
}
