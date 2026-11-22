package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// spaceStub is a person's space whose writes are recorded.
type spaceStub struct {
	remoteStub
	calls []string
	files map[string]string
}

func (s *spaceStub) Library(r *http.Request, id string) (Library, error) {
	switch id {
	case "mine":
		return Library{ID: "mine", Label: "My space", IndexRoot: "remote:abc:mine", Site: "cluster", Space: true, Writable: true}, nil
	case "far":
		return Library{ID: "far", Label: "Far", IndexRoot: "remote:abc:far", Site: "box", Writable: true}, nil
	}
	return Library{}, Error(404, "no library named %s", id)
}

func (s *spaceStub) All(r *http.Request) ([]Library, error) {
	a, _ := s.Library(r, "mine")
	return []Library{a}, nil
}

func (s *spaceStub) note(c string) { s.calls = append(s.calls, c) }

func (s *spaceStub) List(_ *http.Request, _ Library, rel string) ([]Entry, error) {
	s.note("list " + rel)
	return []Entry{{Name: "a.txt", Path: "a.txt", Type: "file"}}, nil
}
func (s *spaceStub) Mkdir(_ *http.Request, _ Library, rel string) error {
	s.note("mkdir " + rel)
	return nil
}
func (s *spaceStub) Put(_ *http.Request, _ Library, rel string, src io.Reader, _ int64) error {
	b, _ := io.ReadAll(src)
	s.files[rel] = string(b)
	s.note("put " + rel)
	return nil
}
func (s *spaceStub) Rename(_ *http.Request, _ Library, rel, name string) (string, error) {
	s.note("rename " + rel + " " + name)
	return filepath.Join(filepath.Dir(rel), name), nil
}
func (s *spaceStub) Move(_ *http.Request, _ Library, rels []string, dest string) ([]Moved, []Skipped, error) {
	s.note("move " + strings.Join(rels, ",") + " " + dest)
	return []Moved{{From: rels[0], To: dest + "/" + filepath.Base(rels[0])}}, nil, nil
}
func (s *spaceStub) Delete(_ *http.Request, _ Library, rels []string) ([]string, []Skipped, error) {
	s.note("delete " + strings.Join(rels, ","))
	return rels, nil, nil
}
func (s *spaceStub) Get(_ *http.Request, _ Library, rel string, _ int64) ([]byte, error) {
	s.note("get " + rel)
	if b, ok := s.files[rel]; ok {
		return []byte(b), nil
	}
	return nil, Error(404, "%s is not there", rel)
}
func (s *spaceStub) Changed(_ *http.Request, _ Library, rels []string) {
	s.note("changed " + strings.Join(rels, ","))
}

// The Library's writes in a person's space go to SpaceFiles, as them; the
// files of this host are never touched for it, and a library on another
// system that is not a space is not written here at all.
func TestWritesInASpaceRunThroughSpaceFiles(t *testing.T) {
	kbRoot, ix := t.TempDir(), t.TempDir()
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	mux := http.NewServeMux()
	m.Routes(mux)
	stub := &spaceStub{files: map[string]string{}}
	oldR, oldS := LibraryResolver, SpaceFiles
	LibraryResolver, SpaceFiles = stub, stub
	defer func() { LibraryResolver, SpaceFiles = oldR, oldS }()

	do := func(method, url, ct string, body io.Reader) (int, map[string]any) {
		req := httptest.NewRequest(method, url, body)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	js := func(url, body string) (int, map[string]any) {
		return do("POST", url, "application/json", strings.NewReader(body))
	}

	if c, b := js("/api/kb/mkdir", `{"library":"mine","path":"notes"}`); c != 200 || b["created"] != "notes" {
		t.Fatalf("mkdir: %d %v", c, b)
	}
	if c, b := js("/api/kb/rename", `{"library":"mine","path":"notes/a.txt","name":"b.txt"}`); c != 200 || fmt.Sprint(b["renamed"]) != "map[from:notes/a.txt to:notes/b.txt]" {
		t.Fatalf("rename: %d %v", c, b)
	}
	if c, b := js("/api/kb/move", `{"library":"mine","paths":["notes/b.txt"],"dest":"plans"}`); c != 200 || b["reindexing"] != true {
		t.Fatalf("move: %d %v", c, b)
	}
	if c, b := js("/api/kb/delete", `{"library":"mine","paths":["plans"]}`); c != 200 {
		t.Fatalf("delete: %d %v", c, b)
	}
	if c, _ := do("DELETE", "/api/kb/dir?library=mine&path=old", "", nil); c != 200 {
		t.Fatalf("delete dir: %d", c)
	}
	// From All, the combined tree's path names the space first.
	if c, b := do("DELETE", "/api/kb/file?library=all&path=mine/old.txt", "", nil); c != 200 || b["deleted"] != "old.txt" {
		t.Fatalf("delete from All: %d %v", c, b)
	}
	var mp bytes.Buffer
	w := multipart.NewWriter(&mp)
	f, _ := w.CreateFormFile("file", "report.txt")
	f.Write([]byte("the report"))
	w.Close()
	if c, b := do("POST", "/api/kb/upload?library=mine&dir=inbox", w.FormDataContentType(), &mp); c != 200 || b["deferred"] != true {
		t.Fatalf("upload: %d %v", c, b)
	}
	if stub.files["inbox/report.txt"] != "the report" {
		t.Fatalf("uploaded: %v", stub.files)
	}
	if c, b := do("GET", "/api/kb/tree?library=mine&path=", "", nil); c != 200 || len(b["entries"].([]any)) != 1 {
		t.Fatalf("tree: %d %v", c, b)
	}
	want := []string{"mkdir notes", "changed notes", "rename notes/a.txt b.txt", "changed notes/a.txt,notes/b.txt",
		"move notes/b.txt plans", "changed notes/b.txt,plans", "delete plans", "changed plans", "delete old", "changed old",
		"delete old.txt", "changed old.txt", "put inbox/report.txt", "changed inbox/report.txt", "list "}
	if strings.Join(stub.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(stub.calls, "\n"), strings.Join(want, "\n"))
	}

	// Refused before anything is written.
	for _, c := range []struct{ url, body, why string }{
		{"/api/kb/delete", `{"library":"mine","paths":[""]}`, "own folder"},
		{"/api/kb/delete", `{"library":"mine","paths":["notes/../../etc"]}`, "not changeable"},
		{"/api/kb/move", `{"library":"mine","paths":[".ssh"],"dest":"x"}`, "not changeable"},
		{"/api/kb/rename", `{"library":"mine","path":"a.txt","name":"../b"}`, "without slashes"},
		{"/api/kb/copy", `{"library":"mine","paths":["a.txt"],"dest":"x"}`, "not available"},
	} {
		n := len(stub.calls)
		code, b := js(c.url, c.body)
		if code < 400 || !strings.Contains(b["error"].(string), c.why) || len(stub.calls) != n {
			t.Errorf("%s %s: %d %v", c.url, c.body, code, b)
		}
	}
	// A library on another system that is not a space is not written here,
	// and this host's folder is untouched.
	if code, b := js("/api/kb/mkdir", `{"library":"far","path":"x"}`); code != 403 {
		t.Fatalf("far: %d %v", code, b)
	}
	if entries, _ := os.ReadDir(kbRoot); len(entries) != 0 {
		t.Fatalf("this host's folder was written: %v", entries)
	}
}
