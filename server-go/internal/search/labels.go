package search

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
)

// TagMaps are labels by path: a folder's (inherited below it) and a file's.
type TagMaps struct {
	Dir  map[string][]string
	File map[string][]string
}

// Labels are the labels beside search hits (tags.ts tagMaps for the main
// library, libraryLabels.ts libraryLabelMaps for the others).
//
// TODO(files track, internal/kb): wire the kb package's label stores here
// once port-files is merged; FileLabels is the stand-in.
type Labels interface {
	Primary(ctx context.Context) TagMaps
	Library(id string) TagMaps
	ForIndexRoot(root string) *TagMaps
}

const tagXattr = "user.studio.tags"

var tagStrip = regexp.MustCompile(`[^a-z0-9 _-]`)
var spaces = regexp.MustCompile(`\s+`)

// NormalizeTag is normalizeTag.
func NormalizeTag(t string) string {
	s := spaces.ReplaceAllString(tagStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(t)), ""), "-")
	if u := utf16Units(s); len(u) > 40 {
		s = utf16String(u[:40])
	}
	return s
}

// FileLabels reads the files Node writes: the main library's labels from
// its index's xattr rows and INDEX_BASE/tags-overlay.json, cached a minute
// as Node does, and each other library's INDEX_BASE/libraries/<id>/
// labels.json, read again when it changes.
type FileLabels struct {
	IndexBase string
	KBRoot    string
	GufiIndex string
	Runner    *gufi.Runner

	mu      sync.Mutex
	primary *TagMaps
	at      time.Time
	libs    map[string]libStore
}

type libStore struct {
	stamp     string
	indexRoot string
	maps      TagMaps
}

// Invalidate is invalidateTagMaps.
func (f *FileLabels) Invalidate() {
	f.mu.Lock()
	f.primary = nil
	f.mu.Unlock()
}

func splitTags(v string) []string {
	out := []string{}
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func (f *FileLabels) rows(ctx context.Context, sql string) [][]string {
	if f.Runner == nil {
		return nil
	}
	args := []string{"-n", "8", "-d", "\x1e", "-E", sql, f.GufiIndex}
	t0 := time.Now()
	res, err := f.Runner.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: args, Timeout: 60 * time.Second})
	o := gufitrace.Run{Start: t0, Elapsed: res.Elapsed, Hide: true, Source: "labels"}
	if res.Signal != "" {
		o.Signal = res.Signal
	} else if err != nil && res.Elapsed == 0 {
		o.Error = err.Error()
	} else {
		c := res.ExitCode
		o.Code = &c
	}
	se := res.Stderr
	o.Stderr, o.Cut = &se, res.StderrCut
	gufitrace.Note(ctx, append([]string{"gufi_query"}, args...), o)
	if err != nil && len(res.Stdout) == 0 {
		return nil
	}
	var out [][]string
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l != "" {
			out = append(out, strings.Split(l, "\x1e"))
		}
	}
	return out
}

func (f *FileLabels) relOf(p string) string {
	idx, _ := filepath.Abs(f.GufiIndex)
	abs, _ := filepath.Abs(p)
	if strings.HasPrefix(abs, idx+string(filepath.Separator)) {
		return abs[len(idx)+1:]
	}
	if abs == idx {
		return ""
	}
	return p
}

// Primary is tagMaps.
func (f *FileLabels) Primary(ctx context.Context) TagMaps {
	f.mu.Lock()
	if f.primary != nil && time.Since(f.at) < 60*time.Second {
		m := *f.primary
		f.mu.Unlock()
		return m
	}
	f.mu.Unlock()
	m := TagMaps{Dir: map[string][]string{}, File: map[string][]string{}}
	if _, err := os.Stat(f.GufiIndex); err == nil {
		for _, r := range f.rows(ctx, "SELECT rpath(sname, sroll), CAST(x.value AS TEXT) FROM vrsummary JOIN xattrs_pwd x ON vrsummary.inode = x.inode WHERE CAST(x.name AS TEXT) = '"+tagXattr+"';") {
			if len(r) >= 2 {
				m.Dir[f.relOf(r[0])] = splitTags(r[1])
			}
		}
		for _, r := range f.rows(ctx, "SELECT rpath(sname, sroll)||'/'||v.name, CAST(x.value AS TEXT) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = '"+tagXattr+"';") {
			if len(r) >= 2 {
				m.File[f.relOf(r[0])] = splitTags(r[1])
			}
		}
	}
	for _, e := range f.overlayAtPath() {
		if e.Dir {
			m.Dir[e.Path] = e.Tags
		} else {
			m.File[e.Path] = e.Tags
		}
	}
	f.mu.Lock()
	f.primary, f.at = &m, time.Now()
	f.mu.Unlock()
	return m
}

