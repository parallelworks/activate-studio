package kb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// TagXattr is the xattr the main library's labels are kept in.
const TagXattr = "user.studio.tags"

const tagDelim = "\x1e"

var (
	tagStrip = regexp.MustCompile(`[^a-z0-9 _-]`)
	tagSpace = regexp.MustCompile(`[` + jsWS + `]+`)
)

// NormalizeTag is tags.ts's normalizeTag.
func NormalizeTag(t string) string {
	s := strings.ToLower(JSTrim(t))
	s = tagStrip.ReplaceAllString(s, "")
	s = tagSpace.ReplaceAllString(s, "-")
	return SliceUTF16(s, 40)
}

func splitTags(v string) []string {
	out := []string{}
	for _, t := range strings.Split(v, ",") {
		if t = JSTrim(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// tagMaps are the main library's labels by path from its index, with the
// overlay over them: folders and files apart.
type tagMaps struct {
	dirs, files       map[string][]string
	dirOrd, fileOrd   []string
	at                time.Time
}

func (m *tagMaps) set(dir bool, p string, t []string) {
	if dir {
		if _, ok := m.dirs[p]; !ok {
			m.dirOrd = append(m.dirOrd, p)
		}
		m.dirs[p] = t
	} else {
		if _, ok := m.files[p]; !ok {
			m.fileOrd = append(m.fileOrd, p)
		}
		m.files[p] = t
	}
}

// Tags is the labels of the main library (on its files, or in the
// overlay) and of every other library (kept by the Studio).
type Tags struct {
	Roots   Roots
	Overlay *Overlay
	Library *LibraryLabels
	Runner  *gufi.Runner
	Indexer Indexer
	// GufiAvailable is config's GufiAvailable.
	GufiAvailable func() bool

	mu    sync.Mutex
	cache *tagMaps
}

// Invalidate drops the label maps here and, through the indexer, Node's.
func (t *Tags) Invalidate(ctx context.Context) {
	t.mu.Lock()
	t.cache = nil
	t.mu.Unlock()
	if t.Indexer != nil {
		t.Indexer.Changed(ctx, "tagMaps")
	}
}

func (t *Tags) gufiOK() bool { return t.GufiAvailable != nil && t.GufiAvailable() && t.Runner != nil }

// rows runs one gufi_query over the main index.
func (t *Tags) rows(ctx context.Context, sqlText string) ([][]string, error) {
	res, err := t.Runner.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Timeout: 60 * time.Second,
		Args: []string{"-n", "8", "-d", tagDelim, "-E", sqlText, t.Roots.GufiIndex}})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && len(res.Stdout) == 0 {
		return nil, &os.PathError{Op: "gufi_query", Path: t.Roots.GufiIndex, Err: os.ErrInvalid}
	}
	var out [][]string
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l != "" {
			out = append(out, strings.Split(l, tagDelim))
		}
	}
	return out, nil
}

func (t *Tags) relOf(p string) string {
	idx := jsResolve(t.Roots.GufiIndex, "")
	abs := jsResolve(p, "")
	if strings.HasPrefix(abs, idx+"/") {
		return abs[len(idx)+1:]
	}
	if abs == idx {
		return ""
	}
	return p
}

const (
	sqlDirTags  = `SELECT rpath(sname, sroll), CAST(x.value AS TEXT) FROM vrsummary JOIN xattrs_pwd x ON vrsummary.inode = x.inode WHERE CAST(x.name AS TEXT) = '` + TagXattr + `';`
	sqlFileTags = `SELECT rpath(sname, sroll)||'/'||v.name, CAST(x.value AS TEXT) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = '` + TagXattr + `';`
)

func (t *Tags) maps(ctx context.Context) *tagMaps {
	t.mu.Lock()
	if c := t.cache; c != nil && time.Since(c.at) < time.Minute {
		t.mu.Unlock()
		return c
	}
	t.mu.Unlock()
	m := &tagMaps{dirs: map[string][]string{}, files: map[string][]string{}}
	if t.gufiOK() {
		dirs, _ := t.rows(ctx, sqlDirTags)
		for _, r := range dirs {
			if len(r) >= 2 {
				m.set(true, t.relOf(r[0]), splitTags(r[1]))
			}
		}
		files, _ := t.rows(ctx, sqlFileTags)
		for _, r := range files {
			if len(r) >= 2 {
				m.set(false, t.relOf(r[0]), splitTags(r[1]))
			}
		}
	}
	// Overlay entries win over index rows, and give labels before a
	// reindex or without GUFI.
	if !t.Overlay.Empty() {
		for _, e := range t.Overlay.AtPath() {
			m.set(e.Dir, e.Path, e.Tags)
		}
	}
	m.at = time.Now()
	t.mu.Lock()
	t.cache = m
	t.mu.Unlock()
	return m
}

