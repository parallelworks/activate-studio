package sessions

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// fixture is a Studio in front of a fake platform: PW_GATEWAY_URL names
// the fake, PW_API_KEY is the deployment key, PW_CLI a stand-in CLI.
type fixture struct {
	t        *testing.T
	platform *httptest.Server
	mu       sync.Mutex
	sessions []map[string]any
	runs     map[string]any
	files    func(w http.ResponseWriter, r *http.Request)
	auth     []string
	studio   *httptest.Server
	routes   *Routes
	cliLog   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ResetCache()
	f := &fixture{t: t, runs: map[string]any{}}
	f.platform = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/sessions":
			w.Header().Set("Content-Type", "application/json")
			f.mu.Lock()
			json.NewEncoder(w).Encode(f.sessions)
			f.mu.Unlock()
		case strings.HasSuffix(r.URL.Path, "/files"):
			f.files(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/workflow-runs/"):
			slug := strings.TrimPrefix(r.URL.Path, "/api/workflow-runs/")
			run, ok := f.runs[slug]
			if !ok {
				w.WriteHeader(404)
				io.WriteString(w, `{"message":"not found"}`)
				return
			}
			json.NewEncoder(w).Encode(run)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.platform.Close)
	dir := t.TempDir()
	f.cliLog = filepath.Join(dir, "cli.log")
	cli := filepath.Join(dir, "pw")
	os.WriteFile(cli, []byte("#!/bin/sh\necho \"$* key=$PW_API_KEY\" >> "+f.cliLog+"\n"), 0o755)
	env := map[string]string{
		"PW_GATEWAY_URL":     f.platform.URL + "/api/openai/v1",
		"PW_API_KEY":         "deploy-key",
		"PW_CLI":             cli,
		"PW_CREDENTIALS_DIR": filepath.Join(dir, "creds"),
	}
	p := platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	a := &app.App{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	f.routes = Register(mux, a, p)
	f.studio = httptest.NewServer(mux)
	t.Cleanup(f.studio.Close)
	return f
}

func (f *fixture) get(path string, out any) int {
	f.t.Helper()
	res, err := http.Get(f.studio.URL + path)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Fatalf("%s: %v %s", path, err, b)
		}
	}
	return res.StatusCode
}

func TestRowShape(t *testing.T) {
	plat := "https://platform.example.com"
	raw := map[string]any{
		"name": "s1", "user": "alex", "type": "tunnel", "status": "running", "healthy": true,
		"externalHref": "https://s1.sessions.example.com/", "domainName": "s1.sessions.example.com",
		"internalHref": "/sessions/alex/s1", "targetName": "gpu",
		"workflowRun": map[string]any{"slug": "r7", "displayName": "Study", "number": 3, "status": "running"},
		"createdAt":   "2026-01-02T00:00:00Z",
	}
	b, _ := json.Marshal(raw)
	v, _ := decode(b)
	got := string(must(json.Marshal(Row(v, plat))))
	want := `{"name":"s1","user":"alex","type":"tunnel","status":"running","healthy":true,"url":"https://s1.sessions.example.com/","ownHost":true,"platformUrl":"https://platform.example.com/sessions/alex/s1","run":{"slug":"r7","workflow":"Study","number":3,"status":"running"},"resource":"gpu","createdAt":"2026-01-02T00:00:00Z"}`
	if got != want {
		t.Fatalf("\n%s\n%s", got, want)
	}
	v, _ = decode([]byte(`{"name":"x","externalHref":"/me/session/x/","internalHref":"/sessions/x?y"}`))
	got = string(must(json.Marshal(Row(v, plat))))
	if got != `{"name":"x","user":"","type":"","status":"","healthy":null,"url":"https://platform.example.com/me/session/x/","ownHost":false,"platformUrl":null,"run":null,"resource":null,"createdAt":null}` {
		t.Fatal(got)
	}
	if v, _ := decode([]byte(`{"user":"a"}`)); Row(v, plat) != nil {
		t.Fatal("a session with no name is dropped")
	}
	// A run recorded wins; without one, the name is the run or ends with it.
	if !OfRun(&SessionRow{Name: "viewer-r9"}, "r9") || OfRun(&SessionRow{Name: "viewer-r9", Run: &SessionRun{Slug: "r1"}}, "r9") || OfRun(&SessionRow{Name: "r9"}, "") {
		t.Fatal("OfRun")
	}
	if e := Embed(&SessionRow{Name: "a b*~", User: "u@x"}); e != "![a b*~](/?embed=session&user=u%40x&name=a+b*%7E)" {
		t.Fatal(e)
	}
}

