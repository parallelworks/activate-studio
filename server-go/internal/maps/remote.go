package maps

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/search"
)

// A library on another system: a sample of its vectors fetched with one
// gufi_query (remoteMap.ts), its map made here in its owner's folder.

const (
	vectorsFile = "vectors.jsonl"
	recordFile  = "state.json"
	delim       = "\x1e"
	// RemoteTextFiles and RemoteTextChars are REMOTE_TEXT_FILES and
	// REMOTE_TEXT_CHARS: files whose text comes along, and how much.
	RemoteTextFiles = 10_000
	RemoteTextChars = 1000
	// HashRange is HASH_RANGE.
	HashRange = 2_147_483_648
	extModels = "%all-MiniLM-L6-v2"
	dirSQL    = "(SELECT rpath(sname, sroll) FROM vrsummary WHERE isroot = 1)"
	entrySQL  = "JOIN vrpentries e ON CAST(x.vinode AS INT64) = e.inode AND CAST(x.vmtime AS INT64) = e.mtime"
)

var (
	unknownOption = regexp.MustCompile(`(?i)unrecognized option|invalid option|unknown option`)
	noVec         = regexp.MustCompile(`no such (?:module: vec0|function: vec_)`)
	hex768        = regexp.MustCompile(`(?i)^[0-9A-F]{768}$`)
)

// remoteMapFiles is remoteMapFiles(): STUDIO_REMOTE_MAP_FILES, default 50,000.
func remoteMapFiles() int {
	n := jsNumber(os.Getenv("STUDIO_REMOTE_MAP_FILES"))
	if os.Getenv("STUDIO_REMOTE_MAP_FILES") != "" && n == math.Trunc(n) && n >= 3 {
		return int(math.Min(n, 1_000_000))
	}
	return 50_000
}

// FileHash is fileHash: the hash a file is sampled by, from its inode.
func FileHash(inode int64) int64 {
	b := new(big.Int).Mod(big.NewInt(inode), big.NewInt(2147483647))
	b.Mul(b, big.NewInt(1103515245)).Add(b, big.NewInt(12345)).Mod(b, big.NewInt(HashRange))
	return b.Int64()
}

func hashSQL(inode string) string {
	return fmt.Sprintf("((CAST(%s AS INTEGER) %% 2147483647) * 1103515245 + 12345) %% %d", inode, HashRange)
}

func clean(sql string) string {
	return "replace(replace(replace(" + sql + ", char(10), ' '), char(13), ' '), char(30), ' ')"
}

func lit(s string) string { return esc(s) }

// ModelCount is a model's files and passages there.
type ModelCount struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	Files  int64  `json:"files"`
	Chunks int64  `json:"chunks"`
	Digest int64  `json:"digest"`
}

// CountStages is countStages.
func CountStages(external *string) []string {
	cols := "s TEXT, m TEXT, f INT64, c INT64, d REAL"
	st := []string{"-I", "CREATE TABLE t (" + cols + ");", "-K", "CREATE TABLE agg (" + cols + ");"}
	if external != nil {
		st = append(st, "--external-copy", *external,
			"INSERT INTO t SELECT 'e', x.vector_model_name, count(DISTINCT e.inode), count(*), total(length(x.chunk_content)) FROM extdb.ext_vectors_by_file x "+entrySQL+" "+
				"WHERE x.vector_model_name LIKE "+lit(extModels)+" AND length(x.embedding) >= 1536 GROUP BY x.vector_model_name;")
	}
	return append(st,
		"-E", "INSERT INTO t SELECT 's', (SELECT model FROM gvecmodel LIMIT 1), count(DISTINCT g.fname), count(*), total(g.cid * (unicode(g.h) % 89 + 1)) "+
			"FROM gchunks g JOIN gvec_rowids r ON r.rowid = g.cid;",
		"-J", "INSERT INTO agg SELECT * FROM t;",
		"-G", "SELECT s, m, sum(f), sum(c), sum(d) FROM agg WHERE m IS NOT NULL GROUP BY s, m HAVING sum(f) > 0 ORDER BY 3 DESC, 2;",
	)
}

