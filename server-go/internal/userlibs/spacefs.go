package userlibs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// The Library's file work in a person's space (kb.SpaceFS): every call
// runs on the cluster as the person, over their own session, in the
// space's folder, through the transfers track's remote helper, which
// refuses a path that leads outside it.

// spaceWrites is the module's state for writes in spaces.
type spaceWrites struct {
	// accounts is the uid a session reached, by key and site, and when.
	accounts map[string]reachedAccount
	// rebuilds is the pending re-index of a space, by owner and id.
	rebuilds map[string]*time.Timer
	// lists is the Library's listings kept for each viewer (listcache.go).
	lists listCache
}

type reachedAccount struct {
	uid int
	at  time.Time
}

// SpaceEvery is how long a space waits after its last change before its
// index is built again.
var SpaceEvery = 20 * time.Second

type spaceFiles struct{ m *Module }

// SpaceFiles is the module's kb.SpaceFS.
func (m *Module) SpaceFiles() kb.SpaceFS { return spaceFiles{m} }

// target is the space a request's viewer may write in, as a remote folder
// reached with their key.
func (s spaceFiles) target(r *http.Request, lib kb.Library) (*transfers.RemoteFS, who, string, *libraries.UserDef, error) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return nil, w, "", nil, err
	}
	owner := w.owner()
	if owner == "" {
		return nil, w, "", nil, apierr.New(401, "Sign in to change your library.")
	}
	o, d, err := m.Users.FindRoot(lib.IndexRoot)
	if err != nil {
		return nil, w, "", nil, err
	}
	// Only the person whose library it is writes in it, and only in a
	// space, a folder shared with them for writing, or a library they
	// built on one of their systems.
	if d != nil && o == owner && d.ReadOnly != nil {
		return nil, w, "", nil, apierr.New(403, "%s", *d.ReadOnly)
	}
	if d == nil || !d.WrittenAsOwner() || o != owner {
		return nil, w, "", nil, apierr.New(404, "no such space")
	}
	site, key, err := m.writeSession(w, owner, d)
	if err != nil {
		return nil, w, "", nil, err
	}
	root := spaceRoot(d)
	if !strings.HasPrefix(root, "/") {
		return nil, w, "", nil, apierr.New(409, "%s has no folder yet.", d.ShownLabel(m.Users.AuthEnabled))
	}
	if err := m.writerCheck(r.Context(), owner, d, site, key); err != nil {
		return nil, w, "", nil, err
	}
	// A space keeps what is written in it to the person; a library of
	// theirs elsewhere takes their umask and the folder's own ACL. Neither
	// writes through a link.
	fs := &transfers.RemoteFS{R: transfers.PoolRunner{P: m.Pool}, SiteID: site, KeyText: key, Dir: root, Name: d.ShownLabel(m.Users.AuthEnabled),
		Timeout: 10 * time.Minute, Private: !d.Mine(), NoLinks: d.Mine(), Own: d.Mine() && d.Own != nil, Kept: true}
	if d.Guard {
		fs.Guard, fs.Shared = true, m.writeShares(owner, d.ID)
	}
	return fs, w, owner, d, nil
}

// writeSession is the site and key a library is written with: the
// person's own key over pw ssh, or, for a library over plain ssh on a
// Studio one person uses with sign-in off, the ssh keys of the account the
// Studio runs as, which are theirs.
func (m *Module) writeSession(w who, owner string, d *libraries.UserDef) (string, string, error) {
	if d.IsSSH() {
		if !w.sshAllowed() || d.Shared != nil {
			return "", "", errSSHSingle()
		}
		site := sshSiteOf(d)
		if site == "" {
			return "", "", apierr.New(409, "%s has no host to reach.", d.ShownLabel(m.Users.AuthEnabled))
		}
		return site, "", nil
	}
	key := m.keyOf(w, owner)
	if key == "" {
		return "", "", apierr.New(409, "Sign in with ACTIVATE under Settings, Accounts, so the Studio can reach your library as you.")
	}
	return d.Site, key, nil
}

