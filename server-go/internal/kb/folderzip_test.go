package kb

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// zipStub is a library on another system with folders.
type zipStub struct {
	remoteStub
	dirs map[string][]Entry
}

func (s *zipStub) Tree(_ *http.Request, _ Library, rel string) ([]Entry, error) {
	return s.dirs[rel], nil
}

func zipEntries(t *testing.T, body []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func names(m map[string]string) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestAFolderDownloadsAsAZip(t *testing.T) {
	kbRoot, ix, outside := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(rel, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(kbRoot, rel)), 0o755)
		os.WriteFile(filepath.Join(kbRoot, rel), []byte(s), 0o644)
	}
	write("reports/plan.md", "# Plan")
	write("reports/sub/notes.txt", "notes")
	write("reports/.secret", "hidden")
	write("reports/node_modules/x.js", "machinery")
	write("reports/old.zip", "archive")
	write("reports/mod.pyc", "build output")
	write("reports/.studio-upload-1/data", "arriving")
	write("top.txt", "top")
	os.WriteFile(filepath.Join(outside, "elsewhere.txt"), []byte("outside"), 0o644)
	os.Symlink(outside, filepath.Join(kbRoot, "reports/linked-dir"))
	os.Symlink(filepath.Join(outside, "elsewhere.txt"), filepath.Join(kbRoot, "reports/linked-file.txt"))

	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	mux := http.NewServeMux()
	m.Routes(mux)
	get := func(url string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec
	}

	// The folder, with what the Library shows in it and nothing a link
	// points to outside it.
	rec := get("/api/kb/download?path=reports&folder=1")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || rec.Header().Get("Content-Disposition") != `attachment; filename="reports.zip"` {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	got := zipEntries(t, rec.Body.Bytes())
	if names(got) != "reports/old.zip reports/plan.md reports/sub/notes.txt" || got["reports/sub/notes.txt"] != "notes" {
		t.Errorf("zip holds %q", names(got))
	}
	// A folder inside a folder is named for itself.
	if got := zipEntries(t, get("/api/kb/download?path=reports/sub&folder=1").Body.Bytes()); names(got) != "sub/notes.txt" {
		t.Errorf("sub: %q", names(got))
	}
	// Without folder=1 a folder is refused, as before.
	if rec := get("/api/kb/download?path=reports"); rec.Code != 400 {
		t.Errorf("unnamed folder: %d", rec.Code)
	}
	// A file still downloads as itself.
	if rec := get("/api/kb/download?path=reports/plan.md"); rec.Code != 200 || rec.Body.String() != "# Plan" {
		t.Errorf("file: %d %q", rec.Code, rec.Body.String())
	}
	// A path that leaves the library is refused.
	if rec := get("/api/kb/download?path=../&folder=1"); rec.Code != 400 {
		t.Errorf("escape: %d", rec.Code)
	}

	// A folder of a library on another system is read through it, file
	// by file, when the Library names it a folder.
	stub := &zipStub{remoteStub: remoteStub{files: map[string][]byte{"docs/one.txt": []byte("one"), "docs/deep/two.txt": []byte("two"), "docs/.hidden": []byte("h")}}, dirs: map[string][]Entry{
		"docs":       {{Name: "one.txt", Type: "file", Size: 3}, {Name: "deep", Type: "dir"}, {Name: ".hidden", Type: "file"}, {Name: "build", Type: "dir"}},
		"docs/deep":  {{Name: "two.txt", Type: "file", Size: 3, Mtime: 1700000000}},
		"docs/build": {{Name: "out.txt", Type: "file"}},
	}}
	old := LibraryResolver
	LibraryResolver = stub
	defer func() { LibraryResolver = old }()
	rec = get("/api/kb/download?library=far&path=docs&folder=1")
	if rec.Code != 200 {
		t.Fatalf("remote: %d %s", rec.Code, rec.Body.String())
	}
	if got := zipEntries(t, rec.Body.Bytes()); names(got) != "docs/deep/two.txt docs/one.txt" || got["docs/deep/two.txt"] != "two" || stub.downloads != 2 {
		t.Errorf("remote zip %q, %d downloads", names(got), stub.downloads)
	}
	// What is still arriving there is not listed, hidden files shown or not.
	stub.dirs["docs"] = append(stub.dirs["docs"], Entry{Name: ".studio-upload-1", Type: "dir"}, Entry{Name: ".studio-transfer-1", Type: "dir"})
	for _, q := range []string{"", "&hidden=1"} {
		rec := get("/api/kb/tree?library=far&path=docs" + q)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "one.txt") || strings.Contains(rec.Body.String(), ".studio-") || strings.Contains(rec.Body.String(), ".hidden") != (q != "") {
			t.Errorf("tree%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	// Too many files is refused before anything is sent.
	many := []Entry{}
	for i := 0; i <= zipMaxRemoteFiles; i++ {
		many = append(many, Entry{Name: "f" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + ".txt", Type: "file"})
	}
	stub.dirs["big"] = many
	if rec := get("/api/kb/download?library=far&path=big&folder=1"); rec.Code != 413 || !strings.Contains(rec.Body.String(), "too large") {
		t.Errorf("big: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAFolderZipIsSizedFirst(t *testing.T) {
	kbRoot, ix := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(kbRoot, "data/deep"), 0o755)
	os.WriteFile(filepath.Join(kbRoot, "data/a.txt"), []byte("12345"), 0o644)
	os.WriteFile(filepath.Join(kbRoot, "data/deep/b.txt"), []byte("123"), 0o644)
	env := map[string]string{"KB_ROOT": kbRoot, "INDEX_BASE": ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(ix, "no-gufi")}
	cfg, _ := config.Load(func(k string) string { return env[k] })
	m := New(app.New(cfg, slog.New(slog.DiscardHandler), nil, nil))
	mux := http.NewServeMux()
	m.Routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/kb/download?path=data&folder=1&check=1", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"bytes":8,"files":2,"name":"data.zip"}` {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