// FetchStages is fetchStages.
func FetchStages(model, source string, below, textBelow int64, external *string) ([]string, error) {
	cols := "h INT64, p TEXT, r REAL, v TEXT, x TEXT"
	text := func(h, x string) string {
		return fmt.Sprintf("CASE WHEN %s < %d THEN %s ELSE '' END", h, textBelow, clean(fmt.Sprintf("substr(%s, 1, %d)", x, RemoteTextChars)))
	}
	fold := func(key string) string {
		return "acc(" + key + ", rn, c, s, h, p, x) AS (SELECT " + key + ", rn, c, e, h, p, x FROM ch WHERE rn = 1 UNION ALL " +
			"SELECT ch." + key + ", ch.rn, ch.c, vec_add(acc.s, ch.e), acc.h, acc.p, acc.x FROM acc JOIN ch ON ch." + key + " = acc." + key + " AND ch.rn = acc.rn + 1) " +
			"SELECT h, p, round(vec_distance_l2(s, vec_sub(s, s)) / c, 5), hex(vec_quantize_int8(vec_normalize(s), 'unit')), " + text("h", "x") + " " +
			"FROM acc WHERE rn = c;"
	}
	st := []string{"-I", "CREATE TABLE t (" + cols + ");", "-K", "CREATE TABLE agg (" + cols + ");"}
	if source == "external" {
		if external == nil {
			return nil, apierr.New(409, "external databases are not read here (STUDIO_EXTERNAL_DB_PATTERN is empty)")
		}
		st = append(st, "--external-copy", *external,
			"INSERT INTO t WITH RECURSIVE ch AS (SELECT e.inode AS i, "+
				"row_number() OVER (PARTITION BY e.inode ORDER BY x.page_number) AS rn, count(*) OVER (PARTITION BY e.inode) AS c, substr(x.embedding, 1, 1536) AS e, "+hashSQL("e.inode")+" AS h, "+
				"rpath(e.sname, e.sroll)||'/'||e.name AS p, x.chunk_content AS x FROM extdb.ext_vectors_by_file x "+entrySQL+" "+
				"WHERE x.vector_model_name = "+lit(model)+" AND length(x.embedding) >= 1536 AND "+hashSQL("e.inode")+" < "+strconv.FormatInt(below, 10)+"), "+fold("i"),
			"-E", "SELECT 1 WHERE 0;")
	} else {
		st = append(st, "-E",
			"INSERT INTO t WITH RECURSIVE ch AS (SELECT g.fname AS f, "+
				"row_number() OVER (PARTITION BY g.fname ORDER BY g.seq, g.cid) AS rn, count(*) OVER (PARTITION BY g.fname) AS c, v.fp384 AS e, "+hashSQL("g.tinode")+" AS h, "+
				dirSQL+"||'/'||g.fname AS p, g.ctext AS x FROM gchunks g JOIN gvec v ON v.cid = g.cid "+
				"WHERE "+hashSQL("g.tinode")+" < "+strconv.FormatInt(below, 10)+" AND (SELECT model FROM gvecmodel LIMIT 1) = "+lit(model)+"), "+fold("f"))
	}
	return append(st, "-J", "INSERT INTO agg SELECT * FROM t;", "-G", "SELECT h, p, r, v, x FROM agg ORDER BY h, p;"), nil
}

