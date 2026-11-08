package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
)

// Module is the files track's state: one per server.
type Module struct {
	A        *app.App
	Roots    Roots
	Libs     *Libraries
	Previews *Previews
	Office   *Office
	Files    *Files
	Tags     *Tags
	Jobs     *Jobs
	// Fetch is the client /api/kb/upload-url fetches with.
	Fetch *http.Client

	stats statsCache
}

// forgetting drops the module's counts whenever the index changes.
type forgetting struct {
	Indexer
	m *Module
}

func (f forgetting) IndexDir(ctx context.Context, rel string) (int64, error) {
	defer f.m.stats.forget()
	return f.Indexer.IndexDir(ctx, rel)
}
func (f forgetting) IndexRootDB(ctx context.Context) (int64, error) {
	defer f.m.stats.forget()
	return f.Indexer.IndexRootDB(ctx)
}
func (f forgetting) Changed(ctx context.Context, what ...string) {
	f.m.stats.forget()
	f.Indexer.Changed(ctx, what...)
}

// New is the module for a.
func New(a *app.App) *Module {
	roots := RootsFrom(a.Cfg)
	m := &Module{A: a, Roots: roots, Office: &Office{}, Jobs: &Jobs{}, Fetch: &http.Client{}}
	ix := forgetting{ProviderIndexer{A: a}, m}
	m.Libs = &Libraries{Roots: roots, Pinned: os.Getenv("STUDIO_LIBRARIES"), AuthEnabled: a.Cfg.AuthEnabled(),
		Label: func() string { return a.Settings.Effective().KbLabel }}
	m.Previews = &Previews{Roots: roots}
	m.Files = &Files{Roots: roots, Previews: m.Previews, Indexer: ix}
	runner := a.Gufi
	if runner == nil {
		runner = gufi.NewRunner(gufi.NewLocal(a.Cfg.GufiBin), a.Log, gufi.OptionsFrom(a.Cfg.Gufi))
	}
	m.Tags = &Tags{Roots: roots, Overlay: &Overlay{Roots: roots}, Library: &LibraryLabels{Roots: roots},
		Runner: runner, Indexer: ix, GufiAvailable: a.Cfg.GufiAvailable}
	return m
}

// Default is the module Register made, for other packages of the server
// (search's label annotations, the index's overlay re-apply).
var Default *Module

// Register registers the KB file, upload and label routes on mux, sets the
// ApplyLabels provider, and the label overlay's recovery at start.
func Register(mux *http.ServeMux, a *app.App) {
	m := New(a)
	Default = m
	m.Routes(mux)
	a.Providers.ApplyLabels = func(ctx context.Context, _ *http.Request, paths, add, remove []string) error {
		_, err := m.Tags.Apply(ctx, paths, add, remove)
		return err
	}
	a.AddBusy(m.Jobs.Running)
	// An empty overlay beside an index that still carries labels is a lost
	// overlay; recover once the index is up. With Node behind the Go
	// server, Node's own timer does it (tags.ts).
	a.OnStart("files: label overlay recovery", func(ctx context.Context) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Second):
		}
		if a.NodeFree() && m.Tags.Overlay.Empty() {
			if n := m.Tags.Recover(ctx); n > 0 {
				a.Log.Warn("label overlay was empty; entries recovered from the index", "adopted", n)
			}
		}
	})
}

// Routes registers the handlers.
func (m *Module) Routes(mux *http.ServeMux) {
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request) error) {
		mux.Handle(pattern, apierr.Handler(m.A.Log, fn))
	}
	h("GET /api/kb/tree", m.tree)
	h("GET /api/kb/file", m.file)
	h("GET /api/kb/download", m.download)
	h("GET /api/kb/raw", m.raw)
	h("GET /api/kb/html", m.html)
	h("GET /api/kb/pdf", m.pdf)
	h("GET /api/kb/pdf-info", m.pdfInfo)
	h("GET /api/kb/pdf-page", m.pdfPage)
	h("GET /api/kb/office-preview", m.officePreview)
	h("GET /api/kb/office-media", m.officeMedia)
	h("GET /api/kb/dirs", m.dirs)
	h("GET /api/kb/stats", m.statsRoute)
	h("POST /api/kb/move", m.move)
	h("POST /api/kb/copy", m.copy)
	h("POST /api/kb/rename", m.rename)
	h("POST /api/kb/delete", m.bulkDelete)
	h("DELETE /api/kb/dir", m.deleteDir)
	h("DELETE /api/kb/file", m.deleteFile)
	h("POST /api/kb/mkdir", m.mkdir)
	h("POST /api/kb/upload", m.upload)
	h("POST /api/kb/upload-url", m.uploadURL)
	h("GET /api/tags", m.vocabulary)
	h("GET /api/kb/tags", m.getTags)
	h("POST /api/kb/tags", m.postTags)
	h("POST /api/kb/tags/recover", m.recoverTags)
	h("GET /api/jobs/{id}", m.job)
}