func decode(b []byte) (any, error) { return jsonv.Decode(b) }

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestListAndItem(t *testing.T) {
	f := newFixture(t)
	f.sessions = []map[string]any{
		{"name": "old", "user": "alex", "status": "completed", "createdAt": "2026-01-01T00:00:00Z", "workflowRun": map[string]any{"slug": "r1"}},
		{"name": "new", "user": "alex", "status": "running", "createdAt": "2026-03-01T00:00:00Z", "workflowRun": map[string]any{"slug": "r1"},
			"externalHref": "https://new.sessions.example.com/", "domainName": "new.sessions.example.com"},
		{"name": "viewer-r2", "user": "sam", "status": "running"},
	}
	var list struct{ Sessions []map[string]any }
	if f.get("/api/sessions", &list) != 200 || len(list.Sessions) != 3 || list.Sessions[0]["name"] != "viewer-r2" || list.Sessions[1]["name"] != "new" {
		// String(null) is "null", which sorts after any date: Node's order.
		t.Fatal(list)
	}
	if f.auth[0] != "Bearer deploy-key" {
		t.Fatal(f.auth)
	}
	f.get("/api/sessions?run=r2", &list)
	if len(list.Sessions) != 1 || list.Sessions[0]["name"] != "viewer-r2" {
		t.Fatal(list)
	}
	var row map[string]any
	if f.get("/api/sessions/item?run=r1", &row); row["name"] != "new" {
		t.Fatal(row, "a run's running session wins")
	}
	if f.get("/api/sessions/item?url=https://NEW.sessions.example.com/x", &row); row["name"] != "new" {
		t.Fatal(row)
	}
	if f.get("/api/sessions/item?name=old&user=sam", &row) != 404 || row["error"] != "No session old is visible to you." {
		t.Fatal(row)
	}
	if f.get("/api/sessions/item?run=r9", &row) != 404 || row["error"] != "Run r9 has no session yet." {
		t.Fatal(row)
	}
	// Kept ten seconds per key.
	n := len(f.auth)
	f.get("/api/sessions", &list)
	if len(f.auth) != n {
		t.Fatal("cached")
	}
}

func TestListRefused(t *testing.T) {
	f := newFixture(t)
	f.platform.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	var body map[string]any
	if f.get("/api/sessions", &body) != 409 || body["error"] != "The platform refused the session list (401)." {
		t.Fatal(body)
	}
	ResetCache()
	f.platform.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	if f.get("/api/sessions", &body) != 502 {
		t.Fatal(body)
	}
}