// NeighborStages is neighborStages.
func NeighborStages(model, source, queryHex string, k int, external *string) ([]string, error) {
	keep := 4 * (k + 4)
	trim := fmt.Sprintf("DELETE FROM t WHERE rowid IN (SELECT rowid FROM t ORDER BY d LIMIT -1 OFFSET %d);", keep)
	st := []string{"-I", "CREATE TABLE q (qe BLOB); INSERT INTO q VALUES (X'" + queryHex + "'); CREATE TABLE t (p TEXT, d REAL);", "-K", "CREATE TABLE agg (p TEXT, d REAL);"}
	if source == "external" {
		if external == nil {
			return nil, apierr.New(409, "external databases are not read here (STUDIO_EXTERNAL_DB_PATTERN is empty)")
		}
		st = append(st, "--external-copy", *external,
			"INSERT INTO t SELECT rpath(e.sname, e.sroll)||'/'||e.name, sqrt(2.0 * vec_distance_cosine(substr(x.embedding, 1, 1536), (SELECT qe FROM q))) "+
				"FROM extdb.ext_vectors_by_file x "+entrySQL+" WHERE x.vector_model_name = "+lit(model)+" AND length(x.embedding) >= 1536 ORDER BY 2 LIMIT "+strconv.Itoa(k+4)+";",
			"-E", trim)
	} else {
		st = append(st, "-E", trim+" INSERT INTO t SELECT "+dirSQL+"||'/'||g.fname, n.distance FROM "+
			"(SELECT cid, distance FROM gvec WHERE fp384 MATCH (SELECT qe FROM q) ORDER BY distance LIMIT "+strconv.Itoa(k+4)+") n JOIN gchunks g ON g.cid = n.cid "+
			"WHERE (SELECT model FROM gvecmodel LIMIT 1) = "+lit(model)+";")
	}
	return append(st, "-J", "INSERT INTO agg SELECT * FROM t;", "-G", fmt.Sprintf("SELECT p, min(d) FROM agg GROUP BY p ORDER BY 2, 1 LIMIT %d;", k+1)), nil
}

type ran struct {
	code   int
	stdout string
	stderr string
	ms     int64
	bytes  int
	site   string
}

// remoteTarget is remoteTarget(root) with the owner's own key, marking a
// host that runs only gufi_query as direct (search.Engine.Target's rule).
func (m *Module) remoteTarget(root string) (*libraries.Target, error) {
	t, err := m.Reg.Users().Target(root, m.Keys, remote.SSHSite)
	if err != nil || t == nil {
		return t, err
	}
	if t.Def.Shell != nil && !*t.Def.Shell && m.Pool != nil {
		m.Pool.SetDirect(t.Site, true)
	}
	return t, nil
}

// runRemote is runRemote: gufi_query over a remote library's index, as its
// searches run it; a GUFI from before --no-print-sql-on-err or
// --external-copy is asked again without them.
func (m *Module) runRemote(ctx context.Context, root string, stages []string, timeout time.Duration) (ran, error) {
	t, err := m.remoteTarget(root)
	if err != nil {
		return ran{}, err
	}
	if t == nil {
		return ran{}, apierr.New(400, "not a library on another system")
	}
	threads := m.Pool.Threads(ctx, t.Site, t.Key)
	run := func(s []string, quiet bool) (ran, error) {
		cmd := []string{libraries.GufiTool(t.GufiBin, ""), "-n", strconv.Itoa(threads), "-d", delim, "-a", "1"}
		if quiet {
			cmd = append(cmd, "--no-print-sql-on-err")
		}
		cmd = append(append(cmd, s...), t.IndexRoot)
		t0 := time.Now()
		r, err := m.Eng.RemoteGufi(ctx, t.Site, t.Key, cmd, search.GufiOpts{Timeout: timeout, Source: "map"})
		if err != nil {
			return ran{}, err
		}
		return ran{r.Code, r.Stdout, r.Stderr, time.Since(t0).Milliseconds(), len(r.Raw), remote.SiteName(t.Site)}, nil
	}
	r, err := run(stages, true)
	if err != nil || r.code == 0 || !unknownOption.MatchString(r.stderr) {
		return r, err
	}
	if i := slices.Index(stages, "--external-copy"); i >= 0 {
		stages = append(append([]string{}, stages[:i]...), stages[i+3:]...)
	}
	return run(stages, false)
}

func rowsOf(stdout string, fields int) [][]string {
	var out [][]string
	for _, l := range strings.Split(stdout, "\n") {
		if l == "" {
			continue
		}
		if r := strings.Split(l, delim); len(r) >= fields {
			out = append(out, r)
		}
	}
	return out
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return ls[len(ls)-1]
}

