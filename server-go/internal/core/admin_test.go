package core

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// Saving Settings sends the whole form back: only a change of the shared
// history switch is kept, as an administrator's choice.
func TestOnlyAChangeOfSharedHistoryIsKept(t *testing.T) {
	e := signedIn(t)
	file := filepath.Join(e.ix, "settings.json")
	saved := func() map[string]any {
		var m map[string]any
		b, _ := os.ReadFile(file)
		json.Unmarshal(b, &m)
		return m
	}
	shared := func() any {
		return e.doAs("sam", "GET", "/api/settings", nil).body["effective"].(map[string]any)["chatSharedHistory"]
	}
	save := func(change map[string]any) {
		t.Helper()
		form := e.doAs("boss", "GET", "/api/settings", nil).body["effective"].(map[string]any)
		for k, v := range change {
			form[k] = v
		}
		if r := e.doAs("boss", "PUT", "/api/settings", form); r.status != 200 {
			t.Fatalf("%d %s", r.status, r.raw)
		}
	}
	if shared() != false {
		t.Fatal("on with sign-in and nothing set")
	}
	save(map[string]any{"appName": "Lab"})
	if m := saved(); m["appName"] != "Lab" || m["chatSharedHistory"] != nil || m["chatSharedHistoryChosen"] != nil {
		t.Errorf("saving another setting kept: %v %v", m["chatSharedHistory"], m["chatSharedHistoryChosen"])
	}
	if r := e.doAs("sam", "PUT", "/api/settings", map[string]any{"chatSharedHistory": true}); r.status != 403 || shared() != false {
		t.Errorf("sam: %d", r.status)
	}
	save(map[string]any{"chatSharedHistory": true})
	save(map[string]any{"appName": "Lab two"})
	if m := saved(); shared() != true || m["chatSharedHistory"] != true || m["chatSharedHistoryChosen"] != true {
		t.Errorf("turned on: %v %v", shared(), m)
	}
	save(map[string]any{"chatSharedHistory": false})
	if shared() != false || saved()["chatSharedHistory"] != false {
		t.Error("turned off again")
	}

	// A file v1.203.39 wrote: the form saved, the switch as it was.
	form := `{"appName":"Lab","ragAdvertiseRagModel":false,"ragEndpointAutoStart":false,"requirePersonalKey":false,"chatSharedHistory":true}`
	os.WriteFile(file, []byte(form), 0o644)
	if shared() != false {
		t.Error("an untouched default stayed on")
	}
	save(map[string]any{"appName": "Lab three"})
	if shared() != false {
		t.Error("a save of another setting turned it on")
	}
	save(map[string]any{"chatSharedHistory": true})
	if shared() != true {
		t.Error("could not be turned on")
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

// The app is told of someone who has not signed in, and of the Studio's own
// sign-in page.
func TestConfigSignedOut(t *testing.T) {
	jwks := map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/keys"}
	oidc := map[string]string{"OIDC_ISSUER": "http://127.0.0.1:9", "OIDC_CLIENT_ID": "studio"}
	for _, c := range []struct {
		env       map[string]string
		who       string
		out, page any
	}{
		{jwks, "", true, nil},
		{jwks, "ada", nil, nil},
		{oidc, "", true, "/api/auth/login"},
		{oidc, "ada", nil, "/api/auth/login"},
		{nil, "", nil, nil},
	} {
		e := newEnv(t, c.env)
		b := e.do("GET", "/api/config", nil).body
		if c.who != "" {
			b = e.doAs(c.who, "GET", "/api/config", nil).body
		}
		if b["signedOut"] != c.out || b["signInUrl"] != c.page {
			t.Errorf("%v %q: %v %v", c.env, c.who, b["signedOut"], b["signInUrl"])
		}
	}
}

// Voice runs on the grounded model's agent mode, which is refused where
// people sign in: the page is offered it only on a Studio without sign-in.
func TestVoiceIsOfferedOnlyWithoutSignIn(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{"VOICE_ENABLED": "1", "VOICE_URL": "https://voice.example"}, true},
		{map[string]string{"VOICE_ENABLED": "1", "VOICE_URL": "https://voice.example", "AUTH_JWKS_URL": "http://127.0.0.1:9/keys"}, false},
	} {
		for k, v := range c.env {
			t.Setenv(k, v)
		}
		e := newEnv(t, c.env)
		for _, who := range []string{"", "ada"} {
			b := e.do("GET", "/api/config", nil).body
			if who != "" {
				b = e.doAs(who, "GET", "/api/config", nil).body
			}
			voice, _ := b["features"].(map[string]any)["voice"].(map[string]any)
			if voice["enabled"] != c.want {
				t.Errorf("%v %q: %v", c.env, who, voice)
			}
		}
	}
}

// STUDIO_CUSTOM_TOOLS=0: Settings says so, keeps the saved tools, and
// takes no change to them; the rest of a save goes through.
func TestCustomToolsOffInSettings(t *testing.T) {
	t.Setenv("STUDIO_CUSTOM_TOOLS", "0")
	e := signedIn(t)
	os.MkdirAll(e.ix, 0o755)
	os.WriteFile(filepath.Join(e.ix, "settings.json"), []byte(`{"customTools":[{"name":"greet","description":"","command":"echo hi"}]}`), 0o644)
	got := e.doAs("boss", "GET", "/api/settings", nil)
	if got.body["customToolsOff"] != true {
		t.Fatalf("customToolsOff: %v", got.body["customToolsOff"])
	}
	add := map[string]any{"customTools": []any{map[string]any{"name": "greet", "description": "", "command": "echo hi"}, map[string]any{"name": "cat_all", "description": "", "command": "cat"}}}
	if r := e.doAs("boss", "PUT", "/api/settings", add); r.status != 403 || !strings.Contains(string(r.raw), "STUDIO_CUSTOM_TOOLS=0") {
		t.Fatalf("a change to custom tools: %d %s", r.status, r.raw)
	}
	// The form sends them back unchanged with another change: saved, and
	// the list an administrator cannot see is not approved by it.
	eff := got.body["effective"].(map[string]any)
	eff["kbMission"] = "Support the design team."
	if r := e.doAs("boss", "PUT", "/api/settings", eff); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	after := e.doAs("boss", "GET", "/api/settings", nil)
	if after.body["customToolsApproved"] != false {
		t.Error("a save with custom tools off approved the hidden list")
	}
	if tools := after.body["effective"].(map[string]any)["customTools"].([]any); len(tools) != 1 {
		t.Errorf("the saved tools were not kept: %v", tools)
	}
}

func TestCustomToolsOnBySettingsDefault(t *testing.T) {
	e := signedIn(t)
	if r := e.doAs("boss", "GET", "/api/settings", nil); r.body["customToolsOff"] != nil {
		t.Fatalf("customToolsOff: %v", r.body["customToolsOff"])
	}
}
