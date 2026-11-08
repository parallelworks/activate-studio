package chat

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/conversations"
	"github.com/parallelworks/activate-studio/server-go/internal/llm"
)

// fakeTools is the tool layer without Node: no tools, a fixed prompt.
type fakeTools struct{ base string }

func (f fakeTools) Begin(r *http.Request, in map[string]any) (*Begin, error) {
	b := &Begin{Viewer: &Viewer{ID: "local", Name: "user"}, Meta: Meta{MoreTools: "more_tools"}}
	raw, _ := json.Marshal(in["used"])
	var used []json.RawMessage
	json.Unmarshal(raw, &used)
	for _, u := range used {
		var m struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		json.Unmarshal(u, &m)
		if m.Role != "system" {
			c := m.Content
			b.History = append(b.History, msg(m.Role, c))
		}
	}
	b.System = "SYSTEM"
	return b, nil
}
func (fakeTools) System(*http.Request, bool, bool) (string, error) { return "SYSTEM", nil }
func (fakeTools) Specs(*http.Request, []string, bool, bool, bool, []string) (*Specs, error) {
	return &Specs{}, nil
}
func (fakeTools) Names(_ *http.Request, _ []string, _ bool, names []string) (*Names, error) {
	n := &Names{Allowed: map[string]bool{}, Labels: map[string]string{}, AsksFirst: map[string]bool{}}
	for _, x := range names {
		n.Allowed[x] = true
	}
	return n, nil
}
func (fakeTools) Execute(*http.Request, string, string, ToolCtx) (*Outcome, error) {
	return &Outcome{Result: "r", Summary: "s"}, nil
}
func (fakeTools) Prepare(*http.Request, string, string, string) (*Prepared, error) {
	return &Prepared{}, nil
}
func (fakeTools) Hold(*http.Request, map[string]any) (json.RawMessage, error) { return nil, nil }
func (fakeTools) Answer(*http.Request, map[string]any, *string) (*Answered, error) {
	return &Answered{Why: "no"}, nil
}
func (fakeTools) Held(*http.Request, string) ([]conversations.Holder, error) { return nil, nil }
func (f fakeTools) Access(*http.Request, int) (*Access, error) {
	return &Access{GatewayBase: f.base, DeploymentKey: os.Getenv("PW_API_KEY")}, nil
}

func newTestModule(t *testing.T, env map[string]string) (*Module, *httptest.Server, *fakeGateway, *int32) {
	dir := t.TempDir()
	gw := &fakeGateway{}
	var listings int32
	gs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			atomic.AddInt32(&listings, 1)
		}
		gw.ServeHTTP(w, r)
	}))
	t.Cleanup(gs.Close)
	base := map[string]string{"INDEX_BASE": dir, "KB_ROOT": filepath.Join(dir, "kb"), "STUDIO_NODE": "off", "PW_API_KEY": "k", "AI_ENABLED": "1", "HOME": dir}
	for k, v := range env {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
	cfg, _ := config.Load(func(k string) string { return base[k] })
	a := app.New(cfg, slog.New(slog.DiscardHandler), nil, nil)
	mux := http.NewServeMux()
	Register(mux, a)
	m := Default
	m.Tools = fakeTools{gs.URL + "/v1"}
	m.setAccess(&Access{GatewayBase: gs.URL + "/v1", DeploymentKey: base["PW_API_KEY"]})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return m, srv, gw, &listings
}

func get(t *testing.T, u string) map[string]any {
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return out
}

func send(t *testing.T, method, u, body string) (int, map[string]any, http.Header) {
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	json.Unmarshal(b, &out)
	return res.StatusCode, out, res.Header
}

