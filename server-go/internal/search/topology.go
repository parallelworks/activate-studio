package search

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TopologyRow is one folder's totals (topology.ts).
type TopologyRow struct {
	Dir   string  `json:"dir"`
	Files float64 `json:"files"`
	Bytes float64 `json:"bytes"`
}

// Topology is topology.ts's Topology, in its key order.
type Topology struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Dirs      *[]TopologyRow `json:"dirs,omitempty"`
	Root      *string        `json:"root,omitempty"`
	Levels    int            `json:"levels,omitempty"`
	MS        *int64         `json:"ms,omitempty"`
}

const (
	TopologyLevels  = 6
	TopologyRowsMax = 20_000
)

// TopologyStages is topologyStages.
func TopologyStages(rootLen, levels int) []string {
	head := `CASE WHEN instr(rest, '/') > 0 THEN substr(rest, 1, instr(rest, '/') - 1) ELSE rest END`
	tail := `CASE WHEN instr(rest, '/') > 0 THEN substr(rest, instr(rest, '/') + 1) ELSE '' END`
	E := "INSERT INTO t WITH RECURSIVE " +
		"d(p, f, b) AS (SELECT ltrim(substr(rpath(sname, sroll), " + strconv.Itoa(rootLen+1) + "), '/'), totfiles, totsize FROM vrsummary), " +
		"s(i, rest, pre, f, b) AS (SELECT 0, p, '', f, b FROM d UNION ALL " +
		"SELECT i + 1, " + tail + ", pre || CASE WHEN pre = '' THEN '' ELSE '/' END || " + head + ", f, b FROM s WHERE i < " + strconv.Itoa(levels) + " AND rest <> '') " +
		"SELECT pre, f, b FROM s WHERE i = " + strconv.Itoa(levels) + " OR rest = '';"
	return []string{
		"-I", "CREATE TABLE t (p TEXT, f INT64, b INT64);",
		"-K", "CREATE TABLE agg (p TEXT, f INT64, b INT64);",
		"-E", E,
		"-J", "INSERT INTO agg SELECT p, sum(f), sum(b) FROM t GROUP BY p;",
		"-G", "SELECT p, sum(f), sum(b) FROM agg GROUP BY p;",
	}
}

// Shallower is shallower.
func Shallower(rows []TopologyRow, levels int) []TopologyRow {
	var order []string
	sum := map[string]*TopologyRow{}
	for _, r := range rows {
		var parts []string
		for _, p := range strings.Split(r.Dir, "/") {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > levels {
			parts = parts[:levels]
		}
		d := strings.Join(parts, "/")
		cur := sum[d]
		if cur == nil {
			cur = &TopologyRow{Dir: d}
			sum[d] = cur
			order = append(order, d)
		}
		cur.Files += r.Files
		cur.Bytes += r.Bytes
	}
	out := make([]TopologyRow, 0, len(order))
	for _, d := range order {
		out = append(out, *sum[d])
	}
	return out
}

func cleanDir(dir string) (string, error) {
	var parts []string
	for _, p := range strings.Split(dir, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." || strings.Contains(p, "\x00") {
			return "", apierr.New(400, "dir: a folder of the library")
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/"), nil
}

type topoEntry struct {
	at time.Time
	v  Topology
}

var topoCache = struct {
	sync.Mutex
	m     map[string]topoEntry
	order []string
}{m: map[string]topoEntry{}}

// ResetTopologyForTests is resetTopologyForTests.
func ResetTopologyForTests() {
	topoCache.Lock()
	topoCache.m, topoCache.order = map[string]topoEntry{}, nil
	topoCache.Unlock()
}

func numOrZero(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

// Topology is topology(lib, dir): lib nil is the main folder.
func (e *Engine) Topology(ctx context.Context, lib *libraries.Library, dir string, refresh bool) (Topology, error) {
	root := e.GufiIndex
	id := libraries.PrimaryID
	if lib != nil {
		root, id = lib.IndexRoot, lib.ID
	}
	sub, err := cleanDir(dir)
	if err != nil {
		return Topology{}, err
	}
	remoteLib := strings.HasPrefix(root, libraries.RemotePrefix)
	if lib == nil && !(e.gufiInstalled() && exists(e.GufiIndex)) {
		return Topology{Available: false, Reason: "none"}, nil
	}
	if lib != nil && !remoteLib && (!e.gufiInstalled() || !lib.Caps.Index) {
		return Topology{Available: false, Reason: "none"}, nil
	}
	key := id + "\x00" + root + "\x00" + sub
	ttl := 2 * time.Minute
	if remoteLib {
		ttl = 10 * time.Minute
	}
	topoCache.Lock()
	hit, ok := topoCache.m[key]
	topoCache.Unlock()
	if ok && !refresh && time.Since(hit.at) < ttl {
		return hit.v, nil
	}
	walked := root
	if remoteLib {
		r, err := e.RemoteIndexRoot(root)
		if err != nil {
			return Topology{}, err
		}
		walked = r
		if sub != "" {
			walked = path.Join(r, sub)
		}
	} else if sub != "" {
		walked = filepath.Join(root, sub)
	}
	t0 := time.Now()
	rows, err := e.QueryAggregate(gufitrace.Source(ctx, "stats"), TopologyStages(len([]rune(walked)), TopologyLevels), root, sub)
	if err != nil {
		return Topology{}, err
	}
	dirs := []TopologyRow{}
	for _, r := range rows {
		get := func(i int) string {
			if i < len(r) {
				return r[i]
			}
			return ""
		}
		row := TopologyRow{Dir: get(0), Files: numOrZero(get(1)), Bytes: numOrZero(get(2))}
		if row.Files > 0 || row.Bytes > 0 || row.Dir != "" {
			dirs = append(dirs, row)
		}
	}
	levels := TopologyLevels
	for len(dirs) > TopologyRowsMax && levels > 1 {
		levels--
		dirs = Shallower(dirs, levels)
	}
	if sub != "" {
		for i := range dirs {
			if dirs[i].Dir != "" {
				dirs[i].Dir = sub + "/" + dirs[i].Dir
			} else {
				dirs[i].Dir = sub
			}
		}
	}
	ms := time.Since(t0).Milliseconds()
	v := Topology{Available: true, Dirs: &dirs, Root: &sub, Levels: levels, MS: &ms}
	topoCache.Lock()
	if _, had := topoCache.m[key]; !had {
		topoCache.order = append(topoCache.order, key)
	}
	topoCache.m[key] = topoEntry{time.Now(), v}
	if len(topoCache.order) > 200 {
		delete(topoCache.m, topoCache.order[0])
		topoCache.order = topoCache.order[1:]
	}
	topoCache.Unlock()
	return v, nil
}

var _ = os.Stat