// writerCheck refuses a session that reaches another account than the one
// the library is written as. A library of the person's own on another
// system learns that account the first time they write in it.
func (m *Module) writerCheck(ctx context.Context, owner string, d *libraries.UserDef, site, key string) error {
	if _, uid, ok := d.Account(); ok {
		return m.sameAccount(ctx, site, key, uid)
	}
	if !d.Mine() {
		return apierr.New(409, "%s does not say which account it is written as.", d.ShownLabel(m.Users.AuthEnabled))
	}
	r, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", "id -u && id -un"}, 60*time.Second)
	if err != nil {
		return m.sessionErr(remote.SiteName(site), key, err)
	}
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if r.Code != 0 || len(lines) < 2 {
		return apierr.New(502, "%s did not say which account your session reaches.", remote.SiteName(site))
	}
	uid, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-2]))
	name := strings.TrimSpace(lines[len(lines)-1])
	if err != nil || name == "" {
		return apierr.New(502, "%s did not say which account your session reaches.", remote.SiteName(site))
	}
	got := m.update(owner, d.ID, func(x *libraries.UserDef) {
		if x.Writer == nil {
			x.Writer = &libraries.WriterInfo{Account: name, UID: uid}
		}
	})
	if got == nil || got.Writer == nil {
		return apierr.New(404, "no such library")
	}
	d.Writer = got.Writer
	m.mu.Lock()
	if m.accounts == nil {
		m.accounts = map[string]reachedAccount{}
	}
	m.accounts[keyID(key)+"|"+site] = reachedAccount{uid: uid, at: time.Now()}
	m.mu.Unlock()
	if got.Writer.UID != uid {
		return apierr.New(409, "Your session reaches %s as another account (uid %d) than the one this library is written as (uid %d).", remote.SiteName(site), uid, got.Writer.UID)
	}
	return nil
}