// noRows is noRows: a walk that answered nothing.
func noRows(r ran, what string) error {
	if noVec.MatchString(r.stderr) {
		return apierr.New(502, "gufi_query on %s cannot read vectors: its GUFI was built without sqlite-vec", r.site)
	}
	if r.code != 0 && unknownOption.MatchString(r.stderr) {
		return apierr.New(502, "gufi_query on %s failed %s: %s", r.site, what, lastLine(r.stderr))
	}
	return nil
}

func numOr0(s string) float64 {
	f := jsNumber(s)
	if math.IsNaN(f) {
		return 0
	}
	return f
}

func (m *Module) countRemoteVectors(ctx context.Context, root string) ([]ModelCount, error) {
	r, err := m.runRemote(ctx, root, CountStages(search.ExternalPattern()), 10*time.Minute)
	if err != nil {
		return nil, err
	}
	var models []ModelCount
	for _, f := range rowsOf(r.stdout, 5) {
		src := "studio"
		if f[0] == "e" {
			src = "external"
		}
		mc := ModelCount{Model: f[1], Source: src, Files: int64(numOr0(f[2])), Chunks: int64(numOr0(f[3])), Digest: int64(jsRound(numOr0(f[4])))}
		if mc.Model != "" && mc.Files > 0 {
			models = append(models, mc)
		}
	}
	if len(models) == 0 {
		if e := noRows(r, "counting the vectors"); e != nil {
			return nil, e
		}
	}
	return models, nil
}

type fetchedFile struct {
	H int64
	P string
	R float64
	V string
	T string
}

func relativeTo(printed, root string) (string, bool) {
	base := strings.TrimRight(root, "/")
	if strings.HasPrefix(printed, base+"/") {
		return printed[len(base)+1:], true
	}
	return "", false
}

func (m *Module) fetchRemoteVectors(ctx context.Context, root string, mc ModelCount, capN int) ([]fetchedFile, int, int64, error) {
	want := float64(capN)*1.05 + 4*math.Sqrt(float64(capN)) + 4
	share := func(n float64) int64 {
		return int64(math.Min(HashRange, math.Ceil(HashRange*math.Min(1, n/math.Max(1, float64(mc.Files))))))
	}
	below := int64(HashRange)
	if mc.Files > int64(capN) {
		below = share(want)
	}
	textBelow := min(below, share(RemoteTextFiles*1.05))
	stages, err := FetchStages(mc.Model, mc.Source, below, textBelow, search.ExternalPattern())
	if err != nil {
		return nil, 0, 0, err
	}
	r, err := m.runRemote(ctx, root, stages, 20*time.Minute)
	if err != nil {
		return nil, 0, 0, err
	}
	t, _ := m.remoteTarget(root)
	top := ""
	if t != nil {
		top = t.IndexRoot
	}
	seen := map[string]bool{}
	var files []fetchedFile
	for _, f := range rowsOf(r.stdout, 5) {
		rel, ok := relativeTo(f[1], top)
		if !ok || seen[rel] || !hex768.MatchString(f[3]) {
			continue
		}
		seen[rel] = true
		files = append(files, fetchedFile{int64(numOr0(f[0])), rel, numOr0(f[2]), f[3], f[4]})
	}
	if len(files) == 0 {
		if e := noRows(r, "fetching the vectors"); e != nil {
			return nil, 0, 0, e
		}
		tail := ""
		if s := strings.TrimSpace(r.stderr); s != "" {
			tail = ": " + lastLine(s)
		}
		return nil, 0, 0, apierr.New(502, "gufi_query on %s returned no vectors%s", r.site, tail)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].H != files[j].H {
			return files[i].H < files[j].H
		}
		return files[i].P < files[j].P
	})
	if len(files) > capN {
		files = files[:capN]
	}
	return files, r.bytes, r.ms, nil
}

