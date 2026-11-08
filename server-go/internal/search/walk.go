package search

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

func marshalJS(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// ONNXModelID is the id the Studio's ONNX model marks its vectors with.
const ONNXModelID = "minilm-l6-mean-int8"

// IsONNXModel is isOnnxModel.
func IsONNXModel(id string) bool { return id == ONNXModelID || strings.HasPrefix(id, ONNXModelID+"-t") }

const legacyModel = "minilm384"

// QueryVectors is queryVectors: the query's embedding for the walk, for
// the Studio's model and the external databases that name it.
func (e *Engine) QueryVectors(ctx context.Context, text string) []gufi.QueryVector {
	if e.Embed == nil || !e.Embed.Present() {
		return nil
	}
	vecs, err := e.Embed.Embed(ctx, []string{jsSlice(text, 500)})
	if err != nil {
		e.Log.Warn("search by meaning: " + err.Error())
		return nil
	}
	if len(vecs) == 0 || len(vecs[0]) != 384*4 {
		return nil
	}
	h := hex.EncodeToString(vecs[0])
	return []gufi.QueryVector{{Pattern: ONNXModelID, Hex: h}, {Pattern: ONNXModelID + "-t%", Hex: h}, {Pattern: "%all-MiniLM-L6-v2", Hex: h}}
}

// jsSlice is s.slice(0, n) in UTF-16 units.
func jsSlice(s string, n int) string {
	u := utf16Units(s)
	if len(u) <= n {
		return s
	}
	return utf16String(u[:n])
}

// WalkSearch is walkSearch: one walk over one or more index roots on one
// machine, local roots in one gufi_query, a remote host's in one command.
func (e *Engine) WalkSearch(ctx context.Context, text string, lim gufi.WalkLimits, roots []string, vectors []gufi.QueryVector, refine *gufi.RefineSpec) ([]gufi.WalkRow, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	isRemote := strings.HasPrefix(roots[0], libraries.RemotePrefix)
	printed := make([]string, len(roots))
	for i, r := range roots {
		if isRemote {
			p, err := e.RemoteIndexRoot(r)
			if err != nil {
				return nil, err
			}
			printed[i] = p
		} else {
			printed[i], _ = filepath.Abs(r)
		}
	}
	stages := gufi.OnePassStages(text, lim, printed, vectors, refine)
	if stages == nil {
		return nil, nil
	}
	exec := func(current bool) (string, error) {
		s := stages
		if !current {
			if i := slices.Index(s, "--external-copy"); i >= 0 {
				s = append(append([]string{}, s[:i]...), s[i+3:]...)
			}
		}
		args := []string{"-n", strconv.Itoa(localThreads()), "-d", Delim, "-a", "1"}
		if current {
			args = append(args, "--no-print-sql-on-err")
		}
		args = append(args, s...)
		if isRemote {
			return e.RemoteQueryMany(ctx, roots, args)
		}
		return e.runLocal(ctx, append(args, printed...), 60*time.Second)
	}
	out, err := exec(true)
	var uo *errUnknownOption
	if errors.As(err, &uo) {
		out, err = exec(false)
	}
	if err != nil {
		return nil, err
	}
	return gufi.WalkRows(out, printed, roots), nil
}

// SearchWordsAndNames is searchWordsAndNames: full-text and name matches of
// one library in one walk (the gufi_vt search setting's path).
func (e *Engine) SearchWordsAndNames(ctx context.Context, text string, ftsLimit, nameLimit int, indexRoot string) (fts, names []gufi.SearchHit, err error) {
	parsed := gufi.ParseSearchQuery(text)
	expr := ""
	if ftsLimit > 0 {
		expr = gufi.FtsExprFor(parsed)
	}
	where := "0"
	if nameLimit > 0 {
		where = gufi.NameWhere(parsed)
	}
	words, nm := expr != "", where != "0"
	if !words && !nm {
		return nil, nil, nil
	}
	ftsTotal, nameTotal := ftsLimit*3, nameLimit
	ftsCols, nameCols := "r REAL, p TEXT, n TEXT, s INT64, m INT64, x TEXT", "r REAL, p TEXT, n TEXT, s INT64, m INT64"
	trim := func(t string, keep int) string {
		return fmt.Sprintf("DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s ORDER BY r, p LIMIT -1 OFFSET %d);", t, t, keep)
	}
	var I, K, E, J, G []string
	if nm {
		I = append(I, "CREATE TABLE tn ("+nameCols+");")
		K = append(K, "CREATE TABLE an ("+nameCols+");")
		E = append(E, fmt.Sprintf("INSERT INTO tn SELECT %s, rpath(sname, sroll)||'/'||name, name, size, mtime FROM vrpentries WHERE %s ORDER BY 1, 2 LIMIT %d;", gufi.NameRank(parsed), where, nameLimit), trim("tn", nameTotal))
		J = append(J, "INSERT INTO an SELECT * FROM tn;")
		G = append(G, fmt.Sprintf("SELECT 'n', r, p, n, s, m, '' FROM (SELECT * FROM an ORDER BY r, p LIMIT %d)", nameTotal))
	}
	if words {
		I = append(I, "CREATE TABLE tf ("+ftsCols+");")
		K = append(K, "CREATE TABLE af ("+ftsCols+");")
		E = append(E, fmt.Sprintf("INSERT INTO tf SELECT bm25(words), %s %s ORDER BY 1, 2 LIMIT %d;", gufi.FTSColumns, gufi.FTSFrom(expr), ftsLimit), trim("tf", ftsTotal))
		J = append(J, "INSERT INTO af SELECT * FROM tf;")
		G = append(G, fmt.Sprintf("SELECT 'f', r, p, n, s, m, x FROM (SELECT * FROM af ORDER BY r, p LIMIT %d)", ftsTotal))
	}
	rows, err := e.QueryAggregate(ctx, []string{"-I", strings.Join(I, " "), "-K", strings.Join(K, " "), "-E", strings.Join(E, " "), "-J", strings.Join(J, " "), "-G", strings.Join(G, " UNION ALL ") + ";"}, indexRoot, "")
	if err != nil {
		return nil, nil, err
	}
	num := func(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }
	for _, r := range rows {
		if len(r) < 7 {
			continue
		}
		rank := num(r[1])
		row := r[2:]
		h := gufi.SearchHit{Path: e.toKbRel(row[0], indexRoot), Name: row[1], Size: num(row[2]), Mtime: num(row[3]), Rank: &rank}
		if r[0] == "f" {
			h.Snippet, h.Score, h.Mode = gufi.WithoutProvenance(row[4]), 1, gufi.KindFTS
			fts = append(fts, h)
		} else if r[0] == "n" {
			h.Score, h.Mode = 0.5, gufi.KindName
			names = append(names, h)
		}
	}
	byRank := func(a []gufi.SearchHit) {
		sort.SliceStable(a, func(i, j int) bool {
			if *a[i].Rank != *a[j].Rank {
				return *a[i].Rank < *a[j].Rank
			}
			return jsLess(a[i].Path, a[j].Path)
		})
	}
	byRank(fts)
	byRank(names)
	if len(fts) > ftsTotal {
		fts = fts[:ftsTotal]
	}
	if len(names) > nameTotal {
		names = names[:nameTotal]
	}
	return fts, names, nil
}

// ---- search by meaning without the walk ----

type vecDB struct{ db, model string }

type dbCache struct {
	mu   sync.Mutex
	dbs  map[string]cachedList
	vecs map[string]cachedVecs
}

type cachedList struct {
	at  time.Time
	dbs []string
}
type cachedVecs struct {
	at  time.Time
	dbs []vecDB
}

var caches = &dbCache{dbs: map[string]cachedList{}, vecs: map[string]cachedVecs{}}

// InvalidateDbList is invalidateDbList.
func InvalidateDbList() {
	caches.mu.Lock()
	caches.dbs, caches.vecs = map[string]cachedList{}, map[string]cachedVecs{}
	caches.mu.Unlock()
}

func listIndexDbs(root string) []string {
	caches.mu.Lock()
	if h, ok := caches.dbs[root]; ok && time.Since(h.at) < time.Minute {
		caches.mu.Unlock()
		return h.dbs
	}
	caches.mu.Unlock()
	var dbs []string
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, x := range entries {
			if x.Name() == "db.db" {
				dbs = append(dbs, filepath.Join(dir, "db.db"))
				break
			}
		}
		for _, x := range entries {
			if x.IsDir() {
				walk(filepath.Join(dir, x.Name()))
			}
		}
	}
	walk(root)
	caches.mu.Lock()
	caches.dbs[root] = cachedList{time.Now(), dbs}
	caches.mu.Unlock()
	return dbs
}

