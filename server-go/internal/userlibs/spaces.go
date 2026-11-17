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
	"regexp"
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
)

// A person's own space (docs/MULTI-USER.md, Personal spaces): a folder they
// own on the cluster the Studio runs beside, indexed, read and written as
// them over their own session. The index goes in their ~/.activate-studio,
// which only they can open, and GUFI runs from their own copy there, so no
// other account can change what runs as them.

// SuggestedSpace is the folder offered first.
const SuggestedSpace = "~/studio"

var homeClusterRe = regexp.MustCompile(`^(?:pw://[A-Za-z0-9_.@-]{1,120}/)?[A-Za-z0-9][A-Za-z0-9_.-]{0,119}$`)

// HomeCluster is the cluster spaces are made on: STUDIO_HOME_CLUSTER, else
// the one a workflow deployment runs on (pw://PW_USER/PW_POOL_NAME); "" for
// none.
func HomeCluster() string {
	v := strings.TrimSpace(os.Getenv("STUDIO_HOME_CLUSTER"))
	if v == "" {
		u, p := os.Getenv("PW_USER"), os.Getenv("PW_POOL_NAME")
		if u == "" || p == "" {
			return ""
		}
		v = "pw://" + u + "/" + p
	}
	if !homeClusterRe.MatchString(v) {
		return ""
	}
	return v
}

// spaceCheckSh reads, as the person, what a space at $1 needs: the account,
// the folder (made, open to them alone, when $3 is 1), whether another
// account could swap a folder on the way to it, whether its file system
// takes ACLs, and where GUFI ($2 the Studio's copy) can run from.
const spaceCheckSh = `umask 077
uid=$(id -u) || exit 2
user=$(id -un)
home=$(cd ~ 2>/dev/null && pwd -P)
printf 'uid=%s\nuser=%s\nhome=%s\nplatform=%s\n' "$uid" "$user" "$home" "$(uname -sm)"
st() { stat -c "$1" -- "$2" 2>/dev/null; }
loose() {
  o=$(st %u "$1"); m=$(st %a "$1"); g=$(st %G "$1")
  if [ -z "$o" ] || [ -z "$m" ]; then echo unknown; return; fi
  if [ "$o" != 0 ] && [ "$o" != "$uid" ]; then echo owner; return; fi
  mm=$((8#$m))
  [ $((mm & 512)) -ne 0 ] && return
  if [ $((mm & 2)) -ne 0 ]; then echo other; return; fi
  if [ $((mm & 16)) -ne 0 ] && [ "$g" != "$user" ]; then echo group; return; fi
}
p=$1
case "$p" in "~") p=$home;; "~/"*) p="$home/${p#"~/"}";; esac
case "$p" in /*) ;; *) echo path=relative; exit 0;; esac
echo "target=$p"
if [ "$3" = 1 ] && [ ! -e "$p" ] && [ ! -L "$p" ]; then mkdir -p -- "$p" 2>/dev/null && echo made=1; fi
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
echo "owner=$(st %u "$r")"
echo "mode=$(st %a "$r")"
echo "fgroup=$(st %G "$r")"
echo "fs=$(stat -f -c %T -- "$r" 2>/dev/null)"
d=$r
while [ "$d" != / ]; do
  d=${d%/*}; [ -n "$d" ] || d=/
  w=$(loose "$d")
  if [ -n "$w" ]; then echo "unsafe=$w $d"; break; fi
done
if [ "$(st %u "$r")" = "$uid" ] && [ -w "$r" ]; then
  if t=$(mktemp -- "$r/.studio-acl-XXXXXX" 2>/dev/null); then
    if command -v setfacl >/dev/null 2>&1 && setfacl -m "u:$uid:r" -- "$t" 2>/dev/null; then echo acl=1; else echo acl=0; fi
    rm -f -- "$t"
  fi
fi
a="$home/.activate-studio"
if [ -e "$a" ] || [ -L "$a" ]; then echo "studio=$(st %u "$a")"; fi
[ -x "$a/gufi/$2/bin/gufi_query" ] && echo gufi=ok
for b in /usr/local/bin /usr/bin /opt/gufi/bin; do
  [ -x "$b/gufi_query" ] && [ -x "$b/gufi_dir2index" ] || continue
  [ "$(st %u "$b")" = 0 ] || continue
  if [ -z "$(loose "$b")" ] && [ -z "$(loose "$b/gufi_query")" ] && [ -z "$(loose "$b/gufi_dir2index")" ]; then echo "sysgufi=$b"; break; fi
done
exit 0`

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
}

