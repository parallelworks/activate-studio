package userlibs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// The Studio's ONNX embedding model and document readers for a build on
// another system (remoteEmbed.ts): the files indexer/embed_deps.json pins,
// sent from here while the first build that needs them runs.

type embedFile struct {
	Name   string
	SHA256 string
	Size   float64
	// Local is the name of a model file in the Studio's own model folder.
	Local string
}

func (f embedFile) json() *jsonv.Object {
	o := jsonv.NewObject("name", f.Name, "sha256", f.SHA256, "size", f.Size)
	if f.Local != "" {
		o.Set("local", f.Local)
	}
	return o
}

type pinsFile struct {
	once sync.Once
	doc  *jsonv.Object
}

func (m *Module) pins() *jsonv.Object {
	m.pinned.once.Do(func() {
		b, err := os.ReadFile(filepath.Join(m.ProjectRoot, "indexer", "embed_deps.json"))
		if err == nil {
			if v, err := jsonv.Decode(b); err == nil {
				m.pinned.doc = jsonv.Obj(v)
			}
		}
		if m.pinned.doc == nil {
			m.pinned.doc = jsonv.NewObject()
		}
	})
	return m.pinned.doc
}

// cacheName is a file's name as fetch_embed.py's cache keeps it.
func cacheName(p *jsonv.Object) string {
	url := jsonv.String(get(p, "url"))
	return slice(jsonv.String(get(p, "sha256")), 16) + "-" + url[strings.LastIndex(url, "/")+1:]
}

func pinFile(p *jsonv.Object, local string) embedFile {
	size, _ := get(p, "size").(float64)
	return embedFile{Name: cacheName(p), SHA256: jsonv.String(get(p, "sha256")), Size: size, Local: local}
}

// embedPlatform is a platform as fetch_embed.py names it: linux-x86_64-cp312.
var embedPlatform = regexp.MustCompile(`^[a-z0-9]+-[a-z0-9_]+-cp\d{2,3}$`)

func (m *Module) wheelsFor(platform string) *jsonv.Object {
	if !embedPlatform.MatchString(platform) {
		return nil
	}
	return jsonv.Obj(get(jsonv.Obj(get(m.pins(), "wheels")), platform))
}

// embedFiles are the files a build on this platform needs, the wheels and
// then the model's; nil when no wheels are pinned for it.
func (m *Module) embedFiles(platform string) []embedFile {
	wheels := m.wheelsFor(platform)
	if wheels == nil {
		return nil
	}
	out := []embedFile{}
	for _, k := range wheels.Keys() {
		out = append(out, pinFile(jsonv.Obj(get(wheels, k)), ""))
	}
	files := jsonv.Obj(get(jsonv.Obj(get(m.pins(), "model")), "files"))
	if files != nil {
		for _, k := range files.Keys() {
			out = append(out, pinFile(jsonv.Obj(get(files, k)), k))
		}
	}
	return out
}

func digest12(v any) string {
	b, _ := jsonv.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:12]
}

// embedVersion is the folder name of a platform's model and packages
// there: the platform and a digest of their pins.
func (m *Module) embedVersion(platform string) string {
	wheels := m.wheelsFor(platform)
	if wheels == nil {
		return ""
	}
	return platform + "-" + digest12(jsonv.NewObject("wheels", wheels, "model", get(m.pins(), "model")))
}

func (m *Module) readerFiles() []embedFile {
	out := []embedFile{}
	readers := jsonv.Obj(get(m.pins(), "readers"))
	if readers != nil {
		for _, k := range readers.Keys() {
			out = append(out, pinFile(jsonv.Obj(get(readers, k)), ""))
		}
	}
	return out
}

func (m *Module) readersVersion() string { return digest12(get(m.pins(), "readers")) }

// embedMegabytes is what a first build sends to a system of this platform.
func (m *Module) embedMegabytes(platform string) float64 {
	n := 0.0
	for _, f := range m.embedFiles(platform) {
		n += f.Size
	}
	return math.Floor(n/1e6 + 0.5)
}

func (m *Module) wheelDirs() []string {
	return []string{filepath.Join(m.ProjectRoot, "indexer", "wheels"), filepath.Join(m.IndexBase, "embed-wheels")}
}

func (m *Module) onnxDir() string { return filepath.Join(m.IndexBase, "models", "minilm-l6") }

// holds is whether p is f, checked against its digest once while its size
// and time stand.
func (m *Module) holds(p string, f embedFile) bool {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || float64(st.Size()) != f.Size {
		return false
	}
	stamp := fmt.Sprintf("%s:%d:%d", f.SHA256, st.Size(), st.ModTime().UnixNano())
	m.mu.Lock()
	seen := m.checked[p] == stamp
	m.mu.Unlock()
	if seen {
		return true
	}
	fh, err := os.Open(p)
	if err != nil {
		return false
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return false
	}
	m.mu.Lock()
	m.checked[p] = stamp
	m.mu.Unlock()
	return true
}