// sameAccount refuses a session that now reaches another account than the
// one the space was made for, read at most every ten minutes.
func (m *Module) sameAccount(ctx context.Context, site, key string, uid int) error {
	k := keyID(key) + "|" + site
	m.mu.Lock()
	a, ok := m.accounts[k]
	m.mu.Unlock()
	if !ok || time.Since(a.at) > 10*time.Minute {
		r, err := m.Pool.Run(ctx, site, key, []string{"id", "-u"}, 60*time.Second)
		if err != nil {
			return m.sessionErr(remote.SiteName(site), key, err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(lastLine(r.Stdout)))
		if err != nil {
			return apierr.New(502, "%s did not say which account your session reaches.", remote.SiteName(site))
		}
		a = reachedAccount{uid: n, at: time.Now()}
		m.mu.Lock()
		if m.accounts == nil {
			m.accounts = map[string]reachedAccount{}
		}
		m.accounts[k] = a
		m.mu.Unlock()
	}
	if a.uid != uid {
		return apierr.New(409, "Your session reaches %s as another account (uid %d) than the one this library was made for (uid %d).", remote.SiteName(site), a.uid, uid)
	}
	return nil
}

// inSpace is a path the Library names in a space, relative to its folder:
// a leading slash is dropped, and a ".." part is refused here, before
// anything runs on the cluster. The cluster's own check comes after the
// name is looked up, so a person a folder is shared with could otherwise
// tell from the answer whether a name exists beside that folder.
func inSpace(rel string) (string, error) {
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		switch {
		case p == "" || p == ".":
			continue
		case p == "..":
			return "", apierr.New(400, "%s leads outside the folder", rel)
		case strings.ContainsRune(p, 0):
			return "", apierr.New(400, "unusable path")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/"), nil
}

func inSpaceAll(rels []string) ([]string, error) {
	out := make([]string, len(rels))
	for i, p := range rels {
		c, err := inSpace(p)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

func (s spaceFiles) List(r *http.Request, lib kb.Library, rel string) ([]kb.Entry, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return nil, err
	}
	fs, _, owner, d, err := s.target(r, lib)
	if err != nil {
		return nil, err
	}
	k := listKey{owner: owner, key: keyHash(fs.KeyText), site: d.Site, root: fs.Dir, rel: rel}
	if kept, again, ok := s.m.lists.get(k); ok {
		if again {
			s.readBehind(k, fs, rel)
		}
		return kept, nil
	}
	return s.read(r.Context(), k, fs, rel)
}

type listTwo interface {
	ListTwo(ctx context.Context, rel string, max int) (transfers.TwoLevels, error)
}

// entriesOf is a folder's entries as the Library lists them: folders
// first, each as its path in the space.
func entriesOf(rel string, list []transfers.Entry) []kb.Entry {
	out := make([]kb.Entry, 0, len(list))
	dirs := 0
	for _, e := range list {
		isDir := e.Type == "dir"
		var mtime int64
		if t, err := time.Parse(time.RFC3339Nano, e.Mtime); err == nil {
			mtime = t.Unix()
		}
		typ := "file"
		if isDir {
			typ = "dir"
		}
		en := kb.Entry{Name: e.Name, Path: path.Join(rel, e.Name), Type: typ, Size: e.Size, Mtime: mtime, Kind: kb.Classify(e.Name, isDir)}
		// Folders first, as the Library lists.
		if isDir {
			out = append(out, kb.Entry{})
			copy(out[dirs+1:], out[dirs:])
			out[dirs] = en
			dirs++
		} else {
			out = append(out, en)
		}
	}
	return out
}

// wrote drops the kept listings of the folders a write in a space
// touched: rels' own folders and, for a folder itself, what is in it.
// site is the library's (d.Site), which its listings are kept under; over
// plain ssh the session's (fs.SiteID, ssh://me@host:port) is another.
func (s spaceFiles) wrote(fs *transfers.RemoteFS, site string, rels ...string) {
	for _, rel := range rels {
		s.m.lists.changed(site, path.Join(fs.Dir, rel))
		s.m.lists.changed(site, path.Dir(path.Join(fs.Dir, rel)))
	}
}

func (s spaceFiles) Mkdir(r *http.Request, lib kb.Library, rel string) error {
	rel, err := inSpace(rel)
	if err != nil {
		return err
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, rel)
	return fs.Mkdir(r.Context(), rel)
}

// capped stops at max bytes and says so.
type capped struct {
	r    io.Reader
	left int64
	over bool
}

var errTooLarge = errors.New("file too large")

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		var one [1]byte
		if n, _ := c.r.Read(one[:]); n > 0 {
			c.over = true
			return 0, errTooLarge
		}
		return 0, io.EOF
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func stagingName(dir string) string {
	var b [6]byte
	rand.Read(b[:])
	return path.Join(dir, transfers.PartsPrefix+hex.EncodeToString(b[:]))
}

// Put writes beside the file, then puts it in place in one rename.
func (s spaceFiles) Put(r *http.Request, lib kb.Library, rel string, src io.Reader, max int64) error {
	rel, err := inSpace(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return apierr.New(400, "a file needs a name")
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, rel)
	ctx := r.Context()
	dir := path.Dir(rel)
	if dir == "." {
		dir = ""
	}
	staged := stagingName(dir)
	c := &capped{r: src, left: max}
	if _, err := fs.Write(ctx, staged, c, 0, 0o644); err != nil {
		fs.Remove(context.WithoutCancel(ctx), []string{staged}, nil)
		if c.over {
			return apierr.New(413, "%s is too large", path.Base(rel))
		}
		return err
	}
	if _, err := fs.Land(ctx, []transfers.LandPair{{Staged: staged, Final: rel, Replace: true}}); err != nil {
		fs.Remove(context.WithoutCancel(ctx), []string{staged}, nil)
		return err
	}
	return nil
}

// StartParts makes the folder a large file's parts are written in, beside
// where the file goes.
func (s spaceFiles) StartParts(r *http.Request, lib kb.Library, rel string, size, need int64) (string, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return "", apierr.New(400, "a file needs a name")
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return "", err
	}
	defer s.wrote(fs, d.Site, rel)
	in := path.Dir(rel)
	if in == "." {
		in = ""
	}
	dir := stagingName(in)
	// The answer may be lost while the folder is made: it goes too.
	if err := fs.StartParts(r.Context(), dir, rel, size, need); err != nil {
		if r.Context().Err() != nil {
			fs.DropParts(context.WithoutCancel(r.Context()), dir)
		}
		return "", err
	}
	return dir, nil
}

// partsDir is dir as StartParts gave it, refused when it is anything else.
func partsDir(dir string) (string, error) {
	c, err := inSpace(dir)
	if err == nil && (c != dir || !strings.HasPrefix(path.Base(c), transfers.PartsPrefix)) {
		err = apierr.New(400, "not a folder of parts")
	}
	return c, err
}

func (s spaceFiles) WritePart(r *http.Request, lib kb.Library, dir string, off, n int64, src io.Reader) error {
	dir, err := partsDir(dir)
	if err != nil {
		return err
	}
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return err
	}
	return fs.WritePart(r.Context(), dir, off, n, src)
}

func (s spaceFiles) LandParts(r *http.Request, lib kb.Library, dir, rel string, size int64) error {
	dir, err := partsDir(dir)
	if err == nil {
		rel, err = inSpace(rel)
	}
	if err != nil {
		return err
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, rel)
	return fs.LandParts(r.Context(), dir, rel, size)
}

func (s spaceFiles) DropParts(r *http.Request, lib kb.Library, dir string) error {
	dir, err := partsDir(dir)
	if err != nil {
		return err
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return err
	}
	defer s.wrote(fs, d.Site, dir)
	return fs.DropParts(r.Context(), dir)
}

func (s spaceFiles) Rename(r *http.Request, lib kb.Library, rel, name string) (string, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return "", err
	}
	if rel == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return "", apierr.New(400, "unusable name")
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return "", err
	}
	defer s.wrote(fs, d.Site, rel, path.Join(path.Dir(rel), name))
	to := path.Join(path.Dir(rel), name)
	if to == rel {
		return rel, nil
	}
	if _, err := fs.Land(r.Context(), []transfers.LandPair{{Staged: rel, Final: to}}); err != nil {
		return "", err
	}
	return to, nil
}

