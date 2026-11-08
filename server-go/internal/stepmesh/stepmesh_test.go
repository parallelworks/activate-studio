package stepmesh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The model-cache files Node's model.ts writes for occt-import-js's own
// test files (testdata/golden.json, from scripts/golden/stepmesh.mjs),
// against what this package and Encode make of the same files. Needs the
// prepared module (STUDIO_OCCT_WASM, scripts/occt/prepare.mjs) and the
// package's test files in server/node_modules.
func TestModelCacheMatchesNode(t *testing.T) {
	wasm := os.Getenv("STUDIO_OCCT_WASM")
	if wasm == "" {
		t.Skip("STUDIO_OCCT_WASM unset")
	}
	testfiles := "../../../server/node_modules/occt-import-js/test/testfiles"
	if _, err := os.Stat(testfiles); err != nil {
		t.Skip("occt-import-js's test files are not installed")
	}
	var golden map[string]struct {
		Bytes  int    `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	raw, _ := os.ReadFile("testdata/golden.json")
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m, err := New(ctx, wasm, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	for name, want := range golden {
		buf, err := os.ReadFile(filepath.Join(testfiles, name))
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		meshes, err := m.ReadStep(ctx, buf)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := Encode("step", Triangles(meshes), meshes)
		sum := sha256.Sum256(got)
		if len(got) != want.Bytes || hex.EncodeToString(sum[:]) != want.SHA256 {
			t.Errorf("%s: %d bytes %x, Node %d bytes %s", name, len(got), sum, want.Bytes, want.SHA256)
		}
		t.Logf("%s: %d meshes, %v", name, len(meshes), time.Since(start).Round(time.Millisecond))
	}
}

func TestEmvalHandles(t *testing.T) {
	e := newEmval()
	if e.toHandle(undefined) != 2 || e.toHandle(nil) != 4 || e.toHandle(true) != 6 || e.toHandle(false) != 8 {
		t.Fatal("reserved handles")
	}
	a := e.toHandle(1.5)
	b := e.toHandle("x")
	if a != 10 || b != 12 {
		t.Fatal(a, b)
	}
	e.incref(a)
	e.decref(a)
	if v, _ := e.toValue(a); v != 1.5 {
		t.Fatal("freed with a count left")
	}
	e.decref(a)
	if e.toHandle(2.5) != 10 {
		t.Fatal("free list not reused")
	}
	if _, err := e.toValue(0); err == nil {
		t.Fatal("handle 0")
	}
}

func TestProperties(t *testing.T) {
	arr := &jsArray{}
	setProperty(arr, 2.0, 7.0)
	if len(arr.items) != 3 || arr.items[0] != nil {
		t.Fatal("holes")
	}
	if v, _ := getProperty(arr, "length"); v != 3.0 {
		t.Fatal(v)
	}
	if v, _ := getProperty(arr, 0.0); v != undefined {
		t.Fatal("a hole reads as undefined")
	}
	obj := newObject()
	setProperty(obj, "b", 1.0)
	setProperty(obj, "a", arr)
	setProperty(obj, "b", 2.0)
	if len(obj.keys) != 2 || obj.keys[0] != "b" || obj.get("b") != 2.0 {
		t.Fatal(obj.keys)
	}
	if v, _ := getProperty(&bytesView{b: []byte{9, 8}}, 1.0); v != 8.0 {
		t.Fatal(v)
	}
	if _, err := getProperty(nil, "x"); err == nil {
		t.Fatal("null has no properties")
	}
	if toUint32(-1) != 4294967295 || toUint32(math.NaN()) != 0 || toUint32(4294967297) != 1 {
		t.Fatal("ToUint32")
	}
}

func TestEncode(t *testing.T) {
	m := []Mesh{
		{Name: `a "b"`, Color: []any{1.0, 0.25, nil}, Positions: []float32{0, 1, 2}, Normals: []float32{0, 0, 1}, Indices: []uint32{0, 0, 0}},
		{Positions: []float32{3}},
	}
	b := Encode("step", Triangles(m), m)
	n := int(b[0]) | int(b[1])<<8
	head := string(b[4 : 4+n])
	want := `{"format":"step","triangles":1,"meshes":[{"name":"a \"b\"","color":[1,0.25,null],"positions":{"o":0,"n":3},"normals":{"o":12,"n":3},"indices":{"o":24,"n":3}},{"positions":{"o":36,"n":1}}]}`
	for len(want)%4 != 0 {
		want += " "
	}
	if head != want || len(b) != 4+n+40 {
		t.Fatalf("%q", head)
	}
	if jsonNumber(1e21) != "1e+21" || jsonNumber(1e-7) != "1e-7" || jsonNumber(math.NaN()) != "null" || jsonNumber(2.0/3) != "0.6666666666666666" {
		t.Fatal("numbers")
	}
}

// The module with exception handling gives the same file as the one
// without, where neither throws; and a file that is not STEP is the 422 on
// either path.
func TestBothModulesAgree(t *testing.T) {
	wasm := os.Getenv("STUDIO_OCCT_WASM")
	buf, err := os.ReadFile("../../../server/node_modules/occt-import-js/test/testfiles/rounded-cube/rounded-cube.step")
	if wasm == "" || err != nil {
		t.Skip("STUDIO_OCCT_WASM unset or the test files not installed")
	}
	ctx := context.Background()
	m, err := New(ctx, wasm, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(ctx)
	if m.fast == nil {
		t.Fatal("no .noeh.wasm beside the module")
	}
	a, errA := m.read(ctx, m.fast, buf)
	b, errB := m.read(ctx, m.full, buf)
	if errA != nil || errB != nil || string(Encode("step", Triangles(a), a)) != string(Encode("step", Triangles(b), b)) {
		t.Fatal(errA, errB)
	}
	for _, junk := range [][]byte{[]byte("ISO-10303-21;\nHEADER;\nENDSEC;\nDATA;\n#1=CARTESIAN_POINT('',(0.,0.,"), {}} {
		if _, err := m.ReadStep(ctx, junk); err != ErrUnconvertible {
			t.Errorf("%q: %v", junk, err)
		}
	}
}
