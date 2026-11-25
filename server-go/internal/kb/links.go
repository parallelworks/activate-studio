package kb

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/links"
)

// Copy link (docs/REFERENCE.md, Links to items): an opaque ID for a file or
// a folder. The ID grants nothing. Following one looks for the item in the
// viewer's own libraries and reads its folder as them, as browsing does,
// and the answer for an ID that is not one, for an item that is not theirs
// to open and for one in a library they do not have is the same.

// LinkStat is one path as its viewer finds it.
type LinkStat struct {
	Dir bool
	// Readable: the viewer can read the file, or list and enter the folder.
	Readable bool
	Same     links.Same
}

// LinkAt is an item in one of the viewer's libraries.
type LinkAt struct {
	Lib Library
	Rel string
}

// LinkPlaces says where the items of people's own libraries are kept, and
// looks at their files as the viewer. The libraries track sets it.
type LinkPlaces interface {
	// Place is where an item in one of the viewer's own libraries is kept:
	// a folder shared with them is in its owner's library.
	Place(r *http.Request, lib Library, rel string) (links.Place, error)
	// Find is where the viewer's own libraries hold the item kept at p:
	// their own, or one made from a folder shared with them that holds it.
	// Without looking at any file.
	Find(r *http.Request, p links.Place) ([]LinkAt, error)
	// Look is the paths of lib that are there, as the viewer finds them in
	// one trip; ok false where the files cannot be looked at and only the
	// library's index says what is there.
	Look(r *http.Request, lib Library, rels []string) (found map[string]LinkStat, ok bool, err error)
}

// Links is set by the libraries track; nil, and only this host's libraries
// have links.
var Links LinkPlaces

// LinkStore is the IDs kept here; nil before the module is registered.
func LinkStore() *links.Store {
	if Default == nil {
		return nil
	}
	return Default.Links
}

// The answers of a link that does not open. linkNone is one text, whatever
// the reason: there is no such link, the viewer cannot open the item, or
// it is in a library they do not have.
const (
	linkNone = "This link does not open anything for you. There may be no such item, or you may not have access to it. If someone shared the folder it is in with you, add that folder to your libraries first, under Shared with me."
	linkGone = "The item this link was made for has been deleted, or was moved somewhere the link does not follow."
	linkAsk  = "Sign in to open this link."
)

func (m *Module) linkUser(r *http.Request) (id, name string, signedIn bool) {
	if u := auth.UserOf(r); u != nil {
		if u.Username != "" {
			return u.ID, u.Username, true
		}
		return u.ID, u.ID, true
	}
	if !m.A.Cfg.AuthEnabled() {
		return "single", "single", true
	}
	return "", "", false
}

// logLink keeps who made or opened a link, and the outcome: the ID and
// never a path or a name.
func (m *Module) logLink(what, id, user, outcome string) {
	if !links.ValidID(id) && id != "" {
		id = "invalid"
	}
	if err := m.Audit.Add(links.Entry{What: what, ID: id, User: user, Outcome: outcome}); err != nil {
		m.A.Log.Warn("a link was not logged in link-log", "err", err.Error())
	}
	m.A.Log.Info("link "+what, "id", id, "user", user, "outcome", outcome)
}

func (m *Module) signInFirst(w http.ResponseWriter) {
	o := O("error", linkAsk, "reason", "signed-out")
	if m.A.Cfg.OIDC.Enabled() {
		o.Set("signIn", auth.LoginPath)
	}
	writeJSON(w, http.StatusUnauthorized, o)
}

// linkRel is a path as links keep it.
func linkRel(rel string) (string, error) {
	p := trimSlashes(filepath.ToSlash(rel))
	if p == "" || p == "." {
		return "", nil
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\x00") {
			return "", Error(400, "path: a file or folder in the library")
		}
	}
	return p, nil
}

