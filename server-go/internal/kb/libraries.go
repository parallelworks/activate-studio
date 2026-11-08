package kb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// PrimaryID is the main library's id.
const PrimaryID = "kb"

// All names every library at once.
const All = "all"

// Library is what the file routes need of libraries.ts's Library: where its
// files and caches are, and whether they may be changed.
type Library struct {
	ID         string
	Label      string
	IndexRoot  string
	SourceRoot string // "" when its files are not on this host
	CacheRoot  string // a built library's extracted text; "" for the main cache
	Writable   bool
	Primary    bool
	Pinned     bool
	// Indexed is caps.index: a db.db at the index root.
	Indexed bool
	// ReadThrough is set when the library names another host for its
	// originals (originals.ts).
	ReadThrough bool
	// Site is the system a viewer's own library lives on; "" for one here.
	Site string
}

// libraryDef is an entry of STUDIO_LIBRARIES or INDEX_BASE/libraries.json.
type libraryDef struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	IndexRoot  string  `json:"indexRoot"`
	SourceRoot *string `json:"sourceRoot"`
	Build      *struct {
		CacheRoot *string `json:"cacheRoot"`
	} `json:"build"`
	ReadThrough json.RawMessage `json:"readThrough"`
}

var libraryIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// Libraries reads the libraries this host has: the main one, those the
// deployment pins (STUDIO_LIBRARIES) and those added in Settings
// (INDEX_BASE/libraries.json, read again when it changes). A viewer's own
// libraries on their clusters (userLibraries.ts) are not here: their
// files are reached over ssh, which Node still does; Has says whether a
// viewer has any.
type Libraries struct {
	Roots Roots
	// Pinned is STUDIO_LIBRARIES.
	Pinned string
	// Label is the main library's label (effectiveSettings().kbLabel).
	Label func() string
	// AuthEnabled is whether people sign in, which decides whose a
	// viewer's own libraries are (ownerOf).
	AuthEnabled bool

	mu    sync.Mutex
	stamp string
	added []libraryDef
}

func (l *Libraries) file() string { return filepath.Join(l.Roots.IndexBase, "libraries.json") }

func (l *Libraries) loadAdded() []libraryDef {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.file()
	stamp := fileStamp(f)
	if stamp == l.stamp && l.added != nil {
		return l.added
	}
	l.stamp = stamp
	l.added = parseDefs(readFileOr(f))
	return l.added
}

func parseDefs(b []byte) []libraryDef {
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return []libraryDef{}
	}
	out := []libraryDef{}
	for _, r := range raw {
		var d libraryDef
		if json.Unmarshal(r, &d) != nil {
			continue
		}
		// isDef: a string id that fits and a non-empty index root.
		var probe map[string]any
		json.Unmarshal(r, &probe)
		if _, ok := probe["id"].(string); !ok || !libraryIDRe.MatchString(d.ID) {
			continue
		}
		if s, ok := probe["indexRoot"].(string); !ok || s == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func (l *Libraries) primary() Library {
	label := ""
	if l.Label != nil {
		label = l.Label()
	}
	if label == "" {
		label = filepath.Base(l.Roots.KBRoot)
	}
	return Library{ID: PrimaryID, Label: label, IndexRoot: l.Roots.GufiIndex, SourceRoot: l.Roots.KBRoot,
		Writable: true, Primary: true, Pinned: true, Indexed: true}
}

func fromDef(d libraryDef, pinned bool) Library {
	abs := jsResolve(d.IndexRoot, "")
	label := d.Label
	if label == "" {
		label = filepath.Base(abs)
	}
	if label == "" || label == "/" {
		label = d.ID
	}
	lib := Library{ID: d.ID, Label: label, IndexRoot: abs, Pinned: pinned, Indexed: exists(filepath.Join(abs, "db.db"))}
	if d.SourceRoot != nil && *d.SourceRoot != "" {
		lib.SourceRoot = jsResolve(*d.SourceRoot, "")
	}
	if d.Build != nil && d.Build.CacheRoot != nil {
		lib.CacheRoot = *d.Build.CacheRoot
	}
	if rt := strings.TrimSpace(string(d.ReadThrough)); rt != "" && rt != "null" {
		lib.ReadThrough = true
	}
	return lib
}

// List is listLibraries without the viewer's own: the main library first,
// then the pinned, then those added in Settings, each id once.
func (l *Libraries) List() []Library {
	seen := map[string]bool{PrimaryID: true}
	out := []Library{l.primary()}
	pinned := parseDefs([]byte(l.Pinned))
	for _, group := range []struct {
		defs   []libraryDef
		pinned bool
	}{{pinned, true}, {l.loadAdded(), false}} {
		for _, d := range group.defs {
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			out = append(out, fromDef(d, group.pinned))
		}
	}
	return out
}

// Get is getLibrary for a library this host has: ok false when the id is
// not one of them (a viewer's own library, or none, which Node answers).
// "all" is refused as getLibrary refuses it.
func (l *Libraries) Get(id string) (Library, bool, error) {
	want := strings.TrimSpace(id)
	if want == "" {
		want = PrimaryID
	}
	if strings.ToLower(want) == All {
		return Library{}, false, Error(400, "This action works in one library at a time, and the items are in more than one (All libraries is selected).")
	}
	for _, lib := range l.List() {
		if lib.ID == want {
			return lib, true, nil
		}
	}
	return Library{}, false, nil
}

// ScopeAll is scopeLibraries('all') without the viewer's own libraries:
// those with an index, and the main one always.
func (l *Libraries) ScopeAll() []Library {
	out := []Library{}
	for _, lib := range l.List() {
		if lib.Indexed || lib.Primary {
			out = append(out, lib)
		}
	}
	return out
}

// RequireWritable is the guard every mutation runs.
func RequireWritable(lib Library) error {
	if !lib.Writable {
		return Error(403, "%s is read only", lib.Label)
	}
	return nil
}

func readFileOr(f string) []byte {
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	return b
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