// ReadTags is readTags: the xattr labels of one file.
func ReadTags(abs string) []string {
	v, ok := getXattr(abs, TagXattr)
	if !ok {
		return []string{}
	}
	return splitTags(v)
}

// ReadBatch is readTagsBatch: each path's own labels from its xattr, the
// overlay's where it has some.
func (t *Tags) ReadBatch(abs []string) map[string][]string {
	out := map[string][]string{}
	for _, p := range abs {
		if v, ok := getXattr(p, TagXattr); ok {
			out[p] = splitTags(v)
		}
	}
	if !t.Overlay.Empty() {
		for _, p := range abs {
			if ov, ok := t.Overlay.GetAbs(p); ok {
				out[p] = ov
			}
		}
	}
	return out
}

// Effective is effectiveTags: a main-library path's own labels and those
// it inherits from the folders above it.
func (t *Tags) Effective(ctx context.Context, rel string) *Obj {
	m := t.maps(ctx)
	own, ok := m.files[rel]
	if !ok {
		own, ok = m.dirs[rel]
	}
	if !ok || own == nil {
		own = []string{}
	}
	ownSet := setOf(own)
	parts := strings.Split(rel, "/")
	parts = parts[:len(parts)-1]
	inherited := []string{}
	seen := map[string]bool{}
	for i := 0; i <= len(parts); i++ {
		for _, x := range m.dirs[strings.Join(parts[:i], "/")] {
			if !seen[x] {
				seen[x] = true
				if !ownSet[x] {
					inherited = append(inherited, x)
				}
			}
		}
	}
	return O("own", own, "inherited", inherited)
}

// LabelsFor is annotateHits's labels for main-library paths: each path's
// own labels and every ancestor folder's.
func (t *Tags) LabelsFor(ctx context.Context, paths []string) [][]string {
	m := t.maps(ctx)
	out := make([][]string, len(paths))
	for i, p := range paths {
		var tags []string
		seen := map[string]bool{}
		add := func(v []string) {
			for _, x := range v {
				if !seen[x] {
					seen[x] = true
					tags = append(tags, x)
				}
			}
		}
		add(m.files[p])
		parts := strings.Split(p, "/")
		for j := 0; j < len(parts); j++ {
			add(m.dirs[strings.Join(parts[:j], "/")])
		}
		if tags == nil {
			tags = []string{}
		}
		out[i] = tags
	}
	return out
}

// AnnotateHits is tags.ts's annotateHits for any hit type: path reads a
// hit's main-library path, with sets its labels. With filter, only the
// hits carrying one of those labels are kept.
func AnnotateHits[T any](ctx context.Context, t *Tags, hits []T, path func(T) string, with func(T, []string) T, filter []string) []T {
	paths := make([]string, len(hits))
	for i, h := range hits {
		paths[i] = path(h)
	}
	labels := t.LabelsFor(ctx, paths)
	return keepLabeled(hits, labels, with, filter)
}

// AnnotateLibraryHits is libraryLabels.ts's annotateLibraryHits: hits of
// libraries other than the main one with the labels kept for them, added
// to the hit's own (have). With filter, only those carrying one.
func AnnotateLibraryHits[T any](t *Tags, hits []T, library func(T) string, path func(T) string, have func(T) []string, with func(T, []string) T, filter []string) []T {
	maps := map[string]map[string][]string{}
	labels := make([][]string, len(hits))
	for i, h := range hits {
		id := library(h)
		m, ok := maps[id]
		if !ok {
			m = t.Library.Map(id)
			maps[id] = m
		}
		var tags []string
		seen := map[string]bool{}
		add := func(v []string) {
			for _, x := range v {
				if !seen[x] {
					seen[x] = true
					tags = append(tags, x)
				}
			}
		}
		add(have(h))
		add(m[path(h)])
		parts := strings.Split(path(h), "/")
		for j := 0; j < len(parts); j++ {
			add(m[strings.Join(parts[:j], "/")])
		}
		if tags == nil {
			tags = []string{}
		}
		labels[i] = tags
	}
	return keepLabeled(hits, labels, with, filter)
}