func (e *Engine) sqliteScript(ctx context.Context, lines []string) (string, error) {
	script := strings.Join(lines, "\n")
	t0 := time.Now()
	res, err := e.Local.Run(ctx, gufi.Command{Tool: gufi.ToolSQLite3, Stdin: []byte(script), Timeout: 60 * time.Second})
	o := gufitrace.Run{Start: t0, Elapsed: res.Elapsed, Engine: "gufi_sqlite3", Script: &script, Hide: true}
	c := res.ExitCode
	o.Code = &c
	se := res.Stderr
	o.Stderr = &se
	gufitrace.Note(ctx, []string{filepath.Join(e.GufiBin, "gufi_sqlite3")}, o)
	if err != nil && len(res.Stdout) == 0 {
		return "", err
	}
	if res.ExitCode != 0 && len(res.Stdout) == 0 {
		return "", fmt.Errorf("gufi_sqlite3: exit %d: %s", res.ExitCode, lastLine(res.Stderr))
	}
	return string(res.Stdout), nil
}

func (e *Engine) listVectorDbs(ctx context.Context, root string) []vecDB {
	caches.mu.Lock()
	if h, ok := caches.vecs[root]; ok && time.Since(h.at) < time.Minute {
		caches.mu.Unlock()
		return h.dbs
	}
	caches.mu.Unlock()
	all := listIndexDbs(root)
	if len(all) == 0 {
		return nil
	}
	var lines []string
	for _, db := range all {
		lines = append(lines, "ATTACH "+q(db)+" AS c;",
			"SELECT 'HASVEC' || "+q(Delim)+" || "+q(db)+" || "+q(Delim)+" || name FROM c.sqlite_master WHERE type IN ('table','view') AND name IN ('gvec', 'gvecmodel');",
			"DETACH c;")
	}
	names, _ := e.sqliteScript(ctx, lines)
	found := map[string]map[string]bool{}
	var order []string
	for _, l := range strings.Split(names, "\n") {
		p := strings.Split(l, Delim)
		if len(p) < 3 || p[0] != "HASVEC" || p[1] == "" || p[2] == "" {
			continue
		}
		if found[p[1]] == nil {
			found[p[1]] = map[string]bool{}
			order = append(order, p[1])
		}
		found[p[1]][p[2]] = true
	}
	models := map[string]string{}
	var withModel []string
	for _, db := range order {
		if found[db]["gvec"] && found[db]["gvecmodel"] {
			withModel = append(withModel, db)
		}
	}
	if len(withModel) > 0 {
		var ml []string
		for _, db := range withModel {
			ml = append(ml, "ATTACH "+q(db)+" AS c;", "SELECT 'VMODEL' || "+q(Delim)+" || "+q(db)+" || "+q(Delim)+" || model FROM c.gvecmodel LIMIT 1;", "DETACH c;")
		}
		out, _ := e.sqliteScript(ctx, ml)
		for _, l := range strings.Split(out, "\n") {
			p := strings.Split(l, Delim)
			if len(p) >= 3 && p[0] == "VMODEL" && p[1] != "" && p[2] != "" {
				models[p[1]] = strings.TrimSpace(p[2])
			}
		}
	}
	var dbs []vecDB
	for _, db := range order {
		if !found[db]["gvec"] {
			continue
		}
		m := models[db]
		if m == "" {
			m = legacyModel
		}
		dbs = append(dbs, vecDB{db, m})
	}
	caches.mu.Lock()
	caches.vecs[root] = cachedVecs{time.Now(), dbs}
	caches.mu.Unlock()
	return dbs
}

