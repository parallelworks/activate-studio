package kb

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// A library on another system has no files on this host, so its PDFs
// (and Office files, where LibreOffice is here) are previewed from a copy
// fetched with the same download the Download button uses. The copy is
// kept under the index base, by library and path, and fetched again each
// time the viewer opens the file (pdf-info); a page request uses the copy
// when there is one. A copy whose bytes have not changed is not rewritten,
// so the page images rendered from it stay valid.

var remoteCopyMu sync.Mutex

// remoteCopies is where the fetched copies are kept.
func (r Roots) remoteCopies(libID string) string {
	return filepath.Join(r.IndexBase, "remote-previews", libID)
}

// previewSource is the file a preview route reads: the file itself on
// this host, or for a library on another system the fetched copy. fresh
// fetches it again even when a copy is kept.
func (m *Module) previewSource(r *http.Request, fresh bool) (Library, string, string, error) {
	lib, err := m.lib(r, body{})
	if err != nil {
		return Library{}, "", "", err
	}
	if !lib.ReadThrough && lib.Site == "" {
		return m.sourceFile(r)
	}
	if LibraryResolver == nil {
		return Library{}, "", "", Error(409, "%s has no files on this host", lib.Label)
	}
	rel := qs(r, "path")
	abs, err := Resolve(rel, m.Roots.remoteCopies(lib.ID))
	if err != nil {
		return Library{}, "", "", err
	}
	remoteCopyMu.Lock()
	defer remoteCopyMu.Unlock()
	if !fresh && exists(abs) {
		return lib, rel, abs, nil
	}
	_, data, err := LibraryResolver.Download(r, lib, rel)
	if err != nil {
		return Library{}, "", "", err
	}
	if old, err := os.ReadFile(abs); err == nil && bytes.Equal(old, data) {
		return lib, rel, abs, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Library{}, "", "", err
	}
	tmp := abs + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Library{}, "", "", err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return Library{}, "", "", err
	}
	return lib, rel, abs, nil
}
