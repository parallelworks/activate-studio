package kb

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/ownfile"
)

// studioText is the AGENTS.md the Studio keeps in a folder.
func studioText() string {
	const body = "Use the index first."
	return ownfile.Begin(body) + "\n\n" + body + "\n\n<!-- ACTIVATE Studio instructions studio_tools: end -->\n"
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func namesOf(es []Entry) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return strings.Join(out, " ")
}

// A folder lists without the Studio's own AGENTS.md, with it when hidden
// files are shown, and always with one a person wrote in.
func TestAFolderListsWithoutTheStudiosOwnAgentsFile(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"AGENTS.md": studioText(), "a.md": "a", "sub/AGENTS.md": studioText() + "\nUse tabs.\n", "mine/AGENTS.md": "# My rules\n"})
	for _, c := range []struct {
		rel    string
		hidden bool
		want   string
	}{{"", false, "mine sub a.md"}, {"", true, "mine sub a.md AGENTS.md"}, {"sub", false, "AGENTS.md"}, {"mine", false, "AGENTS.md"}} {
		es, err := ListDir(c.rel, root, c.hidden)
		if err != nil {
			t.Fatal(err)
		}
		if got := namesOf(es); got != c.want {
			t.Errorf("%q, hidden %v: %s, want %s", c.rel, c.hidden, got, c.want)
		}
	}
	zip, err := localFolderFiles(root, "kb")
	if err != nil {
		t.Fatal(err)
	}
	var in []string
	for _, f := range zip {
		in = append(in, f.name)
	}
	if got := strings.Join(in, " "); got != "kb/a.md kb/mine/AGENTS.md kb/sub/AGENTS.md" {
		t.Errorf("the folder's zip: %s", got)
	}
}

// ownStub is a space whose folder holds an AGENTS.md of a person's or the
// Studio's.
type ownStub struct{ spaceStub }

func (s *ownStub) List(_ *http.Request, _ Library, _ string) ([]Entry, error) {
	return []Entry{{Name: "a.md", Path: "a.md", Type: "file"},
		{Name: "AGENTS.md", Path: "AGENTS.md", Type: "file", Size: int64(len(s.files["AGENTS.md"])), Mtime: 5}}, nil
}

// A space lists as its folder is now, so its AGENTS.md is read there, once
// for each change of it.
func TestASpaceListsWithoutTheStudiosOwnAgentsFile(t *testing.T) {
	kbRoot, ix := t.TempDir(), t.TempDir()
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	mux := http.NewServeMux()
	m.Routes(mux)
	stub := &ownStub{spaceStub{files: map[string]string{"AGENTS.md": studioText()}}}
	oldR, oldS := LibraryResolver, SpaceFiles
	LibraryResolver, SpaceFiles = stub, stub
	defer func() { LibraryResolver, SpaceFiles = oldR, oldS }()
	list := func(q string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/kb/tree?library=mine"+q, nil))
		var out struct{ Entries []Entry }
		if json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		return namesOf(out.Entries)
	}
	for range 3 {
		if got := list(""); got != "a.md" {
			t.Fatalf("listed: %s", got)
		}
	}
	reads := 0
	for _, c := range stub.noted() {
		if c == "get AGENTS.md" {
			reads++
		}
	}
	if reads != 1 {
		t.Errorf("read %d times for one state of it", reads)
	}
	if got := list("&hidden=1"); got != "a.md AGENTS.md" {
		t.Errorf("with hidden files: %s", got)
	}
	stub.files["AGENTS.md"] += "\nUse tabs.\n"
	if got := list(""); got != "a.md AGENTS.md" {
		t.Errorf("with a person's line in it: %s", got)
	}
}
