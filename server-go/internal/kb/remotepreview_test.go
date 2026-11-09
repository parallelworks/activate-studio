package kb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// remoteStub is a library on another system whose files are bytes in a map.
type remoteStub struct {
	files     map[string][]byte
	downloads int
}

func (s *remoteStub) Library(r *http.Request, id string) (Library, error) {
	if id != "far" {
		return Library{}, Error(404, "no library named %s", id)
	}
	return Library{ID: "far", Label: "Far", IndexRoot: "remote:abc:far", Site: "box"}, nil
}
func (s *remoteStub) All(*http.Request) ([]Library, error)                 { return nil, nil }
func (s *remoteStub) Tree(*http.Request, Library, string) ([]Entry, error) { return nil, nil }
func (s *remoteStub) File(*http.Request, Library, string) (*Obj, error)    { return nil, nil }
func (s *remoteStub) Download(_ *http.Request, _ Library, rel string) (string, []byte, error) {
	s.downloads++
	b, ok := s.files[rel]
	if !ok {
		return "", nil, Error(404, "box: no such file")
	}
	return filepath.Base(rel), b, nil
}
func (s *remoteStub) Aggregate(context.Context, string, []string) (string, error) { return "", nil }
func (s *remoteStub) BuiltAt(string) string                                       { return "" }

// A preview of a file on another system reads a copy fetched from there,
// fetched again when the viewer opens the file and kept unchanged when the
// file there has not changed, so pages rendered from it stay valid.
func TestPreviewOfARemoteFileReadsAFetchedCopy(t *testing.T) {
	base := t.TempDir()
	roots := Roots{KBRoot: filepath.Join(base, "kb"), IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}
	m := &Module{Roots: roots, Libs: &Libraries{Roots: roots, Label: func() string { return "Notes" }}}
	stub := &remoteStub{files: map[string][]byte{"papers/a.pdf": []byte("%PDF-1.4 one")}}
	old := LibraryResolver
	LibraryResolver = stub
	defer func() { LibraryResolver = old }()

	req := func() *http.Request {
		return httptest.NewRequest("GET", "/api/kb/pdf-info?library=far&path=papers/a.pdf", nil)
	}
	lib, rel, abs, err := m.previewSource(req(), true)
	if err != nil {
		t.Fatal(err)
	}
	if lib.ID != "far" || rel != "papers/a.pdf" || !strings.HasPrefix(abs, roots.remoteCopies("far")) {
		t.Fatalf("got %s %s %s", lib.ID, rel, abs)
	}
	if b, _ := os.ReadFile(abs); string(b) != "%PDF-1.4 one" {
		t.Fatalf("copy holds %q", b)
	}
	before, _ := os.Stat(abs)

	// A page request uses the copy.
	if _, _, _, err := m.previewSource(req(), false); err != nil || stub.downloads != 1 {
		t.Fatalf("page request: err %v, %d downloads", err, stub.downloads)
	}
	// Opening it again fetches it, and the same bytes leave the copy as it was.
	if _, _, _, err := m.previewSource(req(), true); err != nil || stub.downloads != 2 {
		t.Fatalf("reopen: err %v, %d downloads", err, stub.downloads)
	}
	if after, _ := os.Stat(abs); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged file was written again")
	}
	// A changed file replaces the copy.
	stub.files["papers/a.pdf"] = []byte("%PDF-1.4 two")
	if _, _, _, err := m.previewSource(req(), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(abs); string(b) != "%PDF-1.4 two" {
		t.Fatalf("copy holds %q after a change", b)
	}
	// A path out of the library is refused before anything is fetched.
	bad := httptest.NewRequest("GET", "/api/kb/pdf-info?library=far&path=../../etc/passwd", nil)
	if _, _, _, err := m.previewSource(bad, true); err == nil {
		t.Fatal("a path outside the library was accepted")
	}
}
