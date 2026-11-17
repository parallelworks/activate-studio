package kb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ownedStub is one library id, "far", held by two people, each with
// their own files: as two people's spaces made of folders of one name.
type ownedStub struct {
	files map[string]map[string][]byte // by owner hash, then path
}

func ownerOf(r *http.Request) string { return r.Header.Get("X-Test-Owner") }

func (s *ownedStub) Library(r *http.Request, id string) (Library, error) {
	if id != "far" || ownerOf(r) == "" {
		return Library{}, Error(404, "no library named %s", id)
	}
	return Library{ID: "far", Label: "Far", IndexRoot: "remote:" + ownerOf(r) + ":far", Site: "box"}, nil
}
func (s *ownedStub) All(*http.Request) ([]Library, error)                 { return nil, nil }
func (s *ownedStub) Tree(*http.Request, Library, string) ([]Entry, error) { return nil, nil }
func (s *ownedStub) File(*http.Request, Library, string) (*Obj, error)    { return nil, nil }
func (s *ownedStub) Download(r *http.Request, _ Library, rel string) (string, []byte, error) {
	b, ok := s.files[ownerOf(r)][rel]
	if !ok {
		return "", nil, Error(404, "box: no such file")
	}
	return filepath.Base(rel), b, nil
}
func (s *ownedStub) Aggregate(context.Context, string, []string) (string, error) { return "", nil }
func (s *ownedStub) BuiltAt(string) string                                       { return "" }

func TestStateKeyIsPerOwner(t *testing.T) {
	ada, bob := "a1a1a1a1a1a1a1a1", "b2b2b2b2b2b2b2b2"
	for _, c := range []struct{ id, root, want string }{
		{"far", "remote:" + ada + ":far", "far@" + ada},
		{"far", "remote:" + bob + ":far", "far@" + bob},
		{"far", "remote:" + singleOwnerHash + ":far", "far"},
		{"far", "/srv/index/far", "far"},
		{"far", "", "far"},
		{"far", "remote:" + ada + ":other", "far"},
	} {
		if got := StateKey(c.id, c.root); got != c.want {
			t.Errorf("StateKey(%q, %q) = %q, want %q", c.id, c.root, got, c.want)
		}
	}
}

// One person's preview copies are never served to another whose library
// has the same id: a page request (which reads the kept copy without
// asking the cluster) finds none of the other's.
func TestPreviewCopiesAreKeptPerOwner(t *testing.T) {
	base := t.TempDir()
	roots := Roots{KBRoot: filepath.Join(base, "kb"), IndexBase: filepath.Join(base, "state"), IndexDir: filepath.Join(base, "state")}
	m := &Module{Roots: roots, Libs: &Libraries{Roots: roots, Label: func() string { return "Notes" }}}
	ada, bob := "a1a1a1a1a1a1a1a1", "b2b2b2b2b2b2b2b2"
	stub := &ownedStub{files: map[string]map[string][]byte{ada: {"plan.pdf": []byte("%PDF-1.4 ada's")}, bob: {}}}
	old := LibraryResolver
	LibraryResolver = stub
	defer func() { LibraryResolver = old }()
	req := func(owner string) *http.Request {
		r := httptest.NewRequest("GET", "/api/kb/pdf-page?library=far&path=plan.pdf", nil)
		r.Header.Set("X-Test-Owner", owner)
		return r
	}
	if _, _, abs, err := m.previewSource(req(ada), true); err != nil {
		t.Fatal(err)
	} else if b, _ := os.ReadFile(abs); string(b) != "%PDF-1.4 ada's" {
		t.Fatalf("ada's copy holds %q", b)
	}
	if _, _, abs, err := m.previewSource(req(bob), false); err == nil {
		b, _ := os.ReadFile(abs)
		t.Fatalf("bob was served %s (%q)", abs, b)
	}
}

// Labels on a person's library are theirs: another person's library of
// the same id neither sees them nor changes them.
func TestLibraryLabelsArePerOwner(t *testing.T) {
	roots := Roots{IndexBase: t.TempDir()}
	l := &LibraryLabels{Roots: roots}
	ada := Library{ID: "far", IndexRoot: "remote:a1a1a1a1a1a1a1a1:far"}
	bob := Library{ID: "far", IndexRoot: "remote:b2b2b2b2b2b2b2b2:far"}
	if _, err := l.Set(ada, "private/salary.xlsx", []string{"hr"}); err != nil {
		t.Fatal(err)
	}
	if m := l.Map(bob.StateKey()); len(m) != 0 {
		t.Fatalf("bob sees %v", m)
	}
	if _, err := l.Set(bob, "notes.txt", []string{"mine"}); err != nil {
		t.Fatal(err)
	}
	if m := l.Map(ada.StateKey()); len(m) != 1 || m["private/salary.xlsx"][0] != "hr" {
		t.Fatalf("ada's labels are now %v", m)
	}
	if e, ok := l.EntriesForIndexRoot(bob.IndexRoot); !ok || len(e) != 1 {
		t.Fatalf("bob's by index root: %v %v", e, ok)
	}
}
