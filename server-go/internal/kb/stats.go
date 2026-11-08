package kb

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// jsNumberOr is Number(s), or def where that is NaN.
func jsNumberOr(s string, def float64) float64 {
	n := settings.JSNumber(s)
	if math.IsNaN(n) {
		return def
	}
	return math.Floor(n)
}

type extCount struct {
	Ext   string `json:"ext"`
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
}

// corpus is gufi.ts's CorpusStats for an index here.
type corpus struct {
	Available bool
	Files     int64
	Dirs      int64
	Total     int64
	ByExt     []extCount
}

func (c corpus) obj() *Obj {
	if !c.Available {
		return O("available", false)
	}
	return O("available", true, "files", c.Files, "dirs", c.Dirs, "totalBytes", c.Total, "byExt", c.ByExt)
}

// statsCache keeps counts ten minutes, as gufi.ts's countCache does for an
// index here; a change made through this server drops them.
type statsCache struct {
	mu sync.Mutex
	m  map[string]struct {
		at time.Time
		c  corpus
	}
}

func (s *statsCache) forget() { s.mu.Lock(); s.m = nil; s.mu.Unlock() }

var localThreads = strconv.Itoa(min(max(runtime.NumCPU(), 1), 16))

// corpusStats counts a library's index here in one aggregate walk.
func (m *Module) corpusStats(ctx context.Context, indexRoot string) (corpus, error) {
	if !exists(filepath.Join(m.Roots.GufiBin, "gufi_query")) || !exists(indexRoot) {
		return corpus{}, nil
	}
	m.stats.mu.Lock()
	if hit, ok := m.stats.m[indexRoot]; ok && time.Since(hit.at) < 10*time.Minute {
		m.stats.mu.Unlock()
		return hit.c, nil
	}
	m.stats.mu.Unlock()
	ext := `lower(CASE WHEN name LIKE '%.%' THEN replace(name, rtrim(name, replace(name, '.', '')), '') ELSE '' END)`
	res, err := m.Tags.Runner.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Timeout: 60 * time.Second, Args: []string{
		"-n", localThreads, "-d", tagDelim, "-a", "1",
		"-I", "CREATE TABLE t (k TEXT, n INT64, b INT64);",
		"-K", "CREATE TABLE agg (k TEXT, n INT64, b INT64);",
		"-E", "INSERT INTO t SELECT " + ext + ", count(*), sum(size) FROM vrpentries GROUP BY 1; " +
			"INSERT INTO t SELECT char(1)||'summary', sum(totfiles), sum(totsize) FROM vrsummary; " +
			"INSERT INTO t SELECT char(1)||'dirs', count(*), 0 FROM vrsummary;",
		"-J", "INSERT INTO agg SELECT * FROM t;",
		"-G", "SELECT k, sum(n), sum(b) FROM agg GROUP BY k;",
		indexRoot,
	}})
	if err != nil {
		return corpus{}, err
	}
	if res.ExitCode != 0 && len(res.Stdout) == 0 {
		return corpus{}, errors.New("gufi_query failed: " + firstNonEmpty(res.Stderr, "exit "+strconv.Itoa(res.ExitCode)))
	}
	c := corpus{Available: true, ByExt: []extCount{}}
	num := func(s string) int64 {
		f := settings.JSNumber(s)
		if math.IsNaN(f) {
			return 0
		}
		return int64(f)
	}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l == "" {
			continue
		}
		r := strings.Split(l, tagDelim)
		for len(r) < 3 {
			r = append(r, "")
		}
		switch r[0] {
		case "\x01summary":
			c.Files, c.Total = num(r[1]), num(r[2])
		case "\x01dirs":
			c.Dirs = num(r[1])
		default:
			e := r[0]
			if e == "" {
				e = "(none)"
			}
			c.ByExt = append(c.ByExt, extCount{e, num(r[1]), num(r[2])})
		}
	}
	sort.SliceStable(c.ByExt, func(i, j int) bool { return c.ByExt[i].Count > c.ByExt[j].Count })
	c.ByExt = c.ByExt[:min(len(c.ByExt), 25)]
	m.stats.mu.Lock()
	if m.stats.m == nil {
		m.stats.m = map[string]struct {
			at time.Time
			c  corpus
		}{}
	}
	m.stats.m[indexRoot] = struct {
		at time.Time
		c  corpus
	}{time.Now(), c}
	m.stats.mu.Unlock()
	return c, nil
}

// statsRoute is GET /api/kb/stats: one library's counts, or with
// library=all the total over every library with each one's line.
func (m *Module) statsRoute(w http.ResponseWriter, r *http.Request) error {
	if strings.ToLower(qs(r, "library")) != All {
		lib, err := m.lib(r, body{})
		if err != nil {
			return m.handled(w, r, err)
		}
		c, err := m.corpusStats(r.Context(), lib.IndexRoot)
		if err != nil {
			return err
		}
		writeJSON(w, 200, c.obj())
		return nil
	}
	libs, err := m.allLibs(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	type each struct {
		c   corpus
		err error
	}
	got := make([]each, len(libs))
	var wg sync.WaitGroup
	for i, l := range libs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := m.corpusStats(r.Context(), l.IndexRoot)
			got[i] = each{c, err}
		}()
	}
	wg.Wait()
	byExt := &orderedCounts{}
	bytes := map[string]int64{}
	var files, dirs, total int64
	available := false
	lines := []*Obj{}
	for i, l := range libs {
		e := got[i]
		line := O("id", l.ID, "label", l.Label, "site", nil, "writable", l.Writable)
		if e.err != nil {
			line.Set("available", false).Set("error", SliceUTF16(errMessage(e.err), 200))
			lines = append(lines, line)
			continue
		}
		if e.c.Available {
			available = true
			files += e.c.Files
			dirs += e.c.Dirs
			total += e.c.Total
			for _, x := range e.c.ByExt {
				byExt.add(x.Ext, int(x.Count))
				bytes[x.Ext] += x.Bytes
			}
		}
		line.Set("files", e.c.Files).Set("dirs", e.c.Dirs).Set("totalBytes", e.c.Total).Set("available", e.c.Available)
		lines = append(lines, line)
	}
	exts := []extCount{}
	for _, k := range byExt.keys {
		exts = append(exts, extCount{k, int64(byExt.n[k]), bytes[k]})
	}
	sort.SliceStable(exts, func(i, j int) bool { return exts[i].Count > exts[j].Count })
	exts = exts[:min(len(exts), 25)]
	writeJSON(w, 200, O("available", available, "files", files, "dirs", dirs, "totalBytes", total, "byExt", exts, "libraries", lines))
	return nil
}