func (f *spaceFound) json() *jsonv.Object {
	create := []any{}
	if f.Missing {
		create = append(create, f.Target)
	}
	return jsonv.NewObject("ok", f.OK, "steps", stepsJSON(f.Steps), "sourceRoot", f.Target, "create", create,
		"account", f.Info.Account, "acl", f.Info.ACL, "fs", f.Info.FS)
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

// checkSpace runs the check over the person's session and reads it.
func (m *Module) checkSpace(ctx context.Context, site, key, folder string, create bool) (*spaceFound, error) {
	bundle, version := m.gufiBundle()
	v := version
	if v == "" {
		v = "none"
	}
	flag := "0"
	if create {
		flag = "1"
	}
	res, err := m.Pool.Run(ctx, site, key, []string{"bash", "-c", spaceCheckSh, "_", folder, v, flag}, 90*time.Second)
	if err != nil {
		return nil, apierr.New(502, "Your session to %s did not open: %s", remote.SiteName(site), err.Error())
	}
	kv := keyValues(res.Stdout)
	uid, err := strconv.Atoi(kv["uid"])
	if err != nil {
		return nil, apierr.New(502, "%s did not say which account your session reaches: %s", remote.SiteName(site), lastOrExit(res.Stderr, res.Code))
	}
	user, home := kv["user"], kv["home"]
	f := &spaceFound{Home: home, Info: libraries.SpaceInfo{Account: user, UID: uid, FS: kv["fs"], ACL: kv["acl"] == "1"}}
	add := func(s step) { f.Steps = append(f.Steps, s) }
	done := func() *spaceFound { f.OK = allOK(f.Steps); return f }
	add(step{Name: "account", OK: true, Detail: fmt.Sprintf("Your session reaches %s as %s (uid %d). The space is read and written as that account.", remote.SiteName(site), user, uid)})
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
	exists := kv["exists"] == "1"
	f.Missing = !exists
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
	}
	owner := kv["owner"]
	mode, _ := strconv.ParseUint(kv["mode"], 8, 32)
	mine := owner == strconv.Itoa(uid)
	switch {
	case f.Missing && !mine:
		add(step{Name: "folder", Detail: fmt.Sprintf("%s does not exist, and it would be made in %s, which another account owns.", f.Target, f.Real)})
	case f.Missing:
		add(step{Name: "folder", OK: true, Detail: f.Target + " does not exist yet. It is made, open to you alone, when you confirm."})
	case !mine:
		add(step{Name: "folder", Detail: fmt.Sprintf("%s belongs to another account (uid %s). A space must be a folder you own.", f.Real, owner)})
	case mode&0o002 != 0:
		add(step{Name: "folder", Detail: fmt.Sprintf("Every account there can write in %s (mode %s). Make it yours alone first (chmod o-w), or choose another folder.", f.Real, kv["mode"])})
	case mode&0o020 != 0 && kv["fgroup"] != user:
		add(step{Name: "folder", Detail: fmt.Sprintf("Members of the group %s can write in %s (mode %s). Make it yours alone first (chmod g-w), or choose another folder.", kv["fgroup"], f.Real, kv["mode"])})
	case mode&0o055 != 0:
		add(step{Name: "folder", OK: true, Warn: true, Detail: fmt.Sprintf("Other accounts can open %s now (mode %s). The index stays in your ~/.activate-studio, which only you can open; to keep the files to yourself too, run chmod 700 on the folder.", f.Real, kv["mode"])})
	default:
		add(step{Name: "folder", OK: true, Detail: "Only you can open " + f.Real + "."})
	}
	if u := kv["unsafe"]; u != "" {
		why, dir, _ := strings.Cut(u, " ")
		said := map[string]string{"owner": "belongs to another account", "other": "can be written by every account", "group": "can be written by a group", "unknown": "could not be read"}[why]
		add(step{Name: "path", Detail: fmt.Sprintf("%s %s, so another account could move or replace a folder under it. Choose a folder under your home folder.", dir, said)})
	} else {
		add(step{Name: "path", OK: true, Detail: "No other account can move or replace the folders on the way to it."})
	}
	if s := kv["studio"]; s != "" && s != strconv.Itoa(uid) {
		add(step{Name: "index", Detail: "~/.activate-studio belongs to another account, so the Studio cannot keep your index there."})
	}
	switch kv["acl"] {
	case "1":
		add(step{Name: "sharing", OK: true, Detail: fmt.Sprintf("Its file system (%s) takes POSIX ACLs, so you can share single folders with named people.", kv["fs"])})
	case "0":
		add(step{Name: "sharing", OK: true, Warn: true, Detail: fmt.Sprintf("Its file system (%s) does not take POSIX ACLs here. You can share with everyone on the cluster, or with a group you belong to, by mode bits.", kv["fs"])})
	}
	own := studio + "/gufi/" + version + "/bin"
	switch {
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
		why := "the Studio has no copy of GUFI to send"
		if bundle != "" {
			why = "the Studio's copy of GUFI is not built for " + strings.TrimSpace(kv["platform"])
		}
		add(step{Name: "gufi", Detail: fmt.Sprintf("There is no GUFI there that only root can change, and %s.", why)})
	}
	return done(), nil
}