type overlayEntry struct {
	Tags  []string `json:"tags"`
	Path  string   `json:"path"`
	Dir   bool     `json:"dir"`
	Btime string   `json:"btime"`
}

// overlayAtPath is the overlay's entries still at their recorded path
// (same inode). The creation-time check of sameObject is not made: Go's
// os.Stat gives no birth time on Linux, so a recycled inode is taken as
// the same file here.
func (f *FileLabels) overlayAtPath() []overlayEntry {
	b, err := os.ReadFile(filepath.Join(f.IndexBase, "tags-overlay.json"))
	if err != nil {
		return nil
	}
	var file struct {
		Version int                     `json:"version"`
		Entries map[string]overlayEntry `json:"entries"`
	}
	if json.Unmarshal(b, &file) != nil || file.Version != 2 {
		return nil
	}
	var out []overlayEntry
	for key, e := range file.Entries {
		st, err := os.Stat(filepath.Join(f.KBRoot, e.Path))
		if err != nil {
			continue
		}
		inode, ok := inodeOf(st)
		ino := key
		if i := strings.IndexByte(key, '.'); i >= 0 {
			ino = key[i+1:]
		}
		if ok && strconv.FormatUint(inode, 10) == ino {
			out = append(out, e)
		}
	}
	return out
}

func (f *FileLabels) load(id string) libStore {
	file := filepath.Join(f.IndexBase, "libraries", id, "labels.json")
	stamp := ""
	if st, err := os.Stat(file); err == nil {
		stamp = strconv.FormatInt(st.Size(), 10) + ":" + strconv.FormatInt(st.ModTime().UnixNano(), 10)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.libs == nil {
		f.libs = map[string]libStore{}
	}
	if s, ok := f.libs[id]; ok && s.stamp == stamp {
		return s
	}
	s := libStore{stamp: stamp, maps: TagMaps{Dir: map[string][]string{}, File: map[string][]string{}}}
	if b, err := os.ReadFile(file); err == nil {
		var raw struct {
			Version   int    `json:"version"`
			IndexRoot string `json:"indexRoot"`
			Entries   map[string]struct {
				Tags []string `json:"tags"`
			} `json:"entries"`
		}
		if json.Unmarshal(b, &raw) == nil && raw.Version == 1 && raw.Entries != nil {
			s.indexRoot = raw.IndexRoot
			for rel, e := range raw.Entries {
				s.maps.Dir[rel] = e.Tags
			}
			s.maps.File = s.maps.Dir
		}
	}
	f.libs[id] = s
	return s
}

// Library is libraryLabelMaps.
func (f *FileLabels) Library(id string) TagMaps { return f.load(id).maps }

// ForIndexRoot is libraryLabelsForIndexRoot.
func (f *FileLabels) ForIndexRoot(root string) *TagMaps {
	ents, err := os.ReadDir(filepath.Join(f.IndexBase, "libraries"))
	if err != nil {
		return nil
	}
	for _, e := range ents {
		if _, err := os.Stat(filepath.Join(f.IndexBase, "libraries", e.Name(), "labels.json")); err != nil {
			continue
		}
		s := f.load(e.Name())
		if s.indexRoot != "" && s.indexRoot == root {
			m := s.maps
			return &m
		}
	}
	return nil
}

// tagsOf is a hit's labels: its own and every folder's above it.
func tagsOf(m TagMaps, p string, own []string, other bool) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(t string) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, t := range own {
		add(t)
	}
	if fl, ok := m.File[p]; ok {
		for _, t := range fl {
			add(t)
		}
	} else if other {
		for _, t := range m.Dir[p] {
			add(t)
		}
	}
	parts := strings.Split(p, "/")
	for i := 0; i < len(parts); i++ {
		for _, t := range m.Dir[strings.Join(parts[:i], "/")] {
			add(t)
		}
	}
	return out
}

func wanted(tags []string, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		w := NormalizeTag(f)
		if w == "" {
			continue
		}
		for _, t := range tags {
			if t == w {
				return true
			}
		}
	}
	return false
}
