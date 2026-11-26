package chat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/people"
)

// aPlatform answers GET /api/auth/whoami as the platform does, with the
// account whose key the bearer is ("user:<name>", or another kind of
// account), 401 for a key it does not take, and 500 while down. It is the
// platform the Studio asks; asked counts its answers.
func aPlatform(t *testing.T, accounts map[string]string, down *atomic.Bool) (asked *atomic.Int32) {
	asked = &atomic.Int32{}
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/whoami" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		if down != nil && down.Load() {
			w.WriteHeader(500)
			return
		}
		who, ok := accounts[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(who)
	}))
	t.Cleanup(ps.Close)
	was := platformOrigin
	platformOrigin = func(*Module) string { return ps.URL }
	t.Cleanup(func() { platformOrigin = was })
	owners.mu.Lock()
	owners.at = map[string]keyOwner{}
	owners.mu.Unlock()
	return asked
}

// TestStudioRagAnswersOnlyPeopleOfThisStudio: where people sign in, /v1
// (studio-rag, any other model id, the plain pass-through and the model
// list) answers only a caller whose own key the platform says is that of
// someone who has signed in here or of an administrator. Every other caller
// is refused with 403 before any model call, and the refusal is logged
// without the key. The platform's answer is kept a while. Without sign-in,
// every caller is answered as before and the platform is not asked.
func TestStudioRagAnswersOnlyPeopleOfThisStudio(t *testing.T) {
	// The Studio's own key is its owner's (PW_USER), an administrator.
	accounts := map[string]string{"studio-key": "user:studio", "eves-key": "user:eve", "cys-key": "user:cy", "zeds-key": "user:zed", "svc-key": "service:indexer", "Eves-Other": "eve"}
	for _, signIn := range []bool{true, false} {
		t.Run(map[bool]string{true: "sign-in", false: "a Studio of one"}[signIn], func(t *testing.T) {
			env := map[string]string{"PW_API_KEY": "studio-key", "PW_USER": "studio", "RAG_DEFAULT_MODEL": "test-model", "STUDIO_ADMINS": "cy"}
			if signIn {
				env["AUTH_JWKS_URL"] = "http://127.0.0.1:9/jwks"
			}
			m, srv, _, _ := newTestModule(t, env)
			gw := &keyedGateway{labelGateway: labelGateway{label: "x"}}
			gs := httptest.NewServer(gw)
			t.Cleanup(gs.Close)
			m.Tools = fakeTools{gs.URL + "/v1"}
			m.setAccess(&Access{GatewayBase: gs.URL + "/v1", DeploymentKey: "studio-key"})
			down := &atomic.Bool{}
			asked := aPlatform(t, accounts, down)
			people.Of(m.A).Seen(&auth.User{ID: "user:eve", Username: "eve"})

			send := func(method, path, bearer, body string, h map[string]string) (int, string) {
				req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
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
			ask := func(bearer, model string, h map[string]string) (int, string, []string) {
				code, out := send("POST", "/v1/chat/completions", bearer, `{"model":"`+model+`","messages":[{"role":"user","content":"hello"}],"stream":false}`, h)
				return code, out, gw.took()
			}
			for _, c := range []struct {
				name, bearer string
				h            map[string]string
				person       bool
			}{
				{"no key", "", nil, false},
				{"the Studio's key", "studio-key", nil, false},
				{"a call the forwarder marked", "eves-key", map[string]string{"X-Studio-Injected-Key": "1"}, false},
				{"a key the platform does not take", "made-up-key", nil, false},
				{"a service's key", "svc-key", nil, false},
				{"the key of someone who has not signed in here", "zeds-key", nil, false},
				{"the key of someone who has signed in here", "eves-key", nil, true},
				{"another key of theirs, named without user:", "Eves-Other", nil, true},
				{"an administrator's key", "cys-key", nil, true},
			} {
				first := asked.Load()
				for _, model := range []string{"studio-rag/test-model", "studio-rag", "test-model"} {
					code, out, took := ask(c.bearer, model, c.h)
					answered := code == 200 && len(took) > 0
					switch {
					case !signIn && c.bearer == "":
						if code != 401 {
							t.Errorf("%s, %s: %d %s", c.name, model, code, out)
						}
					case !signIn || c.person:
						if !answered || strings.Contains(out, RagNeedsPerson) {
							t.Errorf("%s, %s: %d %s, took %v", c.name, model, code, out, took)
						}
						for _, k := range took {
							if !strings.HasPrefix(k, c.bearer+" ") {
								t.Errorf("%s, %s: on %v", c.name, model, took)
							}
						}
					default:
						if code != 403 || !strings.Contains(out, RagNeedsPerson) || len(took) != 0 {
							t.Errorf("%s, %s: %d %s, took %v", c.name, model, code, out, took)
						}
					}
				}
				// No key, the Studio's own and a marked call are refused
				// without asking the platform.
				if early := c.bearer == "" || c.bearer == "studio-key" || c.h != nil; early && asked.Load() != first {
					t.Errorf("%s: the platform was asked", c.name)
				}
				code, out := send("GET", "/v1/models", c.bearer, "", c.h)
				if refused := code == 403 && strings.Contains(out, RagNeedsPerson); refused != (signIn && !c.person) {
					t.Errorf("%s, /v1/models: %d %s", c.name, code, out)
				}
				gw.took()
			}
			m.rag.mu.Lock()
			for _, c := range m.rag.calls {
				if c.Detail != nil && (strings.Contains(*c.Detail, "zeds-key") || strings.Contains(*c.Detail, "svc-key")) {
					t.Errorf("a key is in the call log: %s", *c.Detail)
				}
			}
			refusals := 0
			for _, c := range m.rag.calls {
				if c.Status == "error" && c.Detail != nil && strings.Contains(*c.Detail, "only people of this Studio") {
					refusals++
				}
			}
			m.rag.mu.Unlock()
			if signIn != (refusals > 0) {
				t.Errorf("refusals logged: %d", refusals)
			}
			if !signIn {
				if n := asked.Load(); n != 0 {
					t.Errorf("a Studio of one asked the platform %d times", n)
				}
				return
			}
			// Asked once for each key, then kept.
			before := asked.Load()
			ask("eves-key", "test-model", nil)
			ask("zeds-key", "test-model", nil)
			if n := asked.Load(); n != before {
				t.Errorf("asked again: %d then %d", before, n)
			}
			// Once zed signs in here, their key is a person's.
			people.Of(m.A).Seen(&auth.User{ID: "user:zed", Username: "zed"})
			if code, out, took := ask("zeds-key", "test-model", nil); code != 200 || len(took) == 0 {
				t.Errorf("zed, signed in: %d %s", code, out)
			}
			// A platform that does not answer answers no one.
			down.Store(true)
			owners.mu.Lock()
			owners.at = map[string]keyOwner{}
			owners.mu.Unlock()
			if code, out, took := ask("eves-key", "test-model", nil); code != 403 || !strings.Contains(out, RagNeedsPerson) || len(took) != 0 {
				t.Errorf("platform down: %d %s %v", code, out, took)
			}
			// No platform to ask: no one is answered.
			platformOrigin = func(*Module) string { return "" }
			if code, out, took := ask("eves-key", "test-model", nil); code != 403 || !strings.Contains(out, RagNoPlatform) || len(took) != 0 {
				t.Errorf("no platform: %d %s %v", code, out, took)
			}
		})
	}
}

// whoami takes a person's account from the platform's answer, "user:<name>"
// or the bare name, and nothing else: another kind of account, a refused
// key, a platform that does not answer.
func TestWhoamiIsAPersonsAccount(t *testing.T) {
	accounts := map[string]string{"a": "user:ada", "b": "bob", "c": "service:indexer", "d": "org:lab", "e": "", "f": "user:"}
	down := &atomic.Bool{}
	aPlatform(t, accounts, down)
	origin := platformOrigin(nil)
	for key, want := range map[string]string{"a": "ada", "b": "bob", "c": "", "d": "", "e": "", "f": "", "z": ""} {
		got, ok, err := whoami(t.Context(), origin, key)
		if err != nil || got != want || ok != (want != "") {
			t.Errorf("%s: %q %v %v", key, got, ok, err)
		}
	}
	down.Store(true)
	if _, ok, err := whoami(t.Context(), origin, "a"); ok || err == nil {
		t.Errorf("down: %v %v", ok, err)
	}
}
