package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// A client's X-Studio-Internal never reaches Node; only a request a Go
// route marked with app.HandOff carries it.
func TestHandOffMarksOnlyMarkedRequests(t *testing.T) {
	var seen []string
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"="+r.Header.Get(app.InternalHeader))
	}))
	defer node.Close()
	u, _ := url.Parse(node.URL)
	p := newProxy(u, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/marked" {
			r = app.HandOff(r, "test")
		}
		p.ServeHTTP(w, r)
	})
	front := httptest.NewServer(h)
	defer front.Close()
	for _, path := range []string{"/plain", "/forged", "/marked"} {
		req, _ := http.NewRequest("GET", front.URL+path, nil)
		if path == "/forged" {
			req.Header.Set(app.InternalHeader, "1")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	want := []string{"/plain=", "/forged=", "/marked=1"}
	if len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("got %v, want %v", seen, want)
	}
	if why, ok := app.IsHandOff(app.HandOff(httptest.NewRequest("GET", "/", nil), "x")); !ok || why != "x" {
		t.Fatal("IsHandOff")
	}
}
