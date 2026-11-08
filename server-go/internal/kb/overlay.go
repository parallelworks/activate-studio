package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The label overlay (tagsOverlay.ts): labels for filesystems that refuse
// user xattrs, in INDEX_BASE/tags-overlay.json, keyed by fsid.inode with
// the file's creation time so a recycled inode is not taken for the old
// file. The file is shared with the Node server: both read it again when
// it changes and write it by renaming a temporary file over it.

// OverlayEntry is one file's labels.
type OverlayEntry struct {
	Tags  []string `json:"tags"`
	Path  string   `json:"path"` // the last known KB path: a hint
	Dir   bool     `json:"dir"`
	Btime string   `json:"btime"` // ns, "0" where the filesystem has none
	At    string   `json:"at"`
}

type overlayFile struct {
	Version int                      `json:"version"`
	Entries map[string]*OverlayEntry `json:"entries"`
}

// Overlay is the store.
type Overlay struct {
	Roots Roots

	mu    sync.Mutex
	stamp string
	data  *overlayFile
}

func (o *Overlay) file() string { return filepath.Join(o.Roots.IndexBase, "tags-overlay.json") }

// Identity of a path: fsid.inode, creation time and kind.
type Identity struct {
	Key   string
	Btime string
	Dir   bool
}

// Identify is tagsOverlay.ts's identify.
func Identify(abs string) (Identity, bool) {
	k, b, d, ok := identity(abs)
	return Identity{k, b, d}, ok
}

