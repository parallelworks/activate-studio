package userlibs

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// A library built over real ssh with the Studio's helper, against the
// indexer's Python steps run as the build ran them before (enrich.py, then
// embed.py with the ONNX model) on the same tree, on the same host: the
// same tables in every db.db, the same words and passages, the same
// extract cache, the same embedding cache with vectors within the
// embedder's tolerance, and the same reports. Needs STUDIO_TEST_SSHD=1 and:
//
//	EMBED_MODEL_DIR       model.onnx and tokenizer.json as embed_deps.json pins them
//	STUDIO_TEST_ORT_WHEEL a pinned linux-x86_64 onnxruntime wheel
//	ENRICH_DEPS           numpy, onnxruntime and tokenizers for the Python steps
//	                      (default: indexer/.deps)
//
// Nothing is downloaded: the Studio's caches are seeded with those files.

// vecTolerance is the embedder's: Go's vectors and Python's agreed within
// 8.94e-08 on the index's parity runs.
const vecTolerance = 1e-6

var downmarkHead = regexp.MustCompile(`^<!-- extracted by downmark [^\n]*-->\n\n`)

func TestRemoteBuildMatchesPython(t *testing.T) {
	r := newSshdRig(t)
	modelDir, wheel := os.Getenv("EMBED_MODEL_DIR"), os.Getenv("STUDIO_TEST_ORT_WHEEL")
	deps := os.Getenv("ENRICH_DEPS")
	if deps == "" {
		deps, _ = filepath.Abs("../../../indexer/.deps")
	}
	if modelDir == "" || wheel == "" {
		t.Skip("EMBED_MODEL_DIR and STUDIO_TEST_ORT_WHEEL not set")
	}
	if _, err := os.Stat(filepath.Join(deps, "onnxruntime")); err != nil {
		t.Skip("no onnxruntime in ENRICH_DEPS")
	}
	m := r.m
	// The Studio's caches: the wheel under its pinned name, the model in
	// the Studio's own model folder.
	w := m.ortWheel("linux-amd64")
	if w == nil {
		t.Fatal("no onnxruntime wheel pinned for linux-x86_64")
	}
	sum, err := fileSHA256(wheel)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	wheels := jsonv.Obj(get(m.pins(), "wheels"))
	for _, k := range wheels.Keys() {
		p := jsonv.Obj(get(jsonv.Obj(get(wheels, k)), "onnxruntime"))
		if strings.HasPrefix(k, "linux-x86_64-") && jsonv.String(get(p, "sha256")) == sum {
			copyFile(t, wheel, filepath.Join(m.embedCache(), cacheName(p)))
			found = true
			break
		}
	}
	if !found {
		t.Fatal("STUDIO_TEST_ORT_WHEEL is not a pinned linux-x86_64 wheel")
	}
	for _, n := range []string{"model.onnx", "tokenizer.json"} {
		copyFile(t, filepath.Join(modelDir, n), filepath.Join(m.onnxDir(), n))
	}

	h := r.host("compare", true, true)
	prose := []string{
		"Mooring lines are doubled before a storm, and the ground crew checks each anchor at dawn.",
		"The hangar doors open on rails; lubricate the rails every spring and inspect the seals.",
		"Helium purity is measured weekly. Below ninety five percent the envelope is topped up.",
	}
	for i, p := range prose {
		write(t, filepath.Join(h.home, "docs", "log", fmt.Sprintf("day-%d.md", i)), "# Day\n\n"+strings.Repeat(p+" ", 6)+"\n")
	}
	write(t, filepath.Join(h.home, "docs", "crew.txt"), "Crew roster and duty hours for the airship.\n")
	write(t, filepath.Join(h.home, "docs", "data.csv"), "a,b\n1,2\n")
	l := r.build("compare", h, "meaning-full")
	readyOrFail(t, l)
	if l["fullText"] != true || l["vectors"] != true {
		t.Fatalf("the helper's build: fullText %v, vectors %v, gaps %v", l["fullText"], l["vectors"], l["gaps"])
	}
	goTree := l["indexRoot"].(string)
	goIdx := filepath.Dir(goTree)
	builds, _ := filepath.Glob(filepath.Join(h.home, ".activate-studio", "builds", "*"))
	if len(builds) != 1 {
		t.Fatalf("builds there: %v", builds)
	}

	// The Python steps, as the build ran them, on the same folder.
	src := filepath.Join(h.home, "docs")
	pyIdx := t.TempDir()
	stage := filepath.Join(pyIdx, ".build")
	run(t, nil, filepath.Join(r.gufi, "gufi_dir2index"), "-n", "2", "-x", src, stage)
	pyTree := filepath.Join(stage, "docs")
	env := []string{"PATH=" + h.bin, "HOME=" + h.home, "PYTHONPATH=" + deps}
	indexer, _ := filepath.Abs("../../../indexer")
	pyText, pyEmbed := filepath.Join(pyIdx, "text.json"), filepath.Join(pyIdx, "embed.json")
	run(t, env, r.python, "-s", filepath.Join(indexer, "enrich.py"), "--kb-root", src, "--index", pyTree, "--extract-cache", filepath.Join(pyIdx, ".extract"), "--report", pyText)
	run(t, env, r.python, "-s", filepath.Join(indexer, "embed.py"), "--index", pyTree, "--onnx", modelDir, "--gufi-sqlite3", filepath.Join(r.gufi, "gufi_sqlite3"),
		"--cache", filepath.Join(pyIdx, ".embed-cache.db"), "--prune", "--report", pyEmbed)

	t.Run("the same tables, words and passages in every folder's db.db", func(t *testing.T) {
		goDbs, pyDbs := dbsUnder(t, goTree), dbsUnder(t, pyTree)
		eq(t, goDbs, pyDbs, "folders")
		for _, rel := range pyDbs {
			g, p := filepath.Join(goTree, rel), filepath.Join(pyTree, rel)
			for _, q := range []string{
				"SELECT type, name FROM sqlite_master ORDER BY type, name",
				"SELECT tinode, fname, wordf FROM words ORDER BY fname",
				"SELECT cid, tinode, fname, seq, ctext, h FROM gchunks ORDER BY cid",
				"SELECT model FROM gvecmodel",
				"SELECT count(*) FROM gvec_rowids",
			} {
				if gr, pr := rows(t, g, q), rows(t, p, q); !reflect.DeepEqual(gr, pr) {
					t.Errorf("%s: %s\ngo     %v\npython %v", rel, q, gr, pr)
				}
			}
		}
		if n := rows(t, filepath.Join(goTree, "log", "db.db"), "SELECT count(*) FROM gchunks"); fmt.Sprint(n) == "[[0]]" {
			t.Error("no passages were embedded")
		}
	})

	t.Run("the same extract cache, with the header the Studio's own cache has", func(t *testing.T) {
		// The Go reader heads each entry with the line downmark writes, as
		// the Studio's own extract cache has it; the words read past it.
		g := treeFiles(t, filepath.Join(goIdx, ".extract"))
		for k, v := range g {
			head := downmarkHead.FindString(v)
			if head == "" {
				t.Errorf("%s: no downmark line", k)
			}
			g[k] = v[len(head):]
		}
		eq(t, g, treeFiles(t, filepath.Join(pyIdx, ".extract")), "extract cache")
	})

	t.Run("the same embedding cache, vectors within the tolerance", func(t *testing.T) {
		g, p := cacheVectors(t, filepath.Join(goIdx, ".embed-cache.db")), cacheVectors(t, filepath.Join(pyIdx, ".embed-cache.db"))
		if len(g) == 0 || len(g) != len(p) {
			t.Fatalf("%d vectors in Go's cache, %d in Python's", len(g), len(p))
		}
		worst := 0.0
		for k, pv := range p {
			gv, ok := g[k]
			if !ok || len(gv) != len(pv) {
				t.Fatalf("passage %s: not in Go's cache", k)
			}
			for i := range pv {
				worst = math.Max(worst, math.Abs(float64(gv[i]-pv[i])))
			}
		}
		if worst > vecTolerance {
			t.Errorf("largest difference %g", worst)
		}
		t.Logf("%d vectors, largest difference %g", len(p), worst)
	})

	t.Run("the same reports", func(t *testing.T) {
		eq(t, readJSON(t, filepath.Join(builds[0], "text.json")), readJSON(t, pyText), "text.json")
		eq(t, readJSON(t, filepath.Join(builds[0], "embed.json")), readJSON(t, pyEmbed), "embed.json")
	})
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(to), 0o755)
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(name), err, out)
	}
}

func dbsUnder(t *testing.T, root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "db.db" {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func rows(t *testing.T, db, q string) [][]any {
	t.Helper()
	c, err := sql.Open("sqlite", "file:"+db+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rs, err := c.Query(q)
	if err != nil {
		// A table neither has reads as none in both.
		return [][]any{{"error"}}
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out [][]any
	for rs.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rs.Scan(ptrs...)
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return out
}

func treeFiles(t *testing.T, root string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

func cacheVectors(t *testing.T, db string) map[string][]float32 {
	out := map[string][]float32{}
	for _, r := range rows(t, db, "SELECT h, v FROM emb") {
		h, _ := r[0].(string)
		b, _ := r[1].(string)
		v := make([]float32, len(b)/4)
		for i := range v {
			v[i] = math.Float32frombits(binary.LittleEndian.Uint32([]byte(b[i*4:])))
		}
		out[h] = v
	}
	return out
}

func readJSON(t *testing.T, p string) any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return v
}
