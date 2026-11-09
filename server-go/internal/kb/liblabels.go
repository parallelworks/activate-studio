package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Labels on a library whose files the Studio does not write
// (libraryLabels.ts): one file per library, INDEX_BASE/libraries/<id>/
// labels.json, keyed by the file's path in the library. Shared with Node:
// read again when it changes, written by rename.

type libLabelEntry struct {
	Tags []string `json:"tags"`
	At   string   `json:"at"`
}

type libLabelStore struct {
	Version   int                       `json:"version"`
	IndexRoot string                    `json:"indexRoot"`
	Entries   map[string]*libLabelEntry `json:"entries"`
}

// LibraryLabels keeps those stores.
type LibraryLabels struct {
	Roots Roots

	mu     sync.Mutex
	stores map[string]*libLabelStore
	stamps map[string]string
	pruned map[string]time.Time
}

// LabelsFile is a library's labels file.
func (l *LibraryLabels) LabelsFile(id string) string {
	return filepath.Join(l.Roots.IndexBase, "libraries", id, "labels.json")
}

func (l *LibraryLabels) load(id string) *libLabelStore {
	if l.stores == nil {
		l.stores, l.stamps, l.pruned = map[string]*libLabelStore{}, map[string]string{}, map[string]time.Time{}
	}
	f := l.LabelsFile(id)
	now := fileStamp(f)
	if s := l.stores[id]; s != nil && l.stamps[id] == now {
		return s
	}
	l.stamps[id] = now
	store := &libLabelStore{Version: 1, Entries: map[string]*libLabelEntry{}}
	var raw libLabelStore
	if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &raw) == nil && raw.Version == 1 && raw.Entries != nil {
		store = &raw
	}
	l.stores[id] = store
	return store
}

func (l *LibraryLabels) persist(id string) error {
	f := l.LabelsFile(id)
	if err := os.MkdirAll(filepath.Dir(f), 0o777); err != nil {
		return err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(l.stores[id]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", f, os.Getpid())
	if err := os.WriteFile(tmp, bytes.TrimRight(b.Bytes(), "\n"), 0o666); err != nil {
		return err
	}
	if err := os.Rename(tmp, f); err != nil {
		return err
	}
	l.stamps[id] = fileStamp(f)
	return nil
}

// Set is setLibraryLabels: a path's label set; none removes the entry.
func (l *LibraryLabels) Set(lib Library, rel string, tags []string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	store := l.load(lib.ID)
	key := trimSlashes(rel)
	next := uniqueTags(tags)
	store.IndexRoot = lib.IndexRoot
	if len(next) > 0 {
		store.Entries[key] = &libLabelEntry{Tags: next, At: isoNow()}
	} else {
		delete(store.Entries, key)
	}
	return next, l.persist(lib.ID)
}

func uniqueTags(tags []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		n := NormalizeTag(t)
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// Of is libraryLabelsOf: the labels set on this very path.
func (l *LibraryLabels) Of(id, rel string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.load(id).Entries[trimSlashes(rel)]; e != nil {
		return e.Tags
	}
	return []string{}
}

// Map is libraryLabelMaps: labels by path, which serve as a file's own and
// a folder's, inherited below it.
func (l *LibraryLabels) Map(id string) map[string][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string][]string{}
	for rel, e := range l.load(id).Entries {
		out[rel] = e.Tags
	}
	return out
}

// Effective is libraryEffectiveLabels: own plus inherited.
func (l *LibraryLabels) Effective(id, rel string) *Obj {
	key := trimSlashes(rel)
	m := l.Map(id)
	own := uniq(m[key])
	ownSet := setOf(own)
	parts := strings.Split(key, "/")
	parts = parts[:len(parts)-1]
	inherited := []string{}
	seen := map[string]bool{}
	for i := 0; i <= len(parts); i++ {
		for _, t := range m[strings.Join(parts[:i], "/")] {
			if !seen[t] && !ownSet[t] {
				seen[t] = true
				inherited = append(inherited, t)
			}
		}
	}
	return O("own", own, "inherited", inherited)
}

// Counts is libraryLabelCounts over libraries, in first-seen order.
func (l *LibraryLabels) Counts(ids []string, into *orderedCounts) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range ids {
		store := l.load(id)
		keys := make([]string, 0, len(store.Entries))
		for k := range store.Entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, t := range store.Entries[k].Tags {
				into.add(t, 1)
			}
		}
	}
}

// Prune is pruneLibraryLabels: drop labels of files gone from a library
// on this computer, at most every ten minutes; a library whose labeled
// paths are all missing is taken as unmounted and left alone.
func (l *LibraryLabels) Prune(lib Library, force bool) int {
	if lib.SourceRoot == "" {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	store := l.load(lib.ID)
	if !force && time.Since(l.pruned[lib.ID]) < 10*time.Minute {
		return 0
	}
	l.pruned[lib.ID] = time.Now()
	if len(store.Entries) == 0 {
		return 0
	}
	var gone []string
	for rel := range store.Entries {
		if !exists(filepath.Join(lib.SourceRoot, rel)) {
			gone = append(gone, rel)
		}
	}
	if len(gone) == 0 || len(gone) == len(store.Entries) {
		return 0
	}
	if b, err := os.ReadFile(l.LabelsFile(lib.ID)); err == nil {
		os.WriteFile(l.LabelsFile(lib.ID)+".bak", b, 0o666)
	}
	for _, rel := range gone {
		delete(store.Entries, rel)
	}
	l.persist(lib.ID)
	return len(gone)
}

func uniq(v []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range v {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func setOf(v []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

// orderedCounts counts in first-seen order, as a JavaScript Map does.
type orderedCounts struct {
	keys []string
	n    map[string]int
}

func (c *orderedCounts) add(k string, n int) {
	if c.n == nil {
		c.n = map[string]int{}
	}
	if _, ok := c.n[k]; !ok {
		c.keys = append(c.keys, k)
	}
	c.n[k] += n
}
