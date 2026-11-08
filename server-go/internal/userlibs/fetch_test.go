//go:build !windows

package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The downloads against indexer/fetch_embed.py itself, on a local server:
// the same requests, the same files in the cache and the same message on a
// failure. Nothing here reaches the package index.

type fixture struct {
	srv   *httptest.Server
	files map[string][]byte // by path
	mu    sync.Mutex
	asked []string
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked = append(f.asked, r.Method+" "+r.URL.Path)
		body, ok := f.files[r.URL.Path]
		f.mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/partial/"):
			// Half the body, then the connection closes.
			conn, buf, _ := w.(http.Hijacker).Hijack()
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(body))
			buf.Write(body[:len(body)/2])
			buf.Flush()
			conn.Close()
		case strings.HasPrefix(r.URL.Path, "/stall/"):
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body[:10])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		case !ok:
			http.NotFound(w, r)
		default:
			w.Write(body)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// pin serves body at path and answers its pin; served, when given, is what
// is served in its place.
func (f *fixture) pin(path string, body []byte, served ...[]byte) map[string]any {
	f.files[path] = body
	if len(served) > 0 {
		f.files[path] = served[0]
	}
	return map[string]any{"url": f.srv.URL + path, "sha256": digest(body), "size": len(body), "version": "1"}
}

func blob(name string, n int) []byte {
	return []byte(strings.Repeat(name+" ", n))
}

const plat = "linux-x86_64-cp312"

// project is a project root with fetch_embed.py and these pins.
func project(t *testing.T, pins map[string]any) string {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "indexer"), 0o755)
	src, err := os.ReadFile("../../../indexer/fetch_embed.py")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "indexer", "fetch_embed.py"), src, 0o755)
	b, _ := json.Marshal(pins)
	os.WriteFile(filepath.Join(root, "indexer", "embed_deps.json"), b, 0o644)
	return root
}

// standardPins are three wheels, two model files and two readers; change
// adjusts them before they are written.
func standardPins(f *fixture, change func(wheels, model, readers map[string]any)) map[string]any {
	wheels := map[string]any{}
	model := map[string]any{}
	readers := map[string]any{}
	order := func(m map[string]any, keys ...string) orderedMap { return orderedMap{m, keys} }
	wheels["onnxruntime"] = f.pin("/w/onnxruntime-1-cp312.whl", blob("ort", 4000))
	wheels["tokenizers"] = f.pin("/w/tokenizers-1-abi3.whl", blob("tok", 900))
	wheels["numpy"] = f.pin("/w/numpy-1-cp312.whl", blob("np", 3000))
	model["model.onnx"] = f.pin("/m/model_qint8_avx512_vnni.onnx", blob("model", 5000))
	model["tokenizer.json"] = f.pin("/m/tokenizer.json", blob("vocab", 200))
	readers["xlrd"] = f.pin("/r/xlrd-2-py2.py3-none-any.whl", blob("xlrd", 300))
	readers["pypdf"] = f.pin("/r/pypdf-6-py3-none-any.whl", blob("pypdf", 700))
	if change != nil {
		change(wheels, model, readers)
	}
	return map[string]any{
		"packages": map[string]any{"onnxruntime": "1", "tokenizers": "1", "numpy": "1"},
		"wheels":   map[string]any{plat: order(wheels, "onnxruntime", "tokenizers", "numpy")},
		"model":    map[string]any{"id": "minilm", "files": order(model, "model.onnx", "tokenizer.json")},
		"readers":  order(readers, "xlrd", "pypdf"),
	}
}

// orderedMap writes its keys in the order given, as embed_deps.json does.
type orderedMap struct {
	m    map[string]any
	keys []string
}

func (o orderedMap) MarshalJSON() ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, err := json.Marshal(o.m[k])
		if err != nil {
			return nil, err
		}
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

func python3(t *testing.T) string {
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 to compare with")
	}
	return p
}

// runScript runs fetch_embed.py, its process group killed after a minute; it
// answers the message it stopped with, "" when it did not.
func runScript(t *testing.T, py, root, cache string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, append([]string{"-I", filepath.Join(root, "indexer", "fetch_embed.py")}, append(args, "--cache", cache)...)...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var errb strings.Builder
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok || ctx.Err() != nil {
			t.Fatalf("fetch_embed.py: %v", err)
		}
		return strings.TrimSpace(errb.String())
	}
	return ""
}

// cacheFiles are a cache's files and their digests.
func cacheFiles(t *testing.T, dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		st, _ := e.Info()
		out[e.Name()] = fmt.Sprintf("%s %o", digest(b), st.Mode().Perm())
	}
	return out
}

