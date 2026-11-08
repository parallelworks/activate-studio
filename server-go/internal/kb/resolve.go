package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
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
		return lib, nil
	}
	if LibraryResolver != nil {
		return LibraryResolver.Library(r, id)
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
	if LibraryResolver != nil {
		return LibraryResolver.All(r)
	}
	return m.Libs.ScopeAll(), nil
}

// remotePrefix starts the index root of a library on another system.
const remotePrefix = "remote:"

// ---- counts of a library on another system ----

// remoteStats keeps the counts of libraries on other systems by the time
// their index was built, in INDEX_BASE/remote-stats.json (gufi.ts).
type remoteStats struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]remoteStat
	order  []string
}

type remoteStat struct {
	BuiltAt string          `json:"builtAt"`
	Stats   json.RawMessage `json:"stats"`
}

var keptRemote remoteStats

func (m *Module) remoteStatsFile() string {
	return filepath.Join(m.Roots.IndexBase, "remote-stats.json")
}

func (k *remoteStats) load(file string) {
	if k.loaded {
		return
	}
	k.loaded, k.m = true, map[string]remoteStat{}
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
			return
		}
		key, _ := kt.(string)
		var v remoteStat
		if dec.Decode(&v) != nil {
			return
		}
		if _, had := k.m[key]; !had {
			k.order = append(k.order, key)
		}
		k.m[key] = v
	}
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
	if LibraryResolver == nil {
		return []Entry{}, nil
	}
	list, err := LibraryResolver.Tree(r, lib, rel)
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for _, e := range list {
		if showHidden || !HiddenName(e.Name) {
			out = append(out, e)
		}
	}
	kept := m.Tags.Library.Map(lib.ID)
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

// remoteFile is a file of a library on another system, or read through
// another host.
func (m *Module) remoteFile(r *http.Request, lib Library, rel string) (*Obj, error) {
	if LibraryResolver == nil {
		return nil, Error(409, "%s has no files on this host; it can be searched and described but not opened", lib.Label)
	}
	return LibraryResolver.File(r, lib, rel)
}
