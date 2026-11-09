package embed

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// The meaning phase: indexer/embed.py in Go. Each directory's words (the
// fts5 table the text phase writes) are cut into passages in a gchunks
// table, the passages the cache does not hold are embedded, and each
// directory's gvec vec0 table is written from the cache by gufi_sqlite3
// (which carries sqlite-vec) with the same statements embed.py sends it,
// beside a gvecmodel table naming the model.

const (
	chunkChars    = 1000
	chunkOverlap  = 120
	MaxChunks     = 12
	embedCap      = 800
	embedCapShort = 400
	lembedBatch   = 64
	onnxTask      = 256
	// LembedModel is the GGUF model's id, through sqlite-lembed in
	// gufi_sqlite3: the fallback where the ONNX backend cannot run.
	LembedModel = "minilm384"
)

var noEmbedSuffixes = []string{".svg", ".xml", ".html", ".css", ".json", ".csv", ".tsv"}

var documentSuffixes = []string{
	".pdf", ".doc", ".docx", ".odt", ".rtf", ".ppt", ".pptx", ".odp",
	".md", ".markdown", ".txt", ".rst", ".tex", ".org", ".adoc",
	".png", ".jpg", ".jpeg", ".gif", ".webp", ".tif", ".tiff",
	".emlx", ".eml",
}

const provenancePrefix = "<!-- extracted by downmark "

// PassOptions are embed.py's arguments.
type PassOptions struct {
	Index string
	// Model is the GGUF model, for the lembed backend.
	Model string
	// ONNX is the folder with model.onnx and tokenizer.json; minilm-l6
	// beside Model when empty.
	ONNX string
	// Backend is auto, onnx or lembed.
	Backend     string
	MaxTokens   int
	GufiSqlite3 string
	Subdir      string
	NoRecurse   bool
	Jobs        int
	Cache       string
	Prune       bool
	MaxChunks   int
	// DocumentsOnly embeds documents and images only.
	DocumentsOnly bool
	// Scratch is where the pass keeps its scratch files (STUDIO_TMP).
	Scratch string
	// Out gets the lines embed.py prints on stdout; Err those on stderr.
	Out, Err io.Writer
	// Progress gets embed.py's @progress counts.
	Progress func(step string, done, total int)
	// Library is the ONNX Runtime library (FindLibrary() when empty).
	Library string
}

// PassResult is what the pass did (embed.py's --report, and more).
type PassResult struct {
	Model   string `json:"model"`
	Chunks  int    `json:"chunks"`
	Errors  int    `json:"errors"`
	First   string `json:"firstError"`
	Pending int    `json:"-"`
	Tables  int    `json:"-"`
}

func prefixOK(name string, suffixes []string) bool {
	low := strings.ToLower(name)
	for _, s := range suffixes {
		if strings.HasSuffix(low, s) {
			return true
		}
	}
	return false
}

// EmbedsFile is embeds_file: never markup, and with documentsOnly only
// documents and images.
func EmbedsFile(name string, documentsOnly bool) bool {
	if prefixOK(name, noEmbedSuffixes) {
		return false
	}
	return !documentsOnly || prefixOK(name, documentSuffixes)
}

func indexText(text string) string {
	if strings.HasPrefix(text, provenancePrefix) {
		nl := strings.IndexByte(text, '\n')
		if nl < 0 {
			return ""
		}
		return strings.TrimLeft(text[nl+1:], "\n")
	}
	return text
}

func proseLike(c []rune) bool {
	n := 0
	for _, ch := range c {
		if unicode.IsLetter(ch) || pytext.IsSpace(ch) {
			n++
		}
	}
	return float64(n)/float64(max(len(c), 1)) > 0.6
}

// Chunk is chunk(): windows of 1,000 characters stepping by 880, those
// that read as prose, at most limit of them.
func Chunk(text string, limit int) []string {
	r := []rune(text)
	var out []string
	for i := 0; i < len(r) && len(out) < limit; i += chunkChars - chunkOverlap {
		c := r[i:min(i+chunkChars, len(r))]
		if proseLike(c) {
			out = append(out, string(c))
		}
	}
	return out
}

func headRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// ChunkHash is the cache key: the model and the text that is embedded.
func ChunkHash(model, text string) string {
	h := sha1.Sum([]byte(model + "\x00" + headRunes(text, embedCap)))
	return hex.EncodeToString(h[:])
}

func sqlStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

type passProgress struct {
	mu          sync.Mutex
	fn          func(step string, done, total int)
	step        string
	done, total int
	last        time.Time
}

func (p *passProgress) emit(force bool) {
	if p.fn == nil {
		return
	}
	if now := time.Now(); force || now.Sub(p.last) >= 250*time.Millisecond {
		p.last = now
		p.fn(p.step, p.done, p.total)
	}
}

func (p *passProgress) start(step string, total int) {
	p.mu.Lock()
	p.step, p.total, p.done = step, total, 0
	p.emit(true)
	p.mu.Unlock()
}

func (p *passProgress) add(n int) {
	p.mu.Lock()
	p.done += n
	p.emit(false)
	p.mu.Unlock()
}

func openDB(file string, readOnly bool) (*sql.DB, error) {
	dsn := "file:" + file + "?_pragma=busy_timeout(60000)"
	if readOnly {
		dsn += "&mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// OpenCache opens (making it) the embedding cache.
func OpenCache(file string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+file+"?_pragma=busy_timeout(120000)&_pragma=synchronous(OFF)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS emb (h TEXT PRIMARY KEY, v BLOB) WITHOUT ROWID"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// TreeModels is how many directories under an index hold vectors of each
// model (a directory with gvec and no gvecmodel holds minilm384).
func TreeModels(root string) map[string]int {
	counts := map[string]int{}
	for _, f := range dbFiles(root, false) {
		db, err := openDB(f, true)
		if err != nil {
			continue
		}
		func() {
			defer db.Close()
			names := map[string]bool{}
			rows, err := db.Query("SELECT name FROM sqlite_master WHERE name IN ('gvec', 'gvecmodel')")
			if err != nil {
				return
			}
			for rows.Next() {
				var n string
				rows.Scan(&n)
				names[n] = true
			}
			rows.Close()
			if !names["gvec"] {
				return
			}
			model := LembedModel
			if names["gvecmodel"] {
				var m sql.NullString
				if db.QueryRow("SELECT model FROM gvecmodel LIMIT 1").Scan(&m) == nil && m.String != "" {
					model = m.String
				}
			}
			counts[model]++
		}()
	}
	return counts
}

// dbFiles are the db.db files under root, top down with folders sorted
// (os.walk's order); with noRecurse, root's own.
func dbFiles(root string, noRecurse bool) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() && p != root {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if noRecurse && p != root {
				return fs.SkipDir
			}
			if st, err := os.Stat(filepath.Join(p, "db.db")); err == nil && !st.IsDir() {
				out = append(out, filepath.Join(p, "db.db"))
			}
		}
		return nil
	})
	return out
}

type backendChoice struct {
	backend, model, onnxDir, why string
}

func chooseBackend(o *PassOptions) (backendChoice, error) {
	onnxDir := o.ONNX
	if onnxDir == "" && o.Model != "" {
		onnxDir = filepath.Join(filepath.Dir(o.Model), "minilm-l6")
	}
	if o.Backend != "lembed" {
		why := Unavailable(onnxDir)
		if why == "" {
			return backendChoice{"onnx", ModelIDFor(o.MaxTokens), onnxDir, ""}, nil
		}
		if o.Backend == "onnx" {
			return backendChoice{}, fmt.Errorf("the ONNX backend cannot run: %s", why)
		}
		return backendChoice{"lembed", LembedModel, "", why}, nil
	}
	return backendChoice{"lembed", LembedModel, "", ""}, nil
}

// OnnxLayout is the sessions and threads each for a thread budget: a few
// of several threads, which embed as fast as one per thread and keep
// fewer copies of the model in memory (embed.py's onnx_layout).
func OnnxLayout(jobs int) (sessions, threads int) {
	sessions = max(1, min(8, jobs/4))
	return sessions, max(1, jobs/sessions)
}

type chunkRow struct {
	tinode, fname string
	seq           int
	text, h       string
}

