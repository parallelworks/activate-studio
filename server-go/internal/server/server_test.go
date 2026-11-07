package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		if r.URL.Path == "/api/longstream" {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := 0; i < 100; i++ {
				if _, err := fmt.Fprintf(w, "data: %d\n\n", i); err != nil {
					return
				}
				w.(http.Flusher).Flush()
				time.Sleep(20 * time.Millisecond)
			}
			return
		}
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

// The ETag is Node's (W/"<size hex>-<mtime ms hex>"), and a request that
// sends it back gets a 304.
func TestStaticETagAsNode(t *testing.T) {
	dist := webDist(t)
	mtime := time.UnixMilli(1793987792827)
	os.Chtimes(filepath.Join(dist, "index.html"), mtime, mtime)
	srv := front(t, dist, nil)
	res, err := http.Get(srv.URL + "/some/deep/link")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	want := `W/"12-1a1b2014fbb"` // 18 bytes, 1793987792827 ms
	if got := res.Header.Get("ETag"); got != want {
		t.Fatalf("ETag %q, want %q", got, want)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/", nil)
	req.Header.Set("If-None-Match", want)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotModified {
		t.Fatalf("revalidation: %d, want 304", res.StatusCode)
	}
}

// A stream the client leaves (Stop in the chat) is still in the access log.
func TestAbortedStreamIsLogged(t *testing.T) {
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	logs := make(chan string, 16)
	cfg := config.Config{WebDist: webDist(t), GufiBin: t.TempDir(), GufiIndex: filepath.Join(t.TempDir(), "none")}
	h := slog.New(slog.NewTextHandler(writerFunc(func(b []byte) { logs <- string(b) }), nil))
	f := httptest.NewServer(New(cfg, h, u))
	t.Cleanup(f.Close)
	res, err := http.Get(f.URL + "/api/longstream")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(res.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	select {
	case line := <-logs:
		if !strings.Contains(line, "path=/api/longstream") || !strings.Contains(line, "aborted=true") || !strings.Contains(line, "via=node") {
			t.Fatalf("log line %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the aborted stream was not logged")
	}
}

type writerFunc func([]byte)

func (f writerFunc) Write(b []byte) (int, error) { f(append([]byte(nil), b...)); return len(b), nil }

// A second burst of concurrent requests reuses the connections to Node the
// first one opened, in place of opening new ones.
func TestProxyKeepsConnectionsForABurst(t *testing.T) {
	var mu sync.Mutex
	opened := 0
	hold := make(chan struct{})
	node := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hold
		w.Write([]byte("{}"))
	}))
	node.Config.ConnState = func(c net.Conn, st http.ConnState) {
		if st == http.StateNew {
			mu.Lock()
			opened++
			mu.Unlock()
		}
	}
	node.Start()
	t.Cleanup(node.Close)
	u, _ := url.Parse(node.URL)
	f := front(t, webDist(t), u)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 100}}
	burst := func() {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := client.Get(f.URL + "/api/x")
				if err == nil {
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
				}
			}()
		}
		time.Sleep(200 * time.Millisecond) // all 20 waiting in Node at once
		for i := 0; i < 20; i++ {
			hold <- struct{}{}
		}
		wg.Wait()
	}
	burst()
	mu.Lock()
	first := opened
	mu.Unlock()
	burst()
	mu.Lock()
	defer mu.Unlock()
	if first != 20 || opened-first > 2 {
		t.Fatalf("connections to Node: %d for the first burst, %d more for the second", first, opened-first)
	}
}

// Every event of a stream reaches the browser before Node writes the next
// one: Node here waits for the client to read each event, so a buffered
// proxy would stall. This holds for both event streams, for an event
// stream whose type carries a charset, and for a response with no length
// (the session relay).
func TestStreamsAreNotBuffered(t *testing.T) {
	next := make(chan struct{})
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat/stream", "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
		case "/api/charset-stream":
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		default: // the relay: chunked, no Content-Length
			w.Header().Set("Content-Type", "text/html")
		}
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	t.Cleanup(node.Close)
	u, _ := url.Parse(node.URL)
	f := front(t, webDist(t), u)
	for _, p := range []string{"/api/chat/stream", "/v1/chat/completions", "/api/charset-stream", "/api/session-proxy/h/x"} {
		req, _ := http.NewRequest("POST", f.URL+p, strings.NewReader("{}"))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		lines := make(chan string)
		go func() {
			br := bufio.NewReader(res.Body)
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					close(lines)
					return
				}
				if l != "\n" {
					lines <- l
				}
			}
		}()
		for i := 0; i < 3; i++ {
			select {
			case l := <-lines:
				if l != fmt.Sprintf("data: %d\n", i) {
					t.Fatalf("%s: event %d is %q", p, i, l)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: event %d did not arrive before Node wrote the next: buffered", p, i)
			}
			next <- struct{}{}
		}
		res.Body.Close()
	}
}

// A response of known length is not flushed on its way through (it is
// written out once, when it ends); an event stream is.
func TestOnlyStreamsAreFlushed(t *testing.T) {
	node := fakeNode(t)
	u, _ := url.Parse(node.URL)
	s := New(config.Config{WebDist: webDist(t), GufiBin: t.TempDir(), GufiIndex: filepath.Join(t.TempDir(), "none")},
		slog.New(slog.NewTextHandler(io.Discard, nil)), u)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"node":"GET /api/config"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Flushed {
		t.Fatal("a JSON answer of known length was flushed")
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stream", nil))
	if !rec.Flushed || !strings.Contains(rec.Body.String(), "data: 2") {
		t.Fatalf("event stream: flushed %v, body %q", rec.Flushed, rec.Body.String())
	}
}
