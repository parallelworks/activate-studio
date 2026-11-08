package maps

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A file's nearest files by meaning (semanticNeighbors).

const sep = "\x1f"

type place struct {
	x, y, z any
	c       int
}

// neighborCache is what one library's lookups keep: each index db with
// vectors and the model they come from (five minutes, or until the map is
// made again), where each file of the map sits, and the answers given.
type neighborCache struct {
	vectorDbs *struct {
		stamp string
		at    time.Time
		dbs   map[string]string
		order []string
	}
	places *struct {
		stamp  string
		places map[string]place
	}
	answers *answerCache
}

type answer struct {
	once sync.Once
	v    *jsonv.Object
	err  error
}

type answerCache struct {
	order []string
	m     map[string]*answer
}

func (m *Module) neighborCacheOf(key string) *neighborCache {
	c := m.neighbors[key]
	if c == nil {
		c = &neighborCache{answers: &answerCache{m: map[string]*answer{}}}
		m.neighbors[key] = c
	}
	return c
}

// mapStamp is a map's own time: answers made for one map are kept until
// the next.
func mapStamp(out string) string {
	fi, err := os.Stat(filepath.Join(out, "overview.json"))
	if err != nil {
		return "0"
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 10)
}

func esc(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// listVectorDbs is every db.db under root, dot entries left out, in walk order.
func listVectorDbs(root string) []string {
	var out []string
	var walk func(dir string)
	walk = func(dir string) {
		es, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range es {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if e.IsDir() {
				walk(p)
			} else if e.Name() == "db.db" {
				out = append(out, p)
			}
		}
	}
	walk(root)
	return out
}

// sqliteScript runs lines in gufi_sqlite3 here; its output, "" on failure.
func (m *Module) sqliteScript(ctx context.Context, lines []string) string {
	if m.Local == nil {
		return ""
	}
	r, err := m.Local.Run(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Stdin: []byte(strings.Join(lines, "\n")), Timeout: 10 * time.Minute})
	if err != nil {
		return ""
	}
	return string(r.Stdout)
}

func (m *Module) vectorDbModels(ctx context.Context, id, root string) (map[string]string, []string) {
	m.mu.Lock()
	cache := m.neighborCacheOf(id)
	stamp := mapStamp(m.mapDir(id))
	if v := cache.vectorDbs; v != nil && v.stamp == stamp && time.Since(v.at) < 5*time.Minute {
		m.mu.Unlock()
		return v.dbs, v.order
	}
	m.mu.Unlock()
	all := listVectorDbs(root)
	found := map[string]map[string]bool{}
	var foundOrder []string
	for s := 0; s < len(all); s += 200 {
		var lines []string
		for _, db := range all[s:min(s+200, len(all))] {
			lines = append(lines, "ATTACH "+esc(db)+" AS c;",
				"SELECT 'HAS' || "+esc(sep)+" || "+esc(db)+" || "+esc(sep)+" || name FROM c.sqlite_master WHERE name IN ('gvec', 'gvecmodel');", "DETACH c;")
		}
		for _, l := range strings.Split(m.sqliteScript(ctx, lines), "\n") {
			f := strings.Split(l, sep)
			if len(f) >= 3 && f[0] == "HAS" && f[1] != "" && f[2] != "" {
				if found[f[1]] == nil {
					found[f[1]] = map[string]bool{}
					foundOrder = append(foundOrder, f[1])
				}
				found[f[1]][strings.TrimSpace(f[2])] = true
			}
		}
	}
	dbs := map[string]string{}
	var order []string
	var withModel []string
	for _, db := range foundOrder {
		if found[db]["gvec"] && found[db]["gvecmodel"] {
			withModel = append(withModel, db)
		}
	}
	for s := 0; s < len(withModel); s += 200 {
		var lines []string
		for _, db := range withModel[s:min(s+200, len(withModel))] {
			lines = append(lines, "ATTACH "+esc(db)+" AS c;",
				"SELECT 'MODEL' || "+esc(sep)+" || "+esc(db)+" || "+esc(sep)+" || model FROM c.gvecmodel LIMIT 1;", "DETACH c;")
		}
		for _, l := range strings.Split(m.sqliteScript(ctx, lines), "\n") {
			f := strings.Split(l, sep)
			if len(f) >= 3 && f[0] == "MODEL" && f[1] != "" && f[2] != "" {
				if _, ok := dbs[f[1]]; !ok {
					order = append(order, f[1])
				}
				dbs[f[1]] = strings.TrimSpace(f[2])
			}
		}
	}
	// A directory embedded before models were recorded holds the first model's vectors.
	for _, db := range foundOrder {
		if _, ok := dbs[db]; found[db]["gvec"] && !ok {
			dbs[db] = "minilm384"
			order = append(order, db)
		}
	}
	m.mu.Lock()
	cache.vectorDbs = &struct {
		stamp string
		at    time.Time
		dbs   map[string]string
		order []string
	}{stamp, time.Now(), dbs, order}
	m.mu.Unlock()
	return dbs, order
}