/* ---- requests ---- */

// errToNode is a request this module leaves to Node: a library only Node
// reaches (a viewer's own on a cluster, one read through another host).
type errToNode struct{ why string }

func (e errToNode) Error() string { return "to node: " + e.why }

// toNode passes the request on to Node, logging why.
func (m *Module) toNode(w http.ResponseWriter, r *http.Request, why string) error {
	m.A.Log.Info("files: to node", "path", r.URL.Path, "why", why)
	// A deliberate hand-off, marked so the proxy tells Node (and the
	// front harness's strict mode) it was meant. Node-free, it is a 501.
	m.A.Node().ServeHTTP(w, app.HandOff(r, why))
	return nil
}

// handled wraps a handler body: an errToNode goes to Node.
func (m *Module) handled(w http.ResponseWriter, r *http.Request, err error) error {
	var tn errToNode
	if errors.As(err, &tn) {
		return m.toNode(w, r, tn.why)
	}
	return err
}

// q is a query value as Fastify gives it: a repeated key joined by commas
// (String of the array).
func q(r *http.Request, k string) (string, bool) {
	v, ok := r.URL.Query()[k]
	if !ok {
		return "", false
	}
	return strings.Join(v, ","), true
}

func qs(r *http.Request, k string) string { v, _ := q(r, k); return v }

// body is the request's JSON body as Fastify parses it. A body that does
// not parse is a 500, as Fastify's parser error is under the Studio's error
// handler; no body is present false.
type body struct {
	m       map[string]any
	present bool
}

func readBody(r *http.Request) (body, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return body{}, err
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(raw) == 0 && ct == "" {
		return body{}, nil
	}
	if len(raw) > 1<<20 {
		return body{}, errors.New("request body is too large")
	}
	if ct == "application/json" || strings.HasSuffix(ct, "+json") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return body{}, fmt.Errorf("body is not valid JSON: %w", err)
		}
		m, _ := v.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		return body{m: m, present: true}, nil
	}
	// Text: a string body, whose fields are all undefined.
	return body{m: map[string]any{}, present: true}, nil
}

// need is a body that a handler reads fields of: none is a TypeError in
// Node, a 500.
func (b body) need() error {
	if !b.present {
		return errors.New("the request has no body")
	}
	return nil
}

func (b body) str(k string) (string, bool) {
	v, ok := b.m[k]
	if !ok || v == nil {
		return "", false
	}
	return jsString(v), true
}

func (b body) list(k string) []string {
	arr, _ := b.m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		out = append(out, jsString(x))
	}
	return out
}

func (b body) truthy(k string) bool {
	switch v := b.m[k].(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	}
	return true
}

// libraryParam is ?library=, else the body's library.
func libraryParam(r *http.Request, b body) (string, bool) {
	if v, ok := q(r, "library"); ok {
		return v, true
	}
	if v, ok := b.str("library"); ok {
		return v, true
	}
	return "", false
}

// source is sourceOf: a library's files on this host.
func (m *Module) source(lib Library) (string, error) {
	if lib.SourceRoot == "" {
		return "", Error(409, "%s has no files on this host; it can be searched and described but not opened", lib.Label)
	}
	return lib.SourceRoot, nil
}

func inAll(r *http.Request) bool { return strings.ToLower(qs(r, "library")) == All }

// others are listLibraries(label, viewer) without the main one, for the
// label vocabulary.
func (m *Module) others(r *http.Request) ([]Library, error) {
	if _, err := m.allLibs(r); err != nil {
		return nil, err
	}
	return m.Libs.List()[1:], nil
}

