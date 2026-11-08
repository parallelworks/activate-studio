package index

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// history.ts: corpus history, a timeline of what the main folder held,
// captured from the index. Each snapshot is the census the index carries,
// one row per file with its size, mtime and inode, gzipped JSON under
// INDEX_BASE/history.

// Entry is one file of a snapshot.
type Entry struct {
	Path  string  `json:"path"`
	Size  float64 `json:"size"`
	Mtime float64 `json:"mtime"`
	Inode float64 `json:"inode"`
}

// SnapshotMeta is a snapshot without its entries.
type SnapshotMeta struct {
	ID      string  `json:"id"`
	TakenAt int64   `json:"takenAt"`
	Files   int     `json:"files"`
	Bytes   float64 `json:"bytes"`
	Disk    int64   `json:"disk"`
}

type snapshot struct {
	SnapshotMeta
	Entries []Entry `json:"entries"`
}

// History is the main folder's history.
type History struct {
	ix   *Indexer
	dir  string
	keep int
	mu   sync.Mutex
	// cached is the census of one snapshot, which a view walks a level at
	// a time.
	cachedID string
	cached   *snapshot
}

func newHistory(ix *Indexer) *History {
	keep := 30
	if v := os.Getenv("HISTORY_KEEP"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			keep = int(n)
		}
	}
	return &History{ix: ix, dir: filepath.Join(ix.P.IndexBase, "history"), keep: keep}
}

func (h *History) fileFor(id string) string { return filepath.Join(h.dir, id+".json.gz") }

func (h *History) available() bool {
	return h.ix.App == nil || h.ix.App.Cfg.GufiAvailable()
}

func (h *History) read(id string) (*snapshot, error) {
	f, err := os.Open(h.fileFor(id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// List is listSnapshots, oldest first.
func (h *History) List() []SnapshotMeta {
	entries, err := os.ReadDir(h.dir)
	out := []SnapshotMeta{}
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json.gz") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json.gz")
		s, err := h.read(id)
		if err != nil {
			continue
		}
		st, err := os.Stat(h.fileFor(id))
		if err != nil {
			continue
		}
		out = append(out, SnapshotMeta{ID: id, TakenAt: s.TakenAt, Files: s.Files, Bytes: s.Bytes, Disk: st.Size()})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TakenAt < out[j].TakenAt })
	return out
}

// indexMtime is the newest mtime under the index tree, to three levels, in seconds.
func (h *History) indexMtime() int64 {
	var newest float64
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 3 {
			return
		}
		names, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, n := range names {
			p := filepath.Join(dir, n.Name())
			if st, err := os.Stat(p); err == nil && msOf(st) > newest {
				newest = msOf(st)
			}
			if n.IsDir() {
				walk(p, depth+1)
			}
		}
	}
	walk(h.ix.P.GufiIndex, 0)
	return int64(newest / 1000)
}

func (h *History) kbRel(p string) string {
	idx, _ := filepath.Abs(h.ix.P.GufiIndex)
	abs, _ := filepath.Abs(p)
	if strings.HasPrefix(abs, idx) {
		p = abs[len(idx):]
	}
	return strings.TrimLeft(p, "/")
}

