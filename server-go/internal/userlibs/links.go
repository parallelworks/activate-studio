package userlibs

import (
	"context"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/links"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// Copy link in people's own libraries (kb.LinkPlaces). An item in a folder
// shared with someone is kept in its owner's library, so it has one ID
// whoever makes the link, and the ID follows what either of them does to
// it. What a person may open is only what a library they have holds: their
// own, or one they made from a share that covers the item. Two libraries
// of one folder on one system are not matched by the folder's path, which
// anyone may name.

// placeIn is where an item of owner's library d is kept; ok false for a
// library made from a share that is no more.
func (m *Module) placeIn(owner string, d *libraries.UserDef, rel string) (links.Place, bool) {
	if d.Shared == nil {
		return links.Place{Owner: owner, Lib: d.ID, Rel: rel}, true
	}
	for _, s := range m.loadShares() {
		if s.ID == d.Shared.ShareID {
			return links.Place{Owner: s.Owner, Lib: s.Space, Rel: joinRel(s.Rel, rel)}, true
		}
	}
	return links.Place{}, false
}

func joinRel(a, b string) string { return strings.Trim(path.Join(a, b), "/") }

// Place is kb.LinkPlaces'.
func (s spaceFiles) Place(r *http.Request, lib kb.Library, rel string) (links.Place, error) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return links.Place{}, err
	}
	o, d, err := m.Users.FindRoot(lib.IndexRoot)
	if err != nil {
		return links.Place{}, err
	}
	if d == nil || o == "" || o != w.owner() {
		return links.Place{}, apierr.New(404, "no such library")
	}
	p, ok := m.placeIn(o, d, rel)
	if !ok {
		return links.Place{}, apierr.New(409, "%s is not shared with you any more.", lib.Label)
	}
	return p, nil
}

// Find is kb.LinkPlaces'.
func (s spaceFiles) Find(r *http.Request, p links.Place) ([]kb.LinkAt, error) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return nil, err
	}
	me := w.owner()
	if me == "" {
		return nil, nil
	}
	var out []kb.LinkAt
	add := func(id, rel string) {
		lib, err := kb.LibraryResolver.Library(r, id)
		// The library found is the viewer's own by that name, not one of
		// the deployment's.
		if err == nil && lib.IndexRoot == libraries.RemoteRootOf(me, id) {
			out = append(out, kb.LinkAt{Lib: lib, Rel: rel})
		}
	}
	defs := m.load(me)
	if p.Owner == me {
		if d := find(defs, p.Lib); d != nil && d.Shared == nil {
			add(d.ID, p.Rel)
		}
		return out, nil
	}
	for _, sh := range m.loadShares() {
		if sh.Owner != p.Owner || sh.Space != p.Lib {
			continue
		}
		rel, ok := p.Rel, true
		if sh.Rel != "" {
			if rel, ok = strings.CutPrefix(p.Rel+"/", sh.Rel+"/"); !ok {
				continue
			}
			rel = strings.TrimSuffix(rel, "/")
		}
		for _, a := range sh.Adopters {
			if a.Owner != me {
				continue
			}
			if d := find(defs, a.Library); d != nil && d.Shared != nil && d.Shared.ShareID == sh.ID {
				add(d.ID, rel)
			}
		}
	}
	return out, nil
}

// lookWait is how long following a link waits for a system.
const lookWait = 60 * time.Second

// Look is kb.LinkPlaces': the paths as the viewer's own session finds them.
func (s spaceFiles) Look(r *http.Request, lib kb.Library, rels []string) (map[string]kb.LinkStat, bool, error) {
	m := s.m
	w, err := m.whoOf(r)
	if err != nil {
		return nil, false, err
	}
	o, d, err := m.Users.FindRoot(lib.IndexRoot)
	if err != nil {
		return nil, false, err
	}
	if d == nil || o == "" || o != w.owner() {
		return nil, false, apierr.New(404, "no such library")
	}
	root := spaceRoot(d)
	if !strings.HasPrefix(root, "/") || d.ReadThrough != nil || (d.Shell != nil && !*d.Shell) || (d.OS != nil && strings.EqualFold(*d.OS, "windows")) {
		return nil, false, nil
	}
	site, key, err := m.writeSession(w, o, d)
	if err != nil {
		return nil, false, &kb.LinkUnreached{Message: err.Error()}
	}
	// The account the library is read and written as, so a session that
	// now reaches another says nothing about what this one can open.
	if _, uid, ok := d.Account(); ok {
		if err := m.sameAccount(r.Context(), site, key, uid); err != nil {
			return nil, false, &kb.LinkUnreached{Message: err.Error()}
		}
	}
	fs := &transfers.RemoteFS{R: transfers.PoolRunner{P: m.Pool}, SiteID: site, KeyText: key, Dir: root, Name: lib.Label, Timeout: lookWait, Kept: true}
	got, ok, err := fs.Ident(r.Context(), rels)
	if err != nil {
		return nil, false, &kb.LinkUnreached{Message: lib.Label + " could not be read on " + remote.SiteName(site) + " now. Try again in a moment."}
	}
	if !ok {
		return nil, false, nil
	}
	out := map[string]kb.LinkStat{}
	for p, x := range got {
		out[p] = kb.LinkStat{Dir: x.Type == 'd', Readable: x.Readable && x.Type != 'o', Same: sameOf(x)}
	}
	return out, true, nil
}

func sameOf(x transfers.PathIdent) links.Same {
	return links.Same{Dev: x.Dev, Ino: x.Ino, Birth: x.Birth}
}

func identOf(ctx context.Context, fs *transfers.RemoteFS, rel string) links.Same {
	got, ok, err := fs.Ident(ctx, []string{rel})
	if err != nil || !ok {
		return links.Same{}
	}
	return sameOf(got[rel])
}

// rewriting notes the file at rel of owner's library d before the Studio
// writes it anew or moves it, and answers what to call once it is at to: a
// link made for the file goes on to what took its place. Nothing is asked
// of the system unless the file has a link.
func (m *Module) rewriting(ctx context.Context, fs *transfers.RemoteFS, owner string, d *libraries.UserDef, rel string) func(to string) {
	p, ok := m.placeIn(owner, d, rel)
	store := kb.LinkStore()
	if !ok || !store.At(p) {
		return func(string) {}
	}
	was := identOf(ctx, fs, rel)
	return func(to string) {
		if q, ok := m.placeIn(owner, d, to); ok && was.Known() {
			store.Rewrote(q, was, identOf(ctx, fs, to))
		}
	}
}

// linkMoved has the IDs in owner's library d follow a move.
func (m *Module) linkMoved(owner string, d *libraries.UserDef, from, to string) {
	p, ok := m.placeIn(owner, d, from)
	q, ok2 := m.placeIn(owner, d, to)
	if ok && ok2 {
		kb.LinkStore().Moved(p.Owner, p.Lib, p.Rel, q.Rel)
	}
}

// linkDeleted ends the IDs of what was deleted from owner's library d.
func (m *Module) linkDeleted(owner string, d *libraries.UserDef, rels []string) {
	for _, rel := range rels {
		if p, ok := m.placeIn(owner, d, rel); ok {
			kb.LinkStore().Deleted(p.Owner, p.Lib, p.Rel)
		}
	}
}

// linksRemoved forgets the IDs of a library that was removed.
func (m *Module) linksRemoved(root string) {
	if owner, err := m.Users.OwnerOfRoot(root); err == nil && owner != "" {
		kb.LinkStore().DropLibrary(owner, root[strings.LastIndexByte(root, ':')+1:])
	}
}
