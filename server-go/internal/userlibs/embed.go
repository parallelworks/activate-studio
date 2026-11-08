package userlibs

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// What a build on another system needs from the Studio, sent while the
// first build that needs it runs, so the build there runs no Python and
// downloads nothing:
//
//   - the tools: the Studio's helper (cmd/studio-remote) for the system's
//     platform, which runs the text pass and the embedder, in
//     ~/.activate-studio/go/<version>;
//   - the embedding kit: ONNX Runtime's library, taken from the
//     onnxruntime wheel indexer/embed_deps.json pins, and the model files
//     it pins, in ~/.activate-studio/embed/<version>.
//
// Each is sent to <folder>.files with a SHA256SUMS file, which the build
// there checks with the helper before it takes the folder into use.

// helperPlatforms are the platforms a helper is built for, by `uname -s`
// and `uname -m` there.
var helperPlatforms = map[string]string{
	"Linux x86_64": "linux-amd64", "Linux amd64": "linux-amd64",
	"Linux aarch64": "linux-arm64", "Linux arm64": "linux-arm64",
}

// wheelPlatforms are the platforms of embed_deps.json's wheels whose
// ONNX Runtime a helper platform loads.
var wheelPlatforms = map[string]string{"linux-amd64": "linux-x86_64-", "linux-arm64": "linux-aarch64-"}

// helperPlatform is a system's platform as the helpers are named, "" for
// one there is no helper for.
func helperPlatform(uname string) string { return helperPlatforms[strings.TrimSpace(uname)] }

const helperName = "studio-remote"

// helperPath is the helper for a platform here: in the folder
// STUDIO_REMOTE_HELPERS names, beside the server (the packages' bin/remote),
// or in a checkout's server-go/bin/remote; "" for none.
func (m *Module) helperPath(platform string) string {
	if platform == "" {
		return ""
	}
	var dirs []string
	if m.Helpers != "" {
		dirs = append(dirs, m.Helpers)
	}
	if exe, err := os.Executable(); err == nil {
		if r, err := filepath.EvalSymlinks(exe); err == nil {
			exe = r
		}
		d := filepath.Dir(exe)
		dirs = append(dirs, filepath.Join(d, "remote"), filepath.Join(d, "..", "bin", "remote"))
	}
	if m.ProjectRoot != "" {
		dirs = append(dirs, filepath.Join(m.ProjectRoot, "server-go", "bin", "remote"))
	}
	for _, d := range dirs {
		p := filepath.Join(d, platform, helperName)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// sendFile is one file to send: its name there, its digest, its path here.
type sendFile struct {
	Name, SHA256, Path string
	Size               int64
}

// digestOf is a file's SHA-256, kept while its size and time stand.
func (m *Module) digestOf(p string) (string, int64, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", 0, err
	}
	stamp := fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
	m.mu.Lock()
	kept, ok := m.digests[p]
	m.mu.Unlock()
	if ok && kept[0] == stamp {
		return kept[1], st.Size(), nil
	}
	sum, err := fileSHA256(p)
	if err != nil {
		return "", 0, err
	}
	m.mu.Lock()
	m.digests[p] = [2]string{stamp, sum}
	m.mu.Unlock()
	return sum, st.Size(), nil
}

// toolsVersion is the folder name of a platform's helper there: the
// platform and the start of the helper's digest; "" with no helper.
func (m *Module) toolsVersion(platform string) string {
	p := m.helperPath(platform)
	if p == "" {
		return ""
	}
	sum, _, err := m.digestOf(p)
	if err != nil {
		return ""
	}
	return platform + "-" + sum[:12]
}

func (m *Module) toolsMegabytes(platform string) float64 {
	st, err := os.Stat(m.helperPath(platform))
	if err != nil {
		return 0
	}
	return math.Floor(float64(st.Size())/1e6 + 0.5)
}

// ---- the embedding kit ----

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

// ortWheel is the pinned onnxruntime wheel whose library a helper platform
// loads: of embed_deps.json's wheels for its system and machine (one per
// Python, each with the same release of the library), the first here,
// else the first; nil for none.
func (m *Module) ortWheel(platform string) *jsonv.Object {
	prefix := wheelPlatforms[platform]
	wheels := jsonv.Obj(get(m.pins(), "wheels"))
	if prefix == "" || wheels == nil {
		return nil
	}
	var first *jsonv.Object
	for _, k := range wheels.Keys() {
		if strings.HasPrefix(k, prefix) {
			if w := jsonv.Obj(get(jsonv.Obj(get(wheels, k)), "onnxruntime")); w != nil {
				if m.pinnedHere(w, "") != "" {
					return w
				}
				if first == nil {
					first = w
				}
			}
		}
	}
	return first
}

func (m *Module) modelPins() *jsonv.Object {
	return jsonv.Obj(get(jsonv.Obj(get(m.pins(), "model")), "files"))
}

func digest12(v any) string {
	b, _ := jsonv.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])[:12]
}

