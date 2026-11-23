package chat

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// bridgeStudio is a Studio people reach through the ACTIVATE session
// proxy, with its own tools, an attached server and a skill.
func bridgeStudio(t *testing.T) *Module {
	t.Helper()
	m := parityModule(t, nil)
	m.A.Cfg.Auth.JWKSURL = "https://platform.example/api/platform/keys"
	m.A.Cfg.FrontAuth = true
	// The Studio's own tool runner, so a call runs as it would.
	m.Tools = goTools{m}
	return m
}

var (
	bobProxy = &auth.User{ID: "bob", Username: "bob", Source: "proxy"}
	bobOIDC  = &auth.User{ID: "bob", Username: "bob", Source: "oidc"}
)

// mcpAs posts one JSON-RPC message to /api/mcp as u (nil for nobody) with
// the given headers, and answers the status and the decoded body.
func mcpAs(t *testing.T, m *Module, u *auth.User, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/mcp", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	if u != nil {
		r = auth.WithUser(r, u)
	}
	w := httptest.NewRecorder()
	if err := m.mcpPost(w, r); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func listedTools(t *testing.T, m *Module, u *auth.User, headers map[string]string) []string {
	t.Helper()
	_, out := mcpAs(t, m, u, headers, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	res, _ := out["result"].(map[string]any)
	var names []string
	for _, x := range res["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func callText(t *testing.T, m *Module, u *auth.User, name string) (string, bool) {
	t.Helper()
	_, out := mcpAs(t, m, u, nil, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`)
	if e, ok := out["error"].(map[string]any); ok {
		return e["message"].(string), false
	}
	res := out["result"].(map[string]any)
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	return text, res["isError"] != true
}

// A person's own pw code session on another machine, reaching the Studio
// through the session proxy with their key, gets the tools a pw code chat
// of theirs gets (the library tools, the deployment's own, the attached
// servers' and the platform's), and they run.
func TestAPersonsSessionElsewhereGetsTheirChatsTools(t *testing.T) {
	m := bridgeStudio(t)
	if err := m.Keys.Set("bob", "pwt_bobs.key", nil, nil); err != nil {
		t.Fatal(err)
	}
	r := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/mcp", nil), bobProxy)
	got := listedTools(t, m, bobProxy, nil)
	chat := specNames(m.chatTools(r, true))
	for _, n := range chat {
		if !slices.Contains(got, n) && n != "save_chat_to_library" {
			t.Errorf("a pw code chat of theirs has %s and their session elsewhere does not", n)
		}
	}
	for _, n := range []string{"search_kb", "query_corpus", "read_kb_file", "list_libraries", "greet", "use_skill", "crm__find_deals", "list_workflows", "delegate"} {
		if !slices.Contains(got, n) {
			t.Errorf("%s is not listed: %v", n, got)
		}
	}
	for _, n := range []string{"save_chat_to_library", "delete_kb_files", "transfer_files", "start_agent", "stop_cluster"} {
		if slices.Contains(got, n) {
			t.Errorf("%s is listed", n)
		}
	}
	if text, ok := callText(t, m, bobProxy, "greet"); !ok || !strings.Contains(text, "hi") {
		t.Errorf("greet: %q %v", text, ok)
	}
	if tc := m.personCtx(r); deref(tc.UserKey) != "pwt_bobs.key" || deref(tc.UserID) != "bob" {
		t.Errorf("the call does not run as bob on his key: %+v", tc)
	}
}

// Only a person the session proxy vouches for gets them: not a Studio
// without sign-in, not a browser page (an Origin, or a fetch's
// Sec-Fetch-Site), not another bearer, not a sign-in of the Studio's own
// from a browser; MCP access off is off for them too; and without an
// ACTIVATE credential of their own the platform's tools are not offered
// or run, so the Studio's account is never used.
func TestOnlyAPersonTheProxyVouchesForGetsThem(t *testing.T) {
	m := bridgeStudio(t)
	m.Keys.Set("bob", "pwt_bobs.key", nil, nil)
	readOnly := func(names []string) bool {
		return !slices.Contains(names, "greet") && slices.Contains(names, "search_kb")
	}
	for what, c := range map[string]struct {
		u *auth.User
		h map[string]string
	}{
		"a browser page":   {bobProxy, map[string]string{"Origin": "https://elsewhere.example"}},
		"a fetch":          {bobProxy, map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"another bearer":   {bobProxy, map[string]string{"Authorization": "Bearer something"}},
		"an OIDC sign-in":  {bobOIDC, nil},
		"nobody signed in": {nil, nil},
	} {
		if got := listedTools(t, m, c.u, c.h); !readOnly(got) {
			t.Errorf("%s gets %v", what, got)
		}
	}
	m.A.Cfg.Auth.JWKSURL, m.A.Cfg.FrontAuth = "", false
	if got := listedTools(t, m, bobProxy, nil); !readOnly(got) {
		t.Errorf("a Studio without sign-in gives %v", got)
	}
	m.A.Cfg.Auth.JWKSURL, m.A.Cfg.FrontAuth = "https://platform.example/api/platform/keys", true

	t.Setenv("MCP_ENABLED", "0")
	if code, _ := mcpAs(t, m, bobProxy, nil, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code != 403 {
		t.Errorf("MCP access off answers %d", code)
	}
	t.Setenv("MCP_ENABLED", "1")

	m.Keys.Clear("bob")
	got := listedTools(t, m, bobProxy, nil)
	if slices.Contains(got, "list_workflows") || slices.Contains(got, "list_clusters") || !slices.Contains(got, "search_kb") {
		t.Errorf("without a key of their own: %v", got)
	}
	if text, ok := callText(t, m, bobProxy, "list_workflows"); ok || !strings.Contains(text, "unknown tool") {
		t.Errorf("list_workflows without a key of their own: %q", text)
	}
}

// The chat's setup card: the command for this Studio's address on the
// platform, and when a session of theirs last reached the tools.
func TestTheSetupCardNamesTheCommandAndSaysWhenItWorked(t *testing.T) {
	m := bridgeStudio(t)
	get := func(u *auth.User, h map[string]string) map[string]any {
		r := httptest.NewRequest(http.MethodGet, "/api/mcp/bridge", nil)
		r.Host = "127.0.0.1:8080"
		for k, v := range h {
			r.Header.Set(k, v)
		}
		if u != nil {
			r = auth.WithUser(r, u)
		}
		w := httptest.NewRecorder()
		if err := m.bridgeRoute(w, r); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	proxied := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-Host": "platform.example", "X-Forwarded-Prefix": "/me/session/ada/studio/"}
	out := get(bobProxy, proxied)
	want := "pw code mcp add --scope user --transport http --header 'Authorization: Bearer <your ACTIVATE API key>' studio https://platform.example/me/session/ada/studio/api/mcp"
	if out["available"] != true || out["command"] != want || out["keyHole"] != "<your ACTIVATE API key>" || out["seen"] != nil {
		t.Fatalf("card %v", out)
	}
	if out["platformTools"] != false || out["ownKey"] != false {
		t.Errorf("without a key of their own: %v", out)
	}
	// A session of Bob's reaches the tools; the card says so, and Ada's
	// card still does not.
	listedTools(t, m, bobProxy, nil)
	if seen, _ := get(bobProxy, proxied)["seen"].(map[string]any); seen == nil || seen["method"] != "tools/list" {
		t.Errorf("not seen: %v", seen)
	}
	if get(&auth.User{ID: "ada", Source: "proxy"}, proxied)["seen"] != nil {
		t.Error("ada's card says a session reached the tools")
	}
	// Not through the proxy, or an address a command cannot carry as is.
	if out := get(bobOIDC, proxied); out["available"] != false || out["command"] != nil {
		t.Errorf("an OIDC sign-in: %v", out)
	}
	if out := get(bobProxy, map[string]string{"X-Forwarded-Host": "platform.example; rm -rf ~"}); out["available"] != false {
		t.Errorf("a host with a command in it: %v", out)
	}
	// STUDIO_SELF_URL names the address where the Studio cannot tell, and
	// one a command cannot carry as is gives none.
	t.Setenv("STUDIO_SELF_URL", "https://studio.platform.example/")
	if out := get(bobProxy, nil); out["url"] != "https://studio.platform.example/api/mcp" {
		t.Errorf("STUDIO_SELF_URL: %v", out)
	}
	t.Setenv("STUDIO_SELF_URL", "https://studio.platform.example/x'y")
	if out := get(bobProxy, proxied); out["available"] != false {
		t.Errorf("a quote in STUDIO_SELF_URL: %v", out)
	}
	t.Setenv("STUDIO_SELF_URL", "")
	t.Setenv("MCP_ENABLED", "0")
	if out := get(bobProxy, proxied); out["available"] != false || !strings.Contains(out["why"].(string), "MCP access is off") {
		t.Errorf("MCP access off: %v", out)
	}
}

// show_in_viewer reads a workflow with the caller's key: a person without
// an ACTIVATE credential of their own here is not offered it and cannot
// call it, so it never runs on the Studio's key.
func TestTheViewerToolNeedsTheCallersOwnKey(t *testing.T) {
	m := bridgeStudio(t)
	if slices.Contains(listedTools(t, m, bobProxy, nil), "show_in_viewer") {
		t.Errorf("listed without a key of his own")
	}
	_, out := mcpAs(t, m, bobProxy, nil, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"show_in_viewer","arguments":{"kind":"workflow_dag","target":"x"}}}`)
	if out["error"] == nil {
		t.Errorf("called without a key of his own: %v", out)
	}
	if err := m.Keys.Set("bob", "pwt_bobs.key", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(listedTools(t, m, bobProxy, nil), "show_in_viewer") {
		t.Errorf("not listed with his own key")
	}
}