// Dequantize is dequantize: a file's vector put back from its row.
func Dequantize(h string, scale float64) []float32 {
	b, _ := hex.DecodeString(h[:len(h)/2*2])
	out := make([]float32, len(b))
	norm := 0.0
	for i, c := range b {
		q := float64(int8(c))
		sign := 0.0
		if q > 0 {
			sign = 1
		} else if q < 0 {
			sign = -1
		}
		// Node sums the norm over the doubles, before the Float32Array
		// rounds them.
		x := (q+sign*0.5+128)/127.5 - 1
		out[i] = float32(x)
		norm += x * x
	}
	f := 0.0
	if norm > 0 {
		f = scale / math.Sqrt(norm)
	}
	for i := range out {
		out[i] = float32(float64(out[i]) * f)
	}
	return out
}

func (m *Module) remoteNeighborsOf(ctx context.Context, root, model, source string, query []float32, k int) ([][2]any, error) {
	b := make([]byte, 4*len(query))
	for i, x := range query {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	stages, err := NeighborStages(model, source, hex.EncodeToString(b), k, search.ExternalPattern())
	if err != nil {
		return nil, err
	}
	r, err := m.runRemote(ctx, root, stages, 120*time.Second)
	if err != nil {
		return nil, err
	}
	t, _ := m.remoteTarget(root)
	top := ""
	if t != nil {
		top = t.IndexRoot
	}
	var out [][2]any
	for _, f := range rowsOf(r.stdout, 2) {
		p, ok := relativeTo(f[0], top)
		d := jsNumber(f[1])
		if ok && !math.IsNaN(d) && !math.IsInf(d, 0) {
			out = append(out, [2]any{p, d})
		}
	}
	if len(out) == 0 {
		if e := noRows(r, "looking for the nearest files"); e != nil {
			return nil, e
		}
	}
	return out, nil
}

// ---- the record kept beside a remote library's map ----

func readRecord(home string) *jsonv.Object {
	b, err := os.ReadFile(filepath.Join(home, recordFile))
	if err != nil {
		return jsonv.NewObject()
	}
	v, err := jsonv.Decode(b)
	if o := jsonv.Obj(v); err == nil && o != nil {
		return o
	}
	return jsonv.NewObject()
}

func writeRecord(home string, rec *jsonv.Object) error {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	b, err := jsonv.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, recordFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, recordFile))
}

