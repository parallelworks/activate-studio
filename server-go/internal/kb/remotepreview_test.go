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

// statStub is remoteStub that also reads a file's size and time there.
type statStub struct {
	remoteStub
	mtime int64
	stats int
	deny  bool
}

func (s *statStub) Stat(_ *http.Request, _ Library, rel string) (int64, int64, error) {
	s.stats++
	if s.deny {
		return 0, 0, Error(403, "box: permission denied")
	}
	b, ok := s.files[rel]
	if !ok {
		return 0, 0, Error(404, "box: no such file")
	}
	return int64(len(b)), s.mtime, nil
}

// Opening a file on another system again checks its size and time there
// and uses the copy kept from the same ones, without fetching it whole; a
// change fetches it, and a file the viewer can no longer read is refused
// though a copy is kept.
func TestPreviewReusesACopyOfAnUnchangedFile(t *testing.T) {
	base := t.TempDir()
	roots := Roots{KBRoot: filepath.Join(base, "kb"), IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}
	m := &Module{Roots: roots, Libs: &Libraries{Roots: roots, Label: func() string { return "Notes" }}}
	stub := &statStub{remoteStub: remoteStub{files: map[string][]byte{"papers/a.pdf": []byte("%PDF-1.4 one")}}, mtime: 100}
	old := LibraryResolver
	LibraryResolver = stub
	defer func() { LibraryResolver = old }()
	req := func() *http.Request {
		return httptest.NewRequest("GET", "/api/kb/pdf-info?library=far&path=papers/a.pdf", nil)
	}
	for i := 0; i < 3; i++ {
		if _, _, _, err := m.previewSource(req(), true); err != nil {
			t.Fatal(err)
		}
	}
	if stub.downloads != 1 || stub.stats != 3 {
		t.Fatalf("%d downloads and %d checks for three opens of an unchanged file", stub.downloads, stub.stats)
	}
	stub.files["papers/a.pdf"] = []byte("%PDF-1.4 two!")
	stub.mtime = 200
	_, _, abs, err := m.previewSource(req(), true)
	if err != nil || stub.downloads != 2 {
		t.Fatalf("after a change: err %v, %d downloads", err, stub.downloads)
	}
	if b, _ := os.ReadFile(abs); string(b) != "%PDF-1.4 two!" {
		t.Fatalf("copy holds %q", b)
	}
	stub.deny = true
	if _, _, _, err := m.previewSource(req(), true); err == nil {
		t.Fatal("a kept copy was shown to a viewer who can no longer read the file")
	}
}