// findLocal is where each file is here, "" for one that is not.
func (m *Module) findLocal(files []embedFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		for _, d := range m.wheelDirs() {
			if p := filepath.Join(d, f.Name); m.holds(p, f) {
				out[i] = p
				break
			}
		}
		if out[i] == "" && f.Local != "" {
			if p := filepath.Join(m.onnxDir(), f.Local); m.holds(p, f) {
				out[i] = p
			}
		}
	}
	return out
}

// download fetches what is missing here into the Studio's own folder with
// fetch_embed.py; why not, or "".
func (m *Module) download(what []string) string {
	args := append([]string{filepath.Join(m.ProjectRoot, "indexer", "fetch_embed.py")}, what...)
	args = append(args, "--cache", filepath.Join(m.IndexBase, "embed-wheels"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.python(), args...)
	var errb strings.Builder
	cmd.Stderr = &tail{b: &errb, max: 1000}
	err := cmd.Run()
	if err == nil {
		return ""
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if last := lastOf(errb.String()); last != "" {
			return last
		}
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return err.Error()
}

// tail keeps the last max bytes written.
type tail struct {
	b   *strings.Builder
	max int
}

func (t *tail) Write(p []byte) (int, error) {
	s := t.b.String() + string(p)
	if len(s) > t.max {
		s = s[len(s)-t.max:]
	}
	t.b.Reset()
	t.b.WriteString(s)
	return len(p), nil
}

// sendEmbed sends a build there what it needs to embed with the ONNX
// model and does not have, while the build runs: BUILD_START marked the
// version as being sent, and the build waits for that mark to go before
// its meaning phase. One sending per system and version at a time.
func (m *Module) sendEmbed(site, key, platform, version string) {
	files := m.embedFiles(platform)
	if files == nil {
		return
	}
	m.queued(slice16(key)+"|"+site+"|"+version, func() {
		m.send(site, key, "embed/"+version, files, []string{"--download", "--platform", platform}, "the ONNX packages for "+platform)
	})
}

// sendReaders is the same for the document readers, which the text pass
// waits for.
func (m *Module) sendReaders(site, key, version string) {
	m.queued(slice16(key)+"|"+site+"|readers/"+version, func() {
		m.send(site, key, "readers/"+version, m.readerFiles(), []string{"--download-readers"}, "the document readers")
	})
}

// slice16 is key.slice(-16).
func slice16(key string) string {
	if len(key) <= 16 {
		return key
	}
	return key[len(key)-16:]
}

func (m *Module) queued(k string, job func()) {
	m.mu.Lock()
	l := m.sending[k]
	if l == nil {
		l = &sync.Mutex{}
		m.sending[k] = l
	}
	m.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	job()
}

func (m *Module) send(site, key, folder string, files []embedFile, fetchArgs []string, what string) {
	ctx := context.Background()
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.Pool.Run(ctx, site, key, []string{"sh", "-c", embedTouch, "_", folder}, 30*time.Second)
			}
		}
	}()
	defer func() {
		close(stop)
		m.Pool.Run(ctx, site, key, []string{"sh", "-c", embedDone, "_", folder}, 30*time.Second)
	}()
	fail := func(msg string) {
		m.Log.Warn(fmt.Sprintf("a build on %s: sending %s failed: %s", remote.SiteName(site), what, msg))
	}
	local := m.findLocal(files)
	if missing(local) {
		if why := m.download(fetchArgs); why != "" {
			m.Log.Warn(fmt.Sprintf("a build on %s: %s could not be downloaded here (%s); the build there downloads them itself where it can", remote.SiteName(site), what, why))
		}
		local = m.findLocal(files)
	}
	listed, err := m.Pool.Run(ctx, site, key, []string{"sh", "-c", embedList, "_", folder}, 60*time.Second)
	if err != nil {
		fail(err.Error())
		return
	}
	there := map[string]float64{}
	line := regexp.MustCompile(`^(\d+) (.+)$`)
	for _, l := range strings.Split(listed.Stdout, "\n") {
		if g := line.FindStringSubmatch(jsTrim(l)); g != nil {
			there[g[2]] = number(g[1])
		}
	}
	for i, f := range files {
		p := local[i]
		if n, ok := there[f.Name]; p == "" || (ok && n == f.Size) {
			continue
		}
		fh, err := os.Open(p)
		if err != nil {
			fail(err.Error())
			return
		}
		r, err := m.Pool.RunOnce(ctx, site, key, []string{"sh", "-c", embedPut, "_", folder, f.Name}, 30*time.Minute, fh)
		fh.Close()
		if err != nil {
			fail(err.Error())
			return
		}
		if r.Code != 0 {
			why := lastOf(r.Stderr)
			if why == "" {
				why = fmt.Sprintf("exit %d", r.Code)
			}
			fail(fmt.Sprintf("%s could not be written there: %s", f.Name, why))
			return
		}
		if strings.HasPrefix(folder, "embed/") {
			m.mu.Lock()
			m.embedSent++
			m.mu.Unlock()
		}
	}
}

func missing(paths []string) bool {
	for _, p := range paths {
		if p == "" {
			return true
		}
	}
	return false
}