// splitAll takes "<library id>/<path>" apart, as the combined tree's paths are.
func (m *Module) splitAll(r *http.Request, rel string) (Library, string, error) {
	libs, err := m.allLibs(r)
	if err != nil {
		return Library{}, "", err
	}
	parts := strings.Split(strings.TrimLeft(rel, "/"), "/")
	for _, l := range libs {
		if l.ID == parts[0] {
			return l, strings.Join(parts[1:], "/"), nil
		}
	}
	return Library{}, "", Error(404, "no library named %s", parts[0])
}

/* ---- listings and contents ---- */

func (m *Module) tree(w http.ResponseWriter, r *http.Request) error {
	return m.handled(w, r, m.treeBody(w, r))
}

func (m *Module) treeBody(w http.ResponseWriter, r *http.Request) error {
	rel := qs(r, "path")
	showHidden := qs(r, "hidden") == "1"
	if inAll(r) {
		libs, err := m.allLibs(r)
		if err != nil {
			return err
		}
		if trimSlashes(rel) == "" {
			entries := []Entry{}
			for _, l := range libs {
				name := l.Label
				if l.Site != "" {
					name = l.Label + " (on " + l.Site + ")"
				}
				entries = append(entries, Entry{Name: name, Path: l.ID, Type: "dir", Kind: "dir"})
			}
			writeJSON(w, 200, O("path", "", "entries", entries))
			return nil
		}
		lib, sub, err := m.splitAll(r, rel)
		if err != nil {
			return err
		}
		labeled := []Entry{}
		if lib.Site != "" {
			list, err := m.remoteTree(r, lib, sub, showHidden)
			if err != nil {
				return err
			}
			labeled = list
		} else if lib.SourceRoot != "" {
			list, err := ListDir(sub, lib.SourceRoot, showHidden)
			if err != nil {
				return err
			}
			labeled = m.withLocalLabels(lib, lib.SourceRoot, list)
		}
		for i := range labeled {
			labeled[i].Path = lib.ID + "/" + labeled[i].Path
		}
		writeJSON(w, 200, O("path", rel, "entries", labeled))
		return nil
	}
	lib, err := m.lib(r, body{})
	if err != nil {
		return err
	}
	// A user's library on another system lists from its index there.
	if lib.Site != "" {
		list, err := m.remoteTree(r, lib, rel, showHidden)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("path", rel, "entries", list))
		return nil
	}
	root, err := m.source(lib)
	if err != nil {
		return err
	}
	list, err := ListDir(rel, root, showHidden)
	if err != nil {
		return err
	}
	writeJSON(w, 200, O("path", rel, "entries", m.withLocalLabels(lib, root, list)))
	return nil
}

// withLocalLabels gives a listing each entry's own labels from its xattr
// (or the overlay), so a label just applied shows without the index, and
// for a library other than the main one the labels the Studio keeps.
func (m *Module) withLocalLabels(lib Library, root string, entries []Entry) []Entry {
	abs := make([]string, len(entries))
	for i, e := range entries {
		abs[i], _ = Resolve(e.Path, root)
	}
	tagged := m.Tags.ReadBatch(abs)
	for i := range entries {
		if t := tagged[abs[i]]; len(t) > 0 {
			entries[i].Tags = t
		}
	}
	if !lib.Primary {
		kept := m.Tags.Library.Map(lib.ID)
		for i := range entries {
			if k := kept[entries[i].Path]; len(k) > 0 {
				entries[i].Tags = uniq(append(append([]string{}, entries[i].Tags...), k...))
			}
		}
	}
	return entries
}

