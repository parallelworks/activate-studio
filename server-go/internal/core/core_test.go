package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/stepmesh"
)

// The cases of server/test/setup.test.mjs, setupIcon.test.mjs,
// setupSignIn.test.mjs, manifest.test.mjs and aiFeatures.test.mjs that
// reach into Node's module state (setStopper, process.env changed between
// requests), run against the Go routes in Node-free mode.

type env struct {
	t    *testing.T
	mux  *http.ServeMux
	a    *app.App
	home string
	kb   string
	ix   string
}

func newEnv(t *testing.T, extra map[string]string) *env {
	t.Helper()
	e := &env{t: t, home: t.TempDir(), kb: t.TempDir(), ix: t.TempDir()}
	for _, k := range []string{"AUTH_JWKS_URL", "OIDC_ISSUER", "STUDIO_CONFIG_FILE", "STUDIO_RESTARTABLE", "STUDIO_APP_BUNDLE", "PW_PLATFORM_HOST", "APP_ICON", "APP_ICON_DARK", "APP_FAVICON", "AI_ENABLED", "KB_MISSION", "SUGGESTED_PROMPTS", "STUDIO_SECTIONS", "APP_NAME", "KB_LABEL", "PW_API_KEY", "OPENAI_API_KEY", "STUDIO_DATA_DIR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("HOME", e.home)
	vars := map[string]string{"KB_ROOT": e.kb, "INDEX_BASE": e.ix, "STUDIO_NODE": "off", "GUFI_BIN": filepath.Join(e.home, "no-gufi"), "STUDIO_PROJECT_ROOT": e.home}
	for k, v := range extra {
		vars[k] = v
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	cfg, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.a = app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	e.mux = http.NewServeMux()
	Register(e.mux, e.a)
	return e
}

type resp struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (e *env) do(method, url string, body any, headers ...string) resp {
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd, ct = bytes.NewReader(j), "application/json"
	}
	r := httptest.NewRequest(method, url, rd)
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	out := resp{status: w.Code, header: w.Header(), raw: w.Body.Bytes()}
	json.Unmarshal(out.raw, &out.body)
	return out
}

func TestSetupStatusAndBrowse(t *testing.T) {
	e := newEnv(t, nil)
	os.Mkdir(filepath.Join(e.home, "beta"), 0o755)
	os.Mkdir(filepath.Join(e.home, "Alpha"), 0o755)
	os.Mkdir(filepath.Join(e.home, ".hidden"), 0o755)
	os.WriteFile(filepath.Join(e.home, "file.txt"), []byte("x"), 0o644)
	r := e.do("GET", "/api/setup/status", nil)
	p := r.body["primary"].(map[string]any)
	if r.status != 200 || r.body["firstRun"] != true || r.body["canRestart"] != false || p["path"] != e.kb || p["indexed"] != false || p["writable"] != true {
		t.Fatalf("%s", r.raw)
	}
	if r.body["port"].(float64) != 4080 || r.body["portNext"] != nil || r.body["libraries"].([]any)[0].(map[string]any)["id"] != "kb" {
		t.Fatalf("%s", r.raw)
	}
	if !regexp.MustCompile(`^\{"firstRun":true,"standalone":true,"canRestart":false,"launcher":false,"macApp":null,"port":4080,"portNext":null,"primary":\{"path":`).Match(r.raw) {
		t.Fatalf("key order: %s", r.raw)
	}
	t.Setenv("PW_PLATFORM_HOST", "platform.test")
	if e.do("GET", "/api/setup/status", nil).body["firstRun"] != false {
		t.Fatal("platform deployment is no first run")
	}
	os.Unsetenv("PW_PLATFORM_HOST")

	b := e.do("GET", "/api/setup/browse", nil)
	if b.body["path"] != e.home || b.body["parent"] != filepath.Dir(e.home) {
		t.Fatalf("%s", b.raw)
	}
	want := fmt.Sprintf(`[{"name":"Alpha","path":"%s"},{"name":"beta","path":"%s"}]`, filepath.Join(e.home, "Alpha"), filepath.Join(e.home, "beta"))
	if got, _ := json.Marshal(b.body["dirs"]); string(got) != want {
		t.Fatalf("dirs %s", got)
	}
	if e.do("GET", "/api/setup/browse?path=~/Alpha", nil).body["path"] != filepath.Join(e.home, "Alpha") {
		t.Fatal("~")
	}
	if e.do("GET", "/api/setup/browse?path="+filepath.Join(e.home, "nope"), nil).status != 404 || e.do("GET", "/api/setup/browse?path="+filepath.Join(e.home, "file.txt"), nil).status != 400 {
		t.Fatal("missing or not a folder")
	}
	if e.do("GET", "/api/setup/browse?path=/", nil).body["parent"] != nil {
		t.Fatal("root has no parent")
	}
}

func TestPrimaryFolderAndRestart(t *testing.T) {
	e := newEnv(t, nil)
	other := t.TempDir()
	os.Mkdir(filepath.Join(e.home, "beta"), 0o755)
	if e.do("POST", "/api/setup/primary-folder", map[string]any{}).status != 400 {
		t.Fatal("no path")
	}
	holder := e.do("POST", "/api/setup/primary-folder", map[string]any{"path": filepath.Dir(e.ix)})
	if holder.status != 400 || !strings.Contains(holder.body["error"].(string), "own index") {
		t.Fatalf("%s", holder.raw)
	}
	same := e.do("POST", "/api/setup/primary-folder", map[string]any{"path": e.kb})
	if same.body["saved"] != true || same.body["restarting"] != false {
		t.Fatalf("%s", same.raw)
	}
	none := e.do("POST", "/api/setup/primary-folder", map[string]any{"path": other})
	if none.body["saved"] != false || !strings.Contains(none.body["note"].(string), "KB_ROOT="+other) {
		t.Fatalf("%s", none.raw)
	}
	conf := filepath.Join(t.TempDir(), "studio.env")
	t.Setenv("STUDIO_CONFIG_FILE", conf)
	nr := e.do("POST", "/api/setup/primary-folder", map[string]any{"path": "~/beta"})
	if nr.body["saved"] != true || nr.body["restarting"] != false || app.ReadLauncherSettings()["KB_ROOT"] != filepath.Join(e.home, "beta") {
		t.Fatalf("%s", nr.raw)
	}
	t.Setenv("STUDIO_RESTARTABLE", "1")
	code := make(chan int, 1)
	e.a.SetStopper(func(c int) { code <- c })
	if e.do("GET", "/api/setup/status", nil).body["canRestart"] != true {
		t.Fatal("canRestart")
	}
	t.Setenv("STUDIO_APP_BUNDLE", "/Applications/ACTIVATE Studio.app")
	if e.do("GET", "/api/setup/status", nil).body["macApp"] != "/Applications/ACTIVATE Studio.app" {
		t.Fatal("macApp")
	}
	r := e.do("POST", "/api/setup/primary-folder", map[string]any{"path": other})
	if string(r.raw) != `{"saved":true,"restarting":true}` {
		t.Fatalf("%s", r.raw)
	}
	select {
	case c := <-code:
		if c != app.RestartExitCode {
			t.Fatal(c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no restart")
	}
}

func TestPort(t *testing.T) {
	e := newEnv(t, nil)
	probe, _ := net.Listen("tcp", "127.0.0.1:0")
	free := probe.Addr().(*net.TCPAddr).Port
	probe.Close()
	taken, _ := net.Listen("tcp", "127.0.0.1:0")
	defer taken.Close()
	busy := taken.Addr().(*net.TCPAddr).Port
	wide, _ := net.Listen("tcp", "0.0.0.0:0")
	if PortFree(wide.Addr().(*net.TCPAddr).Port, "127.0.0.1") || PortFree(busy, "0.0.0.0") || !PortFree(free, "127.0.0.1") {
		t.Fatal("portFree")
	}
	wide.Close()
	for _, p := range []any{nil, "x", 80, 70000, 4180.5} {
		if e.do("POST", "/api/setup/port", map[string]any{"port": p}).status != 400 {
			t.Error("accepted", p)
		}
	}
	if !strings.Contains(e.do("POST", "/api/setup/port", map[string]any{"port": 4080}).body["note"].(string), "already uses") {
		t.Fatal("same port")
	}
	if n := e.do("POST", "/api/setup/port", map[string]any{"port": free}); n.body["saved"] != false || !strings.Contains(n.body["note"].(string), fmt.Sprintf("PORT=%d", free)) {
		t.Fatalf("%s", n.raw)
	}
	t.Setenv("STUDIO_CONFIG_FILE", filepath.Join(t.TempDir(), "studio.env"))
	in := e.do("POST", "/api/setup/port", map[string]any{"port": busy})
	if in.status != 409 || !strings.Contains(in.body["error"].(string), fmt.Sprintf("port %d is in use", busy)) {
		t.Fatalf("%s", in.raw)
	}
	if s := e.do("POST", "/api/setup/port", map[string]any{"port": free}); s.body["saved"] != true || app.ReadLauncherSettings()["PORT"] != fmt.Sprint(free) {
		t.Fatalf("%s", s.raw)
	}
	if e.do("GET", "/api/setup/status", nil).body["portNext"].(float64) != float64(free) {
		t.Fatal("portNext")
	}
	e.do("POST", "/api/setup/port", map[string]any{"port": 4080})
	if app.ReadLauncherSettings()["PORT"] != "4080" || e.do("GET", "/api/setup/status", nil).body["portNext"] != nil {
		t.Fatal("back to the port in use")
	}
	t.Setenv("STUDIO_RESTARTABLE", "1")
	code := make(chan int, 1)
	e.a.SetStopper(func(c int) { code <- c })
	if r := e.do("POST", "/api/setup/port", map[string]any{"port": free}); string(r.raw) != fmt.Sprintf(`{"saved":true,"restarting":true,"port":%d}`, free) {
		t.Fatalf("%s", r.raw)
	}
	if c := <-code; c != 75 {
		t.Fatal(c)
	}
}

func TestPersonalDefaultsAndComplete(t *testing.T) {
	e := newEnv(t, nil)
	s := e.do("GET", "/api/settings", nil)
	eff := s.body["effective"].(map[string]any)
	if eff["aiEnabled"] != false || len(eff["suggestedPrompts"].([]any)) != 10 {
		t.Fatalf("%s", s.raw)
	}
	r := e.do("PUT", "/api/settings", map[string]any{"suggestedPrompts": eff["suggestedPrompts"], "kbMission": eff["kbMission"]})
	if r.status != 200 {
		t.Fatalf("%s", r.raw)
	}
	file, _ := os.ReadFile(filepath.Join(e.ix, "settings.json"))
	if strings.Contains(string(file), "suggestedPrompts") || strings.Contains(string(file), "kbMission") {
		t.Fatalf("defaults saved: %s", file)
	}
	e.do("PUT", "/api/settings", map[string]any{"suggestedPrompts": []string{"Only this one"}, "kbMission": "My lab notes."})
	file, _ = os.ReadFile(filepath.Join(e.ix, "settings.json"))
	if string(file) != "{\n \"suggestedPrompts\": [\n  \"Only this one\"\n ],\n \"kbMission\": \"My lab notes.\"\n}" {
		t.Fatalf("%q", file)
	}
	c := e.do("POST", "/api/setup/complete", nil)
	if string(c.raw) != `{"setupDone":true}` {
		t.Fatalf("%s", c.raw)
	}
	var saved map[string]any
	file, _ = os.ReadFile(filepath.Join(e.ix, "settings.json"))
	json.Unmarshal(file, &saved)
	if saved["setupDone"] != true || saved["aiEnabled"] != false || saved["sections"] == nil {
		t.Fatalf("%s", file)
	}
	if e.do("GET", "/api/setup/status", nil).body["firstRun"] != false {
		t.Fatal("first run after complete")
	}
}

func TestSettingsValidation(t *testing.T) {
	e := newEnv(t, nil)
	for _, c := range []struct {
		body map[string]any
		err  string
	}{
		{map[string]any{"accent": "Bad Accent"}, "invalid accent"},
		{map[string]any{"iconUrl": "relative.png"}, "icon must be an http(s) URL or an absolute path"},
		{map[string]any{"sweepIntervalSec": 90000}, "sweep interval must be 0 to 86400 seconds"},
		{map[string]any{"maxToolRounds": 4}, "tool rounds must be 5 to 200"},
		{map[string]any{"chatModelWindows": "{nope"}, "chatModelWindows must be JSON"},
		{map[string]any{"updates": []int{}}, "updates must be an object"},
		{map[string]any{"updates": map[string]any{"channel": "beta"}}, "updates.channel must be stable or preview"},
		{map[string]any{"mcpServers": []any{map[string]any{"name": "x", "url": "ftp://x"}}}, "MCP server url must be http(s)"},
		{map[string]any{"agentExecution": "fast"}, "agent execution must be runner, sessions, or both"},
	} {
		r := e.do("PUT", "/api/settings", c.body)
		if r.status != 400 || r.body["error"] != c.err {
			t.Errorf("%v: %d %s", c.body, r.status, r.raw)
		}
	}
	r := e.do("PUT", "/api/settings", map[string]any{"appName": "  My Studio  ", "theme": "dark", "mcpServers": []any{map[string]any{"name": "My Server!", "url": " https://m.example/mcp "}}, "updates": map[string]any{"channel": "prerelease"}, "ragEndpointName": "--My RAG__ end--", "sections": []any{"chat", nil}})
	saved := r.body["saved"].(map[string]any)
	if r.status != 200 || saved["appName"] != "  My Studio  " || saved["theme"] != "dark" || saved["ragEndpointName"] != "my-rag-end" {
		t.Fatalf("%s", r.raw)
	}
	if got, _ := json.Marshal(saved["mcpServers"]); string(got) != `[{"name":"my_server","url":"https://m.example/mcp"}]` {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(string(r.raw), `"updates":{"check":true,"install":"manual","channel":"preview"}`) {
		t.Fatalf("%s", r.raw)
	}
	if got, _ := json.Marshal(saved["sections"]); string(got) != `["chat","null"]` {
		t.Fatalf("String(null) is \"null\": %s", got)
	}
	// On disk Preview is "prerelease", the value v1.188 and older read, so
	// a rollback to one of them stays on the channel.
	file, _ := os.ReadFile(filepath.Join(e.ix, "settings.json"))
	if !strings.Contains(string(file), `"channel": "prerelease"`) {
		t.Fatalf("settings.json: %s", file)
	}
	e.do("PUT", "/api/settings", map[string]any{"updates": map[string]any{"channel": "preview"}})
	if file, _ := os.ReadFile(filepath.Join(e.ix, "settings.json")); !strings.Contains(string(file), `"channel": "prerelease"`) {
		t.Fatalf("settings.json after preview: %s", file)
	}
}

func TestConfigAndManifest(t *testing.T) {
	e := newEnv(t, nil)
	c := e.do("GET", "/api/config", nil)
	if c.status != 200 || c.body["iconUrl"] != nil || c.body["user"].(map[string]any)["id"] != "user" || c.body["workflowCount"].(float64) != 0 {
		t.Fatalf("%s", c.raw)
	}
	if !regexp.MustCompile(`^\{"appName":"Studio","iconUrl":null,"iconUrlDark":null,"faviconUrl":null,"faviconUrlDark":null,"kbLabel":`).Match(c.raw) {
		t.Fatalf("order %s", c.raw)
	}
	m := e.do("GET", "/manifest.webmanifest", nil)
	if m.header.Get("Content-Type") != "application/manifest+json" || m.header.Get("Cache-Control") != "no-store, must-revalidate" || m.body["short_name"] != "Studio" {
		t.Fatalf("%v %s", m.header, m.raw)
	}
	e.do("PUT", "/api/settings", map[string]any{"appName": "ACTIVATE Research Studio", "bannerText": "UNCLASSIFIED", "theme": "dark"})
	m = e.do("GET", "/manifest.webmanifest", nil)
	if m.body["short_name"] != "Studio" || m.body["theme_color"] != "#24612e" || m.body["background_color"] != "#0c1320" || m.body["display_override"] == nil {
		t.Fatalf("%s", m.raw)
	}
	h := e.do("GET", "/api/help", nil)
	if h.header.Get("Content-Type") != "text/markdown; charset=utf-8" || string(h.raw) != "No help content configured." {
		t.Fatalf("%s", h.raw)
	}
	if v := e.do("GET", "/api/version", nil); v.body["version"] == nil || v.body["startedAt"] == nil {
		t.Fatalf("%s", v.raw)
	}
}

func iconForm(t *testing.T, name string, body []byte) ([]byte, string) {
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(body)
	mw.Close()
	return b.Bytes(), mw.FormDataContentType()
}

var pngBytes = append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 'I', 'H', 'D', 'R', 0, 0, 0, 16, 0, 0, 0, 16}, make([]byte, 20)...)

func TestIconUpload(t *testing.T) {
	e := newEnv(t, nil)
	body, ct := iconForm(t, "mark.png", pngBytes)
	r := e.do("POST", "/api/setup/icon", body, "Content-Type", ct)
	if r.status != 200 || !regexp.MustCompile(`^/api/brand-icon\?v=[0-9a-f]{10}$`).MatchString(r.body["light"].(string)) || r.body["dark"] != nil {
		t.Fatalf("%s", r.raw)
	}
	if !regexp.MustCompile(`branding/icon-[0-9a-f]{16}\.png$`).MatchString(e.a.Settings.Effective().IconURL) {
		t.Fatal(e.a.Settings.Effective().IconURL)
	}
	for _, u := range []string{r.body["light"].(string), e.do("GET", "/api/config", nil).body["faviconUrl"].(string)} {
		g := e.do("GET", u, nil)
		if g.status != 200 || g.header.Get("Content-Type") != "image/png" || !bytes.Equal(g.raw, pngBytes) || !strings.Contains(g.header.Get("Content-Security-Policy"), "sandbox") || g.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", u, g.status, g.header)
		}
	}
	gif, ct := iconForm(t, "a.gif", []byte("GIF89a"))
	if g := e.do("POST", "/api/setup/icon", gif, "Content-Type", ct); g.status != 415 || g.body["error"] != "The icon must be a PNG, JPEG, SVG or WebP image." {
		t.Fatalf("%s", g.raw)
	}
	big, ct := iconForm(t, "big.png", append(append([]byte{}, pngBytes...), make([]byte, IconMaxBytes)...))
	if g := e.do("POST", "/api/setup/icon", big, "Content-Type", ct); g.status != 413 || g.body["error"] != "The icon is over 1 MB." {
		t.Fatalf("%s", g.raw)
	}
	if g := e.do("POST", "/api/setup/icon", map[string]string{"file": "x"}); g.status != 400 || !strings.Contains(g.body["error"].(string), "multipart") {
		t.Fatalf("%s", g.raw)
	}
	svg, ct := iconForm(t, "s.svg", []byte(`<svg onload="x()"></svg>`))
	if g := e.do("POST", "/api/setup/icon", svg, "Content-Type", ct); g.status != 415 || g.body["error"] != "This SVG contains an event handler. Export it as a plain SVG, or as a PNG." {
		t.Fatalf("%s", g.raw)
	}
	if SVGProblem(`<svg><image href="data:image/png;base64,AAAA"/></svg>`) != "" || SVGProblem(`<svg><image href="http://x/y.png"/></svg>`) == "" || SVGProblem(`<svg style="fill:url(#g)"/>`) != "" {
		t.Fatal("svg refs")
	}
	d := e.do("DELETE", "/api/setup/icon", nil)
	if d.body["light"] != nil || d.body["iconUrl"] != "" {
		t.Fatalf("%s", d.raw)
	}
	files, _ := os.ReadDir(filepath.Join(e.ix, "branding"))
	if len(files) != 0 {
		t.Fatal("unused icon kept")
	}
}

func TestSignInRefusesSetup(t *testing.T) {
	e := newEnv(t, map[string]string{"AUTH_JWKS_URL": "http://127.0.0.1:9/keys", "STUDIO_CONFIG_FILE": filepath.Join(t.TempDir(), "studio.env")})
	if r := e.do("GET", "/api/setup/status", nil); string(r.raw) != `{"firstRun":false,"standalone":false,"canRestart":false}` {
		t.Fatalf("%s", r.raw)
	}
	for _, c := range [][2]string{{"GET", "/api/setup/browse?path=/"}, {"POST", "/api/setup/primary-folder"}, {"POST", "/api/setup/index"}, {"POST", "/api/setup/complete"}, {"POST", "/api/setup/icon"}, {"DELETE", "/api/setup/icon?variant=dark"}} {
		if r := e.do(c[0], c[1], map[string]any{"path": "/tmp"}); r.status != 403 {
			t.Errorf("%v: %d", c, r.status)
		}
	}
}

func TestExtensions(t *testing.T) {
	e := newEnv(t, nil)
	r := e.do("POST", "/api/extensions/persona", map[string]any{"name": "Code Reviewer!", "description": "Reviews\ncode", "body": "---\nname: x\n---\nBe strict."})
	if string(r.raw) != `{"ok":true,"name":"code_reviewer","shared":true}` {
		t.Fatalf("%s", r.raw)
	}
	doc, _ := os.ReadFile(filepath.Join(e.kb, ".agents", "code_reviewer.md"))
	if string(doc) != "---\nname: code_reviewer\ndescription: Reviews code\n---\n\nBe strict.\n" {
		t.Fatalf("%q", doc)
	}
	if r := e.do("POST", "/api/extensions/skill", map[string]any{"name": "x", "body": "  "}); r.status != 400 || r.body["error"] != "the document is empty" {
		t.Fatalf("%s", r.raw)
	}
	e.do("POST", "/api/extensions/skill", map[string]any{"name": "Deploy", "body": "# Deploy\nRun the script."})
	p := e.do("GET", "/api/extensions/persona?name=code_reviewer", nil)
	if string(p.raw) != `{"name":"code_reviewer","description":"Reviews code","body":"Be strict.","shared":true,"icon":""}` {
		t.Fatalf("%s", p.raw)
	}
	if s := e.do("GET", "/api/extensions/skill?name=deploy", nil); s.body["description"] != "Run the script." {
		t.Fatalf("%s", s.raw)
	}
	body, ct := iconForm(t, "face.PNG", pngBytes)
	ic := e.do("POST", "/api/extensions/persona-icon?name=code_reviewer", body, "Content-Type", ct)
	if string(ic.raw) != `{"ok":true,"icon":".agents/icons/code_reviewer.png","attached":true}` {
		t.Fatalf("%s", ic.raw)
	}
	l := e.do("GET", "/api/extensions", nil)
	if got, _ := json.Marshal(l.body["personas"]); string(got) != `[{"description":"Reviews code","file":"code_reviewer.md","icon":".agents/icons/code_reviewer.png","name":"code_reviewer","shared":true}]` {
		t.Fatalf("%s", got)
	}
	if !strings.Contains(string(l.raw), `"skills":[{"name":"deploy","description":"Run the script.","file":"deploy.md"}]`) {
		t.Fatalf("%s", l.raw)
	}
	e.do("DELETE", "/api/extensions/persona?name=code_reviewer", nil)
	if e.do("GET", "/api/extensions/persona?name=code_reviewer", nil).status != 404 {
		t.Fatal("not deleted")
	}
}

func TestModelSTL(t *testing.T) {
	e := newEnv(t, nil)
	// One binary triangle.
	var b bytes.Buffer
	b.Write(make([]byte, 80))
	binary.Write(&b, binary.LittleEndian, uint32(1))
	b.Write(make([]byte, 12))
	for _, f := range []float32{0, 0, 0, 1, 0, 0, 0, 1, 0} {
		binary.Write(&b, binary.LittleEndian, math.Float32bits(f))
	}
	b.Write([]byte{0, 0})
	os.WriteFile(filepath.Join(e.kb, "part.stl"), b.Bytes(), 0o644)
	r := e.do("GET", "/api/kb/model?path=part.stl", nil)
	if r.status != 200 || r.header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	n := binary.LittleEndian.Uint32(r.raw)
	head := strings.TrimRight(string(r.raw[4:4+n]), " ")
	if head != `{"format":"stl","triangles":1,"meshes":[{"positions":{"o":0,"n":9}}]}` || n%4 != 0 || len(r.raw) != int(4+n+36) {
		t.Fatalf("%q", head)
	}
	if _, err := os.Stat(filepath.Join(e.ix, "model-cache", "part.stl.mdl")); err != nil {
		t.Fatal("not cached")
	}
	if e.do("GET", "/api/kb/model?path=../x.stl", nil).status != 400 || e.do("GET", "/api/kb/model?path=a.txt", nil).status != 415 {
		t.Fatal("refusals")
	}
	// No prepared occt-import-js module and no Node: a STEP file is a 501.
	t.Setenv("STUDIO_OCCT_WASM", filepath.Join(e.home, "none.wasm"))
	os.WriteFile(filepath.Join(e.kb, "part.step"), []byte("ISO-10303-21;"), 0o644)
	if s := e.do("GET", "/api/kb/model?path=part.step", nil); s.status != 501 {
		t.Fatalf("STEP without the module or Node: %d", s.status)
	}
}

// A model in another library on this host is read from that library's
// folder when the request names it (the viewer sends library= as the other
// viewers do), and cached apart from the main folder's; a missing file is
// a 404.
func TestModelInAnotherLibrary(t *testing.T) {
	e := newEnv(t, nil)
	kb.Register(e.mux, e.a)
	t.Cleanup(func() { kb.Default = nil })
	src := t.TempDir()
	var b bytes.Buffer
	b.Write(make([]byte, 80))
	binary.Write(&b, binary.LittleEndian, uint32(1))
	b.Write(make([]byte, 12))
	for _, f := range []float32{0, 0, 0, 1, 0, 0, 0, 1, 0} {
		binary.Write(&b, binary.LittleEndian, math.Float32bits(f))
	}
	b.Write([]byte{0, 0})
	os.MkdirAll(filepath.Join(src, "models"), 0o755)
	os.WriteFile(filepath.Join(src, "models", "part.stl"), b.Bytes(), 0o644)
	defs, _ := json.Marshal([]map[string]any{{"id": "parts", "indexRoot": filepath.Join(e.ix, "parts-index"), "sourceRoot": src}})
	os.WriteFile(filepath.Join(e.ix, "libraries.json"), defs, 0o644)

	if r := e.do("GET", "/api/kb/model?library=parts&path=models/part.stl", nil); r.status != 200 {
		t.Fatalf("model in another library: %d %s", r.status, r.raw)
	}
	if _, err := os.Stat(filepath.Join(e.ix, "model-cache", "libraries", "parts", "models", "part.stl.mdl")); err != nil {
		t.Fatal("not cached under the library")
	}
	// The same path in the main folder is another file, here a missing one.
	if r := e.do("GET", "/api/kb/model?path=models/part.stl", nil); r.status != 404 {
		t.Fatalf("missing in the main folder: %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/kb/model?library=parts&path=models/none.stl", nil); r.status != 404 {
		t.Fatalf("missing in the library: %d %s", r.status, r.raw)
	}
	if r := e.do("GET", "/api/kb/model?library=parts&path=../x.stl", nil); r.status != 400 {
		t.Fatalf("escape: %d", r.status)
	}
	if r := e.do("GET", "/api/kb/model?library=nope&path=models/part.stl", nil); r.status != 404 {
		t.Fatalf("unknown library: %d", r.status)
	}
	// library=kb is the main folder.
	os.WriteFile(filepath.Join(e.kb, "main.stl"), b.Bytes(), 0o644)
	if r := e.do("GET", "/api/kb/model?library=kb&path=main.stl", nil); r.status != 200 {
		t.Fatalf("library=kb: %d %s", r.status, r.raw)
	}
}

// A STEP file is meshed here, without Node, into the cache file Node
// writes (internal/stepmesh/testdata/golden.json).
func TestModelSTEPWithoutNode(t *testing.T) {
	wasm := os.Getenv("STUDIO_OCCT_WASM")
	src, err := os.ReadFile("../../../server/node_modules/occt-import-js/test/testfiles/rounded-cube/rounded-cube.step")
	if wasm == "" || err != nil {
		t.Skip("STUDIO_OCCT_WASM unset or occt-import-js's test files not installed")
	}
	e := newEnv(t, map[string]string{"STUDIO_OCCT_WASM": wasm})
	os.WriteFile(filepath.Join(e.kb, "part.step"), src, 0o644)
	r := e.do("GET", "/api/kb/model?path=part.step", nil)
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	sum := sha256.Sum256(r.raw)
	if hex.EncodeToString(sum[:]) != "70049c6e6cb7b6f401328035ca02b07289d64b9a77ceae2f3198f8b440408310" {
		t.Fatalf("not Node's cache file: %d bytes", len(r.raw))
	}
	os.WriteFile(filepath.Join(e.kb, "junk.stp"), []byte("not a step file"), 0o644)
	if j := e.do("GET", "/api/kb/model?path=junk.stp", nil); j.status != 422 || j.body["error"] != "could not convert STEP geometry" {
		t.Fatalf("%d %s", j.status, j.raw)
	}
}

type fakeStep struct {
	delay time.Duration
	err   error
}

func (f fakeStep) ReadStep(ctx context.Context, _ []byte) ([]stepmesh.Mesh, error) {
	time.Sleep(f.delay)
	if f.err != nil {
		return nil, f.err
	}
	return []stepmesh.Mesh{{Positions: []float32{0, 0, 0}, Indices: []uint32{0, 0, 0}}}, nil
}

// A STEP file the mesher gives up on is Node's failed-mesh answer with its
// own message, logged with its size and time, and not cached: the next
// request meshes it again.
func TestModelSTEPTooComplex(t *testing.T) {
	e := newEnv(t, nil)
	var logs bytes.Buffer
	c := &Core{a: e.a, log: slog.New(slog.NewTextHandler(&logs, nil)), icons: newIconCache()}
	c.step.fake = fakeStep{delay: 20 * time.Millisecond, err: stepmesh.ErrTooComplex}
	mux := http.NewServeMux()
	mux.Handle("GET /api/kb/model", apierr.Handler(c.log, c.model))
	os.WriteFile(filepath.Join(e.kb, "big.step"), []byte("ISO-10303-21;"), 0o644)
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/kb/model?path=big.step", nil))
		return w
	}
	w := get()
	if w.Code != 422 || w.Body.String() != `{"error":"this STEP file is too complex to preview"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !regexp.MustCompile(`STEP meshing stopped at the bound.*path=big.step bytes=13 ms=\d+`).MatchString(logs.String()) {
		t.Fatal(logs.String())
	}
	if _, err := os.Stat(filepath.Join(e.ix, "model-cache", "big.step.mdl")); err == nil {
		t.Fatal("a failed mesh was cached")
	}
	c.step.fake = fakeStep{}
	if w := get(); w.Code != 200 {
		t.Fatalf("not tried again: %d", w.Code)
	}
}
