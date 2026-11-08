package access

import (
	"encoding/json"
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

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// node is a stand-in Node server answering the bridge: the viewer, and
// the access changes Go reports.
type node struct {
	mu      sync.Mutex
	viewer  map[string]any
	changed []string
	srv     *httptest.Server
}

func newNode(t *testing.T, viewer map[string]any) *node {
	n := &node{viewer: viewer}
	n.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		defer n.mu.Unlock()
		switch r.URL.Path {
		case "/api/__bridge/viewer":
			json.NewEncoder(w).Encode(n.viewer)
		case "/api/__bridge/access-changed":
			b, _ := io.ReadAll(r.Body)
			n.changed = append(n.changed, string(b))
			if strings.Contains(string(b), `"clearKey":true`) {
				n.viewer["cred"] = nil
				n.viewer["keyStatus"] = map[string]any{"mode": "none"}
			}
			io.WriteString(w, `{"ok":true}`)
		case "/api/__bridge/provider-probes/invalidate":
			n.changed = append(n.changed, "probes")
			io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(n.srv.Close)
	return n
}

type studio struct {
	t      *testing.T
	url    string
	routes *Routes
	p      *platform.Platform
}

func newStudio(t *testing.T, env map[string]string, n *node) *studio {
	t.Helper()
	p := platform.New(platform.Options{IndexBase: t.TempDir(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	if n != nil {
		u, _ := url.Parse(n.srv.URL)
		p.SetNode(platform.NewNodeBridge(app.NewNode(u, nil, "")))
	}
	a := &app.App{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	s := &studio{t: t, routes: Register(mux, a, p), p: p}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

func (s *studio) do(method, path string, body any) (int, map[string]any, string) {
	s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, s.url+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(b, &out)
	return res.StatusCode, out, string(b)
}

// credsDir writes a pw CLI credential file.
func credsDir(t *testing.T) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "credentials"), []byte(`{"identities":{
		"work":{"apikey":"work-api-key","server":"platform-a.example.com","organization":"org-a"},
		"lab":{"apikey":"lab-api-key","server":"platform-b.example.com","organization":"org-b"}},
		"currentIdentity":"work"}`), 0o600)
	return dir
}

func fakeCLI(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	return p
}

func TestAccessSourcesAndChoice(t *testing.T) {
	cli := fakeCLI(t, `[ "$1" = "--version" ] && echo "pw version v7.1.0"`)
	n := newNode(t, map[string]any{"authEnabled": false, "user": nil,
		"cred":      map[string]any{"key": "local", "baseUrl": "http://127.0.0.1:11434/v1", "endpoint": nil},
		"keyStatus": map[string]any{"mode": "stored", "last4": "ocal", "kind": "api-key"}, "sharedLast4": nil})
	s := newStudio(t, map[string]string{"PW_CREDENTIALS_DIR": credsDir(t), "PW_CLI": cli, "PATH": "/usr/bin:/bin"}, n)

	_, body, raw := s.do("GET", "/api/access", nil)
	if !strings.HasPrefix(raw, `{"single":true,"authEnabled":false,"models":{"source":"endpoint","host":"127.0.0.1:11434","context":null,"baseUrl":"http://127.0.0.1:11434/v1","last4":"ocal","kind":"api-key"},"platform":{"source":"cli","host":"platform-a.example.com","context":"work"},"deployment":{"source":"cli","host":"platform-a.example.com","context":"work"},"gatewayHost":"platform-a.example.com","platformOrigin":"https://platform-a.example.com","cli":{"available":true,"version":"v7.1.0","path":"`+cli+`","source":"env","chosenPath":null,"commonDirs":[`) {
		t.Fatal(raw)
	}
	if !strings.HasSuffix(raw, `"chosen":null,"active":"work","used":"work","gatewayFromEnv":false,"envKey":false},"login":null}`) || strings.Contains(raw, "work-api-key") {
		t.Fatal(raw)
	}
	_ = body

	// Choosing a context with clearKey drops the endpoint.
	st, body, raw := s.do("PUT", "/api/access/source", map[string]any{"context": "lab", "clearKey": true})
	if st != 200 || !strings.HasPrefix(raw, `{"ok":true,"chosen":"lab","active":"lab","models":{"source":"cli","host":"platform-b.example.com","context":"lab"}`) {
		t.Fatal(st, raw)
	}
	n.mu.Lock()
	changed := strings.Join(n.changed, " ")
	n.mu.Unlock()
	if changed != `{"clearKey":false} {"clearKey":true} probes` {
		t.Fatal(changed)
	}
	if st, _, _ := s.do("PUT", "/api/access/source", map[string]any{"context": nil}); st != 200 || s.p.ChosenContext() != "" {
		t.Fatal(st)
	}
	if st, body, _ = s.do("PUT", "/api/access/source", map[string]any{"context": "nope"}); st != 400 || body["error"] != "the pw CLI has no context named nope" {
		t.Fatal(st, body)
	}
	// Fastify answers a malformed body with a 500 before the route runs.
	req, _ := http.NewRequest("PUT", s.url+"/api/access/source", strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != 500 {
		t.Fatal(res.Status)
	}
}

func TestAccessOnASignedInDeployment(t *testing.T) {
	n := newNode(t, map[string]any{"authEnabled": true, "user": map[string]any{"id": "u1", "username": "alex", "name": "Alex"},
		"cred": nil, "keyStatus": map[string]any{"mode": "none"}, "sharedLast4": nil})
	s := newStudio(t, map[string]string{"PW_CREDENTIALS_DIR": credsDir(t), "PW_API_KEY": "env-key"}, n)
	_, _, raw := s.do("GET", "/api/access", nil)
	if raw != `{"single":false,"authEnabled":true,"models":{"source":"env","host":"platform-a.example.com","context":null},"platform":{"source":"env","host":"platform-a.example.com","context":null},"deployment":{"source":"env","host":"platform-a.example.com","context":null},"gatewayHost":"platform-a.example.com","platformOrigin":"https://platform-a.example.com","cli":null,"login":null}` {
		t.Fatal(raw)
	}
	for _, c := range [][2]string{{"PUT", "/api/access/cli/path"}, {"PUT", "/api/access/source"}, {"POST", "/api/access/cli/login"}, {"GET", "/api/access/cli/login"}, {"POST", "/api/access/cli/login/cancel"}} {
		var b any
		if c[0] != "GET" {
			b = map[string]any{}
		}
		if st, body, _ := s.do(c[0], c[1], b); st != 403 || !strings.HasPrefix(body["error"].(string), "The pw CLI on this server belongs to the deployment") {
			t.Fatal(c, st, body)
		}
	}
	// Subscriptions never use the deployment's key on a shared deployment.
	if _, _, raw := s.do("GET", "/api/access/subscriptions", nil); raw != `{"available":false,"reason":"Subscriptions connect to your own ACTIVATE account, which needs your platform token or platform API key here first."}` {
		t.Fatal(raw)
	}
	if st, body, _ := s.do("POST", "/api/access/codex", map[string]any{}); st != 409 || !strings.HasPrefix(body["error"].(string), "Add your platform token") {
		t.Fatal(st, body)
	}
}

func TestCLIPathRoute(t *testing.T) {
	cli := fakeCLI(t, `[ "$1" = "--version" ] && echo "pw version v7.1.0"`)
	bin := t.TempDir()
	onPath := filepath.Join(bin, "pw")
	b, _ := os.ReadFile(cli)
	os.WriteFile(onPath, b, 0o755)
	s := newStudio(t, map[string]string{"PW_CREDENTIALS_DIR": credsDir(t), "PATH": bin}, nil)
	_, body, _ := s.do("GET", "/api/access?fresh=1", nil)
	c := body["cli"].(map[string]any)
	if c["path"] != onPath || c["source"] != "path" || c["available"] != true {
		t.Fatal(c)
	}
	// A location chosen here wins over PATH and is kept beside the index.
	st, body, raw := s.do("PUT", "/api/access/cli/path", map[string]any{"path": " " + cli + " "})
	if st != 200 || raw != `{"cli":{"available":true,"version":"v7.1.0","path":"`+cli+`","source":"chosen"}}` {
		t.Fatal(st, raw)
	}
	if kept, _ := os.ReadFile(filepath.Join(s.p.IndexBase(), "pw-cli.json")); !strings.Contains(string(kept), cli) {
		t.Fatal(string(kept))
	}
	if st, _, _ := s.do("PUT", "/api/access/cli/path", map[string]any{"path": "bin/pw"}); st != 400 {
		t.Fatal(st)
	}
	if st, _, _ := s.do("PUT", "/api/access/cli/path", map[string]any{"path": filepath.Join(bin, "nothing")}); st != 400 {
		t.Fatal(st)
	}
	other := fakeCLI(t, "echo hello")
	if st, body, _ = s.do("PUT", "/api/access/cli/path", map[string]any{"path": other}); st != 400 || body["error"] != other+" did not answer --version as the pw CLI does, so it was not kept." {
		t.Fatal(st, body)
	}
	if s.p.ChosenCLI() != "" {
		t.Fatal("a refused program is not kept, and the earlier choice is forgotten as in Node")
	}
	_, _, raw = s.do("PUT", "/api/access/cli/path", map[string]any{"path": nil})
	if raw != `{"cli":{"available":true,"version":"v7.1.0","path":"`+onPath+`","source":"path"}}` {
		t.Fatal(raw)
	}
}

func TestLoginRoutes(t *testing.T) {
	cli := fakeCLI(t, `echo "open https://$4/device and enter"; echo "  ABCD-EFGH"; exec sleep 10`)
	s := newStudio(t, map[string]string{"PW_CREDENTIALS_DIR": credsDir(t), "PW_CLI": cli}, nil)
	st, body, _ := s.do("POST", "/api/access/cli/login", map[string]any{"host": "https://platform-c.example.com/x"})
	l := body["login"].(map[string]any)
	if st != 200 || l["state"] != "waiting" || l["code"] != "ABCD-EFGH" || l["host"] != "platform-c.example.com" {
		t.Fatal(st, body)
	}
	if _, body, _ := s.do("GET", "/api/access/cli/login", nil); body["login"].(map[string]any)["state"] != "waiting" {
		t.Fatal(body)
	}
	if _, body, _ := s.do("POST", "/api/access/cli/login/cancel", map[string]any{}); body["login"].(map[string]any)["state"] != "canceled" {
		t.Fatal(body)
	}
	if st, _, _ := s.do("POST", "/api/access/cli/login", map[string]any{"host": "not a host!"}); st != 400 {
		t.Fatal(st)
	}
}

/* ---- subscriptions ---- */

type fakePlatform struct {
	mu        sync.Mutex
	calls     []string
	bodies    map[string]string
	polls     int
	copilot   bool
	whoamiTxt string
}

func (f *fakePlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
	f.bodies[r.Method+" "+r.URL.Path] = string(b)
	j := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/api/auth/whoami":
		io.WriteString(w, f.whoamiTxt)
	case p == "/api/auth/whoami/organization":
		j("org-a")
	case p == "/api/organizations/org-a/ai-integrations":
		list := []any{map[string]any{"id": "codex-subscription", "kind": "codex-subscription", "lifecycle": "active"}}
		if f.copilot {
			list = append(list, map[string]any{"id": "copilot-entry", "kind": "github-copilot"})
		}
		j(map[string]any{"integrations": list})
	case p == "/api/organizations/org-a/users/alex/ai-providers" && r.Method == "GET":
		j([]any{map[string]any{"name": "chatgpt", "displayName": "ChatGPT", "catalogEntryId": "codex-subscription", "status": "provisioned", "accountEmail": "alex@example.com"}})
	case p == "/api/organizations/org-a/users/alex/ai-providers":
		j(map[string]any{"name": "copilot", "displayName": "GitHub Copilot", "accountLogin": "alex-gh"})
	case p == "/api/integrations/github/oauth/status":
		j(map[string]any{"appConfigured": true, "tokensAllowed": false, "username": "alex-gh"})
	case p == "/api/organizations/org-a/users/alex/codex-device-authorizations":
		j(map[string]any{"id": "auth-1", "providerName": "chatgpt", "verificationUrl": "https://auth.example.com/codex/device", "userCode": "WXYZ-1234", "expiresAt": "2026-10-22T12:00:00Z", "pollIntervalSeconds": 1})
	case p == "/api/organizations/org-a/users/alex/codex-device-authorizations/auth-1":
		f.polls++
		if f.polls < 2 {
			j(map[string]any{"status": "pending"})
		} else {
			j(map[string]any{"status": "connected", "provider": map[string]any{"name": "chatgpt", "displayName": "ChatGPT", "accountEmail": "alex@example.com"}})
		}
	default:
		w.WriteHeader(404)
		j(map[string]any{"message": "not found"})
	}
}

func TestSubscriptions(t *testing.T) {
	f := &fakePlatform{bodies: map[string]string{}, copilot: true, whoamiTxt: "user:alex"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	n := newNode(t, map[string]any{"authEnabled": false, "user": nil, "cred": nil, "keyStatus": map[string]any{"mode": "none"}, "sharedLast4": nil})
	s := newStudio(t, map[string]string{"PW_GATEWAY_URL": srv.URL + "/api/openai/v1", "PW_API_KEY": "alex-platform-key", "PW_CREDENTIALS_DIR": t.TempDir()}, n)

	st, _, raw := s.do("GET", "/api/access/subscriptions", nil)
	if st != 200 || raw != `{"available":true,"user":"alex","org":"org-a","codex":{"offered":true,"providers":[{"name":"chatgpt","displayName":"ChatGPT","status":"provisioned","account":"alex@example.com"}]},"copilot":{"offered":true,"providers":[],"github":{"linked":true,"username":"alex-gh","appConfigured":true,"tokensAllowed":false}}}` {
		t.Fatal(st, raw)
	}
	for _, c := range f.calls {
		if !strings.HasSuffix(c, " Bearer alex-platform-key") {
			t.Fatal(c)
		}
	}
	st, _, raw = s.do("POST", "/api/access/codex", map[string]any{"name": "ChatGPT Plus!", "reconnect": true})
	if st != 200 || raw != `{"id":"auth-1","url":"https://auth.example.com/codex/device","code":"WXYZ-1234","expiresAt":"2026-10-22T12:00:00Z","intervalSec":2,"provider":"chatgpt"}` {
		t.Fatal(st, raw)
	}
	if b := f.bodies["POST /api/organizations/org-a/users/alex/codex-device-authorizations"]; b != `{"name":"chatgptplus","reconnect":true}` {
		t.Fatal(b)
	}
	if _, body, _ := s.do("POST", "/api/access/codex/auth-1", nil); body["status"] != "pending" {
		t.Fatal(body)
	}
	if _, _, raw := s.do("POST", "/api/access/codex/auth-1", nil); raw != `{"status":"connected","provider":{"name":"chatgpt","displayName":"ChatGPT","account":"alex@example.com"}}` {
		t.Fatal(raw)
	}
	st, _, raw = s.do("POST", "/api/access/copilot", map[string]any{})
	if st != 200 || raw != `{"provider":{"name":"copilot","displayName":"GitHub Copilot","account":"alex-gh"}}` {
		t.Fatal(st, raw)
	}
	if b := f.bodies["POST /api/organizations/org-a/users/alex/ai-providers"]; b != `{"name":"copilot","catalogEntryId":"copilot-entry","displayName":"GitHub Copilot"}` {
		t.Fatal(b)
	}
	n.mu.Lock()
	if strings.Count(strings.Join(n.changed, " "), "probes") != 2 {
		t.Fatal(n.changed, "codex connected and copilot each invalidate the provider probes")
	}
	n.mu.Unlock()
	f.mu.Lock()
	f.copilot = false
	f.mu.Unlock()
	if st, body, _ := s.do("POST", "/api/access/copilot", map[string]any{}); st != 409 || body["error"] != "This platform does not offer GitHub Copilot." {
		t.Fatal(st, body)
	}
}

func TestPlatformRefusalsAndReach(t *testing.T) {
	f := &fakePlatform{bodies: map[string]string{}, whoamiTxt: "gateway-key:x"}
	srv := httptest.NewServer(f)
	s := newStudio(t, map[string]string{"PW_GATEWAY_URL": srv.URL + "/api/openai/v1", "PW_API_KEY": "k", "PW_CREDENTIALS_DIR": t.TempDir()}, nil)
	if st, body, _ := s.do("GET", "/api/access/subscriptions", nil); st != 409 || body["error"] != "This credential does not belong to a platform user." {
		t.Fatal(st, body)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"bad token"}`)
	})
	if st, body, _ := s.do("GET", "/api/access/subscriptions", nil); st != 401 || body["error"] != "The platform refused this credential (401): bad token. A platform token or platform API key is needed; an AI gateway key is not enough." {
		t.Fatal(st, body)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); io.WriteString(w, "oops") })
	if st, body, _ := s.do("POST", "/api/access/codex", map[string]any{}); st != 502 || body["error"] != "The platform answered 500: oops" {
		t.Fatal(st, body)
	}
	// A platform out of reach: nothing listens on its port.
	host := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	if l, err := net.Listen("tcp", host); err == nil {
		l.Close()
	}
	st, body, _ := s.do("POST", "/api/access/codex", map[string]any{})
	if st != 503 || body["error"] != "Nothing answers at "+host+" (ECONNREFUSED). Start the model server on this computer, or check its address." {
		t.Fatal(st, body)
	}
	_ = time.Second
}
