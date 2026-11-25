package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/modelkeys"
)

// keyedGateway answers as labelGateway does and keeps the key of each call.
type keyedGateway struct {
	labelGateway
	mu   sync.Mutex
	keys []string
}

func (g *keyedGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.keys = append(g.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+" "+r.URL.Path[strings.LastIndex(r.URL.Path, "/"):])
	g.mu.Unlock()
	g.labelGateway.ServeHTTP(w, r)
}

func (g *keyedGateway) took() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.keys
	g.keys = nil
	return out
}

// TestEveryCallForAPersonIsOnTheirOwnKey: where each person uses their own
// credential (REQUIRE_PERSONAL_KEY), Suggest labels, Settings' model list
// and pw code's model check never use the Studio's: a person without one is
// told what the Assistant says, and nothing reaches the gateway; one with
// one calls with it. Without the requirement the Studio's endpoint serves
// those without one, as before.
func TestEveryCallForAPersonIsOnTheirOwnKey(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(map[bool]string{true: "required", false: "not required"}[required], func(t *testing.T) {
			env := map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "OPENAI_BASE_URL": "https://models.example.org/v1", "PW_API_KEY": "studio-key"}
			if required {
				env["REQUIRE_PERSONAL_KEY"] = "1"
			}
			m, srv, _, _ := newTestModule(t, env)
			gw := &keyedGateway{labelGateway: labelGateway{label: "x"}}
			gs := httptest.NewServer(gw)
			t.Cleanup(gs.Close)
			m.Tools = fakeTools{gs.URL + "/v1"}
			m.setAccess(&Access{GatewayBase: gs.URL + "/v1", DeploymentKey: "studio-key"})
			do := asWho(t, srv)
			if err := m.Keys.Set("ada", "ada-key", nil, nil); err != nil {
				t.Fatal(err)
			}

			// bob has no credential of his own
			code, out := do("bob", "POST", "/api/kb/label-suggestions", `{"dir":""}`)
			_, models := do("bob", "GET", "/api/chat/models?config=1", "")
			r := auth.WithUser(httptest.NewRequest("GET", "/", nil), &auth.User{ID: "bob", Username: "bob"})
			_, _, checked := m.enabledModels(r)
			took := gw.took()
			if required {
				if code != http.StatusForbidden || !strings.Contains(out, modelkeys.SignInFirst) {
					t.Errorf("labels: %d %s", code, out)
				}
				if !strings.Contains(models, modelkeys.SignInFirst) || strings.Contains(models, "test-model") {
					t.Errorf("models: %s", models)
				}
				if checked || len(took) != 0 {
					t.Errorf("pw code's check %v; the gateway was called %v", checked, took)
				}
			} else {
				if code != http.StatusOK || !checked {
					t.Errorf("labels: %d %s; pw code's check %v", code, out, checked)
				}
				for _, k := range took {
					if !strings.HasPrefix(k, "studio-key ") {
						t.Errorf("bob's calls: %v", took)
					}
				}
			}

			// ada has
			code, out = do("ada", "POST", "/api/kb/label-suggestions", `{"dir":""}`)
			_, models = do("ada", "GET", "/api/chat/models?config=1", "")
			if code != http.StatusOK || !strings.Contains(out, `"model":"test-model"`) || !strings.Contains(models, "test-model") {
				t.Errorf("ada: %d %s %s", code, out, models)
			}
			took = gw.took()
			if len(took) < 2 {
				t.Errorf("ada's calls: %v", took)
			}
			for _, k := range took {
				if !strings.HasPrefix(k, "ada-key ") {
					t.Errorf("ada's calls: %v", took)
				}
			}
		})
	}
}