func (s spaceFiles) Move(r *http.Request, lib kb.Library, rels []string, dest string) ([]kb.Moved, []kb.Skipped, error) {
	rels, err := inSpaceAll(rels)
	if err == nil {
		dest, err = inSpace(dest)
	}
	if err != nil {
		return nil, nil, err
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return nil, nil, err
	}
	defer s.wrote(fs, d.Site, append(append([]string{}, rels...), dest)...)
	ctx := r.Context()
	var skipped []kb.Skipped
	var pairs []transfers.LandPair
	var tos []string
	from := map[string]string{}
	for _, rel := range rels {
		to := path.Join(dest, path.Base(rel))
		switch {
		case path.Dir(rel) == dest || (dest == "" && path.Dir(rel) == "."):
			skipped = append(skipped, kb.Skipped{Path: rel, Reason: "already there"})
		case dest == rel || strings.HasPrefix(dest, rel+"/"):
			skipped = append(skipped, kb.Skipped{Path: rel, Reason: "a folder cannot go inside itself"})
		case from[to] != "":
			skipped = append(skipped, kb.Skipped{Path: rel, Reason: "another item of the same name is moving there"})
		default:
			from[to] = rel
			tos = append(tos, to)
		}
	}
	there, err := fs.Stat(ctx, tos)
	if err != nil {
		return nil, nil, err
	}
	for _, to := range tos {
		if _, ok := there[to]; ok {
			skipped = append(skipped, kb.Skipped{Path: from[to], Reason: "an item of that name is already there"})
			continue
		}
		pairs = append(pairs, transfers.LandPair{Staged: from[to], Final: to})
	}
	var moved []kb.Moved
	if len(pairs) > 0 {
		done, err := fs.Land(ctx, pairs)
		for _, to := range done {
			moved = append(moved, kb.Moved{From: from[to], To: to})
		}
		if err != nil {
			return moved, skipped, err
		}
	}
	return moved, skipped, nil
}

func (s spaceFiles) Delete(r *http.Request, lib kb.Library, rels []string) ([]string, []kb.Skipped, error) {
	rels, err := inSpaceAll(rels)
	if err != nil {
		return nil, nil, err
	}
	for _, rel := range rels {
		if rel == "" {
			return nil, nil, apierr.New(400, "refusing to remove the space's own folder")
		}
	}
	fs, _, _, d, err := s.target(r, lib)
	if err != nil {
		return nil, nil, err
	}
	defer s.wrote(fs, d.Site, rels...)
	done, err := fs.RemoveTrees(r.Context(), rels)
	if err != nil {
		return done, nil, err
	}
	gone := map[string]bool{}
	for _, d := range done {
		gone[d] = true
	}
	var skipped []kb.Skipped
	for _, rel := range rels {
		if !gone[rel] {
			skipped = append(skipped, kb.Skipped{Path: rel, Reason: "not found"})
		}
	}
	return done, skipped, nil
}

// Get reads a plain file of the space whole, as the person. A last link
// is never followed: the file a link names may be outside the space.
func (s spaceFiles) Get(r *http.Request, lib kb.Library, rel string, max int64) ([]byte, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return nil, err
	}
	if rel == "" {
		return nil, apierr.New(400, "a file needs a name")
	}
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	there, err := fs.Stat(ctx, []string{rel})
	if err != nil {
		return nil, err
	}
	st, ok := there[rel]
	switch {
	case !ok:
		return nil, apierr.New(404, "%s is not there", rel)
	case st.Type == 'l':
		return nil, apierr.New(409, "%s is a link", rel)
	case st.Type != 'f':
		return nil, apierr.New(409, "%s is not a file", rel)
	case st.Size > max:
		return nil, apierr.New(413, "%s is too large", path.Base(rel))
	}
	rc, err := fs.Read(ctx, rel)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, apierr.New(413, "%s is too large", path.Base(rel))
	}
	return b, nil
}

