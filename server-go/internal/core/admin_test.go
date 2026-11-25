package core

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/auth"
)

// doAs is do for a signed-in person.
func (e *env) doAs(who, method, url string, body any) resp {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, url, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = auth.WithUser(r, &auth.User{ID: who, Username: who})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	out := resp{status: w.Code, header: w.Header(), raw: w.Body.Bytes()}
	json.Unmarshal(out.raw, &out.body)
	return out
}

func signedIn(t *testing.T) *env {
	t.Setenv("PW_USER", "boss")
	t.Setenv("STUDIO_ADMINS", "deputy")
	return newEnv(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/keys", "STUDIO_CONFIG_FILE": filepath.Join(t.TempDir(), "studio.env")})
}

// deploymentChanges is a change to each of the Studio's settings.
var deploymentChanges = map[string]any{
	"customTools":          []any{map[string]any{"name": "greet", "description": "Says hi", "command": "echo hi"}},
	"mcpServers":           []any{map[string]any{"name": "tools", "url": "https://mcp.example.com/mcp"}},
	"agentExecution":       "both",
	"pwcodePermissionMode": "accept-edits",
	"disabledTools":        []any{"search_kb"},
	"updates":              map[string]any{"install": "auto"},
	"aiEnabled":            false,
	"ragEndpointName":      "lab-model",
	"chatEngine":           "studio",
	"bannerText":           "NOTICE",
	"theme":                "dark",
	"navWidth":             300,
	"accent":               "custom:#123456",
	"sections":             []any{"chat", "library", "settings"},
	"workflowCollection":   []any{"marketplace/hello"},
	"voiceEnabled":         true,
	"voiceUrl":             "https://voice.example.com",
	"appName":              "Lab",
	"kbMission":            "Our lab.",
	"claudeCodeAccounts":   "on",
	"mcpEnabled":           false,
	"ragProxyEnabled":      false,
	"maxToolRounds":        60,
	"serveWorkflows":       `{}`,
}

func TestOnlyAdministratorsChangeTheStudiosSettings(t *testing.T) {
	e := signedIn(t)
	file := filepath.Join(e.ix, "settings.json")
	before, _ := os.ReadFile(file)

	s := e.doAs("sam", "GET", "/api/settings", nil)
	if s.body["locked"] != SettingsAdminOnly {
		t.Fatalf("locked: %v", s.body["locked"])
	}
	for k, v := range deploymentChanges {
		if r := e.doAs("sam", "PUT", "/api/settings", map[string]any{k: v}); r.status != 403 {
			t.Errorf("%s: %d %s", k, r.status, r.raw)
		}
	}
	// One forbidden field refuses the whole request.
	if r := e.doAs("sam", "PUT", "/api/settings", map[string]any{"bannerText": "", "customTools": deploymentChanges["customTools"]}); r.status != 403 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	// The form sent back as it stands is no change, and nothing is written.
	if r := e.doAs("sam", "PUT", "/api/settings", s.body["effective"]); r.status != 200 {
		t.Fatalf("form as it stands: %d %s", r.status, r.raw)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatalf("a refused save wrote: %s", after)
	}
	// Nobody signed in changes nothing either.
	if r := e.do("PUT", "/api/settings", map[string]any{"appName": "Lab"}); r.status != 403 {
		t.Fatalf("anonymous: %d", r.status)
	}

	// The owner and STUDIO_ADMINS change each.
	for _, who := range []string{"boss", "deputy"} {
		if r := e.doAs(who, "GET", "/api/settings", nil); r.body["locked"] != nil {
			t.Fatalf("%s locked: %v", who, r.body["locked"])
		}
	}
	for k, v := range deploymentChanges {
		if r := e.doAs("boss", "PUT", "/api/settings", map[string]any{k: v}); r.status != 200 {
			t.Errorf("admin %s: %d %s", k, r.status, r.raw)
		}
	}
	if r := e.doAs("deputy", "GET", "/api/settings", nil); r.body["customToolsApproved"] != true {
		t.Fatalf("custom tools saved by an administrator: %v", r.body["customToolsApproved"])
	}
}

func TestCustomToolsWaitForAnAdministratorsSave(t *testing.T) {
	e := signedIn(t)
	// A list written by an earlier release, by anyone.
	os.MkdirAll(e.ix, 0o755)
	os.WriteFile(filepath.Join(e.ix, "settings.json"), []byte(`{"customTools":[{"name":"greet","description":"","command":"echo hi"}]}`), 0o644)
	if r := e.doAs("sam", "GET", "/api/settings", nil); r.body["customToolsApproved"] != false {
		t.Fatalf("%v", r.body["customToolsApproved"])
	}
	// Someone else sending it back approves nothing.
	eff := e.doAs("sam", "GET", "/api/settings", nil).body["effective"]
	e.doAs("sam", "PUT", "/api/settings", eff)
	if r := e.doAs("sam", "GET", "/api/settings", nil); r.body["customToolsApproved"] != false {
		t.Fatal("a non-administrator's save approved the tools")
	}
	// An administrator's save, unchanged, does.
	if r := e.doAs("boss", "PUT", "/api/settings", eff); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r := e.doAs("sam", "GET", "/api/settings", nil); r.body["customToolsApproved"] != true {
		t.Fatal("an administrator's save did not approve the tools")
	}
}

func TestWithoutSignInSettingsSaveAsBefore(t *testing.T) {
	e := newEnv(t, nil)
	if r := e.do("GET", "/api/settings", nil); r.body["locked"] != nil || r.body["customToolsApproved"] != true {
		t.Fatalf("%s", r.raw)
	}
	for k, v := range deploymentChanges {
		if r := e.do("PUT", "/api/settings", map[string]any{k: v}); r.status != 200 {
			t.Errorf("%s: %d %s", k, r.status, r.raw)
		}
	}
	saved, _ := os.ReadFile(filepath.Join(e.ix, "settings.json"))
	if bytes.Contains(saved, []byte("customToolsApproved")) {
		t.Error("a Studio without sign-in keeps no approval")
	}
	if r := e.do("POST", "/api/extensions/skill", map[string]any{"name": "Deploy", "body": "Run it."}); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
}

func TestOnlyAdministratorsAddSkillsAndPersonas(t *testing.T) {
	e := signedIn(t)
	for _, c := range []struct {
		url  string
		body map[string]any
	}{
		{"/api/extensions/skill", map[string]any{"name": "Deploy", "body": "Run it."}},
		{"/api/extensions/persona", map[string]any{"name": "Reviewer", "body": "Be strict.", "shared": false}},
		{"/api/extensions/persona", map[string]any{"name": "Reviewer", "body": "Be strict."}},
	} {
		if r := e.doAs("sam", "POST", c.url, c.body); r.status != 403 {
			t.Errorf("%s %v: %d %s", c.url, c.body, r.status, r.raw)
		}
	}
	if r := e.doAs("boss", "POST", "/api/extensions/skill", map[string]any{"name": "Deploy", "body": "Run it."}); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if r := e.doAs("boss", "POST", "/api/extensions/persona", map[string]any{"name": "Reviewer", "body": "Be strict.", "shared": false}); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
}

// The app is told when an ACTIVATE sign-in of each person's own comes
// first: with sign-in on, REQUIRE_PERSONAL_KEY set, and personal keys on.
func TestConfigSignInFirst(t *testing.T) {
	jwks := "http://127.0.0.1:9/keys"
	for _, c := range []struct {
		env  map[string]string
		want any
	}{
		{map[string]string{"AUTH_JWKS_URL": jwks, "REQUIRE_PERSONAL_KEY": "1"}, true},
		{map[string]string{"AUTH_JWKS_URL": jwks}, nil},
		{map[string]string{"AUTH_JWKS_URL": jwks, "REQUIRE_PERSONAL_KEY": "1", "DISABLE_PERSONAL_KEYS": "1"}, nil},
		{map[string]string{"REQUIRE_PERSONAL_KEY": "1"}, nil},
	} {
		t.Setenv("REQUIRE_PERSONAL_KEY", c.env["REQUIRE_PERSONAL_KEY"])
		e := newEnv(t, c.env)
		if got := e.doAs("ada", "GET", "/api/config", nil).body["signInFirst"]; got != c.want {
			t.Fatalf("%v: %v", c.env, got)
		}
	}
}