func isoMs(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func nowMs() float64 { return float64(time.Now().UnixMilli()) }

func (m *Module) remoteOverview(lib *jsonv.Object, t Target, kick bool) *jsonv.Object {
	// Not built yet: its own build says how far it has got.
	if _, err := m.remoteTarget(t.Remote.Root); err != nil {
		return jsonv.NewObject("available", false, "reason", "none", "building", false, "library", lib)
	}
	rec := readRecord(t.Remote.Home)
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.stateOf(t.Key)
	busy := st.running || st.timer != nil
	var checkedAt any
	ck, hasCk := jsonv.Num(get(rec, "checkedAt"))
	if hasCk && ck != 0 {
		checkedAt = isoMs(ck)
	}
	fetchedAt := get(rec, "fetchedAt")
	rinfo := jsonv.NewObject("host", jsonv.String(get(lib, "host")), "fetchedAt", fetchedAt, "checkedAt", checkedAt)
	if rec.Has("bytes") {
		rinfo.Set("bytes", get(rec, "bytes")).Set("fetchMs", get(rec, "fetchMs"))
	}
	if rec.Has("models") {
		rinfo.Set("models", get(rec, "models"))
	}
	due := nowMs()-ck >= float64(m.RemoteCheck.Milliseconds())
	empty := jsonv.Truthy(get(rec, "empty"))
	if hit := readJSON(filepath.Join(t.Out, "overview.json"), st.overview); hit != nil && jsonv.Truthy(get(hit.data, "available")) {
		st.overview = hit
		// Looked at: a check, at most hourly, whether its vectors changed there.
		if kick && due && !busy {
			m.scheduleRemoteLocked(t, false, 0)
		}
		return hit.data.Clone().Set("building", st.running || st.timer != nil).Set("progress", progressOf(st)).
			Set("error", errOrNil(st.err)).Set("library", lib).Set("remote", rinfo)
	}
	if kick && !busy && (!st.firstTried || (empty && due)) {
		st.firstTried = true
		m.scheduleRemoteLocked(t, true, 0)
	}
	now := st.running || st.timer != nil
	reason := "none"
	switch {
	case now:
	case st.err != nil:
		reason = "failed"
	case empty:
		reason = "remote-none"
	}
	out := jsonv.NewObject("available", false, "reason", reason, "building", now, "progress", progressOf(st), "error", errOrNil(st.err), "library", lib, "remote", rinfo)
	if of, ok := jsonv.Num(get(rec, "of")); empty && ok && of != 0 {
		out.Set("of", of)
	}
	return out
}

func progressOf(st *state) any {
	if st.progress == nil {
		return nil
	}
	return st.progress.Clone()
}

// scheduleRemote is scheduleRemoteMap: a remote library's map checked
// (and fetched and made again if its vectors changed there), or with
// force fetched and made again regardless, in delay. One runs at a time.
func (m *Module) scheduleRemote(t Target, force bool, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scheduleRemoteLocked(t, force, delay)
}

func (m *Module) scheduleRemoteLocked(t Target, force bool, delay time.Duration) {
	st := m.stateOf(t.Key)
	tc := t
	st.target = &tc
	st.force = st.force || force
	if st.running {
		st.again = true
		return
	}
	if st.timer != nil {
		return
	}
	var tm *time.Timer
	tm = time.AfterFunc(time.Duration(float64(delay)*m.DelayScale), func() {
		m.mu.Lock()
		if st.timer == tm {
			st.timer = nil
		}
		m.mu.Unlock()
		m.refreshRemote(t.Key)
	})
	st.timer = tm
}

func (m *Module) refreshRemote(key string) {
	m.mu.Lock()
	st := m.stateOf(key)
	t := st.target
	if t == nil || t.Remote == nil || st.running {
		m.mu.Unlock()
		return
	}
	force := st.force
	st.force = false
	st.running = true
	st.progress = jsonv.NewObject("phase", "counting")
	m.mu.Unlock()
	err := m.refreshRemoteNow(*t, force, st)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		msg := cut(errText(err), 400)
		st.err = &msg
		m.Log.Warn("semantic map ("+t.ID+"): "+msg, "library", t.ID)
	} else {
		st.err = nil
	}
	st.running = false
	st.progress = nil
	st.lastEnd = time.Now()
	if st.again {
		st.again = false
		m.scheduleRemoteLocked(*t, false, 0)
	}
}

