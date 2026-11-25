//go:build !windows

package userlibs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/links"
)

// oneResolver finds the libraries a person has, as the libraries track does.
type oneResolver struct {
	kb.Resolver
	h *harness
}

func (o oneResolver) Library(r *http.Request, id string) (kb.Library, error) {
	owner := auth.UserOf(r).ID
	d := find(o.h.m.load(owner), id)
	if d == nil {
		return kb.Library{}, kb.Error(404, "no library named %s", id)
	}
	return kb.Library{ID: id, Label: d.Label, IndexRoot: libraries.RemoteRootOf(owner, id), Site: d.Site, Space: d.Space != nil || d.Shared != nil}, nil
}

// Ada's library, a folder of it shared with bob and with cy, which bob
// added and cy did not, and dee, who has nothing: what each of them finds
// for a link, and what a link follows.
func TestLinksInSharedFolders(t *testing.T) {
	h, homes := buildHarness(t)
	h.auth = true
	h.m.OwnerKey = func(owner, _ string) (string, bool) { return "key-" + owner, true }
	root := filepath.Join(homes["cluster-a"], "studio")
	put := func(rel, s string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("proj/sub/plan.md", "p")
	put("own.md", "o")
	uid := os.Getuid()
	def := func(id, folder string) libraries.UserDef {
		return libraries.UserDef{ID: id, Label: id, Site: "cluster-a", State: "ready", SourceRoot: &folder, IndexRoot: &folder, GufiBin: &folder}
	}
	ada := def("u-space", root)
	ada.Space = &libraries.SpaceInfo{Account: "ada", UID: uid}
	bob := def("u-shared-proj", filepath.Join(root, "proj"))
	bob.Shared = &libraries.SharedInfo{ShareID: "s-1", From: "ada", Access: "write", UID: uid}
	// Bob also has a library of his own by the name of ada's, and one he
	// made himself over the same folder.
	same, beside := def("u-space", filepath.Join(homes["cluster-a"], "work")), def("u-beside", root)
	h.m.Users.Save("ada", []libraries.UserDef{ada})
	h.m.Users.Save("bob", []libraries.UserDef{bob, same})
	h.m.Users.Save("cy", nil)
	h.m.Users.Save("dee", []libraries.UserDef{beside})
	share := Share{ID: "s-1", Owner: "ada", Space: "u-space", Site: "cluster-a", Root: root, Folder: root + "/proj", Rel: "proj", Kind: "user", Name: "bob", Access: "write",
		Adopters: []Adopter{{Owner: "bob", Library: "u-shared-proj"}}}
	h.m.modifyShares(func([]Share) ([]Share, error) { return []Share{share}, nil })

	old, oldDefault := kb.LibraryResolver, kb.Default
	kb.LibraryResolver = oneResolver{h: h}
	store := links.New(t.TempDir())
	kb.Default = &kb.Module{Links: store}
	t.Cleanup(func() { kb.LibraryResolver, kb.Default = old, oldDefault })
	s := spaceFiles{h.m}
	as := func(who string) *http.Request {
		return auth.WithUser(httptest.NewRequest(http.MethodGet, "/api/links/x", nil), &auth.User{ID: who, Username: who})
	}
	lib := func(who, id string) kb.Library {
		l, err := kb.LibraryResolver.Library(as(who), id)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	found := func(who string, p links.Place) string {
		t.Helper()
		at, err := s.Find(as(who), p)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range at {
			out = append(out, x.Lib.ID+":"+x.Rel)
		}
		return strings.Join(out, " ")
	}

	// One item, one place, whoever asks.
	p, err := s.Place(as("ada"), lib("ada", "u-space"), "proj/sub/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Place(as("bob"), lib("bob", "u-shared-proj"), "sub/plan.md")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, q, p, "the same place")
	eq(t, p, links.Place{Owner: "ada", Lib: "u-space", Rel: "proj/sub/plan.md"}, "in its owner's library")
	// Nobody asks for another's library by its root.
	if _, err := s.Place(as("bob"), lib("ada", "u-space"), "own.md"); err == nil {
		t.Error("a place in another person's library")
	}
	if _, _, err := s.Look(as("bob"), lib("ada", "u-space"), []string{"own.md"}); err == nil {
		t.Error("looked in another person's library")
	}

	for _, c := range []struct{ who, rel, want string }{
		{"ada", "proj/sub/plan.md", "u-space:proj/sub/plan.md"},
		{"ada", "", "u-space:"},
		{"bob", "proj/sub/plan.md", "u-shared-proj:sub/plan.md"},
		{"bob", "proj", "u-shared-proj:"},
		{"bob", "own.md", ""},
		{"bob", "project/x.md", ""},
		{"bob", "", ""},
		{"cy", "proj/sub/plan.md", ""},
		{"dee", "proj/sub/plan.md", ""},
	} {
		if got := found(c.who, links.Place{Owner: "ada", Lib: "u-space", Rel: c.rel}); got != c.want {
			t.Errorf("%s at %q: %q, want %q", c.who, c.rel, got, c.want)
		}
	}

	// Look answers as the viewer's session finds it.
	look, ok, err := s.Look(as("bob"), lib("bob", "u-shared-proj"), []string{"sub/plan.md", "sub", "", "gone.md"})
	if err != nil || !ok || len(look) != 3 || !look["sub/plan.md"].Readable || look["sub/plan.md"].Dir || !look["sub"].Dir {
		t.Fatalf("%v %v %v", look, ok, err)
	}
	mine, _, _ := s.Look(as("ada"), lib("ada", "u-space"), []string{"proj/sub/plan.md"})
	if !mine["proj/sub/plan.md"].Same.Known() || mine["proj/sub/plan.md"].Same != look["sub/plan.md"].Same {
		t.Errorf("%v and %v", mine, look)
	}

	// The ID follows what either of them does to it.
	rec, err := store.Make(p, false, mine["proj/sub/plan.md"].Same, "ada")
	if err != nil {
		t.Fatal(err)
	}
	where := func() string { r, _, _ := store.Get(rec.ID); return r.Rel }
	h.m.linkMoved("bob", find(h.m.load("bob"), "u-shared-proj"), "sub", "deeper/sub")
	if where() != "proj/deeper/sub/plan.md" {
		t.Errorf("bob's move: %s", where())
	}
	h.m.linkMoved("ada", find(h.m.load("ada"), "u-space"), "proj/deeper", "proj/sub2")
	if where() != "proj/sub2/sub/plan.md" {
		t.Errorf("ada's move: %s", where())
	}
	// Bob's library of the same name is not hers.
	h.m.linkDeleted("bob", find(h.m.load("bob"), "u-space"), []string{"proj"})
	if r, _, _ := store.Get(rec.ID); r.Deleted != "" {
		t.Error("another library's delete ended it")
	}
	h.m.linkDeleted("bob", find(h.m.load("bob"), "u-shared-proj"), []string{"sub2"})
	if r, _, _ := store.Get(rec.ID); r.Deleted == "" {
		t.Error("bob's delete did not end it")
	}

	// The share is taken back: nothing is found, and the place of an item in
	// what is left of bob's library is not known.
	h.m.modifyShares(func([]Share) ([]Share, error) { return nil, nil })
	if got := found("bob", p); got != "" {
		t.Errorf("after the share ended: %s", got)
	}
	if _, err := s.Place(as("bob"), lib("bob", "u-shared-proj"), "sub/plan.md"); err == nil {
		t.Error("a place in a share that is no more")
	}
	// Bob's library going does not touch ada's links; hers going does.
	h.m.linksRemoved(libraries.RemoteRootOf("bob", "u-space"))
	if _, ok, _ := store.Get(rec.ID); !ok {
		t.Error("bob's library took ada's links with it")
	}
	h.m.linksRemoved(libraries.RemoteRootOf("ada", "u-space"))
	if _, ok, _ := store.Get(rec.ID); ok {
		t.Error("kept after its library was removed")
	}
	if b, _ := json.Marshal(share); strings.Contains(string(b), rec.ID) {
		t.Error("an ID in a share")
	}
}

// A write in a person's library goes on with the file's link: its name and
// place, and the file an upload or a save put in its place.
func TestWritesInAPersonsLibraryKeepItsLinks(t *testing.T) {
	h, homes := buildHarness(t)
	root := filepath.Join(homes["cluster-a"], "work")
	os.MkdirAll(filepath.Join(root, "dest"), 0o755)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("one"), 0o644)
	id := "u-work"
	d := libraries.UserDef{ID: id, Label: "Work", Site: "cluster-a", State: "ready", SourceRoot: &root, IndexRoot: &root, GufiBin: &root,
		Build: &libraries.RemoteBuild{SourceRoot: root}, Own: &libraries.SpaceInfo{Account: "me", UID: os.Getuid()}, Guard: true}
	if err := h.m.Users.Save("single", []libraries.UserDef{d}); err != nil {
		t.Fatal(err)
	}
	store := links.New(t.TempDir())
	oldDefault := kb.Default
	kb.Default = &kb.Module{Links: store}
	t.Cleanup(func() { kb.Default = oldDefault })
	s := spaceFiles{h.m}
	lib := kb.Library{ID: id, Label: "Work", IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	same := func(rel string) links.Same {
		t.Helper()
		got, ok, err := s.Look(req, lib, []string{rel})
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		return got[rel].Same
	}
	p, err := s.Place(req, lib, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Make(p, false, same("a.md"), "single")
	if err != nil {
		t.Fatal(err)
	}
	check := func(what, rel string) {
		t.Helper()
		r, ok, _ := store.Get(rec.ID)
		if !ok || r.Rel != rel || r.Deleted != "" || !r.Same.Is(same(rel)) {
			t.Errorf("%s: %+v, the file is %+v", what, r, same(rel))
		}
	}
	first := same("a.md")
	if err := s.Put(req, lib, "a.md", strings.NewReader("two"), 100); err != nil {
		t.Fatal(err)
	}
	if same("a.md") == first {
		t.Fatal("an upload does not replace the file here, so this tests nothing")
	}
	check("an upload over it", "a.md")
	if _, err := s.Rename(req, lib, "a.md", "b.md"); err != nil {
		t.Fatal(err)
	}
	check("renamed", "b.md")
	if _, _, err := s.Move(req, lib, []string{"b.md"}, "dest"); err != nil {
		t.Fatal(err)
	}
	check("moved", "dest/b.md")
	if _, err := s.Rename(req, lib, "dest", "dest2"); err != nil {
		t.Fatal(err)
	}
	check("its folder renamed", "dest2/b.md")
	if err := s.SaveText(req, lib, "dest2/b.md", []byte("three"), kb.TextVersion([]byte("two"))); err != nil {
		t.Fatal(err)
	}
	check("saved", "dest2/b.md")
	if _, _, err := s.Delete(req, lib, []string{"dest2"}); err != nil {
		t.Fatal(err)
	}
	if r, _, _ := store.Get(rec.ID); r.Deleted == "" {
		t.Error("not ended with its folder")
	}
}