func TestAIOffAnswersWithoutAModel(t *testing.T) {
	_, srv, gw, _ := newTestModule(t, map[string]string{"AI_ENABLED": "0"})
	sse := post(t, srv.URL, map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	if sse != "event: content\ndata: {\"text\":\"AI features are off in this Studio.\"}\n\nevent: done\ndata: {\"finishReason\":\"stop\",\"model\":null}\n\n" {
		t.Errorf("%q", sse)
	}
	if len(gw.requests) != 0 {
		t.Error("a model was called")
	}
	if out := get(t, srv.URL+"/api/chat/models"); out["error"] != AIOff {
		t.Errorf("models: %v", out)
	}
	if out := get(t, srv.URL+"/api/ai/health"); out["status"] != "off" {
		t.Errorf("health: %v", out)
	}
}

func TestNoCredentialIsASentence(t *testing.T) {
	m, srv, _, _ := newTestModule(t, map[string]string{"PW_API_KEY": ""})
	m.setAccess(&Access{GatewayBase: "http://127.0.0.1:9/v1"})
	sse := post(t, srv.URL, map[string]any{"model": "m", "messages": []any{}})
	if !strings.Contains(sse, "event: error\ndata: {\"message\":\"No gateway credential (set PW_API_KEY") {
		t.Errorf("%q", sse)
	}
	if out := get(t, srv.URL+"/api/chat/models"); !strings.HasPrefix(out["error"].(string), "no gateway credential") {
		t.Errorf("models: %v", out)
	}
}

func TestStreamPlainAnswerIsRecorded(t *testing.T) {
	m, srv, gw, _ := newTestModule(t, nil)
	gw.load([]reply{text("Hi ", "there.")})
	sse := post(t, srv.URL, map[string]any{"model": "test-model", "conversationId": "c1", "userMessageId": "u1",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}}})
	want := "event: content\ndata: {\"text\":\"Hi \"}\n\nevent: content\ndata: {\"text\":\"there.\"}\n\nevent: done\ndata: {\"content\":\"Hi there.\",\"finishReason\":\"stop\",\"model\":\"test-model\",\"messageId\":\"srv-N\"}\n\n"
	if normal(sse) != want {
		t.Errorf("%q", sse)
	}
	c := m.Conv.Get("c1")
	if c == nil || len(c.Messages) != 2 || c.Messages[1].Text() != "Hi there." || deref(c.Messages[1].ParentID) != "u1" {
		t.Fatalf("stored %+v", c)
	}
	var body map[string]any
	json.Unmarshal(gw.requests[0], &body)
	msgs := body["messages"].([]any)
	if !strings.HasPrefix(msgs[0].(map[string]any)["content"].(string), "SYSTEM\n\nToday's date is ") || body["tool_choice"] != "auto" {
		t.Errorf("request %s", gw.requests[0])
	}
}

func TestModelKeyWithSignInOff(t *testing.T) {
	_, srv, _, _ := newTestModule(t, nil)
	if out := get(t, srv.URL+"/api/me/model-key"); out["single"] != true || out["mode"] != "none" || out["sharedHistory"] != true {
		t.Fatalf("status %v", out)
	}
	if code, out, _ := send(t, "POST", srv.URL+"/api/me/model-key", `{}`); code != 400 || out["error"] != "key required" {
		t.Errorf("empty: %d %v", code, out)
	}
	code, out, h := send(t, "POST", srv.URL+"/api/me/model-key", `{"key":"sk-abcdef1234","baseUrl":"https://models.example.org/v1/"}`)
	if code != 200 || out["mode"] != "stored" || out["last4"] != "1234" || out["baseUrl"] != "https://models.example.org/v1" {
		t.Errorf("set: %d %v", code, out)
	}
	if len(h.Values("Set-Cookie")) != 0 {
		t.Error("a cookie was set with sign-in off")
	}
	if code, out, _ := send(t, "DELETE", srv.URL+"/api/me/model-key", ``); code != 200 || out["mode"] != "none" {
		t.Errorf("clear: %d %v", code, out)
	}
	if code, out, _ := send(t, "POST", srv.URL+"/api/me/model-key/share", `{}`); code != 403 || out["error"] != "not verified" {
		t.Errorf("share: %d %v", code, out)
	}
}

func TestModelListingIsCached(t *testing.T) {
	_, srv, _, listings := newTestModule(t, nil)
	first := get(t, srv.URL+"/api/chat/models")
	second := get(t, srv.URL+"/api/chat/models")
	if n := atomic.LoadInt32(listings); n != 1 {
		t.Errorf("catalog asked %d times", n)
	}
	if len(first["models"].([]any)) != 1 || len(second["models"].([]any)) != 1 {
		t.Errorf("%v %v", first, second)
	}
	m := first["models"].([]any)[0].(map[string]any)
	if m["provider"] != "Served here (local)" || m["name"] != "test-model" {
		t.Errorf("a model without provider fields is not labelled: %v", m)
	}
	get(t, srv.URL+"/api/chat/models?refresh=1")
	if n := atomic.LoadInt32(listings); n != 2 {
		t.Errorf("refresh=1 did not recompute: %d", n)
	}
}

