package kb

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
)

// SpaceFS does the Library's file work in a person's space, a folder on
// the Studio's cluster read and written as that person over their own
// session (internal/libapi). Paths are relative to the space's folder.
type SpaceFS interface {
	// List is one folder as the file system has it now.
	List(r *http.Request, lib Library, rel string) ([]Entry, error)
	Mkdir(r *http.Request, lib Library, rel string) error
	// Put writes src to rel whole, replacing a file there; more than max
	// bytes is refused and nothing is left.
	Put(r *http.Request, lib Library, rel string, src io.Reader, max int64) error
	// Rename gives rel a new name in its folder and answers the new path.
	Rename(r *http.Request, lib Library, rel, name string) (string, error)
	// Move puts each of rels into the folder dest, never over something
	// already there.
	Move(r *http.Request, lib Library, rels []string, dest string) (moved []Moved, skipped []Skipped, err error)
	// Delete removes files and folders, folders with what is in them.
	Delete(r *http.Request, lib Library, rels []string) (deleted []string, skipped []Skipped, err error)
	// Changed tells the space's index that files under rels changed.
	Changed(r *http.Request, lib Library, rels []string)
	// Get reads a plain file whole, as the person: a missing file is a
	// 404, a link or a folder a 409, more than max bytes a 413.
	Get(r *http.Request, lib Library, rel string, max int64) ([]byte, error)
}

// SpaceFiles is set by the libraries track; nil, and a space is read only.
var SpaceFiles SpaceFS