func num(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// lessUTF16 is JavaScript's < on strings: by UTF-16 code units.
func lessUTF16(a, b string) bool {
	if isASCII(a) && isASCII(b) {
		return a < b
	}
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Capture takes a snapshot now.
func (h *History) Capture(ctx context.Context) (*SnapshotMeta, error) {
	if !h.available() || h.ix.App == nil || h.ix.App.Gufi == nil {
		return nil, nil
	}
	threads := min(max(CPUCount(), 1), 16)
	res, err := h.ix.App.Gufi.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: []string{"-n", strconv.Itoa(threads), "-d", gufi.Delim,
		"-E", "SELECT rpath(sname, sroll, name), size, mtime, inode FROM vrpentries;", h.ix.P.GufiIndex}})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && len(res.Stdout) == 0 {
		return nil, gufi.RawError(string(res.Stdout), res.Stderr)
	}
	var entries []Entry
	var total float64
	for _, r := range gufi.ParseRows(string(res.Stdout), 1) {
		e := Entry{Path: h.kbRel(r[0])}
		if len(r) > 1 {
			e.Size = num(r[1])
		}
		if len(r) > 2 {
			e.Mtime = num(r[2])
		}
		if len(r) > 3 {
			e.Inode = num(r[3])
		}
		if e.Path != "" {
			entries = append(entries, e)
			total += e.Size
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return lessUTF16(entries[i].Path, entries[j].Path) })
	if entries == nil {
		entries = []Entry{}
	}
	takenAt := time.Now().Unix()
	snap := snapshot{SnapshotMeta: SnapshotMeta{ID: strconv.FormatInt(takenAt, 10), TakenAt: takenAt, Files: len(entries), Bytes: total}, Entries: entries}
	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(snap)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(b)
	zw.Close()
	if err := os.WriteFile(h.fileFor(snap.ID), gz.Bytes(), 0o644); err != nil {
		return nil, err
	}
	h.prune()
	st, err := os.Stat(h.fileFor(snap.ID))
	if err != nil {
		return nil, err
	}
	m := snap.SnapshotMeta
	m.Disk = st.Size()
	return &m, nil
}

func (h *History) delete(id string) {
	os.Remove(h.fileFor(id))
	h.mu.Lock()
	if h.cachedID == id {
		h.cachedID, h.cached = "", nil
	}
	h.mu.Unlock()
}

func (h *History) prune() {
	all := h.List()
	for _, s := range all[:max(0, len(all)-h.keep)] {
		os.Remove(h.fileFor(s.ID))
	}
}

// Check is a proof-of-life marker: the index was examined and found unchanged.
type Check struct {
	At int64 `json:"at"`
}

func (h *History) checksFile() string { return filepath.Join(h.dir, "checks.jsonl") }

func (h *History) listChecks() []Check {
	out := []Check{}
	b, err := os.ReadFile(h.checksFile())
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		var c Check
		if json.Unmarshal([]byte(l), &c) != nil {
			return []Check{}
		}
		out = append(out, c)
	}
	return out
}

func (h *History) recordCheck() {
	now := time.Now()
	checks := h.listChecks()
	if len(checks) > 0 {
		y1, m1, d1 := time.Unix(checks[len(checks)-1].At, 0).Date()
		y2, m2, d2 := now.Date()
		if y1 == y2 && m1 == m2 && d1 == d2 {
			return
		}
	}
	os.MkdirAll(h.dir, 0o755)
	kept := append(checks, Check{At: now.Unix()})
	if len(kept) > 60 {
		kept = kept[len(kept)-60:]
	}
	var b strings.Builder
	for _, c := range kept {
		j, _ := json.Marshal(c)
		b.Write(j)
		b.WriteByte('\n')
	}
	os.WriteFile(h.checksFile(), []byte(b.String()), 0o644)
}

// CaptureIfStale captures when the index has changed and the cadence has
// elapsed; otherwise it leaves the day's proof-of-life marker.
func (h *History) CaptureIfStale(ctx context.Context) (*SnapshotMeta, error) {
	if !h.available() {
		return nil, nil
	}
	all := h.List()
	if len(all) > 0 {
		last := all[len(all)-1]
		if h.indexMtime() <= last.TakenAt {
			h.recordCheck()
			return nil, nil
		}
		every := 86400.0
		if h.ix.App != nil && h.ix.App.Settings != nil {
			every = float64(h.ix.App.Settings.Effective().HistoryIntervalSec)
		}
		if every > 0 && float64(time.Now().Unix()-last.TakenAt) < every {
			h.recordCheck()
			return nil, nil
		}
	}
	return h.Capture(ctx)
}

func (h *History) snapshotOf(id string) (*snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cachedID == id && h.cached != nil {
		return h.cached, nil
	}
	s, err := h.read(id)
	if err != nil {
		return nil, err
	}
	h.cachedID, h.cached = id, s
	return s, nil
}

type levelDir struct {
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Files int     `json:"files"`
	Bytes float64 `json:"bytes"`
}

type levelFile struct {
	Name  string  `json:"name"`
	Path  string  `json:"path"`
	Size  float64 `json:"size"`
	Mtime float64 `json:"mtime"`
}

type totals struct {
	Files int     `json:"files"`
	Bytes float64 `json:"bytes"`
}