func (e *Engine) embedModelFile() string { return filepath.Join(e.IndexBase, "models", "minilm384.gguf") }

// vectorsAvailable is vectorsAvailable.
func (e *Engine) vectorsAvailable() bool {
	_, err := os.Stat(e.embedModelFile())
	model := err == nil || (e.Embed != nil && e.Embed.Present())
	_, err = os.Stat(e.GufiIndex)
	return model && e.gufiInstalled() && err == nil
}

// SearchVector is searchVector: nearest passages per directory database,
// for an index the walk could not carry meaning for.
func (e *Engine) SearchVector(ctx context.Context, text string, limit int, indexRoot string) ([]gufi.SearchHit, error) {
	if indexRoot == "" {
		indexRoot = e.GufiIndex
	}
	if !e.vectorsAvailable() {
		return nil, nil
	}
	idx, _ := filepath.Abs(indexRoot)
	vecDbs := e.listVectorDbs(ctx, indexRoot)
	if len(vecDbs) == 0 {
		return nil, nil
	}
	var script []string
	tableFor := map[string]string{}
	if slices.ContainsFunc(vecDbs, func(v vecDB) bool { return v.model == legacyModel }) {
		if _, err := os.Stat(e.embedModelFile()); err == nil {
			script = append(script,
				"INSERT INTO temp.lembed_models(name, model) SELECT 'minilm384', lembed_model_from_file("+q(e.embedModelFile())+");",
				"CREATE TABLE temp.qv AS SELECT lembed('minilm384', "+q(jsSlice(text, 500))+") AS qe;")
			tableFor[legacyModel] = "temp.qv"
		}
	}
	var onnx []string
	for _, v := range vecDbs {
		if IsONNXModel(v.model) && !slices.Contains(onnx, v.model) {
			onnx = append(onnx, v.model)
		}
	}
	if len(onnx) > 0 && e.Embed != nil {
		vecs, err := e.Embed.Embed(ctx, []string{jsSlice(text, 500)})
		if err != nil {
			e.Log.Warn("search by meaning: " + err.Error())
		} else if len(vecs) > 0 && len(vecs[0]) == 384*4 {
			script = append(script, "CREATE TABLE temp.qo AS SELECT X'"+hex.EncodeToString(vecs[0])+"' AS qe;")
			for _, m := range onnx {
				tableFor[m] = "temp.qo"
			}
		}
	}
	any := false
	for _, v := range vecDbs {
		t, ok := tableFor[v.model]
		if !ok {
			continue
		}
		any = true
		rel, _ := filepath.Rel(idx, filepath.Dir(v.db))
		if rel == "." {
			rel = ""
		}
		script = append(script, "ATTACH "+q(v.db)+" AS d;",
			"SELECT 'VROW' || "+q(Delim)+" || "+q(rel)+" || "+q(Delim)+" || g.fname || "+q(Delim)+" || k.distance || "+q(Delim)+" || "+
				gufi.ChunkSnippet()+" "+
				"FROM (SELECT cid, distance FROM d.gvec WHERE fp384 MATCH (SELECT qe FROM "+t+") ORDER BY distance ASC LIMIT 3) k "+
				"JOIN d.gchunks g ON g.cid = k.cid;",
			"DETACH d;")
	}
	if !any {
		return nil, nil
	}
	out, err := e.sqliteScript(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("gufi_sqlite3: %s", err.Error())
	}
	var hits []gufi.SearchHit
	for _, line := range strings.Split(out, "\n") {
		p := strings.Split(line, Delim)
		if p[0] != "VROW" || len(p) < 5 {
			continue
		}
		dist, err := strconv.ParseFloat(strings.TrimSpace(p[3]), 64)
		if err != nil {
			dist = 999
		}
		pth := p[2]
		if p[1] != "." && p[1] != "" {
			pth = p[1] + "/" + p[2]
		}
		hits = append(hits, gufi.SearchHit{Path: pth, Name: p[2], Snippet: gufi.WithoutProvenance(gufi.PassageSnippet(strings.Join(p[4:], " "))), Score: dist, Mode: gufi.KindVector})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score < hits[j].Score })
	seen := map[string]bool{}
	var outHits []gufi.SearchHit
	for _, h := range hits {
		if seen[h.Path] {
			continue
		}
		seen[h.Path] = true
		outHits = append(outHits, h)
		if len(outHits) == limit {
			break
		}
	}
	return outHits, nil
}