// InodeOf is the inode half of a key.
func InodeOf(key string) string {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// sameObject: inode equality plus creation time where both report one.
func sameObject(e *OverlayEntry, btime string) bool {
	if e.Btime == "" || e.Btime == "0" || btime == "" || btime == "0" {
		return true
	}
	return e.Btime == btime
}

// load reads the file when it changed; the caller holds mu.
func (o *Overlay) load() *overlayFile {
	f := o.file()
	stamp := fileStamp(f)
	if o.data != nil && stamp == o.stamp {
		return o.data
	}
	o.stamp = stamp
	b, err := os.ReadFile(f)
	if err != nil {
		if o.data == nil {
			o.data = &overlayFile{Version: 2, Entries: map[string]*OverlayEntry{}}
		}
		return o.data
	}
	if d := o.migrate(b); d != nil {
		o.data = d
	} else if o.data == nil {
		o.data = &overlayFile{Version: 2, Entries: map[string]*OverlayEntry{}}
	}
	return o.data
}

func (o *Overlay) migrate(b []byte) *overlayFile {
	var v2 overlayFile
	if json.Unmarshal(b, &v2) == nil && v2.Version == 2 {
		if v2.Entries == nil {
			v2.Entries = map[string]*OverlayEntry{}
		}
		return &v2
	}
	// v1 was a flat path to labels map; what still exists is keyed by inode.
	var v1 map[string]json.RawMessage
	if json.Unmarshal(b, &v1) != nil {
		return nil
	}
	out := &overlayFile{Version: 2, Entries: map[string]*OverlayEntry{}}
	for rel, raw := range v1 {
		var tags []string
		if json.Unmarshal(raw, &tags) != nil {
			continue
		}
		id, ok := Identify(filepath.Join(o.Roots.KBRoot, rel))
		if !ok {
			continue
		}
		out.Entries[id.Key] = &OverlayEntry{Tags: tags, Path: rel, Dir: id.Dir, Btime: id.Btime, At: isoNow()}
	}
	return out
}

// persist writes the store; the caller holds mu.
func (o *Overlay) persist() error {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(o.data); err != nil {
		return err
	}
	f := o.file()
	tmp := fmt.Sprintf("%s.%d.tmp", f, os.Getpid())
	if err := os.WriteFile(tmp, bytes.TrimRight(b.Bytes(), "\n"), 0o666); err != nil {
		return err
	}
	if err := os.Rename(tmp, f); err != nil {
		os.Remove(tmp)
		return err
	}
	o.stamp = fileStamp(f)
	return nil
}

// Get is overlayGet: a KB path's labels, ok false when the overlay has
// none. An entry whose inode now belongs to another file is retired.
func (o *Overlay) Get(rel string) ([]string, bool) {
	id, ok := Identify(filepath.Join(o.Roots.KBRoot, rel))
	if !ok {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.getLocked(id)
}

// GetAbs is Get for an absolute path (readTagsBatch takes it relative to
// the main folder and back, which is the path itself).
func (o *Overlay) GetAbs(abs string) ([]string, bool) {
	id, ok := Identify(abs)
	if !ok {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.getLocked(id)
}

func (o *Overlay) getLocked(id Identity) ([]string, bool) {
	all := o.load()
	e := all.Entries[id.Key]
	if e == nil {
		return nil, false
	}
	if !sameObject(e, id.Btime) {
		delete(all.Entries, id.Key)
		o.persist()
		return nil, false
	}
	return e.Tags, true
}

// Set is overlaySet; no labels removes the entry.
func (o *Overlay) Set(rel string, tags []string) error {
	id, ok := Identify(filepath.Join(o.Roots.KBRoot, rel))
	if !ok {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	all := o.load()
	if len(tags) > 0 {
		all.Entries[id.Key] = &OverlayEntry{Tags: tags, Path: rel, Dir: id.Dir, Btime: id.Btime, At: isoNow()}
	} else if all.Entries[id.Key] != nil {
		delete(all.Entries, id.Key)
	} else {
		return nil
	}
	return o.persist()
}

// Empty is overlayEmpty.
func (o *Overlay) Empty() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.load().Entries) == 0
}

// AtPath is overlayAtPath: the entries whose file is at their recorded
// path (same inode, same creation time).
func (o *Overlay) AtPath() []OverlayEntry {
	o.mu.Lock()
	type kv struct {
		key string
		e   OverlayEntry
	}
	var list []kv
	for k, e := range o.load().Entries {
		list = append(list, kv{k, *e})
	}
	o.mu.Unlock()
	var out []OverlayEntry
	for _, x := range list {
		id, ok := Identify(filepath.Join(o.Roots.KBRoot, x.e.Path))
		if ok && InodeOf(id.Key) == InodeOf(x.key) && sameObject(&x.e, id.Btime) {
			out = append(out, x.e)
		}
	}
	return out
}

// Adopt is overlayAdopt: labels for a path when the overlay has none for
// that file.
func (o *Overlay) Adopt(rel string, tags []string) bool {
	id, ok := Identify(filepath.Join(o.Roots.KBRoot, rel))
	if !ok || len(tags) == 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	all := o.load()
	if e := all.Entries[id.Key]; e != nil && len(e.Tags) > 0 {
		return false
	}
	all.Entries[id.Key] = &OverlayEntry{Tags: tags, Path: rel, Dir: id.Dir, Btime: id.Btime, At: isoNow()}
	o.persist()
	return true
}

// reapply is the overlay half of tags.ts's reapplyTagOverlay: given where
// the index has each inode now, it notes moved paths, re-keys entries seen
// under a new device id, returns the labels to write into the index by
// path, and with a full pass prunes what is gone.
func (o *Overlay) reapply(rows []indexRow, scope string) map[string][]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	all := o.load()
	byInode := map[string][]string{}
	for k := range all.Entries {
		ino := InodeOf(k)
		byInode[ino] = append(byInode[ino], k)
	}
	updated := map[string][]string{}
	seen := map[string]bool{}
	moved := false
	for _, row := range rows {
		keys := byInode[row.inode]
		if keys == nil {
			continue
		}
		id, ok := Identify(filepath.Join(o.Roots.KBRoot, row.rel))
		if !ok {
			continue
		}
		key := ""
		for _, k := range keys {
			if k == id.Key {
				key = k
				break
			}
		}
		if key == "" {
			for _, k := range keys {
				if e := all.Entries[k]; e != nil && InodeOf(k) == InodeOf(id.Key) && sameObject(e, id.Btime) {
					all.Entries[id.Key] = e
					if k != id.Key {
						delete(all.Entries, k)
					}
					key, moved = id.Key, true
					break
				}
			}
		}
		e := all.Entries[key]
		if key == "" || e == nil || !sameObject(e, id.Btime) {
			continue
		}
		seen[key] = true
		if e.Path != row.rel || e.Dir != row.dir {
			e.Path, e.Dir, moved = row.rel, row.dir, true
		}
		if scope != "" && row.rel != scope && !strings.HasPrefix(row.rel, scope+"/") {
			continue
		}
		updated[row.rel] = e.Tags
	}
	if moved {
		o.persist()
	}
	if scope == "" {
		o.pruneLocked(seen)
	}
	return updated
}

// pruneLocked is overlayPrune: drop entries whose file is gone from the
// index and the filesystem; re-key what is the same file under a new
// device id; refuse a pass that recognizes nothing; back up first.
func (o *Overlay) pruneLocked(seen map[string]bool) int {
	all := o.load()
	if len(all.Entries) == 0 {
		return 0
	}
	total := len(all.Entries)
	var drop []string
	rekeyed := 0
	keys := make([]string, 0, total)
	for k := range all.Entries {
		keys = append(keys, k)
	}
	for _, key := range keys {
		e := all.Entries[key]
		if e == nil || seen[key] {
			continue
		}
		id, ok := Identify(filepath.Join(o.Roots.KBRoot, e.Path))
		if ok && InodeOf(id.Key) == InodeOf(key) && sameObject(e, id.Btime) {
			if id.Key != key {
				all.Entries[id.Key] = e
				delete(all.Entries, key)
				rekeyed++
			}
			continue
		}
		drop = append(drop, key)
	}
	if len(drop) == total && len(seen) == 0 {
		if rekeyed > 0 {
			o.persist()
		}
		return 0
	}
	if len(drop) > 0 {
		if b, err := os.ReadFile(o.file()); err == nil {
			os.WriteFile(o.file()+".bak", b, 0o666)
		}
		for _, k := range drop {
			delete(all.Entries, k)
		}
	}
	if len(drop) > 0 || rekeyed > 0 {
		o.persist()
	}
	return len(drop)
}
