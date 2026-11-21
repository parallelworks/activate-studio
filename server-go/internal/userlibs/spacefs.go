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
	// space or a folder shared with them for writing.
	if d == nil || !d.WrittenAsOwner() || o != owner {
		return nil, w, "", nil, apierr.New(404, "no such space")
	}
	_, uid, _ := d.Account()
	key := m.keyOf(w, owner)
	if key == "" {
		return nil, w, "", nil, apierr.New(409, "Sign in with ACTIVATE under Settings, Model access, so the Studio can reach your library as you.")
	}
	root := ""
	if d.Build != nil {
		root = d.Build.SourceRoot
	}
	if d.SourceRoot != nil && *d.SourceRoot != "" {
		root = *d.SourceRoot
	}
	if !strings.HasPrefix(root, "/") {
		return nil, w, "", nil, apierr.New(409, "%s has no folder yet.", d.Label)
	}
	if err := m.sameAccount(r.Context(), d.Site, key, uid); err != nil {
		return nil, w, "", nil, err
	}
	fs := &transfers.RemoteFS{R: transfers.PoolRunner{P: m.Pool}, SiteID: d.Site, KeyText: key, Dir: root, Name: d.Label, Timeout: 10 * time.Minute, Private: true}
	return fs, w, owner, d, nil
}

// sameAccount refuses a session that now reaches another account than the
// one the space was made for, read at most every ten minutes.
func (m *Module) sameAccount(ctx context.Context, site, key string, uid int) error {
	k := slice16(key) + "|" + site
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
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return nil, err
	}
	list, _, err := fs.List(r.Context(), rel, 10000)
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

func (s spaceFiles) Mkdir(r *http.Request, lib kb.Library, rel string) error {
	rel, err := inSpace(rel)
	if err != nil {
		return err
	}
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return err
	}
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
	return path.Join(dir, ".studio-upload-"+hex.EncodeToString(b[:]))
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
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return err
	}
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

func (s spaceFiles) Rename(r *http.Request, lib kb.Library, rel, name string) (string, error) {
	rel, err := inSpace(rel)
	if err != nil {
		return "", err
	}
	if rel == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return "", apierr.New(400, "unusable name")
	}
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return "", err
	}
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
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return nil, nil, err
	}
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
	fs, _, _, _, err := s.target(r, lib)
	if err != nil {
		return nil, nil, err
	}
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
	m.queueSpaceBuild(w, owner, d.ID, SpaceEvery, 0)
	m.sharesChanged(owner, d)
}

// SpaceAccount is app.Providers.SpaceAccount: a transfer into a space
// with a key that reaches another account than the one the space was made
// for is refused, as the Library's own writes are (sameAccount).
func (m *Module) SpaceAccount(ctx context.Context, viewer string, authOn bool, key, root string) error {
	_, _, d := m.spaceOfRoot(viewer, authOn, key, root)
	if d == nil {
		return nil
	}
	_, uid, ok := d.Account()
	if !ok || key == "" {
		return nil
	}
	return m.sameAccount(ctx, d.Site, key, uid)
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
	if d == nil || (d.Space == nil && d.Shared == nil) {
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