// Pass is embed.py's main(): it returns an error where embed.py exits
// non-zero; errors embedding single passages are counted, not returned.
func Pass(ctx context.Context, o PassOptions) (PassResult, error) {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Err == nil {
		o.Err = io.Discard
	}
	if o.Jobs <= 0 {
		o.Jobs = max(1, runtime.NumCPU())
	}
	if o.MaxTokens == 0 {
		o.MaxTokens = MaxTokens
	}
	if o.MaxChunks == 0 {
		o.MaxChunks = MaxChunks
	}
	o.MaxChunks = max(1, o.MaxChunks)
	if o.Backend == "" {
		o.Backend = "auto"
	}
	bc, err := chooseBackend(&o)
	if err != nil {
		return PassResult{}, err
	}
	if bc.backend == "lembed" && o.Model == "" {
		return PassResult{}, fmt.Errorf("no embedding model: --model names none, and the ONNX backend cannot run (%s)", bc.why)
	}
	note := ""
	if bc.why != "" && bc.backend == "lembed" {
		note = ", " + bc.why
	}
	fmt.Fprintf(o.Out, "model: %s (%s%s)\n", bc.model, bc.backend, note)
	if o.Subdir != "" || o.NoRecurse {
		var others []string
		for m := range TreeModels(o.Index) {
			if m != bc.model {
				others = append(others, m)
			}
		}
		if len(others) > 0 {
			sort.Strings(others)
			fmt.Fprintf(o.Out, "the index holds vectors of %s: embedding all of it with %s\n", strings.Join(others, ", "), bc.model)
			o.Subdir, o.NoRecurse = "", false
		}
	}
	walkRoot := o.Index
	if o.Subdir != "" {
		walkRoot = filepath.Join(o.Index, o.Subdir)
	}
	dbs := dbFiles(walkRoot, o.NoRecurse)
	scratch, err := os.MkdirTemp(o.Scratch, "embed-")
	if err != nil {
		return PassResult{}, err
	}
	defer os.RemoveAll(scratch)
	return run(ctx, o, bc, dbs, scratch)
}

