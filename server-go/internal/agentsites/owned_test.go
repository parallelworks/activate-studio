package agentsites

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// With sign-in on, this host is not a site even with AGENT_LOCAL_SITE=1:
// its daemon runs as the Studio's account, and no person borrows the
// deployment's credential. Nobody lists, reads, steers, sets the goal of,
// answers, stops or starts a session here, and nothing reaches the daemon.
func TestWithSignInOnThisHostIsNoSite(t *testing.T) {
	var mu sync.Mutex
	var touched []string
	sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		mu.Lock()
		touched = append(touched, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/v1/status":
			io.WriteString(w, `{"protocolVersion":2,"hostname":"here"}`)
		case r.URL.Path == "/v1/sessions" && r.Method == "GET":
			io.WriteString(w, `[{"id":"20261122-100000-adaada","status":"idle"},{"id":"20261122-100001-bobbob","status":"idle"}]`)
		case r.URL.Path == "/v1/sessions" && r.Method == "POST":
			io.WriteString(w, `{"id":"20261122-100002-newnew"}`)
		case strings.HasSuffix(r.URL.Path, "/state"):
			io.WriteString(w, `{}`)
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			io.WriteString(w, `{"id":"x","status":"idle","history":[]}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	env := map[string]string{"AGENT_LOCAL_SITE": "1", "PW_CODE_SOCKET": sock, "PW_GATEWAY_URL": "https://platform.example/api/openai/v1"}
	ix := t.TempDir()
	p := platform.New(platform.Options{IndexBase: ix, Home: t.TempDir(), AuthEnabled: func() bool { return true },
		LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
	s := &Sites{P: p, Daemon: pwcode.New(p), Owned: func(r *http.Request) map[string]bool {
		if u := auth.UserOf(r); u != nil && u.ID == "ada" {
			return map[string]bool{"20261122-100000-adaada": true}
		}
		return map[string]bool{}
	}}
	a := app.New(config.Config{IndexBase: ix}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	mux := http.NewServeMux()
	s.Routes(mux, a)
	do := func(u *auth.User, method, path, body string) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = auth.WithUser(req, u)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	ada, bob := &auth.User{ID: "ada"}, &auth.User{ID: "bob"}
	const hers = "/api/agent-sites/local/sessions/20261122-100000-adaada"

	for _, u := range []*auth.User{ada, bob} {
		for _, x := range []struct{ method, path, body string }{
			{"GET", "/api/agent-sites/local/sessions", ""},
			{"GET", hers, ""},
			{"POST", hers + "/goal", `{"action":"clear"}`},
			{"POST", hers + "/steer", `{"text":"read her notes"}`},
			{"POST", hers + "/approvals/ap1", `{"allowed":true}`},
			{"POST", hers + "/interrupt", ""},
			{"POST", "/api/agent-sites/local/sessions", `{"objective":"x"}`},
		} {
			if c, _ := do(u, x.method, x.path, x.body); c < 400 {
				t.Errorf("%s %s %s: %d", u.ID, x.method, x.path, c)
			}
		}
	}
	if c, b := do(ada, "GET", "/api/agent-sites", ""); c != 200 || !strings.Contains(b, `"localOffered":false`) {
		t.Fatalf("sites: %d %s", c, b)
	}
	mu.Lock()
	n := len(touched)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("requests reached the daemon: %v", touched)
	}
}
