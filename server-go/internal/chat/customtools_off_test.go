package chat

import (
	"net/http"

	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// withToolFile writes a tool file, as a deployment drops one in
// extensions/tools.
func withToolFile(t *testing.T, m *Module, name, command string) {
	t.Helper()
	dir := filepath.Join(m.Ext.Ext, "tools")
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(`{"name":"`+name+`","description":"From a file","command":"`+command+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// STUDIO_CUSTOM_TOOLS=0 turns custom tools off: neither the Settings ones
// nor tool files are offered, listed or run, and the saved ones are kept.
func TestCustomToolsOffRunNone(t *testing.T) {
	m, _, _, _ := newTestModule(t, map[string]string{"STUDIO_CUSTOM_TOOLS": "0"})
	m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "greet", "description": "Says hi", "command": "echo hi"}}})
	withToolFile(t, m, "hello", "echo hello")
	r := httptest.NewRequest("POST", "/api/chat/stream", nil)
	for _, name := range []string{"greet", "hello"} {
		if out, ok := m.executeInGo(r, name, `{}`, ToolCtx{}); ok {
			t.Errorf("%s ran with custom tools off: %v", name, out)
		}
		if text, _ := m.expandSlashCommand(r, "/"+name); strings.Contains(text, "Call the "+name) {
			t.Errorf("/%s is offered with custom tools off", name)
		}
	}
	if specs := customToolSpecs(m.eff(), m.Ext); len(specs) != 0 {
		t.Errorf("offered to the model: %v", specs)
	}
	for _, s := range activeToolSpecs(m.eff(), m.Ext) {
		if s.Name == "greet" || s.Name == "hello" {
			t.Errorf("%s is an active tool", s.Name)
		}
	}
	if strings.Contains(m.slashListing(), "Command tools") {
		t.Error("the slash listing names command tools")
	}
	if len(m.A.Settings.Effective().CustomTools) != 1 {
		t.Error("the saved custom tools were not kept")
	}
}

func TestCustomToolFilesRunWhenOn(t *testing.T) {
	m, _, _, _ := newTestModule(t, nil)
	withToolFile(t, m, "hello", "echo hello")
	out, ok := m.executeInGo(httptest.NewRequest("POST", "/api/chat/stream", nil), "hello", `{}`, ToolCtx{})
	if !ok || strings.TrimSpace(out.Result) != "hello" {
		t.Fatalf("%v %v", ok, out)
	}
}

// A custom tool runs without the Studio's secrets in its environment: no
// model key, platform token, client secret or cloud credential.
func TestCustomToolsRunWithoutTheStudiosSecrets(t *testing.T) {
	secrets := map[string]string{
		"OPENAI_API_KEY": "zq-secret-00-xv", "PW_API_KEY": "zq-secret-01-xv", "SIDECAR_MODEL_KEY": "zq-secret-02-xv",
		"OIDC_CLIENT_SECRET": "zq-secret-03-xv", "STUDIO_BRIDGE_SECRET": "zq-secret-04-xv", "STUDIO_GUFI_VT_SECRET": "zq-secret-05-xv",
		"GH_TOKEN": "zq-secret-06-xv", "GITHUB_TOKEN": "zq-secret-07-xv", "AWS_SECRET_ACCESS_KEY": "zq-secret-08-xv",
		"AWS_ACCESS_KEY_ID": "zq-secret-09-xv", "AWS_SESSION_TOKEN": "zq-secret-10-xv", "GOOGLE_APPLICATION_CREDENTIALS": "zq-secret-11-xv",
		"AZURE_CLIENT_SECRET": "zq-secret-12-xv", "PW_CREDENTIALS_DIR": "zq-secret-13-xv", "ANTHROPIC_API_KEY": "zq-secret-14-xv",
		"DB_PASSWORD": "zq-secret-15-xv", "STUDIO_KEEP_ME": "kept",
	}
	m, _, _, _ := newTestModule(t, secrets)
	m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "show_env", "description": "", "command": "env"}}})
	out, ok := m.executeInGo(httptest.NewRequest("POST", "/api/chat/stream", nil), "show_env", `{}`, ToolCtx{})
	if !ok {
		t.Fatal("the tool did not run")
	}
	for k, v := range secrets {
		if k == "STUDIO_KEEP_ME" {
			continue
		}
		if strings.Contains(out.Result, k+"=") || strings.Contains(out.Result, v) {
			t.Errorf("%s reached the tool", k)
		}
	}
	for _, k := range []string{"PATH=", "HOME=", "STUDIO_KEEP_ME=kept"} {
		if !strings.Contains(out.Result, k) {
			t.Errorf("%s did not reach the tool", k)
		}
	}
}

// The grounded model's agent mode (/v1/chat/completions, studio-agent)
// runs no custom tool, on a Studio of one too, whether custom tools are on
// or off (STUDIO_CUSTOM_TOOLS=0): they run in the Studio's own chats.
func TestTheGroundedModelRunsNoCustomTool(t *testing.T) {
	for _, off := range []bool{false, true} {
		env := map[string]string{}
		if off {
			env["STUDIO_CUSTOM_TOOLS"] = "0"
		}
		m, srv, gw, _ := newTestModule(t, env)
		m.Tools = runningTools{m.Tools.(fakeTools), m}
		mark := filepath.Join(t.TempDir(), "ran")
		m.A.Settings.Update(map[string]any{"customTools": []any{map[string]any{"name": "mark", "description": "", "command": "touch " + mark}}})
		m.A.Settings.Update(map[string]any{settings.CustomToolsApprovedKey: settings.CustomToolsDigest(m.A.Settings.Effective().CustomTools)})
		gw.load([]reply{call("mark", `{}`), text("Done.")})
		req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"studio-agent/test-model","messages":[{"role":"user","content":"mark it"}]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer any-gateway-key")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if _, statErr := os.Stat(mark); statErr == nil || res.StatusCode != 200 {
			t.Errorf("off=%v: the tool ran=%v (status %d)", off, statErr == nil, res.StatusCode)
		}
	}
}

// runningTools runs tools as the server does, with the test gateway.
type runningTools struct {
	fakeTools
	m *Module
}

func (t runningTools) Execute(r *http.Request, name, args string, ctx ToolCtx) (*Outcome, error) {
	if o, ok := t.m.executeInGo(r, name, args, ctx); ok {
		return o, nil
	}
	return &Outcome{Result: "no such tool"}, nil
}