func moduleAt(t *testing.T, root string) *Module {
	dir := t.TempDir()
	pool := remote.New(remote.Options{IndexBase: dir, Getenv: func(string) string { return "" }})
	t.Cleanup(pool.Close)
	return New(nil, pool, nil, dir, root, nil)
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDownloadsMatchTheScript(t *testing.T) {
	py := python3(t)
	type run struct {
		readers bool
	}
	cases := []struct {
		name   string
		change func(f *fixture, wheels, model, readers map[string]any)
		seed   func(f *fixture, cache string, pins map[string]any)
		runs   []run
		fails  bool
	}{
		{name: "an empty cache", runs: []run{{false}, {true}}},
		{name: "a full cache asks for nothing", runs: []run{{false}, {true}, {false}, {true}}},
		{name: "a damaged file and leftover parts", runs: []run{{false}},
			seed: func(f *fixture, cache string, pins map[string]any) {
				w := pins["wheels"].(map[string]any)[plat].(orderedMap).m
				os.MkdirAll(cache, 0o755)
				ort, np, tok := w["onnxruntime"].(map[string]any), w["numpy"].(map[string]any), w["tokenizers"].(map[string]any)
				os.WriteFile(filepath.Join(cache, pinName(ort)), []byte("damaged"), 0o644)
				os.WriteFile(filepath.Join(cache, pinName(np)), blob("np", 3000), 0o644)
				os.WriteFile(filepath.Join(cache, pinName(np)+".part"), []byte("left over"), 0o644)
				os.WriteFile(filepath.Join(cache, pinName(tok)+".part"), []byte("left over"), 0o644)
			}},
		{name: "a pin mismatch stops", fails: true, runs: []run{{false}},
			change: func(f *fixture, wheels, model, readers map[string]any) {
				wheels["tokenizers"] = f.pin("/w/tokenizers-1-abi3.whl", blob("tok", 900), blob("tampered", 900))
			}},
		{name: "a partial download stops", fails: true, runs: []run{{false}},
			change: func(f *fixture, wheels, model, readers map[string]any) {
				model["model.onnx"] = f.pin("/partial/model_qint8_avx512_vnni.onnx", blob("model", 5000))
			}},
		{name: "a missing file stops", fails: true, runs: []run{{true}},
			change: func(f *fixture, wheels, model, readers map[string]any) {
				p := f.pin("/r/xlrd-2-py2.py3-none-any.whl", blob("xlrd", 300))
				delete(f.files, "/r/xlrd-2-py2.py3-none-any.whl")
				readers["xlrd"] = p
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			var change func(w, m, r map[string]any)
			if c.change != nil {
				change = func(w, m, r map[string]any) { c.change(f, w, m, r) }
			}
			pins := standardPins(f, change)
			root := project(t, pins)
			pyCache, goCache := filepath.Join(t.TempDir(), "c"), filepath.Join(t.TempDir(), "c")
			if c.seed != nil {
				c.seed(f, pyCache, pins)
				c.seed(f, goCache, pins)
			}
			m := moduleAt(t, root)
			failed := false
			for i, r := range c.runs {
				args := []string{"--download", "--platform", plat}
				if r.readers {
					args = []string{"--download-readers"}
				}
				pyMsg := runScript(t, py, root, pyCache, args...)
				pyAsked := f.take()
				var err error
				if r.readers {
					err = m.downloadReaders(context.Background(), goCache)
				} else {
					err = m.downloadPlatform(context.Background(), plat, goCache)
				}
				goAsked := f.take()
				if errText(err) != pyMsg {
					t.Errorf("run %d: Go stopped with %q, the script with %q", i, errText(err), pyMsg)
				}
				if !reflect.DeepEqual(goAsked, pyAsked) {
					t.Errorf("run %d: Go asked\n%v\nthe script asked\n%v", i, goAsked, pyAsked)
				}
				if got, want := cacheFiles(t, goCache), cacheFiles(t, pyCache); !reflect.DeepEqual(got, want) {
					t.Errorf("run %d: Go's cache\n%v\nthe script's\n%v", i, keysOf(got), keysOf(want))
				}
				failed = failed || pyMsg != ""
			}
			if failed != c.fails {
				t.Errorf("failed %v, want %v", failed, c.fails)
			}
		})
	}
}

func pinName(p map[string]any) string {
	u := p["url"].(string)
	return p["sha256"].(string)[:16] + "-" + u[strings.LastIndex(u, "/")+1:]
}

func keysOf(m map[string]string) []string {
	out := []string{}
	for k, v := range m {
		out = append(out, k+" "+v)
	}
	sort.Strings(out)
	return out
}

func TestDownloadStopsWhenNothingComes(t *testing.T) {
	f := newFixture(t)
	pins := standardPins(f, func(w, m, r map[string]any) {
		r["xlrd"] = f.pin("/stall/xlrd-2-py2.py3-none-any.whl", blob("xlrd", 300))
	})
	m := moduleAt(t, project(t, pins))
	was := fetchIdle
	fetchIdle = 300 * time.Millisecond
	defer func() { fetchIdle = was }()
	cache := filepath.Join(t.TempDir(), "c")
	start := time.Now()
	err := m.downloadReaders(context.Background(), cache)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v, want a time-out", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
	if got := cacheFiles(t, cache); len(got) != 0 {
		t.Errorf("left %v", got)
	}
	if got := f.take(); len(got) != 1 {
		t.Errorf("asked %v, want only the stalled file", got)
	}
}

func TestDownloadForAnUnknownPlatform(t *testing.T) {
	f := newFixture(t)
	m := moduleAt(t, project(t, standardPins(f, nil)))
	err := m.downloadPlatform(context.Background(), "plan9-mips-cp27", t.TempDir())
	if errText(err) != "no pinned wheels for plan9-mips-cp27" {
		t.Errorf("got %v", err)
	}
	if got := f.take(); len(got) != 0 {
		t.Errorf("asked %v", got)
	}
}
