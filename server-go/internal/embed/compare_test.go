package embed

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"testing"
)

// TestPythonVectors compares vectors with embed_onnx.py's
// (EMBED_PY_VECTORS, written by scripts/embed_vectors.py; EMBED_MODEL_DIR).
func TestPythonVectors(t *testing.T) {
	file, dir := os.Getenv("EMBED_PY_VECTORS"), os.Getenv("EMBED_MODEL_DIR")
	if file == "" || dir == "" {
		t.Skip("EMBED_PY_VECTORS and EMBED_MODEL_DIR not set")
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var texts []string
	var want [][]float32
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r struct{ Text, V string }
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		b, _ := base64.StdEncoding.DecodeString(r.V)
		texts = append(texts, r.Text)
		want = append(want, Floats(b))
	}
	e, err := New(dir, Options{Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	got, err := e.Embed(texts)
	if err != nil {
		t.Fatal(err)
	}
	worstAbs, worstCos, exact := 0.0, 1.0, 0
	for i := range texts {
		g := Floats(got[i])
		dot, same := 0.0, true
		for k := range g {
			d := math.Abs(float64(g[k] - want[i][k]))
			worstAbs = max(worstAbs, d)
			dot += float64(g[k]) * float64(want[i][k])
			if g[k] != want[i][k] {
				same = false
			}
		}
		worstCos = min(worstCos, dot)
		if same {
			exact++
		}
	}
	t.Logf("%d texts: %d bit for bit, largest difference %.3g, lowest cosine %.9f", len(texts), exact, worstAbs, worstCos)
	if worstAbs > 1e-4 || worstCos < 0.99999 {
		t.Errorf("vectors outside tolerance")
	}
}
