package libraries

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/index"
)

// A person's own bucket libraries on a Studio where people sign in
// (docs/LIBRARIES.md, Buckets). Their index is on this computer, so unlike
// a library on one of the person's systems nothing but the Studio keeps
// others out of it. What does:
//
//   - Each is kept in its owner's folder, INDEX_BASE/users/<hash>, which is
//     this account's alone (PrivateDir): the list in buckets.json, and the
//     index, the text and the records of each in buckets/<id>.
//   - They are in none of the lists a request without a viewer reads
//     (AddedDef, AddedDefs, the deployment's List). A viewer's List adds
//     their own, and OwnedDef answers only with the owner.
//   - Their ids are random, so one person's says nothing to another, and no
//     id is refused for being taken.
//
// With sign-in off there is one person, and their bucket libraries are
// among the Studio's own.

// ownedRe is an owned library's id: no id made from a label is like it.
var ownedRe = regexp.MustCompile(`^pwb-[0-9a-f]{16}$`)

type ownedList struct {
	owner string
	stamp string
	defs  []Def
}

func (r *Registry) ownedFile(hash string) string {
	return filepath.Join(r.o.IndexBase, "users", hash, "buckets.json")
}

// ownedLocked is the bucket libraries in an owner's folder.
func (r *Registry) ownedLocked(hash string) ownedList {
	f := r.ownedFile(hash)
	st := fileStamp(f)
	if l, ok := r.owned[hash]; ok && l.stamp == st {
		return l
	}
	l := ownedList{stamp: st}
	if b, err := os.ReadFile(f); err == nil {
		for _, d := range parseDefs(b) {
			if ownedRe.MatchString(d.ID) && d.Bucket != nil {
				l.defs = append(l.defs, d)
			}
		}
		if o, err := os.ReadFile(filepath.Join(filepath.Dir(f), "owner")); err == nil {
			l.owner = string(o)
		}
		// A folder that is not its owner's is nobody's.
		if ownerHash(l.owner) != hash {
			l = ownedList{stamp: st}
		}
		for i := range l.defs {
			l.defs[i].Owner = l.owner
		}
	}
	if r.owned == nil {
		r.owned, r.ownedHash = map[string]ownedList{}, map[string]string{}
	}
	r.owned[hash] = l
	for _, d := range l.defs {
		r.ownedHash[d.ID] = hash
	}
	return l
}

// everyOwnedLocked is every person's bucket libraries, for the work the
// Studio does on its own.
func (r *Registry) everyOwnedLocked() []Def {
	var out []Def
	dirs, _ := os.ReadDir(filepath.Join(r.o.IndexBase, "users"))
	for _, d := range dirs {
		if d.IsDir() && hash16.MatchString(d.Name()) {
			out = append(out, r.ownedLocked(d.Name()).defs...)
		}
	}
	return out
}

// hashOfLocked is the owner's folder an owned library is in; "" for none.
func (r *Registry) hashOfLocked(id string) string {
	if !ownedRe.MatchString(id) {
		return ""
	}
	if h, ok := r.ownedHash[id]; ok {
		return h
	}
	r.everyOwnedLocked()
	return r.ownedHash[id]
}

func (r *Registry) saveOwnedLocked(hash string, defs []Def) error {
	if defs == nil {
		defs = []Def{}
	}
	b, err := marshalJS(defs, "  ")
	if err != nil {
		return err
	}
	f := r.ownedFile(hash)
	if err := writeAtomicMode(f, b, 0o600); err != nil {
		return err
	}
	delete(r.owned, hash)
	return nil
}

// Owned is an owner's bucket libraries; none with sign-in off.
func (r *Registry) Owned(owner string) []Def {
	if owner == "" || !r.o.Users.AuthEnabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.ownedLocked(ownerHash(owner))
	if l.owner != owner {
		return nil
	}
	return append([]Def{}, l.defs...)
}

// OwnedDef is an owner's bucket library of this id; nil for anyone else's.
func (r *Registry) OwnedDef(owner, id string) *Def {
	for _, d := range r.Owned(owner) {
		if d.ID == id {
			return &d
		}
	}
	return nil
}