// placesMax is PLACES_MAX: a map of more files keeps no places.
const placesMax = 300_000

// mapPlaces is where each file of a library's map sits, made once per map.
func (m *Module) mapPlaces(key, out string) map[string]place {
	m.mu.Lock()
	defer m.mu.Unlock()
	cache := m.neighborCacheOf(key)
	stamp := mapStamp(out)
	if cache.places != nil && cache.places.stamp == stamp {
		return cache.places.places
	}
	st := m.stateOf(key)
	var places map[string]place
	if hit := readJSON(filepath.Join(out, "overview.json"), st.overview); hit != nil {
		o := hit.data
		files, _ := jsonv.Num(get(o, "files"))
		if jsonv.Truthy(get(o, "available")) && files <= placesMax {
			places = map[string]place{}
			for _, cv := range jsonv.Arr(get(o, "clusters")) {
				cn, _ := jsonv.Num(get(jsonv.Obj(cv), "c"))
				b, err := os.ReadFile(filepath.Join(out, "cluster-"+jsonv.FormatNumber(cn)+".json"))
				if err != nil {
					continue
				}
				v, err := jsonv.Decode(b)
				if err != nil {
					continue
				}
				for _, p := range jsonv.Arr(get(jsonv.Obj(v), "points")) {
					po := jsonv.Obj(p)
					places[jsonv.String(get(po, "p"))] = place{get(po, "x"), get(po, "y"), get(po, "z"), int(cn)}
				}
			}
		}
	}
	cache.places = &struct {
		stamp  string
		places map[string]place
	}{stamp, places}
	return places
}

// vectorHex is a vector's float32 little-endian bytes in hex.
func vectorHex(v []float64) string {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(float32(x)))
	}
	return hex.EncodeToString(b)
}

// simOf is the cosine of two unit vectors from vec0's distance, to three places.
func simOf(d float64) float64 {
	return jsRound(math.Max(-1, math.Min(1, 1-(d*d)/2))*1000) / 1000
}

// jsRound is Math.round.
func jsRound(x float64) float64 { return math.Floor(x + 0.5) }

func neighborOf(p string, d float64, places map[string]place) *jsonv.Object {
	n := jsonv.NewObject("p", p, "sim", simOf(d))
	if pl, ok := places[p]; ok {
		n.Set("x", pl.x).Set("y", pl.y).Set("z", pl.z).Set("c", float64(pl.c))
	}
	return n
}

