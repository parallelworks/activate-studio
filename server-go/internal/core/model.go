package core

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

// 3D previews (model.ts): STL is meshed here and the geometry goes to the
// browser as binary, little endian throughout:
//
//	uint32   header length in bytes
//	bytes    that many bytes of UTF-8 JSON, padded with spaces to a multiple of 4
//	bytes    the arrays, back to back, in the order the header names them
//
// The cache (INDEX_BASE/model-cache/<path>.mdl) is the same file Node
// writes, so either server serves the other's. STEP needs OpenCascade
// (occt-import-js, WebAssembly, in Node); a STEP file not in the cache goes
// to Node while Node runs, and answers 501 without it.

// MaxModelBytes is model.ts's MAX_MODEL_BYTES.
const MaxModelBytes = 80 * 1024 * 1024

var modelSuffixes = map[string]bool{".stl": true, ".step": true, ".stp": true}

// ResolveKb is kb.ts's resolveKb: a client path inside base, or a 400.
func ResolveKb(rel, base string) (string, error) {
	cleaned := strings.TrimLeft(rel, "/")
	abs := filepath.Join(base, cleaned)
	if !filepath.IsAbs(abs) {
		abs, _ = filepath.Abs(abs)
	}
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return "", apierr.New(400, "path escapes the knowledge base root")
	}
	return abs, nil
}

func (c *Core) modelCache(rel string) string {
	return filepath.Join(c.a.Cfg.IndexBase, "model-cache", rel+".mdl")
}

// ParseSTL is model.ts's parseStl: binary when the size says so, else
// ASCII; positions only (STL shares no vertices and its normals add
// nothing a flat-shaded mesh needs).
func ParseSTL(buf []byte) ([]float32, error) {
	var pos []float32
	if len(buf) >= 84 && 84+int(binary.LittleEndian.Uint32(buf[80:]))*50 == len(buf) {
		count := int(binary.LittleEndian.Uint32(buf[80:]))
		pos = make([]float32, count*9)
		for i := 0; i < count; i++ {
			off := 84 + i*50
			for v := 0; v < 3; v++ {
				vo := off + 12 + v*12
				j := i*9 + v*3
				pos[j] = math.Float32frombits(binary.LittleEndian.Uint32(buf[vo:]))
				pos[j+1] = math.Float32frombits(binary.LittleEndian.Uint32(buf[vo+4:]))
				pos[j+2] = math.Float32frombits(binary.LittleEndian.Uint32(buf[vo+8:]))
			}
		}
	} else {
		for _, line := range strings.Split(string(buf), "\n") {
			t := strings.TrimSpace(line)
			if !strings.HasPrefix(t, "vertex") {
				continue
			}
			p := strings.Fields(t)
			for k := 1; k <= 3; k++ {
				var f float64
				if k < len(p) {
					if v, err := strconv.ParseFloat(p[k], 64); err == nil {
						f = v
					}
				}
				pos = append(pos, float32(f))
			}
		}
	}
	if len(pos) == 0 {
		return nil, apierr.New(422, "could not parse STL geometry")
	}
	return pos, nil
}

// EncodeSTL is encodeModel for one STL mesh.
func EncodeSTL(pos []float32) []byte {
	span := jsonobj.New()
	span.SetValue("o", 0)
	span.SetValue("n", len(pos))
	mesh := jsonobj.New()
	mesh.SetValue("positions", span)
	h := jsonobj.New()
	h.SetValue("format", "stl")
	h.SetValue("triangles", len(pos)/9)
	h.SetValue("meshes", []any{mesh})
	js, _ := h.MarshalJSON()
	pad := (4 - len(js)%4) % 4
	var b bytes.Buffer
	b.Grow(4 + len(js) + pad + 4*len(pos))
	binary.Write(&b, binary.LittleEndian, uint32(len(js)+pad))
	b.Write(js)
	b.Write(bytes.Repeat([]byte{' '}, pad))
	for _, f := range pos {
		binary.Write(&b, binary.LittleEndian, math.Float32bits(f))
	}
	return b.Bytes()
}

func (c *Core) model(w http.ResponseWriter, r *http.Request) error {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		return apierr.New(400, "path required")
	}
	abs, err := ResolveKb(rel, c.a.Cfg.KBRoot)
	if err != nil {
		return err
	}
	suffix := strings.ToLower(filepath.Ext(abs))
	if !modelSuffixes[suffix] {
		return apierr.New(415, "not a supported 3D model type")
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if st.Size() > MaxModelBytes {
		mb := func(n int64) string { return strconv.Itoa(int(math.Round(float64(n)/(1024*1024)))) + " MB" }
		return apierr.New(413, "this model is %s and the inline preview stops at %s", mb(st.Size()), mb(MaxModelBytes))
	}
	cached := c.modelCache(rel)
	if cst, err := os.Stat(cached); err == nil && cst.ModTime().After(st.ModTime()) {
		return sendModel(w, cached)
	}
	if suffix != ".stl" {
		c.log.Info("model: STEP meshing is Node's (OpenCascade); handing the request on", "path", rel)
		c.a.Node().ServeHTTP(w, r)
		return nil
	}
	buf, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	pos, err := ParseSTL(buf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return err
	}
	tmp := cached + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, EncodeSTL(pos), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, cached); err != nil {
		return err
	}
	return sendModel(w, cached)
}

func sendModel(w http.ResponseWriter, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	_, err = io.Copy(w, f)
	return err
}