func TestRunItemAndFile(t *testing.T) {
	f := newFixture(t)
	f.runs["r1"] = map[string]any{"slug": "r1", "number": 4, "status": "Completed", "resolved": "dropped", "inputs": map[string]any{"a": 1}}
	var run map[string]any
	if f.get("/api/runs/item?slug=r1", &run) != 200 || run["terminal"] != true || run["resolved"] != nil || run["number"].(float64) != 4 {
		t.Fatal(run)
	}
	res, _ := http.Get(f.studio.URL + "/api/runs/item?slug=r1")
	b, _ := io.ReadAll(res.Body)
	if string(b) != `{"slug":"r1","number":4,"status":"Completed","inputs":{"a":1},"terminal":true}` {
		t.Fatal(string(b))
	}
	if f.get("/api/runs/item?slug=nope", &run) != 404 || run["error"] != "No run nope is visible to you." {
		t.Fatal(run)
	}
	if f.get("/api/runs/item?slug=-x", &run) != 400 {
		t.Fatal(run)
	}
	for _, p := range []string{"", "/etc/passwd", "a/../b", strings.Repeat("x", 513)} {
		if f.get("/api/runs/file?slug=r1&path="+url.QueryEscape(p), &run) != 400 {
			t.Fatal(p)
		}
	}
	var down atomic.Bool
	f.files = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case down.Load():
			w.WriteHeader(500)
			io.WriteString(w, `{"message":"user workspace not found or is not running"}`)
		case r.URL.Query().Get("file") == "missing":
			w.WriteHeader(404)
			io.WriteString(w, `{}`)
		case r.URL.Query().Get("file") == "broken":
			w.WriteHeader(500)
			io.WriteString(w, `{"error":"disk"}`)
		default:
			json.NewEncoder(w).Encode("log of " + r.URL.Query().Get("file"))
		}
	}
	var out map[string]any
	if f.get("/api/runs/file?slug=r1&path=logs/step.out", &out) != 200 || out["text"] != "log of logs/step.out" {
		t.Fatal(out)
	}
	if f.get("/api/runs/file?slug=r1&path=missing", &out) != 404 || out["error"] != "No file missing in run r1." {
		t.Fatal(out)
	}
	if f.get("/api/runs/file?slug=r1&path=broken", &out) != 502 || out["error"] != "The platform could not read broken: disk" {
		t.Fatal(out)
	}
	// A workspace that scaled down: one start per key per three minutes.
	down.Store(true)
	for i := 0; i < 3; i++ {
		if f.get("/api/runs/file?slug=r1&path=x", &out) != 503 || !strings.HasPrefix(out["error"].(string), "Your platform workspace was not running") {
			t.Fatal(out)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	var log []byte
	for time.Now().Before(deadline) {
		if log, _ = os.ReadFile(f.cliLog); len(log) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	log, _ = os.ReadFile(f.cliLog)
	if string(log) != "workspace start key=deploy-key\n" {
		t.Fatalf("%q", log)
	}
}

/* ---- the relay ---- */

// relayFixture serves a session on its own TLS host at 127.0.0.1.
func relayFixture(t *testing.T, session http.Handler) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	srv := httptest.NewTLSServer(session)
	t.Cleanup(srv.Close)
	saved := platform.HTTPClient
	c := *saved
	c.Transport = srv.Client().Transport
	platform.HTTPClient = &c
	t.Cleanup(func() { platform.HTTPClient = saved })
	host := strings.TrimPrefix(srv.URL, "https://")
	f.sessions = []map[string]any{{"name": "s", "user": "alex", "status": "running", "externalHref": srv.URL + "/", "domainName": "127.0.0.1"}}
	return f, host
}

func TestRelayRefusesUnknownHostsAndRedirects(t *testing.T) {
	f, host := relayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://login.example.com/", http.StatusFound)
	}))
	var body map[string]any
	if f.get("/api/session-proxy/other.example.com/", &body) != 404 {
		t.Fatal(body)
	}
	if f.get("/api/session-proxy/"+host+"/x", &body) != 502 || body["error"] != "The session asked for a sign-in the Studio cannot complete; open it in a new tab." {
		t.Fatal(body)
	}
	// The folder address without its slash is redirected to it.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ := c.Get(f.studio.URL + "/api/session-proxy/" + host)
	if res.StatusCode != 302 || res.Header.Get("Location") != "/api/session-proxy/"+host+"/" {
		t.Fatal(res.Status, res.Header)
	}
}

func TestRelayHeadersRangeAndGuard(t *testing.T) {
	var seen http.Header
	var seenURL string
	f, host := relayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenURL = r.Header.Clone(), r.URL.RequestURI()
		w.Header().Set("Set-Cookie", "s=1")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("ETag", `"e1"`)
		if strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<!doctype html><html><head><title>t</title></head><body>hi</body></html>")
			return
		}
		http.ServeContent(w, r, "data.bin", time.Unix(0, 0), strings.NewReader("0123456789"))
	}))
	req, _ := http.NewRequest("GET", f.studio.URL+"/api/session-proxy/"+host+"/data.bin?x=1&y=%2F", nil)
	req.Header.Set("Range", "bytes=2-4")
	req.Header.Set("Cookie", "studio=secret")
	req.Header.Set("X-Other", "no")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 206 || string(b) != "234" || res.Header.Get("ETag") != `"e1"` || res.Header.Get("Set-Cookie") != "" || res.Header.Get("X-Frame-Options") != "" || res.Header.Get("Content-Range") != "" {
		t.Fatal(res.Status, string(b), res.Header)
	}
	if seenURL != "/data.bin?x=1&y=%2F" || seen.Get("Authorization") != "Bearer deploy-key" || seen.Get("Range") != "bytes=2-4" || seen.Get("Cookie") != "" || seen.Get("X-Other") != "" {
		t.Fatal(seenURL, seen)
	}
	res, _ = http.Get(f.studio.URL + "/api/session-proxy/" + host + "/page.html")
	b, _ = io.ReadAll(res.Body)
	if string(b) != "<!doctype html><html><head>"+FrameScrollGuard+"<title>t</title></head><body>hi</body></html>" || res.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatal(string(b))
	}
	// HEAD: the status and headers, no body.
	req, _ = http.NewRequest("HEAD", f.studio.URL+"/api/session-proxy/"+host+"/data.bin", nil)
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 200 || seen == nil || res.Header.Get("Content-Length") != "0" || res.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatal(res.Status, res.Header)
	}
}

