package agentsmd

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// spaceStand is a person's own library and a folder shared with them,
// whose files are kept here.
type spaceStand struct {
	kb.Resolver
	kb.SpaceFS
	files   map[string]string
	changed []string
}

func (s *spaceStand) Library(_ *http.Request, id string) (kb.Library, error) {
	switch id {
	case "mine":
		return kb.Library{ID: "mine", Label: "My library", Space: true, Writable: true, Site: "cluster"}, nil
	case "theirs":
		return kb.Library{ID: "theirs", Label: "Team folder", Space: true, SharedFrom: "pat", Writable: true, Site: "cluster"}, nil
	case "far":
		return kb.Library{ID: "far", Label: "Far", Site: "box"}, nil
	}
	return kb.Library{}, apierr.New(404, "no library named %s", id)
}
func (s *spaceStand) Get(_ *http.Request, _ kb.Library, rel string, _ int64) ([]byte, error) {
	if b, ok := s.files[rel]; ok {
		return []byte(b), nil
	}
	return nil, apierr.New(404, "%s is not there", rel)
}
func (s *spaceStand) Mkdir(*http.Request, kb.Library, string) error { return nil }
func (s *spaceStand) Put(_ *http.Request, _ kb.Library, rel string, src io.Reader, _ int64) error {
	b, _ := io.ReadAll(src)
	s.files[rel] = string(b)
	return nil
}
func (s *spaceStand) Delete(_ *http.Request, _ kb.Library, rels []string) ([]string, []kb.Skipped, error) {
	for _, r := range rels {
		delete(s.files, r)
	}
	return rels, nil, nil
}
func (s *spaceStand) List(_ *http.Request, _ kb.Library, rel string) ([]kb.Entry, error) {
	seen := map[string]bool{}
	var out []kb.Entry
	for f := range s.files {
		if rest, ok := strings.CutPrefix(f, rel+"/"); ok && strings.Contains(rest, "/") {
			d := strings.SplitN(rest, "/", 2)[0]
			if !seen[d] {
				seen[d] = true
				out = append(out, kb.Entry{Name: d, Type: "dir"})
			}
		}
	}
	return out, nil
}
func (s *spaceStand) Changed(_ *http.Request, _ kb.Library, rels []string) {
	s.changed = append(s.changed, rels...)
}

func server(t *testing.T) (*httptest.Server, string, *spaceStand) {
	dir := t.TempDir()
	root := filepath.Join(dir, "kb")
	ix := filepath.Join(dir, "ix")
	os.MkdirAll(filepath.Join(root, ".agents"), 0o755)
	os.WriteFile(filepath.Join(root, ".agents", "reviewer.md"), []byte("---\nname: reviewer\ndescription: Reviews\n---\n\nCheck every claim.\n"), 0o644)
	os.MkdirAll(filepath.Join(ix, "extensions", "skills"), 0o755)
	os.WriteFile(filepath.Join(ix, "extensions", "skills", "pump.md"), []byte("---\nname: pump\ndescription: Size a pump\n---\n\nSteps.\n"), 0o644)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": ix, "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web"), "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(dir, "no-gufi")}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	kb.Register(mux, a)
	Register(mux, a)
	stand := &spaceStand{files: map[string]string{}}
	prevR, prevS := kb.LibraryResolver, kb.SpaceFiles
	kb.LibraryResolver, kb.SpaceFiles = stand, stand
	t.Cleanup(func() { kb.LibraryResolver, kb.SpaceFiles = prevR, prevS })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, root, stand
}

func call(t *testing.T, srv *httptest.Server, method, path string, body any) (int, map[string]any) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func names(v any) string {
	var s []string
	for _, x := range v.([]any) {
		s = append(s, x.(string))
	}
	return strings.Join(s, ",")
}

