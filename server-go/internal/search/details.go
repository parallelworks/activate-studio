package search

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

const tagXattr = "user.studio.tags"

// DetailsLibrary is Details.library.
type DetailsLibrary struct {
	ID       string  `json:"id"`
	Label    string  `json:"label"`
	Kind     string  `json:"kind"`
	Site     *string `json:"site"`
	Writable bool    `json:"writable"`
}

// TypeCount is one extension's count.
type TypeCount struct {
	Ext   string  `json:"ext"`
	Count float64 `json:"count"`
	Bytes float64 `json:"bytes"`
}

// LabelCount is a label and how many carry it.
type LabelCount struct {
	Label string  `json:"label"`
	Count float64 `json:"count"`
}

// DetailsLabels is Details.labels.
type DetailsLabels struct {
	Own       []string     `json:"own"`
	Inherited []string     `json:"inherited"`
	Within    []LabelCount `json:"within"`
}

// Details is libraryDetails.ts's Details, in its key order.
type Details struct {
	Path     string         `json:"path"`
	Name     string         `json:"name"`
	Library  DetailsLibrary `json:"library"`
	Index    *string        `json:"index"`
	FullText bool           `json:"fullText"`
	Type     string         `json:"type"`
	Size     float64        `json:"size"`
	Files    float64        `json:"files"`
	Dirs     float64        `json:"dirs"`
	Mtime    float64        `json:"mtime"`
	Types    []TypeCount    `json:"types"`
	Labels   DetailsLabels  `json:"labels"`
	Counted  bool           `json:"counted"`
	MS       int64          `json:"ms"`
}

// TreeStats is gufi.ts's TreeStats.
type TreeStats struct {
	Files, Dirs, Bytes, Mtime float64
	ByExt                     []TypeCount
	Labels                    []LabelCount
}

type countEntry struct {
	stamp string
	at    time.Time
	done  chan struct{}
	v     TreeStats
	err   error
}

var counts = struct {
	sync.Mutex
	m     map[string]*countEntry
	order []string
}{m: map[string]*countEntry{}}

// InvalidateCounts is the count cache's part of invalidateDbList.
func InvalidateCounts() {
	counts.Lock()
	counts.m, counts.order = map[string]*countEntry{}, nil
	counts.Unlock()
}

func (e *Engine) countStamp(root string) (string, bool) {
	if !strings.HasPrefix(root, libraries.RemotePrefix) {
		return "", false
	}
	if t, err := e.Target(root); err == nil && t != nil {
		return t.BuiltAt, true
	}
	return "", true
}

// kept is keptCount: a fresh entry, made or being made, or nil.
func (e *Engine) kept(id, root string) (*countEntry, string) {
	stamp, isRemote := e.countStamp(root)
	ttl := 10 * time.Minute
	if isRemote && stamp != "" {
		ttl = time.Hour
	}
	counts.Lock()
	defer counts.Unlock()
	if h := counts.m[id]; h != nil && h.stamp == stamp && time.Since(h.at) < ttl {
		return h, stamp
	}
	return nil, stamp
}

func (e *Engine) treeKey(root, sub string) string {
	return root + "\x00tree\x00" + sub + "\x00" + tagXattr
}

// TreeStatsKept is treeStatsKept: whether a count is made or being made.
func (e *Engine) TreeStatsKept(root, sub string) bool {
	h, _ := e.kept(e.treeKey(root, sub), root)
	return h != nil
}

// TreeStats is treeStats, cached as Node's countCache.
func (e *Engine) TreeStats(ctx context.Context, root, sub string) (TreeStats, error) {
	id := e.treeKey(root, sub)
	h, stamp := e.kept(id, root)
	if h == nil {
		h = &countEntry{stamp: stamp, at: time.Now(), done: make(chan struct{})}
		counts.Lock()
		if _, had := counts.m[id]; !had {
			counts.order = append(counts.order, id)
		}
		counts.m[id] = h
		if len(counts.order) > 500 {
			delete(counts.m, counts.order[0])
			counts.order = counts.order[1:]
		}
		counts.Unlock()
		go func() {
			h.v, h.err = e.treeStatsNow(gufitrace.Source(context.WithoutCancel(ctx), "stats"), root, sub)
			if h.err != nil {
				counts.Lock()
				if counts.m[id] == h {
					delete(counts.m, id)
				}
				counts.Unlock()
			}
			close(h.done)
		}()
	}
	select {
	case <-h.done:
		return h.v, h.err
	case <-ctx.Done():
		return TreeStats{}, ctx.Err()
	}
}

