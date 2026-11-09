package ragendpoint

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

func setup(t *testing.T, env map[string]string, settings string) (*http.ServeMux, *Endpoint, string) {
	t.Helper()
	ix, kb := t.TempDir(), t.TempDir()
	if settings != "" {
		os.WriteFile(filepath.Join(ix, "settings.json"), []byte(settings), 0o644)
	}
	cfg, err := config.Load(func(k string) string {
		return map[string]string{"INDEX_BASE": ix, "KB_ROOT": kb, "PORT": "5999", "STUDIO_NODE": "off"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	p := platform.New(platform.Options{IndexBase: ix, KBRoot: kb, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}})
	mux := http.NewServeMux()
	e := Register(mux, a, p)
	return mux, e, ix
}

func call(t *testing.T, mux *http.ServeMux, method, path string) *jsonv.Object {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	v, err := jsonv.Decode(w.Body.Bytes())
	if err != nil || w.Code != 200 {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return jsonv.Obj(v)
}

func TestRefusals(t *testing.T) {
	mux, _, _ := setup(t, map[string]string{"PW_API_KEY": "k"}, "")
	got := call(t, mux, "GET", "/api/rag/endpoint")
	if string(jsonv.MustMarshal(got)) != `{"state":"stopped","name":null,"url":null,"startedAt":null,"detail":null}` {
		t.Fatal(string(jsonv.MustMarshal(got)))
	}
	got = call(t, mux, "POST", "/api/rag/endpoint/start")
	if d, _ := got.Get("detail"); !strings.HasPrefix(d.(string), "No explicit platform target") {
		t.Fatal(d)
	}
	mux, _, _ = setup(t, map[string]string{"PW_API_KEY": "k", "PW_CONTEXT": "c"}, `{"ragProxyEnabled":false}`)
	if d, _ := call(t, mux, "POST", "/api/rag/endpoint/start").Get("detail"); !strings.HasPrefix(d.(string), "The /v1 surface is disabled") {
		t.Fatal(d)
	}
	mux, _, _ = setup(t, map[string]string{"PW_PLATFORM_HOST": "h", "PW_CLI": "/nonexistent-dir-x/pw", "PATH": ""}, "")
	if d, _ := call(t, mux, "POST", "/api/rag/endpoint/start").Get("detail"); !strings.HasPrefix(d.(string), "No deployment gateway credential") {
		t.Fatal(d)
	}
}

func TestNameFromAppName(t *testing.T) {
	_, e, _ := setup(t, nil, `{"appName":"My Fancy  Studio!"}`)
	if e.Name() != "my-fancy-studio-rag" {
		t.Fatal(e.Name())
	}
	_, e, _ = setup(t, nil, `{"ragEndpointName":"pinned"}`)
	if e.Name() != "pinned" {
		t.Fatal(e.Name())
	}
}

// The endpoint agent: a fake pw that records its arguments, prints the
// URL, and runs the command after --, as `pw endpoints run` does.
func TestStartRunsTheForwarderAndStop(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	envf := filepath.Join(dir, "env")
	cli := filepath.Join(dir, "pw")
	os.WriteFile(cli, []byte("#!/bin/sh\necho \"$@\" > "+args+"\necho \"$STUDIO_TARGET_PORT $STUDIO_INJECT_KEY $PW_CONTEXT\" > "+envf+"\necho 'Endpoint ready at https://studio-rag.example.com.'\nexec sleep 30\n"), 0o755)
	mux, e, _ := setup(t, map[string]string{"PW_API_KEY": "deploykey", "PW_CONTEXT": "ctx1", "PW_CLI": cli}, "")
	e.Self = []string{"/bin/self", Subcommand}
	got := call(t, mux, "POST", "/api/rag/endpoint/start")
	if s, _ := got.Get("state"); s != "starting" {
		t.Fatal(string(jsonv.MustMarshal(got)))
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.Status().State != "running" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	st := e.Status()
	if st.State != "running" || st.URL != "https://studio-rag.example.com" || st.Name != "studio-rag" {
		t.Fatalf("%+v", st)
	}
	b, _ := os.ReadFile(args)
	if strings.TrimSpace(string(b)) != "endpoints run --context ctx1 --openai --name studio-rag --subdomain studio-rag -o text -- /bin/self rag-forward" {
		t.Fatalf("%q", b)
	}
	b, _ = os.ReadFile(envf)
	if strings.TrimSpace(string(b)) != "5999 deploykey ctx1" {
		t.Fatalf("%q", b)
	}
	if s, _ := call(t, mux, "POST", "/api/rag/endpoint/stop").Get("state"); s != "stopped" {
		t.Fatal(s)
	}
	// As Node: the child's exit by its signal then reads as an error.
	deadline = time.Now().Add(5 * time.Second)
	for e.Status().State != "error" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if st := e.Status(); st.State != "error" || !strings.HasPrefix(st.Detail, "exited with code null: ") {
		t.Fatalf("%+v", st)
	}
}

func TestMissingCLI(t *testing.T) {
	mux, _, _ := setup(t, map[string]string{"PW_API_KEY": "k", "PW_CONTEXT": "c", "PW_CLI": filepath.Join(t.TempDir(), "pw")}, "")
	got := call(t, mux, "POST", "/api/rag/endpoint/start")
	if s, _ := got.Get("state"); s != "error" {
		t.Fatal(string(jsonv.MustMarshal(got)))
	}
	if d, _ := got.Get("detail"); !strings.Contains(d.(string), "The pw CLI is not available to this deployment (looked for") {
		t.Fatal(d)
	}
}

func TestForwarder(t *testing.T) {
	var seen *http.Request
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(r.Context())
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(201)
		w.Write([]byte("data: " + string(body) + "\n\n"))
		w.(http.Flusher).Flush()
		w.Write([]byte("data: done\n\n"))
	}))
	defer studio.Close()
	port := studio.URL[strings.LastIndex(studio.URL, ":")+1:]
	fwd := httptest.NewServer(Forwarder(port, "inject"))
	defer fwd.Close()

	req, _ := http.NewRequest("POST", fwd.URL+"/v1/chat/completions?x=1", strings.NewReader("hello"))
	req.Host = "public.example"
	start := time.Now()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 201 || string(body) != "data: hello\n\ndata: done\n\n" || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(res.StatusCode, string(body))
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("the answer ends 400 ms after the Studio's, as the script's")
	}
	if seen.Header.Get("Authorization") != "Bearer inject" || seen.Header.Get("X-Studio-Injected-Key") != "1" || seen.Host != "public.example" || seen.URL.RequestURI() != "/v1/chat/completions?x=1" {
		t.Fatal(seen.Header, seen.Host, seen.URL)
	}
	// A caller's own bearer goes through untouched.
	req, _ = http.NewRequest("GET", fwd.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer mine")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if seen.Header.Get("Authorization") != "Bearer mine" || seen.Header.Get("X-Studio-Injected-Key") != "" {
		t.Fatal(seen.Header)
	}
	// The Studio not answering: an empty 502.
	studio.Close()
	res, err = http.Get(fwd.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 502 || len(b) != 0 {
		t.Fatal(res.StatusCode, string(b))
	}
}
