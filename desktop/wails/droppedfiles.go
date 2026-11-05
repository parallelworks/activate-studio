package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// droppedSend is where the page asks the shell to send listed files.
const droppedSend = "/__shell/dropped-send"

// DroppedFile is one file under what was dropped on the window or chosen in
// its folder chooser: where it goes relative to the destination, and the id
// the page names it by.
type DroppedFile struct {
	Rel  string `json:"rel"`
	Size int64  `json:"size"`
	ID   string `json:"id"`
}

// droppedFiles sends files the page cannot reach on its own. WebKitGTK gives
// a page nothing of a drop from a file manager, no File objects and no paths,
// and its file chooser cannot pick a folder. So the shell takes the drop or
// asks for the folder itself and walks what it got, and when the page says
// which listed files to send, streams them to the ordinary upload. The bytes
// stay out of the web view, which holds a blob in memory and stalled on large
// folders. The page never names a path. An id names one listed file and is
// good for one send; nothing else on disk is reachable, including by other
// accounts on this computer, which can reach the proxy's port.
type droppedFiles struct {
	mu    sync.Mutex
	files map[string]string
}

func (d *droppedFiles) add(abs string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.files == nil {
		d.files = map[string]string{}
	}
	d.files[id] = abs
	return id, nil
}

// list walks absolute paths. Names starting with a dot are left out, as the
// upload refuses them, and links are not followed.
func (d *droppedFiles) list(paths []string) ([]DroppedFile, error) {
	out := []DroppedFile{}
	for _, root := range paths {
		root = filepath.Clean(root)
		parent := filepath.Dir(root)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable: the rest still goes
			}
			if strings.HasPrefix(e.Name(), ".") {
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.Type().IsRegular() {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(parent, p)
			if err != nil {
				return nil
			}
			id, err := d.add(p)
			if err != nil {
				return err
			}
			out = append(out, DroppedFile{Rel: filepath.ToSlash(rel), Size: info.Size(), ID: id})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// take gives the paths of listed files, each once, or false if any id is
// unknown, in which case none are spent.
func (d *droppedFiles) take(ids []string) ([]string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		abs, ok := d.files[id]
		if !ok {
			return nil, false
		}
		out = append(out, abs)
	}
	for _, id := range ids {
		delete(d.files, id)
	}
	return out, true
}

// writeForm writes the multipart form the upload route takes: where each file
// goes first, as the field ahead of the files, then the files.
func writeForm(form *multipart.Writer, abs, rels []string) error {
	paths, err := json.Marshal(rels)
	if err != nil {
		return err
	}
	if err := form.WriteField("paths", string(paths)); err != nil {
		return err
	}
	for i, p := range abs {
		if err := copyPart(form, p, path.Base(rels[i])); err != nil {
			return err
		}
	}
	return form.Close()
}

func copyPart(form *multipart.Writer, abs, name string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}

// sendDropped streams listed files to the upload route, as a form the page
// would have sent, and hands back the route's answer. The page's request
// carries its session and is what cancels: when the page aborts, the context
// ends and the stream stops.
func (p *Proxy) sendDropped(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Target string `json:"target"`
		Files  []struct {
			ID  string `json:"id"`
			Rel string `json:"rel"`
		} `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target, err := url.ParseRequestURI(req.Target)
	if err != nil || target.Path != "/api/kb/upload" || target.Host != "" {
		http.Error(w, "not an upload", http.StatusBadRequest)
		return
	}
	ids := make([]string, len(req.Files))
	rels := make([]string, len(req.Files))
	for i, f := range req.Files {
		ids[i], rels[i] = f.ID, f.Rel
	}
	abs, ok := p.dropped.take(ids)
	if !ok || len(abs) == 0 {
		http.NotFound(w, r)
		return
	}
	body, sink := io.Pipe()
	defer body.Close()
	form := multipart.NewWriter(sink)
	go func() { sink.CloseWithError(writeForm(form, abs, rels)) }()

	out := r.Clone(r.Context())
	out.URL = target
	out.RequestURI = target.RequestURI()
	out.Body = body
	out.ContentLength = -1
	out.Header.Del("Content-Length")
	out.Header.Set("Content-Type", form.FormDataContentType())
	p.studio.ServeHTTP(w, out)
}

// onFileDrop tells the page what landed and where, in the web view's
// coordinates, for it to find the folder under the pointer.
func (a *App) onFileDrop(x, y int, paths []string) {
	files, err := a.proxy.dropped.list(paths)
	if err != nil || len(files) == 0 {
		return
	}
	data, err := json.Marshal(files)
	if err != nil {
		return
	}
	wruntime.WindowExecJS(a.ctx, fmt.Sprintf("window.__studioShell && window.__studioShell.fileDrop(%d, %d, %s)", x, y, data))
}