func (e *Engine) treeStatsNow(ctx context.Context, root, sub string) (TreeStats, error) {
	ext := `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END)`
	type res struct {
		rows [][]string
		err  error
	}
	tagCh := make(chan res, 1)
	go func() {
		r, err := e.QueryAggregate(ctx, []string{
			"-I", "CREATE TABLE t (k TEXT, n INT64);",
			"-K", "CREATE TABLE agg (k TEXT, n INT64);",
			"-E", "INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrpentries v JOIN xattrs_pwd x ON v.inode = x.inode WHERE CAST(x.name AS TEXT) = " + q(tagXattr) + " GROUP BY 1; " +
				"INSERT INTO t SELECT CAST(x.value AS TEXT), count(*) FROM vrsummary s JOIN xattrs_pwd x ON s.inode = x.inode WHERE CAST(x.name AS TEXT) = " + q(tagXattr) + " GROUP BY 1;",
			"-J", "INSERT INTO agg SELECT * FROM t;",
			"-G", "SELECT k, sum(n) FROM agg GROUP BY k;",
		}, root, sub)
		tagCh <- res{r, err}
	}()
	rows, err := e.QueryAggregate(ctx, []string{
		"-I", "CREATE TABLE t (k TEXT, n INT64, b INT64);",
		"-K", "CREATE TABLE agg (k TEXT, n INT64, b INT64);",
		"-E", "INSERT INTO t SELECT " + ext + ", count(*), sum(size) FROM vrpentries GROUP BY 1; " +
			"INSERT INTO t SELECT char(1)||'summary', sum(totfiles), sum(totsize) FROM vrsummary; " +
			"INSERT INTO t SELECT char(1)||'dirs', count(*), 0 FROM vrsummary; " +
			"INSERT INTO t SELECT char(1)||'mtime', max(max(maxmtime), max(mtime)), 0 FROM vrsummary;",
		"-J", "INSERT INTO agg SELECT * FROM t;",
		"-G", "SELECT k, CASE WHEN k = char(1)||'mtime' THEN max(n) ELSE sum(n) END, sum(b) FROM agg GROUP BY k;",
	}, root, sub)
	tr := <-tagCh
	if err != nil {
		return TreeStats{}, err
	}
	st := TreeStats{ByExt: []TypeCount{}, Labels: []LabelCount{}}
	at := func(r []string, i int) string {
		if i < len(r) {
			return r[i]
		}
		return ""
	}
	for _, r := range rows {
		k, n, b := at(r, 0), numOrZero(at(r, 1)), numOrZero(at(r, 2))
		switch k {
		case "\x01summary":
			st.Files, st.Bytes = n, b
		case "\x01dirs":
			st.Dirs = n
		case "\x01mtime":
			st.Mtime = n
		default:
			if k == "" {
				k = "(none)"
			}
			st.ByExt = append(st.ByExt, TypeCount{k, n, b})
		}
	}
	sort.SliceStable(st.ByExt, func(i, j int) bool {
		if st.ByExt[i].Bytes != st.ByExt[j].Bytes {
			return st.ByExt[i].Bytes > st.ByExt[j].Bytes
		}
		return st.ByExt[i].Count > st.ByExt[j].Count
	})
	if len(st.ByExt) > 8 {
		st.ByExt = st.ByExt[:8]
	}
	lc := &labelCounts{m: map[string]float64{}}
	if tr.err == nil {
		for _, r := range tr.rows {
			for _, t := range strings.Split(at(r, 0), ",") {
				if t = strings.TrimSpace(t); t != "" {
					lc.add(t, numOrZero(at(r, 1)))
				}
			}
		}
	}
	st.Labels = lc.top(12)
	return st, nil
}

type labelCounts struct {
	order []string
	m     map[string]float64
}

