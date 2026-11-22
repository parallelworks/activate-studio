//go:build !windows

package userlibs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// TestListAfterWrite: a folder listed, written in from the Library, and
// listed again shows the write at once, over pw ssh and over plain ssh,
// where the session's site (ssh://me@host:port) is not the library's
// (me@host) and the kept listing must still be dropped.
func TestListAfterWrite(t *testing.T) {
	h, _ := buildHarness(t)
	for _, c := range []struct {
		name, path string
		body       map[string]any
		site       string
	}{
		{"pw", "/api/user-libraries/pw/build", map[string]any{"cluster": "cluster-a", "sourceRoot": "~/work", "depth": "names"}, "cluster-a"},
		{"ssh", "/api/user-libraries/ssh/build", map[string]any{"destination": "me@linux-box:2222", "sourceRoot": "~/project", "label": "Project", "depth": "names"}, "me@linux-box"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := h.post(c.path, c.body)
			if b.Status != 200 {
				t.Fatalf("%v", b.Body)
			}
			id := b.Body["id"].(string)
			if got := h.waitFor(id); got["state"] != "ready" || got["mine"] != true {
				t.Fatalf("built: %v", got)
			}
			s := h.m.SpaceFiles()
			lib := kb.Library{ID: id, IndexRoot: libraries.RemoteRootOf("single", id), Mine: true, Site: c.site}
			req := httptest.NewRequest(http.MethodGet, "/api/kb/list", nil)
			names := func(rel string) string {
				t.Helper()
				es, err := s.List(req, lib, rel)
				if err != nil {
					t.Fatal(err)
				}
				var n []string
				for _, e := range es {
					n = append(n, e.Name)
				}
				return strings.Join(n, ",")
			}
			before := names("")
			if err := s.Put(req, lib, "added.txt", strings.NewReader("x"), 10); err != nil {
				t.Fatal(err)
			}
			if after := names(""); !strings.Contains(after, "added.txt") {
				t.Fatalf("an upload is missing from the next listing: before=[%s] after=[%s]", before, after)
			}
			if err := s.Mkdir(req, lib, "made"); err != nil {
				t.Fatal(err)
			}
			if after := names(""); !strings.Contains(after, "made") {
				t.Fatalf("a new folder is missing from the next listing: [%s]", after)
			}
			if _, err := s.Rename(req, lib, "added.txt", "renamed.txt"); err != nil {
				t.Fatal(err)
			}
			if after := names(""); !strings.Contains(after, "renamed.txt") || strings.Contains(after, "added.txt") {
				t.Fatalf("a rename is not in the next listing: [%s]", after)
			}
			if _, _, err := s.Delete(req, lib, []string{"renamed.txt"}); err != nil {
				t.Fatal(err)
			}
			if after := names(""); strings.Contains(after, "renamed.txt") {
				t.Fatalf("a deleted file is still listed: [%s]", after)
			}
		})
	}
}
