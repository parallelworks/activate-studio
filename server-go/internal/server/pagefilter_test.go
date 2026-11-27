package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The page goes out as the filter writes it, at every address the app
// routes in the browser; the build's other files go out as they are.
func TestStaticPageFilter(t *testing.T) {
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte(`<link rel="icon" href="/favicon.svg" />`), 0o644)
	os.WriteFile(filepath.Join(dist, "favicon.svg"), []byte(`<svg>the mark</svg>`), 0o644)
	filter := func(r *http.Request, b []byte) []byte {
		return bytes.ReplaceAll(b, []byte("/favicon.svg"), []byte("/api/favicon?v=1"))
	}
	get := func(method, path, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		w := httptest.NewRecorder()
		if !serveStatic(w, r, dist, newCompressCache(), filter) {
			t.Fatal("not served")
		}
		return w
	}
	want := `<link rel="icon" href="/api/favicon?v=1" />`
	for _, p := range []string{"/", "/index.html", "/chat/42"} {
		w := get("GET", p, "")
		if w.Body.String() != want || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || w.Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: %v %s", p, w.Header(), w.Body)
		}
		if w.Header().Get("Cache-Control") != cacheControl("/index.html") {
			t.Errorf("%s: cache %s", p, w.Header().Get("Cache-Control"))
		}
	}
	w := get("GET", "/", "gzip, deflate, br")
	zr, err := gzip.NewReader(w.Body)
	if err != nil || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%v %v", err, w.Header())
	}
	if b, _ := io.ReadAll(zr); string(b) != want {
		t.Errorf("gzip: %s", b)
	}
	if w := get("GET", "/", "gzip;q=0, br"); w.Header().Get("Content-Encoding") != "" || w.Body.String() != want {
		t.Errorf("gzip refused: %v", w.Header())
	}
	if w := get("HEAD", "/", ""); w.Body.Len() != 0 || w.Header().Get("Content-Length") != "43" {
		t.Errorf("head: %v %d", w.Header(), w.Body.Len())
	}
	if w := get("GET", "/favicon.svg", ""); w.Body.String() != `<svg>the mark</svg>` {
		t.Errorf("file: %s", w.Body)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for accept, want := range map[string]bool{
		"": false, "gzip": true, "br": false, "gzip;q=0": false, "*": true, "*;q=0": false,
		"gzip;q=0, *": false, "x-gzip": true, " GZIP ; q=0.5": true, "br, *;q=0.1": true,
	} {
		if acceptsGzip(accept) != want {
			t.Errorf("%q: %v", accept, !want)
		}
	}
}