// Citation repair against citations.ts, piece by piece.
func TestCitationsMatchNode(t *testing.T) {
	node := os.Getenv("STUDIO_GOLDEN_NODE")
	cases := []map[string]any{
		{"pieces": []string{"See [relative/path/notes/a.md](#open=file:relative/path/notes/a.md)."}},
		{"pieces": []string{"[notes/b", ".md] and [x]"}, "known": map[string]string{"notes/b.md": ""}},
		{"pieces": []string{"[notes/c.md](notes/c.md)"}, "known": map[string]string{"notes/c.md": "shelf"}},
		{"pieces": []string{"[report%20one.pdf](#open=file:report%20one.pdf)"}, "library": "lib1"},
		{"pieces": []string{"![img](x.png) `[notes/b.md]` \\[notes/b.md]"}, "known": map[string]string{"notes/b.md": ""}},
		{"pieces": []string{"[open", " no close\nline [a](b) [c](#open=file:c d.md) [d](https://x.org)"}},
		{"pieces": []string{"[", "notes/(1).md", "]"}, "known": map[string]string{"notes/(1).md": ""}},
		{"pieces": []string{"emoji \U0001F600[notes/b.md] [notes/b.md][1] [notes/b.md]: x"}, "known": map[string]string{"notes/b.md": ""}},
		{"pieces": []string{"[" + strings.Repeat("a", 700) + "] tail"}},
		{"pieces": []string{"[relative/path/] [./notes/b.md]"}, "known": map[string]string{"notes/b.md": "kb"}, "library": "all"},
	}
	var got [][]string
	for _, c := range cases {
		known := map[string]string{}
		if k, ok := c["known"].(map[string]string); ok {
			known = k
		}
		lib, _ := c["library"].(string)
		s := NewCitationStream(&CitationContext{Known: known, Library: lib})
		var out []string
		for _, p := range c["pieces"].([]string) {
			out = append(out, s.Push(p))
		}
		got = append(got, append(out, s.Flush()))
	}
	if !strings.Contains(strings.Join(got[0], ""), "[notes/a.md](#open=file:notes/a.md)") {
		t.Errorf("placeholder not repaired: %v", got[0])
	}
	if node == "" {
		t.Skip("STUDIO_GOLDEN_NODE unset: compared with Node only when set")
	}
	in, _ := json.Marshal(cases)
	cmd := exec.Command(node, "../../scripts/golden/chat.mjs", "../../..", "citations")
	cmd.Stdin = strings.NewReader(string(in))
	b, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var want [][]string
	json.Unmarshal(b, &want)
	for i := range cases {
		if strings.Join(want[i], "\x00") != strings.Join(got[i], "\x00") {
			t.Errorf("case %d\nnode %q\ngo   %q", i, want[i], got[i])
		}
	}
}

func msg(role, content string) llm.Message { return llm.Message{Role: role, Content: &content} }

func TestAttachmentsRoundTrip(t *testing.T) {
	_, srv, _, _ := newTestModule(t, nil)
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "../my notes?.txt")
	fw.Write([]byte("hello"))
	mw.Close()
	res, err := http.Post(srv.URL+"/api/chat/attachments", mw.FormDataContentType(), strings.NewReader(buf.String()))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	json.NewDecoder(res.Body).Decode(&ref)
	res.Body.Close()
	if ref["filename"] != "my notes_.txt" || ref["size"] != float64(5) || ref["contentType"] == nil {
		t.Fatalf("upload: %d %v", res.StatusCode, ref)
	}
	id := ref["id"].(string)
	if list := get(t, srv.URL+"/api/chat/attachments"); list["total"] != float64(1) {
		t.Errorf("list: %v", list)
	}
	res, _ = http.Get(srv.URL + "/api/chat/attachments/" + id + "/download")
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(b) != "hello" || !strings.Contains(res.Header.Get("Content-Disposition"), `filename="my notes_.txt"`) {
		t.Errorf("download: %q %v", b, res.Header)
	}
	if code, out, _ := send(t, "DELETE", srv.URL+"/api/chat/attachments/"+id, ""); code != 200 || out["deleted"] != "uploads/chat/my notes_.txt" {
		t.Errorf("delete: %d %v", code, out)
	}
	bad := base64.RawURLEncoding.EncodeToString([]byte("../etc/passwd"))
	if code, out, _ := send(t, "DELETE", srv.URL+"/api/chat/attachments/"+bad, ""); code != 400 || out["error"] != "invalid attachment id" {
		t.Errorf("bad id: %d %v", code, out)
	}
}
