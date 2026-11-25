// Package links keeps the opaque IDs Copy link gives to files and folders.
// An ID names an item, not a place: it follows a rename or a move made
// through the Studio inside its library, and ends when the item is
// deleted. It grants nothing. Who may open the item, and where it is for
// them, is worked out by internal/kb each time a link is followed.
//
// The IDs are kept in INDEX_BASE/links.json, readable by the Studio's own
// account only: it is the one place that says what an ID stands for.
package links

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Place is where an item is kept: a library and the path in it. Owner is
// the person whose library it is, "" for one of the deployment's. A folder
// shared with someone is in its owner's library, whoever makes the link,
// so one item has one ID.
type Place struct {
	Owner string `json:"owner,omitempty"`
	Lib   string `json:"library"`
	Rel   string `json:"path"`
}

// Same tells one file from another that took its path: the device and
// inode it is on, and when it was made where the file system says. Empty
// where none of it is known (a bucket, an index read without its files).
type Same struct {
	Dev   string `json:"dev,omitempty"`
	Ino   string `json:"ino,omitempty"`
	Birth string `json:"birth,omitempty"`
}

// Known says whether there is anything to compare.
func (s Same) Known() bool { return s.Ino != "" }

// Is reports whether o is the same file, and is true when either is not
// known. The device is kept and not compared: a network file system has
// another number on each host it is mounted on and after each mount, and
// the two people a link passes between may reach it on two.
func (s Same) Is(o Same) bool {
	if !s.Known() || !o.Known() {
		return true
	}
	return s.Ino == o.Ino && (s.Birth == "" || o.Birth == "" || s.Birth == o.Birth)
}

// Record is one ID.
type Record struct {
	ID string `json:"id"`
	Place
	Dir  bool   `json:"folder,omitempty"`
	Same Same   `json:"file,omitzero"`
	By   string `json:"by,omitempty"` // who made the link, by id
	Made string `json:"made"`
	// Deleted is when the item was deleted through the Studio.
	Deleted string `json:"deleted,omitempty"`
}

type file struct {
	Version int       `json:"version"`
	Links   []*Record `json:"links"`
}

// MaxEach is the most links one person may make.
const MaxEach = 20_000

// ErrTooMany is MaxEach reached.
var ErrTooMany = errors.New("too many links")

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// ValidID reports whether id has the form of one: 128 random bits, with no
// dot in it, so a later address can put a deployment before one.
func ValidID(id string) bool { return idRe.MatchString(id) }

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Store is links.json. A nil Store keeps nothing and finds nothing.
type Store struct {
	path string
	// Warn is told what went wrong where there is nobody to answer.
	Warn func(msg string, args ...any)

	mu     sync.Mutex
	loaded bool
	byID   map[string]*Record
	byLib  map[[2]string][]*Record
	mine   map[string]int
	all    []*Record
}

// New is the store under indexBase.
func New(indexBase string) *Store {
	return &Store{path: filepath.Join(indexBase, "links.json")}
}

func (s *Store) warn(msg string, args ...any) {
	if s.Warn != nil {
		s.Warn(msg, args...)
	}
}

// load reads the file once; the caller holds mu. A file that cannot be
// read is left as it is, and no link is made over it. One that is not
// JSON is kept beside, for whoever looks into it.
func (s *Store) load() error {
	if s.loaded {
		return nil
	}
	s.byID, s.byLib, s.mine, s.all = map[string]*Record{}, map[[2]string][]*Record{}, map[string]int{}, nil
	b, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.loaded = true
		return nil
	case err != nil:
		return err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		aside := s.path + ".unreadable-" + time.Now().UTC().Format("20060102T150405")
		if rerr := os.Rename(s.path, aside); rerr != nil {
			return err
		}
		s.warn("links.json could not be read and was put aside", "file", aside, "err", err.Error())
		s.loaded = true
		return nil
	}
	for _, r := range f.Links {
		if r == nil || !ValidID(r.ID) || s.byID[r.ID] != nil {
			continue
		}
		s.keep(r)
	}
	s.loaded = true
	return nil
}

func (s *Store) keep(r *Record) {
	s.all = append(s.all, r)
	s.byID[r.ID] = r
	k := [2]string{r.Owner, r.Lib}
	s.byLib[k] = append(s.byLib[k], r)
	s.mine[r.By]++
}

// save writes the whole file beside itself, and puts it in place once it
// is on the disk; the caller holds mu.
func (s *Store) save() error {
	b, err := json.Marshal(file{Version: 1, Links: append([]*Record{}, s.all...)})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// CreateTemp makes it 0600.
	t, err := os.CreateTemp(dir, ".links-*.json")
	if err != nil {
		return err
	}
	_, err = t.Write(b)
	if err == nil {
		err = t.Sync()
	}
	if cerr := t.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(t.Name(), s.path)
	}
	if err != nil {
		os.Remove(t.Name())
		return err
	}
	// The rename itself, where the system lets a folder be synced.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// Get is an ID's record.
