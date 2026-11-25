//go:build !windows

package userlibs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TestTextMadeAndSavedAsThePerson: a text file is made and edited in a
// library of the person's own on a system they reach, over pw ssh and
// plain ssh, and never over a change made there since.
func TestTextMadeAndSavedAsThePerson(t *testing.T) {
	h, homes := buildHarness(t)
	for _, c := range []struct {
		name, path, dir, site string
		body                  map[string]any
	}{
		{"pw", "/api/user-libraries/pw/build", filepath.Join(homes["cluster-a"], "work"), "cluster-a", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/work", "depth": "names"}},
		{"ssh", "/api/user-libraries/ssh/build", filepath.Join(homes["me@linux-box"], "project"), "me@linux-box", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "label": "Project", "depth": "names"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := h.post(c.path, c.body)
			if b.Status != 200 {
				t.Fatalf("%v", b.Body)
			}
			id := b.Body["id"].(string)
			if got := h.waitFor(id); got["state"] != "ready" {
				t.Fatalf("built: %v", got)
			}
			s, ok := h.m.SpaceFiles().(kb.TextFS)
			if !ok {
				t.Fatal("text is not written in a person's library")
			}
			lib := kb.Library{ID: id, IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: c.site}
			req := httptest.NewRequest(http.MethodPut, "/api/kb/text", nil)
			state := func(err error) string {
				var e *kb.TextConflict
				if errors.As(err, &e) {
					return e.State
				}
				t.Helper()
				t.Fatalf("%v", err)
				return ""
			}
			f := filepath.Join(c.dir, "notes", "Untitled.md")
			if _, err := h.m.SpaceFiles().List(req, lib, ""); err != nil {
				t.Fatal(err)
			}
			if err := s.CreateText(req, lib, "notes/Untitled.md", nil); err != nil {
				t.Fatal(err)
			}
			es, err := h.m.SpaceFiles().List(req, lib, "notes")
			if err != nil || len(es) != 1 || es[0].Name != "Untitled.md" {
				t.Fatalf("the new file is not listed: %v %v", es, err)
			}
			if got := state(s.CreateText(req, lib, "notes/Untitled.md", []byte("x"))); got != kb.TextExists {
				t.Error(got)
			}
			if err := s.SaveText(req, lib, "notes/Untitled.md", []byte("# One\n"), kb.TextVersion(nil)); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(f); string(b) != "# One\n" {
				t.Fatalf("%q", b)
			}
			if b, err := s.OpenText(req, lib, "notes/Untitled.md", 100); err != nil || string(b) != "# One\n" {
				t.Fatalf("%q %v", b, err)
			}
			if _, err := s.OpenText(req, lib, "../out.md", 100); err == nil {
				t.Error("opened outside")
			}
			// Changed on its own machine, with the same size.
			write(t, f, "# Two\n")
			err = s.SaveText(req, lib, "notes/Untitled.md", []byte("mine"), kb.TextVersion([]byte("# One\n")))
			var e *kb.TextConflict
			if !errors.As(err, &e) || e.State != kb.TextChanged || e.Version != kb.TextVersion([]byte("# Two\n")) {
				t.Fatalf("%v", err)
			}
			if b, _ := os.ReadFile(f); string(b) != "# Two\n" {
				t.Fatalf("written over: %q", b)
			}
			if err := s.SaveText(req, lib, "notes/Untitled.md", []byte("mine"), e.Version); err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(filepath.Join(c.dir, "notes"))
			if got := state(s.SaveText(req, lib, "notes/Untitled.md", []byte("x"), kb.TextVersion([]byte("mine")))); got != kb.TextMissing {
				t.Error(got)
			}
			if _, err := os.Stat(filepath.Join(c.dir, "notes")); err == nil {
				t.Error("a deleted folder came back")
			}
			for _, p := range []string{"../out.md", "notes/../../out.md", ""} {
				if err := s.CreateText(req, lib, p, []byte("x")); err == nil {
					t.Errorf("made %q", p)
				}
				if err := s.SaveText(req, lib, p, []byte("x"), kb.TextVersion(nil)); err == nil {
					t.Errorf("saved %q", p)
				}
			}
			// Nobody else's library, by its id.
			other := lib
			other.IndexRoot = libraries.RemoteRootOf("someone", id)
			if err := s.CreateText(req, other, "x.md", nil); err == nil || !strings.Contains(err.Error(), "no such") {
				t.Errorf("%v", err)
			}
			if l, _ := filepath.Glob(filepath.Join(c.dir, ".studio-*")); len(l) > 0 {
				t.Errorf("left behind: %v", l)
			}
		})
	}
}

// Where another account could change the folder, text is neither opened to
// edit, made nor saved, as the Library's other writes are not, and is
// answered as that rather than as a name taken or a file changed.
func TestTextIsNotWrittenInAFolderOthersCouldChange(t *testing.T) {
	h, homes := buildHarness(t)
	t.Setenv("STUDIO_HOME_CLUSTER", "cluster-a")
	h.m.GufiBin = fakeBundle(t, gufiBinDir())
	open := filepath.Join(homes["cluster-a"], "open")
	f := filepath.Join(open, "a.md")
	write(t, f, "a")
	os.Chmod(open, 0o755)
	b := h.post("/api/user-libraries/space", map[string]any{"sourceRoot": "~/open", "depth": "names"})
	if b.Status != 200 {
		t.Fatalf("%v", b.Body)
	}
	id := b.Body["id"].(string)
	if got := h.waitFor(id); got["state"] != "ready" || got["mine"] != true {
		t.Fatalf("%v", got)
	}
	s := h.m.SpaceFiles().(kb.TextFS)
	lib := kb.Library{ID: id, IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: "cluster-a"}
	req := httptest.NewRequest(http.MethodPut, "/api/kb/text", nil)
	if err := s.SaveText(req, lib, "a.md", []byte("b"), kb.TextVersion([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	os.Chmod(open, 0o777)
	_, opened := s.OpenText(req, lib, "a.md", 100)
	for op, err := range map[string]error{
		"open":              opened,
		"save":              s.SaveText(req, lib, "a.md", []byte("c"), kb.TextVersion([]byte("b"))),
		"save, out of date": s.SaveText(req, lib, "a.md", []byte("c"), kb.TextVersion([]byte("a"))),
		"make":              s.CreateText(req, lib, "new.md", []byte("c")),
		"make, name taken":  s.CreateText(req, lib, "a.md", []byte("c")),
	} {
		var c *kb.TextConflict
		if err == nil || errors.As(err, &c) || !strings.Contains(err.Error(), "can be written by every account") || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s: %v", op, err)
		}
	}
	if b, _ := os.ReadFile(f); string(b) != "b" {
		t.Fatalf("%q", b)
	}
	if es, _ := os.ReadDir(open); len(es) != 1 {
		t.Fatalf("%v", es)
	}
	os.Chmod(open, 0o755)
	if err := s.CreateText(req, lib, "new.md", nil); err != nil {
		t.Fatal(err)
	}
}
