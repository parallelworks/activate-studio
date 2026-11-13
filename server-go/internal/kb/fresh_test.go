package kb

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A page image is checked against its source file on every view: the
// same version answers 304, a rewritten file a new tag.
func TestFreshRevalidatesAgainstTheSource(t *testing.T) {
	f := filepath.Join(t.TempDir(), "a.pdf")
	os.WriteFile(f, []byte("one"), 0o644)
	w := httptest.NewRecorder()
	if fresh(w, httptest.NewRequest("GET", "/", nil), f, "p1") {
		t.Fatal("no tag sent yet")
	}
	tag := w.Header().Get("ETag")
	if tag == "" || w.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("headers %v", w.Header())
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	if !fresh(w, r, f, "p1") || w.Code != 304 {
		t.Errorf("same version: %d", w.Code)
	}
	w = httptest.NewRecorder()
	if fresh(w, r, f, "p2") {
		t.Error("another page has its own tag")
	}
	os.WriteFile(f, []byte("two, longer"), 0o644)
	os.Chtimes(f, time.Now().Add(time.Second), time.Now().Add(time.Second))
	w = httptest.NewRecorder()
	if fresh(w, r, f, "p1") || w.Header().Get("ETag") == tag {
		t.Error("a rewritten file is sent again")
	}
}