// EveryOwned is every person's bucket libraries, for the Studio's own
// background work; none with sign-in off. Nothing that answers a request
// may use it.
func (r *Registry) EveryOwned() []Def {
	if !r.o.Users.AuthEnabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.everyOwnedLocked()
}

// BuiltDef is the library of this id that the Studio builds, the Studio's
// own or a person's bucket library, for the work of a build. A route finds
// a library by its viewer first (OwnedDef, List).
func (r *Registry) BuiltDef(id string) *Def {
	if !ownedRe.MatchString(id) {
		return r.AddedDef(id)
	}
	for _, d := range r.EveryOwned() {
		if d.ID == id {
			return &d
		}
	}
	return nil
}

// AddOwned keeps a new bucket library of owner's, which gets an id and a
// folder of its own (dir, where def's index goes).
func (r *Registry) AddOwned(owner string, make func(id, dir string) Def) (Library, error) {
	if owner == "" || !r.o.Users.AuthEnabled {
		return Library{}, apierr.New(403, "sign in to add a library of your own")
	}
	raw := [8]byte{}
	if _, err := rand.Read(raw[:]); err != nil {
		return Library{}, err
	}
	id, hash := "pwb-"+hex.EncodeToString(raw[:]), ownerHash(owner)
	home := filepath.Join(r.o.IndexBase, "users", hash)
	for _, dir := range []string{filepath.Dir(home), home, filepath.Join(home, "buckets"), filepath.Join(home, "buckets", id)} {
		if err := PrivateDir(dir); err != nil {
			return Library{}, err
		}
	}
	d := make(id, filepath.Join(home, "buckets", id))
	d.ID, d.Label = id, truncRunes(d.Label, 60)
	r.mu.Lock()
	err := os.WriteFile(filepath.Join(home, "owner"), []byte(owner), 0o600)
	if err == nil {
		err = r.saveOwnedLocked(hash, append(append([]Def{}, r.ownedLocked(hash).defs...), d))
	}
	r.mu.Unlock()
	if err != nil {
		return Library{}, err
	}
	return r.Get(id, "", ViewerOf(owner))
}

// RemoveOwned removes an owner's bucket library and its index.
func (r *Registry) RemoveOwned(owner, id string) error {
	d := r.OwnedDef(owner, id)
	if d == nil {
		return apierr.New(404, "no library named %s", id)
	}
	if d.Build != nil && (d.Build.State == "building" || d.Build.State == "updating") {
		return apierr.New(409, "%s is being indexed; remove it once the build has finished", d.Label)
	}
	dir, hash := r.BuiltLibraryDir(id), ownerHash(owner)
	r.mu.Lock()
	err := r.saveOwnedLocked(hash, slices.DeleteFunc(append([]Def{}, r.ownedLocked(hash).defs...), func(d Def) bool { return d.ID == id }))
	r.mu.Unlock()
	if err != nil {
		return err
	}
	index.RmTree(dir)
	// What else this computer kept for it: the copies previews were made
	// from, and its labels.
	for _, top := range []string{"remote-previews", "libraries"} {
		os.RemoveAll(filepath.Join(r.o.IndexBase, top, id))
	}
	for _, fn := range r.removedHooks {
		fn(id)
	}
	r.o.Settings.Forget(id)
	r.mu.Lock()
	delete(r.ownedHash, id)
	r.mu.Unlock()
	return nil
}

// modifyOwnedLocked changes one owned library's definition.
func (r *Registry) modifyOwnedLocked(id string, fn func(*Def)) bool {
	hash := r.hashOfLocked(id)
	if hash == "" {
		return false
	}
	defs := append([]Def{}, r.ownedLocked(hash).defs...)
	for i := range defs {
		if defs[i].ID == id {
			fn(&defs[i])
			return r.saveOwnedLocked(hash, defs) == nil
		}
	}
	return false
}

// ownedLibrariesOf is the viewer's own bucket libraries, as a list shows
// them.
func (r *Registry) ownedLibrariesOf(viewer Viewer) []Library {
	if viewer == nil {
		return nil
	}
	var out []Library
	for _, d := range r.Owned(r.o.Users.OwnerOf(*viewer)) {
		l := r.fromDef(d, false)
		l.Owner = d.Owner
		out = append(out, l)
	}
	return out
}
