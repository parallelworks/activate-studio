package kb_test

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

// A sheet is served with its file's version as its tag, so a browser
// holding it is answered 304 until the file changes; a long one is
// gzipped for a browser that takes that.
func TestASheetIsServedByItsFilesVersion(t *testing.T) {
	root, get, _ := viewing(t)
	f := excelize.NewFile()
	for r := 1; r <= 600; r++ {
		if err := f.SetSheetRow("Sheet1", fmt.Sprintf("A%d", r), &[]any{fmt.Sprintf("item %d", r), r * 3}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SaveAs(filepath.Join(root, "items.xlsx")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	w := get("/api/kb/sheet?path=items.xlsx")
	var s struct {
		Name      string
		Rows      int
		TotalRows int
		Sheets    []struct{ Name string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || w.Code != 200 || s.Rows != 600 || s.Name != "Sheet1" || len(s.Sheets) != 1 {
		t.Fatalf("%d %v %+v", w.Code, err, s)
	}
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no tag")
	}
	_, _, mux := viewingMux(t, root)
	req := httptest.NewRequest("GET", "/api/kb/sheet?path=items.xlsx", nil)
	req.Header.Set("If-None-Match", tag)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Errorf("same version: %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/kb/sheet?path=items.xlsx&sheet=0", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	zr, err := gzip.NewReader(rec.Body)
	if err != nil || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip: %v %v", err, rec.Header())
	}
	body, _ := io.ReadAll(zr)
	if err := json.Unmarshal(body, &s); err != nil || s.Rows != 600 {
		t.Errorf("gzipped: %v %d", err, s.Rows)
	}
	if w := get("/api/kb/sheet?path=items.xlsx&sheet=x"); w.Code != 400 {
		t.Errorf("bad sheet: %d", w.Code)
	}
	if w := get("/api/kb/sheet?path=../items.xlsx"); w.Code < 400 {
		t.Errorf("outside the library: %d", w.Code)
	}
}
