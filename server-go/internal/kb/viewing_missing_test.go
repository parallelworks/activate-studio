package kb_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/docwrite"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// viewing is a server over a scratch library, with what it logged.
func viewing(t *testing.T) (root string, get func(string) *httptest.ResponseRecorder, logged *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "kb")
	os.MkdirAll(root, 0o755)
	t.Setenv("PW_CLI", filepath.Join(dir, "no-pw"))
	cfg, _ := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": filepath.Join(dir, "ix"), "KB_ROOT": root, "STUDIO_WEB_DIST": filepath.Join(dir, "web")}[k]
	})
	logged = &bytes.Buffer{}
	mux := http.NewServeMux()
	kb.Register(mux, app.New(cfg, slog.New(slog.NewTextHandler(logged, nil)), nil, nil))
	get = func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	return root, get, logged
}

// A file renamed or deleted while it is open is a 404 on every route
// that draws it, with a message the viewer can read and nothing logged
// as a fault.
func TestViewingRoutesAnswer404ForAMissingFile(t *testing.T) {
	root, get, logged := viewing(t)
	os.WriteFile(filepath.Join(root, "plain.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(root, "folder"), 0o755)
	routes := map[string]string{
		"file":           "report.md",
		"download":       "report.pdf",
		"raw":            "report.pdf",
		"html":           "report.html",
		"pdf":            "report.docx",
		"pdf-info":       "report.pdf",
		"pdf-page":       "report.pdf",
		"office-preview": "report.docx",
		"office-media":   "report.docx",
	}
	for route, name := range routes {
		for _, rel := range []string{name, "gone/" + name, "plain.txt/" + name} {
			u := "/api/kb/" + route + "?path=" + url.QueryEscape(rel) + "&page=3&name=word/media/image1.png"
			w := get(u)
			var body struct{ Error string }
			json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != 404 || body.Error != "no such file" {
				t.Errorf("%s %s: %d %s", route, rel, w.Code, w.Body.String())
			}
			if w.Header().Get("ETag") != "" {
				t.Errorf("%s %s: a 404 carries a tag", route, rel)
			}
		}
	}
	// A folder is not a file to draw either.
	for _, route := range []string{"download", "raw", "pdf-info", "pdf-page", "office-preview"} {
		if w := get("/api/kb/" + route + "?path=folder"); w.Code != 400 {
			t.Errorf("%s of a folder: %d %s", route, w.Code, w.Body.String())
		}
	}
	if strings.Contains(logged.String(), "unhandled route error") {
		t.Errorf("logged as a fault:\n%s", logged.String())
	}
}

// The print form of an HTML page reads the file itself; a missing one is
// the same 404.
func TestPrintingAMissingHTMLPageIsA404(t *testing.T) {
	_, get, _ := viewing(t)
	if w := get("/api/kb/html?path=gone.html&print=1"); w.Code != 404 {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

// A page past the end of a PDF, as the viewer asks for after the file is
// rewritten shorter, is a 416 that carries the page count; the pages that
// exist are served, and nothing is logged as a fault.
func TestAPagePastTheEndCarriesThePageCount(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("no poppler here")
	}
	root, get, logged := viewing(t)
	pdf := filepath.Join(root, "report.pdf")
	write := func(md string, at time.Time) int {
		r, err := docwrite.Render(".pdf", md, nil)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(pdf, r.Data, 0o644)
		os.Chtimes(pdf, at, at)
		w := get("/api/kb/pdf-info?path=report.pdf")
		var info struct{ Pages int }
		json.Unmarshal(w.Body.Bytes(), &info)
		if w.Code != 200 || info.Pages < 1 {
			t.Fatalf("pdf-info: %d %s", w.Code, w.Body.String())
		}
		return info.Pages
	}
	long := write("# Long\n\n"+strings.Repeat("A paragraph of sample text for the page.\n\n", 400), time.Now().Add(-time.Hour))
	if long < 3 {
		t.Fatalf("the long version has %d pages", long)
	}
	last := "/api/kb/pdf-page?path=report.pdf&page=" + itoa(long)
	if w := get(last); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("last page of the long version: %d %s", w.Code, w.Body.String())
	}
	short := write("# Short\n\nOne paragraph.", time.Now().Add(time.Second))
	if short >= long {
		t.Fatalf("the short version has %d pages", short)
	}
	w := get(last)
	var body struct {
		Error       string
		Page, Pages int
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 416 || body.Pages != short || body.Page != long || body.Error == "" || w.Header().Get("X-Page-Count") != itoa(short) {
		t.Errorf("past the end: %d %s (X-Page-Count %q)", w.Code, w.Body.String(), w.Header().Get("X-Page-Count"))
	}
	if w.Header().Get("ETag") != "" {
		t.Error("the 416 carries a page image's tag")
	}
	if w := get("/api/kb/pdf-page?path=report.pdf&page=" + itoa(short)); w.Code != 200 {
		t.Errorf("last page of the short version: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(logged.String(), "unhandled route error") {
		t.Errorf("logged as a fault:\n%s", logged.String())
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