// Neighbors is semanticNeighbors: the k files nearest one file of a
// library by meaning, kept until the map is made again.
func (m *Module) Neighbors(ctx context.Context, file string, k float64, id string, viewer libraries.Viewer) (any, error) {
	rel := path.Clean(strings.ReplaceAll(file, `\`, "/"))
	rel = strings.TrimPrefix(rel, "./")
	if file == "" || rel == "." || strings.HasPrefix(rel, "/") || rel == ".." || strings.HasPrefix(rel, "../") {
		return nil, apierr.New(400, "file: a path in the library")
	}
	n := int(math.Min(20, math.Max(1, jsFloorOr(k, 5))))
	_, t, meaningOff, err := m.libraryOf(id, viewer)
	if err != nil {
		return nil, err
	}
	if meaningOff {
		return jsonv.NewObject("file", rel, "neighbors", []any{}, "reason", "off"), nil
	}
	key := mapStamp(t.Out) + "\x00" + rel + "\x00" + strconv.Itoa(n)
	m.mu.Lock()
	cache := m.neighborCacheOf(t.Key)
	a := cache.answers.m[key]
	if a == nil {
		a = &answer{}
		cache.answers.m[key] = a
		cache.answers.order = append(cache.answers.order, key)
		if len(cache.answers.order) > 300 {
			delete(cache.answers.m, cache.answers.order[0])
			cache.answers.order = cache.answers.order[1:]
		}
	}
	m.mu.Unlock()
	a.once.Do(func() {
		if t.Remote != nil {
			a.v, a.err = m.remoteNeighbors(ctx, t, rel, n)
		} else {
			a.v, a.err = m.localNeighbors(ctx, id, t.Index, rel, n)
		}
		if a.err != nil {
			m.mu.Lock()
			if c := m.neighbors[t.Key]; c != nil && c.answers.m[key] == a {
				delete(c.answers.m, key)
			}
			m.mu.Unlock()
		}
	})
	return a.v, a.err
}

func (m *Module) localNeighbors(ctx context.Context, id, root, rel string, k int) (*jsonv.Object, error) {
	dir, name := path.Dir(rel), path.Base(rel)
	ownDir := root
	if dir != "." {
		ownDir = filepath.Join(root, dir)
	}
	own := filepath.Join(ownDir, "db.db")
	dbs, order := m.vectorDbModels(ctx, id, root)
	model, ok := dbs[own]
	none := func() *jsonv.Object { return jsonv.NewObject("file", rel, "neighbors", []any{}, "reason", "none") }
	if !ok {
		return none(), nil
	}
	out := m.sqliteScript(ctx, []string{"ATTACH " + esc(own) + " AS s;",
		"SELECT 'Q' || " + esc(sep) + " || vec_to_json(v.fp384) FROM s.gvec v JOIN s.gchunks g ON g.cid = v.cid WHERE g.fname = " + esc(name) + ";", "DETACH s;"})
	var vecs [][]float64
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "Q"+sep) {
			continue
		}
		var v []float64
		dv, err := jsonv.Decode([]byte(l[2:]))
		if err != nil {
			continue
		}
		for _, x := range jsonv.Arr(dv) {
			f, _ := jsonv.Num(x)
			v = append(v, f)
		}
		if len(v) > 0 {
			vecs = append(vecs, v)
		}
	}
	if len(vecs) == 0 {
		return none(), nil
	}
	mean := make([]float64, len(vecs[0]))
	for i := range mean {
		s := 0.0
		for _, v := range vecs {
			if i < len(v) {
				s += v[i]
			}
		}
		mean[i] = s / float64(len(vecs))
	}
	norm := 0.0
	for _, x := range mean {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		norm = 1
	}
	for i := range mean {
		mean[i] /= norm
	}
	script := []string{"CREATE TABLE temp.q AS SELECT X'" + vectorHex(mean) + "' AS qe;"}
	for _, db := range order {
		if dbs[db] != model {
			continue
		}
		relDir, _ := filepath.Rel(root, filepath.Dir(db))
		if relDir == "." {
			relDir = ""
		}
		script = append(script, "ATTACH "+esc(db)+" AS d;",
			"SELECT 'N' || "+esc(sep)+" || "+esc(relDir)+" || "+esc(sep)+" || g.fname || "+esc(sep)+" || n.distance FROM "+
				"(SELECT cid, distance FROM d.gvec WHERE fp384 MATCH (SELECT qe FROM temp.q) ORDER BY distance ASC LIMIT "+strconv.Itoa(k+4)+") n JOIN d.gchunks g ON g.cid = n.cid;",
			"DETACH d;")
	}
	best := map[string]float64{}
	var seen []string
	for _, l := range strings.Split(m.sqliteScript(ctx, script), "\n") {
		f := strings.Split(l, sep)
		if len(f) < 4 || f[0] != "N" || f[2] == "" {
			continue
		}
		p := f[2]
		if f[1] != "" {
			p = f[1] + "/" + f[2]
		}
		v := jsNumber(f[3])
		if p == rel || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		if b, ok := best[p]; !ok || v < b {
			if !ok {
				seen = append(seen, p)
			}
			best[p] = v
		}
	}
	sort.SliceStable(seen, func(i, j int) bool { return best[seen[i]] < best[seen[j]] })
	places := m.mapPlaces(id, m.mapDir(id))
	neighbors := []any{}
	for _, p := range seen[:min(k, len(seen))] {
		neighbors = append(neighbors, neighborOf(p, best[p], places))
	}
	return jsonv.NewObject("file", rel, "neighbors", neighbors), nil
}