func run(ctx context.Context, o PassOptions, bc backendChoice, dbs []string, scratch string) (PassResult, error) {
	prog := &passProgress{fn: o.Progress}
	cachePath := o.Cache
	if cachePath == "" {
		cachePath = filepath.Join(scratch, "cache.db")
	}
	cache, err := OpenCache(cachePath)
	if err != nil {
		return PassResult{}, err
	}
	known := map[string]bool{}
	rows, err := cache.Query("SELECT h FROM emb")
	if err != nil {
		cache.Close()
		return PassResult{}, err
	}
	for rows.Next() {
		var h string
		rows.Scan(&h)
		known[h] = true
	}
	rows.Close()
	cache.Close()

	// Chunk each directory's words into gchunks, several at a time.
	type chunked struct {
		hasVec bool
		ins    []chunkRow
		err    error
	}
	results := make([]chunked, len(dbs))
	prog.start("chunk", 0)
	var wg sync.WaitGroup
	sem := make(chan struct{}, min(o.Jobs, 8))
	for i, f := range dbs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i].hasVec, results[i].ins, results[i].err = chunkDB(f, bc.model, o)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return PassResult{}, err
	}
	nChunks := 0
	var withChunks []string
	type pend struct{ h, t string }
	var pending []pend
	queued := map[string]bool{}
	used := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			return PassResult{}, fmt.Errorf("%s: %w", dbs[i], r.err)
		}
		nChunks += len(r.ins)
		if len(r.ins) > 0 || r.hasVec {
			withChunks = append(withChunks, dbs[i])
		}
		for _, c := range r.ins {
			used[c.h] = true
			if !known[c.h] && !queued[c.h] {
				queued[c.h] = true
				pending = append(pending, pend{c.h, headRunes(c.text, embedCap)})
			}
		}
	}
	nPending := len(pending)
	fmt.Fprintf(o.Out, "chunked: %d chunks across %d dbs, %d to embed, %d from the cache\n", nChunks, len(dbs), nPending, nChunks-nPending)

	var errs []string
	var errMu sync.Mutex
	addErr := func(s string) { errMu.Lock(); errs = append(errs, s); errMu.Unlock() }
	env := append(os.Environ(), "OMP_THREAD_LIMIT=1")
	sqlite3 := func(script string) (bool, string) {
		cctx, cancel := context.WithTimeout(ctx, time.Hour)
		defer cancel()
		cmd := exec.CommandContext(cctx, o.GufiSqlite3)
		cmd.Stdin = strings.NewReader(script)
		cmd.Env = env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		cmd.Stdout = io.Discard
		err := cmd.Run()
		msg := strings.TrimSpace(stderr.String())
		ok := err == nil && !strings.Contains(stderr.String(), "Error")
		if cctx.Err() == context.DeadlineExceeded {
			return false, "timed out"
		}
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		if msg == "" {
			code := -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else if err == nil {
				code = 0
			}
			msg = fmt.Sprintf("exit %d", code)
		}
		return ok, msg
	}

	switch {
	case nPending > 0 && bc.backend == "onnx":
		prog.start("embed", nPending+len(withChunks))
		sessions, threads := OnnxLayout(o.Jobs)
		embedders := make([]*Embedder, 0, sessions)
		for range sessions {
			e, err := New(bc.onnxDir, Options{Threads: threads, MaxTokens: o.MaxTokens, Library: o.Library})
			if err != nil {
				for _, x := range embedders {
					x.Close()
				}
				return PassResult{}, fmt.Errorf("the ONNX embedding processes stopped: %w", err)
			}
			embedders = append(embedders, e)
		}
		cache, err := OpenCache(cachePath)
		if err != nil {
			return PassResult{}, err
		}
		type done struct {
			rows [][2]any
			err  error
		}
		tasks := make(chan [2]int)
		out := make(chan done)
		var ewg sync.WaitGroup
		for _, e := range embedders {
			ewg.Add(1)
			go func() {
				defer ewg.Done()
				for t := range tasks {
					part := pending[t[0]:t[1]]
					texts := make([]string, len(part))
					for i, p := range part {
						texts[i] = p.t
					}
					blobs, err := e.Embed(texts)
					d := done{err: err}
					if err == nil {
						for i, p := range part {
							d.rows = append(d.rows, [2]any{p.h, blobs[i]})
						}
					}
					out <- d
				}
			}()
		}
		go func() {
		feed:
			for lo := 0; lo < nPending; lo += onnxTask {
				select {
				case tasks <- [2]int{lo, min(lo+onnxTask, nPending)}:
				case <-ctx.Done():
					break feed
				}
			}
			close(tasks)
			ewg.Wait()
			close(out)
		}()
		var firstErr error
		for d := range out {
			if d.err != nil {
				if firstErr == nil {
					firstErr = d.err
				}
				continue
			}
			if err := insertRows(cache, d.rows); err != nil && firstErr == nil {
				firstErr = err
			}
			prog.add(len(d.rows))
		}
		cache.Close()
		for _, e := range embedders {
			e.Close()
		}
		if firstErr != nil {
			return PassResult{}, fmt.Errorf("the ONNX embedding processes stopped: %w", firstErr)
		}
		if err := ctx.Err(); err != nil {
			return PassResult{}, err
		}
	case nPending > 0:
		prog.start("embed", nPending+len(withChunks))
		// The pending passages in a table gufi_sqlite3 reads, rowid from 1.
		pdb := filepath.Join(scratch, "pending.db")
		p, err := sql.Open("sqlite", "file:"+pdb+"?_pragma=synchronous(OFF)&_pragma=journal_mode(OFF)")
		if err != nil {
			return PassResult{}, err
		}
		p.SetMaxOpenConns(1)
		if _, err := p.Exec("CREATE TABLE pending (h TEXT PRIMARY KEY, t TEXT)"); err != nil {
			p.Close()
			return PassResult{}, err
		}
		tx, _ := p.Begin()
		st, _ := tx.Prepare("INSERT OR IGNORE INTO pending (h, t) VALUES (?, ?)")
		for _, x := range pending {
			st.Exec(x.h, x.t)
		}
		st.Close()
		tx.Commit()
		p.Close()
		model := sqlStr(o.Model)
		var embedRange func(lo, hi, cap int, out string) bool
		embedRange = func(lo, hi, cap int, out string) bool {
			script := strings.Join([]string{
				fmt.Sprintf("INSERT INTO temp.lembed_models(name, model) SELECT '%s', lembed_model_from_file(%s);", LembedModel, model),
				fmt.Sprintf("ATTACH %s AS p;", sqlStr(pdb)),
				fmt.Sprintf("ATTACH %s AS o;", sqlStr(out)),
				"PRAGMA o.synchronous=OFF;",
				"CREATE TABLE IF NOT EXISTS o.e (h TEXT PRIMARY KEY, v BLOB);",
				fmt.Sprintf("INSERT OR REPLACE INTO o.e SELECT h, lembed('%s', substr(t, 1, %d)) FROM p.pending WHERE rowid BETWEEN %d AND %d;", LembedModel, cap, lo, hi),
			}, "\n")
			ok, msg := sqlite3(script)
			if ok {
				return true
			}
			if ctx.Err() != nil {
				return false
			}
			if lo < hi {
				mid := (lo + hi) / 2
				a := embedRange(lo, mid, cap, out)
				b := embedRange(mid+1, hi, cap, out)
				return a && b
			}
			if cap != embedCapShort {
				return embedRange(lo, hi, embedCapShort, out)
			}
			addErr(fmt.Sprintf("chunk %d: %s", lo, msg))
			return false
		}
		slots := make(chan int, o.Jobs)
		for i := range o.Jobs {
			slots <- i
		}
		var lw sync.WaitGroup
		for lo := 1; lo <= nPending; lo += lembedBatch {
			if ctx.Err() != nil {
				break
			}
			slot := <-slots
			lw.Add(1)
			go func() {
				defer lw.Done()
				defer func() { slots <- slot }()
				hi := min(lo+lembedBatch-1, nPending)
				embedRange(lo, hi, embedCap, filepath.Join(scratch, fmt.Sprintf("out-%d.db", slot)))
				prog.add(hi - lo + 1)
			}()
		}
		lw.Wait()
		if err := ctx.Err(); err != nil {
			return PassResult{}, err
		}
		cache, err := OpenCache(cachePath)
		if err != nil {
			return PassResult{}, err
		}
		outs, _ := filepath.Glob(filepath.Join(scratch, "out-*.db"))
		sort.Strings(outs)
		for _, out := range outs {
			if _, err := cache.Exec("ATTACH " + sqlStr(out) + " AS o"); err != nil {
				addErr(fmt.Sprintf("%s: %v", filepath.Base(out), err))
				continue
			}
			if _, err := cache.Exec("INSERT OR IGNORE INTO emb (h, v) SELECT h, v FROM o.e"); err != nil {
				addErr(fmt.Sprintf("%s: %v", filepath.Base(out), err))
			}
			cache.Exec("DETACH o")
		}
		cache.Close()
	default:
		prog.start("write", len(withChunks))
	}

	// Each directory's vector table from the cache, 25 directories per
	// gufi_sqlite3 process.
	cacheDB := sqlStr(cachePath)
	dirLines := func(dbfile string) []string {
		return []string{
			fmt.Sprintf("ATTACH %s AS d;", sqlStr(dbfile)),
			"PRAGMA d.synchronous=OFF;",
			"BEGIN;",
			"DROP TABLE IF EXISTS d.gvec;",
			"CREATE VIRTUAL TABLE d.gvec USING vec0(cid INTEGER PRIMARY KEY, fp384 float[384]);",
			"INSERT INTO d.gvec(cid, fp384) SELECT g.cid, e.v FROM d.gchunks g JOIN c.emb e ON e.h = g.h;",
			"DROP TABLE IF EXISTS d.gvecmodel;",
			"CREATE TABLE d.gvecmodel (model TEXT);",
			fmt.Sprintf("INSERT INTO d.gvecmodel VALUES (%s);", sqlStr(bc.model)),
			"COMMIT;",
			"DETACH d;",
		}
	}
	var groups [][]string
	for i := 0; i < len(withChunks); i += 25 {
		groups = append(groups, withChunks[i:min(i+25, len(withChunks))])
	}
	gsem := make(chan struct{}, max(1, min(o.Jobs, 8)))
	var gw sync.WaitGroup
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		gw.Add(1)
		gsem <- struct{}{}
		go func() {
			defer gw.Done()
			defer func() { <-gsem }()
			lines := []string{fmt.Sprintf("ATTACH %s AS c;", cacheDB)}
			for _, f := range g {
				lines = append(lines, dirLines(f)...)
			}
			if ok, _ := sqlite3(strings.Join(lines, "\n")); !ok && ctx.Err() == nil {
				for _, f := range g {
					one := append([]string{lines[0]}, dirLines(f)...)
					if ok1, err1 := sqlite3(strings.Join(one, "\n")); !ok1 {
						addErr(fmt.Sprintf("%s: %s", f, err1))
					}
				}
			}
			prog.add(len(g))
		}()
	}
	gw.Wait()
	if err := ctx.Err(); err != nil {
		return PassResult{}, err
	}

	if o.Prune && o.Cache != "" && o.Subdir == "" && !o.NoRecurse {
		cache, err := OpenCache(cachePath)
		if err == nil {
			gone, err := pruneCache(cache, used)
			cache.Close()
			if err == nil && gone > 0 {
				fmt.Fprintf(o.Out, "pruned %d unused cache entries\n", gone)
			}
		}
	}
	prog.mu.Lock()
	prog.emit(true)
	prog.mu.Unlock()
	for i, e := range errs {
		if i == 10 {
			break
		}
		fmt.Fprintln(o.Err, "embed error:", e)
	}
	fmt.Fprintf(o.Out, "embedded: %d new chunks with %s, %d directory tables written, %d errors\n", nPending, bc.model, len(withChunks), len(errs))
	res := PassResult{Model: bc.model, Chunks: nChunks, Errors: len(errs), Pending: nPending, Tables: len(withChunks)}
	if len(errs) > 0 {
		res.First = headRunes(errs[0], 300)
	}
	return res, nil
}

