package semanticmap

import (
	"bufio"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure Go: sqlite-vec's tables are read as plain tables, no extension
)

// Dims is the length of every vector the Studio's models make.
const Dims = 384

const vectorBytes = Dims * 4

// File is one file with vectors: its path in the library, the mean of its
// passage vectors, the model they come from, and its commonest terms.
type File struct {
	Path  string
	Vec   []float32
	Model string
	Terms []TermCount
}

// Source hands each file with vectors to emit and returns how many folder
// databases it read.
type Source func(emit func(File) error) (dbs int, err error)

// folderVectors returns the files with vectors in one folder's db.db, read
// without the sqlite-vec extension: gvec_rowids says which chunk and offset
// hold a passage, gvec_vector_chunks00 holds the float32 vectors, and
// gvec_chunks marks the valid slots. The terms come from the file's passages
// (gchunks), the text its vectors were made from. Any error reading the
// folder leaves it out, as the Python script does.
func folderVectors(dbPath string) []File {
	u := url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=busy_timeout(30000)"}
	c, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	out, err := readFolder(c)
	if err != nil {
		return nil
	}
	return out
}

func readFolder(c *sql.DB) ([]File, error) {
	names := map[string]bool{}
	rows, err := c.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names[n] = true
	}
	rows.Close()
	if !names["gchunks"] || !names["gvec_rowids"] || !names["gvec_vector_chunks00"] {
		return nil, nil
	}
	model := "minilm384"
	if names["gvecmodel"] {
		var m sql.NullString
		if err := c.QueryRow("SELECT model FROM gvecmodel LIMIT 1").Scan(&m); err == nil && m.Valid && m.String != "" {
			model = m.String
		}
	}
	valid := map[int64][]byte{}
	if names["gvec_chunks"] {
		if err := scanAll(c, "SELECT chunk_id, validity FROM gvec_chunks", func(r *sql.Rows) error {
			var id int64
			var b []byte
			if err := r.Scan(&id, &b); err != nil {
				return err
			}
			valid[id] = b
			return nil
		}); err != nil {
			return nil, err
		}
	}
	blobs := map[int64][]byte{}
	if err := scanAll(c, "SELECT rowid, vectors FROM gvec_vector_chunks00", func(r *sql.Rows) error {
		var id int64
		var b []byte
		if err := r.Scan(&id, &b); err != nil {
			return err
		}
		blobs[id] = b
		return nil
	}); err != nil {
		return nil, err
	}
	fnameOf := map[int64]string{}
	if err := scanAll(c, "SELECT cid, fname FROM gchunks", func(r *sql.Rows) error {
		var id int64
		var f sql.NullString
		if err := r.Scan(&id, &f); err != nil {
			return err
		}
		if f.Valid {
			fnameOf[id] = f.String
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var order []string
	sums := map[string][]float64{}
	counts := map[string]int{}
	errBad := errorString("vector out of range")
	if err := scanAll(c, "SELECT rowid, chunk_id, chunk_offset FROM gvec_rowids", func(r *sql.Rows) error {
		var rowid int64
		var chunk, offset sql.NullInt64
		if err := r.Scan(&rowid, &chunk, &offset); err != nil {
			return err
		}
		fname, ok := fnameOf[rowid]
		if !ok || !chunk.Valid || !offset.Valid {
			return nil
		}
		blob, ok := blobs[chunk.Int64]
		if !ok {
			return nil
		}
		off := offset.Int64
		if bits, ok := valid[chunk.Int64]; ok {
			if off < 0 || int(off>>3) >= len(bits) {
				return errBad // Python's IndexError leaves the folder out
			}
			if (bits[off>>3]>>(off&7))&1 == 0 {
				return nil
			}
		}
		start := off * vectorBytes
		if off < 0 || start+vectorBytes > int64(len(blob)) {
			return errBad // Python's ValueError leaves the folder out
		}
		s, seen := sums[fname]
		if !seen {
			s = make([]float64, Dims)
			sums[fname] = s
			order = append(order, fname)
		}
		for d := 0; d < Dims; d++ {
			s[d] += float64(math.Float32frombits(binary.LittleEndian.Uint32(blob[start+int64(d)*4:])))
		}
		counts[fname]++
		return nil
	}); err != nil {
		return nil, err
	}
	// Whole passages, about TextChars characters of them, so the words at
	// their cuts can be told apart (wholeWords).
	texts := map[string][]string{}
	sizes := map[string]int{}
	if err := scanAll(c, "SELECT fname, ctext FROM gchunks ORDER BY fname, seq", func(r *sql.Rows) error {
		var f, t sql.NullString
		if err := r.Scan(&f, &t); err != nil {
			return err
		}
		if _, ok := sums[f.String]; !ok || !f.Valid || !t.Valid || t.String == "" || sizes[f.String] >= TextChars {
			return nil
		}
		texts[f.String] = append(texts[f.String], t.String)
		sizes[f.String] += len([]rune(t.String))
		return nil
	}); err != nil {
		texts = map[string][]string{}
	}
	out := make([]File, 0, len(order))
	for _, f := range order {
		v := make([]float32, Dims)
		n := float64(counts[f])
		for d, x := range sums[f] {
			v[d] = float32(x / n)
		}
		out = append(out, File{Path: f, Vec: v, Model: model, Terms: fileTerms(wholeWords(texts[f]))})
	}
	return out, nil
}

type errorString string

func (e errorString) Error() string { return string(e) }

func scanAll(c *sql.DB, q string, fn func(*sql.Rows) error) error {
	rows, err := c.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// IndexSource reads every folder's db.db in an index tree, top down with
// each folder's subfolders in name order and hidden ones skipped, as
// Python's os.walk with sorted dirnames does. Symbolic links to folders are
// not followed.
func IndexSource(index string) Source {
	return func(emit func(File) error) (int, error) {
		dbs := 0
		var walk func(dir string) error
		walk = func(dir string) error {
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil // os.walk ignores a folder it cannot list
			}
			var subdirs []string
			hasDB := false
			for _, e := range entries {
				isDir := e.IsDir()
				if e.Type()&os.ModeSymlink != 0 {
					if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
						continue // listed by os.walk but not walked into
					}
				}
				if isDir {
					if !strings.HasPrefix(e.Name(), ".") {
						subdirs = append(subdirs, e.Name())
					}
				} else if e.Name() == "db.db" {
					hasDB = true
				}
			}
			sort.Strings(subdirs)
			if hasDB {
				dbs++
				rel, _ := filepath.Rel(index, dir)
				rel = filepath.ToSlash(rel)
				for _, f := range folderVectors(filepath.Join(dir, "db.db")) {
					if rel != "." {
						f.Path = rel + "/" + f.Path
					}
					if err := emit(f); err != nil {
						return err
					}
				}
			}
			for _, d := range subdirs {
				if err := walk(filepath.Join(dir, d)); err != nil {
					return err
				}
			}
			return nil
		}
		err := walk(index)
		return dbs, err
	}
}

// VectorsSource reads the file means fetched from an index on another
// system (server/src/remoteMap.ts): a JSON line each with its path (p), the
// length of its mean vector (r), the mean normalized and quantized to int8
// by sqlite-vec's 'unit' scale, as hex (v), and, for some, the start of its
// text (t). sqlite-vec truncates (x + 1) * 127.5 - 128, so each value is
// read back at the middle of its step. Lines are decoded and their terms
// taken on every CPU, and handed to emit in file order, so the map is the
// same as from one reader.
func VectorsSource(path, model string) Source {
	return vectorsSource(path, model, runtime.GOMAXPROCS(0))
}

// vectorsBatch is how many lines a worker decodes at a time.
const vectorsBatch = 256

func vectorsSource(path, model string, workers int) Source {
	return func(emit func(File) error) (int, error) {
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		if workers <= 1 {
			for sc.Scan() {
				if file, ok := vectorLine(sc.Bytes(), model); ok {
					if err := emit(file); err != nil {
						return 0, err
					}
				}
			}
			return 0, sc.Err()
		}
		// The reader cuts the file into batches and sends each to the
		// workers and, in file order, to this goroutine, which waits for
		// each batch in turn and emits its files. Both channels bound the
		// batches in flight.
		type batch struct {
			data  []byte
			ends  []int
			files []File
			done  chan struct{}
		}
		jobs := make(chan *batch, 2*workers)
		order := make(chan *batch, 4*workers)
		stop := make(chan struct{})
		var readErr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(order)
			defer close(jobs)
			for {
				b := &batch{done: make(chan struct{})}
				for len(b.ends) < vectorsBatch && sc.Scan() {
					b.data = append(b.data, sc.Bytes()...)
					b.ends = append(b.ends, len(b.data))
				}
				if len(b.ends) == 0 {
					readErr = sc.Err()
					return
				}
				select {
				case order <- b:
				case <-stop:
					return
				}
				select {
				case jobs <- b:
				case <-stop:
					return
				}
			}
		}()
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for b := range jobs {
					start := 0
					for _, end := range b.ends {
						if file, ok := vectorLine(b.data[start:end], model); ok {
							b.files = append(b.files, file)
						}
						start = end
					}
					close(b.done)
				}
			}()
		}
		var emitErr error
		for b := range order {
			<-b.done
			for _, file := range b.files {
				if emitErr = emit(file); emitErr != nil {
					break
				}
			}
			if emitErr != nil {
				close(stop)
				break
			}
		}
		if emitErr != nil {
			// Let the reader and workers finish; a batch the reader still
			// holds is dropped.
			go func() {
				for range order {
				}
			}()
		}
		wg.Wait()
		if emitErr != nil {
			return 0, emitErr
		}
		return 0, readErr
	}
}