// sendGufi puts the Studio's GUFI in the person's ~/.activate-studio.
func (m *Module) sendGufi(ctx context.Context, site, key string) error {
	dir, version := m.gufiBundle()
	if dir == "" {
		return apierr.New(409, "The Studio has no copy of GUFI to send.")
	}
	var err error
	m.queued(slice16(key)+"|"+site+"|gufi/"+version, func() {
		pr, pw := io.Pipe()
		go func() {
			pw.CloseWithError(writeTarGz(pw, dir, []string{"bin", "lib", "licenses", "manifest.json", "README.txt"}))
		}()
		var r remote.Result
		r, err = m.Pool.RunOnce(ctx, site, key, []string{"bash", "-c", gufiUnpackSh, "_", version}, 15*time.Minute, pr)
		pr.Close()
		if err != nil {
			err = apierr.New(502, "GUFI could not be copied to %s: %s", remote.SiteName(site), err.Error())
			return
		}
		if r.Code != 0 || !(strings.Contains(r.Stdout, "installed") || strings.Contains(r.Stdout, "present")) {
			err = apierr.New(502, "GUFI could not be copied to your ~/.activate-studio on %s: %s", remote.SiteName(site), lastOrExit(r.Stderr, r.Code))
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

// spaceSession is the cluster and the person's key for their space, or why
// there is none.
func (m *Module) spaceSession(w who) (owner, site, key string, err error) {
	site = HomeCluster()
	if site == "" {
		return "", "", "", apierr.New(409, "This Studio does not know the cluster it runs beside, so it cannot make spaces. The operator can set STUDIO_HOME_CLUSTER.")
	}
	owner = w.owner()
	if owner == "" {
		return "", "", "", apierr.New(401, "Sign in to make a space.")
	}
	key = m.keyOf(w, owner)
	if key == "" {
		if owner == single {
			return "", "", "", apierr.New(409, "The pw CLI on this computer is not signed in. Run pw auth, then try again.")
		}
		return "", "", "", apierr.New(409, "Sign the pw CLI in under Settings, Model access, so the Studio can reach the cluster as you.")
	}
	return owner, site, key, nil
}

// spaceStatus is what the Add library dialog shows for a space.
func (m *Module) spaceStatus(w who) *jsonv.Object {
	why := ""
	site := HomeCluster()
	if _, _, _, err := m.spaceSession(w); err != nil {
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
		cluster = remote.SiteName(site)
	}
	return jsonv.NewObject("available", why == "", "why", why, "cluster", cluster, "suggested", SuggestedSpace, "spaces", spaces)
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

// testSpace is the check alone.
func (m *Module) testSpace(ctx context.Context, w who, b *jsonv.Object) (*jsonv.Object, error) {
	_, site, key, err := m.spaceSession(w)
	if err != nil {
		return nil, err
	}
	folder, err := spaceFolder(b)
	if err != nil {
		return nil, err
	}
	f, err := m.checkSpace(ctx, site, key, folder, false)
	if err != nil {
		return nil, err
	}
	return f.json(), nil
}

// addSpace makes a person's space: the check, the folder when confirmed,
// their own GUFI, and the build of its index, as them.
func (m *Module) addSpace(ctx context.Context, w who, b *jsonv.Object) (*libraries.UserDef, int, error) {
	owner, site, key, err := m.spaceSession(w)
	if err != nil {
		return nil, 0, err
	}
	folder, err := spaceFolder(b)
	if err != nil {
		return nil, 0, err
	}
	f, err := m.checkSpace(ctx, site, key, folder, false)
	if err != nil {
		return nil, 0, err
	}
	if !f.OK {
		return nil, 0, spaceNotPassed(f)
	}
	if f.Missing {
		if field(b, "create") != true {
			return nil, 0, apierr.New(409, "%s does not exist yet. Confirm to create it.", f.Target)
		}
		if f, err = m.checkSpace(ctx, site, key, folder, true); err != nil {
			return nil, 0, err
		}
		if !f.OK {
			return nil, 0, spaceNotPassed(f)
		}
		if f.Missing {
			return nil, 0, apierr.New(502, "%s could not be made.", f.Target)
		}
	}
	if r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", spacePrepareSh}, 60*time.Second); err != nil || !strings.Contains(r.Stdout, "ready") {
		return nil, 0, apierr.New(502, "Your ~/.activate-studio could not be kept to you alone on %s.", remote.SiteName(site))
	}
	if f.SendGufi {
		if err := m.sendGufi(ctx, site, key); err != nil {
			return nil, 0, err
		}
	}
	for _, d := range m.load(owner) {
		if d.Space != nil && d.Build != nil && d.Site == site && d.Build.SourceRoot == f.Real {
			return nil, 0, apierr.New(409, "%s is already your space %s.", f.Real, d.Label)
		}
	}
	bin := f.GufiBin
	r := &buildReq{owner: owner,
		host: probeHost{Site: site, Key: key, Name: remote.SiteName(site), Reached: remote.SiteName(site),
			Advice: func(msg string) string { return pwAdvice(msg, site) }},
		conn: buildConn{SSHOptions: []string{}, Site: site},
		spec: buildSpec{SourceRoot: f.Real, GufiBin: &bin}}
	info := f.Info
	return m.addBuildFor(ctx, w, "pw", r, b, &addOpts{Space: &info})
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
