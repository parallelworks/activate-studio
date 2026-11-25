package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
	"github.com/parallelworks/activate-studio/server-go/internal/staging"
)

// Resolver is the libraries track's view of what a request can read
// (libraries.ts, userLibraries.ts, originals.ts): the viewer's own
// libraries on their systems, the All scope for the viewer, and a
// library's files on another system or read through another host. The
// libraries track sets LibraryResolver; without it, the module answers from the
// libraries this host has.
type Resolver interface {
	// Library is getLibrary for the viewer: a 404 error for none.
	Library(r *http.Request, id string) (Library, error)
	// All is scopeLibraries('all') for the viewer.
	All(r *http.Request) ([]Library, error)
	// Tree is one folder of a library on another system, from its index.
	Tree(r *http.Request, lib Library, rel string) ([]Entry, error)
	// File is a file of a library on another system, or one read through
	// another host (openOriginal).
	File(r *http.Request, lib Library, rel string) (*Obj, error)
	// Download is the same, whole (downloadOriginal, remoteDownload).
	Download(r *http.Request, lib Library, rel string) (name string, body []byte, err error)
	// Aggregate runs a gufi_query aggregate over a library on another
	// system, answering its printed rows.
	Aggregate(ctx context.Context, indexRoot string, args []string) (stdout string, err error)
	// BuiltAt is when the index of a library on another system was built,
	// as its entry records it; "" when not known.
	BuiltAt(indexRoot string) string
}

// LibraryResolver is set by the libraries track as it registers.
var LibraryResolver Resolver

// lib is libOf: the library a request names, among the viewer's.
func (m *Module) lib(r *http.Request, b body) (Library, error) {
	id, _ := libraryParam(r, b)
	return m.libByID(r, id)
}

// libByID is getLibrary(id) for the request's viewer.
func (m *Module) libByID(r *http.Request, id string) (Library, error) {
	lib, ok, err := m.Libs.Get(id)
	if err != nil {
		return Library{}, err
	}
	if ok {
		return ForRequest(r, lib), nil
	}
	if LibraryResolver != nil {
		lib, err := LibraryResolver.Library(r, id)
		return ForRequest(r, lib), err
	}
	return Library{}, Error(404, "no library named %s", trimmedOr(id, PrimaryID))
}

func trimmedOr(id, def string) string {
	if v := JSTrim(id); v != "" {
		return v
	}
	return def
}

// allLibs is scopeLibraries('all') for the viewer.
func (m *Module) allLibs(r *http.Request) ([]Library, error) {
	var libs []Library
	var err error
	if LibraryResolver != nil {
		libs, err = LibraryResolver.All(r)
	} else {
		libs = m.Libs.ScopeAll()
	}
	for i := range libs {
		libs[i] = ForRequest(r, libs[i])
	}
	return libs, err
}

// MainWriter says whether the person in a request may change the main
// library, and why not (mainlib.Request); nil lets everyone, as on a
// Studio without sign-in.
var MainWriter func(r *http.Request) (bool, string)

// ForRequest is lib as the person in r may use it: the main library is
// read only for one who may not change it.
func ForRequest(r *http.Request, lib Library) Library {
	if lib.Primary && lib.Writable && MainWriter != nil && r != nil {
		if ok, why := MainWriter(r); !ok {
			lib.Writable = false
			lib.ReadOnlyWhy = why
		}
	}
	return lib
}

// remotePrefix starts the index root of a library on another system.
const remotePrefix = "remote:"

// ---- counts of a library on another system ----

// remoteStats keeps the counts of libraries on other systems by the time
// their index was built, in INDEX_BASE/remote-stats.json (gufi.ts), keyed
// by the library's root ("remote:<owner hash>:<id>").
type remoteStats struct {
	mu     sync.Mutex
	file   string
	loaded bool
	m      map[string]remoteStat
	order  []string
}

type remoteStat struct {
	BuiltAt string          `json:"builtAt"`
	Stats   json.RawMessage `json:"stats"`
}

var keptRemote remoteStats

// RemoteStatsKnown says whether a root in remote-stats.json is still a
// library; the libraries track sets it. Entries of libraries removed
// before removal dropped them (or while another server ran) are dropped
// when the file is read.
var RemoteStatsKnown func(root string) bool

func (m *Module) remoteStatsFile() string {
	return filepath.Join(m.Roots.IndexBase, "remote-stats.json")
}

func (k *remoteStats) load(file string) {
	if k.loaded && k.file == file {
		return
	}
	k.file, k.loaded, k.m, k.order = file, true, map[string]remoteStat{}, nil
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			break
		}
		key, _ := kt.(string)
		var v remoteStat
		if dec.Decode(&v) != nil {
			break
		}
		if _, had := k.m[key]; !had {
			k.order = append(k.order, key)
		}
		k.m[key] = v
	}
	if known := RemoteStatsKnown; known != nil {
		n := len(k.order)
		k.order = slices.DeleteFunc(k.order, func(key string) bool {
			if known(key) {
				return false
			}
			delete(k.m, key)
			return true
		})
		if len(k.order) != n {
			k.save(file)
		}
	}
}

// LoadRemoteStats reads remote-stats.json under indexBase now, dropping the
// entries of libraries that no longer exist, so an install that removed
// libraries before this was done is cleaned up at start.
func LoadRemoteStats(indexBase string) {
	keptRemote.mu.Lock()
	defer keptRemote.mu.Unlock()
	keptRemote.loaded = false
	keptRemote.load(filepath.Join(indexBase, "remote-stats.json"))
}