func (c *labelCounts) add(t string, n float64) {
	if _, ok := c.m[t]; !ok {
		c.order = append(c.order, t)
	}
	c.m[t] += n
}

func (c *labelCounts) top(n int) []LabelCount {
	out := []LabelCount{}
	for _, t := range c.order {
		out = append(out, LabelCount{t, c.m[t]})
	}
	// Labels used equally often by name, as in the label list: Node keeps
	// the order the index rows came in, which changes between visits.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

type entry struct {
	size, mtime float64
	labels      []string
}

// entryStats is entryStats.
func (e *Engine) entryStats(ctx context.Context, root, dir, name string) (*entry, error) {
	sql := "SELECT v.size, v.mtime, (SELECT CAST(x.value AS TEXT) FROM xattrs_pwd x WHERE x.inode = v.inode AND CAST(x.name AS TEXT) = " + q(tagXattr) + ") FROM vrpentries v WHERE v.name = " + q(name) + " LIMIT 1;"
	args := []string{"-n", "1", "-d", Delim, "--min-level", "0", "--max-level", "0", "-E", sql}
	ctx = gufitrace.Source(ctx, "stats")
	var out string
	var err error
	if strings.HasPrefix(root, libraries.RemotePrefix) {
		out, err = e.RemoteQuery(ctx, root, args, dir)
	} else {
		target := root
		if dir != "" {
			target = filepath.Join(root, dir)
		}
		out, err = e.runLocal(ctx, append(args, target), 60*time.Second)
	}
	if err != nil {
		return nil, err
	}
	rows := ParseRows(out, 2)
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	en := &entry{size: numOrZero(r[0]), mtime: numOrZero(r[1]), labels: []string{}}
	if len(r) > 2 {
		for _, t := range strings.Split(r[2], ",") {
			if t = strings.TrimSpace(t); t != "" {
				en.labels = append(en.labels, t)
			}
		}
	}
	return en, nil
}

// cleanRelPath is libraryDetails.ts cleanRel.
func cleanRelPath(rel string) (string, error) {
	var parts []string
	for _, p := range strings.Split(rel, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." || strings.Contains(p, "\x00") {
			return "", apierr.New(400, "invalid path")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/"), nil
}

func (e *Engine) userDef(viewer libraries.Viewer, id string) *libraries.UserDef {
	if viewer == nil {
		return nil
	}
	u := e.Reg.Users()
	owner := u.OwnerOf(*viewer)
	if owner == "" {
		return nil
	}
	for _, d := range u.Load(owner) {
		if d.ID == id {
			d := d
			return &d
		}
	}
	return nil
}

func (e *Engine) kindOf(l libraries.Library, viewer libraries.Viewer) string {
	switch {
	case l.Primary:
		return "Main folder"
	case l.Build != nil:
		return "Built from a folder on this computer"
	case l.Site == "":
		return "GUFI index on this computer"
	}
	d := e.userDef(viewer, l.ID)
	if d != nil && d.Build != nil {
		over := "pw ssh"
		if d.IsSSH() {
			over = "ssh"
		}
		return "Built on " + l.Site + " over " + over
	}
	if d != nil && d.IsSSH() {
		return "GUFI index on " + l.Site + " over ssh"
	}
	if d != nil && d.Existing != nil && *d.Existing {
		return "GUFI index on " + l.Site + " over pw ssh"
	}
	return "Built on " + l.Site + " by the index workflow"
}

func (e *Engine) indexOf(l libraries.Library, authEnabled bool) *string {
	if l.Site != "" {
		t, err := e.Target(l.IndexRoot)
		if err != nil || t == nil {
			return nil
		}
		host := remote.SiteName(t.Site)
		if h, _ := remote.ParseSSHSite(t.Site); h != nil {
			host = h.Destination
		}
		s := host + ":" + t.IndexRoot
		return &s
	}
	if authEnabled {
		return nil
	}
	s := l.IndexRoot
	return &s
}

type detailsEntry struct {
	at time.Time
	v  Details
}

var detailsCache = struct {
	sync.Mutex
	m     map[string]detailsEntry
	order []string
}{m: map[string]detailsEntry{}}

func uniqStr(a []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func entriesMap(es []gufi.LabelEntry) map[string][]string {
	m := map[string][]string{}
	for _, e := range es {
		m[e.Rel] = e.Tags
	}
	return m
}

// Describe is describe: a folder or file of a library, from its index.
func (e *Engine) Describe(ctx context.Context, l libraries.Library, sub string, viewer libraries.Viewer, count, authEnabled bool) (Details, error) {
	vk := ""
	if viewer != nil {
		vk = *viewer
	}
	key := vk + "|" + l.ID + "|" + sub
	detailsCache.Lock()
	hit, ok := detailsCache.m[key]
	detailsCache.Unlock()
	if ok && time.Since(hit.at) < 20*time.Second && (!count || hit.v.Counted) {
		return e.withKeptLabels(l, sub, hit.v), nil
	}
	t0 := time.Now()
	name := l.Label
	if sub != "" {
		name = path.Base(sub)
	}
	v := Details{Path: sub, Name: name, Library: DetailsLibrary{ID: l.ID, Label: l.Label, Kind: e.kindOf(l, viewer), Site: strPtr(l.Site), Writable: l.Writable},
		Index: e.indexOf(l, authEnabled), FullText: !e.Reg.Lacks(l, "fullText"), Types: []TypeCount{}}
	dir := ""
	if i := strings.LastIndex(sub, "/"); i >= 0 {
		dir = sub[:i]
	}
	var en *entry
	entryRead := false
	var isDir bool
	if l.Site != "" {
		if sub != "" {
			en, _ = e.entryStats(ctx, l.IndexRoot, dir, name)
		}
		entryRead = true
		isDir = en == nil
	} else {
		hasDB := exists(filepath.Join(l.IndexRoot, sub, "db.db"))
		isDir = hasDB
		if !isDir && l.SourceRoot != "" {
			if st, err := os.Stat(filepath.Join(l.SourceRoot, sub)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir && !hasDB {
			return Details{}, apierr.New(404, "%s is not in the index yet", name)
		}
	}
	unknown := isDir && l.Site != "" && sub == "" && !count && !e.TreeStatsKept(l.IndexRoot, sub)
	switch {
	case unknown:
		d := e.userDef(viewer, l.ID)
		files, dirs := 0.0, 0.0
		if d != nil {
			if d.Files != nil {
				files = float64(*d.Files)
			}
			n := int64(1)
			if d.Directories != nil {
				n = *d.Directories
			}
			dirs = float64(max(0, n-1))
		}
		v.Type, v.Files, v.Dirs = "dir", files, dirs
		v.Labels = DetailsLabels{[]string{}, []string{}, []LabelCount{}}
		v.Counted = false
	case isDir:
		st, err := e.TreeStats(ctx, l.IndexRoot, sub)
		if err != nil {
			return Details{}, err
		}
		lab := DetailsLabels{[]string{}, []string{}, st.Labels}
		if l.Primary && e.Tags != nil {
			own, inh := e.primaryEffective(ctx, sub)
			lab.Own, lab.Inherited = own, inh
			lab.Within = e.labelsBelow(ctx, sub)
		}
		types := st.ByExt
		if len(types) > 5 {
			types = types[:5]
		}
		v.Type, v.Size, v.Files, v.Dirs, v.Mtime, v.Types, v.Labels, v.Counted = "dir", st.Bytes, st.Files, max(0, st.Dirs-1), st.Mtime, types, lab, true
	default:
		if !entryRead {
			en, _ = e.entryStats(ctx, l.IndexRoot, dir, name)
		}
		var size, mtime float64
		if en != nil {
			size, mtime = en.size, en.mtime
		} else if l.SourceRoot != "" {
			st, err := os.Stat(filepath.Join(l.SourceRoot, sub))
			if err != nil {
				return Details{}, apierr.New(404, "no such file: %s", sub)
			}
			size, mtime = float64(st.Size()), float64(st.ModTime().UnixMilli()/1000)
		} else {
			return Details{}, apierr.New(404, "%s is not in the index", name)
		}
		lab := DetailsLabels{Own: []string{}, Inherited: []string{}, Within: []LabelCount{}}
		if l.Primary && e.Tags != nil {
			lab.Own, lab.Inherited = e.primaryEffective(ctx, sub)
		} else if en != nil {
			lab.Own = en.labels
		}
		ext := ""
		if i := strings.LastIndex(name, "."); i >= 0 {
			ext = strings.ToLower(name[i+1:])
		}
		if ext == "" {
			ext = "(none)"
		}
		v.Type, v.Size, v.Files, v.Dirs, v.Mtime, v.Types, v.Labels, v.Counted = "file", size, 1, 0, mtime, []TypeCount{{ext, 1, size}}, lab, true
	}
	v.MS = time.Since(t0).Milliseconds()
	detailsCache.Lock()
	if _, had := detailsCache.m[key]; !had {
		detailsCache.order = append(detailsCache.order, key)
	}
	detailsCache.m[key] = detailsEntry{time.Now(), v}
	for len(detailsCache.order) > 300 && len(detailsCache.order) > 200 {
		delete(detailsCache.m, detailsCache.order[0])
		detailsCache.order = detailsCache.order[1:]
		if len(detailsCache.order) <= 200 {
			break
		}
	}
	detailsCache.Unlock()
	return e.withKeptLabels(l, sub, v), nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (e *Engine) primaryEffective(ctx context.Context, rel string) (own, inherited []string) {
	o := e.Tags.Effective(ctx, rel)
	b, _ := marshalJS(o)
	var x struct {
		Own       []string `json:"own"`
		Inherited []string `json:"inherited"`
	}
	jsonUnmarshal(b, &x)
	if x.Own == nil {
		x.Own = []string{}
	}
	if x.Inherited == nil {
		x.Inherited = []string{}
	}
	return x.Own, x.Inherited
}

func (e *Engine) labelsBelow(ctx context.Context, sub string) []LabelCount {
	c := &labelCounts{m: map[string]float64{}}
	for _, es := range [][]gufi.LabelEntry{e.Tags.DirEntries(ctx), e.Tags.FileEntries(ctx)} {
		for _, en := range es {
			if sub != "" && en.Rel != sub && !strings.HasPrefix(en.Rel, sub+"/") {
				continue
			}
			for _, t := range en.Tags {
				c.add(t, 1)
			}
		}
	}
	return c.top(12)
}

// withKeptLabels is withKeptLabels: a library's own kept labels added.
func (e *Engine) withKeptLabels(l libraries.Library, sub string, v Details) Details {
	if l.Primary || e.Tags == nil {
		return v
	}
	ents := e.Tags.Library.Entries(l.ID)
	m := entriesMap(ents)
	key := strings.Trim(sub, "/")
	own := uniqStr(append(append([]string{}, v.Labels.Own...), m[key]...))
	ownSet := map[string]bool{}
	for _, t := range own {
		ownSet[t] = true
	}
	parts := strings.Split(key, "/")
	parts = parts[:len(parts)-1]
	var inh []string
	for i := 0; i <= len(parts); i++ {
		inh = append(inh, m[strings.Join(parts[:i], "/")]...)
	}
	all := uniqStr(append(append([]string{}, v.Labels.Inherited...), uniqStr(inh)...))
	inherited := []string{}
	for _, t := range all {
		if !ownSet[t] {
			inherited = append(inherited, t)
		}
	}
	within := v.Labels.Within
	if v.Type == "dir" {
		c := &labelCounts{m: map[string]float64{}}
		for _, w := range within {
			c.add(w.Label, w.Count)
		}
		below := &labelCounts{m: map[string]float64{}}
		for _, en := range ents {
			if key != "" && en.Rel != key && !strings.HasPrefix(en.Rel, key+"/") {
				continue
			}
			for _, t := range en.Tags {
				below.add(t, 1)
			}
		}
		for _, t := range below.order {
			c.add(t, below.m[t])
		}
		within = c.top(12)
	}
	v.Labels = DetailsLabels{Own: own, Inherited: inherited, Within: within}
	return v
}

// CleanDetailsPath is the route's cleanRel.
func CleanDetailsPath(p string) (string, error) { return cleanRelPath(p) }