// spaceAware sends a write in a person's space to SpaceFiles, and every
// other write to next. JSON bodies are read here and given back to next.
func (m *Module) spaceAware(op string, next func(w http.ResponseWriter, r *http.Request) error) func(w http.ResponseWriter, r *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		var b body
		switch op {
		case "move", "copy", "rename", "delete", "mkdir", "upload-url":
			raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
			if err != nil {
				return err
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if b, err = readBody(r); err != nil {
				return err
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		lib, rel, ok := m.spaceOf(r, b, op)
		if !ok {
			return next(w, r)
		}
		if SpaceFiles == nil {
			return Error(403, "%s is read only here", lib.Label)
		}
		return m.spaceOp(op, w, r, lib, rel, b)
	}
}

// spaceOf is the space a write names, with the path a combined (All)
// file delete named inside it.
func (m *Module) spaceOf(r *http.Request, b body, op string) (Library, string, bool) {
	if op == "file" && inAll(r) {
		lib, sub, err := m.splitAll(r, qs(r, "path"))
		if err != nil || !lib.AsViewer() {
			return Library{}, "", false
		}
		return lib, sub, true
	}
	id, _ := libraryParam(r, b)
	if strings.ToLower(id) == All {
		return Library{}, "", false
	}
	lib, err := m.libByID(r, id)
	if err != nil || !lib.AsViewer() {
		return Library{}, "", false
	}
	return lib, qs(r, "path"), true
}

func (m *Module) spaceOp(op string, w http.ResponseWriter, r *http.Request, lib Library, rel string, b body) error {
	fs := SpaceFiles
	switch op {
	case "mkdir":
		if err := b.need(); err != nil {
			return err
		}
		p, _ := b.str("path")
		clean, err := SanitizeRelPath(JSTrim(p))
		if err != nil || clean == "" {
			return Error(400, "path required")
		}
		if err := fs.Mkdir(r, lib, clean); err != nil {
			return err
		}
		fs.Changed(r, lib, []string{clean})
		writeJSON(w, 200, O("created", clean))
	case "rename":
		if err := b.need(); err != nil {
			return err
		}
		p, _ := b.str("path")
		n, _ := b.str("name")
		clean, err := spaceRel(p)
		if err != nil {
			return err
		}
		if strings.Contains(n, "/") {
			return Error(400, "a new name, without slashes")
		}
		if n, err = SanitizeName(n); err != nil {
			return err
		}
		to, err := fs.Rename(r, lib, clean, n)
		if err != nil {
			return err
		}
		fs.Changed(r, lib, []string{clean, to})
		// The same reply as the main library's: what was renamed, from and to.
		writeJSON(w, 200, O("renamed", O("from", clean, "to", to), "indexMs", 0, "reindexing", true))
	case "move":
		if err := b.need(); err != nil {
			return err
		}
		paths, err := cleanAll(capPaths(b.list("paths")))
		if err != nil {
			return err
		}
		dest, _ := b.str("dest")
		d := ""
		if strings.Trim(dest, "/") != "" {
			if d, err = spaceRel(dest); err != nil {
				return err
			}
		}
		moved, skipped, err := fs.Move(r, lib, paths, d)
		if err != nil {
			return err
		}
		fs.Changed(r, lib, append(append([]string{}, paths...), d))
		writeJSON(w, 200, O("moved", nonNilMoved(moved), "skipped", nonNilSkipped(skipped), "indexMs", 0, "reindexing", true))
	case "delete":
		if err := b.need(); err != nil {
			return err
		}
		paths, err := cleanAll(capPaths(b.list("paths")))
		if err != nil {
			return err
		}
		if len(paths) == 0 {
			return Error(400, "paths required")
		}
		deleted, skipped, err := fs.Delete(r, lib, paths)
		if err != nil {
			return err
		}
		fs.Changed(r, lib, paths)
		if deleted == nil {
			deleted = []string{}
		}
		writeJSON(w, 200, O("deleted", deleted, "skipped", nonNilSkipped(skipped), "indexMs", 0, "reindexing", true))
	case "dir", "file":
		clean, err := spaceRel(rel)
		if err != nil {
			return err
		}
		deleted, skipped, err := fs.Delete(r, lib, []string{clean})
		if err != nil {
			return err
		}
		if len(deleted) == 0 && len(skipped) > 0 {
			return Error(400, "%s", skipped[0].Reason)
		}
		fs.Changed(r, lib, []string{clean})
		writeJSON(w, 200, O("deleted", clean, "indexMs", 0, "reindexing", true))
	case "upload":
		saved, err := m.spaceUpload(r, lib)
		if err != nil {
			return err
		}
		if len(saved) == 0 {
			return Error(400, "no files in request")
		}
		fs.Changed(r, lib, saved)
		writeJSON(w, 200, O("saved", saved, "indexed", false, "deferred", true, "reindexing", true))
	case "copy":
		return Error(409, "Copying inside your library is not available yet. Download and upload the file, or move it.")
	case "upload-url":
		return Error(409, "Adding a file from a web address is not available in your library yet. Download it, then upload it.")
	default:
		return Error(400, "unknown action")
	}
	return nil
}

func cleanAll(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		c, err := spaceRel(p)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// spaceRel is a path in a space that is there already, as given: never
// rewritten (a rewritten name could be another file's), with no empty, dot
// or hidden part, and never the space's own folder.
func spaceRel(p string) (string, error) {
	p = trimSlashes(p)
	if p == "" {
		return "", Error(400, "refusing to change the space's own folder")
	}
	parts := strings.Split(p, "/")
	if len(parts) > 64 || strings.ContainsRune(p, 0) {
		return "", Error(400, "unusable path")
	}
	for _, s := range parts {
		if s == "" || strings.HasPrefix(s, ".") || ExcludeDirs[s] {
			return "", Error(400, "path not changeable: %s", p)
		}
	}
	return p, nil
}

// spaceUpload streams each file of a multipart upload into the space.
func (m *Module) spaceUpload(r *http.Request, lib Library) ([]string, error) {
	dir := "uploads"
	if v, ok := q(r, "dir"); ok {
		dir = v
	}
	cleanDir := ""
	if strings.Trim(dir, "/") != "" {
		c, err := spaceRel(dir)
		if err != nil {
			return nil, err
		}
		cleanDir = c
	}
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" {
		return nil, Error(400, "the request is not multipart")
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var listed []string
	n := 0
	saved := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return saved, err
		}
		if part.FileName() == "" {
			if part.FormName() == "paths" {
				raw, _ := io.ReadAll(io.LimitReader(part, 1<<20))
				listed = parseList(raw)
			}
			part.Close()
			continue
		}
		if len(saved) >= maxUploadFiles {
			return saved, Error(400, "too many files")
		}
		name := part.FileName()
		if n < len(listed) {
			name = listed[n]
		}
		n++
		rel, err := SanitizeRelPath(name)
		if err != nil {
			return saved, err
		}
		if cleanDir != "" {
			rel = cleanDir + "/" + rel
		}
		if err := SpaceFiles.Put(r, lib, rel, part, maxUploadBytes); err != nil {
			return saved, err
		}
		saved = append(saved, rel)
	}
	return saved, nil
}

func parseList(raw []byte) []string {
	var list []any
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, jsString(x))
	}
	return out
}
