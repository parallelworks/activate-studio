package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

func webDist(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "assets"), 0o755)
	os.WriteFile(filepath.Join(d, "index.html"), []byte("<html>shell</html>"), 0o644)
	os.WriteFile(filepath.Join(d, "assets", "index-AbCd1234.js"), []byte("js"), 0o644)
	os.WriteFile(filepath.Join(d, "logo.svg"), []byte("<svg/>"), 0o644)
	os.WriteFile(filepath.Join(d, "manifest.webmanifest"), []byte("static"), 0o644)
	return d
}

// fakeNode stands in for the Node server: it echoes what it was asked.
func fakeNode(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "data: %d\n\n", i)
				w.(http.Flusher).Flush()
				time.Sleep(100 * time.Millisecond)
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"node": r.Method + " " + r.URL.RequestURI(), "body": string(body), "host": r.Host, "xff": r.Header.Get("X-Forwarded-For")})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func front(t *testing.T, dist string, node *url.URL) *httptest.Server {
	t.Helper()
	cfg := config.Config{WebDist: dist, GufiBin: t.TempDir(), GufiIndex: filepath.Join(t.TempDir(), "none")}
	srv := httptest.NewServer(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), node))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, u string) (*http.Response, string) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func TestHealthzIsGo(t *testing.T) {
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	f := front(t, webDist(t), u)
	res, body := get(t, f.URL+"/healthz")
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"gufi":false,"ok":true}` {
		t.Fatalf("healthz %d %s", res.StatusCode, body)
	}
}

func TestStaticAndFallback(t *testing.T) {
	f := front(t, webDist(t), nil)
	res, body := get(t, f.URL+"/assets/index-AbCd1234.js")
	if body != "js" || res.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("hashed asset: %q %q", body, res.Header.Get("Cache-Control"))
	}
	res, body = get(t, f.URL+"/logo.svg")
	if body != "<svg/>" || res.Header.Get("Cache-Control") != "no-store, must-revalidate" {
		t.Fatalf("plain file: %q %q", body, res.Header.Get("Cache-Control"))
	}
	for _, p := range []string{"/", "/stats/map", "/../../etc/passwd", "/assets/"} {
		res, body = get(t, f.URL+p)
		if res.StatusCode != 200 || body != "<html>shell</html>" || res.Header.Get("Cache-Control") != "no-store, must-revalidate" {
			t.Fatalf("%s: %d %q", p, res.StatusCode, body)
		}
	}
	// No Node: an API path is a JSON 404, as Node answers one it lacks.
	res, body = get(t, f.URL+"/api/nothing")
	if res.StatusCode != 404 || !strings.Contains(body, "not found") {
		t.Fatalf("api without node: %d %s", res.StatusCode, body)
	}
}

func TestProxyToNode(t *testing.T) {
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	f := front(t, webDist(t), u)
	res, body := get(t, f.URL+"/api/kb/tree?path=a%20b")
	if res.StatusCode != 200 || !strings.Contains(body, `"node":"GET /api/kb/tree?path=a%20b"`) {
		t.Fatalf("api: %s", body)
	}
	fu, _ := url.Parse(f.URL)
	if !strings.Contains(body, `"host":"`+fu.Host+`"`) || !strings.Contains(body, `"xff":"127.0.0.1"`) {
		t.Fatalf("host and forwarded-for: %s", body)
	}
	// Node's routes outside /api win over a static file of the same name.
	_, body = get(t, f.URL+"/manifest.webmanifest")
	if !strings.Contains(body, `"node":"GET /manifest.webmanifest"`) {
		t.Fatalf("manifest: %s", body)
	}
	res2, err := http.Post(f.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if !strings.Contains(string(b), `"body":"{\"x\":1}"`) {
		t.Fatalf("post body: %s", b)
	}
}

func TestEventStreamIsNotBuffered(t *testing.T) {
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	f := front(t, webDist(t), u)
	start := time.Now()
	res, err := http.Get(f.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || line != "data: 0\n" {
		t.Fatalf("first event: %q %v", line, err)
	}
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("the first event took %s: the stream is buffered", d)
	}
}

func TestNodeDown(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:1")
	f := front(t, webDist(t), u)
	res, body := get(t, f.URL+"/api/config")
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(body, "Node server") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
}
