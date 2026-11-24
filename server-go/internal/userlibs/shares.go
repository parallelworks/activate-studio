package userlibs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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

// Folder sharing (docs/MULTI-USER.md, Folder sharing): the owner of a space
// gives an account, a group they belong to, or every account read, or read
// and write, on a folder in it. The Studio sets it as the owner, with a
// POSIX ACL and a default ACL where the file system takes them, and
// traverse-only access on the folders above it that the owner owns. The
// recipient adds the folder as a library of their own, indexed and read
// as them, so the kernel decides what they see. The registry here is for
// finding shares and grants nothing.

// Share is one grant.
type Share struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	OwnerAccount string `json:"ownerAccount"`
	Space        string `json:"space"`
	SpaceLabel   string `json:"spaceLabel"`
	Site         string `json:"site"`
	// Root is the space's folder; Folder the one shared, Rel it within Root.
	Root   string `json:"root"`
	Folder string `json:"folder"`
	Rel    string `json:"rel"`
	// Kind is user, group or everyone; Name the account or group.
	Kind      string    `json:"kind"`
	Name      string    `json:"name,omitempty"`
	Access    string    `json:"access"`
	CreatedAt string    `json:"createdAt"`
	Adopters  []Adopter `json:"adopters,omitempty"`
	// Opened is the folders above Folder the share opened for passing
	// through, which a revoke closes again where no other share needs them.
	Opened []string `json:"opened,omitempty"`
}

// Adopter is a recipient's library made from a share.
type Adopter struct {
	Owner   string `json:"owner"`
	Library string `json:"library"`
}

func (s Share) who() string { return s.Kind + ":" + s.Name }

// shareStore guards INDEX_BASE/shares.json.
type shareStore struct {
	sharesMu sync.Mutex
	idents   map[string]clusterIdent
}

type clusterIdent struct {
	account string
	uid     int
	groups  []string
	at      time.Time
}

func (m *Module) sharesFile() string { return filepath.Join(m.IndexBase, "shares.json") }

func (m *Module) loadShares() []Share {
	b, err := os.ReadFile(m.sharesFile())
	if err != nil {
		return nil
	}
	var out []Share
	if json.Unmarshal(b, &out) != nil {
		return nil
	}
	return out
}

// modifyShares loads, changes and saves the registry under its lock.
func (m *Module) modifyShares(fn func([]Share) ([]Share, error)) error {
	m.sharesMu.Lock()
	defer m.sharesMu.Unlock()
	next, err := fn(m.loadShares())
	if err != nil {
		return err
	}
	if next == nil {
		next = []Share{}
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.sharesFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.sharesFile())
}

var accountRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,63}$`)

// shareRel is a folder within a space as a share names it: "" for the
// whole space.
func shareRel(v any) (string, error) {
	p := strings.Trim(str(v), "/")
	if p == "" {
		return "", nil
	}
	parts := strings.Split(p, "/")
	if len(parts) > 64 || strings.ContainsAny(p, "\x00\n\r") {
		return "", apierr.New(400, "path: a folder in your library")
	}
	for _, s := range parts {
		if s == "" || s == "." || s == ".." || strings.HasPrefix(s, ".") {
			return "", apierr.New(400, "path: a folder in your library")
		}
	}
	return p, nil
}

func newShareID() string {
	var b [6]byte
	rand.Read(b[:])
	return "s-" + hex.EncodeToString(b[:])
}

// shareSh applies ($1 apply) or takes back ($1 revoke) a share, as the
// owner: $2 the folder, $3 user, group or everyone, $4 the account or
// group, $5 r or w, $6 the space's folder. With $7 1, an apply is refused
// where another account could change that folder or move it (LooseSh, with
// $8 the grants its owner made). An apply prints opened=<folder>
// for each folder above the shared one that it opened for passing through,
// which had no entry for the recipient before. A revoke reads, on standard
// input, one per line, the folders above it to close again: those the
// share opened that no other share to the same recipient needs.
const shareSh = `set -f
op=$1 f=$2 kind=$3 name=$4 perm=$5 root=$6 LOOSE_OK=$8
uid=$(id -u)
fail() { printf '%s\n' "$2" >&2; exit "$1"; }
` + transfers.LooseSh + `
[ -d "$f" ] && [ ! -L "$f" ] || fail 4 "$f is not a folder."
F=$(cd -P -- "$f" 2>/dev/null && pwd -P) || fail 4 "$f cannot be opened."
R=$(cd -P -- "$root" 2>/dev/null && pwd -P) || fail 4 "$root cannot be opened."
case "$F/" in "${R%/}"/*) ;; *) fail 6 "$f is not in your library.";; esac
if [ "$7" = 1 ] && [ "$op" = apply ]; then
  w=$(looseway "$R")
  case "${w%% *}" in
  '') ;;
  owner) fail 13 "${w#* } belongs to another account, which could change what a share opens." ;;
  *) fail 13 "Another account can write in ${w#* }, and could change what a share opens. Close it to other accounts, then share." ;;
  esac
fi
[ "$(stat -c %u -- "$F")" = "$uid" ] || fail 7 "$F belongs to another account, so it cannot be shared from here."
case "$kind" in
user) id -u -- "$name" >/dev/null 2>&1 || fail 8 "There is no account $name there."; who=u:$name; long=user:$name ;;
group) getent group -- "$name" >/dev/null 2>&1 || fail 8 "There is no group $name there."
  id -Gn | tr ' ' '\n' | grep -qxF -- "$name" || fail 9 "You are not in the group $name, so you cannot share with it."
  who=g:$name; long=group:$name ;;
everyone) who=o ;;
*) fail 2 "unknown kind" ;;
esac
p=rX; [ "$perm" = w ] && p=rwX
# Where an ACL already narrows the owning group, it keeps what it has: the
# mask setfacl works out again must not widen it.
pin() {
  a=$(getfacl -cpE -- "$1" 2>/dev/null) || return 0
  m=$(printf '%s\n' "$a" | sed -n 's/^mask::\(...\).*/\1/p'); [ -n "$m" ] || return 0
  g=$(printf '%s\n' "$a" | sed -n 's/^group::\(...\).*/\1/p')
  e=; i=1
  while [ $i -le 3 ]; do
    c=$(printf %s "$g" | cut -c$i); k=$(printf %s "$m" | cut -c$i)
    if [ "$c" = "$k" ]; then e=$e$c; else e=$e-; fi
    i=$((i+1))
  done
  [ "$e" = "$g" ] || setfacl -m "g::$e" -- "$1"
}
has() { getfacl -cpE -- "$1" 2>/dev/null | sed -n "s/^$2:\(...\).*/\1/p" | head -n 1; }
# tidy: an ACL left with base entries only goes, so the folder or file is
# as it was before any share: a default ACL with no named entry, which
# would set what new files get in place of the umask, and a mask that is
# the only extended entry and equals the owning group's.
tidy() {
  a=$(getfacl -cpE -- "$1" 2>/dev/null) || return 0
  printf '%s\n' "$a" | grep -q '^default:\(user\|group\):[^:]' && return 0
  printf '%s\n' "$a" | grep -q '^default:' && setfacl -k -- "$1"
  printf '%s\n' "$a" | grep -q '^\(user\|group\):[^:]' && return 0
  m=$(printf '%s\n' "$a" | sed -n 's/^mask::\(...\).*/\1/p'); [ -n "$m" ] || return 0
  [ "$m" = "$(printf '%s\n' "$a" | sed -n 's/^group::\(...\).*/\1/p')" ] && setfacl -b -- "$1"
  return 0
}
# Only what the owner owns is changed: a recipient who writes owns what
# they made, and its ACL came from the folder's default ACL.
mine() { find "$F" -user "$uid" ! -type l "$@"; }
me=$(id -un)
case "$op" in
apply)
  if [ "$kind" = everyone ]; then
    mine -exec chmod o+rX -- {} + || fail 10 "$F could not be opened to every account."
  else
    mine -exec getfacl -P -s -p -- {} + 2>/dev/null | sed -n 's/^# file: //p' | while IFS= read -r x; do pin "$x"; done
    mine -exec setfacl -m "$who:$p" -- {} + || fail 10 "The ACL could not be set on $F."
    mine -type d -exec setfacl -d -m "$who:$p" -- {} + || fail 10 "The default ACL could not be set on $F."
    # What a recipient writes there stays open to the owner.
    [ "$perm" = w ] && { mine -type d -exec setfacl -d -m "u:$me:rwX" -- {} + || fail 10 "The default ACL could not be set on $F."; }
  fi
  groups=
  [ "$kind" = user ] && groups=$(id -Gn -- "$name" 2>/dev/null)
  d=$F
  while [ "$d" != / ]; do
    d=${d%/*}; [ -n "$d" ] || d=/
    if [ "$(stat -c %u -- "$d")" = "$uid" ]; then
      if [ "$kind" = everyone ]; then
        if [ $((8#$(stat -c %a -- "$d") & 1)) -eq 0 ]; then
          chmod o+x -- "$d" || fail 10 "$d could not be opened to every account."
          printf 'opened=%s\n' "$d"
        fi
      else
        cur=$(has "$d" "$long")
        case "$cur" in
        ??x) ;;
        '') pin "$d"; setfacl -m "$who:--x" -- "$d" || fail 10 "$d could not be opened to $name."; printf 'opened=%s\n' "$d" ;;
        *) pin "$d"; setfacl -m "$who:${cur%?}x" -- "$d" || fail 10 "$d could not be opened to $name." ;;
        esac
      fi
    else
      mm=$((8#$(stat -c %a -- "$d")))
      [ $((mm & 1)) -ne 0 ] && continue
      g=$(stat -c %G -- "$d")
      if [ $((mm & 8)) -ne 0 ]; then
        [ "$kind" = group ] && [ "$g" = "$name" ] && continue
        printf '%s\n' $groups | grep -qxF -- "$g" && continue
      fi
      fail 11 "$d belongs to another account and is closed to others, so the folder cannot be reached through it."
    fi
  done
  echo shared ;;
revoke)
  if [ "$kind" = everyone ]; then
    mine -exec chmod o-rwx -- {} + || fail 10 "$F could not be closed to other accounts."
  else
    mine -exec setfacl -x "$who" -- {} + 2>/dev/null
    mine -type d -exec setfacl -x "d:$who" -- {} + 2>/dev/null
    mine -exec getfacl -P -s -p -- {} + 2>/dev/null | sed -n 's/^# file: //p' | while IFS= read -r x; do tidy "$x"; done
  fi
  while IFS= read -r d || [ -n "$d" ]; do
    [ -n "$d" ] && [ -d "$d" ] && [ ! -L "$d" ] || continue
    [ "$(stat -c %u -- "$d")" = "$uid" ] || continue
    case "$F/" in "${d%/}"/*) ;; *) continue ;; esac
    if [ "$kind" = everyone ]; then chmod o-x -- "$d"
    elif [ "$(has "$d" "$long")" = "--x" ]; then setfacl -x "$who" -- "$d"; tidy "$d"
    fi
  done
  echo revoked ;;
*) fail 2 "unknown command" ;;
esac`

func (s Share) json(adopted string) *jsonv.Object {
	label := s.SpaceLabel
	if s.Rel != "" {
		label = path.Base(s.Rel)
	}
	o := jsonv.NewObject("id", s.ID, "from", s.OwnerAccount, "space", s.Space, "path", s.Rel, "label", label, "folder", s.Folder,
		"kind", s.Kind, "name", s.Name, "access", s.Access, "createdAt", s.CreatedAt, "recipients", float64(len(s.Adopters)),
		"site", remote.ShortName(s.Site))
	if adopted != "" {
		o.Set("library", adopted)
	}
	return o
}

// spaceRoot is the folder of a space or shared library.
func spaceRoot(d *libraries.UserDef) string {
	if d.SourceRoot != nil && *d.SourceRoot != "" {
		return *d.SourceRoot
	}
	if d.Build != nil {
		return d.Build.SourceRoot
	}
	return ""
}

// ownSpace is the viewer's space id, with their key, or why not.
func (m *Module) ownSpace(ctx context.Context, w who, id string) (string, string, *libraries.UserDef, error) {
	owner := w.owner()
	if owner == "" {
		return "", "", nil, apierr.New(401, "Sign in to share.")
	}
	d := find(m.load(owner), id)
	if d == nil || d.Shareable() == nil {
		return "", "", nil, apierr.New(404, "no such space")
	}
	// A space over plain ssh is on a Studio one person uses, with nobody
	// here to share it with.
	if d.IsSSH() {
		return "", "", nil, apierr.New(409, "%s is reached over ssh on a Studio only you use, so there is nobody here to share it with.", d.ShownLabel(m.Users.AuthEnabled))
	}
	key := m.keyOf(w, owner)
	if key == "" {
		return "", "", nil, apierr.New(409, "Sign the pw CLI in under Settings, Accounts, so the Studio can share as you.")
	}
	if err := m.sameAccount(ctx, d.Site, key, d.Shareable().UID); err != nil {
		return "", "", nil, err
	}
	return owner, key, d, nil
}

// listShares is a space's shares.
func (m *Module) listShares(w who, id string) (*jsonv.Object, error) {
	owner := w.owner()
	d := find(m.load(owner), id)
	if owner == "" || d == nil || d.Shareable() == nil {
		return nil, apierr.New(404, "no such space")
	}
	list := []any{}
	for _, s := range m.loadShares() {
		if s.Owner == owner && s.Space == id {
			list = append(list, s.json(""))
		}
	}
	return jsonv.NewObject("shares", list, "acl", d.Shareable().ACL, "fs", d.Shareable().FS, "account", d.Shareable().Account), nil
}

// runShare runs shareSh; close is a revoke's folders to close again. It
// returns the folders an apply opened.
func (m *Module) runShare(ctx context.Context, site, key string, s Share, op string, close []string) ([]string, error) {
	perm := "r"
	if s.Access == "write" {
		perm = "w"
	}
	guard := ""
	if d := find(m.load(s.Owner), s.Space); d != nil && d.Guard {
		guard = "1"
	}
	input := strings.NewReader(strings.Join(append(close, ""), "\n"))
	r, err := m.Pool.RunOnce(ctx, site, key, []string{"bash", "-c", shareSh, "_", op, s.Folder, s.Kind, s.Name, perm, s.Root, guard, grantsLine(m.writeShares(s.Owner, s.Space))}, 30*time.Minute, input)
	if err != nil {
		return nil, m.sessionErr(remote.ShortName(site), key, err)
	}
	if r.Code != 0 {
		return nil, apierr.New(409, "%s", lastOrExit(remote.QuietStderr(r.Stderr), r.Code))
	}
	var opened []string
	for _, line := range strings.Split(r.Stdout, "\n") {
		if d, ok := strings.CutPrefix(line, "opened="); ok && strings.HasPrefix(d, "/") {
			opened = append(opened, d)
		}
	}
	return opened, nil
}

// union is a and the paths of b not in it.
func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// closeFor is what a revoke of s closes again above its folder: what s
// opened, less what other shares to the same recipient need.
func (m *Module) closeFor(s Share) []string {
	keep := m.keepFor(s)
	var out []string
	for _, d := range s.Opened {
		if !slices.Contains(keep, d) {
			out = append(out, d)
		}
	}
	return out
}

// noteOpened adds folders a share opened to its record.
func (m *Module) noteOpened(id string, opened []string) {
	if len(opened) == 0 {
		return
	}
	m.modifyShares(func(list []Share) ([]Share, error) {
		for i := range list {
			if list[i].ID == id {
				list[i].Opened = union(list[i].Opened, opened)
			}
		}
		return list, nil
	})
}

// shareAsk is a share as it is asked for.
type shareAsk struct{ kind, name, access string }

func (s *shareAsk) object() *jsonv.Object {
	return jsonv.NewObject("path", "", "kind", s.kind, "name", s.name, "access", s.access)
}

// shareRequest reads whom a share is for and what it lets them do, and
// refuses one the folder's file system or owner cannot give.
func shareRequest(b *jsonv.Object, info libraries.SpaceInfo) (*shareAsk, error) {
	kind, name, access := str(field(b, "kind")), strings.TrimSpace(str(field(b, "name"))), str(field(b, "access"))
	if access == "" {
		access = "read"
	}
	switch {
	case kind != "user" && kind != "group" && kind != "everyone":
		return nil, apierr.New(400, "kind: user, group or everyone")
	case access != "read" && access != "write":
		return nil, apierr.New(400, "access: read or write")
	case kind == "everyone" && access == "write":
		return nil, apierr.New(400, "Every account can be given read only. Share with a person or a group to let them write.")
	case kind != "everyone" && !accountRe.MatchString(name):
		return nil, apierr.New(400, "name: an account or group on the cluster")
	case kind == "user" && name == info.Account:
		return nil, apierr.New(400, "That is your own account.")
	case kind != "everyone" && !info.ACL:
		return nil, apierr.New(409, "Sharing with one person or group needs POSIX ACLs, which this file system (%s) does not take. You can share the folder with every account instead.", info.FS)
	}
	if kind == "everyone" {
		name = ""
	}
	return &shareAsk{kind, name, access}, nil
}

// writeShares is who a library of owner's is shared with for writing, as
// ACL entries name them.
func (m *Module) writeShares(owner, id string) []string {
	var out []string
	for _, s := range m.loadShares() {
		if s.Owner == owner && s.Space == id && s.Access == "write" && s.Kind != "everyone" {
			out = append(out, s.who())
		}
	}
	return out
}

// addShare grants a share, or changes the access of one already there.
func (m *Module) addShare(ctx context.Context, w who, id string, b *jsonv.Object) (*jsonv.Object, error) {
	owner, key, d, err := m.ownSpace(ctx, w, id)
	if err != nil {
		return nil, err
	}
	rel, err := shareRel(field(b, "path"))
	if err != nil {
		return nil, err
	}
	if d.ReadOnly != nil {
		return nil, apierr.New(409, "%s", *d.ReadOnly)
	}
	info := *d.Shareable()
	ask, err := shareRequest(b, info)
	if err != nil {
		return nil, err
	}
	kind, name, access := ask.kind, ask.name, ask.access
	root := spaceRoot(d)
	folder := root
	if rel != "" {
		folder = strings.TrimRight(root, "/") + "/" + rel
	}
	s := Share{ID: newShareID(), Owner: owner, OwnerAccount: info.Account, Space: id, SpaceLabel: d.ShownLabel(m.Users.AuthEnabled), Site: d.Site,
		Root: root, Folder: folder, Rel: rel, Kind: kind, Name: name, Access: access, CreatedAt: isoNow()}
	var was *Share
	for _, x := range m.loadShares() {
		if x.Owner == owner && x.Space == id && x.Rel == rel && x.who() == s.who() {
			x := x
			was = &x
		}
	}
	if was != nil && was.Access == "write" && access == "read" {
		// Write taken back to read: the old entry goes first.
		if _, err := m.runShare(ctx, d.Site, key, *was, "revoke", m.closeFor(*was)); err != nil {
			return nil, err
		}
	}
	opened, err := m.runShare(ctx, d.Site, key, s, "apply", nil)
	if err != nil {
		return nil, err
	}
	s.Opened = opened
	if was != nil {
		s.ID, s.CreatedAt, s.Adopters, s.Opened = was.ID, was.CreatedAt, was.Adopters, union(was.Opened, opened)
	}
	err = m.modifyShares(func(list []Share) ([]Share, error) {
		out := []Share{}
		for _, x := range list {
			if x.ID != s.ID {
				out = append(out, x)
			}
		}
		return append(out, s), nil
	})
	if err != nil {
		return nil, err
	}
	if was != nil {
		m.reapplyOverlapping(ctx, d.Site, key, s)
		for _, a := range s.Adopters {
			m.Users.Update(a.Owner, a.Library, func(x *libraries.UserDef) {
				if x.Shared != nil {
					x.Shared.Access = access
				}
			})
		}
	}
	return s.json(""), nil
}

// keepFor is what other shares to the same recipient by the same owner
// need reachable: their folders and every folder above them.
func (m *Module) keepFor(s Share) []string {
	seen := map[string]bool{}
	var keep []string
	for _, x := range m.loadShares() {
		if x.ID == s.ID || x.Owner != s.Owner || x.who() != s.who() {
			continue
		}
		for d := x.Folder; d != "/" && d != "." && d != ""; d = path.Dir(d) {
			if !seen[d] {
				seen[d] = true
				keep = append(keep, d)
			}
		}
	}
	return keep
}

// reapplyOverlapping sets again the other shares to the same recipient
// whose folders hold s's folder or lie in it, which a revoke of s took
// from them.
func (m *Module) reapplyOverlapping(ctx context.Context, site, key string, s Share) {
	for _, x := range m.loadShares() {
		if x.ID == s.ID || x.Owner != s.Owner || x.who() != s.who() {
			continue
		}
		if inside(x.Folder, s.Folder) || inside(s.Folder, x.Folder) {
			opened, err := m.runShare(ctx, site, key, x, "apply", nil)
			if err != nil && m.Log != nil {
				m.Log.Warn("a share was not set again", "share", x.ID, "err", err.Error())
			}
			m.noteOpened(x.ID, opened)
		}
	}
}

// removeShare takes a share back, and the recipients' libraries made from
// it, with their index of it where the Studio can reach their account.
func (m *Module) removeShare(ctx context.Context, w who, id, shareID string) error {
	owner, key, d, err := m.ownSpace(ctx, w, id)
	if err != nil {
		return err
	}
	var s *Share
	for _, x := range m.loadShares() {
		if x.ID == shareID && x.Owner == owner && x.Space == id {
			x := x
			s = &x
		}
	}
	if s == nil {
		return apierr.New(404, "no such share")
	}
	// Whatever happens next, nobody is answered from listings of the
	// folder kept while it was shared.
	m.lists.changed(d.Site, s.Folder)
	if _, err := m.runShare(ctx, d.Site, key, *s, "revoke", m.closeFor(*s)); err != nil {
		return err
	}
	if err := m.modifyShares(func(list []Share) ([]Share, error) {
		out := []Share{}
		for _, x := range list {
			if x.ID != s.ID {
				out = append(out, x)
			}
		}
		return out, nil
	}); err != nil {
		return err
	}
	m.reapplyOverlapping(ctx, d.Site, key, *s)
	for _, a := range s.Adopters {
		m.dropAdopted(a)
	}
	return nil
}

// dropAdopted removes a recipient's library made from a share that is no
// more, and deletes their index of it as them, when their key is at hand.
func (m *Module) dropAdopted(a Adopter) {
	d := find(m.load(a.Owner), a.Library)
	if d == nil || d.Shared == nil {
		return
	}
	if key, _ := m.OwnerKey(a.Owner, ""); key != "" && d.Build != nil && d.Build.IndexDir != "" {
		ctx, cancel := context.WithTimeout(m.Background(), 2*time.Minute)
		m.Pool.Run(ctx, d.Site, key, []string{"sh", "-c", `case "$1" in "$HOME"/.activate-studio/indexes/?*) rm -rf -- "$1" ;; esac`, "_", d.Build.IndexDir}, 2*time.Minute)
		cancel()
	}
	m.remove(who{ID: a.Owner, Auth: true}, a.Library)
}

// identity is the account a session reaches and its groups, read at most
// every ten minutes.
func (m *Module) identity(ctx context.Context, site, key string) (clusterIdent, error) {
	k := keyID(key) + "|" + site
	m.sharesMu.Lock()
	id, ok := m.idents[k]
	m.sharesMu.Unlock()
	if ok && time.Since(id.at) < 10*time.Minute {
		return id, nil
	}
	r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", `echo "uid=$(id -u)"; echo "user=$(id -un)"; echo "groups=$(id -Gn)"`}, 60*time.Second)
	if err != nil {
		return id, m.sessionErr(remote.ShortName(site), key, err)
	}
	kv := keyValues(r.Stdout)
	uid, err := strconv.Atoi(kv["uid"])
	if err != nil || kv["user"] == "" {
		return id, apierr.New(502, "%s did not say which account your session reaches.", remote.ShortName(site))
	}
	id = clusterIdent{account: kv["user"], uid: uid, groups: strings.Fields(kv["groups"]), at: time.Now()}
	m.sharesMu.Lock()
	if m.idents == nil {
		m.idents = map[string]clusterIdent{}
	}
	m.idents[k] = id
	m.sharesMu.Unlock()
	return id, nil
}

func (s Share) reaches(id clusterIdent) bool {
	switch s.Kind {
	case "everyone":
		return true
	case "user":
		return s.Name == id.account
	case "group":
		for _, g := range id.groups {
			if g == s.Name {
				return true
			}
		}
	}
	return false
}

// sharedReachWait is how long Shared with me waits for each system's
// answer; the systems are asked side by side, at most sharedReachAtOnce at
// a time, so one that is stopped delays the list by this much at most.
var (
	sharedReachWait   = 20 * time.Second
	sharedReachAtOnce = 6
)

// sharedWithMe is what others shared with the viewer's account, on each
// system a share is on, as their own session there names them. A system
// whose session did not answer is named in "unreached" with the reason,
// rather than taken as one where nothing is shared.
func (m *Module) sharedWithMe(ctx context.Context, w who) (*jsonv.Object, error) {
	owner := w.owner()
	if _, err := m.spaceSession(w, nil); err != nil && (owner == "" || m.keyOf(w, owner) == "") {
		return jsonv.NewObject("shares", []any{}, "why", err.Error()), nil
	}
	shares := m.loadShares()
	type reached struct {
		id  clusterIdent
		err error
	}
	var sites []string
	targets := map[string]*spaceTarget{}
	for _, s := range shares {
		if s.Owner == owner {
			continue
		}
		if _, seen := targets[s.Site]; seen {
			continue
		}
		t, err := m.pwSpaceTarget(w, owner, s.Site)
		if err != nil {
			return jsonv.NewObject("shares", []any{}, "why", err.Error()), nil
		}
		targets[s.Site] = t
		sites = append(sites, s.Site)
	}
	ids := make(map[string]reached, len(sites))
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, sharedReachAtOnce)
	for _, site := range sites {
		wg.Add(1)
		go func(site string, t *spaceTarget) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			c, cancel := context.WithTimeout(ctx, sharedReachWait)
			defer cancel()
			id, err := m.identity(c, t.site, t.key)
			if err != nil && c.Err() == context.DeadlineExceeded {
				err = apierr.New(504, "%s did not answer within %d seconds.", remote.ShortName(t.site), int(sharedReachWait/time.Second))
			}
			mu.Lock()
			ids[site] = reached{id, err}
			mu.Unlock()
		}(site, targets[site])
	}
	wg.Wait()
	list := []any{}
	unreached := []any{}
	account := ""
	for _, site := range sites {
		if r := ids[site]; r.err != nil {
			unreached = append(unreached, jsonv.NewObject("site", site, "name", remote.ShortName(site), "why", r.err.Error()))
		}
	}
	for _, s := range shares {
		if s.Owner == owner {
			continue
		}
		at := ids[s.Site]
		if at.err != nil || !s.reaches(at.id) {
			continue
		}
		if account == "" || s.Site == HomeCluster() {
			account = at.id.account
		}
		adopted := ""
		for _, a := range s.Adopters {
			if a.Owner == owner {
				adopted = a.Library
			}
		}
		list = append(list, s.json(adopted))
	}
	return jsonv.NewObject("shares", list, "account", account, "unreached", unreached), nil
}

// addShared makes a library of a folder shared with the viewer, indexed
// and read as them, on the system the folder is on.
func (m *Module) addShared(ctx context.Context, w who, shareID string, b *jsonv.Object) (*libraries.UserDef, int, error) {
	owner := w.owner()
	if owner == "" {
		return nil, 0, apierr.New(401, "Sign in to the Studio to make your library.")
	}
	var s *Share
	for _, x := range m.loadShares() {
		if x.ID == shareID && x.Owner != owner {
			x := x
			s = &x
		}
	}
	if s == nil {
		return nil, 0, apierr.New(404, "no such share")
	}
	t, err := m.pwSpaceTarget(w, owner, s.Site)
	if err != nil {
		return nil, 0, err
	}
	site, key := t.site, t.key
	id, err := m.identity(ctx, site, key)
	if err != nil {
		return nil, 0, err
	}
	if !s.reaches(id) {
		return nil, 0, apierr.New(404, "no such share")
	}
	for _, a := range s.Adopters {
		if a.Owner == owner && find(m.load(owner), a.Library) != nil {
			return nil, 0, apierr.New(409, "That folder is already one of your libraries.")
		}
	}
	f, err := m.checkShared(ctx, site, key, s.Folder, s.OwnerAccount)
	if err != nil {
		return nil, 0, err
	}
	if !f.OK {
		return nil, 0, spaceNotPassed(f)
	}
	if r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", spacePrepareSh}, 60*time.Second); err != nil || !strings.Contains(r.Stdout, "ready") {
		return nil, 0, apierr.New(502, "Your ~/.activate-studio could not be kept to you alone on %s.", remote.ShortName(site))
	}
	if f.SendGufi {
		if err := m.sendGufi(ctx, site, key); err != nil {
			return nil, 0, err
		}
	}
	bin := f.GufiBin
	r := &buildReq{owner: owner,
		host: probeHost{Site: site, Key: key, Name: remote.ShortName(site), Reached: remote.ShortName(site),
			Advice: func(msg string) string { return pwAdvice(msg, site) }},
		conn: buildConn{SSHOptions: []string{}, Site: site},
		spec: buildSpec{SourceRoot: f.Real, GufiBin: &bin}}
	label := s.SpaceLabel
	if s.Rel != "" {
		label = path.Base(s.Rel)
	}
	opts := &addOpts{IDBase: "shared-" + label, Label: label + " (" + s.OwnerAccount + ")",
		Shared: &libraries.SharedInfo{ShareID: s.ID, From: s.OwnerAccount, Access: s.Access, Account: id.account, UID: id.uid}}
	d, job, err := m.addBuildFor(ctx, w, "pw", r, b, opts)
	if err != nil {
		return nil, 0, err
	}
	m.modifyShares(func(list []Share) ([]Share, error) {
		for i := range list {
			if list[i].ID == s.ID {
				list[i].Adopters = append(list[i].Adopters, Adopter{Owner: owner, Library: d.ID})
			}
		}
		return list, nil
	})
	return d, job, nil
}

// checkShared is the check for a shared folder: the person can open it,
// and GUFI can run as them.
func (m *Module) checkShared(ctx context.Context, site, key, folder, from string) (*spaceFound, error) {
	f, err := m.checkSpace(ctx, probeHost{Site: site, Key: key, Name: remote.ShortName(site), Reached: remote.ShortName(site),
		Advice: func(msg string) string { return pwAdvice(msg, site) }}, spaceAsk{folder: folder})
	if err != nil {
		return nil, err
	}
	var steps []step
	for _, s := range f.Steps {
		switch s.Name {
		case "account", "gufi", "index":
			steps = append(steps, s)
		}
	}
	if f.Missing || f.Real == "" || !f.readable {
		steps = append(steps, step{Name: "folder", Detail: folder + " is not open to your account. Ask " + from + " to share it again."})
	} else {
		steps = append(steps, step{Name: "folder", OK: true, Detail: "You can open " + f.Real + "."})
	}
	f.Steps = steps
	f.OK = allOK(steps)
	return f, nil
}

// sharesChanged builds again the other libraries over the folder a write
// changed: the owner's space and every recipient's library of it.
func (m *Module) sharesChanged(owner string, d *libraries.UserDef) {
	type lib struct{ owner, id string }
	var todo []lib
	for _, s := range m.loadShares() {
		mine := d.Shareable() != nil && s.Owner == owner && s.Space == d.ID
		fromIt := d.Shared != nil && s.ID == d.Shared.ShareID
		if !mine && !fromIt {
			continue
		}
		if fromIt {
			todo = append(todo, lib{s.Owner, s.Space})
		}
		for _, a := range s.Adopters {
			if a.Owner != owner || a.Library != d.ID {
				todo = append(todo, lib{a.Owner, a.Library})
			}
		}
	}
	for _, l := range todo {
		m.queueSpaceBuild(who{ID: l.owner, Auth: true}, l.owner, l.id, SpaceEvery, 0)
	}
}

// forgetAdopted takes a removed library off the share it was made from.
func (m *Module) forgetAdopted(owner string, d *libraries.UserDef) {
	if d == nil || d.Shared == nil {
		return
	}
	m.modifyShares(func(list []Share) ([]Share, error) {
		for i := range list {
			if list[i].ID != d.Shared.ShareID {
				continue
			}
			var keep []Adopter
			for _, a := range list[i].Adopters {
				if a.Owner != owner || a.Library != d.ID {
					keep = append(keep, a)
				}
			}
			list[i].Adopters = keep
		}
		return list, nil
	})
}

// endShares takes back the shares of a space being removed, as its owner
// when their key is at hand, and the recipients' libraries made from them.
func (m *Module) endShares(w who, owner string, d *libraries.UserDef) {
	key := m.keyOf(w, owner)
	ctx, cancel := context.WithTimeout(m.Background(), 10*time.Minute)
	defer cancel()
	var mine []Share
	for _, s := range m.loadShares() {
		if s.Owner == owner && s.Space == d.ID {
			mine = append(mine, s)
		}
	}
	for _, s := range mine {
		if key != "" {
			if _, err := m.runShare(ctx, d.Site, key, s, "revoke", m.closeFor(s)); err != nil && m.Log != nil {
				m.Log.Warn("a share of a removed space was not taken back", "share", s.ID, "err", err.Error())
			}
		}
		m.modifyShares(func(list []Share) ([]Share, error) {
			out := []Share{}
			for _, x := range list {
				if x.ID != s.ID {
					out = append(out, x)
				}
			}
			return out, nil
		})
		for _, a := range s.Adopters {
			m.dropAdopted(a)
		}
	}
}