func (s *Store) Get(id string) (Record, bool, error) {
	if s == nil || !ValidID(id) {
		return Record{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Record{}, false, err
	}
	r := s.byID[id]
	if r == nil {
		return Record{}, false, nil
	}
	return *r, true, nil
}

// Make is the ID of the item at p, the one it has or a new one. An item
// whose file took another's path is another item.
func (s *Store) Make(p Place, dir bool, same Same, by string) (Record, error) {
	if s == nil {
		return Record{}, errors.New("links are not kept here")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		return Record{}, fmt.Errorf("the links kept here could not be read: %w", err)
	}
	for _, r := range s.byLib[[2]string{p.Owner, p.Lib}] {
		if r.Rel == p.Rel && r.Deleted == "" && r.Dir == dir && r.Same.Is(same) {
			if !r.Same.Known() && same.Known() {
				was := r.Same
				r.Same = same
				if err := s.save(); err != nil {
					r.Same = was
					s.warn("links.json was not written", "err", err.Error())
				}
			}
			return *r, nil
		}
	}
	if s.mine[by] >= MaxEach {
		return Record{}, ErrTooMany
	}
	r := &Record{ID: newID(), Place: p, Dir: dir, Same: same, By: by, Made: now()}
	s.keep(r)
	if err := s.save(); err != nil {
		// A link that would be lost at the next start is not given.
		s.drop(func(x *Record) bool { return x == r })
		return Record{}, fmt.Errorf("the link could not be kept: %w", err)
	}
	return *r, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// under reports whether rel is at or in from, and the rest of it.
func under(rel, from string) (string, bool) {
	if rel == from {
		return "", true
	}
	if from == "" {
		return "/" + rel, true
	}
	if strings.HasPrefix(rel, from+"/") {
		return rel[len(from):], true
	}
	return "", false
}

// Moved takes the IDs at from, and of everything in it, to to. A move
// never goes over something, so what had an ID at to was gone before.
func (s *Store) Moved(owner, lib, from, to string) {
	if from == "" || to == "" || from == to {
		return
	}
	at := now()
	s.change(owner, lib, func(r *Record) bool {
		if r.Deleted != "" {
			return false
		}
		if rest, ok := under(r.Rel, from); ok {
			r.Rel = to + rest
			return true
		}
		if _, ok := under(r.Rel, to); ok {
			r.Deleted = at
			return true
		}
		return false
	})
}

// Deleted ends the IDs at each of rels, and of everything in them.
func (s *Store) Deleted(owner, lib string, rels ...string) {
	at := now()
	s.change(owner, lib, func(r *Record) bool {
		if r.Deleted != "" {
			return false
		}
		for _, rel := range rels {
			if _, ok := under(r.Rel, rel); ok && rel != "" {
				r.Deleted = at
				return true
			}
		}
		return false
	})
}

// At says whether an ID is at p now, so what only matters to one is asked
// for only then.
func (s *Store) At(p Place) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.load() != nil {
		return false
	}
	return slices.ContainsFunc(s.byLib[[2]string{p.Owner, p.Lib}], func(r *Record) bool { return r.Rel == p.Rel && r.Deleted == "" })
}

// Rewrote is a file at p written anew through the Studio, from was to is:
// the ID of the file that was there goes on. An ID of some other file that
// had been there before is left alone.
func (s *Store) Rewrote(p Place, was, is Same) {
	if !was.Known() || !is.Known() || was == is {
		return
	}
	s.change(p.Owner, p.Lib, func(r *Record) bool {
		if r.Rel != p.Rel || r.Deleted != "" || !r.Same.Known() || !r.Same.Is(was) {
			return false
		}
		r.Same = is
		return true
	})
}

// DropLibrary forgets the IDs of a library that was removed.
func (s *Store) DropLibrary(owner, lib string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.load() != nil || len(s.byLib[[2]string{owner, lib}]) == 0 {
		return
	}
	s.drop(func(r *Record) bool { return r.Owner == owner && r.Lib == lib })
	if err := s.save(); err != nil {
		s.warn("links.json was not written", "err", err.Error())
	}
}

func (s *Store) drop(gone func(*Record) bool) {
	all := s.all
	s.byID, s.byLib, s.mine, s.all = map[string]*Record{}, map[[2]string][]*Record{}, map[string]int{}, nil
	for _, r := range all {
		if !gone(r) {
			s.keep(r)
		}
	}
}

// change runs fn over a library's records and writes the file when one
// changed. The change already happened to the files, so it stands here
// even when the file cannot be written, and goes out with the next write.
func (s *Store) change(owner, lib string, fn func(*Record) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(); err != nil {
		s.warn("links.json could not be read", "err", err.Error())
		return
	}
	any := false
	for _, r := range s.byLib[[2]string{owner, lib}] {
		if fn(r) {
			any = true
		}
	}
	if !any {
		return
	}
	if err := s.save(); err != nil {
		s.warn("links.json was not written", "err", err.Error())
	}
}