func keepLabeled[T any](hits []T, labels [][]string, with func(T, []string) T, filter []string) []T {
	var want []string
	for _, f := range filter {
		if n := NormalizeTag(f); n != "" {
			want = append(want, n)
		}
	}
	out := make([]T, 0, len(hits))
	for i, h := range hits {
		if len(filter) > 0 {
			has := setOf(labels[i])
			ok := false
			for _, w := range want {
				if has[w] {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, with(h, labels[i]))
	}
	return out
}

// Vocabulary is GET /api/tags's counts: the main library's labels from its
// index, then the other libraries', most used first.
func (t *Tags) Vocabulary(ctx context.Context, others []Library) []*Obj {
	m := t.maps(ctx)
	counts := &orderedCounts{}
	for _, p := range m.dirOrd {
		for _, x := range m.dirs[p] {
			counts.add(x, 1)
		}
	}
	for _, p := range m.fileOrd {
		for _, x := range m.files[p] {
			counts.add(x, 1)
		}
	}
	ids := make([]string, len(others))
	for i, l := range others {
		t.Library.Prune(l, false)
		ids[i] = l.ID
	}
	t.Library.Counts(ids, counts)
	keys := append([]string(nil), counts.keys...)
	sort.SliceStable(keys, func(i, j int) bool { return counts.n[keys[i]] > counts.n[keys[j]] })
	out := make([]*Obj, len(keys))
	for i, k := range keys {
		out[i] = O("tag", k, "count", counts.n[k])
	}
	return out
}

// Apply is applyTagsCore: add and remove labels on main-library paths, on
// each file's xattr or, where the filesystem refuses, in the overlay; then
// the index's label rows are updated in place, and a path where that
// fails is re-indexed. It answers each path's labels after.
func (t *Tags) Apply(ctx context.Context, rawPaths, addRaw, removeRaw []string) (*Obj, error) {
	paths := rawPaths[:min(len(rawPaths), 1000)]
	add, remove := normalizeAll(addRaw), normalizeAll(removeRaw)
	if len(paths) == 0 || (len(add) == 0 && len(remove) == 0) {
		return nil, Error(400, "paths and add or remove required")
	}
	updated := O()
	values := map[string][]string{}
	cleanedAll := make([]string, 0, len(paths))
	removeSet := setOf(remove)
	for _, rel := range paths {
		cleaned := trimSlashes(rel)
		if protected(cleaned) {
			return nil, Error(400, "not taggable: %s", cleaned)
		}
		abs, err := Resolve(cleaned, t.Roots.KBRoot)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, Error(404, "no such file: %s", cleaned)
		}
		current, ok := t.Overlay.Get(cleaned)
		if !ok {
			current = ReadTags(abs)
		}
		var next []string
		for _, x := range current {
			if !removeSet[x] {
				next = append(next, x)
			}
		}
		next = uniq(append(next, add...))
		var onFs bool
		if len(next) == 0 {
			removeXattr(abs, TagXattr)
			onFs = true
		} else {
			onFs = setXattr(abs, TagXattr, strings.Join(next, ","))
		}
		if onFs {
			t.Overlay.Set(cleaned, nil)
		} else {
			t.Overlay.Set(cleaned, next)
		}
		updated.Set(cleaned, next)
		values[cleaned] = next
		cleanedAll = append(cleanedAll, cleaned)
	}
	failed := t.upsertIndexTags(cleanedAll, values)
	for _, rel := range failed {
		// Best effort: the label is already kept; a reindex that fails (no
		// GUFI yet) does not fail the apply.
		abs, _ := Resolve(rel, t.Roots.KBRoot)
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			ReindexForDir(ctx, t.Indexer, rel)
		} else {
			ReindexForFile(ctx, t.Indexer, rel)
		}
	}
	t.Invalidate(ctx)
	return updated, nil
}

