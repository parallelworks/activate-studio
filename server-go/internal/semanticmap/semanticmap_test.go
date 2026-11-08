package semanticmap

import (
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testdata/python_golden.json holds indexer/semantic_map.py's own answers
// for whole_words, file_terms and name_clusters on the same inputs.
func TestPythonGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/python_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		WholeWords []struct {
			In  []string `json:"in"`
			Out string   `json:"out"`
		} `json:"wholeWords"`
		FileTerms []struct {
			In  string  `json:"in"`
			Out [][]any `json:"out"`
		} `json:"fileTerms"`
		NameClusters struct {
			In  [2]json.RawMessage `json:"in"`
			Out [2]json.RawMessage `json:"out"`
		} `json:"nameClusters"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	for i, c := range g.WholeWords {
		if got := wholeWords(c.In); got != c.Out {
			t.Errorf("wholeWords case %d:\n got %q\nwant %q", i, got, c.Out)
		}
	}
	for i, c := range g.FileTerms {
		got := fileTerms(c.In)
		if len(got) != len(c.Out) {
			t.Fatalf("fileTerms case %d: %v, want %v", i, got, c.Out)
		}
		for j, tc := range got {
			if tc.Term != c.Out[j][0].(string) || float64(tc.Count) != c.Out[j][1].(float64) {
				t.Errorf("fileTerms case %d term %d: %v, want %v", i, j, tc, c.Out[j])
			}
		}
	}
	var cands [][]string
	var folders, wantNames []string
	var wantChosen [][]string
	json.Unmarshal(g.NameClusters.In[0], &cands)
	json.Unmarshal(g.NameClusters.In[1], &folders)
	json.Unmarshal(g.NameClusters.Out[0], &wantNames)
	json.Unmarshal(g.NameClusters.Out[1], &wantChosen)
	names, chosen := nameClusters(cands, folders, 26)
	if !reflect.DeepEqual(names, wantNames) || !reflect.DeepEqual(chosen, wantChosen) {
		t.Errorf("nameClusters: %v %v, want %v %v", names, chosen, wantNames, wantChosen)
	}
}

func TestLabelClusters(t *testing.T) {
	paths := []string{"pw/docs/a", "pw/docs/b", "gs/docs/c", "pw/docs/d", "x/y", "x/z", "top"}
	members := [][]int{{0, 1, 2}, {4, 5}, {6}}
	got := labelClusters(paths, members, 3)
	want := []string{"pw", "x", "(root)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels %v, want %v", got, want)
	}
	// Two clusters both under pw go a level deeper.
	got = labelClusters([]string{"pw/a/1", "pw/a/2", "pw/b/1", "pw/b/2"}, [][]int{{0, 1}, {2, 3}}, 2)
	if !reflect.DeepEqual(got, []string{"pw/a", "pw/b"}) {
		t.Fatalf("deeper labels %v", got)
	}
}

func TestClusterLinks(t *testing.T) {
	sums := [][]float64{{1, 0, 0}, {0.9, 0.1, 0}, {0, 1, 0}, {0, 0.9, 0.2}}
	links := clusterLinks(sums, 4, 2)
	if links[0].A != 0 || links[0].B != 1 {
		t.Fatalf("nearest pair first: %+v", links)
	}
	seen := map[[2]int]bool{}
	for _, l := range links {
		if l.A >= l.B || seen[[2]int{l.A, l.B}] {
			t.Fatalf("pairs listed once, a < b: %+v", links)
		}
		seen[[2]int{l.A, l.B}] = true
	}
}

// writeIndex writes folders whose files point along one axis each, the way
// sqlite-vec stores vectors, with one passage in a slot marked invalid.
func writeIndex(t *testing.T, root string, topics map[string]int, files int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, 2))
	os.MkdirAll(root, 0o755)
	mk := func(dir string) *sql.DB {
		os.MkdirAll(dir, 0o755)
		db, err := sql.Open("sqlite", filepath.Join(dir, "db.db"))
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	db := mk(root)
	db.Exec("CREATE TABLE entries (name TEXT)")
	db.Close()
	for folder, axis := range topics {
		db := mk(filepath.Join(root, folder))
		for _, q := range []string{
			"CREATE TABLE gchunks (cid INTEGER PRIMARY KEY, tinode TEXT, fname TEXT, seq INTEGER, ctext TEXT, h TEXT)",
			"CREATE TABLE gvec_rowids (rowid INTEGER PRIMARY KEY AUTOINCREMENT, id, chunk_id INTEGER, chunk_offset INTEGER)",
			"CREATE TABLE gvec_chunks (chunk_id INTEGER PRIMARY KEY AUTOINCREMENT, size INTEGER NOT NULL, validity BLOB NOT NULL, rowids BLOB NOT NULL)",
			"CREATE TABLE gvec_vector_chunks00 (rowid PRIMARY KEY, vectors BLOB NOT NULL)",
			"CREATE TABLE gvecmodel (model TEXT)",
			"INSERT INTO gvecmodel VALUES ('minilm-l6-mean-int8')",
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		blob := make([]byte, 1024*vectorBytes)
		valid := make([]byte, 128)
		put := func(slot, ax int) {
			for d := 0; d < Dims; d++ {
				v := float32(rng.NormFloat64() * 0.05)
				if d == ax {
					v += 1
				}
				binary.LittleEndian.PutUint32(blob[slot*vectorBytes+d*4:], math.Float32bits(v))
			}
		}
		cid := 0
		for f := 0; f < files; f++ {
			for seq := 0; seq < 2; seq++ {
				cid++
				text := fmt.Sprintf("%s %s notes about %s", folder, folder, folder)
				db.Exec("INSERT INTO gchunks VALUES (?, '1', ?, ?, ?, ?)", cid, fmt.Sprintf("%s-%d.md", folder, f), seq, text, fmt.Sprint("h", cid))
				db.Exec("INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, 1, ?)", cid, cid-1)
				put(cid-1, axis)
				valid[(cid-1)>>3] |= 1 << ((cid - 1) & 7)
			}
		}
		cid++
		db.Exec("INSERT INTO gchunks VALUES (?, '1', 'deleted.md', 0, 'gone', 'hx')", cid)
		db.Exec("INSERT INTO gvec_rowids (rowid, chunk_id, chunk_offset) VALUES (?, 1, ?)", cid, cid-1)
		put(cid-1, 300)
		db.Exec("INSERT INTO gvec_chunks VALUES (1, 1024, ?, x'')", valid)
		db.Exec("INSERT INTO gvec_vector_chunks00 VALUES (1, ?)", blob)
		db.Close()
	}
}

func TestBuildFromIndex(t *testing.T) {
	tmp := t.TempDir()
	index := filepath.Join(tmp, "index")
	writeIndex(t, index, map[string]int{"turbines": 3, "bread": 100, "tax": 200}, 20)
	out := filepath.Join(tmp, "maps", "semantic-map")
	sum, err := Build(IndexSource(index), out, Options{Sample: 3000, Fit: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if !sum.Available || sum.Files != 60 || sum.Dbs != 4 || sum.Model != "minilm-l6-mean-int8" {
		t.Fatalf("summary %+v", sum)
	}
	var ov map[string]any
	raw, _ := os.ReadFile(filepath.Join(out, "overview.json"))
	if err := json.Unmarshal(raw, &ov); err != nil {
		t.Fatal(err)
	}
	if ov["version"].(float64) != 3 || ov["files"].(float64) != 60 {
		t.Fatalf("overview %v", ov["version"])
	}
	// Each topic folder ends whole in one cluster, and no deleted passage counts.
	byFolder := map[string]map[float64]bool{}
	clusters := ov["clusters"].([]any)
	for _, c := range clusters {
		cm := c.(map[string]any)
		raw, err := os.ReadFile(filepath.Join(out, fmt.Sprintf("cluster-%d.json", int(cm["c"].(float64)))))
		if err != nil {
			t.Fatal(err)
		}
		var cf struct {
			Points []struct{ P string } `json:"points"`
		}
		json.Unmarshal(raw, &cf)
		for _, p := range cf.Points {
			if strings.Contains(p.P, "deleted") {
				t.Fatalf("an invalid slot was read: %s", p.P)
			}
			folder := strings.Split(p.P, "/")[0]
			if byFolder[folder] == nil {
				byFolder[folder] = map[float64]bool{}
			}
			byFolder[folder][cm["c"].(float64)] = true
		}
	}
	for f, cs := range byFolder {
		if len(cs) != 1 && len(clusters) <= 3 {
			t.Errorf("%s split across %v", f, cs)
		}
	}
	if len(ov["points"].([]any)) != 60 {
		t.Errorf("all 60 files shown, got %d", len(ov["points"].([]any)))
	}
	// Fewer than three files: no map made, the old one stays.
	empty := filepath.Join(tmp, "empty")
	os.MkdirAll(empty, 0o755)
	sum, err = Build(IndexSource(empty), out, Options{Sample: 3000, Fit: 20000})
	if err != nil || sum.Available {
		t.Fatalf("empty: %+v %v", sum, err)
	}
	if _, err := os.Stat(filepath.Join(out, "overview.json")); err != nil {
		t.Fatal("the old map should stay")
	}
	left, _ := filepath.Glob(filepath.Join(tmp, "maps", "semantic-map-*"))
	if len(left) != 0 {
		t.Fatalf("scratch folders left: %v", left)
	}
}

func TestBuildFromVectors(t *testing.T) {
	tmp := t.TempDir()
	rng := rand.New(rand.NewPCG(3, 4))
	var b strings.Builder
	for i := 0; i < 90; i++ {
		ax := []int{5, 50, 150}[i%3]
		q := make([]byte, Dims)
		for d := range q {
			v := rng.NormFloat64() * 0.02
			if d == ax {
				v += 1
			}
			q[d] = byte(int8(math.Max(-128, math.Min(127, math.Trunc((v+1)*127.5-128)))))
		}
		row := map[string]any{"p": fmt.Sprintf("t%d/f%d.txt", ax, i), "r": 0.8, "v": hex.EncodeToString(q)}
		if i%2 == 0 {
			row["t"] = fmt.Sprintf("topic%d words topic%d more", ax, ax)
		}
		line, _ := json.Marshal(row)
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteString("not json\n{\"p\":\"short\",\"v\":\"00ff\"}\n")
	vec := filepath.Join(tmp, "vectors.jsonl")
	os.WriteFile(vec, []byte(b.String()), 0o644)
	of := 5000
	out := filepath.Join(tmp, "semantic-map")
	sum, err := Build(VectorsSource(vec, "minilm-l6-mean-int8"), out, Options{Sample: 3000, Fit: 20000, Of: &of})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 90 || sum.Of == nil || *sum.Of != 5000 || sum.Dbs != 0 {
		t.Fatalf("summary %+v", sum)
	}
}

// writeVectors writes n files as remoteMap.ts does, on 6 topics, with text
// on every third and a few lines a reader must skip.
func writeVectors(t *testing.T, path string, n int) {
	t.Helper()
	rng := rand.New(rand.NewPCG(7, 8))
	words := []string{"turbine", "inlet", "vortex", "mesh", "solver", "plasma", "reef", "coral", "sensor", "orbit", "laser", "alloy"}
	var b strings.Builder
	for i := 0; i < n; i++ {
		topic := i % 6
		q := make([]byte, Dims)
		for d := range q {
			v := rng.NormFloat64() * 0.03
			if d == topic*40 {
				v += 1
			}
			q[d] = byte(int8(math.Max(-128, math.Min(127, math.Trunc((v+1)*127.5-128)))))
		}
		row := map[string]any{"p": fmt.Sprintf("area%d/d%d/doc%d.md", topic%2, i/100, i), "v": hex.EncodeToString(q)}
		if i%5 != 0 {
			row["r"] = 0.5 + rng.Float64()/2
		}
		if i%3 == 0 {
			var tx []string
			for k := 0; k < 40; k++ {
				tx = append(tx, words[(topic*2+rng.IntN(3))%len(words)])
			}
			row["t"] = strings.Join(tx, " ")
		}
		line, _ := json.Marshal(row)
		b.Write(line)
		b.WriteByte('\n')
		switch i % 997 {
		case 1:
			b.WriteString("not json\n")
		case 2:
			b.WriteString(`{"v":"00"}` + "\n")
		case 3:
			b.WriteString(`{"p":"short.md","v":"00ff"}` + "\n")
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, src Source) []File {
	t.Helper()
	var out []File
	if _, err := src(func(f File) error { out = append(out, f); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

// Reading a vectors.jsonl on many workers gives the same files in the same
// order as one reader, and so the same map, byte for byte.
func TestVectorsSourceParallelSameAsSequential(t *testing.T) {
	tmp := t.TempDir()
	vec := filepath.Join(tmp, "vectors.jsonl")
	writeVectors(t, vec, 6000)
	one := collect(t, vectorsSource(vec, "m", 1))
	if len(one) != 6000 {
		t.Fatalf("one reader: %d files", len(one))
	}
	for _, w := range []int{2, 8, 33} {
		if many := collect(t, vectorsSource(vec, "m", w)); !reflect.DeepEqual(one, many) {
			t.Fatalf("%d workers: the files differ from one reader's", w)
		}
	}
	of := 9000
	opt := Options{Sample: 3000, Fit: 20000, Of: &of}
	outOne, outMany := filepath.Join(tmp, "one", "map"), filepath.Join(tmp, "many", "map")
	if _, err := Build(vectorsSource(vec, "m", 1), outOne, opt); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(vectorsSource(vec, "m", 8), outMany, opt); err != nil {
		t.Fatal(err)
	}
	names, _ := filepath.Glob(filepath.Join(outOne, "*"))
	if len(names) < 3 {
		t.Fatalf("map files: %v", names)
	}
	for _, a := range names {
		b := filepath.Join(outMany, filepath.Base(a))
		x, _ := os.ReadFile(a)
		y, err := os.ReadFile(b)
		if filepath.Base(a) == "overview.json" {
			x, y = withoutTimes(t, x), withoutTimes(t, y)
		}
		if err != nil || string(x) != string(y) {
			t.Fatalf("%s differs between one reader and many", filepath.Base(a))
		}
	}
	if more, _ := filepath.Glob(filepath.Join(outMany, "*")); len(more) != len(names) {
		t.Fatalf("%d map files from many readers, %d from one", len(more), len(names))
	}
}

// withoutTimes drops the fields of overview.json that say when and how
// fast the map was built.
func withoutTimes(t *testing.T, b []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "builtAt")
	delete(m, "ms")
	delete(m, "readMs")
	out, _ := json.Marshal(m)
	return out
}

// An error from emit stops the read and is returned; nothing is left
// running.
func TestVectorsSourceStopsOnEmitError(t *testing.T) {
	vec := filepath.Join(t.TempDir(), "vectors.jsonl")
	writeVectors(t, vec, 5000)
	stop := fmt.Errorf("stop")
	n := 0
	_, err := vectorsSource(vec, "m", 8)(func(File) error {
		if n++; n == 700 {
			return stop
		}
		return nil
	})
	if err != stop || n != 700 {
		t.Fatalf("err %v after %d files", err, n)
	}
}

func TestBuildRejectsZeroOptions(t *testing.T) {
	tmp := t.TempDir()
	index := filepath.Join(tmp, "index")
	writeIndex(t, index, map[string]int{"bread": 30}, 20)
	out := filepath.Join(tmp, "maps", "semantic-map")
	neg := -1
	for name, opt := range map[string]Options{
		"zero":        {},
		"no fit":      {Sample: 3000},
		"no sample":   {Fit: 20000},
		"small fit":   {Sample: 3000, Fit: MinFit - 1},
		"negative of": {Sample: 3000, Fit: 20000, Of: &neg},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: Build panicked: %v", name, r)
				}
			}()
			if _, err := Build(IndexSource(index), out, opt); err == nil {
				t.Errorf("%s: Build accepted %+v", name, opt)
			}
		}()
	}
	if _, err := os.Stat(filepath.Join(tmp, "maps")); !os.IsNotExist(err) {
		t.Errorf("rejected options left files behind: %v", err)
	}
	if _, err := Build(IndexSource(index), out, Options{Sample: MinSample, Fit: MinFit}); err != nil {
		t.Errorf("the floors: %v", err)
	}
}