// makeLink is POST /api/links: the ID of an item the viewer can open.
func (m *Module) makeLink(w http.ResponseWriter, r *http.Request) error {
	id, name, ok := m.linkUser(r)
	if !ok {
		m.logLink("made", "", "", "signed-out")
		m.signInFirst(w)
		return nil
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := b.need(); err != nil {
		return err
	}
	rel, _ := b.str("path")
	lid, _ := libraryParam(r, b)
	var lib Library
	if strings.ToLower(JSTrim(lid)) == All {
		lib, rel, err = m.splitAll(r, rel)
	} else {
		lib, err = m.libByID(r, lid)
	}
	if err != nil {
		return m.handled(w, r, err)
	}
	if rel, err = linkRel(rel); err != nil {
		return err
	}
	st, how, err := m.look(r, lib, rel)
	if err != nil {
		return m.handled(w, r, err)
	}
	if how != lookOpen {
		m.logLink("made", "", name, "404")
		return Error(404, "That item is not there any more, or it is not open to you.")
	}
	place, err := m.placeOf(r, lib, rel)
	if err != nil {
		return err
	}
	rec, err := m.Links.Make(place, st.Dir, st.Same, id)
	if errors.Is(err, links.ErrTooMany) {
		return Error(429, "You have made the most links one person can make on this Studio.")
	}
	if err != nil {
		m.logLink("made", "", name, "error")
		m.A.Log.Error("a link was not made", "err", err.Error())
		return Error(500, "The link could not be kept, so it was not made.")
	}
	m.logLink("made", rec.ID, name, "made")
	writeJSON(w, 200, O("id", rec.ID))
	return nil
}

func (m *Module) placeOf(r *http.Request, lib Library, rel string) (links.Place, error) {
	if strings.HasPrefix(lib.IndexRoot, remotePrefix) {
		if Links == nil {
			return links.Place{}, Error(409, "%s has no links here", lib.Label)
		}
		return Links.Place(r, lib, rel)
	}
	return links.Place{Lib: lib.ID, Rel: rel}, nil
}

// openLink is GET /api/links/{id}: where the item is for the viewer.
func (m *Module) openLink(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	id := r.PathValue("id")
	_, name, ok := m.linkUser(r)
	if !ok {
		m.logLink("opened", id, "", "signed-out")
		m.signInFirst(w)
		return nil
	}
	answer := func(status int, outcome, text, reason string) error {
		m.logLink("opened", id, name, outcome)
		writeJSON(w, status, O("error", text, "reason", reason))
		return nil
	}
	rec, ok, err := m.Links.Get(id)
	if err != nil {
		m.A.Log.Error("the links kept here could not be read", "err", err.Error())
		m.logLink("opened", id, name, "error")
		return Error(500, "The links kept on this Studio could not be read.")
	}
	if !ok {
		return answer(404, "404", linkNone, "none")
	}
	at, err := m.findLink(r, rec.Place)
	if err != nil {
		return m.handled(w, r, err)
	}
	deleted, unreached := false, ""
	for _, x := range at {
		if rec.Deleted != "" {
			// Said to someone who can list the folder it was in.
			if _, how, err := m.lookFolder(r, x.Lib, path.Dir("/" + x.Rel)[1:]); err == nil && how == lookOpen {
				deleted = true
			} else if err != nil {
				unreached = unreachedText(x.Lib, err)
			}
			continue
		}
		st, how, err := m.look(r, x.Lib, x.Rel)
		switch {
		case err != nil:
			unreached = unreachedText(x.Lib, err)
		case how == lookGone:
			deleted = true
		case how == lookOpen:
			replaced := st.Dir != rec.Dir || !st.Same.Is(rec.Same)
			typ := "file"
			if st.Dir {
				typ = "dir"
			}
			outcome := "opened"
			if replaced {
				outcome = "replaced"
			}
			m.logLink("opened", id, name, outcome)
			writeJSON(w, 200, O("id", id, "library", x.Lib.ID, "path", x.Rel, "type", typ, "replaced", replaced))
			return nil
		}
	}
	switch {
	case deleted:
		return answer(410, "deleted", linkGone, "deleted")
	case unreached != "":
		return answer(502, "unreachable", unreached, "unreachable")
	}
	return answer(404, "404", linkNone, "none")
}

// LinkUnreached is why a library could not be looked in, in words that
// name nothing in it. Any other error of a look may hold the item's path,
// which its viewer may have no access to.
type LinkUnreached struct{ Message string }

func (e *LinkUnreached) Error() string { return e.Message }

func unreachedText(lib Library, err error) string {
	var u *LinkUnreached
	if errors.As(err, &u) {
		return u.Message
	}
	return lib.Label + " could not be read now. Try again in a moment."
}

// findLink is where the viewer's libraries hold the item at p.
func (m *Module) findLink(r *http.Request, p links.Place) ([]LinkAt, error) {
	if p.Owner != "" {
		if Links == nil {
			return nil, nil
		}
		return Links.Find(r, p)
	}
	lib, ok, err := m.Libs.Get(p.Lib)
	if err != nil || !ok {
		return nil, nil
	}
	return []LinkAt{{ForRequest(r, lib), p.Rel}}, nil
}

type lookResult int

const (
	// lookNone: nothing is said, whatever the reason.
	lookNone lookResult = iota
	lookOpen
	// lookGone: the viewer can list the nearest folder it was in, and it
	// is not there.
	lookGone
)

// ancestors are the folders above rel, nearest first, "" last.
func ancestors(rel string) []string {
	if rel == "" {
		return nil
	}
	var out []string
	for d := path.Dir(rel); d != "." && d != "/"; d = path.Dir(d) {
		out = append(out, d)
	}
	return append(out, "")
}

// lookFolder is look for a folder something was in: the nearest one that
// is still there, which the viewer must be able to list.
func (m *Module) lookFolder(r *http.Request, lib Library, rel string) (LinkStat, lookResult, error) {
	st, how, err := m.look(r, lib, rel)
	if err == nil && how == lookGone {
		return st, lookOpen, nil
	}
	return st, how, err
}

// look is whether the viewer can open what is at rel in lib now.
func (m *Module) look(r *http.Request, lib Library, rel string) (LinkStat, lookResult, error) {
	up := ancestors(rel)
	if lib.Elsewhere() {
		if Links != nil && lib.Site != "" && !lib.Bucket && !lib.ReadThrough {
			found, ok, err := Links.Look(r, lib, append([]string{rel}, up...))
			if err != nil {
				return LinkStat{}, lookNone, err
			}
			if ok {
				st, how := lookIn(found, rel, up)
				return st, how, nil
			}
		}
		return m.lookIndex(r, lib, rel)
	}
	if lib.SourceRoot == "" {
		return LinkStat{}, lookNone, Error(409, "%s has no files on this host; it can be searched and described but not opened", lib.Label)
	}
	found := map[string]LinkStat{}
	for _, p := range append([]string{rel}, up...) {
		st, ok, err := localLinkStat(lib.SourceRoot, p)
		if err != nil {
			return LinkStat{}, lookNone, nil
		}
		if ok {
			found[p] = st
		}
	}
	st, how := lookIn(found, rel, up)
	return st, how, nil
}

func lookIn(found map[string]LinkStat, rel string, up []string) (LinkStat, lookResult) {
	if st, ok := found[rel]; ok {
		if st.Readable {
			return st, lookOpen
		}
		return LinkStat{}, lookNone
	}
	for _, d := range up {
		if st, ok := found[d]; ok {
			if st.Dir && st.Readable {
				return LinkStat{}, lookGone
			}
			return LinkStat{}, lookNone
		}
	}
	return LinkStat{}, lookNone
}

// localLinkStat is a path of a library here: as ListDir shows it, with
// what the Studio's account can read of it.
func localLinkStat(root, rel string) (LinkStat, bool, error) {
	abs, err := Resolve(rel, root)
	if err != nil {
		return LinkStat{}, false, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		if missing(err) {
			return LinkStat{}, false, nil
		}
		return LinkStat{}, true, nil
	}
	if rel != "" {
		parts := strings.Split(rel, "/")
		for i, p := range parts {
			if Excluded(p, i < len(parts)-1 || st.IsDir()) {
				return LinkStat{}, false, Error(404, "not found")
			}
		}
	}
	out := LinkStat{Dir: st.IsDir()}
	if !out.Dir && !st.Mode().IsRegular() {
		return out, true, nil
	}
	if f, err := os.Open(abs); err == nil {
		out.Readable = true
		if out.Dir {
			if _, err := f.ReadDir(1); err != nil && !errors.Is(err, io.EOF) {
				out.Readable = false
			}
		}
		f.Close()
	}
	out.Same = LocalSame(abs)
	return out, true, nil
}

// LocalSame tells the file at abs from another that takes its path.
func LocalSame(abs string) links.Same {
	key, birth, _, ok := identity(abs)
	if !ok {
		return links.Same{}
	}
	dev, ino, _ := strings.Cut(key, ".")
	if birth == "0" {
		birth = ""
	}
	return links.Same{Dev: dev, Ino: ino, Birth: birth}
}

// lookIndex is look in a library whose files only its index lists (a
// bucket, an index connected as it is, files read through another host):
// nothing there tells one file from another at the same path. A folder
// that lists empty may be one the viewer cannot read, so it says nothing
// of what was in it.
func (m *Module) lookIndex(r *http.Request, lib Library, rel string) (LinkStat, lookResult, error) {
	if rel == "" {
		return LinkStat{Dir: true, Readable: true}, lookOpen, nil
	}
	for i, d := range ancestors(rel) {
		list, err := m.remoteTree(r, lib, d, true)
		if err != nil {
			var ae *apierr.Error
			switch {
			case errors.As(err, &ae) && ae.Status == 404:
				continue
			case errors.As(err, &ae) && ae.Status < 500 && ae.Status != 409 && ae.Status != 401:
				return LinkStat{}, lookNone, nil
			}
			return LinkStat{}, lookNone, err
		}
		if i == 0 {
			for _, e := range list {
				if e.Path == rel {
					return LinkStat{Dir: e.Type == "dir", Readable: true}, lookOpen, nil
				}
			}
		}
		if len(list) > 0 {
			return LinkStat{}, lookGone, nil
		}
		return LinkStat{}, lookNone, nil
	}
	return LinkStat{}, lookNone, nil
}

// rewriting notes the file at rel in a library of this host before the
// Studio writes it anew, and answers what to call after: a link made for
// the file goes on to the file that took its place.
func (f *Files) rewriting(rel, abs string) func() {
	p := links.Place{Lib: PrimaryID, Rel: trimSlashes(filepath.ToSlash(rel))}
	if !f.Links.At(p) {
		return func() {}
	}
	was := LocalSame(abs)
	return func() { f.Links.Rewrote(p, was, LocalSame(abs)) }
}

// RewritingMain is rewriting for a file of the main library written by
// another track.
func RewritingMain(rel, abs string) func() {
	if Default == nil {
		return func() {}
	}
	return Default.Files.rewriting(rel, abs)
}