func normalizeAll(v []string) []string {
	out := []string{}
	for _, x := range v {
		if n := NormalizeTag(x); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// upsertIndexTags writes the label rows straight into the per-directory
// index dbs, under the inode the index has for the path (a folder copied
// or restored since it was indexed has new inodes on disk). It returns
// the paths that could not be updated in place.
func (t *Tags) upsertIndexTags(rels []string, tags map[string][]string) []string {
	var failed []string
	for _, rel := range rels {
		if !t.upsertOne(rel, strings.Join(tags[rel], ",")) {
			failed = append(failed, rel)
		}
	}
	return failed
}

func (t *Tags) upsertOne(rel, value string) bool {
	abs, err := Resolve(rel, t.Roots.KBRoot)
	if err != nil {
		return false
	}
	st, err := os.Stat(abs)
	if err != nil {
		return false
	}
	dir := filepath.Dir(rel)
	if dir == "." {
		dir = ""
	}
	db := filepath.Join(t.Roots.GufiIndex, dir, "db.db")
	if st.IsDir() {
		db = filepath.Join(t.Roots.GufiIndex, rel, "db.db")
	}
	if !exists(db) {
		return false
	}
	conn, err := sql.Open("sqlite", "file:"+db+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return false
	}
	defer conn.Close()
	var inode string
	if st.IsDir() {
		err = conn.QueryRow("SELECT CAST(inode AS TEXT) FROM summary WHERE isroot = 1 LIMIT 1").Scan(&inode)
	} else {
		err = conn.QueryRow("SELECT CAST(inode AS TEXT) FROM entries WHERE name = ? LIMIT 1", filepath.Base(rel)).Scan(&inode)
	}
	if err != nil {
		return false
	}
	tx, err := conn.Begin()
	if err != nil {
		return false
	}
	if _, err := tx.Exec("DELETE FROM xattrs_pwd WHERE CAST(inode AS TEXT) = ? AND CAST(name AS TEXT) = 'user.studio.tags'", inode); err != nil {
		tx.Rollback()
		return false
	}
	if value != "" {
		if _, err := tx.Exec("INSERT INTO xattrs_pwd (inode, name, value) VALUES (?, 'user.studio.tags', ?)", inode, value); err != nil {
			tx.Rollback()
			return false
		}
	}
	return tx.Commit() == nil
}

type indexRow struct {
	rel, inode string
	dir        bool
}

// ReapplyOverlay is reapplyTagOverlay, for the index track to run after an
// index job (indexing.ts's afterChange): overlay labels re-attached to a
// rebuilt index by inode, below prefix ("" is the whole index, which also
// prunes the overlay). It returns how many paths were written.
func (t *Tags) ReapplyOverlay(ctx context.Context, prefix string) int {
	if t.Overlay.Empty() || !t.gufiOK() {
		return 0
	}
	var rows []indexRow
	dirs, _ := t.rows(ctx, "SELECT rpath(sname, sroll), CAST(inode AS TEXT) FROM vrsummary;")
	for _, r := range dirs {
		if len(r) >= 2 {
			rows = append(rows, indexRow{t.relOf(r[0]), r[1], true})
		}
	}
	files, _ := t.rows(ctx, "SELECT rpath(sname, sroll)||'/'||name, CAST(inode AS TEXT) FROM vrpentries;")
	for _, r := range files {
		if len(r) >= 2 {
			rows = append(rows, indexRow{t.relOf(r[0]), r[1], false})
		}
	}
	updated := t.Overlay.reapply(rows, trimSlashes(prefix))
	rels := make([]string, 0, len(updated))
	for r := range updated {
		rels = append(rels, r)
	}
	sort.Strings(rels)
	failed := t.upsertIndexTags(rels, updated)
	t.Invalidate(ctx)
	return len(rels) - len(failed)
}

var recoverSplit = regexp.MustCompile(`[,` + jsWS + `]+`)

// Recover is recoverOverlayFromIndex: overlay entries rebuilt from the
// label rows the index still carries, when the overlay was lost.
func (t *Tags) Recover(ctx context.Context) int {
	if !t.gufiOK() {
		return 0
	}
	dirs, _ := t.rows(ctx, sqlDirTags)
	files, _ := t.rows(ctx, sqlFileTags)
	adopted := 0
	for _, r := range append(dirs, files...) {
		if len(r) < 2 {
			continue
		}
		var tags []string
		for _, x := range recoverSplit.Split(r[1], -1) {
			if n := NormalizeTag(x); n != "" {
				tags = append(tags, n)
			}
		}
		if len(tags) > 0 && t.Overlay.Adopt(t.relOf(r[0]), tags) {
			adopted++
		}
	}
	if adopted > 0 {
		t.Invalidate(ctx)
	}
	return adopted
}
