package chat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
	"github.com/parallelworks/activate-studio/server-go/internal/ragendpoint"
)

// TestV1RunsOnlyOnTheCallersOwnKey: /v1 knows no signed-in person, so where
// each person uses their own credential (REQUIRE_PERSONAL_KEY) a call runs
// on the key it brings or not at all. One with no key, with the Studio's
// own, or with the one the registration forwarder adds (which the
// platform's gateway and anyone on the host reach without a key) is told
// what the Assistant says, and nothing reaches a model. Without the
// requirement, the forwarder's key serves keyless callers, as before.
func TestV1RunsOnlyOnTheCallersOwnKey(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(map[bool]string{true: "required", false: "not required"}[required], func(t *testing.T) {
			env := map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "PW_API_KEY": "studio-key", "RAG_DEFAULT_MODEL": "test-model"}
			if required {
				env["REQUIRE_PERSONAL_KEY"] = "1"
			}
			m, srv, _, _ := newTestModule(t, env)
			gw := &keyedGateway{labelGateway: labelGateway{label: "x"}}
			gs := httptest.NewServer(gw)
			t.Cleanup(gs.Close)
			m.Tools = fakeTools{gs.URL + "/v1"}
			m.setAccess(&Access{GatewayBase: gs.URL + "/v1", DeploymentKey: "studio-key"})
			// The registration's forwarder in front of this Studio, given the
			// Studio's key to add as it was before.
			fwd := httptest.NewServer(ragendpoint.Forwarder(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"), "studio-key"))
			t.Cleanup(fwd.Close)

			do := func(base, method, path, bearer string, h map[string]string) (int, string) {
				var body io.Reader
				if method == "POST" {
					body = strings.NewReader(`{"model":"studio-rag/test-model","messages":[{"role":"user","content":"hello"}],"stream":false}`)
				}
				req, _ := http.NewRequest(method, base+path, body)
				req.Header.Set("Content-Type", "application/json")
				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}
				for k, v := range h {
					req.Header.Set(k, v)
				}
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				b, _ := io.ReadAll(res.Body)
				return res.StatusCode, string(b)
			}
			for _, c := range []struct {
				name, base, bearer string
				h                  map[string]string
			}{
				{"no key", srv.URL, "", nil},
				{"the Studio's key", srv.URL, "studio-key", nil},
				{"a key marked as the forwarder's", srv.URL, "someone-key", map[string]string{"X-Studio-Injected-Key": "1"}},
				{"no key, through the forwarder", fwd.URL, "", nil},
			} {
				for _, p := range []struct{ method, path string }{{"GET", "/v1/models"}, {"POST", "/v1/chat/completions"}} {
					code, out := do(c.base, p.method, p.path, c.bearer, c.h)
					took := gw.took()
					switch {
					case required && (code != 401 || !strings.Contains(out, modelkeys.SignInFirst) || len(took) != 0):
						t.Errorf("%s, %s: %d %s; the gateway took %v", c.name, p.path, code, out, took)
					case !required && c.name == "no key" && (code != 401 || len(took) != 0):
						t.Errorf("%s, %s: %d %s; the gateway took %v", c.name, p.path, code, out, took)
					case !required && strings.Contains(c.name, "Studio's key") || !required && strings.Contains(c.name, "forwarder") && c.bearer == "":
						for _, k := range took {
							if !strings.HasPrefix(k, "studio-key ") {
								t.Errorf("%s, %s: %v", c.name, p.path, took)
							}
						}
						if code != 200 || p.method == "POST" && len(took) == 0 {
							t.Errorf("%s, %s: %d %s", c.name, p.path, code, out)
						}
					}
				}
			}
			// A caller's own key, at the Studio or through the forwarder.
			for _, base := range []string{srv.URL, fwd.URL} {
				code, out := do(base, "POST", "/v1/chat/completions", "bobs-key", nil)
				took := gw.took()
				if code != 200 || len(took) == 0 {
					t.Errorf("bob at %s: %d %s", base, code, out)
				}
				for _, k := range took {
					if !strings.HasPrefix(k, "bobs-key ") {
						t.Errorf("bob at %s: %v", base, took)
					}
				}
			}
		})
	}
}
