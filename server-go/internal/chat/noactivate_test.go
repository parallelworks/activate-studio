package chat

import (
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