// ForgetRemoteStats drops a removed library's counts from
// remote-stats.json under indexBase.
func ForgetRemoteStats(indexBase, root string) {
	file := filepath.Join(indexBase, "remote-stats.json")
	keptRemote.mu.Lock()
	defer keptRemote.mu.Unlock()
	keptRemote.load(file)
	if _, ok := keptRemote.m[root]; !ok {
		return
	}
	delete(keptRemote.m, root)
	keptRemote.order = slices.DeleteFunc(keptRemote.order, func(key string) bool { return key == root })
	keptRemote.save(file)
}

func (k *remoteStats) save(file string) {
	buf := []byte{'{'}
	for i, key := range k.order {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, _ := json.Marshal(key)
		vb, _ := json.Marshal(k.m[key])
		buf = append(append(append(buf, kb...), ':'), vb...)
	}
	buf = append(buf, '}')
	os.WriteFile(file, buf, 0o644)
}

// remoteCorpus is corpusStats for a library on another system: counted
// there in one aggregate, kept until its index is built again.
func (m *Module) remoteCorpus(ctx context.Context, indexRoot string, args []string) (corpus, error) {
	if LibraryResolver == nil {
		return corpus{}, nil
	}
	file := m.remoteStatsFile()
	keptRemote.mu.Lock()
	keptRemote.load(file)
	hit, ok := keptRemote.m[indexRoot]
	keptRemote.mu.Unlock()
	builtAt := LibraryResolver.BuiltAt(indexRoot)
	if ok && builtAt != "" && hit.BuiltAt == builtAt {
		var c corpusJSON
		if json.Unmarshal(hit.Stats, &c) == nil {
			return c.corpus(), nil
		}
	}
	stdout, err := LibraryResolver.Aggregate(ctx, indexRoot, args)
	if err != nil {
		return corpus{}, err
	}
	c := parseCorpus(stdout)
	if builtAt != "" {
		raw, _ := json.Marshal(c.json())
		keptRemote.mu.Lock()
		if _, had := keptRemote.m[indexRoot]; !had {
			keptRemote.order = append(keptRemote.order, indexRoot)
		}
		keptRemote.m[indexRoot] = remoteStat{BuiltAt: builtAt, Stats: raw}
		keptRemote.save(file)
		keptRemote.mu.Unlock()
	}
	return c, nil
}

type corpusJSON struct {
	Available  bool       `json:"available"`
	Files      int64      `json:"files"`
	Dirs       int64      `json:"dirs"`
	TotalBytes int64      `json:"totalBytes"`
	ByExt      []extCount `json:"byExt"`
}

func (c corpus) json() corpusJSON {
	return corpusJSON{c.Available, c.Files, c.Dirs, c.Total, c.ByExt}
}

func (c corpusJSON) corpus() corpus {
	if c.ByExt == nil {
		c.ByExt = []extCount{}
	}
	return corpus{c.Available, c.Files, c.Dirs, c.TotalBytes, c.ByExt}
}

// remoteTree is a folder of a library on another system, from its index
// there (which holds dot entries too), with the labels the Studio keeps.
func (m *Module) remoteTree(r *http.Request, lib Library, rel string, showHidden bool) ([]Entry, error) {
	var list []Entry
	var err error
	switch {
	case lib.AsViewer() && SpaceFiles != nil:
		// A space lists as its folder is now, so a file just written shows
		// before the index catches up.
		list, err = SpaceFiles.List(r, lib, rel)
	case LibraryResolver == nil:
		return []Entry{}, nil
	default:
		list, err = LibraryResolver.Tree(r, lib, rel)
	}
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, e := range list {
		if (showHidden || !HiddenName(e.Name)) && !staging.Name(e.Name) && (showHidden || !m.ownInSpace(r, lib, e)) {
			out = append(out, e)
		}
	}
	kept := m.Tags.Library.Map(lib.StateKey())
	if len(kept) == 0 {
		return out, nil
	}
	for i := range out {
		if t := kept[out[i].Path]; len(t) > 0 {
			out[i].Tags = t
		}
	}
	return out, nil
}

// ownInSpace reports whether e, an entry of a space listed as it is now, is
// the Studio's own AGENTS.md (internal/ownfile), read there once for each
// change of it. Any other library on another system lists from its index,
// which does not hold it.
func (m *Module) ownInSpace(r *http.Request, lib Library, e Entry) bool {
	if e.Name != ownfile.Name || e.Type != "file" || e.Size > ownfile.Max || !lib.AsViewer() || SpaceFiles == nil {
		return false
	}
	k := fmt.Sprint(lib.IndexRoot, "\x00", e.Path, "\x00", e.Size, "\x00", e.Mtime)
	m.ownMu.Lock()
	is, known := m.own[k]
	m.ownMu.Unlock()
	if known {
		return is
	}
	b, err := SpaceFiles.Get(r, lib, e.Path, ownfile.Max)
	if err != nil {
		return false
	}
	is = ownfile.Is(b)
	m.ownMu.Lock()
	defer m.ownMu.Unlock()
	if m.own == nil || len(m.own) >= 2000 {
		m.own = map[string]bool{}
	}
	m.own[k] = is
	return is
}

// remoteFile is a file of a library on another system, or read through
// another host.
func (m *Module) remoteFile(r *http.Request, lib Library, rel string) (*Obj, error) {
	if LibraryResolver == nil {
		return nil, Error(409, "%s has no files on this host; it can be searched and described but not opened", lib.Label)
	}
	return LibraryResolver.File(r, lib, rel)
}