// vectorLine decodes one line of a vectors.jsonl; ok is false for a line
// that is not a file with a whole vector, which is skipped.
func vectorLine(line []byte, model string) (File, bool) {
	var row struct {
		P *string  `json:"p"`
		R *float64 `json:"r"`
		V string   `json:"v"`
		T string   `json:"t"`
	}
	if json.Unmarshal(line, &row) != nil || row.P == nil {
		return File{}, false
	}
	q, err := hex.DecodeString(row.V)
	if err != nil || len(q) != Dims {
		return File{}, false
	}
	x := make([]float64, Dims)
	norm := 0.0
	for i, b := range q {
		v := float64(int8(b))
		sign := 0.0
		if v > 0 {
			sign = 1
		} else if v < 0 {
			sign = -1
		}
		x[i] = (v+sign*0.5+128)/127.5 - 1
		norm += x[i] * x[i]
	}
	norm = math.Sqrt(norm)
	r := 1.0
	if row.R != nil && *row.R != 0 {
		r = *row.R
	}
	scale := 0.0
	if norm > 0 {
		scale = r / norm
	}
	vec := make([]float32, Dims)
	for i := range x {
		vec[i] = float32(x[i] * scale)
	}
	var terms []TermCount
	if row.T != "" {
		terms = fileTerms(wholeWords([]string{row.T}))
	}
	return File{Path: *row.P, Vec: vec, Model: model, Terms: terms}, true
}
