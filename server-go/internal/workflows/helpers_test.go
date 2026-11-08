package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/keycookie"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// hosts routes every outbound request of the package by host to a
// handler, as the Node tests' globalThis.fetch stubs do; an unknown host
// is a network failure.
type hosts struct {
	mu   sync.Mutex
	h    map[string]http.HandlerFunc
	seen []seenReq
}

type seenReq struct{ Method, URL, Auth string }

func (h *hosts) RoundTrip(req *http.Request) (*http.Response, error) {
	h.mu.Lock()
	f := h.h[req.URL.Host]
	if req.Method != http.MethodHead {
		h.seen = append(h.seen, seenReq{req.Method, req.URL.String(), req.Header.Get("Authorization")})
	}
	h.mu.Unlock()
	if f == nil {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: req.URL.Hostname(), IsNotFound: true}}
	}
	rec := httptest.NewRecorder()
	if req.Method == http.MethodHead {
		rec.WriteHeader(200)
	} else {
		f(rec, req)
	}
	res := rec.Result()
	res.Request = req
	return res, nil
}

func (h *hosts) set(host string, f http.HandlerFunc) {
	h.mu.Lock()
	h.h[host] = f
	h.mu.Unlock()
}

func (h *hosts) reset() []seenReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.seen
	h.seen = nil
	return s
}

// fakeNet installs a hosts transport for the test.
func fakeNet(t *testing.T) *hosts {
	t.Helper()
	h := &hosts{h: map[string]http.HandlerFunc{}}
	saved := platform.HTTPClient
	platform.HTTPClient = &http.Client{Transport: h}
	t.Cleanup(func() { platform.HTTPClient = saved })
	ResetGitHubCaches()
	t.Cleanup(ResetGitHubCaches)
	return h
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonv.MustMarshal(v))
}

// mustJSON decodes a JSON literal written in a test.
func mustJSON(t *testing.T, s string) any {
	t.Helper()
	v, err := jsonv.DecodeString(s)
	if err != nil {
		t.Fatal(s, err)
	}
	return v
}

// cliCall records one CLI run: its arguments and the key it ran with.
type cliCall struct {
	Args []string
	Key  string
}

type fixture struct {
	t      *testing.T
	p      *platform.Platform
	r      *Routes
	mux    *http.ServeMux
	mu     sync.Mutex
	calls  []cliCall
	user   *auth.User
	answer func(args []string, key string) (string, error)
}

// newFixture is the routes over a platform at https://platform.test with
// PW_API_KEY as the deployment key and curated as the collection.
func newFixture(t *testing.T, env map[string]string, curated ...string) *fixture {
	t.Helper()
	full := map[string]string{"PW_GATEWAY_URL": "https://platform.test/api/openai/v1"}
	for k, v := range env {
		full[k] = v
	}
	ix := t.TempDir()
	kb := t.TempDir()
	p := platform.New(platform.Options{IndexBase: ix, KBRoot: kb, Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := full[k]; return v, ok }})
	f := &fixture{t: t, p: p}
	r := NewRoutes(nil, p)
	r.Curated = func() []string { return curated }
	r.CLI = func(ctx context.Context, args []string, key string, _ time.Duration) (string, error) {
		f.mu.Lock()
		f.calls = append(f.calls, cliCall{append([]string{}, args...), key})
		ans := f.answer
		f.mu.Unlock()
		if ans == nil {
			return "", errors.New("unexpected " + strings.Join(args, " "))
		}
		return ans(args, key)
	}
	r.Runs.CLI = func(ctx context.Context, args []string) (string, error) { return "", errors.New("no watcher in tests") }
	r.Runs.Note = func(string, string) {}
	r.Runs.Log = func(string) {}
	t.Cleanup(r.Runs.Stop)
	f.r = r
	f.mux = http.NewServeMux()
	r.register(f.mux)
	f.mux.HandleFunc("GET /api/kb/dag", platform.Handler(p.Log(), r.kbDag))
	f.mux.HandleFunc("POST /internal/platform/runs", platform.Handler(p.Log(), r.recordFromNode))
	return f
}

// ownKey stores key as the single owner's own platform key (sign-in off).
func (f *fixture) ownKey(key string) {
	f.t.Helper()
	blob, err := keycookie.New(f.p.IndexBase()).Encrypt([]byte(key))
	if err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"single": map[string]any{"blob": blob, "last4": key[len(key)-4:]}})
	os.WriteFile(filepath.Join(f.p.IndexBase(), "user-credentials.json"), b, 0o600)
}

type answer struct {
	status int
	header http.Header
	body   []byte
	json   any
}

func (f *fixture) do(method, target string, body any) answer {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(jsonv.MustMarshal(body)))
	}
	req := httptest.NewRequest(method, target, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if f.user != nil {
		req = auth.WithUser(req, f.user)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	a := answer{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		a.json, _ = jsonv.Decode(a.body)
	}
	return a
}

func (f *fixture) cliCalls() []cliCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cliCall{}, f.calls...)
}

func js(v any) string { return string(jsonv.MustMarshal(v)) }

func eq(t *testing.T, what string, got any, want string) {
	t.Helper()
	if g := js(got); g != want {
		t.Errorf("%s:\n got %s\nwant %s", what, g, want)
	}
}