func (m *Module) file(w http.ResponseWriter, r *http.Request) error {
	rel := qs(r, "path")
	if inAll(r) {
		lib, sub, err := m.splitAll(r, rel)
		if err != nil {
			return err
		}
		if lib.ReadThrough || lib.Site != "" {
			out, err := m.remoteFile(r, lib, sub)
			if err != nil {
				return err
			}
			writeJSON(w, 200, out.Set("path", rel))
			return nil
		}
		if lib.SourceRoot == "" {
			return Error(409, "%s has no files on this host; it can be searched and described but not opened", lib.Label)
		}
		out, err := ReadFileContent(sub, lib.SourceRoot, m.cacheRoot(lib))
		if err != nil {
			return err
		}
		writeJSON(w, 200, out.Set("path", rel))
		return nil
	}
	lib, err := m.lib(r, body{})
	if err != nil {
		return err
	}
	if lib.ReadThrough || lib.Site != "" {
		out, err := m.remoteFile(r, lib, rel)
		if err != nil {
			return err
		}
		writeJSON(w, 200, out)
		return nil
	}
	root, err := m.source(lib)
	if err != nil {
		return err
	}
	out, err := ReadFileContent(rel, root, m.cacheRoot(lib))
	if err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

func (m *Module) cacheRoot(lib Library) string {
	if lib.CacheRoot != "" {
		return lib.CacheRoot
	}
	return m.Roots.ExtractCache()
}

// contentDisposition is attachment; filename="<name>" for an ASCII name, as
// Node writes it. Node cannot send a name outside Latin-1 (it answers 500)
// and sends Latin-1 as raw bytes; here such a name is sent with its UTF-8
// form in filename* (RFC 6266) and an ASCII stand-in in filename.
func contentDisposition(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	ascii := true
	for i := 0; i < len(name); i++ {
		if name[i] >= 0x80 || name[i] < 0x20 {
			ascii = false
			break
		}
	}
	if ascii {
		return `attachment; filename="` + name + `"`
	}
	var fallback, enc strings.Builder
	for _, r := range name {
		if r >= 0x20 && r < 0x7f {
			fallback.WriteRune(r)
		} else {
			fallback.WriteByte('_')
		}
	}
	for _, c := range []byte(name) {
		if c >= 0x80 || c < 0x20 || strings.IndexByte(" \"%'()*,/:;<=>?@[\\]{}", c) >= 0 {
			fmt.Fprintf(&enc, "%%%02X", c)
		} else {
			enc.WriteByte(c)
		}
	}
	return `attachment; filename="` + fallback.String() + `"; filename*=UTF-8''` + enc.String()
}

// sendFile streams a file as Node's reply.send(createReadStream) does: the
// whole file with a 200 (no ranges), and a file that cannot be opened is a
// 500 with the headers already set left off.
func sendFile(w http.ResponseWriter, abs string, headers map[string]string) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.IsDir() {
		return &os.PathError{Op: "read", Path: abs, Err: errors.New("is a directory")}
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.WriteHeader(200)
	io.Copy(w, f)
	return nil
}

func (m *Module) download(w http.ResponseWriter, r *http.Request) error {
	rel := qs(r, "path")
	var lib Library
	sub := rel
	var err error
	if inAll(r) {
		if lib, sub, err = m.splitAll(r, rel); err != nil {
			return err
		}
	} else if lib, err = m.lib(r, body{}); err != nil {
		return err
	}
	if lib.ReadThrough || lib.Site != "" {
		if LibraryResolver == nil {
			return Error(409, "%s has no files on this host", lib.Label)
		}
		name, data, err := LibraryResolver.Download(r, lib, sub)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", MimeFor(name))
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "")+`"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(200)
		w.Write(data)
		return nil
	}
	root := lib.SourceRoot
	if inAll(r) {
		if root == "" {
			return Error(409, "%s has no files on this host", lib.Label)
		}
	} else if root, err = m.source(lib); err != nil {
		return err
	}
	abs, err := Resolve(sub, root)
	if err != nil {
		return err
	}
	return sendFile(w, abs, map[string]string{"Content-Type": MimeFor(abs), "Content-Disposition": contentDisposition(filepath.Base(abs))})
}

// sourceFile is the file a viewing route names in the library asked for.
func (m *Module) sourceFile(r *http.Request) (Library, string, string, error) {
	lib, err := m.lib(r, body{})
	if err != nil {
		return Library{}, "", "", err
	}
	root, err := m.source(lib)
	if err != nil {
		return Library{}, "", "", err
	}
	rel := qs(r, "path")
	abs, err := Resolve(rel, root)
	return lib, rel, abs, err
}

// SourceFile is the library, path and file on this host that a request's
// library and path name, for routes outside this package (the 3D model
// preview).
func (m *Module) SourceFile(r *http.Request) (lib, rel, abs string, err error) {
	l, rel, abs, err := m.sourceFile(r)
	return l.ID, rel, abs, err
}

func (m *Module) raw(w http.ResponseWriter, r *http.Request) error {
	_, _, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	return sendFile(w, abs, map[string]string{"Content-Type": MimeFor(abs), "Content-Disposition": "inline"})
}

var htmlFile = func(p string) bool {
	low := strings.ToLower(p)
	return strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".htm")
}

// html serves corpus HTML under a hard sandbox: inline script and style
// run, nothing loads, and the page has no origin.
func (m *Module) html(w http.ResponseWriter, r *http.Request) error {
	_, _, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	if !htmlFile(abs) {
		return Error(415, "not an html file")
	}
	return sendFile(w, abs, map[string]string{"Content-Type": "text/html; charset=utf-8",
		"Content-Security-Policy": "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:"})
}

func (m *Module) pdf(w http.ResponseWriter, r *http.Request) error {
	_, rel, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	if !Convertible[strings.ToLower(ExtName(abs))] {
		return Error(415, "no PDF preview for this file type")
	}
	out, err := m.Previews.OfficeToPDF(r.Context(), abs, rel)
	if err != nil {
		return err
	}
	return sendFile(w, out, map[string]string{"Content-Type": "application/pdf", "Content-Disposition": "inline"})
}

// previewKey: another library's pages are cached under its id.
func previewKey(lib Library, rel string) string {
	if lib.Primary {
		return rel
	}
	return filepath.Join(".libraries", lib.ID, rel)
}

func (m *Module) pdfInfo(w http.ResponseWriter, r *http.Request) error {
	lib, rel, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	pdf, err := m.Previews.PreviewPDF(r.Context(), abs, previewKey(lib, rel))
	if err != nil {
		return err
	}
	n, err := m.Previews.PageCount(r.Context(), pdf)
	if err != nil {
		return err
	}
	writeJSON(w, 200, O("pages", n))
	return nil
}

func (m *Module) pdfPage(w http.ResponseWriter, r *http.Request) error {
	lib, rel, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	page := "1"
	if v, ok := q(r, "page"); ok {
		page = v
	}
	n := 1
	if f := jsNumberOr(page, 1); f >= 1 {
		n = int(f)
	}
	png, err := m.Previews.PagePNG(r.Context(), abs, previewKey(lib, rel), n)
	if err != nil {
		return err
	}
	return sendFile(w, png, map[string]string{"Content-Type": "image/png", "Cache-Control": "public, max-age=300"})
}

func (m *Module) officePreview(w http.ResponseWriter, r *http.Request) error {
	_, _, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	out, err := m.Office.Outline(abs)
	if err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

func (m *Module) officeMedia(w http.ResponseWriter, r *http.Request) error {
	_, _, abs, err := m.sourceFile(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	b, typ, err := m.Office.Media(abs, qs(r, "name"))
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Type", typ)
	h.Set("Cache-Control", "private, max-age=300")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	w.Write(b)
	return nil
}

func (m *Module) dirs(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, O("dirs", Dirs(m.Roots.KBRoot)))
	return nil
}

/* ---- file operations ---- */

func (m *Module) writableLib(r *http.Request, b body) error {
	lib, err := m.lib(r, b)
	if err != nil {
		return err
	}
	return RequireWritable(lib)
}

// mutation reads the body and checks the library is writable.
func (m *Module) mutation(w http.ResponseWriter, r *http.Request, fn func(b body) error) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := m.writableLib(r, b); err != nil {
		return m.handled(w, r, err)
	}
	return fn(b)
}

func capPaths(p []string) []string { return p[:min(len(p), 500)] }

func (m *Module) move(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		paths := capPaths(b.list("paths"))
		dest, _ := b.str("dest")
		if b.truthy("async") {
			job := m.Jobs.Start(r.Context(), "move", len(paths), nil, func(ctx context.Context, h *JobHandle) (any, error) {
				o, err := m.Files.Move(paths, dest, func(done int, cur string) { h.Progress("moving", done, cur) })
				if err != nil {
					return nil, err
				}
				h.Progress("indexing", len(paths), "")
				ms, err := ReindexRoots(ctx, m.Files.Indexer, o.Roots, o.RootDB)
				o.IndexMS = ms
				if err != nil {
					return nil, err
				}
				return o.Result(), nil
			})
			writeJSON(w, 200, job)
			return nil
		}
		o, err := m.Files.Move(paths, dest, nil)
		if err != nil {
			return err
		}
		ms, err := ReindexRoots(r.Context(), m.Files.Indexer, o.Roots, o.RootDB)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("moved", o.Moved, "skipped", o.Skipped, "indexMs", ms))
		return nil
	})
}

func (m *Module) copy(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		paths := capPaths(b.list("paths"))
		dest, _ := b.str("dest")
		if b.truthy("async") {
			job := m.Jobs.Start(r.Context(), "move", len(paths), nil, func(ctx context.Context, h *JobHandle) (any, error) {
				o, err := m.Files.Copy(paths, dest, func(done int, cur string) { h.Progress("copying", done, cur) })
				if err != nil {
					return nil, err
				}
				h.Progress("indexing", len(paths), "")
				ms, err := ReindexRoots(ctx, m.Files.Indexer, o.Roots, o.RootDB)
				o.IndexMS = ms
				if err != nil {
					return nil, err
				}
				return o.Result(), nil
			})
			writeJSON(w, 200, job)
			return nil
		}
		o, err := m.Files.Copy(paths, dest, nil)
		if err != nil {
			return err
		}
		ms, err := ReindexRoots(r.Context(), m.Files.Indexer, o.Roots, o.RootDB)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("copied", o.Moved, "skipped", o.Skipped, "indexMs", ms))
		return nil
	})
}

func (m *Module) rename(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		p, _ := b.str("path")
		n, _ := b.str("name")
		o, err := m.Files.Rename(p, n)
		if err != nil {
			return err
		}
		ms, err := ReindexRoots(r.Context(), m.Files.Indexer, o.Roots, o.RootDB)
		if err != nil {
			return err
		}
		var renamed any
		if len(o.Moved) > 0 {
			renamed = o.Moved[0]
		}
		writeJSON(w, 200, O("renamed", renamed, "indexMs", ms))
		return nil
	})
}

func (m *Module) bulkDelete(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		paths := capPaths(b.list("paths"))
		if len(paths) == 0 {
			return Error(400, "paths required")
		}
		reindex := func(ctx context.Context, d *Deleted) (int64, error) {
			return ReindexRoots(ctx, m.Files.Indexer, d.Roots, d.RootDB)
		}
		if b.truthy("async") {
			first := func(h *JobHandle) { h.Progress("deleting", 0, trimSlashes(paths[0])) }
			job := m.Jobs.Start(r.Context(), "delete", len(paths), first, func(ctx context.Context, h *JobHandle) (any, error) {
				d := m.Files.DeletePaths(paths, func(done int, cur string) { h.Progress("deleting", done, cur) })
				h.Progress("indexing", len(paths), "")
				ms, err := reindex(ctx, d)
				if err != nil {
					return nil, err
				}
				return O("deleted", d.Deleted, "skipped", d.Skipped, "indexMs", ms), nil
			})
			writeJSON(w, 200, job)
			return nil
		}
		d := m.Files.DeletePaths(paths, nil)
		ms, err := reindex(r.Context(), d)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("deleted", d.Deleted, "skipped", d.Skipped, "indexMs", ms))
		return nil
	})
}

func (m *Module) deleteDir(w http.ResponseWriter, r *http.Request) error {
	if err := m.writableLib(r, body{}); err != nil {
		return m.handled(w, r, err)
	}
	cleaned := trimSlashes(qs(r, "path"))
	if cleaned == "" {
		return Error(400, "refusing to delete the knowledge base root")
	}
	if protected(cleaned) {
		return Error(400, "path not deletable")
	}
	abs, err := Resolve(cleaned, m.Roots.KBRoot)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Error(404, "not found")
	}
	if !st.IsDir() {
		return Error(400, "not a directory; use the file delete")
	}
	if qs(r, "async") == "1" {
		first := func(h *JobHandle) { h.Phase("deleting", cleaned) }
		job := m.Jobs.Start(r.Context(), "delete", 1, first, func(ctx context.Context, h *JobHandle) (any, error) {
			if err := m.Files.DeleteDir(ctx, cleaned, abs); err != nil {
				return nil, err
			}
			return O("deleted", []string{cleaned}, "skipped", []Skipped{}), nil
		})
		writeJSON(w, 200, job)
		return nil
	}
	if err := m.Files.DeleteDir(r.Context(), cleaned, abs); err != nil {
		return err
	}
	writeJSON(w, 200, O("deleted", cleaned))
	return nil
}

func (m *Module) deleteFile(w http.ResponseWriter, r *http.Request) error {
	rel := qs(r, "path")
	err := func() error {
		// With All libraries an earlier client sent the combined tree's
		// path; it is taken apart.
		if inAll(r) {
			lib, sub, err := m.splitAll(r, rel)
			if err != nil {
				return err
			}
			if err := RequireWritable(lib); err != nil {
				return err
			}
			rel = sub
		} else if err := m.writableLib(r, body{}); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		return m.handled(w, r, err)
	}
	if rel == "" {
		return Error(400, "path required")
	}
	for _, part := range strings.Split(rel, "/") {
		if ExcludeDirs[part] || strings.HasPrefix(part, ".") {
			return Error(400, "path not deletable")
		}
	}
	abs, err := Resolve(rel, m.Roots.KBRoot)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return Error(404, "not found")
	}
	if !st.Mode().IsRegular() {
		return Error(400, "only files can be deleted")
	}
	ms, err := m.Files.DeleteFile(r.Context(), rel, abs)
	if err != nil {
		return err
	}
	writeJSON(w, 200, O("deleted", rel, "indexMs", ms))
	return nil
}

/* ---- uploads ---- */

func (m *Module) mkdir(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		p, _ := b.str("path")
		rel := JSTrim(p)
		if rel == "" {
			return Error(400, "path required")
		}
		clean, err := SanitizeRelPath(rel)
		if err != nil {
			return err
		}
		_, cleaned, err := m.Files.TargetDir(clean)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("created", cleaned))
		return nil
	})
}

func (m *Module) upload(w http.ResponseWriter, r *http.Request) error {
	if err := m.writableLib(r, body{}); err != nil {
		return m.handled(w, r, err)
	}
	dir := "uploads"
	if v, ok := q(r, "dir"); ok {
		dir = v
	}
	saved, rel, err := m.Files.Upload(r, dir)
	if err != nil {
		return err
	}
	if len(saved) == 0 {
		return Error(400, "no files in request")
	}
	// index=0 answers once the files are on disk; the client asks for one
	// pass at the end of a bulk upload.
	if qs(r, "index") == "0" {
		writeJSON(w, 200, O("saved", saved, "indexed", false, "deferred", true))
		return nil
	}
	ms, err := ReindexForDir(r.Context(), m.Files.Indexer, rel)
	if err != nil {
		m.A.Log.Error("upload saved but indexing failed", "err", err.Error())
		writeJSON(w, 200, O("saved", saved, "indexed", false, "indexError", SliceUTF16(errMessage(err), 300)))
		return nil
	}
	writeJSON(w, 200, O("saved", saved, "indexed", true, "indexMs", ms))
	return nil
}

func (m *Module) uploadURL(w http.ResponseWriter, r *http.Request) error {
	return m.mutation(w, r, func(b body) error {
		if err := b.need(); err != nil {
			return err
		}
		u, _ := b.str("url")
		dir := "uploads"
		if v, ok := b.str("dir"); ok {
			dir = v
		}
		saved, rel, err := m.Files.FetchURL(r.Context(), u, dir, m.Fetch)
		if err != nil {
			return err
		}
		ms, err := ReindexForDir(r.Context(), m.Files.Indexer, rel)
		if err != nil {
			return err
		}
		writeJSON(w, 200, O("saved", []string{saved}, "indexed", true, "indexMs", ms))
		return nil
	})
}

/* ---- labels ---- */

// labelLib is tags.ts's libraryAndPath.
func (m *Module) labelLib(r *http.Request, b body, rel string) (Library, string, error) {
	want := ""
	if v, ok := libraryParam(r, b); ok {
		want = strings.ToLower(JSTrim(v))
	}
	cleaned := trimSlashes(rel)
	if want == All {
		libs, err := m.allLibs(r)
		if err != nil {
			return Library{}, "", err
		}
		parts := strings.Split(cleaned, "/")
		for _, l := range libs {
			if l.ID == parts[0] {
				return l, strings.Join(parts[1:], "/"), nil
			}
		}
		return Library{}, "", Error(404, "no library named %s", parts[0])
	}
	lib, err := m.libByID(r, want)
	if err != nil {
		return Library{}, "", err
	}
	return lib, cleaned, nil
}

func stored(lib Library) string {
	if lib.ID == PrimaryID {
		return "files"
	}
	return "studio"
}

func (m *Module) vocabulary(w http.ResponseWriter, r *http.Request) error {
	others, err := m.others(r)
	if err != nil {
		return m.handled(w, r, err)
	}
	writeJSON(w, 200, O("tags", m.Tags.Vocabulary(r.Context(), others)))
	return nil
}

func (m *Module) getTags(w http.ResponseWriter, r *http.Request) error {
	lib, rel, err := m.labelLib(r, body{}, qs(r, "path"))
	if err != nil {
		return m.handled(w, r, err)
	}
	var out *Obj
	if lib.ID == PrimaryID {
		out = m.Tags.Effective(r.Context(), rel)
	} else {
		out = m.Tags.Library.Effective(lib.ID, rel)
	}
	writeJSON(w, 200, out.Set("stored", stored(lib)))
	return nil
}

// applyLibraryLabels is tags.ts's applyLibraryLabels: labels on a library
// whose files the Studio does not write, kept by the Studio.
func (m *Module) applyLibraryLabels(lib Library, rels, addRaw, removeRaw []string) (map[string][]string, error) {
	add, remove := normalizeAll(addRaw), normalizeAll(removeRaw)
	if len(rels) == 0 || (len(add) == 0 && len(remove) == 0) {
		return nil, Error(400, "paths and add or remove required")
	}
	removeSet := setOf(remove)
	out := map[string][]string{}
	for _, rel := range rels[:min(len(rels), 1000)] {
		for _, part := range strings.Split(rel, "/") {
			if part == ".." || strings.HasPrefix(part, ".") {
				return nil, Error(400, "not taggable: %s", rel)
			}
		}
		if lib.SourceRoot != "" && !exists(filepath.Join(lib.SourceRoot, rel)) {
			return nil, Error(404, "no such file: %s", rel)
		}
		var next []string
		for _, t := range m.Tags.Library.Of(lib.ID, rel) {
			if !removeSet[t] {
				next = append(next, t)
			}
		}
		got, err := m.Tags.Library.Set(lib, rel, append(next, add...))
		if err != nil {
			return nil, err
		}
		out[rel] = got
	}
	return out, nil
}

func (m *Module) postTags(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := b.need(); err != nil {
		return err
	}
	type group struct {
		lib   Library
		rels  []string
		asked []string
	}
	var order []string
	groups := map[string]*group{}
	for _, p := range b.list("paths") {
		lib, rel, err := m.labelLib(r, b, p)
		if err != nil {
			return m.handled(w, r, err)
		}
		g := groups[lib.ID]
		if g == nil {
			g = &group{lib: lib}
			groups[lib.ID] = g
			order = append(order, lib.ID)
		}
		g.rels = append(g.rels, rel)
		g.asked = append(g.asked, p)
	}
	if len(groups) == 0 {
		return Error(400, "paths and add or remove required")
	}
	updated := O()
	st := "files"
	for _, id := range order {
		g := groups[id]
		got := map[string][]string{}
		if g.lib.ID == PrimaryID {
			o, err := m.Tags.Apply(r.Context(), g.rels, b.list("add"), b.list("remove"))
			if err != nil {
				return err
			}
			for _, k := range o.keys {
				got[k] = o.vals[k].([]string)
			}
		} else {
			if got, err = m.applyLibraryLabels(g.lib, g.rels, b.list("add"), b.list("remove")); err != nil {
				return err
			}
			st = "studio"
		}
		for i, rel := range g.rels {
			if v, ok := got[rel]; ok {
				updated.Set(g.asked[i], v)
			}
		}
	}
	writeJSON(w, 200, O("updated", updated, "stored", st))
	return nil
}

func (m *Module) recoverTags(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, O("adopted", m.Tags.Recover(r.Context())))
	return nil
}

/* ---- jobs ---- */

// job is GET /api/jobs/:id: this server's jobs, and Node's (ids below
// JobIDBase) from Node.
func (m *Module) job(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	n := jsNumberOr(id, -1)
	if n >= JobIDBase {
		if j := m.Jobs.Get(int(n)); j != nil {
			writeJSON(w, 200, j)
			return nil
		}
		writeJSON(w, 404, map[string]string{"error": "no such job"})
		return nil
	}
	// Every route that starts a job runs here, so Node has none to show.
	writeJSON(w, 404, map[string]string{"error": "no such job"})
	return nil
}