type level struct {
	Available bool        `json:"available"`
	Dir       string      `json:"dir"`
	Dirs      []levelDir  `json:"dirs"`
	Files     []levelFile `json:"files"`
	Totals    totals      `json:"totals"`
}

func (h *History) level(id, dir string) (level, error) {
	snap, err := h.snapshotOf(id)
	if err != nil {
		return level{}, err
	}
	prefix := ""
	if dir != "" {
		prefix = strings.TrimRight(dir, "/") + "/"
	}
	dirs := map[string]*levelDir{}
	var order []string
	files := []levelFile{}
	var t totals
	for _, e := range snap.Entries {
		if prefix != "" && !strings.HasPrefix(e.Path, prefix) {
			continue
		}
		rest := e.Path[len(prefix):]
		if rest == "" {
			continue
		}
		t.Files++
		t.Bytes += e.Size
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			files = append(files, levelFile{rest, e.Path, e.Size, e.Mtime})
			continue
		}
		name := rest[:slash]
		d := dirs[name]
		if d == nil {
			d = &levelDir{Name: name, Path: prefix + name}
			dirs[name] = d
			order = append(order, name)
		}
		d.Files++
		d.Bytes += e.Size
	}
	ds := []levelDir{}
	for _, n := range order {
		ds = append(ds, *dirs[n])
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Bytes > ds[j].Bytes })
	sort.SliceStable(files, func(i, j int) bool { return lessUTF16(files[i].Name, files[j].Name) })
	return level{Available: true, Dir: dir, Dirs: ds, Files: files, Totals: t}, nil
}

type modified struct {
	Path     string  `json:"path"`
	Size     float64 `json:"size"`
	Mtime    float64 `json:"mtime"`
	WasSize  float64 `json:"wasSize"`
	WasMtime float64 `json:"wasMtime"`
}

type renamed struct {
	From string  `json:"from"`
	To   string  `json:"to"`
	Size float64 `json:"size"`
}

type diffCounts struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
	Renamed  int `json:"renamed"`
}

type diffOut struct {
	Available bool         `json:"available"`
	From      SnapshotMeta `json:"from"`
	To        SnapshotMeta `json:"to"`
	Added     []Entry      `json:"added"`
	Removed   []Entry      `json:"removed"`
	Modified  []modified   `json:"modified"`
	Renamed   []renamed    `json:"renamed"`
	Counts    diffCounts   `json:"counts"`
}

func capList[T any](xs []T) []T {
	if len(xs) > 500 {
		return xs[:500]
	}
	return xs
}

func (h *History) diff(fromID, toID string) (diffOut, error) {
	a, err := h.read(fromID)
	if err != nil {
		return diffOut{}, err
	}
	b, err := h.read(toID)
	if err != nil {
		return diffOut{}, err
	}
	byA := map[string]Entry{}
	for _, e := range a.Entries {
		byA[e.Path] = e
	}
	byB := map[string]bool{}
	for _, e := range b.Entries {
		byB[e.Path] = true
	}
	var addedRaw, removedRaw []Entry
	mods := []modified{}
	for _, e := range b.Entries {
		was, ok := byA[e.Path]
		if !ok {
			addedRaw = append(addedRaw, e)
		} else if was.Size != e.Size || was.Mtime != e.Mtime {
			mods = append(mods, modified{e.Path, e.Size, e.Mtime, was.Size, was.Mtime})
		}
	}
	for _, e := range a.Entries {
		if !byB[e.Path] {
			removedRaw = append(removedRaw, e)
		}
	}
	byInode := map[float64]Entry{}
	for _, e := range removedRaw {
		if e.Inode != 0 {
			byInode[e.Inode] = e
		}
	}
	ren := []renamed{}
	added := []Entry{}
	for _, e := range addedRaw {
		if prior, ok := byInode[e.Inode]; ok && e.Inode != 0 {
			ren = append(ren, renamed{prior.Path, e.Path, e.Size})
			delete(byInode, e.Inode)
		} else {
			added = append(added, e)
		}
	}
	removed := []Entry{}
	for _, e := range removedRaw {
		if _, ok := byInode[e.Inode]; e.Inode == 0 || ok {
			removed = append(removed, e)
		}
	}
	meta := func(s *snapshot) SnapshotMeta {
		m := s.SnapshotMeta
		if st, err := os.Stat(h.fileFor(s.ID)); err == nil {
			m.Disk = st.Size()
		}
		return m
	}
	return diffOut{Available: true, From: meta(a), To: meta(b), Added: capList(added), Removed: capList(removed), Modified: capList(mods), Renamed: capList(ren),
		Counts: diffCounts{len(added), len(removed), len(mods), len(ren)}}, nil
}

