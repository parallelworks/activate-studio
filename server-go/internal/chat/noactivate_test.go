package chat

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The deployment's ACTIVATE credential (PW_API_KEY "k" in newTestModule)
// is the administrator's on a Studio several people use: models, platform
// tools and the model list never use it for a person, and nothing reaches
// the gateway without a credential of the person's own.
func TestSharedStudioModelsAndToolsNeverUseTheDeploymentCredential(t *testing.T) {
	m, srv, gw, listings := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "PW_PLATFORM_HOST": "platform.example.com"})
	if !m.pf.PlatformGateway() || m.GatewayKey() != "k" {
		t.Fatal("the deployment has an ACTIVATE credential and gateway")
	}
	if k := m.deploymentModelsKey(); k != "" {
		t.Fatalf("model calls fall back to %q", k)
	}
	if k := m.platformKey(ToolCtx{}); k != "" {
		t.Fatalf("platform tools fall back to %q", k)
	}
	mine := "mine"
	if k := m.platformKey(ToolCtx{UserKey: &mine}); k != "mine" {
		t.Fatal(k)
	}
	sse := post(t, srv.URL, map[string]any{"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	if !strings.Contains(sse, "event: error") {
		t.Fatalf("%q", sse)
	}
	get(t, srv.URL+"/api/chat/models")
	gw.mu.Lock()
	n := len(gw.requests)
	gw.mu.Unlock()
	if n != 0 || atomic.LoadInt32(listings) != 0 {
		t.Fatalf("the gateway was called %d times and listed %d times without the person's credential", n, atomic.LoadInt32(listings))
	}
}

// An OpenAI-compatible endpoint the administrator set (a gateway that is
// not an ACTIVATE platform's) may serve everyone's models.
func TestSharedStudioMayUseTheAdministratorsEndpointForModels(t *testing.T) {
	m, _, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks", "OPENAI_BASE_URL": "https://models.example.org/v1"})
	if m.pf.PlatformGateway() {
		t.Fatal("no platform here")
	}
	if m.deploymentModelsKey() != "k" {
		t.Fatal("the administrator's endpoint")
	}
	if m.platformKey(ToolCtx{}) != "" {
		t.Fatal("never for the platform")
	}
}

// After Stop using the pw CLI, a Studio of one has no ACTIVATE credential:
// no model call, model listing or platform tool uses one.
func TestStoppedStudioOfOneMakesNoActivateCall(t *testing.T) {
	m, srv, gw, listings := newTestModule(t, map[string]string{"PW_PLATFORM_HOST": "platform.example.com"})
	if m.deploymentModelsKey() != "k" {
		t.Fatal("before: the owner's")
	}
	if err := m.pf.Stop(true); err != nil {
		t.Fatal(err)
	}
	if m.GatewayKey() != "" || m.deploymentModelsKey() != "" || m.platformKey(ToolCtx{}) != "" {
		t.Fatal("a credential is still in use")
	}
	sse := post(t, srv.URL, map[string]any{"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	if !strings.Contains(sse, "No model credential.") {
		t.Fatalf("%q", sse)
	}
	if out := get(t, srv.URL+"/api/chat/models"); !strings.HasPrefix(out["error"].(string), "No model credential.") {
		t.Fatal(out)
	}
	gw.mu.Lock()
	n := len(gw.requests)
	gw.mu.Unlock()
	if n != 0 || atomic.LoadInt32(listings) != 0 {
		t.Fatalf("the gateway was called %d times and listed %d times", n, atomic.LoadInt32(listings))
	}
}

// Where everyone uses an account of their own (REQUIRE_PERSONAL_KEY), each
// answer without one says the same: sign in with ACTIVATE first. It marks
// itself as that, so the app says it in one place. Without the requirement,
// and where personal keys are off, the text is as it was.
func TestStrictStudioAsksForTheSignInFirst(t *testing.T) {
	const first = "Sign in with ACTIVATE first, under Settings, Accounts."
	for name, strict := range map[string]map[string]string{
		"strict":                            {"AUTH_REQUIRED": "1", "REQUIRE_PERSONAL_KEY": "1", "PW_PLATFORM_HOST": "platform.example.com"},
		"with the administrator's endpoint": {"REQUIRE_PERSONAL_KEY": "1", "OPENAI_BASE_URL": "https://models.example.org/v1"},
	} {
		t.Run(name, func(t *testing.T) {
			strict["AUTH_JWKS_URL"] = "http://127.0.0.1:9/jwks"
			_, srv, _, _ := newTestModule(t, strict)
			out := get(t, srv.URL+"/api/chat/models")
			if s, _ := out["error"].(string); !strings.HasPrefix(s, first) || out["kind"] != "credential" {
				t.Fatal(out)
			}
			if h := get(t, srv.URL+"/api/ai/health"); h["status"] != "key-required" || !strings.HasPrefix(h["message"].(string), first) {
				t.Fatal(h)
			}
			if sse := post(t, srv.URL, map[string]any{"model": "test-model", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}); !strings.Contains(sse, first) {
				t.Fatalf("%q", sse)
			}
		})
	}
	for name, env := range map[string]map[string]string{
		"not required":      {},
		"personal keys off": {"REQUIRE_PERSONAL_KEY": "1", "DISABLE_PERSONAL_KEYS": "1"},
	} {
		t.Run(name, func(t *testing.T) {
			env["AUTH_JWKS_URL"], env["PW_PLATFORM_HOST"] = "http://127.0.0.1:9/jwks", "platform.example.com"
			_, srv, _, _ := newTestModule(t, env)
			out := get(t, srv.URL+"/api/chat/models")
			if s, _ := out["error"].(string); !strings.HasPrefix(s, "No model credential of your own.") || out["kind"] != "credential" {
				t.Fatal(out)
			}
		})
	}
}

// Without ACTIVATE the slash list and /name leave out the tools that reach
// pw code; Settings lists them all. They are there again with a sign-in.
func TestSlashListLeavesOutPWCodeWithoutActivate(t *testing.T) {
	m, srv, _, _ := newTestModule(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/jwks"})
	has := false
	m.A.Providers.PlatformAvailable = func(*http.Request, string) bool { return has }
	listed := func(q string) (agents, search bool) {
		for _, x := range get(t, srv.URL+"/api/chat/tools"+q)["tools"].([]any) {
			n := x.(map[string]any)["name"].(string)
			agents = agents || toolGroupOf(n) == "agents"
			search = search || n == "search_kb"
		}
		return
	}
	r := httptest.NewRequest("GET", "/", nil)
	if a, s := listed("?mine=1"); a || !s {
		t.Fatal("pw code tools are listed, or others are not", a, s)
	}
	if a, _ := listed(""); !a {
		t.Fatal("Settings lists every tool")
	}
	if text, l := m.expandSlashCommand(r, "/start_agent"); text != "" || l == nil || !strings.Contains(*l, "There is no /start_agent command.") {
		t.Fatal(text, l)
	}
	if text, l := m.expandSlashCommand(r, "/delegate the sweep"); text != "" || l != nil {
		t.Fatal(text, l)
	}
	if text, _ := m.expandSlashCommand(r, "/search_kb drag"); !strings.Contains(text, "search_kb") {
		t.Fatal(text)
	}
	has = true
	if a, _ := listed("?mine=1"); !a {
		t.Fatal("not listed with ACTIVATE")
	}
	if text, _ := m.expandSlashCommand(r, "/delegate the sweep"); !strings.Contains(text, "Call the delegate tool") {
		t.Fatal(text)
	}
}