func TestWithScrollGuard(t *testing.T) {
	g := FrameScrollGuard
	cases := map[string]string{
		"<HEAD lang=x>a":       "<HEAD lang=x>" + g + "a",
		"  <!DOCTYPE html><p>": "  <!DOCTYPE html>" + g + "<p>",
		"<p>bare":              g + "<p>bare",
	}
	for in, want := range cases {
		if got := WithScrollGuard(in); got != want {
			t.Fatalf("%q", got)
		}
	}
	if !strings.HasPrefix(g, "<script>/* ACTIVATE Studio: keep this page's scrolling inside its frame */(function () {\n  if (window.top === window) return;") || !strings.HasSuffix(g, "})();</script>") {
		t.Fatal("guard text")
	}
}

func TestRelayStreams(t *testing.T) {
	release := make(chan struct{})
	f, host := relayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "second\n")
	}))
	res, err := http.Get(f.studio.URL + "/api/session-proxy/" + host + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line := make(chan string, 1)
	rd := bufio.NewReader(res.Body)
	go func() { l, _ := rd.ReadString('\n'); line <- l }()
	select {
	case l := <-line:
		if l != "first\n" {
			t.Fatal(l)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first chunk did not arrive before the session finished")
	}
	close(release)
	rest, _ := io.ReadAll(rd)
	if string(rest) != "second\n" {
		t.Fatal(string(rest))
	}
}

// slowReader is size bytes of 'x'.
type slowReader struct {
	left int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.left == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > s.left {
		n = s.left
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	s.left -= n
	return n, nil
}

func TestRelayLargeBodyIsNotBuffered(t *testing.T) {
	const size = 50 << 20
	var sent atomic.Int64
	gate := make(chan struct{})
	f, host := relayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		buf := make([]byte, 1<<20)
		src := &slowReader{left: size}
		for i := 0; ; i++ {
			n, err := src.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				sent.Add(int64(n))
				w.(http.Flusher).Flush()
			}
			if i == 4 {
				<-gate // hold the rest until the client has the start
			}
			if err != nil {
				return
			}
		}
	}))
	res, err := http.Get(f.studio.URL + "/api/session-proxy/" + host + "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	// The session holds the rest until the client has the first megabyte:
	// a relay that buffered the body would never hand it over.
	head := make([]byte, 1<<20)
	got := make(chan error, 1)
	go func() { _, err := io.ReadFull(res.Body, head); got <- err }()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(gate)
		t.Fatal("the first megabyte did not arrive while the session held the rest")
	}
	if s := sent.Load(); s >= size {
		t.Fatal("the session sent everything before the client had the first megabyte")
	}
	close(gate)
	n, err := io.Copy(io.Discard, res.Body)
	if err != nil || n+int64(len(head)) != size {
		t.Fatal(n, err)
	}
}

// Node's relay has no websocket upgrade: an Upgrade request is fetched as
// a plain GET (undici drops the header), and the answer is relayed as is.
func TestRelayUpgradeIsAPlainGet(t *testing.T) {
	var upgrade string
	f, host := relayFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrade = r.Header.Get("Upgrade") + r.Header.Get("Connection")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(426)
		io.WriteString(w, "upgrade required")
	}))
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.studio.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /api/session-proxy/%s/ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", host)
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 100))
	if res.StatusCode != 426 || string(b) != "upgrade required" || upgrade != "" {
		t.Fatal(res.Status, string(b), upgrade)
	}
}