func storedBytes(s []SnapshotMeta) int64 {
	var n int64
	for _, x := range s {
		n += x.Disk
	}
	return n
}

var digits = regexp.MustCompile(`^\d+$`)

func (h *History) register(mux *http.ServeMux, wrap func(func(http.ResponseWriter, *http.Request) error) http.HandlerFunc) {
	mux.HandleFunc("GET /api/kb/history", wrap(func(w http.ResponseWriter, r *http.Request) error {
		h.CaptureIfStale(r.Context())
		snaps := h.List()
		checks := h.listChecks()
		if len(checks) > 14 {
			checks = checks[len(checks)-14:]
		}
		return writeOK(w, struct {
			Available   bool           `json:"available"`
			Snapshots   []SnapshotMeta `json:"snapshots"`
			Keep        int            `json:"keep"`
			StoredBytes int64          `json:"storedBytes"`
			Checks      []Check        `json:"checks"`
		}{h.available(), snaps, h.keep, storedBytes(snaps), checks})
	}))
	mux.HandleFunc("DELETE /api/kb/history/snapshot", wrap(func(w http.ResponseWriter, r *http.Request) error {
		id := r.URL.Query().Get("id")
		if !digits.MatchString(id) {
			return apierr.New(400, "snapshot id required")
		}
		h.delete(id)
		snaps := h.List()
		return writeOK(w, struct {
			Snapshots   []SnapshotMeta `json:"snapshots"`
			StoredBytes int64          `json:"storedBytes"`
		}{snaps, storedBytes(snaps)})
	}))
	mux.HandleFunc("POST /api/kb/history/prune", wrap(func(w http.ResponseWriter, r *http.Request) error {
		body := readBody(r)
		n := 10
		if k, ok := body["keep"]; ok {
			if f := jsNum(k); f == f && f != 0 {
				n = int(f)
			}
		}
		n = max(1, min(500, n))
		all := h.List()
		for _, s := range all[:max(0, len(all)-n)] {
			h.delete(s.ID)
		}
		snaps := h.List()
		return writeOK(w, struct {
			Snapshots   []SnapshotMeta `json:"snapshots"`
			Removed     int            `json:"removed"`
			StoredBytes int64          `json:"storedBytes"`
		}{snaps, len(all) - len(snaps), storedBytes(snaps)})
	}))
	mux.HandleFunc("POST /api/kb/history/capture", wrap(func(w http.ResponseWriter, r *http.Request) error {
		snap, err := h.Capture(r.Context())
		if err != nil {
			return err
		}
		return writeOK(w, map[string]any{"captured": snap})
	}))
	mux.HandleFunc("GET /api/kb/history/level", wrap(func(w http.ResponseWriter, r *http.Request) error {
		all := h.List()
		if len(all) == 0 {
			return writeOK(w, map[string]any{"available": false})
		}
		q := r.URL.Query()
		id := all[len(all)-1].ID
		if q.Has("id") {
			id = q.Get("id")
		}
		l, err := h.level(id, q.Get("dir"))
		if err != nil {
			return err
		}
		return writeOK(w, l)
	}))
	mux.HandleFunc("GET /api/kb/history/diff", wrap(func(w http.ResponseWriter, r *http.Request) error {
		all := h.List()
		if len(all) < 2 {
			return writeOK(w, map[string]any{"available": false})
		}
		q := r.URL.Query()
		to, from := all[len(all)-1].ID, all[len(all)-2].ID
		if q.Has("to") {
			to = q.Get("to")
		}
		if q.Has("from") {
			from = q.Get("from")
		}
		d, err := h.diff(from, to)
		if err != nil {
			return err
		}
		return writeOK(w, d)
	}))
}