// Changed builds the space's index again once its changes have settled.
func (s spaceFiles) Changed(r *http.Request, lib kb.Library, _ []string) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return
	}
	owner := w.owner()
	o, d, err := m.Users.FindRoot(lib.IndexRoot)
	if err != nil || d == nil || !d.WrittenAsOwner() || o != owner {
		return
	}
	m.queueSpaceBuild(w, owner, d.ID, SpaceEvery, 0)
	// Everyone else who indexes the same folder builds again too.
	m.sharesChanged(owner, d)
}

// spaceOfRoot is the space a transfer's viewer writes in as themselves,
// by its index root, with its owner and how the viewer is known; nil when
// the root names no such library of theirs.
func (m *Module) spaceOfRoot(viewer string, authOn bool, key, root string) (who, string, *libraries.UserDef) {
	w := who{ID: viewer, Auth: authOn, Own: key}
	owner := w.owner()
	if owner == "" {
		return w, "", nil
	}
	o, d, err := m.Users.FindRoot(root)
	if err != nil || d == nil || !d.WrittenAsOwner() || o != owner {
		return w, "", nil
	}
	return w, owner, d
}

// SpaceTransferred is app.Providers.SpaceChanged: a transfer changed the
// files of a space, so its index is built again once changes settle, as
// after a write from the Library, and so is every index of the same
// folder.
func (m *Module) SpaceTransferred(viewer string, authOn bool, key, root string) {
	w, owner, d := m.spaceOfRoot(viewer, authOn, key, root)
	if d == nil {
		return
	}
	if folder := spaceRoot(d); folder != "" {
		m.lists.changed(d.Site, folder)
	}
	m.queueSpaceBuild(w, owner, d.ID, SpaceEvery, 0)
	m.sharesChanged(owner, d)
}

// SpaceAccount is app.Providers.SpaceAccount: a transfer into a space
// with a key that reaches another account than the one the space was made
// for is refused, as the Library's own writes are (sameAccount).
func (m *Module) SpaceAccount(ctx context.Context, viewer string, authOn bool, key, root string) error {
	w, owner, d := m.spaceOfRoot(viewer, authOn, key, root)
	if d == nil {
		return nil
	}
	if d.IsSSH() {
		site, _, err := m.writeSession(w, owner, d)
		if err != nil {
			return err
		}
		return m.writerCheck(ctx, owner, d, site, "")
	}
	if key == "" {
		return nil
	}
	return m.writerCheck(ctx, owner, d, d.Site, key)
}

func (m *Module) queueSpaceBuild(w who, owner, id string, after time.Duration, tries int) {
	k := owner + "\x00" + id
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rebuilds == nil {
		m.rebuilds = map[string]*time.Timer{}
	}
	if t := m.rebuilds[k]; t != nil {
		t.Stop()
	}
	m.rebuilds[k] = time.AfterFunc(after, func() {
		m.mu.Lock()
		delete(m.rebuilds, k)
		m.mu.Unlock()
		m.spaceBuild(w, owner, id, tries)
	})
}

// spaceQueued is search.IndexQueued: a build of the space at indexRoot
// waits for its changes to settle, or for the build under way to end.
func (m *Module) spaceQueued(indexRoot string) bool {
	o, d, err := m.Users.FindRoot(indexRoot)
	if err != nil || d == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rebuilds[o+"\x00"+d.ID] != nil
}

// spaceBuild runs the build, or waits for one under way to finish, so
// what changed during it is indexed too.
func (m *Module) spaceBuild(w who, owner, id string, tries int) {
	var d *libraries.UserDef
	for _, x := range m.load(owner) {
		if x.ID == id {
			d = &x
			break
		}
	}
	if d == nil || !d.WrittenAsOwner() {
		return
	}
	if d.State == "building" || d.State == "updating" {
		if tries < 60 {
			m.queueSpaceBuild(w, owner, id, time.Minute, tries+1)
		}
		return
	}
	// The person's key now: an access token from their sign-in renews.
	w = m.pollKey(w, owner)
	if _, err := m.rebuildRemote(m.Background(), w, owner, d); err != nil && m.Log != nil {
		m.Log.Warn("a space's index was not built again", "library", id, "err", err.Error())
	}
}