func insertRows(cache *sql.DB, rows [][2]any) error {
	tx, err := cache.Begin()
	if err != nil {
		return err
	}
	st, err := tx.Prepare("INSERT OR REPLACE INTO emb (h, v) VALUES (?, ?)")
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rows {
		if _, err := st.Exec(r[0], r[1]); err != nil {
			st.Close()
			tx.Rollback()
			return err
		}
	}
	st.Close()
	return tx.Commit()
}

func pruneCache(cache *sql.DB, used map[string]bool) (int64, error) {
	conn, err := cache.Conn(context.Background())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.ExecContext(ctx, "CREATE TEMP TABLE used (h TEXT PRIMARY KEY)"); err != nil {
		return 0, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	st, _ := tx.Prepare("INSERT OR IGNORE INTO temp.used VALUES (?)")
	for h := range used {
		st.Exec(h)
	}
	st.Close()
	r, err := tx.Exec("DELETE FROM emb WHERE h NOT IN (SELECT h FROM temp.used)")
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := r.RowsAffected()
	return n, tx.Commit()
}

// chunkDB writes one directory's gchunks table.
func chunkDB(file, model string, o PassOptions) (hasVec bool, ins []chunkRow, err error) {
	db, err := openDB(file, false)
	if err != nil {
		return false, nil, err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA synchronous=OFF"); err != nil {
		return false, nil, err
	}
	var one int
	hasVec = db.QueryRow("SELECT 1 FROM sqlite_master WHERE name = 'gvec'").Scan(&one) == nil
	if _, err := db.Exec("DROP TABLE IF EXISTS gchunks"); err != nil {
		return false, nil, err
	}
	if _, err := db.Exec("CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)"); err != nil {
		return false, nil, err
	}
	rows, qerr := db.Query("SELECT tinode, fname, wordf FROM words")
	if qerr == nil {
		for rows.Next() {
			var tinode, fname, wordf sql.NullString
			if err := rows.Scan(&tinode, &fname, &wordf); err != nil {
				continue
			}
			if !EmbedsFile(fname.String, o.DocumentsOnly) {
				continue
			}
			for seq, c := range Chunk(indexText(wordf.String), o.MaxChunks) {
				if pytext.Strip(c) != "" {
					ins = append(ins, chunkRow{tinode.String, fname.String, seq, c, ChunkHash(model, c)})
				}
			}
		}
		rows.Close()
	}
	if len(ins) > 0 {
		tx, err := db.Begin()
		if err != nil {
			return false, nil, err
		}
		st, err := tx.Prepare("INSERT INTO gchunks (tinode, fname, seq, ctext, h) VALUES (?, ?, ?, ?, ?)")
		if err != nil {
			tx.Rollback()
			return false, nil, err
		}
		for _, c := range ins {
			if _, err := st.Exec(c.tinode, c.fname, c.seq, c.text, c.h); err != nil {
				st.Close()
				tx.Rollback()
				return false, nil, err
			}
		}
		st.Close()
		if err := tx.Commit(); err != nil {
			return false, nil, err
		}
	}
	return hasVec, ins, nil
}

// MarshalReport is the --report file's contents.
func (r PassResult) MarshalReport() []byte {
	b, _ := json.Marshal(r)
	return b
}