func errText(err error) string {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

func (m *Module) setProgress(st *state, p *jsonv.Object) {
	m.mu.Lock()
	st.progress = p
	m.mu.Unlock()
}

func (m *Module) refreshRemoteNow(t Target, force bool, st *state) error {
	ctx := background()
	root, home := t.Remote.Root, t.Remote.Home
	models, err := m.countRemoteVectors(ctx, root)
	if err != nil {
		return err
	}
	was := readRecord(home)
	checkedAt := nowMs()
	fpb, _ := json.Marshal(models)
	if models == nil {
		fpb = []byte("[]")
	}
	fingerprint := string(fpb)
	found := []any{}
	for _, mc := range models {
		found = append(found, jsonv.NewObject("model", mc.Model, "files", float64(mc.Files)))
	}
	if len(models) == 0 {
		// No vectors there (any longer): no map.
		os.RemoveAll(t.Out)
		os.Remove(filepath.Join(home, vectorsFile))
		return writeRecord(home, jsonv.NewObject("fingerprint", fingerprint, "models", found, "checkedAt", checkedAt, "empty", true))
	}
	best := models[0]
	if !force && jsonv.String(get(was, "fingerprint")) == fingerprint && exists(filepath.Join(t.Out, "overview.json")) {
		rec := was.Clone().Set("models", found).Set("checkedAt", checkedAt)
		return writeRecord(home, rec)
	}
	capN := m.RemoteFiles()
	m.setProgress(st, jsonv.NewObject("phase", "fetching", "files", float64(min(int64(capN), best.Files)), "of", float64(best.Files)))
	files, nbytes, ms, err := m.fetchRemoteVectors(ctx, root, best, capN)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	vectors := filepath.Join(home, vectorsFile)
	var sb strings.Builder
	for _, f := range files {
		row := jsonv.NewObject("p", f.P, "r", f.R, "v", f.V)
		if f.T != "" {
			row.Set("t", f.T)
		}
		b, _ := jsonv.Marshal(row)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(vectors+".tmp", []byte(sb.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(vectors+".tmp", vectors); err != nil {
		return err
	}
	m.setProgress(st, jsonv.NewObject("phase", "drawing", "files", float64(len(files)), "of", float64(best.Files)))
	bt := t
	rt := *t.Remote
	rt.Vectors, rt.Model, rt.Of = vectors, best.Model, int(best.Files)
	bt.Remote = &rt
	if err := m.build(bt); err != nil {
		return err
	}
	if !exists(filepath.Join(t.Out, "overview.json")) {
		// Too few files to place (fewer than three): none, with how many.
		return writeRecord(home, jsonv.NewObject("fingerprint", fingerprint, "models", found, "model", best.Model, "of", float64(best.Files), "checkedAt", checkedAt, "empty", true))
	}
	m.Log.Info(fmt.Sprintf("semantic map (%s): %d of %d files fetched, %d bytes in %d ms", t.ID, len(files), best.Files, nbytes, ms), "library", t.ID)
	return writeRecord(home, jsonv.NewObject("fingerprint", fingerprint, "models", found, "model", best.Model, "source", best.Source,
		"files", float64(len(files)), "of", float64(best.Files), "bytes", float64(nbytes), "fetchMs", float64(ms),
		"fetchedAt", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "checkedAt", checkedAt))
}

// fetchedRow is a fetched file's row, found by its path at the start of its line.
func fetchedRow(file, rel string) *jsonv.Object {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	text := string(b)
	pj, _ := jsonv.Marshal(rel)
	head := `{"p":` + string(pj) + `,`
	at := 0
	if !strings.HasPrefix(text, head) {
		i := strings.Index(text, "\n"+head)
		if i < 0 {
			return nil
		}
		at = i + 1
	}
	end := strings.IndexByte(text[at:], '\n')
	line := text[at:]
	if end >= 0 {
		line = text[at : at+end]
	}
	v, err := jsonv.DecodeString(line)
	if err != nil {
		return nil
	}
	return jsonv.Obj(v)
}

func (m *Module) remoteNeighbors(ctx context.Context, t Target, rel string, k int) (*jsonv.Object, error) {
	rec := readRecord(t.Remote.Home)
	model := jsonv.String(get(rec, "model"))
	none := jsonv.NewObject("file", rel, "neighbors", []any{}, "reason", "none")
	if model == "" {
		return none, nil
	}
	row := fetchedRow(filepath.Join(t.Remote.Home, vectorsFile), rel)
	if row == nil {
		return none, nil
	}
	source := jsonv.String(get(rec, "source"))
	if source == "" {
		source = "studio"
	}
	found, err := m.remoteNeighborsOf(ctx, t.Remote.Root, model, source, Dequantize(jsonv.String(get(row, "v")), 1), k)
	if err != nil {
		return nil, err
	}
	places := m.mapPlaces(t.Key, t.Out)
	neighbors := []any{}
	for _, f := range found {
		if f[0].(string) == rel {
			continue
		}
		if len(neighbors) == k {
			break
		}
		neighbors = append(neighbors, neighborOf(f[0].(string), f[1].(float64), places))
	}
	return jsonv.NewObject("file", rel, "neighbors", neighbors), nil
}
