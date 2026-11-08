package core

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/stepmesh"
)

// 3D previews (model.ts): STL is meshed here and the geometry goes to the
// browser as binary, little endian throughout:
//
//	uint32   header length in bytes
//	bytes    that many bytes of UTF-8 JSON, padded with spaces to a multiple of 4
//	bytes    the arrays, back to back, in the order the header names them
//
// The cache (INDEX_BASE/model-cache/<path>.mdl) is the same file Node
// writes, byte for byte, so either server serves the other's. STEP is
// meshed by OpenCascade: occt-import-js's WebAssembly, as Node runs it, here
// under wazero (internal/stepmesh) from the module scripts/occt/prepare.mjs
// prepares (STUDIO_OCCT_WASM, else ProjectRoot/server-go/occt/
// occt-import-js.wasm). Without that module a STEP file not in the cache
// goes to Node while Node runs, and answers 501 without it.

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
	return stepmesh.Encode("stl", float64(len(pos))/9, []stepmesh.Mesh{{Positions: pos}})
}

// OcctWasm is the prepared occt-import-js module.
func OcctWasm(projectRoot string) string {
	if p := os.Getenv("STUDIO_OCCT_WASM"); p != "" {
		return p
	}
	return filepath.Join(projectRoot, "server-go", "occt", "occt-import-js.wasm")
}

// stepMesher is the STEP mesher, made at the first STEP file; nil (and
// the reason logged once) when the module is not there or does not load.
type stepMesher struct {
	mu   sync.Mutex
	m    *stepmesh.Mesher
	err  error
	done bool
}

func (c *Core) mesher() (*stepmesh.Mesher, error) {
	c.step.mu.Lock()
	defer c.step.mu.Unlock()
	if !c.step.done {
		c.step.done = true
		wasm := OcctWasm(c.a.Cfg.ProjectRoot)
		start := time.Now()
		c.step.m, c.step.err = stepmesh.New(context.Background(), wasm, filepath.Join(c.a.Cfg.IndexBase, "wasm-cache"))
		if c.step.err != nil {
			c.log.Warn("model: STEP meshing unavailable", "wasm", wasm, "err", c.step.err)
		} else {
			c.step.m.Log = func(line string) { c.log.Debug("occt-import-js", "out", line) }
			c.log.Info("model: STEP mesher ready", "package", c.step.m.Package(), "ms", time.Since(start).Milliseconds())
		}
	}
	return c.step.m, c.step.err
}

// stepTimeout bounds one STEP file's meshing; Node has no bound.
const stepTimeout = 10 * time.Minute

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
	var data []byte
	if suffix == ".stl" {
		buf, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		pos, err := ParseSTL(buf)
		if err != nil {
			return err
		}
		data = EncodeSTL(pos)
	} else {
		m, merr := c.mesher()
		if merr != nil {
			c.a.Node().ServeHTTP(w, app.HandOff(r, "STEP meshing: no prepared occt-import-js module ("+OcctWasm(c.a.Cfg.ProjectRoot)+")"))
			return nil
		}
		buf, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		// The file is meshed and cached whether or not the client waits, as
		// Node does.
		ctx, cancel := context.WithTimeout(contextWithoutCancel(r), stepTimeout)
		defer cancel()
		meshes, err := m.ReadStep(ctx, buf)
		if errors.Is(err, stepmesh.ErrUnconvertible) {
			return apierr.New(422, "could not convert STEP geometry")
		}
		if err != nil {
			return err
		}
		data = stepmesh.Encode("step", stepmesh.Triangles(meshes), meshes)
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return err
	}
	tmp := cached + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
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