// embedVersion is the folder name of a platform's embedding kit there: the
// platform and a digest of the wheel and model pins; "" with none.
func (m *Module) embedVersion(platform string) string {
	w := m.ortWheel(platform)
	if w == nil || m.modelPins() == nil {
		return ""
	}
	return platform + "-" + digest12(jsonv.NewObject("onnxruntime", w, "model", get(m.pins(), "model")))
}

// embedMegabytes is about what a first build sends of the kit: the wheel's
// size stands for its library's.
func (m *Module) embedMegabytes(platform string) float64 {
	n := 0.0
	if w := m.ortWheel(platform); w != nil {
		n, _ = get(w, "size").(float64)
	}
	if files := m.modelPins(); files != nil {
		for _, k := range files.Keys() {
			s, _ := get(jsonv.Obj(get(files, k)), "size").(float64)
			n += s
		}
	}
	return math.Floor(n/1e6 + 0.5)
}

func (m *Module) wheelDirs() []string {
	return []string{filepath.Join(m.ProjectRoot, "indexer", "wheels"), m.embedCache()}
}

func (m *Module) embedCache() string { return filepath.Join(m.IndexBase, "embed-wheels") }

func (m *Module) onnxDir() string { return filepath.Join(m.IndexBase, "models", "minilm-l6") }

// pinnedHere is where a pinned file is here, checked against its digest: the
// Studio's wheel folders, then (for a model file) its own model folder;
// "" for none.
func (m *Module) pinnedHere(p *jsonv.Object, local string) string {
	want := jsonv.String(get(p, "sha256"))
	check := func(path string) bool {
		sum, _, err := m.digestOf(path)
		return err == nil && sum == want
	}
	for _, d := range m.wheelDirs() {
		if path := filepath.Join(d, cacheName(p)); check(path) {
			return path
		}
	}
	if local != "" {
		if path := filepath.Join(m.onnxDir(), local); check(path) {
			return path
		}
	}
	return ""
}

// have is a pinned file here, downloaded into the Studio's cache when it
// is not (fetch_embed.py --download's step for one file).
func (m *Module) have(ctx context.Context, p *jsonv.Object, local string) (string, error) {
	if path := m.pinnedHere(p, local); path != "" {
		return path, nil
	}
	if m.offline != nil {
		return "", fmt.Errorf("%s", m.offline(""))
	}
	fetchMu.Lock()
	err := fetchPinned(ctx, p, m.embedCache())
	fetchMu.Unlock()
	if err != nil {
		return "", err
	}
	return filepath.Join(m.embedCache(), cacheName(p)), nil
}

// ortLibrary is ONNX Runtime's library from the wheel, unpacked beside it
// once: <wheel digest>-libonnxruntime.so.
var ortMember = regexp.MustCompile(`^onnxruntime/capi/libonnxruntime\.so\.\d[\d.]*$`)

func (m *Module) ortLibrary(wheel, wheelSHA string) (string, error) {
	dest := filepath.Join(m.embedCache(), wheelSHA[:16]+"-libonnxruntime.so")
	z, err := zip.OpenReader(wheel)
	if err != nil {
		return "", err
	}
	defer z.Close()
	for _, f := range z.File {
		if !ortMember.MatchString(f.Name) {
			continue
		}
		if st, err := os.Stat(dest); err == nil && st.Size() == int64(f.UncompressedSize64) {
			return dest, nil
		}
		r, err := f.Open()
		if err != nil {
			return "", err
		}
		defer r.Close()
		if err := os.MkdirAll(m.embedCache(), 0o777); err != nil {
			return "", err
		}
		tmp := dest + ".part"
		out, err := os.Create(tmp)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(out, r) // the zip reader checks the member's CRC-32
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
			return "", err
		}
		return dest, os.Rename(tmp, dest)
	}
	return "", fmt.Errorf("%s holds no ONNX Runtime library", filepath.Base(wheel))
}