// Applying a template and a skill to a main-library folder writes both,
// reports them, and taking them off leaves the folder as it was.
func TestRoutesMainLibrary(t *testing.T) {
	srv, root, _ := server(t)
	code, out := call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"folder": "agents/review", "instructions": "Reviewer", "skills": []string{"pump"}})
	if code != 200 || out["exists"] != true || names(out["instructions"]) != "reviewer" || names(out["skills"]) != "pump" || out["path"] != "agents/review/AGENTS.md" {
		t.Fatalf("%d %v", code, out)
	}
	b, _ := os.ReadFile(filepath.Join(root, "agents/review/AGENTS.md"))
	if !strings.Contains(string(b), "Check every claim.") || strings.Contains(string(b), "description: Reviews") {
		t.Fatalf("AGENTS.md:\n%s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "agents/review/.agents/skills/pump/SKILL.md")); !strings.Contains(string(b), "description: \"Size a pump\"") {
		t.Fatalf("SKILL.md:\n%s", b)
	}
	code, out = call(t, srv, "GET", "/api/agents-md?folder=agents/review", nil)
	if code != 200 || names(out["instructions"]) != "reviewer" {
		t.Fatalf("%d %v", code, out)
	}
	code, out = call(t, srv, "POST", "/api/agents-md/remove", map[string]any{"folder": "agents/review", "instructions": "reviewer", "skills": []string{"pump"}})
	if code != 200 || out["exists"] != false || len(out["skills"].([]any)) != 0 {
		t.Fatalf("%d %v", code, out)
	}
	if code, out = call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"folder": "x", "instructions": "nobody"}); code != 404 {
		t.Fatalf("unknown template: %d %v", code, out)
	}
	if code, _ = call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"folder": "../out", "instructions": "reviewer"}); code != 400 {
		t.Fatalf("a folder outside the library: %d", code)
	}
}

// My library is written through the space's files, as the person; a
// folder shared with them and a library elsewhere are refused.
func TestRoutesSpaces(t *testing.T) {
	srv, root, stand := server(t)
	code, out := call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"library": "mine", "folder": "notes", "instructions": "reviewer"})
	if code != 200 || out["space"] != true || names(out["instructions"]) != "reviewer" {
		t.Fatalf("%d %v", code, out)
	}
	if !strings.Contains(stand.files["notes/AGENTS.md"], "Check every claim.") || strings.Join(stand.changed, ",") != "notes/AGENTS.md" {
		t.Fatalf("space files %v changed %v", stand.files, stand.changed)
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); !os.IsNotExist(err) {
		t.Fatal("a write in My library touched the main library")
	}
	for _, lib := range []string{"theirs", "far"} {
		if code, out := call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"library": lib, "folder": "notes", "instructions": "reviewer"}); code < 400 {
			t.Fatalf("%s: %d %v", lib, code, out)
		}
	}
}

// The folders list finds main-library folders by their markers (a file
// without any is left out) and folders of My library from the record,
// read again each time, so one whose section was removed drops out.
func TestRoutesFolders(t *testing.T) {
	srv, root, stand := server(t)
	os.MkdirAll(filepath.Join(root, "mine-only"), 0o755)
	os.WriteFile(filepath.Join(root, "mine-only", "AGENTS.md"), []byte("# my own\n"), 0o644)
	call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"folder": "agents/review", "instructions": "reviewer"})
	call(t, srv, "POST", "/api/agents-md/apply", map[string]any{"library": "mine", "folder": "notes", "skills": []string{"pump"}})
	code, out := call(t, srv, "GET", "/api/agents-md/folders", nil)
	list, _ := out["folders"].([]any)
	if code != 200 || len(list) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["folder"] != "agents/review" || names(first["instructions"]) != "reviewer" || second["library"] != "mine" || names(second["skills"]) != "pump" {
		t.Fatalf("folders %v", list)
	}
	delete(stand.files, ".agents/skills/pump/SKILL.md")
	delete(stand.files, "notes/.agents/skills/pump/SKILL.md")
	_, out = call(t, srv, "GET", "/api/agents-md/folders", nil)
	if l, _ := out["folders"].([]any); len(l) != 1 {
		t.Fatalf("a folder with nothing left stayed: %v", l)
	}
}