// embedKit is the kit's files for a platform, downloading and unpacking
// what is not here yet.
func (m *Module) embedKit(ctx context.Context, platform string) ([]sendFile, error) {
	w := m.ortWheel(platform)
	if w == nil {
		return nil, fmt.Errorf("no ONNX Runtime is pinned for %s", platform)
	}
	wheel, err := m.have(ctx, w, "")
	if err != nil {
		return nil, err
	}
	lib, err := m.ortLibrary(wheel, jsonv.String(get(w, "sha256")))
	if err != nil {
		return nil, err
	}
	files := []sendFile{{Name: "libonnxruntime.so", Path: lib}}
	model := m.modelPins()
	for _, k := range model.Keys() {
		p, err := m.have(ctx, jsonv.Obj(get(model, k)), k)
		if err != nil {
			return nil, err
		}
		files = append(files, sendFile{Name: k, Path: p})
	}
	return m.digested(files)
}

func (m *Module) digested(files []sendFile) ([]sendFile, error) {
	for i := range files {
		sum, size, err := m.digestOf(files[i].Path)
		if err != nil {
			return nil, err
		}
		files[i].SHA256, files[i].Size = sum, size
	}
	return files, nil
}

// toolsKit is the helper for a platform.
func (m *Module) toolsKit(platform string) ([]sendFile, error) {
	p := m.helperPath(platform)
	if p == "" {
		return nil, fmt.Errorf("the Studio has no helper for %s", platform)
	}
	return m.digested([]sendFile{{Name: helperName, Path: p}})
}

// sums is the SHA256SUMS file of a kit, as sha256sum writes one.
func sums(files []sendFile) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s  %s\n", f.SHA256, f.Name)
	}
	return b.String()
}

// sendTools sends a build there the helper, which its text pass waits
// for: BUILD_START marked the version as being sent, and the build waits
// for that mark to go. One sending per system and version at a time.
func (m *Module) sendTools(site, key, platform, version string) {
	m.queued(slice16(key)+"|"+site+"|go/"+version, func() {
		m.send(site, key, "go/"+version, func(context.Context) ([]sendFile, error) { return m.toolsKit(platform) }, "the Studio's helper for "+platform)
	})
}

// sendEmbed is the same for the embedding kit, which its meaning phase
// waits for.
func (m *Module) sendEmbed(site, key, platform, version string) {
	m.queued(slice16(key)+"|"+site+"|embed/"+version, func() {
		m.send(site, key, "embed/"+version, func(ctx context.Context) ([]sendFile, error) { return m.embedKit(ctx, platform) },
			"ONNX Runtime and the embedding model for "+platform)
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

func (m *Module) send(site, key, folder string, prepare func(context.Context) ([]sendFile, error), what string) {
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
	pctx, cancel := context.WithTimeout(ctx, fetchTotal)
	files, err := prepare(pctx)
	cancel()
	if err != nil {
		fail(err.Error())
		return
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
	put := func(name string, body io.Reader) bool {
		r, err := m.Pool.RunOnce(ctx, site, key, []string{"sh", "-c", embedPut, "_", folder, name}, 30*time.Minute, body)
		if err != nil {
			fail(err.Error())
			return false
		}
		if r.Code != 0 {
			why := lastOf(r.Stderr)
			if why == "" {
				why = fmt.Sprintf("exit %d", r.Code)
			}
			fail(fmt.Sprintf("%s could not be written there: %s", name, why))
			return false
		}
		return true
	}
	for _, f := range files {
		if n, ok := there[f.Name]; ok && int64(n) == f.Size {
			continue
		}
		fh, err := os.Open(f.Path)
		if err != nil {
			fail(err.Error())
			return
		}
		ok := put(f.Name, fh)
		fh.Close()
		if !ok {
			return
		}
		m.mu.Lock()
		m.sent++
		m.mu.Unlock()
	}
	// Last, so the folder there is whole once it is.
	put("SHA256SUMS", strings.NewReader(sums(files)))
}
